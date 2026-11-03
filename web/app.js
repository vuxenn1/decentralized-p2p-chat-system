let ws;
let activePeer = null;
let activePeerName = "";
let peers = [];
let trustedPeers = [];
let pendingRequests = [];
const offlinePeers = new Map();
const messagesByPeer = {};

const qs = id => document.getElementById(id);

// =====================
// ACCESS TOKEN
// =====================
function readToken() {
    const params = new URLSearchParams(location.hash.replace(/^#/, ''));
    const fromHash = params.get('token');
    if (fromHash) {
        sessionStorage.setItem('agdas_token', fromHash);
        return fromHash;
    }
    return sessionStorage.getItem('agdas_token') || '';
}

const apiToken = readToken();

function wsURL() {
    const proto = location.protocol === 'https:' ? 'wss' : 'ws';
    return `${proto}://${location.host}/ws?token=${encodeURIComponent(apiToken)}`;
}

// =====================
// WEBSOCKET CONNECTION
// =====================
function connectWS() {
    if (!apiToken) {
        setStatus('Missing access token. Open the URL printed in the terminal.');
        return;
    }

    ws = new WebSocket(wsURL());

    ws.onopen = () => {
        console.log('WebSocket connected');
    };

    ws.onmessage = e => {
        const message = JSON.parse(e.data);
        handleWebSocketMessage(message);
    };

    ws.onerror = error => {
        console.error('WebSocket error:', error);
    };

    ws.onclose = () => {
        console.log('WebSocket disconnected, reconnecting...');
        setTimeout(connectWS, 3000);
    };
}

function wsReady() {
    return ws && ws.readyState === WebSocket.OPEN;
}

function handleWebSocketMessage(message) {
    switch (message.type) {
        case "node_info":
            qs("nodeNick").textContent = message.payload.nickname || "anon";
            qs("nodeId").textContent = message.payload.id.substring(message.payload.id.length - 8);
            qs("nodeAddr").textContent = message.payload.addresses[0] || "-";
            break;

        case "peers_list":
            peers = Array.isArray(message.payload) ? message.payload : [];
            peers.forEach(peer => offlinePeers.delete(peer.id));
            renderPeers();
            renderOffline();
            renderTrusted();
            updateActiveState();
            break;

        case "new_message":
            storeMessage(message.payload);
            break;

        case "connection_request": {
            const request = message.payload || {};
            if (request.peerId && !pendingRequests.some(item => item.peerId === request.peerId)) {
                pendingRequests.push(request);
                renderRequests();
                setStatus(`Connection request from ${request.nickname || request.shortId || request.peerId}`);
            }
            break;
        }

        case "connection_rejected":
            setStatus('Your connection request was rejected');
            break;

        case "peer_history":
            mergeHistory(message.payload || {});
            break;

        case "trusted_peers_list":
            trustedPeers = Array.isArray(message.payload) ? message.payload : [];
            renderTrusted();
            renderOffline();
            updateActiveState();
            break;

        case "peer_disconnected": {
            const payload = message.payload || {};
            if (payload.peerId) {
                offlinePeers.set(payload.peerId, {
                    peerId: payload.peerId,
                    nickname: payload.nickname || "",
                    shortId: payload.shortId || shortId(payload.peerId)
                });
                renderOffline();
                renderTrusted();
                updateActiveState();
                setStatus(`${payload.nickname || payload.shortId || 'Peer'} went offline`);
            }
            break;
        }

        case "reconnect_status": {
            const payload = message.payload || {};
            if (payload.status === 'connected' && payload.peerId) {
                offlinePeers.delete(payload.peerId);
                renderOffline();
                renderTrusted();
            }
            setStatus(payload.message || payload.status || 'Reconnect status changed');
            break;
        }

        case "error":
            setStatus((message.payload && message.payload.message) || 'Backend error');
            break;

        case "discovery_status":
            setStatus((message.payload && message.payload.status) || '');
            break;

        case "discovered_peer":
            addDiscoveredPeer(message.payload);
            setStatus(`Found ${message.payload.count} peer(s)...`);
            break;

        case "discovery_complete":
            setStatus(`Discovery complete. Found ${message.payload.count} peer(s)`);
            break;
    }
}

// =====================
// STATUS
// =====================
let statusTimer = null;

function setStatus(text) {
    const statusEl = qs("discoveryStatus");
    statusEl.textContent = text;
    statusEl.style.display = "block";

    if (statusTimer) {
        clearTimeout(statusTimer);
    }
    statusTimer = setTimeout(() => {
        statusEl.style.display = "none";
    }, 6000);
}

// =====================
// DISCOVERED PEERS
// =====================
function addDiscoveredPeer(peer) {
    const section = qs("discoveredSection");
    const list = qs("discoveredList");

    section.style.display = "block";

    const item = document.createElement("div");
    item.className = "discovered-peer-item";

    const info = document.createElement("div");
    info.className = "discovered-peer-info";

    const name = document.createElement("div");
    name.className = "discovered-peer-name";
    name.textContent = `Peer ${shortId(peer.id)}`;

    const addr = document.createElement("div");
    addr.className = "discovered-peer-addr";
    addr.textContent = peer.address;

    info.append(name, addr);

    const button = document.createElement("button");
    button.className = "btn btn-connect";
    button.textContent = "Connect";
    button.addEventListener("click", () => connectToDiscoveredPeer(peer.address));

    item.append(info, button);
    list.appendChild(item);
}

function connectToDiscoveredPeer(address) {
    const confirmed = confirm(`Do you want to connect to this peer?\n\n${address}`);
    if (!confirmed) return;

    if (!wsReady()) {
        setStatus("Not connected to the local backend.");
        return;
    }

    ws.send(JSON.stringify({
        type: "connect_peer",
        payload: { address: address }
    }));

    setStatus("Connecting to peer...");
    setTimeout(() => {
        qs("discoveredList").innerHTML = "";
        qs("discoveredSection").style.display = "none";
    }, 2000);
}

qs("closeDiscovered").onclick = () => {
    qs("discoveredSection").style.display = "none";
    qs("discoveredList").innerHTML = "";
};

// =====================
// PEER HELPERS
// =====================
function shortId(id) {
    if (!id) return "";
    return id.length > 8 ? id.substring(id.length - 8) : id;
}

function isConnected(id) {
    return peers.some(peer => peer.id === id);
}

function peerLabel(id) {
    const connected = peers.find(peer => peer.id === id);
    if (connected) return connected.nickname || shortId(id);

    const saved = trustedPeers.find(peer => peer.peerId === id);
    if (saved) return saved.nickname || shortId(id);

    const offline = offlinePeers.get(id);
    if (offline) return offline.nickname || offline.shortId || shortId(id);

    return shortId(id);
}

// =====================
// PEER RENDERING
// =====================
function renderPeers() {
    const list = qs("peersList");
    list.innerHTML = "";

    if (peers.length === 0) {
        list.innerHTML = `
            <div class="empty-state">
                <p>No connected peers yet</p>
                <small>Click "Discover" to find peers on the network</small>
            </div>
        `;
        return;
    }

    peers.forEach(peer => {
        const row = document.createElement("div");
        row.className = "peer-item";
        if (activePeer === peer.id) {
            row.classList.add("active");
        }

        const name = document.createElement("div");
        name.className = "peer-name";
        name.textContent = peer.nickname || shortId(peer.id);

        const id = document.createElement("div");
        id.className = "peer-id";
        id.textContent = peer.shortId || shortId(peer.id);

        row.append(name, id);
        row.onclick = () => openPeer(peer.id, name.textContent);
        list.appendChild(row);
    });
}

function renderRequests() {
    const section = qs("requestsSection");
    const list = qs("requestsList");

    if (pendingRequests.length === 0) {
        section.style.display = "none";
        list.innerHTML = "";
        return;
    }

    section.style.display = "block";
    list.innerHTML = "";

    pendingRequests.forEach(request => {
        const item = document.createElement("div");
        item.className = "request-item";

        const name = document.createElement("div");
        name.className = "request-name";
        name.textContent = request.nickname || shortId(request.peerId);

        const id = document.createElement("div");
        id.className = "request-id";
        id.textContent = request.shortId || shortId(request.peerId);

        const actions = document.createElement("div");
        actions.className = "request-actions";

        const reject = document.createElement("button");
        reject.className = "btn btn-small btn-reject";
        reject.textContent = "Reject";
        reject.onclick = () => respondToRequest(request.peerId, false);

        const accept = document.createElement("button");
        accept.className = "btn btn-small btn-accept";
        accept.textContent = "Accept";
        accept.onclick = () => respondToRequest(request.peerId, true);

        actions.append(reject, accept);
        item.append(name, id, actions);
        list.appendChild(item);
    });
}

function respondToRequest(peerID, accepted) {
    if (!wsReady()) {
        setStatus("Not connected to the local backend.");
        return;
    }
    ws.send(JSON.stringify({
        type: "connection_response",
        payload: { peerId: peerID, accepted: accepted }
    }));
    pendingRequests = pendingRequests.filter(request => request.peerId !== peerID);
    renderRequests();
    setStatus(accepted ? "Connection accepted" : "Connection rejected");
}

function renderTrusted() {
    const section = qs("trustedSection");
    const list = qs("trustedList");

    if (trustedPeers.length === 0) {
        section.style.display = "none";
        list.innerHTML = "";
        return;
    }

    section.style.display = "block";
    list.innerHTML = "";

    trustedPeers.forEach(peer => {
        const row = document.createElement("div");
        row.className = "trusted-item";

        const dot = document.createElement("span");
        dot.className = "peer-status-dot";
        if (isConnected(peer.peerId)) {
            dot.classList.add("online");
        }

        const meta = document.createElement("div");
        meta.className = "peer-meta";

        const name = document.createElement("div");
        name.className = "peer-name";
        name.textContent = peer.nickname || shortId(peer.peerId);

        const id = document.createElement("div");
        id.className = "peer-id";
        id.textContent = shortId(peer.peerId);

        meta.append(name, id);

        const remove = document.createElement("button");
        remove.className = "btn btn-small btn-danger";
        remove.textContent = "Remove";
        remove.onclick = event => {
            event.stopPropagation();
            if (!wsReady()) return;
            ws.send(JSON.stringify({
                type: "remove_peer",
                payload: { peerId: peer.peerId }
            }));
            trustedPeers = trustedPeers.filter(item => item.peerId !== peer.peerId);
            renderTrusted();
            renderOffline();
            setStatus("Saved peer removed");
        };

        row.append(dot, meta, remove);
        row.onclick = () => openPeer(peer.peerId, name.textContent);
        list.appendChild(row);
    });
}

function renderOffline() {
    const section = qs("offlineSection");
    const list = qs("offlineList");

    const entries = [...offlinePeers.values()].filter(peer => {
        if (isConnected(peer.peerId)) return false;
        return !trustedPeers.some(item => item.peerId === peer.peerId);
    });

    if (entries.length === 0) {
        section.style.display = "none";
        list.innerHTML = "";
        return;
    }

    section.style.display = "block";
    list.innerHTML = "";

    entries.forEach(peer => {
        const row = document.createElement("div");
        row.className = "offline-item";

        const meta = document.createElement("div");
        meta.className = "peer-meta";

        const name = document.createElement("div");
        name.className = "peer-name";
        name.textContent = peer.nickname || peer.shortId || shortId(peer.peerId);

        const id = document.createElement("div");
        id.className = "peer-id";
        id.textContent = peer.shortId || shortId(peer.peerId);

        meta.append(name, id);

        const reconnect = document.createElement("button");
        reconnect.className = "btn btn-small";
        reconnect.textContent = "Reconnect";
        reconnect.onclick = event => {
            event.stopPropagation();
            if (!wsReady()) return;
            ws.send(JSON.stringify({
                type: "reconnect_peer",
                payload: { peerId: peer.peerId }
            }));
            setStatus("Reconnecting...");
        };

        row.append(meta, reconnect);
        row.onclick = () => openPeer(peer.peerId, name.textContent);
        list.appendChild(row);
    });
}

// =====================
// ACTIVE CHAT
// =====================
function openPeer(id, name) {
    activePeer = id;
    activePeerName = name || peerLabel(id);

    qs("chatTitle").textContent = `Chat with ${activePeerName}`;

    renderPeers();
    renderTrusted();
    renderOffline();
    updateActiveState();
    renderMessages();
}

function updateActiveState() {
    const connected = activePeer ? isConnected(activePeer) : false;

    qs("messageInput").disabled = !activePeer || !connected;
    qs("sendBtn").disabled = !activePeer || !connected;
    qs("savePeerBtn").disabled = !activePeer;
    qs("clearHistoryBtn").disabled = !activePeer;
    qs("disconnectBtn").disabled = !activePeer || !connected;

    const status = qs("statusIndicator");
    status.textContent = connected ? "●" : "○";
    status.style.color = connected ? "#16a34a" : "#9ca3af";
}

function mergeHistory(payload) {
    const peerId = payload.peerId || "";
    const messages = Array.isArray(payload.messages) ? payload.messages : [];
    if (!peerId) return;

    const existing = messagesByPeer[peerId] || [];
    const byId = new Map();

    existing.forEach(message => {
        byId.set(message.id || `${message.timestamp}-${message.content}`, message);
    });
    messages.forEach(message => {
        byId.set(message.id || `${message.timestamp}-${message.content}`, message);
    });

    messagesByPeer[peerId] = [...byId.values()].sort((a, b) => {
        return new Date(a.timestamp) - new Date(b.timestamp);
    });

    if (!isConnected(peerId) && !trustedPeers.some(peer => peer.peerId === peerId)) {
        const lastReceived = [...messages].reverse().find(message => !message.isOwn);
        offlinePeers.set(peerId, {
            peerId: peerId,
            nickname: lastReceived ? lastReceived.fromName : "",
            shortId: shortId(peerId)
        });
        renderOffline();
    }

    if (peerId === activePeer) {
        renderMessages();
    }
}

// =====================
// MESSAGES
// =====================
function storeMessage(message) {
    if (!message) return;

    const peerId = message.isOwn
        ? (message.to || activePeer)
        : message.from;

    if (!peerId) return;

    const list = messagesByPeer[peerId] || [];
    const key = message.id || `${message.timestamp}-${message.content}`;
    if (list.some(item => (item.id || `${item.timestamp}-${item.content}`) === key)) {
        return;
    }

    list.push(message);
    messagesByPeer[peerId] = list;

    if (peerId === activePeer) {
        renderMessages();
    }
}

function renderMessages() {
    const box = qs("messages");
    box.innerHTML = "";

    const messages = messagesByPeer[activePeer] || [];

    if (messages.length === 0) {
        box.innerHTML = `
            <div class="empty-chat">
                <p>No messages yet</p>
                <small>Start the conversation!</small>
            </div>
        `;
        return;
    }

    messages.forEach(message => {
        const row = document.createElement("div");
        row.className = message.isOwn ? "msg own" : "msg";

        const time = new Date(message.timestamp).toLocaleTimeString('en-US', {
            hour: '2-digit',
            minute: '2-digit'
        });

        const header = document.createElement("div");
        header.className = "msg-header";
        header.textContent = message.isOwn ? "You" : (message.fromName || peerLabel(message.from));

        const content = document.createElement("div");
        content.className = "msg-content";
        content.textContent = message.content;

        const timeEl = document.createElement("div");
        timeEl.className = "msg-time";
        timeEl.textContent = time;

        row.append(header, content, timeEl);
        box.appendChild(row);
    });

    box.scrollTop = box.scrollHeight;
}

// =====================
// BUTTON ACTIONS
// =====================
qs("discoverBtn").onclick = () => {
    if (!wsReady()) {
        setStatus("Not connected to the local backend.");
        return;
    }

    qs("discoveredList").innerHTML = "";
    qs("discoveredSection").style.display = "none";

    ws.send(JSON.stringify({ type: "discover_peers" }));
    setStatus("Starting discovery...");
};

qs("sendBtn").onclick = () => sendMessage();

qs("messageInput").addEventListener("keypress", event => {
    if (event.key === "Enter" && !event.shiftKey) {
        event.preventDefault();
        sendMessage();
    }
});

function sendMessage() {
    if (!activePeer || !isConnected(activePeer)) return;

    const input = qs("messageInput");
    const content = input.value.trim();
    if (!content) return;

    if (!wsReady()) {
        setStatus("Not connected to the local backend.");
        return;
    }

    ws.send(JSON.stringify({
        type: "send_message",
        payload: { to: activePeer, content: content }
    }));

    input.value = "";
}

qs("connectBtn").onclick = () => {
    const address = prompt("Enter peer multiaddress:\n\nExample:\n/ip4/192.168.1.100/tcp/12345/p2p/...");
    if (!address) return;

    if (!wsReady()) {
        setStatus("Not connected to the local backend.");
        return;
    }

    ws.send(JSON.stringify({
        type: "connect_peer",
        payload: { address: address }
    }));

    setStatus("Connecting to peer...");
};

qs("savePeerBtn").onclick = () => {
    if (!activePeer) return;
    const nickname = prompt("Save this peer as:", activePeerName);
    if (!nickname || !wsReady()) return;

    ws.send(JSON.stringify({
        type: "save_peer",
        payload: { peerId: activePeer, nickname: nickname }
    }));
    setStatus("Peer saved");
};

qs("clearHistoryBtn").onclick = () => {
    if (!activePeer) return;
    if (!confirm(`Clear history with ${activePeerName}?`)) return;
    if (!wsReady()) return;

    ws.send(JSON.stringify({
        type: "clear_history",
        payload: { peerId: activePeer }
    }));

    messagesByPeer[activePeer] = [];
    renderMessages();
    setStatus("History cleared");
};

qs("disconnectBtn").onclick = () => {
    if (!activePeer || !wsReady()) return;

    ws.send(JSON.stringify({
        type: "disconnect_peer",
        payload: { peerId: activePeer }
    }));
    setStatus("Disconnected");
};

// =====================
// INITIALIZATION
// =====================
connectWS();
