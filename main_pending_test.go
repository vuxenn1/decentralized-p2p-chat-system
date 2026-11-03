package main

import (
	"context"
	"testing"

	peerstore "github.com/libp2p/go-libp2p/core/peer"

	"p2p-chat/protocol"
)

func TestPendingAckRemoval(t *testing.T) {
	sm := newStreamManager(nil, context.Background())
	peerID := peerstore.ID("peer-a")

	envelope, err := protocol.NewChat("conversation", "peer-a", 1, chatBody{Text: "hello"})
	if err != nil {
		t.Fatal(err)
	}

	sm.trackPending(peerID, envelope)
	if got := sm.pendingCount(peerID); got != 1 {
		t.Fatalf("expected 1 pending message, got %d", got)
	}

	sm.ackPending(peerID, envelope.ID)
	if got := sm.pendingCount(peerID); got != 0 {
		t.Fatalf("expected 0 pending messages after ack, got %d", got)
	}

	sm.trackPending(peerID, envelope)
	sm.clearPending(peerID)
	if got := sm.pendingCount(peerID); got != 0 {
		t.Fatalf("expected 0 pending messages after clear, got %d", got)
	}
}
