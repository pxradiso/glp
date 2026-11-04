#!/bin/sh
# Install glp from a source checkout or from the latest signed release.
#
#   ./install.sh              build this checkout, or install a release when
#                             there is no checkout next to the script
#   ./install.sh --build      build this checkout
#   ./install.sh --release    install the latest signed release
#
# Environment:
#   PREFIX          install prefix (default: ~/.local)
#   GLP_UPDATE_URL  release manifest URL
#   GLP_VERSION     version string for a source build
#
# No root is needed. PREFIX must be writable by the current user.
set -eu

# Must match embeddedPubKey in trust.go. This is the key that signs both the
# catalog and update.json, so the installer checks the release manifest with it
# before trusting any checksum inside.
PUBKEY_SPKI_B64="MCowBQYDK2VwAyEA1yDAinE2bJC7PDMKdBwV/37NcxShTYgTMrPDLdXd9Z0="
DEFAULT_UPDATE_URL="https://github.com/pxradiso/glp/releases/latest/download/update.json"

say() {
	printf '%s\n' "$*"
}

die() {
	printf 'install.sh: %s\n' "$*" >&2
	exit 1
}

usage() {
	cat <<'EOF'
usage: install.sh [--build|--release] [--force]

  --build     build this checkout and install it
  --release   install the latest signed release
  --force     reinstall even if the same version is already present
  (default)   build when run from a checkout, otherwise install a release

Environment:
  PREFIX           install prefix (default: ~/.local)
  GLP_UPDATE_URL   release manifest URL
  GLP_VERSION      version string for a source build
EOF
}

tmp=
cleanup() {
	if [ -n "$tmp" ]; then
		rm -rf "$tmp"
	fi
}
trap cleanup EXIT INT TERM HUP

fetch() {
	url=$1
	out=$2
	if command -v curl >/dev/null 2>&1; then
		curl -fLsS --retry 2 -o "$out" "$url" ||
			die "cannot download $url"
	elif command -v wget >/dev/null 2>&1; then
		wget -q -O "$out" "$url" || die "cannot download $url"
	else
		die "curl or wget is required"
	fi
}

sha256() {
	if command -v sha256sum >/dev/null 2>&1; then
		sha256sum "$1" | cut -d' ' -f1
	elif command -v shasum >/dev/null 2>&1; then
		shasum -a 256 "$1" | cut -d' ' -f1
	else
		openssl dgst -sha256 -r "$1" | cut -d' ' -f1
	fi
}

install_payload() {
	mkdir -p "$BIN_DIR" 2>/dev/null || die "cannot create $BIN_DIR"
	[ -w "$BIN_DIR" ] || die "$BIN_DIR is not writable"

	tmpbin="$BIN_DIR/.glp-install.$$"
	cp "$payload" "$tmpbin" || die "cannot write to $BIN_DIR"
	chmod 0755 "$tmpbin"
	if ! mv -f "$tmpbin" "$target"; then
		rm -f "$tmpbin"
		die "cannot replace $target"
	fi
	say "glp $version installed to $target"
	case ":$PATH:" in
	*":$BIN_DIR:"*) ;;
	*) say "add $BIN_DIR to PATH to use glp" ;;
	esac
}

build_from_source() {
	[ -n "$script_dir" ] && [ -f "$script_dir/go.mod" ] ||
		die "--build must run from the glp source tree"
	command -v go >/dev/null 2>&1 ||
		die "go is required to build from source, or run with --release"

	version=${GLP_VERSION:-}
	if [ -z "$version" ] && command -v git >/dev/null 2>&1; then
		version=$(cd "$script_dir" && git describe --tags --abbrev=0 2>/dev/null |
			sed 's/^v//') || version=
	fi
	[ -n "$version" ] || version=0.1.2

	say "building glp $version"
	(cd "$script_dir" &&
		CGO_ENABLED=0 go build -trimpath -ldflags "-s -w -X main.version=$version" \
			-o "$tmp/glp" .) || die "build failed"
	payload="$tmp/glp"
}

install_release() {
	case $(uname -s) in
	Linux) ;;
	*) die "glp only runs on Linux" ;;
	esac

	case $(uname -m) in
	x86_64 | amd64) arch=amd64 ;;
	aarch64 | arm64) arch=arm64 ;;
	*) die "no glp build for $(uname -s)/$(uname -m)" ;;
	esac
	platform="linux/$arch"

	manifest_url=${GLP_UPDATE_URL:-$DEFAULT_UPDATE_URL}
	manifest="$tmp/update.json"
	fetch "$manifest_url" "$manifest"
	fetch "$manifest_url.sig" "$manifest.sig"

	command -v openssl >/dev/null 2>&1 ||
		die "openssl is required to verify the release signature"
	printf '%s' "$PUBKEY_SPKI_B64" | openssl base64 -d -A -out "$tmp/pub.der" ||
		die "cannot prepare the trusted key"
	openssl pkey -pubin -inform DER -in "$tmp/pub.der" -out "$tmp/pub.pem" \
		>/dev/null 2>&1 || die "cannot prepare the trusted key"
	openssl base64 -d -A -in "$manifest.sig" -out "$tmp/update.sig" ||
		die "cannot decode the release signature"
	openssl pkeyutl -verify -pubin -inkey "$tmp/pub.pem" -rawin \
		-in "$manifest" -sigfile "$tmp/update.sig" >/dev/null 2>&1 ||
		die "release manifest signature is not valid, refusing to install"

	version=$(sed -n 's/^[[:space:]]*"version":[[:space:]]*"\([^"]*\)".*/\1/p' \
		"$manifest" | head -n 1)
	[ -n "$version" ] || die "cannot read the version from the release manifest"

	block=$(awk -v p="\"$platform\"" 'index($0, p) {on=1} on {print} on && /}/ {exit}' \
		"$manifest")
	asset_url=$(printf '%s\n' "$block" |
		sed -n 's/.*"url":[[:space:]]*"\([^"]*\)".*/\1/p')
	asset_sha=$(printf '%s\n' "$block" |
		sed -n 's/.*"sha256":[[:space:]]*"\([^"]*\)".*/\1/p')
	asset_size=$(printf '%s\n' "$block" |
		sed -n 's/.*"size":[[:space:]]*\([0-9][0-9]*\).*/\1/p')

	[ -n "$asset_url" ] || die "no glp build for $platform in the release manifest"
	[ -n "$asset_sha" ] || die "release manifest has no checksum for $platform"
	asset_sha=$(printf '%s' "$asset_sha" | tr 'A-F' 'a-f')

	if [ "$force" -eq 0 ] && [ -x "$target" ]; then
		have=$("$target" version 2>/dev/null | awk '{print $2}') || have=
		if [ "$have" = "$version" ]; then
			say "glp $version is already installed"
			exit 0
		fi
	fi

	say "downloading glp $version"
	payload="$tmp/glp-download"
	fetch "$asset_url" "$payload"

	actual=$(sha256 "$payload")
	if [ "$actual" != "$asset_sha" ]; then
		die "checksum does not match, refusing to install"
	fi
	if [ -n "$asset_size" ]; then
		got=$(wc -c <"$payload" | tr -d ' ')
		[ "$got" = "$asset_size" ] ||
			die "downloaded $got bytes, expected $asset_size"
	fi
}

mode=auto
force=0
for arg in "$@"; do
	case $arg in
	--build) mode=build ;;
	--release) mode=release ;;
	--force) force=1 ;;
	-h | --help) usage; exit 0 ;;
	*) die "unknown option: $arg (try --help)" ;;
	esac
done

script_dir=$(CDPATH= cd -- "$(dirname -- "$0")" 2>/dev/null && pwd) || script_dir=

if [ "$mode" = auto ]; then
	if [ -n "$script_dir" ] && [ -f "$script_dir/go.mod" ] &&
		[ -f "$script_dir/main.go" ] &&
		grep -q '^module github.com/pxradiso/glp$' "$script_dir/go.mod" 2>/dev/null; then
		mode=build
	else
		mode=release
	fi
fi

PREFIX=${PREFIX:-$HOME/.local}
BIN_DIR=$PREFIX/bin
target=$BIN_DIR/glp

tmp=$(mktemp -d "${TMPDIR:-/tmp}/glp-install.XXXXXX") ||
	die "cannot create a temporary directory"

if [ "$mode" = build ]; then
	build_from_source
else
	install_release
fi

install_payload
