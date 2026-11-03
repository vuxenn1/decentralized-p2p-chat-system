package protocol

import (
	"encoding/json"
	"strings"
	"testing"
	"time"
)

func TestRoundTripChat(t *testing.T) {
	envelope, err := New(TypeChat, "conversation-1", "peer-1", map[string]string{"text": "hello"})
	if err != nil {
		t.Fatal(err)
	}
	if envelope.ID == "" {
		t.Fatal("expected a generated id")
	}
	if envelope.Timestamp <= 0 {
		t.Fatal("expected a positive timestamp")
	}

	data, err := envelope.Encode()
	if err != nil {
		t.Fatal(err)
	}

	decoded, err := Decode(data)
	if err != nil {
		t.Fatal(err)
	}
	if decoded.Version != Version {
		t.Fatalf("unexpected version: %d", decoded.Version)
	}
	if decoded.Type != TypeChat {
		t.Fatalf("unexpected type: %s", decoded.Type)
	}
	if decoded.Conversation != "conversation-1" {
		t.Fatalf("unexpected conversation: %s", decoded.Conversation)
	}
	if decoded.Sender != "peer-1" {
		t.Fatalf("unexpected sender: %s", decoded.Sender)
	}

	var body map[string]string
	if err := json.Unmarshal(decoded.Body, &body); err != nil {
		t.Fatal(err)
	}
	if body["text"] != "hello" {
		t.Fatalf("unexpected body: %+v", body)
	}
}

func TestNewRejectsUnknownType(t *testing.T) {
	if _, err := New(MessageType("unknown"), "conversation-1", "peer-1", nil); err == nil {
		t.Fatal("expected an unknown type to be rejected")
	}
}

func TestNewRejectsChatWithoutConversation(t *testing.T) {
	if _, err := New(TypeChat, "", "peer-1", nil); err == nil {
		t.Fatal("expected a chat envelope without a conversation to be rejected")
	}
}

func TestNewRejectsChatWithoutSender(t *testing.T) {
	if _, err := New(TypeChat, "conversation-1", "", nil); err == nil {
		t.Fatal("expected a chat envelope without a sender to be rejected")
	}
}

func TestDecodeRejectsOldVersion(t *testing.T) {
	payload := `{"v":1,"t":"control","id":"abc","ts":1}`
	if _, err := Decode([]byte(payload)); err == nil {
		t.Fatal("expected version 1 to be rejected")
	}
}

func TestDecodeRejectsMissingID(t *testing.T) {
	payload := `{"v":2,"t":"control","ts":1}`
	if _, err := Decode([]byte(payload)); err == nil {
		t.Fatal("expected a missing id to be rejected")
	}
}

func TestDecodeRejectsInvalidBody(t *testing.T) {
	payload := `{"v":2,"t":"control","id":"abc","ts":1,"body":{bad}}`
	if _, err := Decode([]byte(payload)); err == nil {
		t.Fatal("expected invalid json to be rejected")
	}
}

func TestNewRejectsOversizedBody(t *testing.T) {
	body := strings.Repeat("a", MaxBodySize+1)
	if _, err := New(TypeControl, "", "", map[string]string{"data": body}); err == nil {
		t.Fatal("expected an oversized body to be rejected")
	}
}

func TestDecodeRejectsOversizedEnvelope(t *testing.T) {
	payload := make([]byte, MaxEnvelopeSize+1)
	if _, err := Decode(payload); err == nil {
		t.Fatal("expected an oversized envelope to be rejected")
	}
}

func TestNewIDIsUnique(t *testing.T) {
	first, err := NewID()
	if err != nil {
		t.Fatal(err)
	}
	second, err := NewID()
	if err != nil {
		t.Fatal(err)
	}
	if first == "" || second == "" {
		t.Fatal("expected non-empty ids")
	}
	if first == second {
		t.Fatal("expected different ids")
	}
}

func TestValidationRejectsFarFutureTimestamp(t *testing.T) {
	envelope := &Envelope{
		Version:   Version,
		Type:      TypeControl,
		ID:        "abc",
		Timestamp: time.Now().Add(24 * time.Hour).UnixMilli(),
	}
	if err := envelope.Validate(); err == nil {
		t.Fatal("expected a far-future timestamp to be rejected")
	}
}
