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
- Protocol v2 envelope package with validation and tests. The envelope is not wired into the message loop yet.
- Draft protocol v2 specification.

### Changed

- History writes now use a temporary file, `fsync` and rename.
- A corrupt history file is preserved instead of being overwritten.
- The public README describes the public core, private Flutter app and web demo.
- The web demo reads the token from the URL fragment and uses safe DOM text insertion.
