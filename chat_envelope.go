package main

import (
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"sync/atomic"
	"time"

	"github.com/libp2p/go-libp2p/core/network"
	peerstore "github.com/libp2p/go-libp2p/core/peer"

	"p2p-chat/protocol"
	"p2p-chat/security"
)

const (
	controlNickname       = "nickname"
	controlLatencyPing    = "latency_ping"
	controlLatencyPong    = "latency_pong"
	controlConcurrentTest = "concurrent_test"
)

type chatBody struct {
	Text string `json:"text"`
}

type controlBody struct {
	Kind      string `json:"kind"`
	Name      string `json:"name,omitempty"`
	Timestamp int64  `json:"timestamp,omitempty"`
	Payload   string `json:"payload,omitempty"`
	Index     int    `json:"index,omitempty"`
}

type ackBody struct {
	ID string `json:"id"`
}

type pendingChat struct {
	envelope *protocol.Envelope
	lastSent time.Time
	attempts int
}

const (
	maxReceiveBuffer  = 64
	pendingTimeout    = 2 * time.Second
	maxPendingRetries = 5
)

// conversationID returns a stable identifier for a one-to-one conversation.
// Both peers compute the same value regardless of who initiated the stream.
func conversationID(first, second peerstore.ID) string {
	ids := []string{first.String(), second.String()}
	sort.Strings(ids)
	sum := sha256.Sum256([]byte(ids[0] + "|" + ids[1]))
	return base64.RawURLEncoding.EncodeToString(sum[:16])
}

func (sm *StreamManager) newChatEnvelope(remote peerstore.ID, sequence uint64, text string) (*protocol.Envelope, error) {
	if sm.node == nil {
		return nil, errors.New("node is not initialized")
	}
	local := sm.node.ID()
	return protocol.NewChat(conversationID(local, remote), local.String(), sequence, chatBody{Text: text})
}

func (sm *StreamManager) newControlEnvelope(remote peerstore.ID, body controlBody) (*protocol.Envelope, error) {
	if sm.node == nil {
		return nil, errors.New("node is not initialized")
	}
	local := sm.node.ID()
	return protocol.New(protocol.TypeControl, conversationID(local, remote), local.String(), body)
}

func (sm *StreamManager) newAckEnvelope(remote peerstore.ID, messageID string) (*protocol.Envelope, error) {
	if sm.node == nil {
		return nil, errors.New("node is not initialized")
	}
	local := sm.node.ID()
	return protocol.New(protocol.TypeAck, conversationID(local, remote), local.String(), ackBody{ID: messageID})
}

func (sm *StreamManager) sendEnvelope(stream network.Stream, session *security.Session, envelope *protocol.Envelope) error {
	encoded, err := envelope.Encode()
	if err != nil {
		return err
	}
	packet, err := session.Encrypt(encoded)
	if err != nil {
		return err
	}

	// Multiple goroutines can send on the same stream, for example a chat
	// message and a latency response. Serialize the frame writes.
	sm.writeMu.Lock()
	defer sm.writeMu.Unlock()
	_, err = stream.Write([]byte(packet + "\n"))
	return err
}

func (sm *StreamManager) sendChat(remote peerstore.ID, text string) (*protocol.Envelope, error) {
	sm.mu.RLock()
	stream, hasStream := sm.streams[remote]
	session, hasSession := sm.sessions[remote]
	sm.mu.RUnlock()

	if !hasStream || !hasSession {
		return nil, errors.New("peer is not connected")
	}

	// Serialize chat sequence assignment and the frame write so the receiver
	// sees sequence numbers in the same order they were assigned.
	sm.sendMu.Lock()
	defer sm.sendMu.Unlock()

	sm.mu.Lock()
	sequence := sm.sendSeq[remote] + 1
	sm.sendSeq[remote] = sequence
	sm.mu.Unlock()

	envelope, err := sm.newChatEnvelope(remote, sequence, text)
	if err != nil {
		return nil, err
	}

	// Track before writing so an acknowledgement that arrives immediately is
	// not lost.
	sm.trackPending(remote, envelope)
	if err := sm.sendEnvelope(stream, session, envelope); err != nil {
		sm.ackPending(remote, envelope.ID)
		return nil, err
	}
	return envelope, nil
}

func (sm *StreamManager) sendControl(remote peerstore.ID, body controlBody) error {
	sm.mu.RLock()
	stream, hasStream := sm.streams[remote]
	session, hasSession := sm.sessions[remote]
	sm.mu.RUnlock()

	if !hasStream || !hasSession {
		return errors.New("peer is not connected")
	}

	envelope, err := sm.newControlEnvelope(remote, body)
	if err != nil {
		return err
	}
	return sm.sendEnvelope(stream, session, envelope)
}

func (sm *StreamManager) sendAck(remote peerstore.ID, messageID string) error {
	sm.mu.RLock()
	stream, hasStream := sm.streams[remote]
	session, hasSession := sm.sessions[remote]
	sm.mu.RUnlock()

	if !hasStream || !hasSession {
		return errors.New("peer is not connected")
	}

	envelope, err := sm.newAckEnvelope(remote, messageID)
	if err != nil {
		return err
	}
	return sm.sendEnvelope(stream, session, envelope)
}

func (sm *StreamManager) trackPending(remote peerstore.ID, envelope *protocol.Envelope) {
	sm.pendingMu.Lock()
	defer sm.pendingMu.Unlock()

	messages := sm.pending[remote]
	if messages == nil {
		messages = make(map[string]*pendingChat)
		sm.pending[remote] = messages
	}
	messages[envelope.ID] = &pendingChat{
		envelope: envelope,
		lastSent: time.Now(),
		attempts: 1,
	}
}

func (sm *StreamManager) ackPending(remote peerstore.ID, messageID string) {
	sm.pendingMu.Lock()
	defer sm.pendingMu.Unlock()

	messages := sm.pending[remote]
	if messages == nil {
		return
	}
	delete(messages, messageID)
	if len(messages) == 0 {
		delete(sm.pending, remote)
	}
}

func (sm *StreamManager) clearPending(remote peerstore.ID) {
	sm.pendingMu.Lock()
	defer sm.pendingMu.Unlock()
	delete(sm.pending, remote)
}

func (sm *StreamManager) pendingCount(remote peerstore.ID) int {
	sm.pendingMu.Lock()
	defer sm.pendingMu.Unlock()
	return len(sm.pending[remote])
}

func (sm *StreamManager) retransmitLoop() {
	ticker := time.NewTicker(time.Second)
	defer ticker.Stop()

	for {
		select {
		case <-ticker.C:
			sm.retransmitDue()
		case <-sm.ctx.Done():
			return
		}
	}
}

func (sm *StreamManager) retransmitDue() {
	type item struct {
		peer    peerstore.ID
		message *pendingChat
	}

	now := time.Now()
	due := make([]item, 0)

	sm.pendingMu.Lock()
	for remote, messages := range sm.pending {
		for _, message := range messages {
			if now.Sub(message.lastSent) >= pendingTimeout {
				due = append(due, item{peer: remote, message: message})
			}
		}
	}
	sm.pendingMu.Unlock()

	for _, pending := range due {
		sm.mu.RLock()
		stream, hasStream := sm.streams[pending.peer]
		session, hasSession := sm.sessions[pending.peer]
		sm.mu.RUnlock()

		if !hasStream || !hasSession {
			continue
		}

		err := sm.sendEnvelope(stream, session, pending.message.envelope)

		sm.pendingMu.Lock()
		messages := sm.pending[pending.peer]
		current, stillPending := messages[pending.message.envelope.ID]
		if !stillPending || current != pending.message {
			sm.pendingMu.Unlock()
			continue
		}

		if err != nil || current.attempts >= maxPendingRetries {
			delete(messages, pending.message.envelope.ID)
			if len(messages) == 0 {
				delete(sm.pending, pending.peer)
			}
			sm.pendingMu.Unlock()
			NotifyWebError("Message delivery failed")
			continue
		}

		current.attempts++
		current.lastSent = now
		sm.pendingMu.Unlock()
	}
}

func (sm *StreamManager) handleEnvelope(peerID peerstore.ID, envelope *protocol.Envelope) {
	if sm.node == nil {
		return
	}
	if envelope.Conversation != conversationID(sm.node.ID(), peerID) {
		fmt.Printf("Rejected envelope with an unexpected conversation from [%s]\n", peerID)
		return
	}
	if envelope.Sender != peerID.String() {
		fmt.Printf("Rejected envelope with an unexpected sender from [%s]\n", peerID)
		return
	}

	switch envelope.Type {
	case protocol.TypeChat:
		var body chatBody
		if err := json.Unmarshal(envelope.Body, &body); err != nil {
			fmt.Printf("Invalid chat body from [%s]: %v\n", peerID, err)
			return
		}
		if strings.TrimSpace(body.Text) == "" {
			return
		}

		sm.mu.Lock()
		last := sm.recvSeq[peerID]
		if envelope.Sequence <= last {
			sm.mu.Unlock()
			_ = sm.sendAck(peerID, envelope.ID)
			return
		}
		if envelope.Sequence > last+1 {
			buffer := sm.recvBuffer[peerID]
			if buffer == nil {
				buffer = make(map[uint64]*protocol.Envelope)
				sm.recvBuffer[peerID] = buffer
			}
			if len(buffer) >= maxReceiveBuffer {
				sm.mu.Unlock()
				fmt.Printf("Receive buffer is full for [%s]\n", peerID)
				return
			}
			buffer[envelope.Sequence] = envelope
			sm.mu.Unlock()
			_ = sm.sendAck(peerID, envelope.ID)
			return
		}

		sm.recvSeq[peerID] = envelope.Sequence
		sm.mu.Unlock()

		sm.deliverChat(peerID, body.Text)
		if err := sm.sendAck(peerID, envelope.ID); err != nil {
			fmt.Printf("Failed to send ack to [%s]: %v\n", peerID, err)
		}

		sm.drainReceiveBuffer(peerID)

	case protocol.TypeAck:
		var body ackBody
		if err := json.Unmarshal(envelope.Body, &body); err != nil {
			fmt.Printf("Invalid ack body from [%s]: %v\n", peerID, err)
			return
		}
		sm.ackPending(peerID, body.ID)

	case protocol.TypeControl:
		var body controlBody
		if err := json.Unmarshal(envelope.Body, &body); err != nil {
			fmt.Printf("Invalid control body from [%s]: %v\n", peerID, err)
			return
		}
		sm.handleControl(peerID, body)

	case protocol.TypeError:
		fmt.Printf("Peer [%s] reported an error\n", peerID)

	default:
		// Acknowledgements, typing indicators and presence are reserved for
		// later protocol steps.
	}
}

func (sm *StreamManager) deliverChat(peerID peerstore.ID, text string) {
	nick, shortID := sm.displayName(peerID)
	display := shortID
	if nick != "" {
		display = nick
	}
	fmt.Printf("[%s] [%s]: %s\n",
		colorize(93, time.Now().Format("15:04")),
		colorize(96, display),
		text)
	NotifyWebMessage(peerID.String(), display, text, false, peerID.String(), true)
}

func (sm *StreamManager) drainReceiveBuffer(peerID peerstore.ID) {
	for {
		sm.mu.Lock()
		next := sm.recvSeq[peerID] + 1
		envelope, ok := sm.recvBuffer[peerID][next]
		if ok {
			delete(sm.recvBuffer[peerID], next)
			sm.recvSeq[peerID] = next
		}
		sm.mu.Unlock()

		if !ok {
			return
		}

		var body chatBody
		if err := json.Unmarshal(envelope.Body, &body); err != nil {
			fmt.Printf("Invalid buffered chat body from [%s]: %v\n", peerID, err)
			continue
		}
		if strings.TrimSpace(body.Text) == "" {
			continue
		}

		sm.deliverChat(peerID, body.Text)
		if err := sm.sendAck(peerID, envelope.ID); err != nil {
			fmt.Printf("Failed to send ack to [%s]: %v\n", peerID, err)
		}
	}
}

func (sm *StreamManager) handleControl(peerID peerstore.ID, body controlBody) {
	switch body.Kind {
	case controlNickname:
		nick := strings.TrimSpace(body.Name)
		if nick == "" {
			return
		}
		sm.mu.Lock()
		old := sm.peerNicks[peerID]
		sm.peerNicks[peerID] = nick
		sm.mu.Unlock()
		if old != nick {
			_, shortID := sm.displayName(peerID)
			fmt.Printf("Peer [%s] is now known as [%s]\n",
				colorize(96, shortID), colorize(92, nick))
			NotifyWebPeerUpdate()
		}

	case controlLatencyPing:
		if body.Timestamp <= 0 {
			return
		}
		if err := sm.sendControl(peerID, controlBody{
			Kind:      controlLatencyPong,
			Timestamp: body.Timestamp,
		}); err != nil {
			fmt.Printf("Failed to send latency response to [%s]: %v\n", peerID, err)
		}

	case controlLatencyPong:
		if body.Timestamp <= 0 {
			return
		}
		rtt := time.Now().UnixMilli() - body.Timestamp
		fmt.Printf("LATENCY_RESULT:%d\n", rtt/2)

	case controlConcurrentTest:
		count := atomic.AddInt64(&concurrentTestReceived, 1)
		fmt.Printf("CONCURRENT_RECEIVED:%d\n", count)

	default:
		fmt.Printf("Unknown control message from [%s]: %s\n", peerID, body.Kind)
	}
}
