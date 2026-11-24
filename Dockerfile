# 1) Build the React panel. 2) Build the Go binary with the panel embedded. 3) Run it on distroless, non-root.
FROM node:24-slim AS web
WORKDIR /web
COPY web/package.json web/package-lock.json ./
RUN npm ci --no-audit --no-fund
COPY web/ ./
RUN npm run build

FROM golang:1.27 AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY cmd/ cmd/
COPY internal/ internal/
COPY web/embed.go web/
COPY --from=web /web/dist web/dist
RUN CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /out/payments ./cmd/payments \
    && mkdir -p /out/data

FROM gcr.io/distroless/static-debian12:nonroot
WORKDIR /app
COPY --from=build --chown=nonroot:nonroot /out/payments /app/payments
# distroless has no shell to mkdir at runtime, and /app belongs to root: ship the data directory owned by nonroot.
COPY --from=build --chown=nonroot:nonroot /out/data /app/data
ENV HTTP_ADDR=:8080 \
    DB_PATH=/app/data/payments.db \
    ACQUIRER_DB_PATH=/app/data/acquirer.db
EXPOSE 8080
ENTRYPOINT ["/app/payments"]
