package main

import (
	"crypto/sha256"
	"encoding/binary"
	"errors"
	"fmt"
	"sort"
	"strings"

	cryptopb "github.com/libp2p/go-libp2p/core/crypto/pb"
	peer "github.com/libp2p/go-libp2p/core/peer"
	libp2ppeerstore "github.com/libp2p/go-libp2p/core/peerstore"
)

const (
	safetyNumberGroups = 12
	safetyNumberDomain = "agdas-safety-number-v1"
)

// safetyNumber derives a short, human-comparable fingerprint from both
// identity public keys. Sorting by peer ID makes the result symmetric, so both
// sides see the same number for the same pair.
func safetyNumber(ps libp2ppeerstore.Peerstore, local, remote peer.ID) (string, error) {
	if ps == nil {
		return "", errors.New("peerstore is not available")
	}
	if local == remote {
		return "", errors.New("cannot verify a peer against itself")
	}

	localKey := ps.PubKey(local)
	if localKey == nil {
		return "", errors.New("local public key is not available")
	}
	remoteKey := ps.PubKey(remote)
	if remoteKey == nil {
		return "", errors.New("remote public key is not available")
	}

	localRaw, err := localKey.Raw()
	if err != nil {
		return "", fmt.Errorf("local public key bytes: %w", err)
	}
	remoteRaw, err := remoteKey.Raw()
	if err != nil {
		return "", fmt.Errorf("remote public key bytes: %w", err)
	}

	type keyEntry struct {
		id      string
		keyType cryptopb.KeyType
		raw     []byte
	}
	entries := []keyEntry{
		{id: local.String(), keyType: localKey.Type(), raw: localRaw},
		{id: remote.String(), keyType: remoteKey.Type(), raw: remoteRaw},
	}
	sort.Slice(entries, func(i, j int) bool {
		return entries[i].id < entries[j].id
	})

	digest := sha256.New()
	digest.Write([]byte(safetyNumberDomain))
	for _, entry := range entries {
		var typeBuf [4]byte
		binary.BigEndian.PutUint32(typeBuf[:], uint32(entry.keyType))
		digest.Write(typeBuf[:])

		var lengthBuf [4]byte
		binary.BigEndian.PutUint32(lengthBuf[:], uint32(len(entry.raw)))
		digest.Write(lengthBuf[:])
		digest.Write(entry.raw)
	}
	seed := digest.Sum(nil)

	groups := make([]string, 0, safetyNumberGroups)
	for i := 0; i < safetyNumberGroups; i++ {
		round := sha256.New()
		round.Write(seed)
		round.Write([]byte{byte(i)})
		sum := round.Sum(nil)
		value := binary.BigEndian.Uint32(sum[:4]) % 100000
		groups = append(groups, fmt.Sprintf("%05d", value))
	}
	return strings.Join(groups, " "), nil
}

// showSafetyNumber prints the safety number for the active peer and can set
// or clear the local verification marker.
func (sm *StreamManager) showSafetyNumber(args []string) {
	sm.mu.RLock()
	activeID := sm.activeID
	sm.mu.RUnlock()

	if activeID == "" {
		fmt.Println("No active peer. Use '/switch <number>' first.")
		return
	}

	number, err := safetyNumber(sm.node.Peerstore(), sm.node.ID(), activeID)
	if err != nil {
		fmt.Printf("Could not compute the safety number: %v\n", err)
		return
	}

	nick, shortID := sm.displayName(activeID)
	display := shortID
	if nick != "" {
		display = nick
	}

	verified := sm.verificationStore != nil && sm.verificationStore.IsVerified(activeID.String())

	if len(args) > 0 {
		switch strings.ToLower(args[0]) {
		case "ok", "verify":
			if sm.verificationStore == nil {
				fmt.Println("Verification storage is not available.")
				return
			}
			if err := sm.verificationStore.SetVerified(activeID.String(), true); err != nil {
				fmt.Printf("Could not save verification: %v\n", err)
				return
			}
			verified = true
			NotifyVerifiedPeersUpdate()
		case "remove", "clear":
			if sm.verificationStore == nil {
				fmt.Println("Verification storage is not available.")
				return
			}
			if err := sm.verificationStore.SetVerified(activeID.String(), false); err != nil {
				fmt.Printf("Could not remove verification: %v\n", err)
				return
			}
			verified = false
			NotifyVerifiedPeersUpdate()
		default:
			fmt.Println("Usage: '/verify [ok|remove]'")
			return
		}
	}

	status := "not verified"
	statusColor := 91
	if verified {
		status = "verified"
		statusColor = 92
	}

	fmt.Printf("\nSafety number for [%s] [%s]:\n", colorize(96, display), colorize(94, shortID))
	fmt.Println(colorize(97, number))
	fmt.Printf("Status: %s\n", colorize(statusColor, status))
	fmt.Printf("Compare this number with the peer over another channel.\n")
	if !verified {
		fmt.Printf("Use '%s' to mark this contact as verified.\n\n", colorize(93, "/verify ok"))
	} else {
		fmt.Printf("Use '%s' to remove the marker.\n\n", colorize(93, "/verify remove"))
	}
}
