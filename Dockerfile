# syntax=docker/dockerfile:1
# ── Stage 1: build ──────────────────────────────────────────────────────────
# Builds on the runner's own platform and cross-compiles to TARGETARCH, so a
# multi-arch build never runs the Go toolchain under QEMU emulation.
FROM --platform=$BUILDPLATFORM golang:1.26-alpine AS builder

ARG TARGETOS=linux
ARG TARGETARCH
# Stamped into main.version (shown on the About page). CI passes the git tag.
ARG VERSION=dev

WORKDIR /src

# Cache module downloads as a separate layer.
COPY go.mod go.sum ./
RUN --mount=type=cache,target=/go/pkg/mod go mod download

# Copy source and build a fully-static binary. timetzdata embeds the zoneinfo
# database (time.LoadLocation for household timezones), so the runtime image
# does not need the tzdata package.
COPY . .
RUN --mount=type=cache,target=/go/pkg/mod --mount=type=cache,target=/root/.cache/go-build \
	CGO_ENABLED=0 GOOS=$TARGETOS GOARCH=$TARGETARCH go build -trimpath -tags timetzdata \
	-ldflags="-s -w -X main.version=${VERSION}" -o /goeat .

# ── Stage 2: runtime ────────────────────────────────────────────────────────
FROM alpine:3.22

# CA roots for outbound HTTPS (AI providers, Kroger API, scraping), and an
# unprivileged user to run as. /data is created here, owned by that user, so
# a fresh named volume mounted over it inherits the ownership. Same for
# /models, the whisper sidecar's model volume (docker-compose.yml).
RUN apk add --no-cache ca-certificates \
	&& addgroup -S -g 10001 goeat \
	&& adduser -S -D -H -u 10001 -G goeat goeat \
	&& mkdir -p /data /models \
	&& chown goeat:goeat /data /models

# Video recipe import tools (yt-dlp, FFmpeg) are not part of the image: an
# admin downloads them from Settings → AI Setup onto the data volume.
# Speech-to-text runs in the whisper sidecar (whisper/Dockerfile).
ENV TOOLS_DIR=/data/tools

WORKDIR /app

# Templates, static files and migrations are embedded in the binary. Owned by
# root and not writable by goeat: the app can only write to /data.
COPY --from=builder /goeat /app/goeat

# Data volume: database and recipe images live here so they survive container restarts.
VOLUME ["/data"]

USER goeat:goeat

EXPOSE 8080

ENTRYPOINT ["/app/goeat"]
