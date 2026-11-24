# 001 · Idempotency keys

## Context
A client that times out does not know whether its payment went through. Retrying without protection can charge the card twice; not retrying can lose the sale. With an `Idempotency-Key`, the client can retry safely: the server executes the request once and replays the result.

## Behavior
- `POST /v1/payments` requires an `Idempotency-Key` header (1–255 characters).
- The server fingerprints the request (SHA-256 of method, path and body) and claims the key.
- First request with a key: executed; the status and body of the response are stored with the key.
- Same key, same fingerprint, finished: the stored response is replayed byte for byte with `Idempotent-Replayed: true`. The handler does not run again.
- Same key, same fingerprint, still running: `409 Conflict` with `Retry-After: 1`.
- Same key, different fingerprint: `422 Unprocessable Content`. A key belongs to one request.
- A `5xx` response is **not** stored: the key is released so the client can retry it.
- If the process dies while a key is claimed, the claim expires after `LOCK_TIMEOUT` (default 30 s) and the next retry takes it over.
- Keys expire after `KEY_TTL` (default 24 h).

## Acceptance criteria
- 20 concurrent requests with the same key and body run the handler once; the others get `409` or the replay.
- Missing or too long header → `400`.

## Out of scope
Per-customer key scopes (there is no authentication), `GET` requests (they are already idempotent).
