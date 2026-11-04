package main

import (
	"bufio"
	"fmt"
	"io"
	"os"
	"strings"
	"text/tabwriter"
	"time"
	"unicode/utf8"
)

// stdinReader is shared by every prompt in a run. A new bufio.Reader per
// question would swallow the answers that arrived with the first line.
var stdinReader = bufio.NewReader(os.Stdin)

func isTTY(f *os.File) bool {
	fi, err := f.Stat()
	if err != nil {
		return false
	}
	return fi.Mode()&os.ModeCharDevice != 0
}

// confirm asks a yes/no question. Enter means yes; a run with no input at all
// (piped stdin that closes immediately) still answers no, so scripts cannot
// install by accident.
func confirm(format string, a ...any) bool {
	return confirmEnv("GLP_YES", format, a...)
}

// confirmRoot guards root-level actions. GLP_YES is deliberately ignored here:
// automating an app install must never imply automating a package manager run
// as root. Only GLP_YES_ROOT=1 (or an interactive yes) approves this.
func confirmRoot(format string, a ...any) bool {
	return confirmEnv("GLP_YES_ROOT", format, a...)
}

// cancel is a prompt the user declined. Saying no is a choice, not a failure,
// so main reports it quietly instead of prefixing it with "glp:".
type cancel struct {
	msg  string // last line, defaults to "nothing changed"
	hint string // optional exact name or command to try instead
}

func (c *cancel) Error() string { return "cancelled" }

func confirmEnv(env, format string, a ...any) bool {
	fmt.Fprintf(os.Stderr, format+" [Y/n] ", a...)
	if os.Getenv(env) == "1" {
		fmt.Fprintln(os.Stderr, "yes")
		return true
	}
	line, err := stdinReader.ReadString('\n')
	answer := strings.ToLower(strings.TrimSpace(line))
	if err != nil && err != io.EOF {
		fmt.Fprintln(os.Stderr, "no")
		return false
	}
	if answer == "" && err == io.EOF {
		fmt.Fprintln(os.Stderr, "no (no input)")
		return false
	}
	yes := answer == "" || answer == "y" || answer == "yes" || answer == "s" || answer == "si" || answer == "sì"
	// A terminal already shows what was typed. A pipe does not, so keep the
	// answer in the log there.
	if !isTTY(os.Stdin) {
		if yes {
			fmt.Fprintln(os.Stderr, "yes")
		} else {
			fmt.Fprintln(os.Stderr, "no")
		}
	}
	return yes
}

// progress draws a single-line download progress bar on stderr. It stays quiet
// when stderr is not a terminal, so logs only contain the step lines.
type progress struct {
	r     io.Reader
	total int64
	done  int64
	base  int64 // bytes already on disk before this transfer
	start time.Time
	last  time.Time
}

func newProgressAt(r io.Reader, total, done int64) *progress {
	return &progress{
		r:     r,
		total: total,
		done:  done,
		base:  done,
		start: time.Now(),
		last:  time.Now(),
	}
}

func (p *progress) Read(b []byte) (int, error) {
	n, err := p.r.Read(b)
	p.done += int64(n)
	if time.Since(p.last) > 100*time.Millisecond || (err != nil && p.done > 0) {
		p.draw()
		p.last = time.Now()
	}
	return n, err
}

func (p *progress) draw() {
	if !isTTY(os.Stderr) {
		return
	}
	elapsed := time.Since(p.start).Seconds()
	rate := 0.0
	if elapsed > 0.2 && p.done > p.base {
		rate = float64(p.done-p.base) / elapsed
	}

	if p.total <= 0 {
		line := fmt.Sprintf("  %s  %s", style("36", spinnerFrame(p.start)), humanBytes(p.done))
		if rate > 0 {
			line += "  " + humanRate(rate)
		}
		fmt.Fprintf(os.Stderr, "\r\033[K%s", line)
		return
	}

	pct := float64(p.done) / float64(p.total) * 100
	if pct > 100 {
		pct = 100
	}
	head := int(pct / 100 * float64(barWidth))
	line := fmt.Sprintf("  [%s] %3.0f%%  %s / %s",
		renderBar(head), pct, humanBytes(p.done), humanBytes(p.total))
	if rate > 0 {
		line += "  " + humanRate(rate)
		left := p.total - p.done
		if left > 0 {
			line += "  eta " + humanDuration(time.Duration(float64(left)/rate*float64(time.Second)))
		}
	}
	fmt.Fprintf(os.Stderr, "\r\033[K%s", line)
}

func (p *progress) Done() {
	if isTTY(os.Stderr) && p.done > 0 {
		p.draw()
		fmt.Fprintln(os.Stderr)
	}
}

func humanBytes(n int64) string {
	const unit = 1024
	if n < unit {
		return fmt.Sprintf("%d B", n)
	}
	div, exp := int64(unit), 0
	for m := n / unit; m >= unit; m /= unit {
		div *= unit
		exp++
	}
	return fmt.Sprintf("%.1f %ciB", float64(n)/float64(div), "KMGTPE"[exp])
}

// shortDigest keeps the install prompt readable; `glp info` shows the full
// SHA-256 when you need to check it by hand.
func shortDigest(s string) string {
	if len(s) <= 16 {
		return s
	}
	return s[:16] + "…"
}

func humanRate(bytesPerSecond float64) string {
	return humanBytes(int64(bytesPerSecond)) + "/s"
}

func humanDuration(d time.Duration) string {
	if d < 0 {
		d = 0
	}
	secs := int(d.Round(time.Second) / time.Second)
	switch {
	case secs < 3600:
		return fmt.Sprintf("%d:%02d", secs/60, secs%60)
	default:
		return fmt.Sprintf("%d:%02d:%02d", secs/3600, (secs/60)%60, secs%60)
	}
}

// writeTable keeps columns aligned without counting ANSI escapes. indent is
// prepended to every line, rightCols lists the columns that should be right
// aligned (usually size columns), and an empty headers slice skips the header
// row.
func writeTable(w io.Writer, indent string, headers []string, rows [][]string, rightCols ...int) {
	right := make(map[int]bool, len(rightCols))
	for _, c := range rightCols {
		right[c] = true
	}
	widths := make([]int, len(headers))
	measure := func(cells []string) {
		for i, cell := range cells {
			if i < len(widths) {
				if n := utf8.RuneCountInString(cell); n > widths[i] {
					widths[i] = n
				}
			}
		}
	}
	measure(headers)
	for _, row := range rows {
		measure(row)
	}

	tw := tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
	write := func(cells []string) {
		fmt.Fprint(tw, indent)
		for i, cell := range cells {
			if i > 0 {
				fmt.Fprint(tw, "\t")
			}
			if right[i] && i < len(widths) {
				if pad := widths[i] - utf8.RuneCountInString(cell); pad > 0 {
					cell = strings.Repeat(" ", pad) + cell
				}
			}
			fmt.Fprint(tw, cell)
		}
		fmt.Fprintln(tw)
	}
	if len(headers) > 0 {
		write(headers)
	}
	for _, row := range rows {
		write(row)
	}
	tw.Flush()
}
