# 003 · Panel

## Context
The behavior is easiest to understand by clicking it. The React panel is embedded in the Go binary, so one container serves both.

## Behavior
- A form with amount, currency and test card, and the current idempotency key (new key on demand).
- Buttons: **Pay**, **Retry same request**, **Double click ×5** (five concurrent requests with one key), **Same key, other amount**.
- Failure injection: acquirer latency, fail the next calls, charge and then drop the connection.
- A log of every request with status, `Idempotent-Replayed` and latency; the payments list; counters of payments, acquirer charges and replays.

## Acceptance criteria
- After **Double click ×5**, payments and acquirer charges each grow by exactly one.
- The panel works with no build step for the reviewer (`docker compose up`).

## Out of scope
Design system, authentication.
