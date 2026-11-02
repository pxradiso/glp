# glp

Install desktop apps on Ubuntu, Debian, Fedora (and friends) with one command.
No daemon, no FUSE, no root by default, no runtime to download.

```console
$ glp install appimagetool
appimagetool continuous
  Build an AppImage from an AppDir
  size    14.4 MiB
  sha256  95cbe7cc…

Install? [Y/n]
(1/4) downloading appimagetool-x86_64.AppImage
  ✔ (2/4) extracting
  ✔ (3/4) linking appimagetool
  ✔ (4/4) desktop entry and icon

run: appimagetool
```

## What it does

`glp` is a resolver and installer for AppImages:

1. looks the app up in a **signed** catalog,
2. shows you the exact URL, checksum and size,
3. downloads it and verifies the SHA-256,
4. extracts it (no FUSE, no mount) into `~/.local/opt/glp/<app>/<version>`,
5. symlinks the binaries into `~/.local/bin`, writes a `.desktop` file and an icon,
6. installs the system packages it declares, named in the confirmation and
   covered by the same yes.

Updates are atomic (`current` symlink swap) and the previous version is kept
until you remove the app or run `glp clean`.

## What it is not

- **Not a sandbox.** Extracted AppImages run with your user's full privileges.
  If you need isolation, use Flatpak or `bwrap` yourself.
- **Not a store.** `glp` ships no binaries; the catalog only carries metadata
  and the download happens from the upstream vendor.
- **Not a distro.** It integrates into `~/.local` and leaves your system
  package database alone (except for the packages you explicitly approve).

## Install

```console
$ curl -fsSL https://raw.githubusercontent.com/pxradiso/glp/main/install.sh | sh
```

The script detects `linux/amd64` or `linux/arm64`, verifies the release
manifest's ed25519 signature with the key compiled into the source, checks the
binary's SHA-256, and installs to `~/.local/bin/glp`. It never needs root. Set
`PREFIX=/usr/local` if you want another prefix.

From a checkout, the same script builds and installs the source you are looking
at:

```console
$ ./install.sh              # build this checkout
$ ./install.sh --release    # install the latest signed release instead
```

Source builds need Go 1.27+. The result is a single static binary.

## Commands

| Command | |
|---|---|
| `glp search <term>` | fuzzy search over names, labels and commands |
| `glp info <app>` | version, size, checksum, dependencies |
| `glp install <app>[@ver]` | download, verify, extract, integrate |
| `glp list` | installed apps |
| `glp upgrade [app]` | upgrade one app or everything |
| `glp remove <app>` | delete binaries, menu entry, icon, files |
| `glp doctor [app]` | find missing libraries and install them |
| `glp update [--check]` | check for a new glp release and install it |
| `glp index refresh` | re-fetch and re-verify the catalog |

Names are forgiving. An exact name always wins and never prompts. Otherwise a
unique near miss asks first, and several near misses become a numbered list:

```console
$ glp install win
did you mean winboat? [Y/n]

$ glp install vscod
2 apps match "vscod":
  1) vscode
  2) vscodium
pick one [1-2, enter to cancel]:
```

Matching looks at the catalog name, the menu label and the launcher command, so
`code` finds vscode and `chrome` finds chromium. With `GLP_YES=1` a single
candidate is accepted and an ambiguous one is refused, so unattended runs never
guess.

Environment:

- `GLP_INDEX_URL` — catalog location. Supports `https://`, `file://` and plain paths.
- `GLP_TRUST_KEY` — override the embedded public key (development only).
- `GLP_YES=1` — skip the install confirmation (unattended installs).
- `GLP_YES_ROOT=1` — **separately** approve root package installs, needed when
  `GLP_YES=1` runs unattended.
- `GLP_UPDATE_URL` — release manifest location (defaults to this project's releases).
- `GLP_YES_UPDATE=1` — skip the update prompt, never the catalog prompt.
- `GLP_NO_UPDATE_CHECK=1` — never check for a new release automatically.
- `GLP_PLAIN=1` — no colour, no animation (also automatic when stderr is not a tty).

## Apps that only ship source code

A few vendors publish nothing but a source tarball. Those entries carry a
`build` block instead of a ready binary, like Vinegar:

```json
"format": "tar.xz",
"deps": { "fedora": ["gtk4-devel", "libadwaita-devel", "..."] },
"build": { "commands": [["make", "install", "PREFIX=/usr", "DESTDIR={dest}"]] }
```

`glp` verifies the tarball, asks before installing the build packages as root,
then runs each step as an argv slice, never through a shell. `{src}`, `{dest}`
and `{final}` are substituted; `{final}` is where the build will live after the
atomic rename. All of it comes from the signed catalog, so a build step cannot
appear without a new catalog signature.

The result is the same as a binary install: `~/.local/opt/glp/<app>`, a launcher
in `~/.local/bin`, a menu entry. Vinegar compiles to about 30 MB instead of the
1 GB Flatpak runtime it would otherwise pull in.

## When an app installs but does not start

Almost always it is one missing system library. `glp` checks the launcher with
`ldd`, ignores anything the AppImage already bundles, and maps the rest to the
package that provides it:

```console
$ glp doctor discord
discord 1.0.167
  missing libraries (1):
    libXss.so.1

packages for discord (Fedora/RHEL (dnf)):
  sudo dnf install -y libXScrnSaver
Install them? [y/N]
```

This also runs automatically at the end of an install, and re-checks afterwards.
Mapping is done by the local package manager (`dnf provides`, `apt-file`,
`pacman -F`, `zypper search --provides`), so no table of library names is baked
into the binary.

## System packages the app needs

Some apps cannot start without a distribution package, like `vlc` for Friday
Night Funkin. The catalog lists them, the install summary names them, and the
same yes installs them:

```console
$ glp install fnf-game
  needs   vlc (root)

Install? [Y/n]

  installing vlc (root)
```

If root is not approved the app is still installed, and glp prints the command
it would have run instead of asking a second time:

```console
vlc still missing (root not approved)
  run: sudo pacman -S --noconfirm vlc
```

## Security model

The catalog is the trust root, so it is treated as hostile input:

- The catalog is signed with **ed25519**; the public key is compiled into the
  binary. Signature verification happens on every load, including cached copies.
  An unsigned or tampered catalog is refused before anything is downloaded.
- Every download is pinned by **SHA-256** in the signed catalog and verified
  after download. A mismatched file is deleted, never executed. For an itch.io
  entry the link is resolved at install time, but the bytes are still pinned.
- Package names are validated against `^[A-Za-z0-9][A-Za-z0-9+._-]*$` and passed
  to the package manager **via `execve`, never through a shell**. A catalog can
  therefore name packages, but cannot smuggle commands.
- Root actions only happen after a yes from you. The install confirmation names
  the packages it will install, so that one yes covers them; `GLP_YES` alone
  does not, and automating `glp install` never implies automating `sudo`.

What this does *not* protect you from: a compromised signing key, or an upstream
vendor replacing a release file at a URL you already trust (SHA-256 pins the file,
not the vendor's intent). Keep the private key offline.

## Catalog format

One JSON document, plus a detached signature (`index.json.sig`, base64):

```json
{
  "schema": 1,
  "updated": "2026-10-28T11:36:00Z",
  "apps": [
    {
      "name": "discord",
      "version": "0.0.84",
      "summary": "Chat for communities",
      "homepage": "https://discord.com",
      "license": "proprietary",
      "archs": ["x86_64"],
      "url": "https://upstream.example/Discord.AppImage",
      "sha256": "…64 hex chars…",
      "size": 98765432,
      "format": "tar.gz",
      "bins": ["usr/bin/discord"],
      "desktop": { "name": "Discord", "icon": "discord", "categories": ["Network"] },
      "deps": { "debian": ["libasound2t64"], "fedora": ["alsa-lib"] }
    }
  ]
}
```

`format` is `appimage` when omitted. Tarballs (`tar.gz`, `tar.xz`, `tar.bz2`,
`tar.zst`) are supported too, because some vendors only ship those — Discord
publishes nothing else. A single wrapping directory is flattened automatically,
and `tar.gz` is unpacked in-process with a path-traversal guard.

Zip archives are supported for vendors that wrap an AppDir or a single AppImage,
and `.deb` for vendors that only publish a Debian package, like Spotify. A deb
is just an `ar` container around `data.tar.*`.

Some games only ship through itch.io, and the download link itch hands out is
signed and expires after about a minute. Those entries name the game page and
the upload id instead of a URL:

```json
"format": "zip",
"itch": { "page": "https://example.itch.io/game", "upload": 123456 },
```

glp replays the same free download flow a browser does, then verifies the bytes
against the pinned SHA-256 like any other entry. The link is dynamic, the
content is not: a different file is still refused.

`icon` points at art inside the archive, for builds that ship a logo but no
desktop file to name it. Games keep their sprites in a sheet, so `crop` cuts one
frame, as `[x, y, width, height]`:

```json
"icon": { "path": "assets/images/icons/icon-bf.png", "crop": [0, 0, 150, 150] },
```

`workdir: true` starts the program from its own directory. Game engines such as
Lime look for the native library in the current directory, not next to the
binary, so the menu entry gets a `Path=` line and the terminal launcher a one
line `cd`. Without it the game starts from the menu and fails from a shell, or
the other way around.

`bins` are paths relative to the AppDir root; a bare name is also accepted and
looked up under `usr/bin`. `deps` is keyed by distro family (`debian`, `fedora`,
`suse`, `arch`) and is usually empty — a well-built AppImage needs nothing.

## Adding an app does not need a new release

The catalog lives outside the binary. `glp search`, `info`, `install` and
`upgrade` check the signed catalog source every time, so publishing a new
`index.json` and `index.json.sig` is enough: users see the new app on their next
command and nothing in the installed program changes. If the source is
unreachable, the last signed copy in the cache is used.

`glp` never swaps its own binary without an explicit yes, and installed apps
never update on their own. Only the catalog refreshes automatically.

## Updating glp

The binary is a static, CGO-free Go program, so one `linux/amd64` build runs on
every distribution where the kernel and CPU are new enough, regardless of glibc,
musl or the package manager. There is nothing distro-specific to update, and no
platform without a matching asset is ever offered an update.

`glp update` fetches `update.json` from the releases page, verifies its ed25519
signature with the same key that signs the catalog, and compares versions:

```console
$ glp update
glp 0.2.0 is available (you have 0.1.0)
faster catalog refresh, deb support
Update now? [Y/n]
glp updated to 0.2.0
```

The new binary's SHA-256 is pinned in the signed manifest, checked after
download, and only then renamed over the running executable. The old inode stays
alive until the process exits, so the update is safe while `glp` is running. If
`glp` lives in a directory you cannot write to, the update fails and names that
path instead of trying to escalate.

If nothing is published at the update URL, which is normal for a build made
from source, `glp update` says so and exits clean instead of reporting a 404.
A source that cannot be reached at all gets one short line, with no host or
status code to read:

```console
$ glp update
no update channel for this build

$ glp update
glp: update servers are not responding
```

`glp update --check` reports the same information and stops without asking.
Daily, on an interactive terminal only, the first `glp` command of the day asks
once. Declining or being offline costs nothing and is not retried until the next
day. `GLP_NO_UPDATE_CHECK=1` disables the automatic check entirely.

The manifest is small and separate from the binaries:

```json
{
  "schema": 1,
  "version": "0.2.0",
  "notes": "faster catalog refresh, deb support",
  "assets": {
    "linux/amd64": {
      "url": "https://github.com/pxradiso/glp/releases/download/v0.2.0/glp-linux-amd64",
      "sha256": "…64 hex chars…",
      "size": 5432109
    }
  }
}
```

Assets are keyed by `GOOS/GOARCH`. An entry that is missing for the current
platform means no update is offered, which is how unsupported systems are
handled without a special case in the code.

### Making a release

```console
$ VERSION=0.2.0 ./scripts/release.sh
```

This builds `linux/amd64` and `linux/arm64`, writes `dist/update.json` with the
URL and checksum of each, and signs it. Upload the two binaries, `update.json`
and `update.json.sig` to the `v0.2.0` GitHub release; the default
`releases/latest/download/update.json` URL then serves the new version.

## Signing

```console
# once, offline
$ go run ./tools/sign keygen ~/.config/glp/dev.key
# copy the printed public key into embeddedPubKey in trust.go, then rebuild

# every time the catalog changes
$ go run ./tools/sign sign ~/.config/glp/dev.key catalog/index.json

# release manifests are signed the same way
$ go run ./tools/sign sign ~/.config/glp/dev.key dist/update.json
```

Never commit the private key. Rotating it requires shipping a new `glp`.

## Limitations worth knowing

- **glibc.** A binary built on a recent distro will not run on an older one.
  Build AppImages inside an old container (Debian oldstable) — that is the whole
  trick behind "runs anywhere".
- **Dead links.** Because binaries come from upstream, a moved release breaks the
  entry until the catalog is refreshed. `glp` fails loudly rather than silently.
- **itch.io entries.** The link itch hands out expires in about a minute, so glp
  replays the page flow at install time. If itch changes that flow the install
  stops with an error; the pinned SHA-256 still guarantees the bytes.
- **Debian needs `apt-file`** for `glp doctor` to map a soname to a package.
  Without it the missing libraries are still listed, just not auto-installed.
- **No portals.** File choosers and notifications come from the host desktop.
  That is fine for normal apps and is exactly the complexity `glp` avoids.
- **Bootstrapper apps.** Discord's official tarball is a ~2 MB launcher that
  downloads the real application (hundreds of MB) into `~/.config/discord` on
  first start. `glp remove` cleans up what `glp` installed, not that payload.
- **Download cache.** `~/.cache/glp/downloads` keeps the archive of an installed
  app, so a repair does not download it again. `glp index refresh` clears
  anything that no longer belongs to an installed app.
- **Desktop cache.** If a menu entry does not appear immediately, run
  `update-desktop-database ~/.local/share/applications`.
