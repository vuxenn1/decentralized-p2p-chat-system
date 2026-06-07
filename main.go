package main

import (
	"bufio"
	"context"
	"crypto/rand"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"time"

	"github.com/libp2p/go-libp2p"
	crypto "github.com/libp2p/go-libp2p/core/crypto"
	"github.com/libp2p/go-libp2p/core/host"
	"github.com/libp2p/go-libp2p/core/network"
	peerstore "github.com/libp2p/go-libp2p/core/peer"
	multiaddr "github.com/multiformats/go-multiaddr"

	"p2p-chat/discovery"
	"p2p-chat/security"
	"p2p-chat/storage"
)

const DISCOVERY_TIME = 5
const ChatProtocolID = "/p2pchat/0.75"
const NickPrefix = "__NICKNAME__:"
const ConnReqPrefix = "__CONNREQ__:"
const ConnAcceptMsg = "__ACCEPT__"
const ConnRejectMsg = "__REJECT__"

var concurrentTestReceived int64

func colorize(color int, text string) string {
	return fmt.Sprintf("\x1b[%dm%s\x1b[0m", color, text)
}

func readLine(r io.Reader) (string, error) {
	var buf []byte
	b := make([]byte, 1)
	for {
		_, err := r.Read(b)
		if err != nil {
			return string(buf), err
		}
		if b[0] == '\n' {
			break
		}
		buf = append(buf, b[0])
	}
	return strings.TrimSpace(string(buf)), nil
}

type StreamManager struct {
	streams   map[peerstore.ID]network.Stream
	activeID  peerstore.ID
	mu        sync.RWMutex
	inputChan chan string
	node      host.Host
	ctx       context.Context

	dht      *discovery.DHTService
	sessions map[peerstore.ID]*security.Session

	localNick string
	peerNicks map[peerstore.ID]string

	historyStore *storage.HistoryStore
	peerStore    *storage.PeerStore

	pendingApprovals   map[peerstore.ID]chan bool
	pendingApprovalsMu sync.Mutex
	pendingRequestNums map[int]peerstore.ID
	nextRequestNum     int
}

func newStreamManager(node host.Host, ctx context.Context) *StreamManager {
	return &StreamManager{
		streams:            make(map[peerstore.ID]network.Stream),
		inputChan:          make(chan string, 100),
		node:               node,
		ctx:                ctx,
		sessions:           make(map[peerstore.ID]*security.Session),
		peerNicks:          make(map[peerstore.ID]string),
		pendingApprovals:   make(map[peerstore.ID]chan bool),
		pendingRequestNums: make(map[int]peerstore.ID),
	}
}

func (sm *StreamManager) getSortedPeers() []peerstore.ID {
	sm.mu.RLock()
	defer sm.mu.RUnlock()

	peers := make([]peerstore.ID, 0, len(sm.streams))
	for pid := range sm.streams {
		peers = append(peers, pid)
	}

	sort.Slice(peers, func(i, j int) bool {
		return peers[i].String() < peers[j].String()
	})

	return peers
}

func (sm *StreamManager) AddStream(s network.Stream) {
	peerID := s.Conn().RemotePeer()

	session, err := security.Handshake(s)
	if err != nil {
		_ = s.Close()
		return
	}

	sm.mu.Lock()
	sm.streams[peerID] = s
	sm.sessions[peerID] = session

	if sm.activeID == "" {
		sm.activeID = peerID
	}
	sm.mu.Unlock()

	go sm.readLoop(s)

	time.Sleep(10 * time.Millisecond)
	if sm.localNick != "" {
		enc, err := session.Encrypt([]byte(NickPrefix + sm.localNick))
		if err == nil {
			_, _ = s.Write([]byte(enc + "\n"))
		}
	}

	NotifyWebPeerUpdate()

	// Load and send history for this peer
	if sm.historyStore != nil {
		go func(pid peerstore.ID) {
			msg, err := sm.historyStore.LoadHistory(pid.String())
			if err == nil && len(msg) > 0 {
				NotifyPeerHistory(pid.String(), msg)
			}
		}(peerID)
	}
}

func (sm *StreamManager) HandleIncomingStream(s network.Stream) {
	peerID := s.Conn().RemotePeer()
	shortID := peerID.String()
	if len(shortID) > 8 {
		shortID = shortID[len(shortID)-8:]
	}

	line, err := readLine(s)
	if err != nil || !strings.HasPrefix(line, ConnReqPrefix) {
		_ = s.Close()
		return
	}

	nickname := strings.TrimPrefix(line, ConnReqPrefix)
	if nickname == "" {
		nickname = shortID
	}

	// Assign a request number
	sm.pendingApprovalsMu.Lock()
	sm.nextRequestNum++
	reqNum := sm.nextRequestNum
	ch := make(chan bool, 1)
	sm.pendingApprovals[peerID] = ch
	sm.pendingRequestNums[reqNum] = peerID
	sm.pendingApprovalsMu.Unlock()

	fmt.Printf("\nIncoming connection request (%d) from \"%s\" [%s]\n", reqNum, colorize(92, nickname), colorize(96, shortID))
	fmt.Printf("Type '%s' or '%s'\n\n", colorize(93, fmt.Sprintf("/accept %d", reqNum)), colorize(91, fmt.Sprintf("/reject %d", reqNum)))

	NotifyConnectionRequest(peerID.String(), shortID, nickname)

	var accepted bool
	select {
	case accepted = <-ch:
	case <-time.After(30 * time.Second):
		accepted = false
		fmt.Printf("Connection request (%d) from \"%s\" timed out\n", reqNum, nickname)
	}

	if accepted {
		_, _ = s.Write([]byte(ConnAcceptMsg + "\n"))
		sm.AddStream(s)
	} else {
		_, _ = s.Write([]byte(ConnRejectMsg + "\n"))
		fmt.Printf("Connection rejected from \"%s\" [%s]\n", colorize(91, nickname), colorize(96, shortID))
		_ = s.Close()
	}

	// Cleanup
	sm.pendingApprovalsMu.Lock()
	delete(sm.pendingApprovals, peerID)
	delete(sm.pendingRequestNums, reqNum)
	if len(sm.pendingRequestNums) == 0 {
		sm.nextRequestNum = 0
	}
	sm.pendingApprovalsMu.Unlock()
}

func (sm *StreamManager) RespondToConnectionRequest(peerIDStr string, accepted bool) {
	peerID, err := peerstore.Decode(peerIDStr)
	if err != nil {
		return
	}
	sm.pendingApprovalsMu.Lock()
	ch, ok := sm.pendingApprovals[peerID]
	sm.pendingApprovalsMu.Unlock()
	if ok {
		ch <- accepted
	}
}

func (sm *StreamManager) RespondToConnectionRequestByNum(num int, accepted bool) {
	sm.pendingApprovalsMu.Lock()
	peerID, ok := sm.pendingRequestNums[num]
	sm.pendingApprovalsMu.Unlock()
	if !ok {
		fmt.Printf("No pending request with number %d\n", num)
		return
	}

	sm.pendingApprovalsMu.Lock()
	ch, ok := sm.pendingApprovals[peerID]
	sm.pendingApprovalsMu.Unlock()
	if ok {
		ch <- accepted
	}
}

func (sm *StreamManager) readLoop(s network.Stream) {
	peerID := s.Conn().RemotePeer()
	remote := peerID.String()
	short := remote
	if len(remote) > 8 {
		short = remote[len(remote)-8:]
	}

	r := bufio.NewReader(s)
	for {
		line, err := r.ReadString('\n')
		if err != nil {
			if err == io.EOF {
				nick, shortID := sm.displayName(peerID)
				if nick != "" {
					fmt.Printf("Connection closed by [%s] [%s]\n", colorize(91, nick), colorize(96, shortID))
				} else {
					fmt.Printf("Connection closed by [%s]\n", colorize(96, shortID))
				}
			} else {
				fmt.Printf("Error reading from [%s]: %v\n", short, err)
			}

			sm.mu.Lock()
			delete(sm.streams, peerID)
			delete(sm.sessions, peerID)
			if sm.activeID == peerID {
				sm.activeID = ""
			}
			sm.mu.Unlock()

			NotifyWebPeerUpdate()
			_ = s.Close()
			return
		}

		sm.mu.RLock()
		session := sm.sessions[peerID]
		sm.mu.RUnlock()

		plain, err := session.Decrypt(strings.TrimSpace(line))
		if err != nil {
			continue
		}

		msg := string(plain)

		if strings.HasPrefix(msg, NickPrefix) {
			nick := strings.TrimSpace(strings.TrimPrefix(msg, NickPrefix))
			if nick != "" {
				sm.mu.Lock()
				old := sm.peerNicks[peerID]
				sm.peerNicks[peerID] = nick
				sm.mu.Unlock()

				if old != nick {
					shortID := peerID.String()
					if len(shortID) > 8 {
						shortID = shortID[len(shortID)-8:]
					}
					fmt.Printf("Peer [%s] is now known as [%s]\n",
						colorize(96, shortID), colorize(92, nick))
					NotifyWebPeerUpdate()

					sm.mu.RLock()
					nick := sm.peerNicks[peerID]
					sm.mu.RUnlock()
					NotifyPeerDisconnected(peerID.String(), nick, short)
				}
			}
			continue
		}

		sm.mu.RLock()
		display := short
		if n, ok := sm.peerNicks[peerID]; ok {
			display = n
		}
		sm.mu.RUnlock()

		// Message Latency Tests
		if strings.HasPrefix(msg, "LATENCY_PING:") {
			// Receiver side: echo back immediately
			parts := strings.SplitN(msg, ":", 3)
			if len(parts) >= 2 {
				pong := fmt.Sprintf("LATENCY_PONG:%s", parts[1])
				enc, err := session.Encrypt([]byte(pong))
				if err == nil {
					s.Write([]byte(enc + "\n"))
				}
			}
		} else if strings.HasPrefix(msg, "LATENCY_PONG:") {
			// Sender side: compute RTT/2
			parts := strings.SplitN(msg, ":", 2)
			if len(parts) == 2 {
				sentMs, err := strconv.ParseInt(parts[1], 10, 64)
				if err == nil {
					rtt := time.Now().UnixMilli() - sentMs
					fmt.Printf("LATENCY_RESULT:%d\n", rtt/2)
				}
			}
			// Concurrent Connection Tests
		} else if strings.HasPrefix(msg, "CONCURRENT_TEST:") {
			atomic.AddInt64(&concurrentTestReceived, 1)
			fmt.Printf("CONCURRENT_RECEIVED:%d\n", atomic.LoadInt64(&concurrentTestReceived))
		} else {
			coloredID := colorize(96, display)
			coloredTime := colorize(93, time.Now().Format("15:04"))
			fmt.Printf("[%s] [%s]: %s\n", coloredTime, coloredID, msg)
		}

		NotifyWebMessage(peerID.String(), display, msg, false)
	}
}

func (sm *StreamManager) displayName(peerID peerstore.ID) (nick string, shortID string) {
	shortID = peerID.String()
	if len(shortID) > 8 {
		shortID = shortID[len(shortID)-8:]
	}
	sm.mu.RLock()
	nick = sm.peerNicks[peerID]
	sm.mu.RUnlock()
	return nick, shortID
}

func (sm *StreamManager) HandleInput() {
	scanner := bufio.NewScanner(os.Stdin)
	for scanner.Scan() {
		text := scanner.Text()
		if len(text) > 0 && text[0] == '/' {
			sm.handleCommand(text)
			continue
		}

		sm.mu.RLock()
		activeID := sm.activeID
		stream, exists := sm.streams[activeID]
		session := sm.sessions[activeID]
		sm.mu.RUnlock()

		if !exists || activeID == "" {
			fmt.Println("No active peer, Use '/list' to see available connections.")
			continue
		}

		enc, err := session.Encrypt([]byte(text))
		if err != nil {
			fmt.Printf("Failed to encrypt message: %v\n", colorize(91, err.Error()))
			continue
		}

		_, err = stream.Write([]byte(enc + "\n"))
		if err == nil && sm.historyStore != nil {
			go sm.historyStore.SaveMessage(activeID.String(), storage.StoredMsg{
				ID:        fmt.Sprintf("%d", time.Now().UnixNano()),
				From:      sm.node.ID().String(),
				FromName:  sm.localNick,
				Content:   text,
				Timestamp: time.Now(),
				IsOwn:     true,
			})
		}
		if err != nil {
			fmt.Printf("Error, message sending to peer: %v\n", err)
			sm.mu.Lock()
			delete(sm.streams, activeID)
			delete(sm.sessions, activeID)
			if sm.activeID == activeID {
				sm.activeID = ""
			}
			sm.mu.Unlock()
		}
	}

	if err := scanner.Err(); err != nil {
		fmt.Printf("Input error: %v\n", err)
	}
}

func (sm *StreamManager) handleCommand(cmd string) {
	parts := strings.Fields(cmd)
	if len(parts) == 0 {
		return
	}

	switch parts[0] {
	case "/list":
		sm.listPeers()
	case "/switch":
		if len(parts) < 2 {
			fmt.Println("Usage: '/switch <peer_number>'")
			return
		}
		sm.switchPeer(parts[1])
	case "/connect":
		if len(parts) < 2 {
			fmt.Println("Usage: '/connect <multiaddr>'")
			return
		}
		sm.connectToPeer(parts[1])
	case "/disconnect":
		sm.disconnectActivePeer()
	case "/discover":
		sm.discoverPeers()
	case "/accept":
		if len(parts) < 2 {
			fmt.Println("Usage: '/accept <number>'")
			return
		}
		num := 0
		fmt.Sscanf(parts[1], "%d", &num)
		if num == 0 {
			fmt.Println("Invalid request number")
			return
		}
		sm.RespondToConnectionRequestByNum(num, true)
	case "/reject":
		if len(parts) < 2 {
			fmt.Println("Usage: '/reject <number>'")
			return
		}
		num := 0
		fmt.Sscanf(parts[1], "%d", &num)
		if num == 0 {
			fmt.Println("Invalid request number")
			return
		}
		sm.RespondToConnectionRequestByNum(num, false)
	case "/peers":
		sm.listSavedPeers()
	case "/save":
		if len(parts) < 2 {
			fmt.Println("Usage: '/save <fullpeerid> <customnick>'")
			return
		}
		customNick := ""
		if len(parts) >= 3 {
			customNick = parts[2]
		}
		sm.savePeer(parts[1], customNick)
	case "/rename":
		if len(parts) < 3 {
			fmt.Println("Usage: '/rename <number> <newnick>'")
			return
		}
		num := 0
		fmt.Sscanf(parts[1], "%d", &num)
		if num == 0 {
			fmt.Println("Invalid number")
			return
		}
		sm.renameSavedPeer(num, parts[2])
	case "/remove":
		if len(parts) < 2 {
			fmt.Println("Usage: '/remove <number>'")
			return
		}
		num := 0
		fmt.Sscanf(parts[1], "%d", &num)
		if num == 0 {
			fmt.Println("Invalid number")
			return
		}
		sm.removeSavedPeer(num)
	case "/latencytest":
		if len(parts) < 2 {
			fmt.Println("Usage: '/latencytest <small|medium|large>'")
			return
		}
		go sm.runLatencyTest(parts[1])
	case "/concurrenttest":
		go sm.runConcurrentTest()
	case "/help":
		sm.showHelp()
	default:
		fmt.Printf("Unknown command <%s>.\n'/help' for commands.\n", parts[0])
	}
}

func (sm *StreamManager) runLatencyTest(size string) {
	sm.mu.RLock()
	activeID := sm.activeID
	stream, exists := sm.streams[activeID]
	session := sm.sessions[activeID]
	sm.mu.RUnlock()

	if !exists || activeID == "" {
		fmt.Println("No active peer for latency test")
		return
	}

	msgBody := ""
	msgSize := 10
	switch size {
	case "small":
		msgBody = strings.Repeat("a", msgSize)
	case "medium":
		msgBody = strings.Repeat("a", msgSize*10)
	case "large":
		msgBody = strings.Repeat("a", msgSize*100)
	default:
		fmt.Println("Unknown size. Use: small, medium, large")
		return
	}

	fmt.Printf("Starting latency test [%s] — 100 messages...\n", size)

	for i := 1; i <= 100; i++ {
		ts := time.Now().UnixMilli()
		msg := fmt.Sprintf("LATENCY_PING:%d:%s", ts, msgBody)
		enc, err := session.Encrypt([]byte(msg))
		if err != nil {
			continue
		}
		stream.Write([]byte(enc + "\n"))
		time.Sleep(50 * time.Millisecond)
	}

	fmt.Printf("Latency test [%s] complete.\n", size)
}

func (sm *StreamManager) runConcurrentTest() {
	sm.mu.RLock()
	peers := make([]peerstore.ID, 0)
	for pid := range sm.streams {
		peers = append(peers, pid)
	}
	sm.mu.RUnlock()

	if len(peers) == 0 {
		fmt.Println("No connected peers for concurrent test")
		return
	}

	fmt.Printf("Starting concurrent test — %d peers, 20 messages each...\n", len(peers))

	var wg sync.WaitGroup
	for _, pid := range peers {
		wg.Add(1)
		go func(peerID peerstore.ID) {
			defer wg.Done()

			sm.mu.RLock()
			stream := sm.streams[peerID]
			session := sm.sessions[peerID]
			sm.mu.RUnlock()

			nick, _ := sm.displayName(peerID)

			for i := 1; i <= 20; i++ {
				msg := fmt.Sprintf("CONCURRENT_TEST:%s:%d", nick, i)
				enc, err := session.Encrypt([]byte(msg))
				if err != nil {
					continue
				}
				stream.Write([]byte(enc + "\n"))
				time.Sleep(10 * time.Millisecond)
			}
		}(pid)
	}

	wg.Wait()
	fmt.Printf("\nConcurrent test complete. Sent %d total messages.\n",
		len(peers)*20)
}

func (sm *StreamManager) listPeers() {
	peers := sm.getSortedPeers()
	if len(peers) == 0 {
		fmt.Println("No connected peers")
		return
	}

	fmt.Println("\nConnected Peers")
	fmt.Println("-------------------")
	for i, peerID := range peers {
		activeMarker := " "
		if peerID == sm.activeID {
			activeMarker = ">"
		}
		nick, shortID := sm.displayName(peerID)
		name := colorize(96, shortID)
		if nick != "" {
			name = fmt.Sprintf("%s [%s]", colorize(94, nick), colorize(93, shortID))
		}
		fmt.Printf("%s [%d] %s\n", activeMarker, i+1, name)
	}
	fmt.Println("-------------------")
	fmt.Println("Use /switch <number> to change active peer")
	fmt.Println()
}

func (sm *StreamManager) switchPeer(arg string) {
	peers := sm.getSortedPeers()
	num := 0
	_, err := fmt.Sscanf(arg, "%d", &num)
	if err != nil || num < 1 || num > len(peers) {
		fmt.Println("Invalid peer number")
		return
	}

	sm.mu.Lock()
	sm.activeID = peers[num-1]
	sm.mu.Unlock()

	nick, shortID := sm.displayName(sm.activeID)
	if nick != "" {
		fmt.Printf("Switched to peer: [%s] [%s]\n", colorize(92, nick), colorize(96, shortID))
	} else {
		fmt.Printf("Switched to peer: [%s]\n", colorize(96, shortID))
	}
}

func (sm *StreamManager) showHelp() {
	fmt.Println()
	fmt.Println(colorize(94, "Available Commands:"))
	fmt.Println(colorize(94, "----------------------"))
	fmt.Println(colorize(93, "/list") + "               - Show all connected peers")
	fmt.Println(colorize(93, "/switch <number>") + "    - Switch active peer")
	fmt.Println(colorize(93, "/connect <addr>") + "     - Connect to a new peer")
	fmt.Println(colorize(93, "/disconnect") + "          - Disconnect from active peer")
	fmt.Println(colorize(93, "/discover") + "           - Discover peers on the network")
	fmt.Println(colorize(93, "/accept <number>") + "    - Accept a connection request")
	fmt.Println(colorize(93, "/reject <number>") + "    - Reject a connection request")
	fmt.Println(colorize(93, "/peers") + "               - List saved peers")
	fmt.Println(colorize(93, "/save <peerid> <nick>") + "  - Save a peer with custom nick")
	fmt.Println(colorize(93, "/rename <number> <nick>") + " - Rename a saved peer")
	fmt.Println(colorize(93, "/remove <number>") + "     - Remove a saved peer")
	fmt.Println(colorize(93, "/help") + "               - Show this help message")
	fmt.Println(colorize(94, "----------------------"))
	fmt.Printf("Type any message (without %s) to send to active peer\n\n", colorize(93, "/"))
}

func (sm *StreamManager) disconnectActivePeer() {
	sm.mu.RLock()
	activeID := sm.activeID
	stream, exists := sm.streams[activeID]
	sm.mu.RUnlock()

	if !exists || activeID == "" {
		fmt.Println("No active peer to disconnect")
		return
	}

	nick, shortID := sm.displayName(activeID)
	_ = stream.Close()

	if nick != "" {
		fmt.Printf("Disconnected from [%s] [%s]\n", colorize(91, nick), colorize(96, shortID))
	} else {
		fmt.Printf("Disconnected from [%s]\n", colorize(96, shortID))
	}
}

func (sm *StreamManager) disconnectSpecificPeer(peerIDStr string) {
	peerID, err := peerstore.Decode(peerIDStr)
	if err != nil {
		return
	}
	sm.mu.RLock()
	stream, exists := sm.streams[peerID]
	sm.mu.RUnlock()
	if !exists {
		fmt.Println("Peer not connected")
		return
	}
	nick, shortID := sm.displayName(peerID)
	_ = stream.Close()
	if nick != "" {
		fmt.Printf("Disconnected from [%s] [%s]\n", colorize(91, nick), colorize(96, shortID))
	} else {
		fmt.Printf("Disconnected from [%s]\n", colorize(96, shortID))
	}
}

func (sm *StreamManager) ReconnectPeer(peerIDStr string) {
	if sm.dht == nil {
		NotifyReconnectStatus(peerIDStr, "failed", "DHT başlatılmamış")
		return
	}

	peerID, err := peerstore.Decode(peerIDStr)
	if err != nil {
		NotifyReconnectStatus(peerIDStr, "failed", "Geçersiz peer ID")
		return
	}

	sm.mu.RLock()
	_, alreadyConnected := sm.streams[peerID]
	sm.mu.RUnlock()
	if alreadyConnected {
		NotifyReconnectStatus(peerIDStr, "connected", "Zaten bağlı")
		return
	}

	NotifyReconnectStatus(peerIDStr, "searching", "Peer aranıyor...")

	peerInfo, err := sm.dht.FindPeer(peerID)
	if err != nil || len(peerInfo.Addrs) == 0 {
		NotifyReconnectStatus(peerIDStr, "failed", "Peer ağda bulunamadı")
		return
	}

	addrs, err := peerstore.AddrInfoToP2pAddrs(&peerInfo)
	if err != nil || len(addrs) == 0 {
		NotifyReconnectStatus(peerIDStr, "failed", "Peer adresi alınamadı")
		return
	}

	NotifyReconnectStatus(peerIDStr, "connecting", "Bağlanılıyor...")
	sm.connectToPeer(addrs[0].String())
}

func (sm *StreamManager) listSavedPeers() {
	if sm.peerStore == nil {
		fmt.Println("Peer store not available")
		return
	}
	peers, err := sm.peerStore.LoadPeers()
	if err != nil || len(peers) == 0 {
		fmt.Println("No saved peers.")
		return
	}
	fmt.Println("\nSaved Peers:")
	fmt.Println("-------------------")
	for i, p := range peers {
		short := p.PeerID
		if len(short) > 8 {
			short = short[len(short)-8:]
		}
		fmt.Printf("[%d] \"%s\" [%s]\n", i+1,
			colorize(92, p.Nickname),
			colorize(96, short))
	}
	fmt.Println("-------------------")
	fmt.Println()
}

func (sm *StreamManager) savePeer(peerID string, customNick string) {
	if sm.peerStore == nil {
		fmt.Println("Peer store not available")
		return
	}
	pid, err := peerstore.Decode(peerID)
	if err != nil {
		fmt.Printf("Invalid peer ID: %v\n", err)
		return
	}
	_ = pid

	nick := customNick
	if nick == "" {
		// fallback to known nickname
		pid2, _ := peerstore.Decode(peerID)
		sm.mu.RLock()
		nick = sm.peerNicks[pid2]
		sm.mu.RUnlock()
		if nick == "" {
			nick = peerID
			if len(nick) > 8 {
				nick = nick[len(nick)-8:]
			}
		}
	}

	if err := sm.peerStore.SavePeer(peerID, nick); err != nil {
		fmt.Printf("Failed to save peer: %v\n", err)
		return
	}
	fmt.Printf("Saved \"%s\" to trusted peers.\n", colorize(92, nick))
	NotifyTrustedPeersUpdate(sm.peerStore)
}

func (sm *StreamManager) renameSavedPeer(num int, newNick string) {
	if sm.peerStore == nil {
		fmt.Println("Peer store not available")
		return
	}
	peers, err := sm.peerStore.LoadPeers()
	if err != nil || num < 1 || num > len(peers) {
		fmt.Printf("No saved peer with number %d\n", num)
		return
	}
	peer := peers[num-1]
	if err := sm.peerStore.SavePeer(peer.PeerID, newNick); err != nil {
		fmt.Printf("Failed to rename: %v\n", err)
		return
	}
	fmt.Printf("Renamed to \"%s\".\n", colorize(92, newNick))
	NotifyTrustedPeersUpdate(sm.peerStore)
}

func (sm *StreamManager) removeSavedPeer(num int) {
	if sm.peerStore == nil {
		fmt.Println("Peer store not available")
		return
	}
	removed, err := sm.peerStore.RemovePeerByNum(num)
	if err != nil {
		fmt.Printf("%v\n", err)
		return
	}
	fmt.Printf("Removed \"%s\" from saved peers.\n", colorize(91, removed.Nickname))
	NotifyTrustedPeersUpdate(sm.peerStore)
}

func (sm *StreamManager) connectToPeer(address string) {
	addr, err := multiaddr.NewMultiaddr(address)
	if err != nil {
		fmt.Printf("Invalid multiaddr: %v\n", err)
		return
	}

	peerInfo, err := peerstore.AddrInfoFromP2pAddr(addr)
	if err != nil {
		fmt.Printf("Invalid peer info: %v\n", err)
		return
	}

	sm.mu.RLock()
	_, alreadyConnected := sm.streams[peerInfo.ID]
	sm.mu.RUnlock()

	if alreadyConnected {
		fmt.Println("Already connected to this peer")
		return
	}

	fmt.Printf("Connecting to peer...\n")

	if err := sm.node.Connect(sm.ctx, *peerInfo); err != nil {
		fmt.Printf("Connection failed: %v\n", err)
		return
	}

	s, err := sm.node.NewStream(sm.ctx, peerInfo.ID, ChatProtocolID)
	if err != nil {
		fmt.Printf("Failed to open stream: %v\n", err)
		return
	}

	// Send connection request with our nickname
	_, err = s.Write([]byte(ConnReqPrefix + sm.localNick + "\n"))
	if err != nil {
		fmt.Printf("Failed to send connection request: %v\n", err)
		_ = s.Close()
		return
	}

	// Wait for accept/reject using goroutine timeout
	type lineResult struct {
		line string
		err  error
	}
	resultCh := make(chan lineResult, 1)
	go func() {
		line, err := readLine(s)
		resultCh <- lineResult{line, err}
	}()

	var response string
	select {
	case res := <-resultCh:
		if res.err != nil {
			fmt.Printf("No response from peer: %v\n", res.err)
			_ = s.Close()
			return
		}
		response = res.line
	case <-time.After(35 * time.Second):
		fmt.Printf("Connection request timed out\n")
		_ = s.Close()
		return
	}

	if response != ConnAcceptMsg {
		fmt.Printf("Connection rejected by peer\n")
		NotifyWebConnectionRejected(peerInfo.ID.String())
		_ = s.Close()
		return
	}

	fmt.Printf("Connection accepted! Establishing session...\n")
	sm.AddStream(s)

	nick, shortID := sm.displayName(peerInfo.ID)
	if nick != "" {
		fmt.Printf("Successfully connected to [%s] [%s]\n", colorize(92, nick), colorize(96, shortID))
	} else {
		fmt.Printf("Successfully connected to peer [%s] (nickname pending)\n", colorize(96, shortID))
	}
}

func (sm *StreamManager) discoverPeers() {
	if sm.dht == nil {
		fmt.Println(colorize(91, "DHT not initialized"))
		return
	}

	fmt.Printf(colorize(94, "Discovering peers on the network... %s\n"),
		colorize(93, fmt.Sprintf("Threshold is '%d' seconds", DISCOVERY_TIME)))

	ctx, cancel := context.WithTimeout(sm.ctx, 30*time.Second)
	defer cancel()

	peerChan, err := sm.dht.DiscoverPeers(ctx, discovery.DefaultNamespace)
	if err != nil {
		fmt.Printf(colorize(91, "Discovery failed: %v\n"), err)
		return
	}

	discoveredCount := 0
	timeout := time.After(DISCOVERY_TIME * time.Second)

	discoverStart := time.Now()

	fmt.Println("\nDiscovered Peers:")
	fmt.Println("-------------------")

loop:
	for {
		select {
		case <-timeout:
			break loop
		case <-ctx.Done():
			break loop
		case peer, ok := <-peerChan:
			if !ok {
				break loop
			}
			discoveredCount++
			if len(peer.Addrs) > 0 {
				address := fmt.Sprintf("%s/p2p/%s", peer.Addrs[0].String(), peer.PeerID.String())
				elapsed := time.Since(discoverStart)
				fmt.Printf("[%d] %s - in %dms\n",
					discoveredCount,
					colorize(96, address),
					elapsed.Milliseconds())
			}
		}
	}

	fmt.Println("-------------------")
	if discoveredCount == 0 {
		fmt.Println("No peers found. Make sure other instances are running and advertising.")
	} else {
		fmt.Printf("Found %s peers. Use '%s' to connect.\n",
			colorize(92, fmt.Sprintf("%d", discoveredCount)),
			colorize(93, "/connect <address>"))
	}
	fmt.Println()
}

func getIdentityPath(customName string, dataDir string) (string, error) {
	var configDir string
	if dataDir != "" {
		configDir = dataDir
	} else {
		home, err := os.UserHomeDir()
		if err != nil {
			return "", err
		}
		configDir = filepath.Join(home, ".p2pchat")
	}
	err := os.MkdirAll(configDir, 0700)
	if err != nil {
		return "", err
	}
	filename := "identity.key"
	if customName != "" {
		filename = fmt.Sprintf("identity_%s.key", customName)
	}
	return filepath.Join(configDir, filename), nil
}

func loadOrGenerateKey(customName string, dataDir string) (crypto.PrivKey, error) {
	keyPath, err := getIdentityPath(customName, dataDir)
	if err != nil {
		return nil, fmt.Errorf("failed to get identity path: %w", err)
	}
	if _, err := os.Stat(keyPath); err == nil {
		return loadKey(keyPath)
	}
	identityLabel := "default"
	if customName != "" {
		identityLabel = customName
	}
	fmt.Printf("No existing identity found for '%s'. Generating new identity...\n",
		colorize(94, identityLabel))
	return generateAndSaveKey(keyPath)
}

func generateAndSaveKey(keyPath string) (crypto.PrivKey, error) {
	priv, _, err := crypto.GenerateEd25519Key(rand.Reader)
	if err != nil {
		return nil, fmt.Errorf("failed to generate key: %w", err)
	}
	keyBytes, err := crypto.MarshalPrivateKey(priv)
	if err != nil {
		return nil, fmt.Errorf("failed to marshal key: %w", err)
	}
	err = os.WriteFile(keyPath, keyBytes, 0600)
	if err != nil {
		return nil, fmt.Errorf("failed to save key: %w", err)
	}
	fmt.Printf("New identity saved to: %s\n", colorize(96, keyPath))
	return priv, nil
}

func loadKey(keyPath string) (crypto.PrivKey, error) {
	keyBytes, err := os.ReadFile(keyPath)
	if err != nil {
		return nil, fmt.Errorf("failed to read key file: %w", err)
	}
	priv, err := crypto.UnmarshalPrivateKey(keyBytes)
	if err != nil {
		return nil, fmt.Errorf("failed to unmarshal key: %w", err)
	}
	fmt.Printf("Loaded existing identity from: %s\n", colorize(96, keyPath))
	return priv, nil
}

func CreateNode(sm *StreamManager, identityName string, dataDir string, listenIP string) (host.Host, error) {
	priv, err := loadOrGenerateKey(identityName, dataDir)
	if err != nil {
		return nil, fmt.Errorf("failed to load identity: %w", err)
	}

	ip := "0.0.0.0"
	if listenIP != "" {
		ip = listenIP
	}

	node, err := libp2p.New(
		libp2p.Identity(priv),
		libp2p.ListenAddrStrings(fmt.Sprintf("/ip4/%s/tcp/0", ip)),
	)
	if err != nil {
		return nil, err
	}

	node.SetStreamHandler(ChatProtocolID, func(s network.Stream) {
		go sm.HandleIncomingStream(s)
	})

	return node, nil
}

func ConnectToPeer(node host.Host, address string, ctx context.Context) (peerstore.AddrInfo, error) {
	addr, err := multiaddr.NewMultiaddr(address)
	if err != nil {
		return peerstore.AddrInfo{}, fmt.Errorf("invalid multiaddr: %w", err)
	}
	peer, err := peerstore.AddrInfoFromP2pAddr(addr)
	if err != nil {
		return peerstore.AddrInfo{}, fmt.Errorf("invalid peer info: %w", err)
	}
	if err := node.Connect(ctx, *peer); err != nil {
		return *peer, fmt.Errorf("connection failed: %w", err)
	}
	short := peer.ID.String()
	if len(short) > 8 {
		short = short[len(short)-8:]
	}
	fmt.Printf("Connected to '%s' at [%s]\n",
		colorize(93, short),
		colorize(93, time.Now().Format("15:04:05.000")))
	return *peer, nil
}

func waitForExitSignal() {
	ch := make(chan os.Signal, 1)
	signal.Notify(ch, syscall.SIGINT, syscall.SIGTERM)
	<-ch
	stopMessage := colorize(91, "\nReceived exit signal, shutting down...\n")
	fmt.Println(stopMessage)
}

func main() {
	identityName := flag.String("identity", "", "Identity name for testing")
	dataDir := flag.String("datadir", "", "Data directory for storage (used on Android)")
	listenIP := flag.String("listenip", "", "Specific IP to listen on (used on Android for LAN)")
	flag.Parse()

	resolvedDataDir := *dataDir
	if resolvedDataDir == "" {
		home, err := os.UserHomeDir()
		if err == nil {
			resolvedDataDir = filepath.Join(home, ".p2pchat")
		}
	}

	ctx := context.Background()

	sm := newStreamManager(nil, ctx)
	sm.localNick = *identityName
	if sm.localNick == "" {
		sm.localNick = "anon"
	}

	node, err := CreateNode(sm, *identityName, *dataDir, *listenIP)
	if err != nil {
		fmt.Printf("Failed to create node: %v\n", err)
		return
	}
	defer node.Close()

	sm.node = node

	privKeyBytes, pkErr := node.Peerstore().PrivKey(node.ID()).Raw()
	if pkErr == nil {
		dir := resolvedDataDir
		if dir == "" {
			home, _ := os.UserHomeDir()
			dir = filepath.Join(home, ".p2pchat")
		}

		ps, psErr := storage.NewPeerStore(dir)
		if psErr == nil {
			sm.peerStore = ps
		}

		hs, hsErr := storage.NewHistoryStore(dir, privKeyBytes)
		if hsErr == nil {
			sm.historyStore = hs
			fmt.Println(colorize(95, "Message history storage initialized."))
		}
	}

	go StartWebServer(sm)

	fmt.Println(colorize(95, "Initializing DHT..."))
	dhtService, err := discovery.NewDHTService(ctx, node)
	if err != nil {
		fmt.Printf("Failed to create DHT: %v\n", err)
		return
	}
	defer dhtService.Close()

	sm.dht = dhtService

	if err := dhtService.Bootstrap(); err != nil {
		fmt.Printf("Failed to bootstrap DHT: %v\n", err)
		return
	}

	go dhtService.AdvertiseContinuously(discovery.DefaultNamespace)

	info := peerstore.AddrInfo{
		ID:    node.ID(),
		Addrs: node.Addrs(),
	}

	addrs, err := peerstore.AddrInfoToP2pAddrs(&info)
	if err != nil {
		panic(err)
	}

	identityLabel := "default"
	if *identityName != "" {
		identityLabel = *identityName
	}

	fmt.Printf("\n======= %s =======\n", colorize(94, identityLabel))
	fmt.Println("Listening Peer Address:")
	fmt.Println(colorize(92, addrs[0].String()))
	fmt.Println()

	args := flag.Args()
	if len(args) > 0 {
		peer, err := ConnectToPeer(node, args[0], ctx)
		if err != nil {
			panic(err)
		}
		s, err := node.NewStream(ctx, peer.ID, ChatProtocolID)
		if err != nil {
			fmt.Println("Failed to open chat stream:", err)
			return
		}
		sm.AddStream(s)
		fmt.Printf("Chat started. Type your messages or use %s for commands\n", colorize(93, "/help"))
	}

	go sm.HandleInput()

	fmt.Printf("%s. Use '%s' to find peers or '%s' for commands.\n",
		colorize(92, "Node Ready"),
		colorize(93, "/discover"),
		colorize(93, "/help"))
	waitForExitSignal()
}
