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
	"sync/atomic"

	"golang.org/x/crypto/chacha20poly1305"
	"golang.org/x/crypto/curve25519"
	"golang.org/x/crypto/hkdf"
)

// Session holds the encryption key and nonce counter for one peer connection.
// Each message increments the nonce to ensure unique encryption per message.
//
// SECURITY LIMITATION: This implementation does not verify received nonces,
// making it vulnerable to replay attacks. A production system should track
// received nonces to detect and reject replayed messages.
type Session struct {
	aead     cipher.AEAD // ChaCha20-Poly1305 cipher (encrypts + authenticates)
	nonceCtr uint64      // Counter to generate unique nonces (prevents reuse)
}

// Handshake performs a key exchange with a peer to establish encrypted communication.
//
// Process:
// 1. Generate random private key (ephemeral, used once)
// 2. Calculate public key from private key
// 3. Exchange public keys with peer
// 4. Both peers calculate the same shared secret
// 5. Derive encryption key from shared secret
//
// Security: Uses X25519 (Diffie-Hellman on Curve25519) for key exchange.
// Result: Both peers have the same encryption key without sending it over the network.
func Handshake(rw io.ReadWriter) (*Session, error) {
	// ===== STEP 1: Generate private key =====
	// Create 32 random bytes as our secret key
	var priv [32]byte
	if _, err := rand.Read(priv[:]); err != nil {
		return nil, err
	}

	// Format the private key for X25519 (required bit patterns)
	priv[0] &= 248  // Clear bottom 3 bits
	priv[31] &= 127 // Clear top bit
	priv[31] |= 64  // Set second-to-top bit
	// Why? X25519 spec requires these for mathematical security

	// ===== STEP 2: Calculate public key =====
	// Multiply private key by the curve's base point
	// Think: pub = priv × BasePoint (elliptic curve math)
	pub, err := curve25519.X25519(priv[:], curve25519.Basepoint)
	if err != nil {
		return nil, err
	}

	// ===== STEP 3: Exchange public keys =====
	reader := bufio.NewReader(rw)
	writer := bufio.NewWriter(rw)

	// Send our public key to peer (base64 encoded for text transmission)
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

	// ===== STEP 4: Calculate shared secret =====
	// Multiply peer's public key by our private key
	// Think: shared = peerPub × our_priv
	// Peer does: shared = ourPub × their_priv
	// Result is the same! (Diffie-Hellman magic)
	shared, err := curve25519.X25519(priv[:], peerPub)
	if err != nil {
		return nil, err
	}

	// ===== STEP 5: Derive encryption key =====
	// HKDF strengthens the shared secret and binds it to our application
	// Think: encryption_key = SHA256(shared_secret + "p2pchat-password-protocol")
	h := hkdf.New(sha256.New, shared, nil, []byte("p2pchat-password-protocol"))

	key := make([]byte, chacha20poly1305.KeySize) // 32 bytes
	if _, err := io.ReadFull(h, key); err != nil {
		return nil, err
	}

	// ===== STEP 6: Create cipher =====
	// ChaCha20-Poly1305 provides:
	// - Encryption (ChaCha20): scrambles the message
	// - Authentication (Poly1305): detects tampering
	aead, err := chacha20poly1305.New(key)
	if err != nil {
		return nil, err
	}

	return &Session{aead: aead}, nil
}

// Encrypt converts plaintext to encrypted packet.
//
// Format: "base64(nonce).base64(ciphertext)"
// Nonce: unique number for each message (prevents pattern analysis)
// Ciphertext: encrypted message + authentication tag
func (s *Session) Encrypt(plain []byte) (string, error) {
	// Generate unique nonce (12 bytes)
	nonce := make([]byte, chacha20poly1305.NonceSize)

	// Use atomic counter to ensure no two messages share a nonce
	// CRITICAL: Reusing a nonce breaks security completely
	ctr := atomic.AddUint64(&s.nonceCtr, 1)
	binary.LittleEndian.PutUint64(nonce[:8], ctr) // Store counter in first 8 bytes

	// Encrypt + authenticate in one operation
	// Output: [encrypted_data][16-byte authentication tag]
	ct := s.aead.Seal(nil, nonce, plain, nil)

	// Return as base64 string for text transmission
	return fmt.Sprint(base64.StdEncoding.EncodeToString(nonce) + "." + base64.StdEncoding.EncodeToString(ct)), nil
}

// Decrypt converts encrypted packet back to plaintext.
//
// Steps:
// 1. Parse nonce and ciphertext from packet
// 2. Verify authentication tag (detects tampering)
// 3. Decrypt ciphertext to plaintext
//
// Returns error if: packet format wrong, authentication fails, or decryption fails
func (s *Session) Decrypt(packet string) ([]byte, error) {
	// Parse packet format: "nonce.ciphertext"
	parts := strings.Split(packet, ".")
	if len(parts) != 2 {
		return nil, errors.New("invalid packet format")
	}

	// Decode nonce
	nonce, err := base64.StdEncoding.DecodeString(parts[0])
	if err != nil || len(nonce) != chacha20poly1305.NonceSize {
		return nil, errors.New("invalid nonce")
	}

	// Decode ciphertext
	ct, err := base64.StdEncoding.DecodeString(parts[1])
	if err != nil {
		return nil, errors.New("invalid ciphertext")
	}

	// Decrypt and verify authentication tag
	// If authentication fails, message was tampered with
	plain, err := s.aead.Open(nil, nonce, ct, nil)
	if err != nil {
		return nil, errors.New("decrypt/auth failed")
	}

	return plain, nil
}
