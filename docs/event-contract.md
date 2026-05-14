# Event Contract

All Masterdale commands share this event shape so they can cooperate without tightly importing each other.

```json
{
  "id": "01HX...",
  "schema": "masterdale.event.v1",
  "timestamp": "2026-05-09T20:00:00Z",
  "device_id": "example-device",
  "actor": "user|agent|system",
  "channel": "chat|task|codex|monitor|commerce",
  "kind": "message.created",
  "body": {},
  "refs": [],
  "hash": "hmac-sha256:..."
}
```

The hash is computed over the event with `hash` empty. In v1 it is a local integrity guard, not a distributed trust system.
