package main

import (
	"strings"
	"testing"
)

// termWindow gives the window a terminal session that runs no shell, so the
// panel builds without a pty.
func termWindow(w *window) {
	s := newTermSession(w)
	s.name = "zsh"
	w.terms = []*termSession{s}
	w.termActive = 0
	w.termShown = true
}

func TestTerminalHideButton(t *testing.T) {
	w, tt := newTestWindow(t, testRepo(t))
	termWindow(w)
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
	termWindow(w)
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
	termWindow(w)
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
	termWindow(w)
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
	termWindow(w)
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
