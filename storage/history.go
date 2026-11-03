// Package storage provides encrypted local message history for p2p-chat.
//
// Each peer conversation is stored in a separate encrypted file.
// The encryption key is derived from the local private key + peer ID,
// so only the owner of the private key can decrypt their own history.
package storage

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"golang.org/x/crypto/chacha20poly1305"
	"golang.org/x/crypto/hkdf"
)

// StoredMsg is the message format saved to disk.
type StoredMsg struct {
	ID        string    `json:"id"`
	From      string    `json:"from"`     // sender peer ID
	FromName  string    `json:"fromName"` // sender nickname at time of message (for display, not used in encryption)
	Content   string    `json:"content"`  // message content in encrypted form
	Timestamp time.Time `json:"timestamp"`
	IsOwn     bool      `json:"isOwn"`
}

// HistoryStore manages per-peer encrypted message history on disk.
type HistoryStore struct {
	dataDir string
	privKey []byte     // local private key bytes for key derivation
	mu      sync.Mutex // protects all file I/O
}

// NewHistoryStore creates a HistoryStore.
//   - dataDir: directory where history files are written
//   - privKeyBytes: raw bytes of local identity private key
func NewHistoryStore(dataDir string, privKeyBytes []byte) (*HistoryStore, error) {
	dir := filepath.Join(dataDir, "history")
	if err := os.MkdirAll(dir, 0700); err != nil {
		return nil, fmt.Errorf("create history dir: %w", err)
	}
	return &HistoryStore{
		dataDir: dir,
		privKey: privKeyBytes,
	}, nil
}

// deriveKey produces a unique 32-byte AES key for a given peer conversation.
// HKDF(sha256, localPrivKey, peerID, "p2pchat-history-v1") → 32 bytes
func (h *HistoryStore) deriveKey(peerID string) ([]byte, error) {
	r := hkdf.New(sha256.New, h.privKey, []byte(peerID), []byte("p2pchat-history-v1"))
	key := make([]byte, chacha20poly1305.KeySize)
	if _, err := io.ReadFull(r, key); err != nil {
		return nil, fmt.Errorf("hkdf: %w", err)
	}
	return key, nil
}

// filePath returns the path for a peer's history file.
// Uses the full peer ID as filename (safe characters only).
func (h *HistoryStore) filePath(peerID string) string {
	return filepath.Join(h.dataDir, peerID+".enc")
}

// validatePeerID rejects path separators and traversal components. Peer IDs
// are expected to be opaque identifiers, not filesystem paths.
func validatePeerID(peerID string) error {
	if peerID == "" {
		return fmt.Errorf("empty peer id")
	}
	if peerID == "." || peerID == ".." {
		return fmt.Errorf("invalid peer id %q", peerID)
	}
	if strings.ContainsAny(peerID, `/\`) || strings.Contains(peerID, "..") {
		return fmt.Errorf("invalid peer id %q", peerID)
	}
	return nil
}

// SaveMessage appends one message to a peer's encrypted history file.
func (h *HistoryStore) SaveMessage(peerID string, msg StoredMsg) error {
	h.mu.Lock()
	defer h.mu.Unlock()

	if err := validatePeerID(peerID); err != nil {
		return err
	}

	msgs, err := h.readFile(peerID)
	if err != nil {
		// Do not overwrite an unreadable history file. The previous
		// implementation ignored this error and silently replaced the entire
		// conversation with a single message.
		return fmt.Errorf("read existing history: %w", err)
	}

	msgs = append(msgs, msg)
	return h.writeFile(peerID, msgs)
}

// LoadHistory returns all stored messages for a peer, decrypted.
// Returns empty slice (not error) if no history file exists yet.
func (h *HistoryStore) LoadHistory(peerID string) ([]StoredMsg, error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.readFile(peerID)
}

// LoadAllHistories scans the history directory and returns all peer histories.
// Key = peer ID, Value = messages.
func (h *HistoryStore) LoadAllHistories() (map[string][]StoredMsg, error) {
	h.mu.Lock()
	defer h.mu.Unlock()

	entries, err := os.ReadDir(h.dataDir)
	if err != nil {
		if os.IsNotExist(err) {
			return map[string][]StoredMsg{}, nil
		}
		return nil, fmt.Errorf("read history dir: %w", err)
	}

	result := make(map[string][]StoredMsg)
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".enc") {
			continue
		}
		peerID := strings.TrimSuffix(entry.Name(), ".enc")
		msgs, err := h.readFile(peerID)
		if err != nil {
			continue // skip corrupt files
		}
		if len(msgs) > 0 {
			result[peerID] = msgs
		}
	}
	return result, nil
}

// readFile decrypts and parses the history file for a peer.
func (h *HistoryStore) readFile(peerID string) ([]StoredMsg, error) {
	if err := validatePeerID(peerID); err != nil {
		return nil, err
	}

	data, err := os.ReadFile(h.filePath(peerID))
	if err != nil {
		if os.IsNotExist(err) {
			return []StoredMsg{}, nil
		}
		return nil, fmt.Errorf("read file: %w", err)
	}

	key, err := h.deriveKey(peerID)
	if err != nil {
		return nil, err
	}

	aead, err := chacha20poly1305.New(key)
	if err != nil {
		return nil, fmt.Errorf("create aead: %w", err)
	}

	ns := aead.NonceSize()
	if len(data) < ns {
		return nil, fmt.Errorf("file too short")
	}

	plain, err := aead.Open(nil, data[:ns], data[ns:], nil)
	if err != nil {
		return nil, fmt.Errorf("decrypt: %w", err)
	}

	var msgs []StoredMsg
	if err := json.Unmarshal(plain, &msgs); err != nil {
		return nil, fmt.Errorf("parse json: %w", err)
	}

	return msgs, nil
}

// writeFile encrypts and writes the full message list for a peer.
func (h *HistoryStore) writeFile(peerID string, msgs []StoredMsg) error {
	if err := validatePeerID(peerID); err != nil {
		return err
	}

	plain, err := json.Marshal(msgs)
	if err != nil {
		return fmt.Errorf("marshal: %w", err)
	}

	key, err := h.deriveKey(peerID)
	if err != nil {
		return err
	}

	aead, err := chacha20poly1305.New(key)
	if err != nil {
		return fmt.Errorf("create aead: %w", err)
	}

	nonce := make([]byte, aead.NonceSize())
	if _, err := rand.Read(nonce); err != nil {
		return fmt.Errorf("nonce: %w", err)
	}

	// File format: nonce || ciphertext
	out := aead.Seal(nonce, nonce, plain, nil)

	tmp, err := os.CreateTemp(h.dataDir, peerID+".tmp-*")
	if err != nil {
		return fmt.Errorf("create temp file: %w", err)
	}
	tmpName := tmp.Name()
	defer os.Remove(tmpName)

	if _, err := tmp.Write(out); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("write temp file: %w", err)
	}
	if err := tmp.Sync(); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("sync temp file: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("close temp file: %w", err)
	}

	if err := os.Rename(tmpName, h.filePath(peerID)); err != nil {
		return fmt.Errorf("replace history file: %w", err)
	}
	return nil
}

// ClearHistory deletes the history file for a peer.
func (h *HistoryStore) ClearHistory(peerID string) error {
	h.mu.Lock()
	defer h.mu.Unlock()

	if err := validatePeerID(peerID); err != nil {
		return err
	}

	err := os.Remove(h.filePath(peerID))
	if os.IsNotExist(err) {
		return nil
	}
	return err
}
