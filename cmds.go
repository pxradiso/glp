package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
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

	var title string
	switch {
	case term == "":
		title = fmt.Sprintf("%d apps", len(results))
	case len(results) == 1:
		title = fmt.Sprintf("1 app matches %q", term)
	default:
		title = fmt.Sprintf("%d apps match %q", len(results), term)
	}
	fmt.Printf("%s\n\n", outStyle("1", title))

	rows := make([][]string, 0, len(results))
	for _, a := range results {
		rows = append(rows, []string{a.Name, a.Version, humanBytes(a.Size), truncate(a.Summary, 56)})
	}
	writeTable(os.Stdout, "  ", []string{"app", "version", "size", "what it is"}, rows, 2)

	if term == "" {
		return nil
	}
	if len(results) == 1 {
		fmt.Printf("\ninstall: %s\n", outStyle("36", "glp install "+results[0].Name))
	} else {
		fmt.Printf("\ninstall one: %s\n", outStyle("36", "glp install <app>"))
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

	fmt.Printf("%s %s\n", outStyle("1", app.Name), outStyle("2", app.Version))
	if app.Summary != "" {
		fmt.Printf("  %s\n", app.Summary)
	}
	fmt.Println()

	facts := [][]string{{"size", humanBytes(app.Size)}}
	if app.License != "" {
		facts = append(facts, []string{"license", app.License})
	}
	if len(app.Archs) > 0 {
		facts = append(facts, []string{"arch", strings.Join(app.Archs, ", ")})
	}
	source := downloadLabel(app)
	if app.Build != nil {
		source = "built from source"
	}
	facts = append(facts, []string{"source", source})
	if names := commandNames(app.Bins); len(names) > 0 {
		facts = append(facts, []string{"command", strings.Join(names, ", ")})
	}
	if app.Homepage != "" {
		facts = append(facts, []string{"homepage", app.Homepage})
	}
	facts = append(facts, []string{"sha256", app.SHA256})
	writeTable(os.Stdout, "  ", nil, facts)

	fam := distroFamily()
	if len(app.Deps) == 0 {
		fmt.Printf("\n  no system packages needed\n")
	} else if pkgs := app.Deps[fam]; len(pkgs) > 0 {
		fmt.Printf("\n  system packages (%s)\n", fam)
		rows := make([][]string, 0, len(pkgs))
		for _, p := range pkgs {
			status := "missing"
			if isInstalled(fam, p) {
				status = "installed"
			}
			rows = append(rows, []string{p, status})
		}
		writeTable(os.Stdout, "    ", nil, rows)
	} else {
		fmt.Printf("\n  no system packages for %s\n", fam)
	}

	if m := readMeta(app.Name); m != nil {
		fmt.Printf("\n  installed  %s on %s\n", m.Version, m.InstalledAt.Format("2006-01-02"))
		if compareVersions(app.Version, m.Version) > 0 {
			fmt.Printf("  available  %s, run: %s\n", app.Version, outStyle("36", "glp upgrade "+app.Name))
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
		fmt.Println(outStyle("1", fmt.Sprintf("catalog refreshed, %d apps", len(idx.Apps))))
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
		fmt.Println(outStyle("1", "catalog source changed"))
		fmt.Println()
		writeTable(os.Stdout, "  ", nil, [][]string{
			{"source", args[1]},
			{"next", outStyle("36", "glp index refresh")},
		})
		return nil
	case "show":
		idx, err := loadIndex(false)
		if err != nil {
			return err
		}
		updated := "unknown"
		if t, err := time.Parse(time.RFC3339, idx.Updated); err == nil {
			updated = t.Local().Format("2006-01-02 15:04")
		} else if idx.Updated != "" {
			updated = idx.Updated
		}
		fmt.Println(outStyle("1", "catalog"))
		fmt.Println()
		writeTable(os.Stdout, "  ", nil, [][]string{
			{"apps", fmt.Sprintf("%d", len(idx.Apps))},
			{"updated", updated},
			{"source", indexURL()},
			{"cache", indexCache()},
		})
		return nil
	}
	return fmt.Errorf("unknown index subcommand %q (use: refresh, show, set)", sub)
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
