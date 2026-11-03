package storage

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func newStore(t *testing.T) (*HistoryStore, string) {
	t.Helper()
	root := t.TempDir()
	store, err := NewHistoryStore(root, []byte("0123456789abcdef0123456789abcdef"))
	if err != nil {
		t.Fatal(err)
	}
	return store, root
}

func testMessage(id, content string) StoredMsg {
	return StoredMsg{
		ID:        id,
		From:      "peerA",
		FromName:  "alice",
		Content:   content,
		Timestamp: time.Now(),
		IsOwn:     true,
	}
}

func TestSaveAndLoadHistory(t *testing.T) {
	store, _ := newStore(t)

	if err := store.SaveMessage("peerA", testMessage("1", "hello")); err != nil {
		t.Fatal(err)
	}
	if err := store.SaveMessage("peerA", testMessage("2", "world")); err != nil {
		t.Fatal(err)
	}

	messages, err := store.LoadHistory("peerA")
	if err != nil {
		t.Fatal(err)
	}
	if len(messages) != 2 {
		t.Fatalf("expected 2 messages, got %d", len(messages))
	}
	if messages[0].Content != "hello" || messages[1].Content != "world" {
		t.Fatalf("unexpected messages: %+v", messages)
	}
}

func TestClearHistoryRejectsTraversal(t *testing.T) {
	store, root := newStore(t)
	victim := filepath.Join(root, "victim.enc")
	if err := os.WriteFile(victim, []byte("do not delete"), 0600); err != nil {
		t.Fatal(err)
	}

	if err := store.ClearHistory("../victim"); err == nil {
		t.Fatal("expected path traversal to be rejected")
	}
	if _, err := os.Stat(victim); err != nil {
		t.Fatalf("victim file was removed: %v", err)
	}
}

func TestSaveMessageRejectsTraversal(t *testing.T) {
	store, root := newStore(t)
	victim := filepath.Join(root, "victim.enc")
	if err := os.WriteFile(victim, []byte("do not overwrite"), 0600); err != nil {
		t.Fatal(err)
	}

	if err := store.SaveMessage("../victim", testMessage("1", "bad")); err == nil {
		t.Fatal("expected path traversal to be rejected")
	}
	data, err := os.ReadFile(victim)
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != "do not overwrite" {
		t.Fatalf("victim file was modified: %q", string(data))
	}
}

func TestCorruptHistoryIsNotOverwritten(t *testing.T) {
	store, root := newStore(t)

	if err := store.SaveMessage("peerA", testMessage("1", "first")); err != nil {
		t.Fatal(err)
	}
	if err := store.SaveMessage("peerA", testMessage("2", "second")); err != nil {
		t.Fatal(err)
	}

	path := filepath.Join(root, "history", "peerA.enc")
	corrupt := []byte("corrupt-data")
	if err := os.WriteFile(path, corrupt, 0600); err != nil {
		t.Fatal(err)
	}

	if err := store.SaveMessage("peerA", testMessage("3", "third")); err == nil {
		t.Fatal("expected SaveMessage to fail on corrupt history")
	}

	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != string(corrupt) {
		t.Fatalf("corrupt file was overwritten: %q", string(data))
	}
}

func TestWriteReplacesExistingFile(t *testing.T) {
	store, root := newStore(t)

	if err := store.SaveMessage("peerA", testMessage("1", "first")); err != nil {
		t.Fatal(err)
	}
	if err := store.SaveMessage("peerA", testMessage("2", "second")); err != nil {
		t.Fatal(err)
	}

	entries, err := os.ReadDir(filepath.Join(root, "history"))
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range entries {
		if entry.Name() != "peerA.enc" {
			t.Fatalf("unexpected leftover file: %s", entry.Name())
		}
	}

	messages, err := store.LoadHistory("peerA")
	if err != nil {
		t.Fatal(err)
	}
	if len(messages) != 2 {
		t.Fatalf("expected 2 messages, got %d", len(messages))
	}
}
