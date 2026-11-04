#!/bin/sh
# Build both architectures, write update.json and sign it.
#   VERSION=0.2.0 ./scripts/release.sh
# Then upload dist/* to the GitHub release tagged v<version>.
set -eu
cd "$(dirname "$0")/.."

VERSION="${VERSION:?set VERSION, for example: VERSION=0.2.0 ./scripts/release.sh}"
VERSION="${VERSION#v}"
KEY="${GLP_KEY:-$HOME/.config/glp/dev.key}"
BASE="${BASE_URL:-https://github.com/pxradiso/glp/releases/download/v$VERSION}"
NOTES="${NOTES:-}"
OUT=dist

[ -f "$KEY" ] || { echo "missing key: $KEY" >&2; exit 1; }
mkdir -p "$OUT"

json="$OUT/update.json"
{
	printf '{\n  "schema": 1,\n  "version": "%s",\n' "$VERSION"
	if [ -n "$NOTES" ]; then
		# NOTES is shown by `glp update`; keep it to one line.
		escaped=$(printf '%s' "$NOTES" | sed -e 's/\\/\\\\/g' -e 's/"/\\"/g')
		printf '  "notes": "%s",\n' "$escaped"
	fi
	printf '  "assets": {\n'
	first=1
	for arch in amd64 arm64; do
		bin="$OUT/glp-linux-$arch"
		GOOS=linux GOARCH=$arch CGO_ENABLED=0 \
			go build -trimpath -ldflags "-s -w -X main.version=$VERSION" -o "$bin" .
		sum=$(sha256sum "$bin" | cut -d' ' -f1)
		size=$(stat -c%s "$bin")
		[ "$first" -eq 1 ] || printf ',\n'
		first=0
		printf '    "linux/%s": {\n      "url": "%s/glp-linux-%s",\n      "sha256": "%s",\n      "size": %s\n    }' \
			"$arch" "$BASE" "$arch" "$sum" "$size"
	done
	printf '\n  }\n}\n'
} > "$json"

go run ./tools/sign sign "$KEY" "$json"
echo "wrote $json and $json.sig"
echo "upload $OUT/glp-linux-amd64, $OUT/glp-linux-arm64, $json and $json.sig to the release tagged v$VERSION"
