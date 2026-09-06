# syntax=docker/dockerfile:1
# ── Stage 1: build ──────────────────────────────────────────────────────────
FROM golang:1.26-bookworm AS builder

WORKDIR /src

# Cache module downloads as a separate layer.
COPY go.mod go.sum ./
RUN go mod download

# Copy source and build a fully-static binary.
COPY . .
RUN CGO_ENABLED=0 GOOS=linux go build -trimpath -ldflags="-s -w" -o /goeat .

# ── Stage 2: runtime ────────────────────────────────────────────────────────
FROM gcr.io/distroless/static-debian12

WORKDIR /app

# Copy the binary and embedded assets (templates/static are embedded at build time).
COPY --from=builder /goeat /app/goeat

# Data volume: database and recipe images live here so they survive container restarts.
VOLUME ["/data"]

EXPOSE 8080

ENTRYPOINT ["/app/goeat"]
