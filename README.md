# Ağdaş Core

Ağdaş is a serverless, peer-to-peer messaging system built in Go. This repository contains the networking core, the protocol implementation, encrypted local storage, the CLI and a browser-based demo client.

The primary product client is a private Flutter application. It is not part of this repository.

## Status

**Stabilization in progress.** The project is not production-ready. The security model is being revised, the local API is being hardened and the protocol will move to a versioned v2 format.

Do not use this software for sensitive communication yet.

## What is in this repository

- libp2p host and Kademlia DHT peer discovery
- Persistent Ed25519 identity
- Explicit connection approval before a chat session starts
- Encrypted messaging over libp2p streams
- Encrypted local message history
- CLI for desktop use
- Browser-based demo client (`web/`)

## Architecture

```text
CLI  ─┐
      ├──> Ağdaş Core (Go) ──> libp2p ──> remote peer
Web  ─┘          │
                 └──> encrypted local history
```

The core owns identity, sessions, messaging, storage, discovery and transport. Clients only talk to the core.

### Wire protocol

The chat and control messages use the versioned v2 envelope in `protocol/`. The libp2p protocol ID is `/p2pchat/2`. Chat messages carry a per-session sequence number and are acknowledged by the receiver. Connection approval still uses a separate pre-handshake line. The application-layer handshake signs a transcript with the peer Ed25519 identity and verifies it against the libp2p public key. libp2p Noise remains the transport trust anchor.

Two clients exist:

| Client | Location | Visibility |
|---|---|---|
| Web demo | `web/` + `web.go` | Public, in this repository |
| Flutter app | separate private repository | Private |

The APK is a build artifact of the private Flutter app. It is not part of this repository.

## Security model (current)

- **Transport:** libp2p uses Noise/TLS between peers. This provides authenticated transport encryption and peer identity binding.
- **Application layer:** a second X25519 + ChaCha20-Poly1305 layer is present. It uses direction-separated keys, transcript binding and a 64-packet replay window. The handshake signs the transcript with the Ed25519 identity and verifies the remote libp2p public key. It remains defense in depth.
- **Local API:** the browser API binds to `127.0.0.1` and requires a per-run token. The token is written to `api.token` in the data directory and printed in the web URL fragment.
- **Local storage:** message history is encrypted at rest. The key is derived from the local identity key. The identity key is currently stored as a file, not in an OS keystore.
- **Discovery:** the public IPFS DHT is used by default. This exposes peer IDs and network addresses to the DHT. A LAN-first discovery mode is planned.

Known limitations are tracked in the project roadmap and issue tracker.

## Run the desktop CLI

Requirements: Go 1.25 or newer.

```bash
go run . --identity yournick
```

On first run, an Ed25519 identity is created under `~/.p2pchat`. Use `--datadir` to choose another directory.

### CLI commands

```text
/discover               Find peers on the network
/connect <addr>         Connect to a peer by multiaddress
/accept <number>        Accept an incoming connection request
/reject <number>        Reject an incoming connection request
/list                   Show connected peers
/switch <number>        Switch active peer
/peers                  Show saved peers
/save <peerid> <nick>   Save a trusted peer with a custom nickname
/rename <number> <nick> Rename a saved peer
/remove <number>        Remove a saved peer
/disconnect             Disconnect from the active peer
/help                   Show all commands
```

## Web demo (optional)

The local API always listens on `127.0.0.1:9090` for native clients. The browser demo is opt-in:

```bash
go run . --identity yournick --web
```

When enabled, the core prints a URL containing a one-time access token in the URL fragment:

```text
http://127.0.0.1:9090/#token=...
```

Open that URL in a browser. The token is kept in `sessionStorage` and is required by the WebSocket endpoint. Opening the page without the token will not connect.

Without `--web`, the API still runs on loopback and the root path returns a short "web UI disabled" response. The CLI remains the default interface.

The web demo is a reference client, not the primary product. It is intentionally local-only.

## Build and test

```bash
go build ./...
go test ./...
go vet ./...
```

The security and storage packages contain focused tests for replay-window behavior, direction separation, reflection rejection, path traversal and corrupt-file behavior.

## Android binaries

The private Flutter app embeds the core as an Android native binary. On Windows, build both ABIs with:

```powershell
powershell -File scripts/build-android.ps1
```

The script writes `dist/android/arm64-v8a/libp2pchat.so` and `dist/android/x86_64/libp2pchat.so`. It requires the Android NDK. Set `ANDROID_NDK_HOME` or install the NDK under `%LOCALAPPDATA%\Android\Sdk\ndk`.

The build uses `-ldflags="-s -w -checklinkname=0"`. The `checklinkname=0` flag is required because `github.com/wlynxg/anet` uses `go:linkname` for Android interface discovery.

## Repository layout

```text
main.go          CLI, stream manager, connection approval, message loop
web.go           local web server and WebSocket bridge
security/        handshake and message encryption
storage/         encrypted history and trusted peers
discovery/       libp2p DHT discovery
protocol/        versioned envelope for protocol v2 (draft)
web/             browser demo client
```

## Roadmap

The project is being stabilized before new features are added. The public roadmap covers the security baseline, protocol v2, storage redesign, Android/platform work, networking, UX and release hardening.

## License

MIT. See [LICENSE](LICENSE).

## Academic notice

This project started as a graduation engineering design project at Erzurum Technical University, Department of Computer Engineering, 2025-2026. It is now maintained as an independent engineering project.

The public core is shared for study and reuse under the MIT license. The private Flutter application remains proprietary.
