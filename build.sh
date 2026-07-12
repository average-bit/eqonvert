#!/usr/bin/env bash
set -euo pipefail
cd "$(dirname "$0")"

VERSION=$(git describe --tags --always --dirty 2>/dev/null || echo "dev")
LDFLAGS="-s -w -X main.version=${VERSION}"
OUT="dist"

mkdir -p "$OUT"

targets=(
    "linux   amd64"
    "linux   arm64"
    "darwin  amd64"
    "darwin  arm64"
    "windows amd64"
    "windows arm64"
)

for t in "${targets[@]}"; do
    OS=$(echo "$t" | awk '{print $1}')
    ARCH=$(echo "$t" | awk '{print $2}')
    BIN="${OUT}/eqonvert-${OS}-${ARCH}"
    [ "$OS" = "windows" ] && BIN="${BIN}.exe"
    printf "  %-20s → %s\n" "${OS}/${ARCH}" "$BIN"
    GOOS="$OS" GOARCH="$ARCH" go build -ldflags="$LDFLAGS" -o "$BIN" .
done

echo ""
echo "Built ${#targets[@]} binaries in ${OUT}/:"
ls -lh "$OUT"/
