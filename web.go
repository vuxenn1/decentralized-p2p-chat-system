package main

import (
	"crypto/rand"
	"crypto/subtle"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"sync"
	"sync/atomic"
	"time"

	"github.com/gorilla/websocket"
	peerstore "github.com/libp2p/go-libp2p/core/peer"
	multiaddr "github.com/multiformats/go-multiaddr"

	"p2p-chat/discovery"
	"p2p-chat/storage"
)

var wsUpgrader = websocket.Upgrader{
	CheckOrigin: func(r *http.Request) bool {
		return false
	},
}

type WebClient struct {
	sm              *StreamManager
	clients         map[*websocket.Conn]bool
	clientsMu       sync.RWMutex
	writeMu         map[*websocket.Conn]*sync.Mutex
	messages        []WebMsg
	messagesMu      sync.RWMutex
	pendingRequests []map[string]string
	pendingReqMu    sync.Mutex
	token           string
}

type WebMsg struct {
	ID        string    `json:"id"`
	From      string    `json:"from"`
	FromName  string    `json:"fromName"`
	Content   string    `json:"content"`
	Timestamp time.Time `json:"timestamp"`
	IsOwn     bool      `json:"isOwn"`
}

type WSMessage struct {
	Type    string          `json:"type"`
	Payload json.RawMessage `json:"payload"`
}

var webClient atomic.Pointer[WebClient]

func StartWebServer(sm *StreamManager, dataDir string, serveUI bool) {
	listener, err := net.Listen("tcp", "127.0.0.1:9090")
	if err != nil {
		fmt.Printf("Listener error: %v\n", err)
		return
	}

	token, err := newAPIToken()
	if err != nil {
		_ = listener.Close()
		fmt.Printf("Failed to generate API token: %v\n", err)
		return
	}

	port := listener.Addr().(*net.TCPAddr).Port
	client := &WebClient{
		sm:              sm,
		clients:         make(map[*websocket.Conn]bool),
		writeMu:         make(map[*websocket.Conn]*sync.Mutex),
		messages:        make([]WebMsg, 0),
		pendingRequests: make([]map[string]string, 0),
		token:           token,
	}
	webClient.Store(client)

	wsUpgrader = websocket.Upgrader{
		CheckOrigin: func(r *http.Request) bool {
			return originAllowed(r, port)
		},
	}

	if err := writeAPIConfig(dataDir, port, token); err != nil {
		_ = listener.Close()
		fmt.Printf("Failed to write API credentials: %v\n", err)
		return
	}

	webRoot := filepath.Join(".", "web")

	mux := http.NewServeMux()
	mux.HandleFunc("/ws", handleWebSocket)
	if serveUI {
		mux.Handle("/", http.FileServer(http.Dir(webRoot)))
		fmt.Printf("\nWeb interface: %s\n\n", colorize(92, fmt.Sprintf("http://127.0.0.1:%d/#token=%s", port, token)))
	} else {
		mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
			http.Error(w, "web UI disabled; start the core with --web to enable it", http.StatusNotFound)
		})
		fmt.Printf("Local API listening on 127.0.0.1:%d (web UI disabled; use --web to enable)\n", port)
	}

	go http.Serve(listener, mux)
}

func newAPIToken() (string, error) {
	raw := make([]byte, 32)
	if _, err := rand.Read(raw); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(raw), nil
}

func writeAPIConfig(dataDir string, port int, token string) error {
	if dataDir == "" {
		return fmt.Errorf("empty data directory")
	}
	if err := os.MkdirAll(dataDir, 0700); err != nil {
		return err
	}
	if err := os.WriteFile(filepath.Join(dataDir, "api.token"), []byte(token+"\n"), 0600); err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(dataDir, "api.port"), []byte(strconv.Itoa(port)+"\n"), 0600)
}

func originAllowed(r *http.Request, port int) bool {
	origin := r.Header.Get("Origin")
	if origin == "" {
		// Native clients (such as the Flutter app) do not send an Origin
		// header. They still have to present a valid token.
		return true
	}
	return origin == fmt.Sprintf("http://127.0.0.1:%d", port) ||
		origin == fmt.Sprintf("http://localhost:%d", port)
}

func (wc *WebClient) authorized(r *http.Request) bool {
	if wc == nil || wc.token == "" {
		return false
	}
	provided := r.URL.Query().Get("token")
	return subtle.ConstantTimeCompare([]byte(provided), []byte(wc.token)) == 1
}

func handleWebSocket(w http.ResponseWriter, r *http.Request) {
	wc := webClient.Load()
	if wc == nil || !wc.authorized(r) {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}

	conn, err := wsUpgrader.Upgrade(w, r, nil)
	if err != nil {
		return
	}

	wc.clientsMu.Lock()
	wc.clients[conn] = true
	wc.writeMu[conn] = &sync.Mutex{}
	wc.clientsMu.Unlock()

	wc.sendInitialState(conn)
	go wc.readWebSocket(conn)
}

func (wc *WebClient) readWebSocket(conn *websocket.Conn) {
	defer func() {
		wc.clientsMu.Lock()
		delete(wc.clients, conn)
		delete(wc.writeMu, conn)
		wc.clientsMu.Unlock()
		conn.Close()
	}()

	for {
		var msg WSMessage
		if err := conn.ReadJSON(&msg); err != nil {
			break
		}
		wc.handleWSMessage(msg)
	}
}

func (wc *WebClient) handleWSMessage(msg WSMessage) {
	switch msg.Type {

	case "send_message":
		var p struct {
			To      string `json:"to"`
			Content string `json:"content"`
		}
		json.Unmarshal(msg.Payload, &p)
		wc.sendMessageToPeer(p.To, p.Content)

	case "connect_peer":
		var p struct {
			Address string `json:"address"`
		}
		json.Unmarshal(msg.Payload, &p)
		go wc.sm.connectToPeer(p.Address)

	case "discover_peers":
		go wc.handleDiscoverPeers()

	case "connection_response":
		var p struct {
			PeerID   string `json:"peerId"`
			Accepted bool   `json:"accepted"`
		}
		json.Unmarshal(msg.Payload, &p)
		RemovePendingRequest(p.PeerID)
		wc.sm.RespondToConnectionRequest(p.PeerID, p.Accepted)
	case "reconnect_peer":
		var p struct {
			PeerID string `json:"peerId"`
		}
		json.Unmarshal(msg.Payload, &p)
		if p.PeerID != "" {
			go wc.sm.ReconnectPeer(p.PeerID)
		}

	case "disconnect_peer":
		var p struct {
			PeerID string `json:"peerId"`
		}
		json.Unmarshal(msg.Payload, &p)
		if p.PeerID != "" {
			wc.sm.disconnectSpecificPeer(p.PeerID)
		} else {
			wc.sm.disconnectActivePeer()
		}
	case "save_peer":
		var p struct {
			PeerID   string `json:"peerId"`
			Nickname string `json:"nickname"`
		}
		json.Unmarshal(msg.Payload, &p)
		if wc.sm.peerStore != nil && p.PeerID != "" {
			pid, err := peerstore.Decode(p.PeerID)
			if err != nil {
				return
			}
			if err := wc.sm.peerStore.SavePeer(pid.String(), p.Nickname); err == nil {
				NotifyTrustedPeersUpdate(wc.sm.peerStore)
			}
		}

	case "remove_peer":
		var p struct {
			PeerID string `json:"peerId"`
		}
		json.Unmarshal(msg.Payload, &p)
		if wc.sm.peerStore != nil && p.PeerID != "" {
			pid, err := peerstore.Decode(p.PeerID)
			if err != nil {
				return
			}
			if err := wc.sm.peerStore.RemovePeerByID(pid.String()); err == nil {
				NotifyTrustedPeersUpdate(wc.sm.peerStore)
			}
		}

	case "clear_history":
		var p struct {
			PeerID string `json:"peerId"`
		}
		json.Unmarshal(msg.Payload, &p)
		if wc.sm.historyStore != nil && p.PeerID != "" {
			pid, err := peerstore.Decode(p.PeerID)
			if err != nil {
				return
			}
			_ = wc.sm.historyStore.ClearHistory(pid.String())
		}
	}
}

func (wc *WebClient) handleDiscoverPeers() {
	if wc.sm.dht == nil {
		wc.broadcast("discovery_status", map[string]string{
			"status": "DHT not initialized",
		})
		return
	}

	wc.broadcast("discovery_status", map[string]string{
		"status": "Discovering peers...",
	})

	ctx, cancel := wc.sm.ctx, func() {}
	defer cancel()

	peerChan, err := wc.sm.dht.DiscoverPeers(ctx, discovery.DefaultNamespace)
	if err != nil {
		wc.broadcast("discovery_status", map[string]string{
			"status": fmt.Sprintf("Discovery failed: %v", err),
		})
		return
	}

	discoveredPeers := []map[string]string{}
	timeout := time.After(discoveryTime * time.Second)

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

			if len(peer.Addrs) > 0 {
				address := fmt.Sprintf("%s/p2p/%s", peer.Addrs[0].String(), peer.PeerID.String())
				discoveredPeers = append(discoveredPeers, map[string]string{
					"address": address,
					"id":      peer.PeerID.String(),
				})

				wc.broadcast("discovered_peer", map[string]interface{}{
					"address": address,
					"id":      peer.PeerID.String(),
					"count":   len(discoveredPeers),
				})
			}
		}
	}

	wc.broadcast("discovery_complete", map[string]interface{}{
		"count": len(discoveredPeers),
		"peers": discoveredPeers,
	})
}

func (wc *WebClient) sendInitialState(conn *websocket.Conn) {
	info := map[string]interface{}{
		"id":        wc.sm.node.ID().String(),
		"addresses": wc.formatAddrs(wc.sm.node.Addrs()),
		"nickname":  wc.sm.localNick,
	}
	wc.sendToClient(conn, "node_info", info)
	wc.sendToClient(conn, "peers_list", wc.getPeersList())

	// Send any pending connection requests
	wc.pendingReqMu.Lock()
	pending := make([]map[string]string, len(wc.pendingRequests))
	copy(pending, wc.pendingRequests)
	wc.pendingReqMu.Unlock()
	for _, req := range pending {
		wc.sendToClient(conn, "connection_request", req)
	}

	// Send all stored peer histories
	if wc.sm.historyStore != nil {
		go func() {
			all, err := wc.sm.historyStore.LoadAllHistories()
			if err != nil {
				return
			}
			for peerID, msgs := range all {
				if len(msgs) > 0 {
					NotifyPeerHistory(peerID, msgs)
				}
			}
		}()
	}

	// Send trusted peers
	if wc.sm.peerStore != nil {
		NotifyTrustedPeersUpdate(wc.sm.peerStore)
	}
}

func (wc *WebClient) getPeersList() []map[string]interface{} {
	wc.sm.mu.RLock()
	defer wc.sm.mu.RUnlock()

	peers := make([]map[string]interface{}, 0)
	for peerID := range wc.sm.streams {
		id := peerID.String()
		short := id
		if len(id) > 8 {
			short = id[len(id)-8:]
		}

		nick := wc.sm.peerNicks[peerID]
		if nick == "" {
			nick = short
		}

		peers = append(peers, map[string]interface{}{
			"id":        id,
			"shortId":   short,
			"nickname":  nick,
			"connected": true,
		})
	}
	return peers
}

func (wc *WebClient) notifyPeerUpdate() {
	wc.broadcast("peers_list", wc.getPeersList())
}

func (wc *WebClient) sendMessageToPeer(peerIDStr, content string) {
	peerID, err := peerstore.Decode(peerIDStr)
	if err != nil {
		return
	}

	if _, err := wc.sm.sendChat(peerID, content); err != nil {
		return
	}

	wc.addMessage(wc.sm.node.ID().String(), wc.sm.localNick, content, true, peerIDStr)
}

func (wc *WebClient) addMessage(from, fromName, content string, isOwn bool, conversationPeerID string) {
	msg := WebMsg{
		ID:        fmt.Sprintf("%d", time.Now().UnixNano()),
		From:      from,
		FromName:  fromName,
		Content:   content,
		Timestamp: time.Now(),
		IsOwn:     isOwn,
	}

	wc.messagesMu.Lock()
	wc.messages = append(wc.messages, msg)
	wc.messagesMu.Unlock()

	// Save to encrypted history
	if wc.sm.historyStore != nil && conversationPeerID != "" {
		go wc.sm.historyStore.SaveMessage(conversationPeerID, storage.StoredMsg{
			ID:        msg.ID,
			From:      msg.From,
			FromName:  msg.FromName,
			Content:   msg.Content,
			Timestamp: msg.Timestamp,
			IsOwn:     msg.IsOwn,
		})
	}

	wc.broadcast("new_message", msg)
}

func (wc *WebClient) formatAddrs(addrs []multiaddr.Multiaddr) []string {
	out := make([]string, len(addrs))
	for i, a := range addrs {
		out[i] = a.String()
	}
	return out
}

func (wc *WebClient) broadcast(t string, payload interface{}) {
	wc.clientsMu.RLock()
	defer wc.clientsMu.RUnlock()

	for c := range wc.clients {
		wc.sendToClient(c, t, payload)
	}
}

func (wc *WebClient) sendToClient(conn *websocket.Conn, t string, payload interface{}) {
	p, _ := json.Marshal(payload)
	data, _ := json.Marshal(WSMessage{
		Type:    t,
		Payload: p,
	})

	wc.clientsMu.RLock()
	mu, ok := wc.writeMu[conn]
	wc.clientsMu.RUnlock()
	if !ok {
		return
	}
	mu.Lock()
	defer mu.Unlock()
	conn.WriteMessage(websocket.TextMessage, data)
}

func NotifyWebMessage(from, fromName, content string, isOwn bool) {
	if wc := webClient.Load(); wc != nil {
		wc.addMessage(from, fromName, content, isOwn, from)
	}
}

func NotifyWebPeerUpdate() {
	if wc := webClient.Load(); wc != nil {
		wc.notifyPeerUpdate()
	}
}

func NotifyConnectionRequest(peerID, shortID, nickname string) {
	wc := webClient.Load()
	if wc == nil {
		return
	}
	req := map[string]string{
		"peerId":   peerID,
		"shortId":  shortID,
		"nickname": nickname,
	}
	wc.pendingReqMu.Lock()
	wc.pendingRequests = append(wc.pendingRequests, req)
	wc.pendingReqMu.Unlock()
	wc.broadcast("connection_request", req)
}

func NotifyWebConnectionRejected(peerID string) {
	if wc := webClient.Load(); wc != nil {
		wc.broadcast("connection_rejected", map[string]string{
			"peerId": peerID,
		})
	}
}

func RemovePendingRequest(peerID string) {
	wc := webClient.Load()
	if wc == nil {
		return
	}
	wc.pendingReqMu.Lock()
	defer wc.pendingReqMu.Unlock()
	filtered := make([]map[string]string, 0)
	for _, r := range wc.pendingRequests {
		if r["peerId"] != peerID {
			filtered = append(filtered, r)
		}
	}
	wc.pendingRequests = filtered
}

func NotifyPeerHistory(peerID string, msgs []storage.StoredMsg) {
	wc := webClient.Load()
	if wc == nil {
		return
	}

	type historyPayload struct {
		PeerID   string              `json:"peerId"`
		Messages []storage.StoredMsg `json:"messages"`
	}

	wc.broadcast("peer_history", historyPayload{
		PeerID:   peerID,
		Messages: msgs,
	})
}

func NotifyTrustedPeersUpdate(ps *storage.PeerStore) {
	wc := webClient.Load()
	if wc == nil || ps == nil {
		return
	}
	peers, err := ps.LoadPeers()
	if err != nil {
		return
	}
	wc.broadcast("trusted_peers_list", peers)
}

func NotifyPeerDisconnected(peerID, nickname, shortID string) {
	if wc := webClient.Load(); wc != nil {
		wc.broadcast("peer_disconnected", map[string]string{
			"peerId":   peerID,
			"nickname": nickname,
			"shortId":  shortID,
		})
	}
}

func NotifyReconnectStatus(peerID, status, message string) {
	if wc := webClient.Load(); wc != nil {
		wc.broadcast("reconnect_status", map[string]string{
			"peerId":  peerID,
			"status":  status,
			"message": message,
		})
	}
}
