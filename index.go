package main

import (
	"errors"
	"fmt"
	"sort"
	"strings"
)

// App is one entry of the signed catalog.
type App struct {
	Name     string   `json:"name"`
	Version  string   `json:"version"`
	Summary  string   `json:"summary"`
	Homepage string   `json:"homepage"`
	License  string   `json:"license"`
	Archs    []string `json:"archs"`
	URL      string   `json:"url"`
	SHA256   string   `json:"sha256"`
	Size     int64    `json:"size"`

	// Format is "appimage" (the default when empty) or a tarball such as
	// "tar.gz". Some vendors ship official tarballs and never an AppImage.
	Format string   `json:"format,omitempty"`
	Bins   []string `json:"bins"`

	Desktop *Desktop            `json:"desktop,omitempty"`
	Deps    map[string][]string `json:"deps,omitempty"`

	// Build is set when the catalog entry is source code instead of a ready
	// binary. The steps run inside the signed tarball with {src} and {dest}
	// substituted, and never through a shell.
	Build *Build `json:"build,omitempty"`

	// Icon points at art inside the archive, for builds that ship a logo but
	// no desktop file naming one. Crop is [x y w h], because games tend to
	// keep every sprite in a sheet.
	Icon *Icon `json:"icon,omitempty"`

	// Itch is set when the only vendor build lives behind itch.io's download
	// flow. The URL is resolved at install time, the bytes stay pinned.
	Itch *Itch `json:"itch,omitempty"`

	// Workdir runs the program from its own directory. Engines like Lime look
	// for their native library in the current directory, not next to the
	// binary, so a launcher that starts in $HOME cannot find it.
	Workdir bool `json:"workdir,omitempty"`
}

type Icon struct {
	Path string `json:"path"`
	Crop []int  `json:"crop,omitempty"`
}

type Build struct {
	Commands [][]string `json:"commands"`
}

type Desktop struct {
	Name       string   `json:"name"`
	Comment    string   `json:"comment"`
	Icon       string   `json:"icon"`
	Categories []string `json:"categories"`
	Terminal   bool     `json:"terminal"`
}

type Index struct {
	Schema  int    `json:"schema"`
	Updated string `json:"updated"`
	Apps    []App  `json:"apps"`
}

func (idx *Index) Find(name string) (*App, error) {
	want := strings.ToLower(strings.TrimSpace(name))
	for i := range idx.Apps {
		if strings.ToLower(idx.Apps[i].Name) == want {
			return &idx.Apps[i], nil
		}
	}
	return nil, fmt.Errorf("%q is not in the catalog (try `glp search %s`)", name, name)
}

// Search returns apps whose name, desktop label, launcher command, or summary
// contains the term. Typing "funkin" should find fnf-game.
func (idx *Index) Search(term string) []App {
	term = strings.ToLower(strings.TrimSpace(term))
	if term == "" {
		out := append([]App(nil), idx.Apps...)
		sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
		return out
	}
	var out []App
	for i := range idx.Apps {
		a := &idx.Apps[i]
		if strings.Contains(strings.ToLower(a.Name), term) ||
			matchedAlias(a, term) ||
			strings.Contains(strings.ToLower(a.Summary), term) {
			out = append(out, *a)
		}
	}
	sort.Slice(out, func(i, j int) bool {
		in := matchedName(&out[i], term)
		jn := matchedName(&out[j], term)
		if in != jn {
			return in // name and alias matches rank above summary-only matches
		}
		return out[i].Name < out[j].Name
	})
	return out
}

// matchedName reports whether the term is in the catalog name or an alias,
// the strings a user actually sees on their desktop or types in a shell.
func matchedName(a *App, term string) bool {
	if strings.Contains(strings.ToLower(a.Name), term) {
		return true
	}
	return matchedAlias(a, term)
}

func matchedAlias(a *App, term string) bool {
	for _, alias := range a.aliases() {
		if strings.Contains(alias, term) {
			return true
		}
	}
	return false
}

// ArchMatches maps Go's GOARCH names to the catalog's AppImage-ish names.
func (a *App) ArchMatches(goarch string) bool {
	want := map[string]string{
		"amd64": "x86_64",
		"arm64": "aarch64",
		"386":   "i686",
	}[goarch]
	if want == "" {
		return false
	}
	if len(a.Archs) == 0 {
		return true
	}
	for _, x := range a.Archs {
		if x == want {
			return true
		}
	}
	return false
}

// splitRef splits "app@1.2.3" into ("app", "1.2.3").
func splitRef(ref string) (string, string) {
	if i := strings.LastIndex(ref, "@"); i > 0 {
		return ref[:i], ref[i+1:]
	}
	return ref, ""
}

// compareVersions compares dotted versions numerically, falling back to
// lexicographic comparison for non-numeric segments.
func compareVersions(a, b string) int {
	as := strings.FieldsFunc(a, func(r rune) bool { return r == '.' || r == '-' || r == '_' || r == '+' })
	bs := strings.FieldsFunc(b, func(r rune) bool { return r == '.' || r == '-' || r == '_' || r == '+' })
	for i := 0; i < len(as) || i < len(bs); i++ {
		var x, y string
		if i < len(as) {
			x = as[i]
		}
		if i < len(bs) {
			y = bs[i]
		}
		if x == y {
			continue
		}
		var xi, yi int
		xnum := isNumeric(x) && parseInt(x, &xi) == nil
		ynum := isNumeric(y) && parseInt(y, &yi) == nil
		switch {
		case xnum && ynum:
			if xi != yi {
				return cmpInt(xi, yi)
			}
		case xnum:
			return 1 // "1.2.0" > "1.2-rc1"
		case ynum:
			return -1
		default:
			return strings.Compare(x, y)
		}
	}
	return 0
}

func cmpInt(a, b int) int {
	switch {
	case a < b:
		return -1
	case a > b:
		return 1
	}
	return 0
}

func isNumeric(s string) bool {
	if s == "" {
		return false
	}
	for _, r := range s {
		if r < '0' || r > '9' {
			return false
		}
	}
	return true
}

func parseInt(s string, out *int) error {
	if !isNumeric(s) {
		return errors.New("not a number")
	}
	n := 0
	for _, r := range s {
		n = n*10 + int(r-'0')
	}
	*out = n
	return nil
}
