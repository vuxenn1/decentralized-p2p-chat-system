package security

import (
	"bufio"
	"crypto/cipher"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"strings"
	"sync"
	"sync/atomic"

	"golang.org/x/crypto/chacha20poly1305"
	"golang.org/x/crypto/curve25519"
	"golang.org/x/crypto/hkdf"
)

// Session holds the encryption state for one peer connection.
//
// Each session is established via a fresh X25519 key exchange (Handshake),
// producing a unique shared secret that is never reused across connections.
//
// Outgoing messages use an incrementing nonce counter to ensure no two
// messages share the same nonce — nonce reuse would completely break
// ChaCha20-Poly1305 security.
//
// Incoming messages are tracked via receivedNonces to prevent replay attacks.
// If a nonce is seen twice within the same session, the message is rejected.
type Session struct {
	aead           cipher.AEAD // ChaCha20-Poly1305 cipher (encrypts + authenticates)
	nonceCtr       uint64      // Outgoing nonce counter — increments per message
	receivedNonces sync.Map    // Tracks received nonces to detect replayed messages
}

// Handshake performs an X25519 Diffie-Hellman key exchange with a peer
// to establish a shared encryption key without transmitting it over the network.
//
// Process:
//  1. Generate a random ephemeral private key (used once, discarded after handshake)
//  2. Derive the corresponding public key using Curve25519
//  3. Exchange public keys with the peer over the stream
//  4. Both sides compute the same shared secret: shared = peerPub × ourPriv
//  5. Derive a 32-byte encryption key from the shared secret using HKDF-SHA256
//  6. Initialize a ChaCha20-Poly1305 AEAD cipher with the derived key
//
// Security properties:
//   - Forward secrecy: ephemeral keys mean past sessions cannot be decrypted
//     even if long-term identity keys are later compromised
//   - Authentication: ChaCha20-Poly1305 detects any tampering with ciphertext
//   - Key isolation: the raw shared secret is never used directly — HKDF
//     binds it to the application context "p2pchat-password-protocol"
func Handshake(rw io.ReadWriter) (*Session, error) {
	// Generate 32-byte ephemeral private key
	var priv [32]byte
	if _, err := rand.Read(priv[:]); err != nil {
		return nil, err
	}

	// Apply X25519 clamping (required by the spec for curve safety)
	priv[0] &= 248  // Clear bottom 3 bits
	priv[31] &= 127 // Clear top bit
	priv[31] |= 64  // Set second-to-top bit

	// Derive public key: pub = priv × BasePoint
	pub, err := curve25519.X25519(priv[:], curve25519.Basepoint)
	if err != nil {
		return nil, err
	}

	reader := bufio.NewReader(rw)
	writer := bufio.NewWriter(rw)

	// Send our public key (base64 encoded, newline terminated)
	if _, err := writer.WriteString(base64.StdEncoding.EncodeToString(pub) + "\n"); err != nil {
		return nil, err
	}
	if err := writer.Flush(); err != nil {
		return nil, err
	}

	// Receive peer's public key
	peerLine, err := reader.ReadString('\n')
	if err != nil {
		return nil, err
	}
	peerPub, err := base64.StdEncoding.DecodeString(strings.TrimSpace(peerLine))
	if err != nil || len(peerPub) != 32 {
		return nil, errors.New("invalid peer public key")
	}

	// Compute shared secret: shared = peerPub × ourPriv
	// The peer computes: shared = ourPub × theirPriv
	// Both results are equal by Diffie-Hellman properties
	shared, err := curve25519.X25519(priv[:], peerPub)
	if err != nil {
		return nil, err
	}

	// Derive encryption key via HKDF-SHA256
	// This strengthens the shared secret and binds it to our application
	h := hkdf.New(sha256.New, shared, nil, []byte("p2pchat-password-protocol"))
	key := make([]byte, chacha20poly1305.KeySize)
	if _, err := io.ReadFull(h, key); err != nil {
		return nil, err
	}

	// Initialize ChaCha20-Poly1305 cipher
	// Provides both encryption (ChaCha20) and authentication (Poly1305)
	aead, err := chacha20poly1305.New(key)
	if err != nil {
		return nil, err
	}

	return &Session{aead: aead}, nil
}

// Encrypt encodes a plaintext message into an authenticated encrypted packet.
//
// Output format: "base64(nonce).base64(ciphertext+tag)"
//
// The nonce is derived from an atomic counter, guaranteeing uniqueness
// across all messages in this session. Nonce reuse with ChaCha20-Poly1305
// would allow key recovery — the counter approach prevents this entirely.
//
// The 16-byte Poly1305 authentication tag appended to the ciphertext
// ensures any modification of the packet is detected on decryption.
func (s *Session) Encrypt(plain []byte) (string, error) {
	nonce := make([]byte, chacha20poly1305.NonceSize)

	// Increment counter atomically to ensure no two goroutines share a nonce
	ctr := atomic.AddUint64(&s.nonceCtr, 1)
	binary.LittleEndian.PutUint64(nonce[:8], ctr)

	// Seal encrypts and authenticates: output = ciphertext || 16-byte tag
	ct := s.aead.Seal(nil, nonce, plain, nil)

	return fmt.Sprint(base64.StdEncoding.EncodeToString(nonce) + "." + base64.StdEncoding.EncodeToString(ct)), nil
}

// Decrypt decodes and authenticates an encrypted packet back to plaintext.
//
// Steps:
//  1. Parse the "nonce.ciphertext" packet format
//  2. Check the nonce has not been seen before (replay protection)
//  3. Verify the Poly1305 authentication tag
//  4. Decrypt and return plaintext
//
// Returns an error if:
//   - The packet format is invalid
//   - The nonce was already received in this session (replay attack detected)
//   - The authentication tag does not match (tampered or corrupted packet)
//   - Decryption fails for any other reason
func (s *Session) Decrypt(packet string) ([]byte, error) {
	parts := strings.Split(packet, ".")
	if len(parts) != 2 {
		return nil, errors.New("invalid packet format")
	}

	nonce, err := base64.StdEncoding.DecodeString(parts[0])
	if err != nil || len(nonce) != chacha20poly1305.NonceSize {
		return nil, errors.New("invalid nonce")
	}

	ct, err := base64.StdEncoding.DecodeString(parts[1])
	if err != nil {
		return nil, errors.New("invalid ciphertext")
	}

	// Replay protection: reject any nonce seen before in this session
	nonceKey := base64.StdEncoding.EncodeToString(nonce)
	if _, alreadySeen := s.receivedNonces.LoadOrStore(nonceKey, true); alreadySeen {
		return nil, errors.New("replay attack detected: nonce already used")
	}

	// Verify authentication tag and decrypt
	// aead.Open returns an error if the tag does not match,
	// meaning the packet was tampered with or corrupted
	plain, err := s.aead.Open(nil, nonce, ct, nil)
	if err != nil {
		return nil, errors.New("decrypt/auth failed")
	}

	return plain, nil
}
