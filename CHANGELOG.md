# Changelog

All notable changes to the public core are documented here. The project does not have a stable release yet; versions will follow semantic versioning once the protocol and storage formats are frozen.

## [Unreleased] - Stage 0 baseline

### Security

- Bind the local web API to loopback instead of all interfaces.
- Generate a random per-run API token and require it for WebSocket access.
- Restrict browser origins to the local web origin.
- Reject peer IDs containing path separators or traversal components.
- Derive direction-separated session keys and add a direction marker to nonces.
- Verify AEAD tags before the replay check so forged packets cannot consume a legitimate counter.
- Reject reflected packets and out-of-order counters.
- Reuse the handshake reader in the message loop to prevent buffered-byte loss.
- Disable Android Auto Backup for identity and history data.
- Handle the Android 15+ dataSync foreground-service timeout.

### Added

- Security and storage test suites.
- Threat model v1.
- Security policy.
- GitHub Actions CI for vet, race tests, build and web syntax check.
- MIT license.
- Optional `--web` flag for the browser demo. The local API remains available without it.
- Protocol v2 envelope package with validation and tests.
- Draft protocol v2 specification.
- Ed25519 identity binding in the application-layer handshake. Each side signs a transcript containing both ephemeral keys and both identity keys.
- Per-session chat sequence numbers and delivery acknowledgements.
- Duplicate chat messages are rejected and acknowledged. Out-of-order messages are buffered within a 64-message limit and delivered in order after a gap is filled.
- Android build script for both supported ABIs.
- A 64-packet sliding replay window at the session layer. Limited reordering is accepted and duplicates are rejected.
- Sender retransmission for unacknowledged chat messages.
- Receiver buffering for out-of-order chat messages, with ordered delivery after a gap is filled.
- Web demo support for connection requests, peer history, saved peers, offline peers, reconnect status and backend errors.
- A `to` field on web messages so own messages are routed to the correct conversation.
- CLI messages are now broadcast to web and Flutter clients.
- Contact verification with a symmetric 60-digit safety number derived from both identity public keys.
- A local verification marker stored in `verified_peers.json`.
- `/verify [ok|remove]` in the CLI and a verification dialog in the web and Flutter clients.
- A shared light and dark design system for the web demo and the Flutter client.
- A theme toggle in the web demo. The Flutter client follows the system theme.

### Changed

- History writes now use a temporary file, `fsync` and rename.
- A corrupt history file is preserved instead of being overwritten.
- The public README describes the public core, private Flutter app and web demo.
- The web demo reads the token from the URL fragment and uses safe DOM text insertion.
- Chat and control messages now use the v2 envelope.
- Nickname exchange uses a typed control message instead of the `__NICKNAME__` prefix.
- Latency and concurrent-test messages use control envelopes and no longer enter the chat history or the web UI.
- The global web client pointer is atomic to avoid cross-goroutine races during shutdown and tests.
