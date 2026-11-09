package main

import (
	"strings"
	"testing"
	"time"

	"github.com/egoist/mygo/ui"
)

// termWindow gives the window a terminal session that runs no shell, so the
// panel builds without a pty. The panel opens at once, for the tests that
// are not about its slide.
func termWindow(w *window, tt *ui.Tester) {
	tt.SetPreferences(ui.Preferences{ReduceMotion: true})
	s := newTermSession(w)
	s.name = "zsh"
	w.terms = []*termSession{s}
	w.termActive = 0
	w.termShown = true
}

func TestTerminalHideButton(t *testing.T) {
	w, tt := newTestWindow(t, testRepo(t))
	termWindow(w, tt)
	tt.Frame()
	if err := tt.Click("Hide Terminal (⌃`)"); err != nil {
		t.Fatal(err)
	}
	tt.Frame()
	if w.termShown {
		t.Fatal("the terminal still shows after clicking Hide Terminal")
	}
}

func TestTerminalTabCloseButton(t *testing.T) {
	w, tt := newTestWindow(t, testRepo(t))
	termWindow(w, tt)
	tt.Frame()
	if err := tt.Click("Close zsh"); err != nil {
		t.Fatalf("%v; texts %q", err, tt.Texts())
	}
	tt.Frame()
	if w.termShown || len(w.terms) != 0 {
		t.Fatalf("closing the last tab: shown %v, terms %d", w.termShown, len(w.terms))
	}
}

// TestTerminalResizerDirection checks that the divider of the right-hand
// panel moves its edge with the pointer, as the sidebar's does.
func TestTerminalResizerDirection(t *testing.T) {
	w, tt := newTestWindow(t, testRepo(t))
	termWindow(w, tt)
	w.settings.TerminalPosition = "right"
	w.termWidth, w.termDrag = 420, 420
	tt.Frame()

	// The divider sits 1px left of the panel; dragging it 100 right shrinks
	// the panel from 420 to 320.
	x := float32(1280 - 420 - 1)
	tt.Press(x, 400)
	tt.Frame()
	tt.Move(x+100, 400)
	tt.Frame()
	tt.Release(x+100, 400)
	tt.Frame()
	if w.termWidth != termMinWidth {
		t.Fatalf("width %v after dragging right 100, want %v", w.termWidth, float32(termMinWidth))
	}
}

// TestTerminalPositionMenu checks the View menu's Terminal Position items,
// which update the settings and move the panel.
func TestTerminalPositionMenu(t *testing.T) {
	w, tt := newTestWindow(t, testRepo(t))
	termWindow(w, tt)
	w.termWidth, w.termDrag = 420, 420
	old := cfg.Get()
	off := cfg.OnChange(func(s Settings) { w.applySettings(s) })
	defer func() {
		off()
		cfg.Update(func(s *Settings) { *s = old })
	}()
	tt.Frame()

	item := buildMenu().ItemByID("term-right")
	if item == nil || item.Click == nil {
		t.Fatal("the menu has no Terminal Position: Right")
	}
	item.Click(item, nil)
	tt.Frame()
	if w.settings.TerminalPosition != "right" {
		t.Fatalf("position %q after choosing Right", w.settings.TerminalPosition)
	}
	r, ok := tt.Find("zsh")
	if !ok || r.X < 800 {
		t.Fatalf("the panel did not move right: tab x %v, found %v", r.X, ok)
	}
}

// termLine is what row y of a session shows, without the padding.
func termLine(s *termSession, y int) string {
	var b strings.Builder
	for _, g := range s.term.Line(y, nil) {
		if g.Width != 0 {
			b.WriteString(g.Content)
		}
	}
	return strings.TrimRight(b.String(), " ")
}

// TestTerminalReflowOnPositionSwitch checks that the content printed in a
// narrow pane is re-wrapped when the panel moves to a wider one.
func TestTerminalReflowOnPositionSwitch(t *testing.T) {
	w, tt := newTestWindow(t, testRepo(t))
	termWindow(w, tt)
	w.settings.TerminalPosition = "right"
	w.termWidth, w.termDrag = 420, 420
	tt.Frame()

	const line = "The quick brown fox jumps over the lazy dog, and keeps running far past the edge of a narrow pane."
	s := w.terms[0]
	s.term.Write([]byte(line))
	tt.Frame()
	narrow, _ := s.term.Size()
	if got := termLine(s, 0); got == line {
		t.Fatalf("the line did not wrap in a %d-column pane: %q", narrow, got)
	}

	w.settings.TerminalPosition = "bottom"
	tt.Frame()
	tt.Frame()
	wide, _ := s.term.Size()
	if wide <= narrow {
		t.Fatalf("the pane did not widen: %d columns, was %d", wide, narrow)
	}
	if got := termLine(s, 0); got != line {
		t.Fatalf("after moving to the bottom, row 0 = %q", got)
	}
}

// TestTerminalSlidesOpenAndClosed checks that the panel takes the review's
// room as it opens and gives it back as it closes, and that the closed
// panel stops building its tabs.
func TestTerminalSlidesOpenAndClosed(t *testing.T) {
	w, tt := newTestWindow(t, testRepo(t))
	// No shell: the panel is what slides, not what it runs.
	s := newTermSession(w)
	s.name = "zsh"
	w.terms, w.termActive = []*termSession{s}, 0
	w.termShown = true
	tt.Frame()
	time.Sleep(termSlide + 60*time.Millisecond)
	tt.Frame()
	if w.termOpen != 1 {
		t.Fatalf("the panel opened to %v, not 1", w.termOpen)
	}
	if _, ok := tt.Find("zsh"); !ok {
		t.Fatalf("the open panel shows no tab: %v", tt.Texts())
	}
	w.termShown, w.termClosing = false, true
	tt.Frame()
	time.Sleep(termSlide + 60*time.Millisecond)
	tt.Frame()
	if w.termOpen != 0 || w.termClosing {
		t.Fatalf("the panel closed to %v, still closing %v", w.termOpen, w.termClosing)
	}
	if _, ok := tt.Find("zsh"); ok {
		t.Fatalf("the closed panel still builds its tabs: %v", tt.Texts())
	}
}

// TestTerminalWrapsWhileDragging checks that the screen follows the divider
// as it is dragged, re-wrapping at the width the pane takes every frame, and
// that the history re-wraps whole once the drag rests.
func TestTerminalWrapsWhileDragging(t *testing.T) {
	w, tt := newTestWindow(t, testRepo(t))
	termWindow(w, tt)
	w.settings.TerminalPosition = "right"
	w.termWidth, w.termDrag = 420, 420
	tt.Frame()

	const text = "The quick brown fox jumps over the lazy dog, and keeps running far past the edge of the pane."
	s := w.terms[0]
	s.term.Write([]byte(text))
	tt.Frame()
	beforeCols, _ := s.term.Size()
	firstLine := termLine(s, 0)

	// Drag the divider left, widening the pane. (A frame lays the pane out;
	// the next one reads the width it took.)
	x := float32(1280 - 420 - 1)
	tt.Press(x, 400)
	tt.Frame()
	tt.Move(x-120, 400)
	tt.Frame()
	tt.Frame()
	cols, _ := s.term.Size()
	if cols <= beforeCols {
		t.Fatalf("the screen did not re-wrap as the drag began: %d columns, was %d", cols, beforeCols)
	}
	if line := termLine(s, 0); len(line) <= len(firstLine) {
		t.Fatalf("the dragged screen wraps at no wider a line: %q, was %q", line, firstLine)
	}

	// It follows the drag frame by frame, not only as the drag rests.
	tt.Move(x-150, 400)
	tt.Frame()
	tt.Frame()
	if wider, _ := s.term.Size(); wider <= cols {
		t.Fatalf("the screen did not follow the drag: %d columns, was %d", wider, cols)
	}
	// Once the drag rests, the history takes the width the screen took.
	tt.Release(x-150, 400)
	time.Sleep(termReflowRate + 20*time.Millisecond)
	tt.Frame()
	tt.Frame()
	if s.term.LogStale() {
		t.Error("the history still waits for the width the drag left it at")
	}
}
