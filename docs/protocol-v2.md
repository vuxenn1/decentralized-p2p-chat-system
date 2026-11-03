# Protocol v2 Draft

**Status:** Draft. The envelope is integrated for chat, nickname, latency and concurrent-test control messages. Chat messages use per-session sequence numbers and acknowledgements. Connection approval still uses the legacy pre-handshake line. Identity binding is implemented at the handshake layer.

## Goals

- Versioned messages.
- Explicit message types instead of string prefixes.
- Size limits before parsing.
- A stable contract for the core, the web demo and the private Flutter app.
- Room for acknowledgements and ordering.

## Envelope

Every v2 message is a JSON object with these fields:

| Field | Type | Required | Description |
|---|---|---|---|
| `v` | integer | yes | Envelope version. Current value is `2`. |
| `t` | string | yes | Message type. |
| `id` | string | yes | Unique message identifier. Maximum 128 characters. |
| `conv` | string | chat only | Conversation identifier. Maximum 128 characters. |
| `from` | string | chat only | Sender peer ID. Maximum 128 characters. |
| `ts` | integer | yes | Unix timestamp in milliseconds. |
| `seq` | integer | chat only | Per-session chat sequence number, starting at 1. |
| `body` | JSON value | no | Type-specific payload. Maximum 48 KB. |

The encoded envelope must not exceed 64 KB.

## Message types

| Type | Purpose |
|---|---|
| `chat` | User message. Requires `conv` and `from`. |
| `ack` | Delivery or read acknowledgement. |
| `control` | Connection, nickname or session control. |
| `error` | Typed error response. |
| `typing` | Ephemeral typing indicator. |
| `presence` | Online or offline state. |

Unknown types must be rejected with a clear error. New types require a version or capability negotiation.

## Validation rules

- `v` must be `2`.
- `t` must be a known type.
- `id` must be present and no longer than 128 characters.
- `ts` must be positive and must not be more than five minutes in the future.
- `seq` must be positive for chat messages.
- `conv` and `from` must be present for `chat` messages.
- `conv` and `from` must not exceed 128 characters.
- `body` must be valid JSON when present.
- `body` must not exceed 48 KB.
- The encoded envelope must not exceed 64 KB.

## Example chat envelope

```json
{
  "v": 2,
  "t": "chat",
  "id": "6Yb3x0Kq2m9Q",
  "conv": "12D3KooW...",
  "from": "12D3KooW...",
  "ts": 1767225600000,
  "seq": 1,
  "body": {
    "text": "hello"
  }
}
```

## Framing

The envelope is encoded as JSON, encrypted by the session layer, then written as one base64 packet line over the libp2p stream. Connection approval remains a separate pre-handshake line until the handshake is redesigned.

## Body schemas

### Chat

```json
{"text": "hello"}
```

Chat messages are persisted in the local history and shown in the clients.

### Control

```json
{"kind": "nickname", "name": "alice"}
{"kind": "latency_ping", "timestamp": 1767225600000, "payload": "aaaa"}
{"kind": "latency_pong", "timestamp": 1767225600000}
{"kind": "concurrent_test", "name": "alice", "index": 1}
```

Control messages are not persisted in the chat history and are not shown as chat messages. Unknown control kinds are ignored.

### Ack

```json
{"id": "6Yb3x0Kq2m9Q"}
```

The receiver sends an ack for every accepted chat message. Acks are not persisted and are not shown in the clients.

## Ordering and acknowledgements

- Each stream starts with sequence number 1 for each sender.
- The receiver accepts a chat message only when its sequence is exactly one greater than the last accepted sequence.
- Duplicate and out-of-order chat messages are rejected.
- A new stream resets both sequence counters.
- TCP already preserves byte order. The sequence check guards against missing messages and prepares the protocol for transports that can reorder.
- The current implementation does not retransmit a missing message. A gap blocks later chat messages until the stream is re-established. Retransmission is future work.

## Conversation identifier

The current draft uses a deterministic identifier derived from the two peer IDs. The IDs are sorted, joined with a separator and hashed with SHA-256. The first 16 bytes are encoded with base64url. Both peers compute the same value for a one-to-one conversation.

## Sender validation

The receiver verifies that the envelope sender matches the remote peer ID of the stream. A mismatch is rejected.

## Compatibility

- Peers advertise their supported version during the control handshake.
- A peer that receives an unsupported version returns an `error` envelope.
- A peer that receives an unknown type returns an `error` envelope.
- The legacy line protocol remains until both clients support v2.

## Security notes

- The envelope does not replace transport encryption. libp2p Noise remains the transport trust anchor.
- The application-layer session binds the envelope to direction-separated keys.
- The handshake signs a transcript containing both ephemeral keys and both identity keys. The envelope is bound to the resulting session keys.
- Replay handling belongs to the session layer and uses a monotonic counter.
- All identifiers and sizes must be validated before use.

## Open questions

1. Should `from` be a peer ID or a device ID after multi-device support?
2. Should `conv` be derived from both peer IDs for one-to-one chats?
3. Should retransmission use the same stream or a separate control stream?
4. Should the body be JSON, protobuf or a hybrid?
5. How should capability negotiation interact with the libp2p protocol ID?
