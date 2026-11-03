// Package protocol defines the versioned message envelope for Ağdaş.
//
// The legacy wire format uses line-oriented strings with control prefixes.
// This package is the foundation for protocol v2. It is intentionally kept
// independent from the transport so it can be tested without a network.
package protocol

import (
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"time"
)

// Version is the current envelope version.
const Version = 2

const (
	// MaxEnvelopeSize limits a single encoded envelope.
	MaxEnvelopeSize = 64 * 1024

	// MaxBodySize limits the JSON body inside an envelope.
	MaxBodySize = 48 * 1024

	// MaxIDLength limits message and conversation identifiers.
	MaxIDLength = 128

	// MaxFutureSkew limits how far ahead an envelope timestamp may be.
	MaxFutureSkew = 5 * time.Minute
)

// MessageType identifies the purpose of an envelope.
type MessageType string

const (
	TypeChat     MessageType = "chat"
	TypeAck      MessageType = "ack"
	TypeControl  MessageType = "control"
	TypeError    MessageType = "error"
	TypeTyping   MessageType = "typing"
	TypePresence MessageType = "presence"
)

// Envelope is the versioned unit exchanged by protocol v2 peers.
type Envelope struct {
	Version      int             `json:"v"`
	Type         MessageType     `json:"t"`
	ID           string          `json:"id"`
	Conversation string          `json:"conv,omitempty"`
	Sender       string          `json:"from,omitempty"`
	Timestamp    int64           `json:"ts"`
	Sequence     uint64          `json:"seq,omitempty"`
	Body         json.RawMessage `json:"body,omitempty"`
}

// NewID returns a random identifier for a message or conversation.
func NewID() (string, error) {
	raw := make([]byte, 16)
	if _, err := rand.Read(raw); err != nil {
		return "", fmt.Errorf("generate id: %w", err)
	}
	return base64.RawURLEncoding.EncodeToString(raw), nil
}

// New builds and validates an envelope with a generated ID and current time.
// Chat messages must use NewChat so a sequence number can be supplied.
func New(typ MessageType, conversation, sender string, body any) (*Envelope, error) {
	return newEnvelope(typ, conversation, sender, 0, body)
}

// NewChat builds a chat envelope with a positive per-session sequence number.
func NewChat(conversation, sender string, sequence uint64, body any) (*Envelope, error) {
	return newEnvelope(TypeChat, conversation, sender, sequence, body)
}

func newEnvelope(typ MessageType, conversation, sender string, sequence uint64, body any) (*Envelope, error) {
	id, err := NewID()
	if err != nil {
		return nil, err
	}

	var raw json.RawMessage
	if body != nil {
		encoded, err := json.Marshal(body)
		if err != nil {
			return nil, fmt.Errorf("marshal body: %w", err)
		}
		raw = encoded
	}

	envelope := &Envelope{
		Version:      Version,
		Type:         typ,
		ID:           id,
		Conversation: conversation,
		Sender:       sender,
		Timestamp:    time.Now().UnixMilli(),
		Sequence:     sequence,
		Body:         raw,
	}
	if err := envelope.Validate(); err != nil {
		return nil, err
	}
	return envelope, nil
}

// Valid reports whether the message type is known.
func (t MessageType) Valid() bool {
	switch t {
	case TypeChat, TypeAck, TypeControl, TypeError, TypeTyping, TypePresence:
		return true
	default:
		return false
	}
}

// Validate checks the envelope against the v2 rules.
func (e *Envelope) Validate() error {
	if e == nil {
		return errors.New("nil envelope")
	}
	if e.Version != Version {
		return fmt.Errorf("unsupported envelope version %d", e.Version)
	}
	if !e.Type.Valid() {
		return fmt.Errorf("unknown message type %q", e.Type)
	}
	if e.ID == "" || len(e.ID) > MaxIDLength {
		return fmt.Errorf("invalid message id")
	}
	if e.Timestamp <= 0 {
		return fmt.Errorf("invalid timestamp")
	}
	if e.Timestamp > time.Now().Add(MaxFutureSkew).UnixMilli() {
		return fmt.Errorf("timestamp is too far in the future")
	}
	if len(e.Conversation) > MaxIDLength {
		return fmt.Errorf("conversation id is too long")
	}
	if len(e.Sender) > MaxIDLength {
		return fmt.Errorf("sender id is too long")
	}
	if e.Type == TypeChat && (e.Conversation == "" || e.Sender == "") {
		return fmt.Errorf("chat envelope requires conversation and sender")
	}
	if e.Type == TypeChat && e.Sequence == 0 {
		return fmt.Errorf("chat envelope requires a sequence")
	}
	if len(e.Body) > MaxBodySize {
		return fmt.Errorf("body is too large")
	}
	if len(e.Body) > 0 && !json.Valid(e.Body) {
		return fmt.Errorf("body is not valid json")
	}
	return nil
}

// Encode serializes and size-checks the envelope.
func (e *Envelope) Encode() ([]byte, error) {
	if err := e.Validate(); err != nil {
		return nil, err
	}
	data, err := json.Marshal(e)
	if err != nil {
		return nil, fmt.Errorf("marshal envelope: %w", err)
	}
	if len(data) > MaxEnvelopeSize {
		return nil, fmt.Errorf("envelope is too large")
	}
	return data, nil
}

// Decode parses and validates an envelope.
func Decode(data []byte) (*Envelope, error) {
	if len(data) == 0 {
		return nil, errors.New("empty envelope")
	}
	if len(data) > MaxEnvelopeSize {
		return nil, fmt.Errorf("envelope is too large")
	}

	var envelope Envelope
	if err := json.Unmarshal(data, &envelope); err != nil {
		return nil, fmt.Errorf("decode envelope: %w", err)
	}
	if err := envelope.Validate(); err != nil {
		return nil, err
	}
	return &envelope, nil
}
