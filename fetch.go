package main

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

const defaultIndexURL = "https://raw.githubusercontent.com/pxradiso/glp-catalog/main/index.json"

const indexMaxAge = 24 * time.Hour

var httpClient = &http.Client{
	Transport: &http.Transport{
		DialContext:           (&net.Dialer{Timeout: 15 * time.Second}).DialContext,
		TLSHandshakeTimeout:   15 * time.Second,
		ResponseHeaderTimeout: 30 * time.Second,
	},
}

func indexURL() string {
	if u := strings.TrimSpace(os.Getenv("GLP_INDEX_URL")); u != "" {
		return u
	}
	if b, err := os.ReadFile(filepath.Join(configDir(), "config.json")); err == nil {
		var c struct {
			IndexURL string `json:"index_url"`
		}
		if json.Unmarshal(b, &c) == nil && strings.TrimSpace(c.IndexURL) != "" {
			return c.IndexURL
		}
	}
	return defaultIndexURL
}

// localPath reports whether url points at a local file (plain path or file://).
func localPath(url string) (string, bool) {
	if rest, ok := strings.CutPrefix(url, "file://"); ok {
		return rest, true
	}
	if !strings.Contains(url, "://") {
		return url, true
	}
	return "", false
}

func fetchBytes(url string) ([]byte, error) {
	return fetchBytesClient(httpClient, url)
}

// statusError is a non-200 answer from a remote source. Keeping the code lets
// callers tell "this file does not exist" from "the network is down".
type statusError struct {
	URL  string
	Code int
}

func (e *statusError) Error() string {
	return fmt.Sprintf("%s: HTTP %d %s", e.URL, e.Code, http.StatusText(e.Code))
}

func fetchBytesClient(client *http.Client, url string) ([]byte, error) {
	if path, ok := localPath(url); ok {
		return os.ReadFile(path)
	}
	if client == nil {
		client = httpClient
	}
	req, err := http.NewRequest(http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", "glp/"+version)
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, &statusError{URL: url, Code: resp.StatusCode}
	}
	return io.ReadAll(resp.Body)
}

func indexURLCache() string { return filepath.Join(cacheDir(), "index.url") }

// cacheSourceMatches reports whether the cached catalog came from the source
// currently configured. Without it, switching catalogs would silently serve
// the previous one.
func cacheSourceMatches() bool {
	b, err := os.ReadFile(indexURLCache())
	if err != nil {
		return false
	}
	return strings.TrimSpace(string(b)) == indexURL()
}

func cacheValid() bool {
	return cacheSourceMatches() && !stale(indexCache())
}

func refreshIndex() error {
	u := indexURL()
	payload, err := fetchBytes(u)
	if err != nil {
		return err
	}
	sig, err := fetchBytes(u + ".sig")
	if err != nil {
		return fmt.Errorf("cannot fetch catalog signature: %w", err)
	}
	if err := verifySignature(payload, sig); err != nil {
		return err
	}
	if err := mustMkdirAll(cacheDir()); err != nil {
		return err
	}
	if err := writeFileAtomic(indexCache(), payload); err != nil {
		return err
	}
	if err := writeFileAtomic(sigCache(), sig); err != nil {
		return err
	}
	return writeFileAtomic(indexURLCache(), []byte(u+"\n"))
}

func writeFileAtomic(dest string, data []byte) error {
	if err := mustMkdirAll(filepath.Dir(dest)); err != nil {
		return err
	}
	tmp := dest + ".tmp"
	if err := os.WriteFile(tmp, data, 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, dest)
}

func fileExists(p string) bool {
	_, err := os.Stat(p)
	return err == nil
}

func stale(p string) bool {
	fi, err := os.Stat(p)
	if err != nil {
		return true
	}
	return time.Since(fi.ModTime()) > indexMaxAge
}

// loadIndex returns the catalog, refreshing it when the cached copy is a day
// old. The signature is always verified, including for a cached copy.
func loadIndex(force bool) (*Index, error) {
	if force || !cacheValid() {
		if err := refreshIndex(); err != nil {
			if force || !fileExists(indexCache()) {
				return nil, err
			}
			fmt.Fprintf(os.Stderr, "glp: warning: catalog refresh failed (%v), using cached copy\n", err)
		}
	}
	return readIndex()
}

// loadIndexFresh always checks the catalog source first, so a newly published
// app shows up on the next search or install without a new glp release. If the
// source is unreachable, the signed cached copy is used instead.
func loadIndexFresh() (*Index, error) {
	if err := refreshIndex(); err != nil {
		if !fileExists(indexCache()) || !cacheSourceMatches() {
			return nil, err
		}
		fmt.Fprintf(os.Stderr, "glp: warning: catalog refresh failed (%v), using cached copy\n", err)
	}
	return readIndex()
}

func readIndex() (*Index, error) {
	payload, err := os.ReadFile(indexCache())
	if err != nil {
		return nil, fmt.Errorf("no catalog, run `glp index refresh`: %w", err)
	}
	sig, err := os.ReadFile(sigCache())
	if err != nil {
		return nil, fmt.Errorf("catalog signature missing: %w", err)
	}
	if err := verifySignature(payload, sig); err != nil {
		return nil, err
	}

	var idx Index
	if err := json.Unmarshal(payload, &idx); err != nil {
		return nil, fmt.Errorf("catalog is corrupt: %w", err)
	}
	if idx.Schema != 1 {
		return nil, fmt.Errorf("unsupported catalog schema %d (this glp understands schema 1)", idx.Schema)
	}
	return &idx, nil
}

// ensureBlob returns a local path to the verified AppImage, downloading it if
// necessary. The on-disk name is the checksum, which gives us dedup for free.
func ensureBlob(app *App) (string, error) {
	if err := mustMkdirAll(downloadDir()); err != nil {
		return "", err
	}
	dest := filepath.Join(downloadDir(), strings.ToLower(app.SHA256)+blobExt(app))
	// Older glp versions used a fixed .AppImage suffix.
	if legacy := filepath.Join(downloadDir(), strings.ToLower(app.SHA256)+".AppImage"); legacy != dest {
		if ok, _ := hashMatches(legacy, app.SHA256); ok {
			os.Rename(legacy, dest)
		}
	}
	if ok, _ := hashMatches(dest, app.SHA256); ok {
		fmt.Fprintf(os.Stderr, "  using cached download\n")
		return dest, nil
	}
	if err := downloadApp(app, dest); err != nil {
		return "", err
	}
	return dest, nil
}

// downloadApp fetches the catalog URL, or resolves the short lived link an
// itch entry needs. The itch link expires after about a minute, so a failed
// attempt gets one fresh link before giving up.
func downloadApp(app *App, dest string) error {
	if app.Itch == nil {
		return downloadTo(app.URL, dest, app.Size, app.SHA256)
	}
	first, err := itchFileURL(app)
	if err != nil {
		return err
	}
	err = downloadTo(first, dest, app.Size, app.SHA256)
	if err == nil {
		return err
	}
	second, rerr := itchFileURL(app)
	if rerr != nil || second == first {
		return err
	}
	fmt.Fprintf(os.Stderr, "  link expired, asking itch.io again\n")
	return downloadTo(second, dest, app.Size, app.SHA256)
}

// blobExt keeps the cache honest about what is inside each file.
func blobExt(app *App) string {
	if ext := urlExt(app.URL); ext != "" {
		return ext
	}
	if app.Itch != nil && app.Format != "" {
		return "." + strings.ToLower(strings.TrimPrefix(app.Format, "."))
	}
	return ".bin"
}

// downloadLabel names the blob in progress messages. An itch entry has no
// filename until the flow resolves one, so it says where it comes from.
func downloadLabel(app *App) string {
	if app.Itch != nil {
		return "from itch.io"
	}
	return filepath.Base(app.URL)
}

func urlExt(rawURL string) string {
	base := strings.ToLower(rawURL)
	if i := strings.IndexAny(base, "?#"); i >= 0 {
		base = base[:i]
	}
	for _, ext := range []string{".tar.gz", ".tar.xz", ".tar.bz2", ".tar.zst", ".tar", ".appimage", ".zip", ".deb"} {
		if strings.HasSuffix(base, ext) {
			return ext
		}
	}
	return ""
}

// pruneDownloads drops cached blobs that are not needed by an installed app.
// A cached file only saves a download when you reinstall or repair the exact
// same version, so keeping every historical blob just eats disk.
func pruneDownloads(idx *Index) {
	keep := make(map[string]bool, len(idx.Apps))
	for _, a := range idx.Apps {
		m := readMeta(a.Name)
		if m == nil || compareVersions(m.Version, a.Version) != 0 {
			continue
		}
		keep[strings.ToLower(a.SHA256)] = true
	}
	entries, err := os.ReadDir(downloadDir())
	if err != nil {
		return
	}
	var freed int64
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		name := e.Name()
		key := name
		if i := strings.IndexByte(key, '.'); i > 0 {
			key = key[:i]
		}
		if keep[key] || strings.HasSuffix(name, ".part") {
			continue
		}
		if fi, err := e.Info(); err == nil {
			freed += fi.Size()
		}
		os.Remove(filepath.Join(downloadDir(), name))
	}
	if freed > 0 {
		fmt.Fprintf(os.Stderr, "  freed %s from the download cache\n", humanBytes(freed))
	}
}

const downloadAttempts = 3

// retryError marks a failed attempt that is worth repeating from the current
// .part file, for example a server that answered a range request strangely.
type retryError struct{ err error }

func (e *retryError) Error() string { return e.err.Error() }
func (e *retryError) Unwrap() error { return e.err }

// checksumError is a finished transfer whose bytes do not match the catalog.
// resumed means the bad data may have come from an old partial file, so one
// clean retry from zero is allowed.
type checksumError struct {
	want, got string
	resumed   bool
}

func (e *checksumError) Error() string {
	return fmt.Sprintf("checksum mismatch, not installing\n  expected %s\n  got      %s", e.want, e.got)
}

func downloadTo(rawURL, dest string, size int64, wantSHA string) error {
	if path, ok := localPath(rawURL); ok {
		src, err := os.Open(path)
		if err != nil {
			return err
		}
		defer src.Close()
		return savePart(src, dest, 0, size, wantSHA)
	}

	var last error
	for attempt := 1; attempt <= downloadAttempts; attempt++ {
		err := fetchToPart(rawURL, dest, size, wantSHA)
		if err == nil {
			return nil
		}
		last = err
		if !retryableDownload(err) {
			return err
		}
		if attempt < downloadAttempts {
			time.Sleep(time.Duration(attempt) * 400 * time.Millisecond)
		}
	}
	return last
}

// fetchToPart downloads one attempt, resuming from .part when the server
// supports ranges. A 200 answer means the server ignored the range and the
// partial file is restarted from zero.
func fetchToPart(rawURL, dest string, size int64, wantSHA string) error {
	part := dest + ".part"
	off := partSize(part)
	if size > 0 && off > size {
		os.Remove(part)
		off = 0
	}
	if size > 0 && off == size {
		if ok, _ := hashMatches(part, wantSHA); ok {
			return os.Rename(part, dest)
		}
		os.Remove(part)
		off = 0
	}

	req, err := http.NewRequest(http.MethodGet, rawURL, nil)
	if err != nil {
		return err
	}
	req.Header.Set("User-Agent", "glp/"+version)
	if off > 0 {
		req.Header.Set("Range", fmt.Sprintf("bytes=%d-", off))
	}
	resp, err := httpClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	start := off
	total := size
	switch resp.StatusCode {
	case http.StatusPartialContent:
		rstart, _, rtotal, ok := parseContentRange(resp.Header.Get("Content-Range"))
		if !ok || rstart != off {
			os.Remove(part)
			return &retryError{errors.New("server returned an unexpected range")}
		}
		if rtotal > 0 {
			total = rtotal
		}
	case http.StatusOK:
		start = 0
	case http.StatusRequestedRangeNotSatisfiable:
		os.Remove(part)
		return &retryError{errors.New("server rejected the resume range")}
	default:
		return &statusError{URL: rawURL, Code: resp.StatusCode}
	}
	if resp.ContentLength >= 0 {
		if t := resp.ContentLength + start; t > 0 {
			total = t
		}
	}
	return savePart(resp.Body, dest, start, total, wantSHA)
}

func partSize(path string) int64 {
	fi, err := os.Stat(path)
	if err != nil || fi.IsDir() {
		return 0
	}
	return fi.Size()
}

// parseContentRange reads "bytes 1024-2047/4096". A "*" total leaves total at
// zero, which the progress bar treats as unknown.
func parseContentRange(v string) (start, end, total int64, ok bool) {
	v = strings.TrimSpace(v)
	if !strings.HasPrefix(v, "bytes ") {
		return 0, 0, 0, false
	}
	rng, tot, found := strings.Cut(strings.TrimSpace(strings.TrimPrefix(v, "bytes ")), "/")
	if !found {
		return 0, 0, 0, false
	}
	s, e, found := strings.Cut(rng, "-")
	if !found {
		return 0, 0, 0, false
	}
	start, err := strconv.ParseInt(s, 10, 64)
	if err != nil {
		return 0, 0, 0, false
	}
	end, err = strconv.ParseInt(e, 10, 64)
	if err != nil || end < start {
		return 0, 0, 0, false
	}
	if tot != "*" {
		total, err = strconv.ParseInt(tot, 10, 64)
		if err != nil || total <= end {
			return 0, 0, 0, false
		}
	}
	return start, end, total, true
}

// savePart writes the body to dest.part, hashing as it goes. On a network
// failure the partial file stays, so the next attempt can resume; on a
// checksum mismatch it is deleted because the bytes are not trustworthy.
func savePart(src io.Reader, dest string, start, total int64, wantSHA string) error {
	part := dest + ".part"
	if err := mustMkdirAll(filepath.Dir(dest)); err != nil {
		return err
	}
	flags := os.O_CREATE | os.O_WRONLY
	if start > 0 {
		flags |= os.O_APPEND
	} else {
		flags |= os.O_TRUNC
	}
	f, err := os.OpenFile(part, flags, 0o644)
	if err != nil {
		return err
	}

	hasher := sha256.New()
	if start > 0 {
		rf, err := os.Open(part)
		if err != nil {
			f.Close()
			return err
		}
		_, err = io.Copy(hasher, rf)
		rf.Close()
		if err != nil {
			f.Close()
			return err
		}
	}

	pr := newProgressAt(src, total, start)
	_, copyErr := io.Copy(io.MultiWriter(f, hasher), pr)
	pr.Done()
	if copyErr != nil {
		f.Close()
		return copyErr
	}
	if err := f.Sync(); err != nil {
		f.Close()
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}

	got := hex.EncodeToString(hasher.Sum(nil))
	if !strings.EqualFold(got, wantSHA) {
		os.Remove(part)
		return &checksumError{want: wantSHA, got: got, resumed: start > 0}
	}
	return os.Rename(part, dest)
}

func retryableDownload(err error) bool {
	var ce *checksumError
	if errors.As(err, &ce) {
		return ce.resumed
	}
	var re *retryError
	if errors.As(err, &re) {
		return true
	}
	var se *statusError
	if errors.As(err, &se) {
		return se.Code == http.StatusRequestTimeout ||
			se.Code == http.StatusTooManyRequests ||
			se.Code >= 500
	}
	var ue *url.Error
	if errors.As(err, &ue) {
		return true
	}
	var ne net.Error
	if errors.As(err, &ne) {
		return true
	}
	return errors.Is(err, io.ErrUnexpectedEOF)
}

func hashMatches(path, wantSHA string) (bool, error) {
	f, err := os.Open(path)
	if err != nil {
		return false, err
	}
	defer f.Close()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return false, err
	}
	return strings.EqualFold(hex.EncodeToString(h.Sum(nil)), wantSHA), nil
}
