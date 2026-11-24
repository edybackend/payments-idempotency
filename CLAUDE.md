# Engineering rules for AI agents

This file is loaded by Claude Code in every session. It defines how changes are made in this repository.
Hooks in `.claude/settings.json` enforce the parts that can be automated.

## Workflow
1. **Spec first.** A new behavior starts as a spec in `specs/NNN-name.md` (context, behavior, acceptance criteria, out of scope).
2. **Small steps.** One concern per commit, [Conventional Commits](https://www.conventionalcommits.org/) (`feat(idempotency): ...`, `fix(panel): ...`).
3. **Definition of done** = the CI gates pass locally: `gofmt -l .` is empty, `go vet ./...`, `go test -race ./...`, `golangci-lint run`, and in `web/` `npm run lint && npm run build`. The Stop hook blocks finishing while they are red.

## Layout
- `cmd/payments` wires everything; `internal/` holds the packages; no `pkg/`.
- `internal/idempotency` knows nothing about payments: it is HTTP middleware plus a key store.
- `internal/acquirer` stands for an external system. `internal/payments` talks to it only through the `Acquirer` interface.
- `web/` is the React panel; its build (`web/dist`) is embedded into the binary with `go:embed`.

## Payment rules
- **Every POST/PATCH route is wrapped by the idempotency middleware.** A hook blocks unwrapped ones.
- **The key travels downstream.** The acquirer call carries the same idempotency key, so a retry after a lost answer never charges twice.
- **5xx releases the key; everything else is stored and replayed** byte for byte. Do not store a response the server is not sure about.
- **A key belongs to one request.** Same key + different body is `422`, never a silent replay of the other request.
- **Card data:** only the last 4 digits are stored or returned. Never log card numbers (a hook blocks it).
- Money is an integer in minor units (`1990` = 19.90). Never floating point.

## Go conventions
- Errors are returned and wrapped with `%w`; no `panic` for control flow. `context.Context` is the first parameter of I/O.
- Small interfaces declared by the consumer. Time comes from an injected `func() time.Time`.
- Table-driven tests; `go test -race` must pass. Errors to clients are RFC 9457 problem details.

## Never
- Edit the SQLite files, `go.sum`, `package-lock.json` or `web/dist` by hand. Blocked by hook.
- Read or write secrets (`.env`, keys). Blocked by permissions and hook.
- Disable a linter or a test to make the build pass.
- Add a dependency without a reason stated in the commit message.
