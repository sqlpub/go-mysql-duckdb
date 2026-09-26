# Build Linux binaries (CGO + DuckDB). Use: make release-linux
ARG GO_VERSION=1.25.0
FROM --platform=$BUILDPLATFORM golang:${GO_VERSION}-bookworm AS builder

WORKDIR /src
# golang bookworm image already includes gcc/g++ for cgo

COPY go.mod go.sum ./
RUN go mod download

COPY . .

ARG TARGETOS=linux
ARG TARGETARCH=amd64
ARG VERSION=dev

# Native build inside matching platform container (set by docker --platform).
RUN CGO_ENABLED=1 GOOS=${TARGETOS} GOARCH=${TARGETARCH} \
	go build -trimpath -ldflags="-s -w -X main.version=${VERSION}" \
	-o /out/syncer ./cmd/syncer

FROM debian:bookworm-slim AS runtime
RUN apt-get update && apt-get install -y --no-install-recommends ca-certificates \
	&& rm -rf /var/lib/apt/lists/* || true
WORKDIR /app
COPY --from=builder /out/syncer /app/syncer
COPY configs/config.example.yaml /app/configs/config.example.yaml
ENTRYPOINT ["/app/syncer"]
CMD ["-config", "/app/configs/config.yaml"]
