#!/bin/sh
# Sign catalog/index.json with the offline key.
#   GLP_KEY=~/.config/glp/dev.key ./scripts/sign-catalog.sh
set -eu
cd "$(dirname "$0")/.."

KEY="${GLP_KEY:-$HOME/.config/glp/dev.key}"
[ -f "$KEY" ] || { echo "missing key: $KEY" >&2; exit 1; }
[ -f catalog/index.json ] || { echo "missing catalog/index.json" >&2; exit 1; }

go run ./tools/sign sign "$KEY" catalog/index.json
echo "catalog signed. Publish catalog/index.json and catalog/index.json.sig together."
