package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

type Meta struct {
	Name        string    `json:"name"`
	Version     string    `json:"version"`
	URL         string    `json:"url"`
	SHA256      string    `json:"sha256"`
	Bins        []string  `json:"bins"`
	DesktopFile string    `json:"desktop_file,omitempty"`
	IconFile    string    `json:"icon_file,omitempty"`
	Size        int64     `json:"size,omitempty"`
	InstalledAt time.Time `json:"installed_at"`
}

func metaPath(name string) string {
	return filepath.Join(dataDir(), name, "meta.json")
}

func readMeta(name string) *Meta {
	b, err := os.ReadFile(metaPath(name))
	if err != nil {
		return nil
	}
	var m Meta
	if err := json.Unmarshal(b, &m); err != nil {
		return nil
	}
	return &m
}

func writeMeta(m Meta) error {
	b, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		return err
	}
	return writeFileAtomic(metaPath(m.Name), append(b, '\n'))
}

// removeTree deletes an app directory one entry at a time so the removal can be
// shown as progress. It refuses to touch anything outside the glp tree.
func removeTree(dir string) error {
	clean := filepath.Clean(dir)
	if clean == filepath.Clean(dataDir()) || !strings.HasPrefix(clean, filepath.Clean(dataDir())+string(os.PathSeparator)) {
		return fmt.Errorf("refusing to delete %s: outside %s", dir, dataDir())
	}

	var paths []string
	filepath.WalkDir(dir, func(p string, d fs.DirEntry, err error) error {
		if err == nil {
			paths = append(paths, p)
		}
		return nil
	})

	bar := newCountBar(len(paths), true)
	for i := len(paths) - 1; i >= 0; i-- {
		os.Remove(paths[i])
		bar.Update(len(paths) - i)
	}
	bar.Done()

	// Anything that survived (permissions, races) gets a final sweep.
	return os.RemoveAll(dir)
}

// binsIntact reports whether every launcher recorded in meta still exists.
func binsIntact(m *Meta) bool {
	for _, b := range m.Bins {
		if _, err := os.Lstat(filepath.Join(binDir(), binName(b, b))); err != nil {
			return false
		}
	}
	return true
}

// AppUpdate is one installed app whose catalog entry moved ahead.
type AppUpdate struct {
	Name      string
	Installed string
	Available string
	Size      int64
}

// pendingUpdates lists installed apps with a newer version in the catalog.
// Nothing is downloaded here: `glp upgrade` does the work.
func pendingUpdates(idx *Index) []AppUpdate {
	var out []AppUpdate
	for _, m := range installedMetas() {
		app, err := idx.Find(m.Name)
		if err != nil {
			continue
		}
		if compareVersions(app.Version, m.Version) > 0 {
			out = append(out, AppUpdate{
				Name:      app.Name,
				Installed: m.Version,
				Available: app.Version,
				Size:      app.Size,
			})
		}
	}
	return out
}

func installedMetas() []Meta {
	entries, err := os.ReadDir(dataDir())
	if err != nil {
		return nil
	}
	var out []Meta
	for _, e := range entries {
		if !e.IsDir() || strings.HasPrefix(e.Name(), ".") {
			continue
		}
		if m := readMeta(e.Name()); m != nil {
			out = append(out, *m)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

func cmdList(args []string) error {
	metas := installedMetas()
	if len(metas) == 0 {
		fmt.Println("no apps installed")
		return nil
	}
	fmt.Printf("%-22s %-12s %10s  %s\n", "APP", "VERSION", "SIZE", "INSTALLED")
	for _, m := range metas {
		fmt.Printf("%-22s %-12s %10s  %s\n",
			m.Name, m.Version, humanBytes(m.Size), m.InstalledAt.Format("2006-01-02"))
	}
	return nil
}

func cmdRemove(args []string) error {
	if len(args) != 1 {
		return errors.New("usage: glp remove <app>")
	}
	name, err := resolveInstalled(args[0])
	if err != nil {
		return err
	}
	m := readMeta(name)
	if m == nil {
		return fmt.Errorf("%s is not installed", name)
	}
	const steps = 3
	root := filepath.Join(dataDir(), name)

	s1 := startStep(1, steps, "removing launchers", true)
	for _, b := range m.Bins {
		// Only remove launchers that really point into this app's tree.
		removeLauncher(filepath.Join(binDir(), binName(b, b)), root)
	}
	s1.Done()

	s2 := startStep(2, steps, "removing menu entry and icon", true)
	if m.DesktopFile != "" && strings.HasPrefix(filepath.Clean(m.DesktopFile), appsDir()) {
		os.Remove(m.DesktopFile)
	}
	if m.IconFile != "" && strings.HasPrefix(filepath.Clean(m.IconFile), iconsDir()) {
		os.Remove(m.IconFile)
	}
	s2.Done()

	// Deletion draws its own file counter, so no marching animation here.
	printStep(3, steps, "deleting files")
	if err := removeTree(root); err != nil {
		return err
	}

	fmt.Printf("%s removed\n", name)
	return nil
}

func cmdUpgrade(args []string) error {
	idx, err := loadIndexFresh()
	if err != nil {
		return err
	}

	var targets []*App
	if len(args) == 1 {
		app, err := resolveApp(idx, args[0])
		if err != nil {
			return err
		}
		targets = append(targets, app)
	} else if len(args) > 1 {
		return errors.New("usage: glp upgrade [app]")
	} else {
		for _, m := range installedMetas() {
			app, err := idx.Find(m.Name)
			if err != nil {
				fmt.Fprintf(os.Stderr, "glp: %s is installed but no longer in the catalog, skipping\n", m.Name)
				continue
			}
			if compareVersions(app.Version, m.Version) > 0 {
				targets = append(targets, app)
			}
		}
	}

	if len(targets) == 0 {
		fmt.Println("everything is up to date")
		return nil
	}
	for _, app := range targets {
		cur := ""
		if m := readMeta(app.Name); m != nil {
			cur = m.Version
		}
		if cur == app.Version {
			fmt.Printf("%s is already at %s\n", app.Name, cur)
			continue
		}
		fmt.Printf("\n%s: %s -> %s\n", app.Name, cur, app.Version)
		if err := cmdInstall([]string{app.Name}); err != nil {
			return err
		}
	}
	return nil
}
