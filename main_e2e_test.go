package main

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/libp2p/go-libp2p"
	"github.com/libp2p/go-libp2p/core/host"
	"github.com/libp2p/go-libp2p/core/network"

	"p2p-chat/storage"
)

func testNode(t *testing.T, nick, dir string) (*StreamManager, host.Host, func()) {
	t.Helper()

	h, err := libp2p.New(libp2p.ListenAddrStrings("/ip4/127.0.0.1/tcp/0"))
	if err != nil {
		t.Fatal(err)
	}

	sm := newStreamManager(h, context.Background())
	sm.localNick = nick
	h.SetStreamHandler(ChatProtocolID, func(s network.Stream) {
		go sm.HandleIncomingStream(s)
	})

	priv, err := h.Peerstore().PrivKey(h.ID()).Raw()
	if err != nil {
		_ = h.Close()
		t.Fatal(err)
	}
	history, err := storage.NewHistoryStore(dir, priv)
	if err != nil {
		_ = h.Close()
		t.Fatal(err)
	}
	sm.historyStore = history

	return sm, h, func() { _ = h.Close() }
}

func waitFor(t *testing.T, description string, timeout time.Duration, condition func() bool) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if condition() {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Errorf("timed out waiting for %s", description)
}

func fakeWeb(sm *StreamManager) *WebClient {
	return &WebClient{
		sm:              sm,
		messages:        make([]WebMsg, 0),
		pendingRequests: make([]map[string]string, 0),
	}
}

func TestProtocolV2ChatAndControl(t *testing.T) {
	smA, hA, closeA := testNode(t, "alice", t.TempDir())
	defer closeA()
	smB, hB, closeB := testNode(t, "bob", t.TempDir())
	defer closeB()

	webClient.Store(fakeWeb(smB))
	fake := webClient.Load()

	bobAddr := fmt.Sprintf("%s/p2p/%s", hB.Addrs()[0].String(), hB.ID())
	go smA.connectToPeer(bobAddr)

	var requestNumber int
	waitFor(t, "connection request", 5*time.Second, func() bool {
		smB.pendingApprovalsMu.Lock()
		defer smB.pendingApprovalsMu.Unlock()
		for number := range smB.pendingRequestNums {
			requestNumber = number
			return true
		}
		return false
	})
	smB.RespondToConnectionRequestByNum(requestNumber, true)

	waitFor(t, "both streams", 10*time.Second, func() bool {
		smA.mu.RLock()
		_, hasA := smA.streams[hB.ID()]
		smA.mu.RUnlock()
		smB.mu.RLock()
		_, hasB := smB.streams[hA.ID()]
		smB.mu.RUnlock()
		return hasA && hasB
	})

	waitFor(t, "nickname control", 5*time.Second, func() bool {
		smB.mu.RLock()
		defer smB.mu.RUnlock()
		return smB.peerNicks[hA.ID()] == "alice"
	})

	if _, err := smA.sendChat(hB.ID(), "hello"); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "chat message", 5*time.Second, func() bool {
		fake.messagesMu.RLock()
		defer fake.messagesMu.RUnlock()
		for _, message := range fake.messages {
			if message.Content == "hello" {
				return true
			}
		}
		return false
	})

	if _, err := smA.sendChat(hB.ID(), "__NICKNAME__:admin"); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "second chat message", 5*time.Second, func() bool {
		fake.messagesMu.RLock()
		defer fake.messagesMu.RUnlock()
		return len(fake.messages) == 2
	})

	if err := smA.sendControl(hB.ID(), controlBody{
		Kind:      controlLatencyPing,
		Timestamp: time.Now().UnixMilli(),
		Payload:   "aaaa",
	}); err != nil {
		t.Fatal(err)
	}
	if err := smA.sendControl(hB.ID(), controlBody{
		Kind:  controlConcurrentTest,
		Name:  "alice",
		Index: 1,
	}); err != nil {
		t.Fatal(err)
	}

	time.Sleep(500 * time.Millisecond)

	fake.messagesMu.RLock()
	if len(fake.messages) != 2 {
		t.Errorf("control messages leaked into the UI: got %d messages", len(fake.messages))
	}
	for _, message := range fake.messages {
		if message.Content == "__NICKNAME__:admin" {
			continue
		}
		if message.Content != "hello" {
			t.Errorf("unexpected UI message: %q", message.Content)
		}
	}
	fake.messagesMu.RUnlock()

	smB.mu.RLock()
	nick := smB.peerNicks[hA.ID()]
	smB.mu.RUnlock()
	if nick != "alice" {
		t.Fatalf("chat text changed the peer nickname to %q", nick)
	}

	waitFor(t, "stored chat messages", 5*time.Second, func() bool {
		messages, err := smB.historyStore.LoadHistory(hA.ID().String())
		return err == nil && len(messages) == 2
	})

	smB.mu.RLock()
	receivedSequence := smB.recvSeq[hA.ID()]
	smB.mu.RUnlock()
	if receivedSequence != 2 {
		t.Fatalf("unexpected receive sequence: %d", receivedSequence)
	}

	smA.mu.RLock()
	stream := smA.streams[hB.ID()]
	session := smA.sessions[hB.ID()]
	smA.mu.RUnlock()
	gapEnvelope, err := smA.newChatEnvelope(hB.ID(), 4, "out-of-order")
	if err != nil {
		t.Fatal(err)
	}
	if err := smA.sendEnvelope(stream, session, gapEnvelope); err != nil {
		t.Fatal(err)
	}
	time.Sleep(200 * time.Millisecond)

	smB.mu.RLock()
	receivedSequence = smB.recvSeq[hA.ID()]
	smB.mu.RUnlock()
	if receivedSequence != 2 {
		t.Fatalf("out-of-order message changed the receive sequence to %d", receivedSequence)
	}

	messages, err := smB.historyStore.LoadHistory(hA.ID().String())
	if err != nil {
		t.Fatal(err)
	}
	if len(messages) != 2 {
		t.Fatalf("expected 2 stored chat messages, got %d", len(messages))
	}
	for _, message := range messages {
		if message.Content == "LATENCY_PING" || message.Content == "CONCURRENT_TEST" {
			t.Fatalf("control message stored in history: %q", message.Content)
		}
	}

	// Close the streams before the next test touches the shared webClient.
	closeB()
	closeA()
	time.Sleep(100 * time.Millisecond)
	webClient.Store(nil)
}
