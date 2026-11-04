package main

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
)

// suggestMax caps how many candidates are shown at once. Anything longer is a
// list the user should narrow down instead of reading.
const suggestMax = 8

// Suggest ranks catalog entries that look like the thing the user typed, so
// `glp install win` can offer winboat and `glp install vscod` can offer vscode.
// Exact names are handled by the caller; this only produces fallbacks.
func (idx *Index) Suggest(query string) []App {
	q := strings.ToLower(strings.TrimSpace(query))
	if len(q) < 2 {
		return nil
	}

	type scored struct {
		app   App
		score int
	}
	var out []scored
	for _, a := range idx.Apps {
		if s := scoreMatch(&a, q); s > 0 {
			out = append(out, scored{a, s})
		}
	}
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].score != out[j].score {
			return out[i].score > out[j].score
		}
		return out[i].app.Name < out[j].app.Name
	})

	apps := make([]App, len(out))
	for i, s := range out {
		apps[i] = s.app
	}
	return apps
}

// scoreMatch is 0 when the query has nothing to do with the app. Higher is a
// better match: exact alias beats prefix beats substring beats a typo.
func scoreMatch(a *App, q string) int {
	name := strings.ToLower(a.Name)
	best := 0
	take := func(s int) {
		if s > best {
			best = s
		}
	}

	if strings.HasPrefix(name, q) {
		take(800 - len(name))
	} else if i := strings.Index(name, q); i > 0 {
		take(600 - i*4 - len(name))
	}
	for _, alias := range a.aliases() {
		switch {
		case alias == q:
			take(700)
		case strings.HasPrefix(alias, q):
			take(500 - len(alias))
		case strings.Contains(alias, q):
			take(300 - len(alias))
		}
	}
	if d := editDistance(name, q); d <= typoBudget(q) {
		take(200 - d*20)
	}
	return best
}

// aliases are the other names a user may reasonably type: the menu label and
// the launcher commands, which often differ from the package name.
func (a *App) aliases() []string {
	seen := map[string]bool{strings.ToLower(a.Name): true}
	var out []string
	add := func(s string) {
		s = strings.ToLower(strings.TrimSpace(s))
		if s == "" || seen[s] {
			return
		}
		seen[s] = true
		out = append(out, s)
	}
	if a.Desktop != nil {
		add(a.Desktop.Name)
	}
	for _, b := range a.Bins {
		name, _, _ := strings.Cut(b, "=")
		add(filepath.Base(name))
	}
	return out
}

// typoBudget is how many edits a query of that length may be off by. Short
// names are not forgiven: "win" is already a prefix of something real.
func typoBudget(q string) int {
	switch {
	case len(q) < 4:
		return 0
	case len(q) < 8:
		return 1
	}
	return 2
}

// editDistance is the Levenshtein distance between two lowercase strings.
func editDistance(a, b string) int {
	if a == b {
		return 0
	}
	ar, br := []rune(a), []rune(b)
	prev := make([]int, len(br)+1)
	cur := make([]int, len(br)+1)
	for j := range prev {
		prev[j] = j
	}
	for i := 1; i <= len(ar); i++ {
		cur[0] = i
		for j := 1; j <= len(br); j++ {
			cost := 1
			if ar[i-1] == br[j-1] {
				cost = 0
			}
			cur[j] = min3(prev[j]+1, cur[j-1]+1, prev[j-1]+cost)
		}
		prev, cur = cur, prev
	}
	return prev[len(br)]
}

func min3(a, b, c int) int {
	m := a
	if b < m {
		m = b
	}
	if c < m {
		m = c
	}
	return m
}

// resolveApp finds the app the user meant. An exact name wins silently; a
// unique near miss asks first; several near misses become a numbered menu.
func resolveApp(idx *Index, query string) (*App, error) {
	q := strings.ToLower(strings.TrimSpace(query))
	if q == "" {
		return nil, errors.New("no app name given")
	}
	for i := range idx.Apps {
		if strings.ToLower(idx.Apps[i].Name) == q {
			return &idx.Apps[i], nil
		}
	}

	matches := idx.Suggest(query)
	if len(matches) == 0 {
		return nil, fmt.Errorf("%q is not in the catalog (try `glp search %s`)", query, query)
	}
	if len(matches) == 1 {
		if !confirm("did you mean %s?", matches[0].Name) {
			return nil, &cancel{hint: "the exact name is " + matches[0].Name}
		}
		return &matches[0], nil
	}

	i, err := choose(query, matches)
	if err != nil {
		return nil, err
	}
	return &matches[i], nil
}

// resolveInstalled is the same idea for `glp remove`, where the answer comes
// from what is actually on disk rather than from the catalog.
func resolveInstalled(query string) (string, error) {
	name := strings.ToLower(strings.TrimSpace(query))
	var names []string
	for _, m := range installedMetas() {
		if strings.EqualFold(m.Name, name) {
			return m.Name, nil
		}
		names = append(names, m.Name)
	}
	if len(names) == 0 {
		return "", fmt.Errorf("%s is not installed", query)
	}

	cands := rankNames(names, name)
	if len(cands) == 0 {
		return "", fmt.Errorf("%s is not installed", query)
	}
	if len(cands) == 1 {
		if !confirm("did you mean %s?", cands[0]) {
			return "", &cancel{hint: "the exact name is " + cands[0]}
		}
		return cands[0], nil
	}

	i, err := chooseNames(query, cands)
	if err != nil {
		return "", err
	}
	return cands[i], nil
}

// rankNames does the same prefix and typo scoring on a bare list of names.
func rankNames(names []string, q string) []string {
	if len(q) < 2 {
		return nil
	}
	type scored struct {
		name  string
		score int
	}
	var out []scored
	for _, n := range names {
		low := strings.ToLower(n)
		s := 0
		switch {
		case strings.HasPrefix(low, q):
			s = 800 - len(low)
		case strings.Contains(low, q):
			s = 600 - len(low)
		}
		if d := editDistance(low, q); d <= typoBudget(q) {
			if t := 200 - d*20; t > s {
				s = t
			}
		}
		if s > 0 {
			out = append(out, scored{n, s})
		}
	}
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].score != out[j].score {
			return out[i].score > out[j].score
		}
		return out[i].name < out[j].name
	})
	ranked := make([]string, len(out))
	for i, s := range out {
		ranked[i] = s.name
	}
	return ranked
}

func choose(query string, matches []App) (int, error) {
	names := make([]string, len(matches))
	details := make([]string, len(matches))
	for i, a := range matches {
		names[i] = a.Name
		details[i] = a.Summary
	}
	return chooseList(query, names, details)
}

func chooseNames(query string, names []string) (int, error) {
	return chooseList(query, names, make([]string, len(names)))
}

// chooseList prints a numbered menu and reads one number. It never guesses:
// an empty answer, a closed stdin or GLP_YES all mean "be explicit".
func chooseList(query string, names, details []string) (int, error) {
	shown := len(names)
	if shown > suggestMax {
		shown = suggestMax
	}
	title := fmt.Sprintf("%d apps match %q", len(names), query)
	if len(names) == 1 {
		title = fmt.Sprintf("1 app matches %q", query)
	}
	fmt.Fprintf(os.Stderr, "\n%s\n\n", title)

	withDetails := false
	for i := 0; i < shown; i++ {
		if strings.TrimSpace(details[i]) != "" {
			withDetails = true
			break
		}
	}
	rows := make([][]string, 0, shown)
	for i := 0; i < shown; i++ {
		if withDetails {
			rows = append(rows, []string{strconv.Itoa(i + 1), names[i], truncate(strings.TrimSpace(details[i]), 40)})
		} else {
			rows = append(rows, []string{strconv.Itoa(i + 1), names[i]})
		}
	}
	writeTable(os.Stderr, "  ", nil, rows, 0)

	if shown < len(names) {
		fmt.Fprintf(os.Stderr, "  ... and %d more, be more specific\n", len(names)-shown)
	}

	if os.Getenv("GLP_YES") == "1" || !isTTY(os.Stdin) {
		return 0, fmt.Errorf("%q is ambiguous, use the full name", query)
	}

	fmt.Fprintf(os.Stderr, "\npick one [1-%d, enter to cancel]: ", shown)
	line, err := stdinReader.ReadString('\n')
	answer := strings.TrimSpace(line)
	if answer == "" {
		return 0, &cancel{}
	}
	if n, cerr := strconv.Atoi(answer); cerr == nil {
		if n < 1 || n > shown {
			return 0, fmt.Errorf("no choice %d", n)
		}
		return n - 1, nil
	}
	// A typed name is fine too, as long as it is one of the candidates.
	for i := 0; i < shown; i++ {
		if strings.EqualFold(answer, names[i]) {
			return i, nil
		}
	}
	if err != nil {
		return 0, &cancel{}
	}
	return 0, fmt.Errorf("no choice %q", answer)
}
