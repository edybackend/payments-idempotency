# 002 · Payments and the acquirer

## Context
The payment service charges cards through an acquirer (simulated). The network between them can fail after the acquirer charged, so the acquirer call carries the same idempotency key.

## Behavior
- Body: `amount` (minor units, e.g. `1990` = 19.90), `currency` (`BRL`, `USD`, `EUR`), `card` (test card number), `description`.
- Test cards: `4242 4242 4242 4242` approves; `4000 0000 0000 0002` is declined (`card_declined`); amounts above 1,000,000.00 are declined (`amount_too_large`).
- Approved or declined, a payment is created: `201` with `status` `approved` or `declined`, an authorization code or a decline code, and the last 4 digits only.
- If the acquirer is unreachable or fails, the API answers `502` (the key is released, the client can retry).
- `GET /v1/payments`, `GET /v1/payments/{id}`, `GET /v1/stats` (payments, acquirer charges, keys, replays).

## Acceptance criteria
- The acquirer deduplicates by idempotency key: the same key never creates a second charge.
- The acquirer charges and then the connection fails (`crashAfterCharge`): the client retries with the same key and gets the payment; there is one charge and one payment.
- `idempotency_key` is unique in `payments`, a second guard against duplicates.
- The full card number is never stored or returned.
- Invalid amount, currency or card → `400` with the field.

## Out of scope
Refunds, captures, 3-D Secure, real card networks.
