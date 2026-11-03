# Protocol v2 Draft

**Status:** Draft. The envelope package exists and is tested. The application still uses the legacy line protocol until the integration step.

## Goals

- Versioned messages.
- Explicit message types instead of string prefixes.
- Size limits before parsing.
- A stable contract for the core, the web demo and the private Flutter app.
- Room for acknowledgements, ordering and identity binding.

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
  "body": {
    "text": "hello"
  }
}
```

## Framing

The draft uses one JSON envelope per line over the existing libp2p stream. A future revision may use length-prefixed frames or protobuf.

## Compatibility

- Peers advertise their supported version during the control handshake.
- A peer that receives an unsupported version returns an `error` envelope.
- A peer that receives an unknown type returns an `error` envelope.
- The legacy line protocol remains until both clients support v2.

## Security notes

- The envelope does not replace transport encryption. libp2p Noise remains the transport trust anchor.
- The application-layer session binds the envelope to direction-separated keys.
- The draft does not yet bind the envelope to the Ed25519 identity. That step is required before the protocol can be called independently authenticated.
- Replay handling belongs to the session layer and uses a monotonic counter.
- All identifiers and sizes must be validated before use.

## Open questions

1. Should `from` be a peer ID or a device ID after multi-device support?
2. Should `conv` be derived from both peer IDs for one-to-one chats?
3. Should acknowledgements use a separate stream or the same stream?
4. Should the body be JSON, protobuf or a hybrid?
5. How should capability negotiation interact with the libp2p protocol ID?
