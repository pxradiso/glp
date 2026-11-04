#!/bin/sh
# Build a release binary. VERSION=0.2.0 ./scripts/build.sh
set -eu
cd "$(dirname "$0")/.."

VERSION="${VERSION:-0.1.2}"
OUT="${1:-glp}"
LDFLAGS="-s -w -X main.version=$VERSION"

CGO_ENABLED=0 go build -trimpath -ldflags "$LDFLAGS" -o "$OUT" .
echo "built $OUT ($(du -h "$OUT" | cut -f1))"

for arch in amd64 arm64; do
	GOOS=linux GOARCH=$arch CGO_ENABLED=0 \
		go build -trimpath -ldflags "$LDFLAGS" -o "glp-linux-$arch" .
	echo "built glp-linux-$arch"
done
