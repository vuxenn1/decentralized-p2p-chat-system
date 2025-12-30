# Go P2P Chat
## About
A decentralized Peer-to-Peer messaging application developed in Go that allows peers to communicate directly with End-to-End Encryption, discovering each other through a distributed network.

## Features
- **Decentralized messaging**: Direct P2P communication without central servers.
- **End-to-end encryption**: All messages encrypted before transmission.
- **Peer discovery**: Automatic peer discovery through the network.
- **Persistent identities**: Locally stored cryptographic identities.

## Architecture
- **Discovery**: DHT-based peer discovery for finding other nodes.
- **Security**: End-to-End Encryption for all sent and received messages.
- **P2P**: Direct encrypted communication streams between peers.

## How to Use
1. Run the application with a custom identity name:
   ```bash
   go run main.go --identity custom_nick
   ````
2. Start the program on another terminal or machine with a different identity:
   ```bash
   go run main.go --identity another_custom_nick
   ```
3. Each peer loads or creates a persistent cryptographic identity.
4. Peers discover each other through the decentralized network.
5. Use CLI commands such as `/discover`, `/connect`, `/help`, etc.
6. Select a peer and start sending messages.
---
All messages are exchanged directly between peers using encrypted peer-to-peer connections.
