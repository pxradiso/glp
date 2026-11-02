package main

import (
	"os"
	"path/filepath"
)

func homeDir() string {
	if h, err := os.UserHomeDir(); err == nil {
		return h
	}
	return os.Getenv("HOME")
}

// configDir holds user settings and the (private) dev signing key.
func configDir() string { return filepath.Join(homeDir(), ".config", "glp") }

// dataDir is where extracted AppDirs live, one directory per app.
func dataDir() string { return filepath.Join(homeDir(), ".local", "opt", "glp") }

func binDir() string   { return filepath.Join(homeDir(), ".local", "bin") }
func appsDir() string  { return filepath.Join(homeDir(), ".local", "share", "applications") }
func iconsDir() string { return filepath.Join(homeDir(), ".local", "share", "icons", "hicolor") }

func cacheDir() string    { return filepath.Join(homeDir(), ".cache", "glp") }
func downloadDir() string { return filepath.Join(cacheDir(), "downloads") }
func indexCache() string  { return filepath.Join(cacheDir(), "index.json") }
func sigCache() string    { return filepath.Join(cacheDir(), "index.json.sig") }

func mustMkdirAll(dir string) error {
	return os.MkdirAll(dir, 0o755)
}
