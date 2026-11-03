package security

import (
	"bufio"
	"crypto/rand"
	"encoding/base64"
	"net"
	"strings"
	"testing"
	"time"

	"golang.org/x/crypto/curve25519"
)

// pair performs a real on-the-wire handshake over loopback TCP.
func pair(t *testing.T) (*Session, *Session) {
	t.Helper()

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()

	type result struct {
		session *Session
		err     error
	}
	serverCh := make(chan result, 1)

	go func() {
		conn, err := ln.Accept()
		if err != nil {
			serverCh <- result{err: err}
			return
		}
		defer conn.Close()
		session, _, err := Handshake(conn)
		serverCh <- result{session: session, err: err}
	}()

	clientConn, err := net.Dial("tcp", ln.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	defer clientConn.Close()

	clientSession, _, err := Handshake(clientConn)
	if err != nil {
		t.Fatal(err)
	}

	server := <-serverCh
	if server.err != nil {
		t.Fatal(server.err)
	}

	return clientSession, server.session
}

func TestDirectionSeparation(t *testing.T) {
	alice, bob := pair(t)

	alicePacket, err := alice.Encrypt([]byte("from alice"))
	if err != nil {
		t.Fatal(err)
	}
	bobPacket, err := bob.Encrypt([]byte("from bob"))
	if err != nil {
		t.Fatal(err)
	}

	aliceNonce := strings.Split(alicePacket, ".")[0]
	bobNonce := strings.Split(bobPacket, ".")[0]
	if aliceNonce == bobNonce {
		t.Error("first packets use the same nonce across directions")
	}

	if _, err := alice.Decrypt(bobPacket); err != nil {
		t.Errorf("alice could not decrypt bob's packet: %v", err)
	}
	if _, err := bob.Decrypt(alicePacket); err != nil {
		t.Errorf("bob could not decrypt alice's packet: %v", err)
	}
	if _, err := alice.Decrypt(alicePacket); err == nil {
		t.Error("alice accepted her own outgoing packet (reflection)")
	}
	if _, err := bob.Decrypt(bobPacket); err == nil {
		t.Error("bob accepted his own outgoing packet (reflection)")
	}
}

func TestReflectionRejected(t *testing.T) {
	alice, bob := pair(t)

	packet, err := alice.Encrypt([]byte("reflected"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := bob.Decrypt(packet); err != nil {
		t.Fatalf("bob could not decrypt the original packet: %v", err)
	}
	if _, err := alice.Decrypt(packet); err == nil {
		t.Error("alice accepted her own packet as incoming")
	}
}

func TestForgedPacketDoesNotBurnNonce(t *testing.T) {
	alice, bob := pair(t)

	realPacket, err := alice.Encrypt([]byte("real"))
	if err != nil {
		t.Fatal(err)
	}
	nonce := strings.Split(realPacket, ".")[0]
	forged := nonce + "." + base64.StdEncoding.EncodeToString(make([]byte, 40))

	if _, err := bob.Decrypt(forged); err == nil {
		t.Fatal("forged packet was accepted")
	}
	if _, err := bob.Decrypt(realPacket); err != nil {
		t.Errorf("real packet was rejected after forged packet: %v", err)
	}
}

func TestReplayRejected(t *testing.T) {
	alice, bob := pair(t)

	packet, err := alice.Encrypt([]byte("once"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := bob.Decrypt(packet); err != nil {
		t.Fatal(err)
	}
	if _, err := bob.Decrypt(packet); err == nil {
		t.Error("replayed packet was accepted")
	}
}

func TestOutOfOrderCounterRejected(t *testing.T) {
	alice, bob := pair(t)

	first, err := alice.Encrypt([]byte("first"))
	if err != nil {
		t.Fatal(err)
	}
	second, err := alice.Encrypt([]byte("second"))
	if err != nil {
		t.Fatal(err)
	}

	if _, err := bob.Decrypt(second); err != nil {
		t.Fatalf("second packet should be accepted first: %v", err)
	}
	if _, err := bob.Decrypt(first); err == nil {
		t.Error("older packet was accepted after a newer counter")
	}
}

// The handshake reader must be reused for the rest of the stream. Creating a
// second bufio.Reader can silently discard bytes that arrived together with
// the peer's public key.
func TestHandshakeReaderKeepsFollowingData(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()

	serverCh := make(chan error, 1)
	serverReaderCh := make(chan *bufio.Reader, 1)

	go func() {
		conn, err := ln.Accept()
		if err != nil {
			serverCh <- err
			return
		}
		defer conn.Close()

		_, reader, err := Handshake(conn)
		if err != nil {
			serverCh <- err
			return
		}
		serverReaderCh <- reader
		serverCh <- nil
	}()

	clientConn, err := net.Dial("tcp", ln.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	defer clientConn.Close()

	clientReader := bufio.NewReader(clientConn)
	if _, err := clientReader.ReadString('\n'); err != nil {
		t.Fatal(err)
	}

	var priv [32]byte
	if _, err := rand.Read(priv[:]); err != nil {
		t.Fatal(err)
	}
	pub, err := curve25519.X25519(priv[:], curve25519.Basepoint)
	if err != nil {
		t.Fatal(err)
	}

	payload := base64.StdEncoding.EncodeToString(pub) + "\nEXTRA\n"
	if _, err := clientConn.Write([]byte(payload)); err != nil {
		t.Fatal(err)
	}

	if err := <-serverCh; err != nil {
		t.Fatal(err)
	}
	reader := <-serverReaderCh

	_ = clientConn.SetReadDeadline(time.Now().Add(2 * time.Second))
	line, err := reader.ReadString('\n')
	if err != nil {
		t.Fatalf("handshake reader lost buffered data: %v", err)
	}
	if strings.TrimSpace(line) != "EXTRA" {
		t.Fatalf("unexpected buffered line: %q", line)
	}
}
