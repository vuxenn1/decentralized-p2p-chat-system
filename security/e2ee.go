package security

import (
	"bufio"
	"bytes"
	"crypto/cipher"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"sort"
	"strings"
	"sync"
	"sync/atomic"

	libp2pcrypto "github.com/libp2p/go-libp2p/core/crypto"
	"golang.org/x/crypto/chacha20poly1305"
	"golang.org/x/crypto/curve25519"
	"golang.org/x/crypto/hkdf"
)

const (
	nonceSize = chacha20poly1305.NonceSize // 12 bytes
	keySize   = chacha20poly1305.KeySize   // 32 bytes

	directionLowToHigh byte = 1
	directionHighToLow byte = 2

	replayWindowBits = 64
)

var (
	// ErrReplay is returned when a valid packet is received more than once
	// or when its counter is not strictly increasing.
	ErrReplay = errors.New("replay attack detected: nonce already used")

	// ErrWrongDirection is returned when a packet was produced for the other
	// direction of the session. This also rejects reflected packets.
	ErrWrongDirection = errors.New("packet direction does not match session")
)

// Session holds the encryption state for one peer connection.
//
// The handshake derives two independent keys: one for the low-key side to
// send to the high-key side and one for the reverse direction. The direction
// is also encoded in the nonce, so a packet cannot be reflected back to its
// sender even if a key were reused by mistake.
//
// Outgoing messages use an increasing counter per direction. Incoming
// messages are accepted only after AEAD verification and only if their
// counter is inside a 64-packet replay window. This accepts limited
// reordering while rejecting duplicates and old packets.
type Session struct {
	sendAEAD cipher.AEAD
	recvAEAD cipher.AEAD

	sendDir byte
	recvDir byte

	sendCtr uint64

	recvMu     sync.Mutex
	recvMax    uint64
	recvWindow uint64
}

// Handshake performs an authenticated X25519 key exchange and derives
// direction-separated ChaCha20-Poly1305 keys.
//
// Each side signs a transcript that contains both ephemeral X25519 keys and
// both long-term identity public keys. The remote signature is verified with
// the public key obtained from the libp2p connection. This binds the
// application session to the peer identity.
//
// The returned reader must be used by the caller for the rest of the stream.
// Creating a second bufio.Reader after the handshake can silently swallow
// bytes that arrived together with the peer's public key.
func Handshake(rw io.ReadWriter, localIdentity libp2pcrypto.PrivKey, remoteIdentity libp2pcrypto.PubKey) (*Session, *bufio.Reader, error) {
	if localIdentity == nil || remoteIdentity == nil {
		return nil, nil, errors.New("identity key is missing")
	}

	var priv [32]byte
	if _, err := rand.Read(priv[:]); err != nil {
		return nil, nil, err
	}

	// curve25519.X25519 clamps the scalar internally. Manual clamping here
	// would be redundant.
	pub, err := curve25519.X25519(priv[:], curve25519.Basepoint)
	if err != nil {
		return nil, nil, err
	}

	reader := bufio.NewReader(rw)
	writer := bufio.NewWriter(rw)

	if _, err := writer.WriteString(base64.StdEncoding.EncodeToString(pub) + "\n"); err != nil {
		return nil, nil, err
	}
	if err := writer.Flush(); err != nil {
		return nil, nil, err
	}

	peerLine, err := reader.ReadString('\n')
	if err != nil {
		return nil, nil, err
	}
	peerPub, err := base64.StdEncoding.DecodeString(strings.TrimSpace(peerLine))
	if err != nil || len(peerPub) != 32 {
		return nil, nil, errors.New("invalid peer public key")
	}

	shared, err := curve25519.X25519(priv[:], peerPub)
	if err != nil {
		return nil, nil, err
	}

	transcript, err := handshakeTranscript(pub, peerPub, localIdentity.GetPublic(), remoteIdentity)
	if err != nil {
		return nil, nil, err
	}

	signature, err := localIdentity.Sign(transcript)
	if err != nil {
		return nil, nil, fmt.Errorf("sign handshake transcript: %w", err)
	}
	if _, err := writer.WriteString(base64.StdEncoding.EncodeToString(signature) + "\n"); err != nil {
		return nil, nil, err
	}
	if err := writer.Flush(); err != nil {
		return nil, nil, err
	}

	peerSignatureLine, err := reader.ReadString('\n')
	if err != nil {
		return nil, nil, err
	}
	peerSignature, err := base64.StdEncoding.DecodeString(strings.TrimSpace(peerSignatureLine))
	if err != nil || len(peerSignature) == 0 {
		return nil, nil, errors.New("invalid peer signature")
	}
	verified, err := remoteIdentity.Verify(transcript, peerSignature)
	if err != nil {
		return nil, nil, fmt.Errorf("verify peer signature: %w", err)
	}
	if !verified {
		return nil, nil, errors.New("peer identity verification failed")
	}

	lowToHighKey, err := deriveKey(shared, transcript, "p2pchat-v2-low-to-high")
	if err != nil {
		return nil, nil, err
	}
	highToLowKey, err := deriveKey(shared, transcript, "p2pchat-v2-high-to-low")
	if err != nil {
		return nil, nil, err
	}

	lowToHigh, err := chacha20poly1305.New(lowToHighKey)
	if err != nil {
		return nil, nil, err
	}
	highToLow, err := chacha20poly1305.New(highToLowKey)
	if err != nil {
		return nil, nil, err
	}

	session := &Session{}
	switch bytes.Compare(pub, peerPub) {
	case -1:
		// This side has the lower ephemeral public key.
		session.sendAEAD = lowToHigh
		session.recvAEAD = highToLow
		session.sendDir = directionLowToHigh
		session.recvDir = directionHighToLow
	case 1:
		// This side has the higher ephemeral public key.
		session.sendAEAD = highToLow
		session.recvAEAD = lowToHigh
		session.sendDir = directionHighToLow
		session.recvDir = directionLowToHigh
	default:
		return nil, nil, errors.New("identical ephemeral public keys")
	}

	return session, reader, nil
}

func handshakeTranscript(localEphemeral, remoteEphemeral []byte, localIdentity, remoteIdentity libp2pcrypto.PubKey) ([]byte, error) {
	localID, err := localIdentity.Raw()
	if err != nil {
		return nil, fmt.Errorf("marshal local identity: %w", err)
	}
	remoteID, err := remoteIdentity.Raw()
	if err != nil {
		return nil, fmt.Errorf("marshal remote identity: %w", err)
	}

	ephemerals := [][]byte{localEphemeral, remoteEphemeral}
	sort.Slice(ephemerals, func(i, j int) bool {
		return bytes.Compare(ephemerals[i], ephemerals[j]) < 0
	})

	identities := [][]byte{localID, remoteID}
	sort.Slice(identities, func(i, j int) bool {
		return bytes.Compare(identities[i], identities[j]) < 0
	})

	return bytes.Join([][]byte{
		[]byte("p2pchat-handshake-v2"),
		ephemerals[0],
		ephemerals[1],
		identities[0],
		identities[1],
	}, []byte("|")), nil
}

func deriveKey(shared, transcript []byte, info string) ([]byte, error) {
	h := hkdf.New(sha256.New, shared, transcript, []byte(info))
	key := make([]byte, keySize)
	if _, err := io.ReadFull(h, key); err != nil {
		return nil, fmt.Errorf("hkdf: %w", err)
	}
	return key, nil
}

// Encrypt encodes a plaintext message into an authenticated encrypted packet.
//
// Output format: "base64(nonce).base64(ciphertext+tag)".
// The first nonce byte is the direction marker; the remaining bytes carry an
// increasing 64-bit counter starting at 1.
func (s *Session) Encrypt(plain []byte) (string, error) {
	if s == nil || s.sendAEAD == nil {
		return "", errors.New("session is not initialized")
	}

	nonce := make([]byte, nonceSize)
	nonce[0] = s.sendDir
	ctr := atomic.AddUint64(&s.sendCtr, 1)
	binary.LittleEndian.PutUint64(nonce[4:], ctr)

	ct := s.sendAEAD.Seal(nil, nonce, plain, nil)
	return base64.StdEncoding.EncodeToString(nonce) + "." +
		base64.StdEncoding.EncodeToString(ct), nil
}

// Decrypt verifies and decrypts a packet.
//
// The AEAD tag is verified before the replay check. A forged or corrupted
// packet therefore cannot mark a legitimate packet's counter as consumed.
func (s *Session) Decrypt(packet string) ([]byte, error) {
	if s == nil || s.recvAEAD == nil {
		return nil, errors.New("session is not initialized")
	}

	parts := strings.Split(packet, ".")
	if len(parts) != 2 {
		return nil, errors.New("invalid packet format")
	}

	nonce, err := base64.StdEncoding.DecodeString(parts[0])
	if err != nil || len(nonce) != nonceSize {
		return nil, errors.New("invalid nonce")
	}
	if nonce[0] != s.recvDir {
		return nil, ErrWrongDirection
	}

	ct, err := base64.StdEncoding.DecodeString(parts[1])
	if err != nil {
		return nil, errors.New("invalid ciphertext")
	}

	plain, err := s.recvAEAD.Open(nil, nonce, ct, nil)
	if err != nil {
		return nil, errors.New("decrypt/auth failed")
	}

	ctr := binary.LittleEndian.Uint64(nonce[4:])
	if !s.acceptCounter(ctr) {
		return nil, ErrReplay
	}

	return plain, nil
}

// acceptCounter applies a 64-packet sliding replay window. The AEAD tag has
// already been verified when this is called.
func (s *Session) acceptCounter(ctr uint64) bool {
	s.recvMu.Lock()
	defer s.recvMu.Unlock()

	if ctr > s.recvMax {
		shift := ctr - s.recvMax
		if shift >= replayWindowBits {
			s.recvWindow = 0
		} else {
			s.recvWindow <<= shift
		}
		s.recvWindow |= 1
		s.recvMax = ctr
		return true
	}

	diff := s.recvMax - ctr
	if diff >= replayWindowBits {
		return false
	}
	mask := uint64(1) << diff
	if s.recvWindow&mask != 0 {
		return false
	}
	s.recvWindow |= mask
	return true
}
