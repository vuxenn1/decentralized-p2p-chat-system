# Peer-to-Peer Connection based Secure Chat System

A fully decentralized peer-to-peer chat application built with Go. No central server. No accounts. No data collection.

**Erzurum Technical University - Computer Engineering**

---

## Features

- Fully decentralized: No central server or authority
- End-to-end encrypted messaging (X25519 key exchange + ChaCha20-Poly1305)
- Persistent cryptographic identity per user (Ed25519)
- DHT-based peer discovery (Kademlia via libp2p)
- Connection approval: Incoming connections require explicit accept/reject
- Encrypted local message history (HKDF key derivation + ChaCha20-Poly1305)
- Trusted peer saving with custom nicknames
- Android mobile app with embedded Go backend
- Cross-platform CLI for desktop use

---

## Tech Stack

| Component | Technology |
|-----------|-----------|
| Language | Go 1.25 |
| Networking | libp2p |
| Peer Discovery | Kademlia DHT |
| Encryption | X25519 + ChaCha20-Poly1305 |
| Identity | Ed25519 |

---

## Run on Desktop (CLI)

```bash
go run . --identity yournick
```

**Available commands:**
```
/discover               - Find peers on the network
/connect <addr>         - Connect to a peer by multiaddress
/accept <number>        - Accept an incoming connection request
/reject <number>        - Reject an incoming connection request
/list                   - Show connected peers
/switch <number>        - Switch active peer
/peers                  - Show saved peers
/save <peerid> <nick>   - Save a trusted peer with custom nickname
/rename <number> <nick> - Rename a saved peer
/remove <number>        - Remove a saved peer
/help                   - Show all commands
```

---

## Security Properties

- **No plaintext transmission**: all messages encrypted before leaving the device
- **Forward secrecy**: Temporary session keys derived per connection
- **Local storage encryption**: Message history encrypted with HKDF-derived key unique to each conversation
- **Identity verification**: Peer IDs are derived from Ed25519 public keys, cannot be spoofed
- **No central authority**: No server can read, store, or censor messages

---

## Limitations

- Requires same WiFi network for direct phone-to-phone connections (no NAT traversal)
- No message sync across multiple devices
- No group chat
- DHT discovery may show offline peers (stale records expire with TTL)

---

## Academic Notice
This project was developed as a graduation engineering design project at Erzurum Technical University, Department of Computer Engineering, 2025–2026 academic year.

All rights reserved. This codebase is shared for academic and demonstration purposes only.
