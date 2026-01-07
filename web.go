package main

import (
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"path/filepath"
	"sync"
	"time"

	"github.com/gorilla/websocket"
	peerstore "github.com/libp2p/go-libp2p/core/peer"
	multiaddr "github.com/multiformats/go-multiaddr"

	"p2p-chat/discovery"
)

var wsUpgrader = websocket.Upgrader{
	CheckOrigin: func(r *http.Request) bool {
		return true
	},
}

type WebClient struct {
	sm         *StreamManager
	clients    map[*websocket.Conn]bool
	clientsMu  sync.RWMutex
	messages   []WebMsg
	messagesMu sync.RWMutex
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

var webClient *WebClient

func StartWebServer(sm *StreamManager) {
	webClient = &WebClient{
		sm:       sm,
		clients:  make(map[*websocket.Conn]bool),
		messages: make([]WebMsg, 0),
	}

	listener, err := net.Listen("tcp", ":0")
	if err != nil {
		fmt.Printf("Listener error: %v\n", err)
		return
	}

	port := listener.Addr().(*net.TCPAddr).Port
	webRoot := filepath.Join(".", "web")

	mux := http.NewServeMux()
	mux.HandleFunc("/ws", handleWebSocket)
	mux.Handle("/", http.FileServer(http.Dir(webRoot)))

	fmt.Printf("\n🌐 Web interface: %s\n\n", colorize(92, fmt.Sprintf("http://localhost:%d", port)))

	go http.Serve(listener, mux)
}

func handleWebSocket(w http.ResponseWriter, r *http.Request) {
	conn, err := wsUpgrader.Upgrade(w, r, nil)
	if err != nil {
		return
	}

	webClient.clientsMu.Lock()
	webClient.clients[conn] = true
	webClient.clientsMu.Unlock()

	webClient.sendInitialState(conn)
	go webClient.readWebSocket(conn)
}

func (wc *WebClient) readWebSocket(conn *websocket.Conn) {
	defer func() {
		wc.clientsMu.Lock()
		delete(wc.clients, conn)
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
		wc.sm.connectToPeer(p.Address)

	case "discover_peers":
		go wc.handleDiscoverPeers()
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
	timeout := time.After(DISCOVERY_TIME * time.Second)

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
	wc.sendToClient(conn, "message_history", wc.messages)
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

	wc.sm.mu.RLock()
	stream, ok := wc.sm.streams[peerID]
	session, sessionOk := wc.sm.sessions[peerID]
	wc.sm.mu.RUnlock()

	if !ok || !sessionOk {
		return
	}

	enc, err := session.Encrypt([]byte(content))
	if err != nil {
		return
	}

	stream.Write([]byte(enc + "\n"))
	wc.addMessage(wc.sm.node.ID().String(), wc.sm.localNick, content, true)
}

func (wc *WebClient) addMessage(from, fromName, content string, isOwn bool) {
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
	conn.WriteMessage(websocket.TextMessage, data)
}

func NotifyWebMessage(from, fromName, content string, isOwn bool) {
	if webClient != nil {
		webClient.addMessage(from, fromName, content, isOwn)
	}
}

func NotifyWebPeerUpdate() {
	if webClient != nil {
		webClient.notifyPeerUpdate()
	}
}
