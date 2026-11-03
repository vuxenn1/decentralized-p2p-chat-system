package storage

import "testing"

func TestVerificationStoreRoundTrip(t *testing.T) {
	dir := t.TempDir()
	store, err := NewVerificationStore(dir)
	if err != nil {
		t.Fatal(err)
	}

	if store.IsVerified("peer-a") {
		t.Fatal("new peer should not be verified")
	}

	if err := store.SetVerified("peer-a", true); err != nil {
		t.Fatal(err)
	}
	if !store.IsVerified("peer-a") {
		t.Fatal("peer-a should be verified")
	}

	ids, err := store.VerifiedPeerIDs()
	if err != nil {
		t.Fatal(err)
	}
	if len(ids) != 1 || ids[0] != "peer-a" {
		t.Fatalf("unexpected verified peers: %v", ids)
	}

	reloaded, err := NewVerificationStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	if !reloaded.IsVerified("peer-a") {
		t.Fatal("verification did not survive a reload")
	}

	if err := store.SetVerified("peer-a", false); err != nil {
		t.Fatal(err)
	}
	if store.IsVerified("peer-a") {
		t.Fatal("peer-a should no longer be verified")
	}
}
