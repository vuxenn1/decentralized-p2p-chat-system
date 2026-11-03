// Package storage - local contact verification state.
// Stores which peer IDs the user has marked as verified.
package storage

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"sync"
	"time"
)

// Verification records a locally confirmed peer identity.
type Verification struct {
	PeerID    string    `json:"peerId"`
	Verified  bool      `json:"verified"`
	UpdatedAt time.Time `json:"updatedAt"`
}

// VerificationStore manages verified peers on disk.
type VerificationStore struct {
	filePath string
	mu       sync.Mutex
}

// NewVerificationStore creates a VerificationStore.
func NewVerificationStore(dataDir string) (*VerificationStore, error) {
	if err := os.MkdirAll(dataDir, 0700); err != nil {
		return nil, fmt.Errorf("create dir: %w", err)
	}
	return &VerificationStore{
		filePath: filepath.Join(dataDir, "verified_peers.json"),
	}, nil
}

// SetVerified stores or removes a verification marker for a peer.
func (v *VerificationStore) SetVerified(peerID string, verified bool) error {
	v.mu.Lock()
	defer v.mu.Unlock()

	records, _ := v.load()
	byID := make(map[string]Verification, len(records))
	for _, record := range records {
		byID[record.PeerID] = record
	}

	if verified {
		byID[peerID] = Verification{
			PeerID:    peerID,
			Verified:  true,
			UpdatedAt: time.Now(),
		}
	} else {
		delete(byID, peerID)
	}

	updated := make([]Verification, 0, len(byID))
	for _, record := range byID {
		updated = append(updated, record)
	}
	sort.Slice(updated, func(i, j int) bool {
		return updated[i].PeerID < updated[j].PeerID
	})
	return v.save(updated)
}

// IsVerified reports whether a peer is marked as verified.
func (v *VerificationStore) IsVerified(peerID string) bool {
	v.mu.Lock()
	defer v.mu.Unlock()

	records, err := v.load()
	if err != nil {
		return false
	}
	for _, record := range records {
		if record.PeerID == peerID {
			return record.Verified
		}
	}
	return false
}

// VerifiedPeerIDs returns the verified peer IDs.
func (v *VerificationStore) VerifiedPeerIDs() ([]string, error) {
	v.mu.Lock()
	defer v.mu.Unlock()

	records, err := v.load()
	if err != nil {
		return nil, err
	}
	ids := make([]string, 0, len(records))
	for _, record := range records {
		if record.Verified {
			ids = append(ids, record.PeerID)
		}
	}
	sort.Strings(ids)
	return ids, nil
}

func (v *VerificationStore) load() ([]Verification, error) {
	data, err := os.ReadFile(v.filePath)
	if err != nil {
		if os.IsNotExist(err) {
			return []Verification{}, nil
		}
		return nil, err
	}
	var records []Verification
	if err := json.Unmarshal(data, &records); err != nil {
		return nil, err
	}
	return records, nil
}

func (v *VerificationStore) save(records []Verification) error {
	data, err := json.MarshalIndent(records, "", "  ")
	if err != nil {
		return err
	}

	tmp, err := os.CreateTemp(filepath.Dir(v.filePath), ".verified_peers-*.tmp")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	defer os.Remove(tmpName)

	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmpName, v.filePath)
}
