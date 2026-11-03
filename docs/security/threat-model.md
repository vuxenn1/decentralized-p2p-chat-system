# Threat Model v1

**Status:** Draft for Stage 0
**Last updated:** 2026-11-03

This document describes the assets, adversaries, trust boundaries and mitigations for the public core. It is intentionally conservative: if a property is not tested or enforced in code, it is listed as a residual risk.

## 1. Assets

| Asset | Why it matters |
|---|---|
| Identity private key | Possession allows impersonation and decryption of local history |
| Message plaintext | The content users expect to keep private |
| Local message history | Past conversations, contacts, timestamps |
| Contact graph | Who talks to whom; sensitive metadata |
| Peer IDs and addresses | Network metadata; can reveal identity and location |
| Availability | A chat that can be silenced or crashed is not useful |
| Build/release integrity | A tampered binary defeats every other control |

## 2. Adversaries

### A. Passive network observer

Capabilities: can capture packets on the LAN or on the path to bootstrap/relay nodes.

Goals: read messages, enumerate peers, build a contact graph.

Current mitigation:

- libp2p Noise/TLS protects transport payloads.
- Application-layer AEAD adds a second layer.
- Direct connections avoid a central message server.

Residual risk:

- IP addresses, peer IDs, timing and message sizes are visible.
- Public DHT advertisements reveal online peers and addresses.
- A relay, if added later, sees metadata.

### B. Active network attacker / MITM

Capabilities: can intercept, drop, delay, replay or inject packets.

Goals: impersonate a peer, read or modify messages, cause denial of service.

Current mitigation:

- libp2p Noise authenticates the remote peer ID.
- Application-layer packets are AEAD-protected.
- Direction-separated keys reject reflection.
- Replay counters are checked after authentication.
- Connection approval is required before a chat stream is accepted.

Residual risk:

- The application-layer handshake signs the transcript with the Ed25519 identity and verifies the remote libp2p public key. Formal verification is still pending.
- Replay handling uses a 64-packet sliding window. Duplicates and packets older than the window are rejected. Limited reordering is accepted.
- No formal verification yet.

### C. Malicious peer

Capabilities: can complete a connection, send arbitrary bytes, open many streams and send malformed input.

Goals: crash the process, exhaust resources, spoof a nickname, inject control messages or cause data loss.

Current mitigation:

- Connection approval.
- AEAD authentication.
- Direction and replay checks.
- Peer IDs are validated before use as file names.
- History corruption does not overwrite existing data.
- Local API requires a token.

Residual risk:

- No rate limiting, stream limits, read deadlines or maximum line length yet.
- The receiver buffers at most 64 out-of-order chat messages per peer. When the buffer is full, further out-of-order messages are dropped and can only be recovered by a new stream.
- In-band control messages still exist in the application protocol; the protocol v2 work will remove them.
- A peer can choose any nickname; nicknames are not identities.
- A peer can send a `LATENCY_PING`-style message if the legacy control path remains.
- A malicious peer can flood connection requests.

### D. Local attacker / malicious application

Capabilities: another app on the same Android device, another user or process on a desktop or a malicious website in the user's browser.

Goals: read history, send messages as the user, change settings, delete history.

Current mitigation:

- Local API binds to `127.0.0.1`.
- A random per-run token is required.
- Token file is written with `0600`.
- Browser origins are restricted to the local web origin.
- The Android app sandbox prevents other apps from reading the token file.

Residual risk:

- On desktop, another process with the same user can read the token file and connect.
- A malicious local application can bind port 9090 before the core starts; the core then fails to write credentials and the app shows an unavailable state. This prevents silent impersonation but is still a denial of service.
- The long-term fix is local IPC with OS-level authorization.

### E. Stolen or compromised device

Capabilities: physical access, unlocked device, rooted device or filesystem access.

Goals: recover the identity key and history.

Current mitigation:

- Android app sandbox.
- History encrypted at rest.
- Auto Backup is disabled in the app manifest.

Residual risk:

- The identity key is stored as a file, not in the Android Keystore / OS keychain.
- An attacker who can read the data directory can read the history key material.
- No secure deletion guarantee on flash storage.

### F. Supply-chain attacker

Capabilities: compromised dependency, build environment or release artifact.

Goals: ship a backdoored build.

Current mitigation:

- Go modules with checksums.
- CI runs tests, vet and the race detector.

Residual risk:

- No SBOM, reproducible build, signed release artifact or dependency vulnerability scanning yet.
- No external audit.

## 3. Trust boundaries

```text
[ remote peer ] --network--> [ libp2p Noise ] --> [ application crypto ] --> [ core ]
                                                                           |
                                                                     [ local storage ]
                                                                           |
[ web demo ] --loopback+token--> [ local API ] <----token---- [ Flutter app ]
```

| Boundary | Control |
|---|---|
| Network | Noise/TLS, AEAD, approval, replay checks |
| Local API | Loopback bind, token, origin allowlist |
| Client ↔ core | Token (short term), IPC + OS auth (long term) |
| Core ↔ disk | AEAD, file permissions, OS sandbox, backup exclusion |
| Build/release | CI, checksums, signing (planned) |

## 4. Assumptions

- The user's device and operating system are not already compromised.
- The user verifies peer identities out of band when confidentiality matters.
- libp2p's Noise implementation is correct and configured with peer authentication.
- The DHT is a discovery mechanism, not a trust authority.
- The private Flutter app and the public web demo are separate clients; neither is trusted by the core without a token.

## 5. Non-goals

- Strong anonymity.
- Metadata hiding against a global observer.
- Censorship circumvention at scale.
- Post-compromise security / forward-secret ratcheting.
- Group messaging security.
- Protection against a compromised operating system.
- Formal verification.

## 6. Security test mapping

| Property | Test |
|---|---|
| Direction separation | `security.TestDirectionSeparation` |
| Reflection rejection | `security.TestReflectionRejected` |
| Forged packet does not burn nonce | `security.TestForgedPacketDoesNotBurnNonce` |
| Replay rejection | `security.TestReplayRejected` |
| Out-of-order rejection | `security.TestOutOfOrderCounterRejected` |
| Handshake reader reuse | `security.TestHandshakeReaderKeepsFollowingData` |
| Path traversal rejection | `storage.TestClearHistoryRejectsTraversal`, `storage.TestSaveMessageRejectsTraversal` |
| Corrupt history preservation | `storage.TestCorruptHistoryIsNotOverwritten` |
| Atomic replacement | `storage.TestWriteReplacesExistingFile` |
| WebSocket token | `TestAuthorized`, `TestWebSocketRejectsMissingToken` |
| Origin allowlist | `TestOriginAllowed` |

## 7. Open items

1. Add formal verification for the handshake transcript and state machine.
2. Remove in-band control messages.
3. Add read deadlines, line limits, connection limits and rate limiting.
4. Move the local API to IPC.
5. Store identity keys in the OS keystore.
6. Add SBOM, dependency scanning and signed releases.
7. Revisit the threat model after every protocol change.
