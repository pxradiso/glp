package main

import (
	"encoding/json"
	"fmt"
	"path/filepath"
	"strings"
)

func cmdSearch(args []string) error {
	idx, err := loadIndexFresh()
	if err != nil {
		return err
	}
	term := strings.Join(args, " ")
	results := idx.Search(term)
	if len(results) == 0 {
		fmt.Printf("nothing matches %q\n", term)
		return nil
	}
	fmt.Printf("%-22s %-12s %8s  %s\n", "APP", "VERSION", "SIZE", "SUMMARY")
	for _, a := range results {
		fmt.Printf("%-22s %-12s %8s  %s\n", a.Name, a.Version,
			humanBytes(a.Size), truncate(a.Summary, 48))
	}
	if len(results) > 0 && term != "" {
		fmt.Printf("\ninstall with: glp install %s\n", results[0].Name)
	}
	return nil
}

func cmdInfo(args []string) error {
	if len(args) != 1 {
		return fmt.Errorf("usage: glp info <app>")
	}
	idx, err := loadIndexFresh()
	if err != nil {
		return err
	}
	app, err := resolveApp(idx, args[0])
	if err != nil {
		return err
	}

	fmt.Printf("%s %s\n", app.Name, app.Version)
	if app.Summary != "" {
		fmt.Printf("  %s\n", app.Summary)
	}

	facts := []string{humanBytes(app.Size)}
	if app.License != "" {
		facts = append(facts, app.License)
	}
	facts = append(facts, app.Archs...)
	fmt.Printf("\n  %s\n", strings.Join(facts, ", "))
	if app.Homepage != "" {
		fmt.Printf("  %s\n", app.Homepage)
	}
	if names := commandNames(app.Bins); len(names) > 0 {
		fmt.Printf("  command: %s\n", strings.Join(names, ", "))
	}
	if app.Build != nil {
		fmt.Printf("  builds from source\n")
	}
	if app.Itch != nil {
		fmt.Printf("  downloads from itch.io\n")
	}
	fmt.Printf("  sha256 %s\n", app.SHA256)

	fam := distroFamily()
	if len(app.Deps) == 0 {
		fmt.Printf("\n  no extra system packages\n")
	} else if pkgs := app.Deps[fam]; len(pkgs) > 0 {
		fmt.Printf("\n  system packages (%s):\n", fam)
		for _, p := range pkgs {
			status := "missing"
			if isInstalled(fam, p) {
				status = "installed"
			}
			fmt.Printf("    %s, %s\n", p, status)
		}
	} else {
		fmt.Printf("\n  no system packages for %s\n", fam)
	}

	if m := readMeta(app.Name); m != nil {
		fmt.Printf("\n  installed %s, %s\n", m.Version, m.InstalledAt.Format("2006-01-02"))
		if compareVersions(app.Version, m.Version) > 0 {
			fmt.Printf("  %s is available, run `glp upgrade %s`\n", app.Version, app.Name)
		}
	}
	return nil
}

func cmdIndex(args []string) error {
	sub := "refresh"
	if len(args) > 0 {
		sub = args[0]
	}
	switch sub {
	case "refresh":
		if err := refreshIndex(); err != nil {
			return err
		}
		idx, err := loadIndex(false)
		if err != nil {
			return err
		}
		fmt.Printf("catalog refreshed, %d apps\n", len(idx.Apps))
		pruneDownloads(idx)
		return nil
	case "set":
		if len(args) < 2 {
			return fmt.Errorf("usage: glp index set <url>")
		}
		b, _ := json.MarshalIndent(map[string]string{"index_url": args[1]}, "", "  ")
		if err := writeFileAtomic(filepath.Join(configDir(), "config.json"), append(b, '\n')); err != nil {
			return err
		}
		fmt.Printf("catalog source set to %s\n", args[1])
		fmt.Printf("  run `glp index refresh` to fetch it\n")
		return nil
	case "show":
		idx, err := loadIndex(false)
		if err != nil {
			return err
		}
		fmt.Printf("%d apps, updated %s\n", len(idx.Apps), idx.Updated)
		fmt.Printf("  source  %s\n", indexURL())
		fmt.Printf("  cache   %s\n", indexCache())
		return nil
	}
	return fmt.Errorf("unknown subcommand %q (try: glp index refresh)", sub)
}

func commandNames(bins []string) []string {
	names := make([]string, 0, len(bins))
	for _, b := range bins {
		names = append(names, binName(b, b))
	}
	return names
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	cut := s[:n-1]
	// Prefer cutting at a word so the summary does not end mid-word.
	if i := strings.LastIndexByte(cut, ' '); i > n/2 {
		cut = cut[:i]
	}
	cut = strings.TrimRight(cut, " ,;:-")
	return cut + "…"
}
