let ws;
let activePeer = null;
let activePeerName = "";
let peers = [];
let trustedPeers = [];
let pendingRequests = [];
const offlinePeers = new Map();
const messagesByPeer = {};

let verifiedPeers = new Set();
const safetyNumbers = {};

let verificationPeer = null;
let verificationVerified = false;

const qs = id => document.getElementById(id);

const verifyHelpDefault = "If both sides show the same number, the conversation is encrypted without a man in the middle.";

// =====================
// THEME
// =====================
const themeToggle = qs("themeToggle");

function applyTheme(theme, persist) {
    const next = theme === "dark" ? "dark" : "light";
    document.documentElement.dataset.theme = next;
    themeToggle.setAttribute(
        "aria-label",
        next === "dark" ? "Switch to light theme" : "Switch to dark theme"
    );
    if (persist) {
        try {
            localStorage.setItem("agdas_theme", next);
        } catch (error) {
            console.warn("Could not store the theme choice:", error);
        }
    }
}

function storedTheme() {
    try {
        return localStorage.getItem("agdas_theme");
    } catch (error) {
        return null;
    }
}

themeToggle.addEventListener("click", () => {
    applyTheme(document.documentElement.dataset.theme === "dark" ? "light" : "dark", true);
});

const themeMedia = window.matchMedia("(prefers-color-scheme: dark)");
themeMedia.addEventListener("change", event => {
    if (!storedTheme()) {
        applyTheme(event.matches ? "dark" : "light", false);
    }
});

applyTheme(document.documentElement.dataset.theme, false);

// =====================
// ACCESS TOKEN
// =====================
function readToken() {
    const params = new URLSearchParams(location.hash.replace(/^#/, ""));
    const fromHash = params.get("token");
    if (fromHash) {
        sessionStorage.setItem("agdas_token", fromHash);
        return fromHash;
    }
    return sessionStorage.getItem("agdas_token") || "";
}

const apiToken = readToken();

function wsURL() {
    const proto = location.protocol === "https:" ? "wss" : "ws";
    return `${proto}://${location.host}/ws?token=${encodeURIComponent(apiToken)}`;
}

// =====================
// WEBSOCKET CONNECTION
// =====================
function connectWS() {
    if (!apiToken) {
        setStatus("Missing access token. Open the URL printed in the terminal.");
        return;
    }

    ws = new WebSocket(wsURL());

    ws.onopen = () => {
        console.log("WebSocket connected");
    };

    ws.onmessage = event => {
        try {
            handleWebSocketMessage(JSON.parse(event.data));
        } catch (error) {
            console.error("Could not handle a backend message:", error);
        }
    };

    ws.onerror = error => {
        console.error("WebSocket error:", error);
    };

    ws.onclose = () => {
        console.log("WebSocket disconnected, reconnecting...");
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
            if (activePeer) {
                activePeerName = peerLabel(activePeer);
            }
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
            setStatus("Your connection request was rejected");
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

        case "verified_peers_list":
            verifiedPeers = new Set(Array.isArray(message.payload) ? message.payload : []);
            if (verificationPeer) {
                verificationVerified = verifiedPeers.has(verificationPeer);
                renderVerification();
            }
            renderPeers();
            renderTrusted();
            updateActiveState();
            break;

        case "peer_verification": {
            const payload = message.payload || {};
            const peerId = payload.peerId;
            if (!peerId) break;

            if (payload.safetyNumber) {
                safetyNumbers[peerId] = payload.safetyNumber;
            }
            if (payload.verified) {
                verifiedPeers.add(peerId);
            } else {
                verifiedPeers.delete(peerId);
            }

            if (verificationPeer === peerId) {
                verificationVerified = Boolean(payload.verified);
                qs("verifyHelp").textContent = payload.keyAvailable === false
                    ? (payload.message || "The peer public key is not available yet.")
                    : verifyHelpDefault;
                renderVerification();
            }

            renderPeers();
            renderTrusted();
            updateActiveState();
            break;
        }

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
                setStatus(`${payload.nickname || payload.shortId || "Peer"} went offline`);
            }
            break;
        }

        case "reconnect_status": {
            const payload = message.payload || {};
            if (payload.status === "connected" && payload.peerId) {
                offlinePeers.delete(payload.peerId);
                renderOffline();
                renderTrusted();
                updateActiveState();
            }
            setStatus(payload.message || payload.status || "Reconnect status changed");
            break;
        }

        case "error":
            setStatus((message.payload && message.payload.message) || "Backend error");
            break;

        case "discovery_status":
            setStatus((message.payload && message.payload.status) || "");
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
    statusEl.hidden = false;

    if (statusTimer) {
        clearTimeout(statusTimer);
    }
    statusTimer = setTimeout(() => {
        statusEl.hidden = true;
    }, 6000);
}

// =====================
// DISCOVERED PEERS
// =====================
function addDiscoveredPeer(peer) {
    const section = qs("discoveredSection");
    const list = qs("discoveredList");

    section.hidden = false;

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
    button.className = "btn-connect";
    button.type = "button";
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
        qs("discoveredSection").hidden = true;
    }, 2000);
}

qs("closeDiscovered").onclick = () => {
    qs("discoveredSection").hidden = true;
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

function initialFor(id) {
    const label = peerLabel(id) || "?";
    return label.trim().charAt(0).toUpperCase() || "?";
}

function verifiedMark() {
    const mark = document.createElement("span");
    mark.className = "verified-mark";
    mark.title = "Verified contact";
    mark.setAttribute("aria-label", "Verified contact");
    return mark;
}

function nameRow(name, id) {
    const row = document.createElement("div");
    row.className = "peer-name-row";

    const label = document.createElement("div");
    label.className = "peer-name";
    label.textContent = name;

    row.append(label);
    if (id && verifiedPeers.has(id)) {
        row.append(verifiedMark());
    }
    return row;
}

// =====================
// PEER RENDERING
// =====================
function renderPeers() {
    const list = qs("peersList");
    list.innerHTML = "";
    qs("peerCount").textContent = String(peers.length);

    if (peers.length === 0) {
        const empty = document.createElement("div");
        empty.className = "empty-state";
        const title = document.createElement("p");
        title.textContent = "No connected peers";
        const hint = document.createElement("small");
        hint.textContent = "Use Discover or Connect to find someone.";
        empty.append(title, hint);
        list.appendChild(empty);
        return;
    }

    peers.forEach(peer => {
        const row = document.createElement("div");
        row.className = "peer-item";
        if (activePeer === peer.id) {
            row.classList.add("active");
        }

        const avatar = document.createElement("div");
        avatar.className = "avatar";
        avatar.textContent = initialFor(peer.id);

        const meta = document.createElement("div");
        meta.className = "peer-meta";

        const name = peer.nickname || shortId(peer.id);
        meta.append(nameRow(name, peer.id));

        const id = document.createElement("div");
        id.className = "peer-id";
        id.textContent = peer.shortId || shortId(peer.id);
        meta.append(id);

        row.append(avatar, meta);
        row.onclick = () => openPeer(peer.id, name);
        list.appendChild(row);
    });
}

function renderRequests() {
    const section = qs("requestsSection");
    const list = qs("requestsList");

    if (pendingRequests.length === 0) {
        section.hidden = true;
        list.innerHTML = "";
        return;
    }

    section.hidden = false;
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
        reject.type = "button";
        reject.textContent = "Reject";
        reject.onclick = () => respondToRequest(request.peerId, false);

        const accept = document.createElement("button");
        accept.className = "btn btn-small btn-accept";
        accept.type = "button";
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
        section.hidden = true;
        list.innerHTML = "";
        return;
    }

    section.hidden = false;
    list.innerHTML = "";

    trustedPeers.forEach(peer => {
        const row = document.createElement("div");
        row.className = "trusted-item";
        if (activePeer === peer.peerId) {
            row.classList.add("active");
        }

        const dot = document.createElement("span");
        dot.className = "peer-status-dot";
        if (isConnected(peer.peerId)) {
            dot.classList.add("online");
        }

        const meta = document.createElement("div");
        meta.className = "peer-meta";

        const name = peer.nickname || shortId(peer.peerId);
        meta.append(nameRow(name, peer.peerId));

        const id = document.createElement("div");
        id.className = "peer-id";
        id.textContent = shortId(peer.peerId);
        meta.append(id);

        const actions = document.createElement("div");
        actions.className = "row-actions";

        if (!isConnected(peer.peerId)) {
            const reconnect = document.createElement("button");
            reconnect.className = "btn btn-small";
            reconnect.type = "button";
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
            actions.append(reconnect);
        }

        const remove = document.createElement("button");
        remove.className = "btn btn-small btn-danger-ghost";
        remove.type = "button";
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
        actions.append(remove);

        row.append(dot, meta, actions);
        row.onclick = () => openPeer(peer.peerId, name);
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
        section.hidden = true;
        list.innerHTML = "";
        return;
    }

    section.hidden = false;
    list.innerHTML = "";

    entries.forEach(peer => {
        const row = document.createElement("div");
        row.className = "offline-item";
        if (activePeer === peer.peerId) {
            row.classList.add("active");
        }

        const meta = document.createElement("div");
        meta.className = "peer-meta";

        const name = peer.nickname || peer.shortId || shortId(peer.peerId);
        meta.append(nameRow(name, peer.peerId));

        const id = document.createElement("div");
        id.className = "peer-id";
        id.textContent = peer.shortId || shortId(peer.peerId);
        meta.append(id);

        const reconnect = document.createElement("button");
        reconnect.className = "btn btn-small";
        reconnect.type = "button";
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
        row.onclick = () => openPeer(peer.peerId, name);
        list.appendChild(row);
    });
}

// =====================
// ACTIVE CHAT
// =====================
function openPeer(id, name) {
    activePeer = id;
    activePeerName = name || peerLabel(id);

    closeVerifyModal();

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
    qs("verifyBtn").disabled = !activePeer;
    qs("savePeerBtn").disabled = !activePeer;
    qs("clearHistoryBtn").disabled = !activePeer;
    qs("disconnectBtn").disabled = !activePeer || !connected;

    qs("chatTitle").textContent = activePeer ? activePeerName : "Select a peer";
    qs("chatAvatar").textContent = activePeer ? initialFor(activePeer) : "?";

    const statusDot = qs("statusDot");
    statusDot.classList.toggle("online", connected);
    qs("statusText").textContent = activePeer
        ? (connected ? "Online" : "Offline")
        : "No conversation selected";

    qs("verifiedBadge").hidden = !(activePeer && verifiedPeers.has(activePeer));
    qs("messageInput").placeholder = connected ? "Write a message" : "Peer is offline";
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
        const empty = document.createElement("div");
        empty.className = "empty-chat";

        const icon = document.createElement("div");
        icon.className = "empty-icon";
        icon.innerHTML = '<svg viewBox="0 0 24 24" aria-hidden="true"><path d="M4 5.5h16v10H9l-5 4v-14Z" fill="none" stroke="currentColor" stroke-width="1.6" stroke-linejoin="round"/></svg>';

        const title = document.createElement("p");
        title.textContent = "No messages yet";
        const hint = document.createElement("small");
        hint.textContent = activePeer
            ? "Start the conversation."
            : "Choose a peer from the sidebar to start chatting.";

        empty.append(icon, title, hint);
        box.appendChild(empty);
        return;
    }

    messages.forEach(message => {
        const row = document.createElement("div");
        row.className = message.isOwn ? "msg own" : "msg";

        const time = new Date(message.timestamp).toLocaleTimeString("en-US", {
            hour: "2-digit",
            minute: "2-digit"
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
// VERIFICATION
// =====================
function requestVerification(peerId) {
    if (!wsReady() || !peerId) return;
    ws.send(JSON.stringify({
        type: "get_verification",
        payload: { peerId: peerId }
    }));
}

function openVerifyModal() {
    if (!activePeer) return;

    verificationPeer = activePeer;
    verificationVerified = verifiedPeers.has(activePeer);
    qs("verifySubtitle").textContent = `Compare this safety number with ${activePeerName}.`;
    qs("verifyHelp").textContent = verifyHelpDefault;
    qs("verifyModal").hidden = false;

    renderVerification();
    requestVerification(activePeer);
}

function closeVerifyModal() {
    qs("verifyModal").hidden = true;
    verificationPeer = null;
}

function renderVerification() {
    const status = qs("verificationStatus");
    status.textContent = verificationVerified ? "Verified" : "Not verified";
    status.classList.toggle("verified", verificationVerified);

    const box = qs("safetyNumber");
    box.innerHTML = "";

    const number = verificationPeer ? safetyNumbers[verificationPeer] : "";

    if (!number) {
        const placeholder = document.createElement("span");
        placeholder.className = "safety-placeholder";
        placeholder.textContent = "Loading safety number...";
        box.appendChild(placeholder);
    } else {
        number.split(" ").forEach(group => {
            const cell = document.createElement("div");
            cell.className = "safety-group";
            cell.textContent = group;
            box.appendChild(cell);
        });
    }

    const mark = qs("markVerified");
    mark.textContent = verificationVerified ? "Remove verification" : "Mark as verified";
    mark.disabled = !number;

    qs("copySafety").disabled = !number;
}

qs("verifyBtn").onclick = openVerifyModal;
qs("closeVerify").onclick = closeVerifyModal;

qs("verifyModal").addEventListener("click", event => {
    if (event.target === qs("verifyModal")) {
        closeVerifyModal();
    }
});

document.addEventListener("keydown", event => {
    if (event.key === "Escape" && !qs("verifyModal").hidden) {
        closeVerifyModal();
    }
});

qs("markVerified").onclick = () => {
    if (!verificationPeer || !wsReady()) return;
    ws.send(JSON.stringify({
        type: "set_verification",
        payload: {
            peerId: verificationPeer,
            verified: !verificationVerified
        }
    }));
};

qs("copySafety").onclick = async () => {
    const number = verificationPeer ? safetyNumbers[verificationPeer] : "";
    if (!number) return;

    try {
        await navigator.clipboard.writeText(number);
        setStatus("Safety number copied");
    } catch (error) {
        setStatus("Could not copy the safety number");
    }
};

// =====================
// BUTTON ACTIONS
// =====================
qs("discoverBtn").onclick = () => {
    if (!wsReady()) {
        setStatus("Not connected to the local backend.");
        return;
    }

    qs("discoveredList").innerHTML = "";
    qs("discoveredSection").hidden = true;

    ws.send(JSON.stringify({ type: "discover_peers" }));
    setStatus("Starting discovery...");
};

qs("sendBtn").onclick = () => sendMessage();

qs("messageInput").addEventListener("keydown", event => {
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
