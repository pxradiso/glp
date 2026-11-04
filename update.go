package main

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"time"
)

const defaultUpdateURL = "https://github.com/pxradiso/glp/releases/latest/download/update.json"

// updateCheckAge is how often an interactive run may ask about a new release.
const updateCheckAge = 24 * time.Hour

var updateClient = &http.Client{
	Timeout:   10 * time.Second,
	Transport: httpClient.Transport,
}

// UpdateManifest is published next to the release binaries, signed with the
// same offline key as the catalog. Assets are keyed by GOOS/GOARCH, so the
// updater only offers a build that exists for the current platform. Because
// glp is a static Go binary, one linux/amd64 asset covers every distribution.
type UpdateManifest struct {
	Schema  int              `json:"schema"`
	Version string           `json:"version"`
	Notes   string           `json:"notes,omitempty"`
	Assets  map[string]Asset `json:"assets"`
}

type Asset struct {
	URL    string `json:"url"`
	SHA256 string `json:"sha256"`
	Size   int64  `json:"size"`
}

func updateURL() string {
	if u := strings.TrimSpace(os.Getenv("GLP_UPDATE_URL")); u != "" {
		return u
	}
	if b, err := os.ReadFile(filepath.Join(configDir(), "config.json")); err == nil {
		var c struct {
			UpdateURL string `json:"update_url"`
		}
		if json.Unmarshal(b, &c) == nil && strings.TrimSpace(c.UpdateURL) != "" {
			return c.UpdateURL
		}
	}
	return defaultUpdateURL
}

func platformKey() string {
	return runtime.GOOS + "/" + runtime.GOARCH
}

// noRelease means the update channel exists in the binary but nothing is
// published there yet, which is normal for a build made from source.
type noRelease struct{ url string }

func (e *noRelease) Error() string { return "no update manifest at " + e.url }

// fetchUpdateManifest downloads and verifies update.json. The signature is
// checked with the key embedded in the binary, exactly like the catalog.
func fetchUpdateManifest() (*UpdateManifest, error) {
	u := updateURL()
	payload, err := fetchBytesClient(updateClient, u)
	if err != nil {
		var se *statusError
		if errors.As(err, &se) && se.Code == http.StatusNotFound {
			return nil, &noRelease{url: u}
		}
		return nil, err
	}
	sig, err := fetchBytesClient(updateClient, u+".sig")
	if err != nil {
		return nil, fmt.Errorf("cannot fetch update signature: %w", err)
	}
	if err := verifySignature(payload, sig); err != nil {
		return nil, err
	}

	var m UpdateManifest
	if err := json.Unmarshal(payload, &m); err != nil {
		return nil, fmt.Errorf("update manifest is corrupt: %w", err)
	}
	if m.Schema != 1 {
		return nil, fmt.Errorf("unsupported update schema %d", m.Schema)
	}
	if strings.TrimSpace(m.Version) == "" {
		return nil, errors.New("update manifest has no version")
	}
	return &m, nil
}

// reportAppUpdates refreshes the catalog and prints the installed apps that
// have a newer version. It reports whether anything was listed; installing is
// left to `glp upgrade`, with its own confirmation per app.
func reportAppUpdates() bool {
	idx, err := loadIndexFresh()
	if err != nil {
		fmt.Fprintf(os.Stderr, "glp: cannot check app updates: %v\n", err)
		return false
	}
	ups := pendingUpdates(idx)
	if len(ups) == 0 {
		fmt.Println("all apps are up to date")
		return false
	}
	title := fmt.Sprintf("%d apps can be updated", len(ups))
	if len(ups) == 1 {
		title = "1 app can be updated"
	}
	fmt.Printf("%s\n\n", outStyle("1", title))
	rows := make([][]string, 0, len(ups))
	for _, u := range ups {
		rows = append(rows, []string{u.Name, u.Installed, u.Available, humanBytes(u.Size)})
	}
	writeTable(os.Stdout, "  ", []string{"app", "installed", "available", "size"}, rows, 2, 3)
	fmt.Printf("\nupgrade: %s\n", outStyle("36", "glp upgrade"))
	return true
}

// unreachable reports whether the update source could not be talked to at all:
// a network failure or any HTTP status that is not a normal answer.
func unreachable(err error) bool {
	var se *statusError
	var ue *url.Error
	return errors.As(err, &se) || errors.As(err, &ue)
}

func cmdUpdate(args []string) error {
	checkOnly := false
	if len(args) > 0 {
		switch args[0] {
		case "--check", "-c":
			checkOnly = true
		default:
			return errors.New("usage: glp update [--check]")
		}
	}

	// Apps first: the catalog check is quick and the table is the part that
	// tells you what `glp upgrade` would do.
	if reportAppUpdates() {
		fmt.Println()
	}

	m, err := fetchUpdateManifest()
	if err != nil {
		var nr *noRelease
		switch {
		case errors.As(err, &nr):
			fmt.Println("no update channel for this build")
			return nil
		case unreachable(err):
			return errors.New("update servers are not responding")
		}
		return err
	}
	asset, ok := m.Assets[platformKey()]
	if !ok {
		return fmt.Errorf("no glp build for %s in the update manifest", platformKey())
	}
	if compareVersions(m.Version, version) <= 0 {
		fmt.Printf("glp %s is up to date\n", outStyle("2", version))
		return nil
	}

	fmt.Printf("%s is available (you have %s)\n", outStyle("1", "glp "+m.Version), version)
	if m.Notes != "" {
		fmt.Printf("  %s\n", m.Notes)
	}
	if checkOnly {
		return nil
	}
	if !confirmEnv("GLP_YES_UPDATE", "Update now?") {
		fmt.Println(outStyle("2", "nothing changed"))
		return nil
	}
	if err := applyUpdate(asset); err != nil {
		return err
	}
	fmt.Printf("%s\n", outStyle("32", "glp updated to "+m.Version))
	return nil
}

// applyUpdate downloads the new binary, verifies its pinned SHA-256 and swaps
// it over the running executable. The rename is atomic and the old inode stays
// alive until this process exits.
func applyUpdate(a Asset) error {
	if len(a.SHA256) != 64 {
		return errors.New("update asset has no valid sha256")
	}
	exe, err := os.Executable()
	if err != nil {
		return err
	}
	if real, err := filepath.EvalSymlinks(exe); err == nil {
		exe = real
	}

	data, err := fetchBytesClient(updateClient, a.URL)
	if err != nil {
		return err
	}
	if a.Size > 0 && int64(len(data)) != a.Size {
		return fmt.Errorf("downloaded %d bytes, expected %d", len(data), a.Size)
	}
	sum := sha256.Sum256(data)
	if !strings.EqualFold(hex.EncodeToString(sum[:]), a.SHA256) {
		return errors.New("update checksum does not match, refusing to install")
	}

	tmp, err := os.CreateTemp(filepath.Dir(exe), ".glp-update-*")
	if err != nil {
		return fmt.Errorf("cannot write next to %s: %w", exe, err)
	}
	tmpName := tmp.Name()
	defer os.Remove(tmpName)
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Chmod(0o755); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if err := os.Rename(tmpName, exe); err != nil {
		return err
	}
	return nil
}

func updateCheckDue() bool {
	fi, err := os.Stat(filepath.Join(cacheDir(), "update-check"))
	if err != nil {
		return true
	}
	return time.Since(fi.ModTime()) > updateCheckAge
}

func markUpdateChecked() {
	_ = mustMkdirAll(cacheDir())
	_ = writeFileAtomic(filepath.Join(cacheDir(), "update-check"), []byte(time.Now().UTC().Format(time.RFC3339)+"\n"))
}

// maybeCheckUpdate runs at most once a day on an interactive terminal. It only
// asks, never installs without an explicit yes. Failures are silent: an
// offline machine must not be nagged or slowed down.
func maybeCheckUpdate(cmd string) {
	switch cmd {
	case "update", "version", "--version", "-v", "help", "--help", "-h":
		return
	}
	if os.Getenv("GLP_NO_UPDATE_CHECK") == "1" || !isTTY(os.Stdin) || !updateCheckDue() {
		return
	}
	// Stamp first: a broken or unreachable source must not add a timeout to
	// every command for the rest of the day.
	markUpdateChecked()

	m, err := fetchUpdateManifest()
	if err != nil {
		return
	}
	asset, ok := m.Assets[platformKey()]
	if !ok || compareVersions(m.Version, version) <= 0 {
		return
	}
	fmt.Printf("\n%s is available (you have %s)\n", outStyle("1", "glp "+m.Version), version)
	if !confirmEnv("GLP_YES_UPDATE", "Update now?") {
		return
	}
	if err := applyUpdate(asset); err != nil {
		fmt.Fprintf(os.Stderr, "glp: update failed: %v\n", err)
		return
	}
	fmt.Printf("%s\n\n", outStyle("32", "glp updated to "+m.Version))
}
