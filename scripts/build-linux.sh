#!/usr/bin/env bash
# Build Linux deployment tarballs via Docker (required for CGO/DuckDB).
set -euo pipefail

ROOT="$(cd "$(dirname "$0")/.." && pwd)"
cd "$ROOT"

VERSION="${VERSION:-$(git describe --tags --always --dirty 2>/dev/null || echo dev)}"
GO_IMAGE="${GO_IMAGE:-golang:1.25.0-bookworm}"
ARCHS="${ARCHS:-amd64}"
DIST="${ROOT}/dist"
mkdir -p "$DIST"
export COPYFILE_DISABLE=1

package_one() {
  local arch="$1"
  local bin="$2"
  local stage="${DIST}/go-mysql-duckdb-${VERSION}-linux-${arch}"
  rm -rf "$stage"
  mkdir -p "$stage/configs"
  cp "$bin" "$stage/syncer"
  chmod +x "$stage/syncer"
  cp configs/config.example.yaml "$stage/configs/"
  cp README.md README_zh.md LICENSE "$stage/"
  cat >"$stage/run.sh" <<'EOF'
#!/usr/bin/env bash
set -euo pipefail
cd "$(dirname "$0")"
mkdir -p data
if [[ ! -f configs/config.yaml ]]; then
  cp configs/config.example.yaml configs/config.yaml
  echo "created configs/config.yaml — edit MySQL settings then re-run"
  exit 1
fi
exec ./syncer -config configs/config.yaml "$@"
EOF
  chmod +x "$stage/run.sh"
  tar -C "$DIST" -czf "${stage}.tar.gz" "$(basename "$stage")"
  echo "==> ${stage}.tar.gz ($(du -h "$stage/syncer" | awk '{print $1}'))"
}

for arch in $ARCHS; do
  echo "==> building linux/${arch} (version=${VERSION})"
  out="${DIST}/.syncer-linux-${arch}"
  rm -f "$out"
  docker run --rm --platform "linux/${arch}" \
    -v "$ROOT":/src -w /src \
    -e CGO_ENABLED=1 \
    -e GOOS=linux \
    -e GOARCH="${arch}" \
    "${GO_IMAGE}" \
    go build -trimpath -ldflags="-s -w -X main.version=${VERSION}" \
      -o "/src/dist/.syncer-linux-${arch}" ./cmd/syncer
  package_one "$arch" "$out"
  rm -f "$out"
done

echo "done. artifacts:"
ls -lh "${DIST}"/go-mysql-duckdb-*-linux-*.tar.gz
