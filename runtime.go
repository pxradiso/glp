package main

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"
)

var errNoResolver = errors.New("no package resolver on this system")

// libIndex collects every basename in the AppDir so we can tell a genuinely
// missing host library from one the app already bundles.
func libIndex(appdir string) map[string]bool {
	idx := make(map[string]bool, 256)
	filepath.WalkDir(appdir, func(p string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return nil
		}
		idx[filepath.Base(p)] = true
		return nil
	})
	return idx
}

func isScript(path string) bool {
	f, err := os.Open(path)
	if err != nil {
		return false
	}
	defer f.Close()
	buf := make([]byte, 2)
	if _, err := f.Read(buf); err != nil {
		return false
	}
	return buf[0] == '#' && buf[1] == '!'
}

// missingLibs reports shared objects the loader cannot find and the AppDir does
// not already provide. This is the "why does this app not start" check.
func missingLibs(bin, appdir string) ([]string, error) {
	if _, err := os.Stat(bin); err != nil {
		return nil, fmt.Errorf("launcher not found: %s", bin)
	}
	if isScript(bin) {
		return nil, nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), 25*time.Second)
	defer cancel()

	out, err := exec.CommandContext(ctx, "ldd", bin).CombinedOutput()
	if err != nil && len(out) == 0 {
		return nil, fmt.Errorf("ldd failed: %w", err)
	}

	idx := libIndex(appdir)
	seen := make(map[string]bool)
	var missing []string
	for _, line := range strings.Split(string(out), "\n") {
		line = strings.TrimSpace(line)
		if !strings.Contains(line, "=> not found") {
			continue
		}
		soname := strings.TrimSpace(strings.TrimSuffix(line, "=> not found"))
		if soname == "" || seen[soname] || idx[soname] {
			continue
		}
		seen[soname] = true
		missing = append(missing, soname)
	}
	sort.Strings(missing)
	return missing, nil
}

func binFor(name string, bins []string) string {
	root := filepath.Join(dataDir(), name, "current")
	if len(bins) > 0 {
		if rel, err := resolveBin(root, bins[0]); err == nil {
			return filepath.Join(root, rel)
		}
	}
	return filepath.Join(root, "AppRun")
}

// resolvePackage asks the local package manager which package provides a soname.
// resolverHint explains the one step a machine is usually missing before a
// soname can be mapped to a package.
func resolverHint(fam string) string {
	switch fam {
	case "arch":
		return "no package found, sync the file database with `sudo pacman -Fy` and try again"
	case "debian":
		return "no package found, `apt-file` is needed to map libraries to packages"
	}
	return "no packages found, install them by hand"
}

func resolvePackage(fam, soname string) (string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 25*time.Second)
	defer cancel()

	var out []byte
	var err error
	switch fam {
	case "fedora":
		out, err = exec.CommandContext(ctx, "dnf", "-q", "provides", "*/"+soname).Output()
		if err == nil {
			for _, p := range parseDnfProvides(string(out)) {
				return p, nil
			}
		}
	case "debian":
		if _, lerr := exec.LookPath("apt-file"); lerr != nil {
			return "", errNoResolver
		}
		out, err = exec.CommandContext(ctx, "apt-file", "search", "--regexp", "/"+regexp.QuoteMeta(soname)+"$").Output()
		if err == nil {
			for _, p := range parseAptFile(string(out)) {
				return p, nil
			}
		}
	case "arch":
		out, err = exec.CommandContext(ctx, "pacman", "-F", soname).Output()
		if err == nil {
			for _, p := range parsePacmanFile(string(out)) {
				return p, nil
			}
		}
	case "suse":
		out, err = exec.CommandContext(ctx, "zypper", "--non-interactive", "--quiet", "search", "--provides", soname).Output()
		if err == nil {
			for _, p := range parseZypper(string(out)) {
				return p, nil
			}
		}
	default:
		return "", errNoResolver
	}
	if err != nil {
		return "", err
	}
	return "", fmt.Errorf("no package provides %s", soname)
}

// parseDnfProvides pulls package names out of "name-version-release.arch : summary".
func parseDnfProvides(out string) []string {
	var pkgs []string
	for _, line := range strings.Split(out, "\n") {
		left, _, ok := strings.Cut(line, " : ")
		if !ok {
			continue
		}
		name := strings.TrimSpace(left)
		// Drop the trailing ".arch", then "-version-release".
		if i := strings.LastIndex(name, "."); i > 0 {
			name = name[:i]
		}
		parts := strings.Split(name, "-")
		if len(parts) < 3 {
			continue
		}
		name = strings.Join(parts[:len(parts)-2], "-")
		if name != "" && pkgNameOK.MatchString(name) {
			pkgs = append(pkgs, name)
		}
	}
	return pkgs
}

func parseAptFile(out string) []string {
	var pkgs []string
	for _, line := range strings.Split(out, "\n") {
		name, _, ok := strings.Cut(line, ":")
		if !ok {
			continue
		}
		name = strings.TrimSpace(name)
		if pkgNameOK.MatchString(name) {
			pkgs = append(pkgs, name)
		}
	}
	return pkgs
}

func parsePacmanFile(out string) []string {
	var pkgs []string
	for _, line := range strings.Split(out, "\n") {
		if strings.HasPrefix(line, " ") || !strings.Contains(line, "/") {
			continue
		}
		f := strings.Fields(line)
		if len(f) == 0 {
			continue
		}
		_, name, _ := strings.Cut(f[0], "/")
		if pkgNameOK.MatchString(name) {
			pkgs = append(pkgs, name)
		}
	}
	return pkgs
}

func parseZypper(out string) []string {
	var pkgs []string
	for _, line := range strings.Split(out, "\n") {
		f := strings.Fields(line)
		if len(f) < 2 || f[0] == "S" || f[0] == "Loading" {
			continue
		}
		for _, tok := range f {
			if pkgNameOK.MatchString(tok) && strings.Contains(tok, "-") {
				pkgs = append(pkgs, tok)
				break
			}
		}
	}
	return pkgs
}

// fixRuntime is the feature: figure out what is missing, map it to packages and
// offer to install them as root.
func fixRuntime(name string, bins []string) error {
	versionDir, err := filepath.EvalSymlinks(filepath.Join(dataDir(), name, "current"))
	if err != nil {
		return fmt.Errorf("%s is not installed", name)
	}
	bin := binFor(name, bins)
	missing, err := missingLibs(bin, versionDir)
	if err != nil {
		return err
	}
	if len(missing) == 0 {
		fmt.Printf("  %s libraries ok\n", style("32", "✔"))
		return nil
	}

	fmt.Printf("  missing (%d):\n", len(missing))
	for _, s := range missing {
		fmt.Printf("    %s\n", s)
	}

	fam := distroFamily()
	if fam == "" {
		return errors.New("unknown distro, install them by hand")
	}

	var pkgs, unresolved []string
	seen := make(map[string]bool)
	for _, soname := range missing {
		p, err := resolvePackage(fam, soname)
		if err != nil {
			unresolved = append(unresolved, soname)
			continue
		}
		if !seen[p] {
			seen[p] = true
			pkgs = append(pkgs, p)
		}
	}
	for _, s := range unresolved {
		fmt.Fprintf(os.Stderr, "  ? %s: not found\n", s)
	}
	if len(pkgs) == 0 {
		return errors.New(resolverHint(fam))
	}

	cmd, err := installCommand(fam, pkgs)
	if err != nil {
		return err
	}
	fmt.Printf("\n%s needs (root, %s):\n  %s\n", name, fam, strings.Join(cmd, " "))
	if !confirmRoot("Install them?") {
		fmt.Println("skipped")
		return nil
	}

	c := exec.Command(cmd[0], cmd[1:]...)
	c.Stdin, c.Stdout, c.Stderr = os.Stdin, os.Stdout, os.Stderr
	if err := c.Run(); err != nil {
		return err
	}

	fmt.Println("\nchecking again")
	missing, _ = missingLibs(bin, versionDir)
	if len(missing) == 0 {
		fmt.Printf("  %s libraries ok\n", style("32", "✔"))
	} else {
		fmt.Printf("  still missing: %s\n", strings.Join(missing, " "))
	}
	return nil
}

func cmdDoctor(args []string) error {
	var targets []Meta
	if len(args) == 1 {
		m := readMeta(args[0])
		if m == nil {
			return fmt.Errorf("%s is not installed", args[0])
		}
		targets = []Meta{*m}
	} else if len(args) > 1 {
		return errors.New("usage: glp doctor [app]")
	} else {
		targets = installedMetas()
	}

	if len(targets) == 0 {
		fmt.Println("no apps installed")
		return nil
	}
	for _, m := range targets {
		fmt.Printf("\n%s %s\n", style("1", m.Name), m.Version)
		if err := fixRuntime(m.Name, m.Bins); err != nil {
			fmt.Fprintf(os.Stderr, "glp: %v\n", err)
		}
	}
	return nil
}
