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
	"strings"
	"sync"
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
)

const DISCOVERY_TIME = 5               // Discovery time threshold in seconds
const ChatProtocolID = "/p2pchat/0.75" // Custom chat protocol ID
const NickPrefix = "__NICKNAME__:"     // control message for nicknames

func colorize(color int, text string) string {
	return fmt.Sprintf("\x1b[%dm%s\x1b[0m", color, text)
}

type StreamManager struct {
	streams   map[peerstore.ID]network.Stream
	activeID  peerstore.ID
	mu        sync.RWMutex
	inputChan chan string
	node      host.Host
	ctx       context.Context

	dht      *discovery.DHTService // Handles DHT peer discovery
	sessions map[peerstore.ID]*security.Session

	localNick string
	peerNicks map[peerstore.ID]string
}

func newStreamManager(node host.Host, ctx context.Context) *StreamManager {
	return &StreamManager{
		streams:   make(map[peerstore.ID]network.Stream),
		inputChan: make(chan string, 100),
		node:      node,
		ctx:       ctx,

		sessions:  make(map[peerstore.ID]*security.Session),
		peerNicks: make(map[peerstore.ID]string),
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

	// WEB: Notify web interface about peer update
	NotifyWebPeerUpdate()
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

			// WEB: Notify web interface about peer disconnect
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

				// print only when nickname is first learned or changed
				if old != nick {
					shortID := peerID.String()
					if len(shortID) > 8 {
						shortID = shortID[len(shortID)-8:]
					}

					fmt.Printf(
						"Peer [%s] is now known as [%s]\n",
						colorize(96, shortID),
						colorize(92, nick),
					)

					// WEB: Notify web interface about nickname update
					NotifyWebPeerUpdate()
				}
			}
			continue
		}

		// Display nickname if known
		sm.mu.RLock()
		display := short
		if n, ok := sm.peerNicks[peerID]; ok {
			display = n
		}
		sm.mu.RUnlock()

		coloredID := colorize(96, display)
		coloredTime := colorize(93, time.Now().Format("15:04"))
		fmt.Printf("[%s] [%s]: %s\n", coloredTime, coloredID, msg)

		// WEB: Notify web interface about new message
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
}

func (sm *StreamManager) handleCommand(cmd string) {
	parts := strings.Fields(cmd)
	if len(parts) == 0 {
		return
	}

	command := parts[0]

	switch command {
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
	case "/discover":
		sm.discoverPeers()
	case "/help":
		sm.showHelp()
	default:
		fmt.Printf("Unknown command <%s>.\n'/help' for commands.\n", command)
	}
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

	fmt.Println(colorize(93, "/list") + "              - Show all connected peers")
	fmt.Println(colorize(93, "/switch <number>") + "   - Switch active peer (use number from /list)")
	fmt.Println(colorize(93, "/connect <addr>") + "    - Connect to a new peer")
	fmt.Println(colorize(93, "/discover") + "          - Discover peers on the network")
	fmt.Println(colorize(93, "/help") + "              - Show this help message")

	fmt.Println(colorize(94, "----------------------"))
	fmt.Printf("Type any message (without %s) to send to active peer\n\n", colorize(93, "/"))
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

	sm.AddStream(s)

	short := peerInfo.ID.String()
	if len(short) > 8 {
		short = short[len(short)-8:]
	}

	nick, shortID := sm.displayName(peerInfo.ID)

	if nick != "" {
		fmt.Printf("Successfully connected to [%s] [%s]\n", colorize(92, nick), colorize(96, shortID))
	} else {
		fmt.Printf("Successfully connected to peer [%s] (nickname pending)\n", colorize(96, shortID))
	}
}

// discoverPeers queries the DHT and prints discovered peers.
func (sm *StreamManager) discoverPeers() {
	if sm.dht == nil {
		fmt.Println(colorize(91, "DHT not initialized"))
		return
	}

	fmt.Printf(colorize(94, "Discovering peers on the network... %s\n"), colorize(93, fmt.Sprintf("Threshold is '%d' seconds", DISCOVERY_TIME)))

	// Cap discovery time so the prompt stays responsive.
	ctx, cancel := context.WithTimeout(sm.ctx, 30*time.Second)
	defer cancel()

	peerChan, err := sm.dht.DiscoverPeers(ctx, discovery.DefaultNamespace)
	if err != nil {
		fmt.Printf(colorize(91, "Discovery failed: %v\n"), err)
		return
	}

	discoveredCount := 0

	// Stop printing after a short window.
	timeout := time.After(DISCOVERY_TIME * time.Second)

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
				address := fmt.Sprintf(
					"%s/p2p/%s", peer.Addrs[0].String(), peer.PeerID.String(),
				)

				fmt.Printf("[%d] %s\n", discoveredCount, colorize(96, address))
			}
		}
	}

	fmt.Println("-------------------")
	if discoveredCount == 0 {
		fmt.Println("No peers found. Make sure other instances are running and advertising.")
	} else {
		fmt.Printf("Found %s peers. Use '%s' to connect.\n", colorize(92, fmt.Sprintf("%d", discoveredCount)), colorize(93, "/connect <address>"))
	}
	fmt.Println()
}

func getIdentityPath(customName string) (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}

	configDir := filepath.Join(home, ".p2pchat")

	err = os.MkdirAll(configDir, 0700)
	if err != nil {
		return "", err
	}

	filename := "identity.key"
	if customName != "" {
		filename = fmt.Sprintf("identity_%s.key", customName)
	}

	return filepath.Join(configDir, filename), nil
}

func loadOrGenerateKey(customName string) (crypto.PrivKey, error) {
	keyPath, err := getIdentityPath(customName)
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

	coloredIdentityLabel := colorize(94, identityLabel)
	fmt.Printf("No existing identity found for '%s'. Generating new identity...\n", coloredIdentityLabel)
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

// CreateNode sets up a libp2p host and registers the chat stream handler.
func CreateNode(sm *StreamManager, identityName string) (host.Host, error) {
	priv, err := loadOrGenerateKey(identityName)
	if err != nil {
		return nil, fmt.Errorf("failed to load identity: %w", err)
	}

	listenAddr := "/ip4/0.0.0.0/tcp/0"

	node, err := libp2p.New(
		libp2p.Identity(priv),
		libp2p.ListenAddrStrings(listenAddr),
	)
	if err != nil {
		return nil, err
	}

	node.SetStreamHandler(ChatProtocolID, func(s network.Stream) {

		coloredTime := colorize(93, time.Now().Format("15:04:05.000"))
		shortID := s.Conn().RemotePeer().String()
		if len(shortID) > 8 {
			shortID = shortID[len(shortID)-8:]
		}
		fmt.Printf("Incoming connection from peer [%s] at [%s]\n", colorize(96, shortID), coloredTime)

		sm.AddStream(s)
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

	coloredID := colorize(93, short)
	coloredTime := colorize(93, time.Now().Format("15:04:05.000"))
	fmt.Printf("Connected to '%s' at [%s]\n", coloredID, coloredTime)
	return *peer, nil
}

func waitForExitSignal() {
	ch := make(chan os.Signal, 1)
	signal.Notify(ch, syscall.SIGINT, syscall.SIGTERM)
	<-ch
	fmt.Println("Received signal, shutting down...")
}

func main() {
	identityName := flag.String("identity", "", "Identity name for testing")
	flag.Parse()

	ctx := context.Background()

	sm := newStreamManager(nil, ctx)
	sm.localNick = *identityName
	if sm.localNick == "" {
		sm.localNick = "anon"
	}

	node, err := CreateNode(sm, *identityName)
	if err != nil {
		fmt.Printf("Failed to create node: %v\n", err)
		return
	}
	defer node.Close()

	sm.node = node

	// WEB: Start web server
	go StartWebServer(sm)

	// Initialize discovery via the DHT.
	fmt.Println(colorize(95, "Initializing DHT..."))
	dhtService, err := discovery.NewDHTService(ctx, node)
	if err != nil {
		fmt.Printf("Failed to create DHT: %v\n", err)
		return
	}
	defer dhtService.Close()

	sm.dht = dhtService

	// Populate routing table before advertising.
	if err := dhtService.Bootstrap(); err != nil {
		fmt.Printf("Failed to bootstrap DHT: %v\n", err)
		return
	}

	// Keep announcing presence while running.
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

	coloredIdentityLabel := colorize(94, identityLabel)
	fmt.Printf("\n======= %s =======\n", coloredIdentityLabel)
	fmt.Println("Listening Peer Address:")
	var coloredAddress = colorize(92, addrs[0].String())
	fmt.Println(coloredAddress)
	fmt.Println()

	args := flag.Args()
	if len(args) > 0 {
		addr := args[0]
		peer, err := ConnectToPeer(node, addr, ctx)
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

	fmt.Printf("%s. Use '%s' to find peers or '%s' for commands.\n", colorize(92, "Node Ready"), colorize(93, "/discover"), colorize(93, "/help"))
	waitForExitSignal()
}
