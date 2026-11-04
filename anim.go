package main

import (
	"fmt"
	"os"
	"strings"
	"sync"
	"time"
)

const barWidth = 24

// animations is false for pipes, dumb terminals and GLP_PLAIN=1, so scripted
// output stays readable.
func animations() bool {
	if os.Getenv("GLP_PLAIN") == "1" || os.Getenv("TERM") == "dumb" {
		return false
	}
	if os.Getenv("NO_COLOR") != "" {
		return false
	}
	return isTTY(os.Stderr)
}

func style(code, s string) string {
	if !animations() {
		return s
	}
	return "\033[" + code + "m" + s + "\033[0m"
}

// barParts returns the filled and empty halves as separate strings. Colour
// needs them apart, and slicing a bar string by cell count would split the
// multi-byte block characters in half.
func barParts(head int) (filled, empty string) {
	if head < 0 {
		head = 0
	}
	if head > barWidth {
		head = barWidth
	}
	return strings.Repeat("█", head), strings.Repeat("░", barWidth-head)
}

// barCells is the plain bar, so it can be tested without a terminal. The
// caller only adds colour.
func barCells(head int) string {
	filled, empty := barParts(head)
	return filled + empty
}

func renderBar(head int) string {
	filled, empty := barParts(head)
	if !animations() {
		return filled + empty
	}
	return style("36", filled) + style("2", empty)
}

var spinnerFrames = []string{"⠋", "⠙", "⠹", "⠸", "⠼", "⠴", "⠦", "⠧", "⠇", "⠏"}

func spinnerFrame(start time.Time) string {
	if !animations() {
		return "*"
	}
	i := int(time.Since(start)/(80*time.Millisecond)) % len(spinnerFrames)
	return spinnerFrames[i]
}

// printStep is for steps that draw their own progress (downloads, deletions).
func printStep(index, total int, label string) {
	fmt.Fprintf(os.Stderr, "  %s  %s\n",
		style("2", fmt.Sprintf("[%d/%d]", index, total)), label)
}

// stepAnim shows a spinner on one line and replaces it with a check when the
// step finishes. It draws nothing when the output is not a terminal.
type stepAnim struct {
	label  string
	on     bool
	stop   chan struct{}
	wg     sync.WaitGroup
	frame  int
	failed bool
}

func startStep(index, total int, label string) *stepAnim {
	s := &stepAnim{
		label: fmt.Sprintf("[%d/%d] %s", index, total, label),
		on:    animations(),
		stop:  make(chan struct{}),
	}
	if s.on {
		s.wg.Add(1)
		go s.run()
	}
	return s
}

func (s *stepAnim) run() {
	defer s.wg.Done()
	t := time.NewTicker(80 * time.Millisecond)
	defer t.Stop()
	for {
		select {
		case <-s.stop:
			fmt.Fprint(os.Stderr, "\r\033[K")
			return
		case <-t.C:
			s.frame++
			fmt.Fprintf(os.Stderr, "\r\033[K  %s  %s",
				style("36", spinnerFrames[s.frame%len(spinnerFrames)]),
				style("2", s.label))
		}
	}
}

func (s *stepAnim) Done() {
	if s.failed {
		return
	}
	if s.on {
		close(s.stop)
		s.wg.Wait()
	}
	fmt.Fprintf(os.Stderr, "  %s %s\n", style("32", "✓"), s.label)
}

func (s *stepAnim) Fail(msg string) {
	if s.failed {
		return
	}
	s.failed = true
	if s.on {
		close(s.stop)
		s.wg.Wait()
	}
	fmt.Fprintf(os.Stderr, "  %s %s: %s\n", style("31", "✗"), s.label, msg)
}

// countBar tracks a known number of steps, e.g. files being deleted.
type countBar struct {
	total int
	on    bool
	last  time.Time
}

func newCountBar(total int) *countBar {
	return &countBar{total: total, on: animations(), last: time.Now()}
}

func (b *countBar) Update(n int) {
	if !b.on {
		return
	}
	// Repaint at most ~15x/second; the caller may tick thousands of times.
	if time.Since(b.last) < 60*time.Millisecond && n < b.total {
		return
	}
	b.last = time.Now()
	head := 0
	if b.total > 0 {
		head = n * barWidth / b.total
	}
	fmt.Fprintf(os.Stderr, "\r\033[K  [%s]  %s", renderBar(head),
		style("2", fmt.Sprintf("%d/%d files", n, b.total)))
}

func (b *countBar) Done() {
	if b.on {
		fmt.Fprint(os.Stderr, "\r\033[K")
	}
}
