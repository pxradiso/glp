package main

import (
	"fmt"
	"os"
	"strings"
	"sync"
	"time"
)

const barWidth = 28

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

// chompCells builds the raw bar: pacman at head, already eaten cells blank,
// remaining pellets as dashes. Plain characters only, so it can be tested.
func chompCells(head int, reverse bool) string {
	if head < 0 {
		head = 0
	}
	if head >= barWidth {
		head = barWidth - 1
	}
	buf := make([]byte, barWidth)
	for i := 0; i < barWidth; i++ {
		switch {
		case i == head:
			buf[i] = 'C'
		case reverse && i > head:
			buf[i] = ' '
		case !reverse && i < head:
			buf[i] = ' '
		default:
			buf[i] = '-'
		}
	}
	return string(buf)
}

func renderChomp(head int, reverse bool) string {
	cells := chompCells(head, reverse)
	i := strings.IndexByte(cells, 'C')
	if i < 0 || !animations() {
		return cells
	}
	left, right := cells[:i], cells[i+1:]
	if strings.Contains(left, "-") {
		left = style("2", left)
	}
	if strings.Contains(right, "-") {
		right = style("2", right)
	}
	return left + style("1;33", "C") + right
}

// printStep is for steps that draw their own progress (downloads).
func printStep(index, total int, label string) {
	fmt.Fprintf(os.Stderr, "%s %s\n", style("2", fmt.Sprintf("(%d/%d)", index, total)), label)
}

// stepAnim animates an indeterminate step in place and replaces it with a
// checkmark when finished.
type stepAnim struct {
	label   string
	on      bool
	reverse bool
	stop    chan struct{}
	wg      sync.WaitGroup
	pos     int
	failed  bool
}

func startStep(index, total int, label string, reverse bool) *stepAnim {
	s := &stepAnim{
		label:   fmt.Sprintf("(%d/%d) %s", index, total, label),
		on:      animations(),
		reverse: reverse,
		stop:    make(chan struct{}),
	}
	if !reverse {
		s.pos = 0
	} else {
		s.pos = barWidth - 1
	}
	if s.on {
		s.wg.Add(1)
		go s.run()
	}
	return s
}

func (s *stepAnim) run() {
	defer s.wg.Done()
	t := time.NewTicker(70 * time.Millisecond)
	defer t.Stop()
	for {
		select {
		case <-s.stop:
			fmt.Fprint(os.Stderr, "\r\033[K")
			return
		case <-t.C:
			if s.reverse {
				s.pos--
				if s.pos < 0 {
					s.pos = barWidth - 1
				}
			} else {
				s.pos++
				if s.pos >= barWidth {
					s.pos = 0
				}
			}
			fmt.Fprintf(os.Stderr, "\r\033[K  %s  %s", renderChomp(s.pos, s.reverse), style("2", s.label))
		}
	}
}

func (s *stepAnim) Done() {
	if !s.failed {
		close(s.stop)
		s.wg.Wait()
	}
	fmt.Fprintf(os.Stderr, "  %s %s\n", style("32", "✔"), s.label)
}

// countBar tracks a known number of steps, e.g. files being deleted.
type countBar struct {
	total   int
	reverse bool
	on      bool
	last    time.Time
}

func newCountBar(total int, reverse bool) *countBar {
	return &countBar{total: total, reverse: reverse, on: animations(), last: time.Now()}
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
	fmt.Fprintf(os.Stderr, "\r\033[K  %s  %s", renderChomp(head, b.reverse),
		style("2", fmt.Sprintf("%d/%d files", n, b.total)))
}

func (b *countBar) Done() {
	if b.on {
		fmt.Fprint(os.Stderr, "\r\033[K")
	}
}

func (s *stepAnim) Fail(msg string) {
	s.failed = true
	close(s.stop)
	s.wg.Wait()
	fmt.Fprintf(os.Stderr, "  %s %s: %s\n", style("31", "✘"), s.label, msg)
}
