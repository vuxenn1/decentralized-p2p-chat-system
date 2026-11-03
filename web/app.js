let ws;
let activePeer = null;
const messagesByPeer = {};

const qs = id => document.getElementById(id);

// =====================
// ACCESS TOKEN
// =====================
// The Go backend prints a URL with the token in the fragment:
//   http://127.0.0.1:9090/#token=...
// The fragment never leaves the browser, so the token is not sent to the
// static file server. It is kept in sessionStorage for reconnects.
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
        showDiscoveryStatus('Missing access token. Open the URL printed in the terminal.');
        return;
    }

    ws = new WebSocket(wsURL());

    ws.onopen = () => {
        console.log('WebSocket connected');
    };

    ws.onmessage = e => {
        const m = JSON.parse(e.data);
        handleWebSocketMessage(m);
    };

    ws.onerror = (error) => {
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

function handleWebSocketMessage(m) {
    switch (m.type) {
        case "node_info":
            qs("nodeNick").textContent = m.payload.nickname || "anon";
            qs("nodeId").textContent = m.payload.id.substring(m.payload.id.length - 8);
            qs("nodeAddr").textContent = m.payload.addresses[0] || "-";
            break;

        case "peers_list":
            renderPeers(m.payload);
            break;

        case "new_message":
            storeMessage(m.payload);
            break;

        case "discovery_status":
            showDiscoveryStatus(m.payload.status);
            break;

        case "discovered_peer":
            addDiscoveredPeer(m.payload);
            showDiscoveryStatus(`Found ${m.payload.count} peer(s)...`);
            break;

        case "discovery_complete":
            showDiscoveryStatus(`Discovery complete! Found ${m.payload.count} peer(s)`);
            setTimeout(() => {
                qs("discoveryStatus").style.display = "none";
            }, 3000);
            break;
    }
}

// =====================
// DISCOVERY STATUS
// =====================
function showDiscoveryStatus(text) {
    const statusEl = qs("discoveryStatus");
    statusEl.textContent = text;
    statusEl.style.display = "block";
}

// =====================
// DISCOVERED PEERS
// =====================
function addDiscoveredPeer(peer) {
    const section = qs("discoveredSection");
    const list = qs("discoveredList");

    section.style.display = "block";

    const shortId = peer.id.substring(peer.id.length - 8);

    const item = document.createElement("div");
    item.className = "discovered-peer-item";

    const info = document.createElement("div");
    info.className = "discovered-peer-info";

    const name = document.createElement("div");
    name.className = "discovered-peer-name";
    name.textContent = `Peer ${shortId}`;

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
        showDiscoveryStatus("Not connected to the local backend.");
        return;
    }

    ws.send(JSON.stringify({
        type: "connect_peer",
        payload: { address: address }
    }));

    showDiscoveryStatus("Connecting to peer...");

    setTimeout(() => {
        qs("discoveredList").innerHTML = "";
        qs("discoveredSection").style.display = "none";
    }, 2000);
}

// Close discovered peers list
qs("closeDiscovered").onclick = () => {
    qs("discoveredSection").style.display = "none";
    qs("discoveredList").innerHTML = "";
};

// =====================
// PEERS MANAGEMENT
// =====================
function renderPeers(peers) {
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

    peers.forEach(p => {
        const d = document.createElement("div");
        d.className = "peer-item";
        if (activePeer === p.id) {
            d.classList.add("active");
        }

        const name = document.createElement("div");
        name.className = "peer-name";
        name.textContent = p.nickname;

        const id = document.createElement("div");
        id.className = "peer-id";
        id.textContent = p.shortId;

        d.append(name, id);
        d.onclick = () => openPeer(p.id, p.nickname, d);
        list.appendChild(d);
    });
}

function openPeer(id, name, element) {
    activePeer = id;

    document.querySelectorAll('.peer-item').forEach(item => {
        item.classList.remove('active');
    });
    if (element) {
        element.classList.add('active');
    }

    qs("chatTitle").textContent = `Chat with ${name}`;
    qs("messageInput").disabled = false;
    qs("sendBtn").disabled = false;

    renderMessages();
}

// =====================
// MESSAGES MANAGEMENT
// =====================
function storeMessage(m) {
    const pid = m.isOwn ? activePeer : m.from;

    if (!pid) return;

    if (!messagesByPeer[pid]) {
        messagesByPeer[pid] = [];
    }

    messagesByPeer[pid].push(m);

    if (pid === activePeer) {
        renderMessages();
    }
}

function renderMessages() {
    const box = qs("messages");
    box.innerHTML = "";

    const msgs = messagesByPeer[activePeer] || [];

    if (msgs.length === 0) {
        box.innerHTML = `
            <div class="empty-chat">
                <p>No messages yet</p>
                <small>Start the conversation!</small>
            </div>
        `;
        return;
    }

    msgs.forEach(m => {
        const d = document.createElement("div");
        d.className = m.isOwn ? "msg own" : "msg";

        const time = new Date(m.timestamp).toLocaleTimeString('en-US', {
            hour: '2-digit',
            minute: '2-digit'
        });

        const displayName = m.isOwn ? "You" : m.fromName;

        const header = document.createElement("div");
        header.className = "msg-header";
        header.textContent = displayName;

        const content = document.createElement("div");
        content.className = "msg-content";
        content.textContent = m.content;

        const timeEl = document.createElement("div");
        timeEl.className = "msg-time";
        timeEl.textContent = time;

        d.append(header, content, timeEl);
        box.appendChild(d);
    });

    box.scrollTop = box.scrollHeight;
}

// =====================
// BUTTON ACTIONS
// =====================
qs("discoverBtn").onclick = () => {
    if (!wsReady()) {
        showDiscoveryStatus("Not connected to the local backend.");
        return;
    }

    qs("discoveredList").innerHTML = "";
    qs("discoveredSection").style.display = "none";

    ws.send(JSON.stringify({ type: "discover_peers" }));
    showDiscoveryStatus("Starting discovery...");
};

qs("sendBtn").onclick = () => {
    sendMessage();
};

qs("messageInput").addEventListener("keypress", (e) => {
    if (e.key === "Enter" && !e.shiftKey) {
        e.preventDefault();
        sendMessage();
    }
});

function sendMessage() {
    if (!activePeer) return;

    const input = qs("messageInput");
    const content = input.value.trim();

    if (!content) return;

    if (!wsReady()) {
        showDiscoveryStatus("Not connected to the local backend.");
        return;
    }

    ws.send(JSON.stringify({
        type: "send_message",
        payload: {
            to: activePeer,
            content: content
        }
    }));

    input.value = "";
}

qs("connectBtn").onclick = () => {
    const addr = prompt("Enter peer multiaddress:\n\nExample:\n/ip4/192.168.1.100/tcp/12345/p2p/...");

    if (!addr) return;

    if (!wsReady()) {
        showDiscoveryStatus("Not connected to the local backend.");
        return;
    }

    ws.send(JSON.stringify({
        type: "connect_peer",
        payload: { address: addr }
    }));

    showDiscoveryStatus("Connecting to peer...");
    setTimeout(() => {
        qs("discoveryStatus").style.display = "none";
    }, 3000);
};

// =====================
// INITIALIZATION
// =====================
connectWS();
