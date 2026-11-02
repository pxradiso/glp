package main

import (
	"bufio"
	"fmt"
	"io"
	"os"
	"strings"
	"time"
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

// progress draws a single-line download progress bar on stderr.
type progress struct {
	r     io.Reader
	label string
	total int64
	done  int64
	last  time.Time
}

func newProgress(r io.Reader, total int64) *progress {
	return &progress{r: r, total: total, last: time.Now()}
}

func (p *progress) Read(b []byte) (int, error) {
	n, err := p.r.Read(b)
	p.done += int64(n)
	if time.Since(p.last) > 120*time.Millisecond || (err != nil && p.done > 0) {
		p.draw()
		p.last = time.Now()
	}
	return n, err
}

func (p *progress) draw() {
	if !isTTY(os.Stderr) {
		return
	}
	if p.total > 0 {
		pct := float64(p.done) / float64(p.total) * 100
		head := int(pct / 100 * float64(barWidth))
		fmt.Fprintf(os.Stderr, "\r\033[K  %s %3.0f%%  %s / %s",
			renderChomp(head, false), pct, humanBytes(p.done), humanBytes(p.total))
	} else {
		fmt.Fprintf(os.Stderr, "\r\033[K  %s %s", renderChomp(0, false), humanBytes(p.done))
	}
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
