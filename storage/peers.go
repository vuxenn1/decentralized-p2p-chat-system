// Package storage - trusted peer management.
// Saves peer ID + nickname to a plain JSON file.
// Peer IDs are public information so no encryption needed.
package storage

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"
)

// TrustedPeer holds a saved peer's info.
type TrustedPeer struct {
	PeerID   string    `json:"peerId"`
	Nickname string    `json:"nickname"`
	SavedAt  time.Time `json:"savedAt"`
}

// PeerStore manages trusted peers on disk.
type PeerStore struct {
	filePath string
	mu       sync.Mutex
}

// NewPeerStore creates a PeerStore.
func NewPeerStore(dataDir string) (*PeerStore, error) {
	if err := os.MkdirAll(dataDir, 0700); err != nil {
		return nil, fmt.Errorf("create dir: %w", err)
	}
	return &PeerStore{
		filePath: filepath.Join(dataDir, "trusted_peers.json"),
	}, nil
}

// SavePeer adds or updates a trusted peer.
func (p *PeerStore) SavePeer(peerID, nickname string) error {
	p.mu.Lock()
	defer p.mu.Unlock()

	peers, _ := p.load()

	for i, peer := range peers {
		if peer.PeerID == peerID {
			peers[i].Nickname = nickname
			peers[i].SavedAt = time.Now()
			return p.save(peers)
		}
	}

	peers = append(peers, TrustedPeer{
		PeerID:   peerID,
		Nickname: nickname,
		SavedAt:  time.Now(),
	})
	return p.save(peers)
}

// RemovePeerByNum removes a peer by their position number (1-based).
// Returns the removed peer so the caller can print its name.
func (p *PeerStore) RemovePeerByNum(num int) (TrustedPeer, error) {
	p.mu.Lock()
	defer p.mu.Unlock()

	peers, err := p.load()
	if err != nil {
		return TrustedPeer{}, err
	}

	if num < 1 || num > len(peers) {
		return TrustedPeer{}, fmt.Errorf("no saved peer with number %d", num)
	}

	removed := peers[num-1]
	filtered := append(peers[:num-1], peers[num:]...)
	return removed, p.save(filtered)
}

// RemovePeerByID removes a peer by their full peer ID.
func (p *PeerStore) RemovePeerByID(peerID string) error {
	p.mu.Lock()
	defer p.mu.Unlock()

	peers, err := p.load()
	if err != nil {
		return err
	}

	filtered := make([]TrustedPeer, 0, len(peers))
	for _, peer := range peers {
		if peer.PeerID != peerID {
			filtered = append(filtered, peer)
		}
	}
	return p.save(filtered)
}

// LoadPeers returns all saved peers.
func (p *PeerStore) LoadPeers() ([]TrustedPeer, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.load()
}

// IsSaved returns true if a peer ID is already saved.
func (p *PeerStore) IsSaved(peerID string) bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	peers, err := p.load()
	if err != nil {
		return false
	}
	for _, peer := range peers {
		if peer.PeerID == peerID {
			return true
		}
	}
	return false
}

func (p *PeerStore) load() ([]TrustedPeer, error) {
	data, err := os.ReadFile(p.filePath)
	if err != nil {
		if os.IsNotExist(err) {
			return []TrustedPeer{}, nil
		}
		return nil, err
	}
	var peers []TrustedPeer
	if err := json.Unmarshal(data, &peers); err != nil {
		return nil, err
	}
	return peers, nil
}

func (p *PeerStore) save(peers []TrustedPeer) error {
	data, err := json.MarshalIndent(peers, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(p.filePath, data, 0600)
}
