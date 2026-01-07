let ws;
let activePeer = null;
const messagesByPeer = {};

const qs = id => document.getElementById(id);

// =====================
// WEBSOCKET CONNECTION
// =====================
function connectWS() {
    ws = new WebSocket(`ws://${location.host}/ws`);

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
    
    const item = document.createElement("div");
    item.className = "discovered-peer-item";
    
    const shortId = peer.id.substring(peer.id.length - 8);
    
    item.innerHTML = `
        <div class="discovered-peer-info">
            <div class="discovered-peer-name">Peer ${shortId}</div>
            <div class="discovered-peer-addr">${peer.address}</div>
        </div>
        <button class="btn btn-connect" onclick="connectToDiscoveredPeer('${peer.address}')">Connect</button>
    `;
    
    list.appendChild(item);
}

function connectToDiscoveredPeer(address) {
    const confirmed = confirm(`Do you want to connect to this peer?\n\n${address}`);
    
    if (!confirmed) return;
    
    ws.send(JSON.stringify({
        type: "connect_peer",
        payload: { address: address }
    }));
    
    showDiscoveryStatus("Connecting to peer...");
    
    // Clear discovered list after connecting
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

        d.innerHTML = `
            <div class="peer-name">${p.nickname}</div>
            <div class="peer-id">${p.shortId}</div>
        `;

        d.onclick = () => openPeer(p.id, p.nickname);
        list.appendChild(d);
    });
}

function openPeer(id, name) {
    activePeer = id;

    // Update UI
    const peerItems = document.querySelectorAll('.peer-item');
    peerItems.forEach(item => item.classList.remove('active'));
    event.currentTarget.classList.add('active');

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

        d.innerHTML = `
            <div class="msg-header">${displayName}</div>
            <div class="msg-content">${escapeHtml(m.content)}</div>
            <div class="msg-time">${time}</div>
        `;

        box.appendChild(d);
    });

    box.scrollTop = box.scrollHeight;
}

function escapeHtml(text) {
    const div = document.createElement('div');
    div.textContent = text;
    return div.innerHTML;
}

// =====================
// BUTTON ACTIONS
// =====================
qs("discoverBtn").onclick = () => {
    // Clear previous discoveries
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