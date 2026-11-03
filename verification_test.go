package main

import (
	"regexp"
	"strings"
	"testing"

	"github.com/libp2p/go-libp2p"
	"github.com/libp2p/go-libp2p/core/host"
	peer "github.com/libp2p/go-libp2p/core/peer"
)

func exchangePublicKeys(t *testing.T, hosts ...host.Host) {
	t.Helper()
	for _, from := range hosts {
		for _, to := range hosts {
			if from.ID() == to.ID() {
				continue
			}
			key := to.Peerstore().PubKey(to.ID())
			if key == nil {
				t.Fatalf("missing public key for %s", to.ID())
			}
			if err := from.Peerstore().AddPubKey(to.ID(), key); err != nil {
				t.Fatal(err)
			}
		}
	}
}

func TestSafetyNumberIsSymmetric(t *testing.T) {
	h1, err := libp2p.New(libp2p.NoListenAddrs)
	if err != nil {
		t.Fatal(err)
	}
	defer h1.Close()

	h2, err := libp2p.New(libp2p.NoListenAddrs)
	if err != nil {
		t.Fatal(err)
	}
	defer h2.Close()

	exchangePublicKeys(t, h1, h2)

	first, err := safetyNumber(h1.Peerstore(), h1.ID(), h2.ID())
	if err != nil {
		t.Fatal(err)
	}
	second, err := safetyNumber(h2.Peerstore(), h2.ID(), h1.ID())
	if err != nil {
		t.Fatal(err)
	}

	if first != second {
		t.Fatalf("safety numbers differ: %q and %q", first, second)
	}

	groups := strings.Fields(first)
	if len(groups) != safetyNumberGroups {
		t.Fatalf("expected %d groups, got %d", safetyNumberGroups, len(groups))
	}
	valid := regexp.MustCompile(`^[0-9]{5}$`)
	for _, group := range groups {
		if !valid.MatchString(group) {
			t.Fatalf("invalid safety number group: %q", group)
		}
	}
}

func TestSafetyNumberDiffersPerPeer(t *testing.T) {
	h1, err := libp2p.New(libp2p.NoListenAddrs)
	if err != nil {
		t.Fatal(err)
	}
	defer h1.Close()

	h2, err := libp2p.New(libp2p.NoListenAddrs)
	if err != nil {
		t.Fatal(err)
	}
	defer h2.Close()

	h3, err := libp2p.New(libp2p.NoListenAddrs)
	if err != nil {
		t.Fatal(err)
	}
	defer h3.Close()

	exchangePublicKeys(t, h1, h2, h3)

	withTwo, err := safetyNumber(h1.Peerstore(), h1.ID(), h2.ID())
	if err != nil {
		t.Fatal(err)
	}
	withThree, err := safetyNumber(h1.Peerstore(), h1.ID(), h3.ID())
	if err != nil {
		t.Fatal(err)
	}
	if withTwo == withThree {
		t.Fatal("different peers produced the same safety number")
	}
}

func TestSafetyNumberNeedsPublicKey(t *testing.T) {
	h1, err := libp2p.New(libp2p.NoListenAddrs)
	if err != nil {
		t.Fatal(err)
	}
	defer h1.Close()

	missing := peer.ID("missing-peer-key")
	if _, err := safetyNumber(h1.Peerstore(), h1.ID(), missing); err == nil {
		t.Fatal("expected an error when the remote public key is missing")
	}
}
