package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/cookiejar"
	"net/url"
	"regexp"
	"strings"
)

// Itch describes a file that only exists behind itch.io's free download flow.
// The game page and the upload id are stable, but the CDN link itch hands out
// carries a signature that lasts about a minute, so it is resolved at install
// time. The catalog still pins the SHA-256 of the bytes themselves.
type Itch struct {
	Page   string `json:"page"`
	Upload int64  `json:"upload"`
}

var csrfToken = regexp.MustCompile(`name="csrf_token"\s+(?:value|content)="([^"]*)"`)

// itchFileURL walks the same requests a browser makes: game page, purchase
// page, download session, then the per-file endpoint.
func itchFileURL(app *App) (string, error) {
	if app.Itch == nil {
		return "", errors.New("not an itch entry")
	}
	page := strings.TrimRight(strings.TrimSpace(app.Itch.Page), "/")
	if page == "" {
		return "", fmt.Errorf("%s: itch entry has no page", app.Name)
	}
	if app.Itch.Upload <= 0 {
		return "", fmt.Errorf("%s: itch entry has no upload id", app.Name)
	}

	jar, err := cookiejar.New(nil)
	if err != nil {
		return "", err
	}
	client := *httpClient
	client.Jar = jar
	// 1. The game page sets the visitor cookie the rest of the flow expects.
	if _, err := itchDo(&client, http.MethodGet, page, "", ""); err != nil {
		return "", err
	}

	// 2. The purchase page carries the csrf token for the download endpoints.
	purchase, err := itchDo(&client, http.MethodGet, page+"/purchase", "", "")
	if err != nil {
		return "", err
	}
	token := csrfToken.FindStringSubmatch(purchase)
	if len(token) < 2 {
		return "", fmt.Errorf("%s: itch page has no csrf token", app.Name)
	}

	// 3. Ask for a download session. This is the "no thanks, just take me to
	// the downloads" path, so no account is involved.
	body, err := itchDo(&client, http.MethodPost, page+"/download_url", token[1], "")
	if err != nil {
		return "", err
	}
	var session struct {
		URL string `json:"url"`
	}
	if err := json.Unmarshal([]byte(body), &session); err != nil || session.URL == "" {
		return "", fmt.Errorf("%s: itch did not return a download session", app.Name)
	}

	// 4. Opening the session page authorises the file request and gives us a
	// fresh csrf token.
	downloadPage, err := itchDo(&client, http.MethodGet, session.URL, "", "")
	if err != nil {
		return "", err
	}
	token = csrfToken.FindStringSubmatch(downloadPage)
	if len(token) < 2 {
		return "", fmt.Errorf("%s: itch download page has no csrf token", app.Name)
	}

	// 5. The file endpoint answers with a short lived signed URL.
	body, err = itchDo(&client, http.MethodPost,
		fmt.Sprintf("%s/file/%d", page, app.Itch.Upload), token[1], session.URL)
	if err != nil {
		return "", err
	}
	var file struct {
		URL string `json:"url"`
	}
	if err := json.Unmarshal([]byte(body), &file); err != nil || file.URL == "" {
		return "", fmt.Errorf("%s: itch did not return a file link", app.Name)
	}
	return file.URL, nil
}

// itchDo performs one request of the flow. token is the csrf form value for
// POSTs, referer is set on the file request so itch sees the session page.
func itchDo(client *http.Client, method, target, token, referer string) (string, error) {
	var body io.Reader
	if method == http.MethodPost {
		form := url.Values{"csrf_token": {token}}
		if strings.HasSuffix(target, "/download_url") {
			form.Set("reward_id", "")
		}
		body = strings.NewReader(form.Encode())
	}
	req, err := http.NewRequest(method, target, body)
	if err != nil {
		return "", err
	}
	req.Header.Set("User-Agent", "Mozilla/5.0 (X11; Linux x86_64) glp/"+version)
	if method == http.MethodPost {
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		req.Header.Set("X-Requested-With", "XMLHttpRequest")
	}
	if referer != "" {
		req.Header.Set("Referer", referer)
	}

	resp, err := client.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("itch.io: HTTP %s", resp.Status)
	}
	b, err := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	return string(b), err
}
