# VaultDock server image: web UI + API in one static binary.

# Build stages run on the build machine's native platform and cross-compile,
# so multi-architecture images build without emulation.

# The shared SDK is vendored in ./sdk (go.mod: replace => ./sdk), so this
# repository alone builds the image.

FROM --platform=$BUILDPLATFORM node:24-alpine AS web
WORKDIR /src/web
COPY web/package.json web/package-lock.json ./
RUN --mount=type=cache,target=/root/.npm npm ci --no-audit --no-fund
COPY web/ ./
RUN npm run build

FROM --platform=$BUILDPLATFORM golang:1.26-alpine AS build
WORKDIR /src
ENV CGO_ENABLED=0 GOFLAGS=-trimpath
COPY go.mod go.sum ./
COPY sdk ./sdk
RUN --mount=type=cache,target=/go/pkg/mod go mod download
COPY cmd ./cmd
COPY internal ./internal
COPY deploy/embed.go deploy/env.schema.json ./deploy/
COPY web/embed.go ./web/embed.go
COPY --from=web /src/web/dist ./web/dist
ARG VERSION=dev
ARG TARGETOS=linux
ARG TARGETARCH
RUN --mount=type=cache,target=/go/pkg/mod --mount=type=cache,target=/root/.cache/go-build \
    GOOS=$TARGETOS GOARCH=$TARGETARCH go build -ldflags "-s -w -X github.com/ASPEC-RACING-PTY-LTD/vad-server/internal/api.Version=${VERSION}" -o /out/vaultdock ./cmd/vaultdock \
 && mkdir -p /out/state /out/storage/default
# Storage node agents (install.sh "Install Node"), served by the server at
# /api/v1/node/agent/linux/<arch>; same version as the server.
RUN --mount=type=cache,target=/go/pkg/mod --mount=type=cache,target=/root/.cache/go-build \
    for arch in amd64 arm64; do \
      GOOS=linux GOARCH=$arch go build -ldflags "-s -w -X github.com/ASPEC-RACING-PTY-LTD/vad-server/internal/nodeagent.Version=${VERSION}" \
        -o /out/nodes/vad-node-linux-$arch ./cmd/vad-node || exit 1; \
    done

# FFmpeg and FFprobe for video thumbnails and the video editor's clip
# exports: static builds (no shared libraries), so the runtime stays
# distroless. Pinned; amd64 and arm64.
FROM mwader/static-ffmpeg:7.1.1 AS ffmpeg

FROM gcr.io/distroless/static-debian12:nonroot
ARG VERSION=dev
LABEL org.opencontainers.image.title="VaultDock server" \
      org.opencontainers.image.description="Self-hosted file storage and intelligent synchronisation" \
      org.opencontainers.image.vendor="ASPEC Racing Pty Ltd" \
      org.opencontainers.image.licenses="PolyForm-Noncommercial-1.0.0" \
      org.opencontainers.image.version="${VERSION}" \
      org.opencontainers.image.source="https://github.com/ASPEC-RACING-PTY-LTD/vad-server"
# The licence and third-party notices ship with the binary (BSD/Apache notice terms).
COPY LICENSE THIRD_PARTY_NOTICES.md FFMPEG_NOTICE.md /usr/share/doc/vaultdock/
COPY --from=build /out/vaultdock /usr/local/bin/vaultdock
COPY --from=build /out/nodes /usr/local/share/vaultdock/nodes
COPY --from=ffmpeg /ffmpeg /ffprobe /usr/local/bin/
COPY --from=build --chown=65532:65532 /out/state /state
COPY --from=build --chown=65532:65532 /out/storage /storage
ENV VAULTDOCK_STATE_DIR=/state \
    VAULTDOCK_DEFAULT_STORAGE=/storage/default \
    VAULTDOCK_STORAGE_ROOTS=/storage \
    VAULTDOCK_LISTEN=:8080
EXPOSE 8080
VOLUME ["/state", "/storage"]
USER 65532:65532
HEALTHCHECK --interval=30s --timeout=5s --start-period=10s --retries=3 CMD ["/usr/local/bin/vaultdock", "healthcheck"]
ENTRYPOINT ["/usr/local/bin/vaultdock"]
CMD ["serve"]
