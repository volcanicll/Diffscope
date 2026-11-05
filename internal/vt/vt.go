// Package vt adapts charmbracelet/x/vt's terminal emulator, the same
// mature core that drives Go TUIs, to the terminal panel: it turns the
// bytes a program writes into rows of cells the view renders, and keeps
// the state the keyboard and the session ask about. The emulator knows
// the protocols — UTF-8 and grapheme clusters, the C0 controls, the CSI
// and OSC sequences, colors up to truecolor, the alternate screen and
// the scrollback — so this layer only translates.
package vt

import (
	"image/color"
	"io"
	"sync"

	uv "github.com/charmbracelet/ultraviolet"
	"github.com/charmbracelet/x/ansi"
	cvt "github.com/charmbracelet/x/vt"
)

// Attr is a cell's rendering attributes besides its colors.
type Attr uint8

const (
	AttrBold Attr = 1 << iota
	AttrDim
	AttrItalic
	AttrBlink
	AttrReverse
	AttrInvisible
	AttrStrike
)

// Glyph is one cell of the screen: a grapheme cluster — mostly one
// character, sometimes an emoji composed of several runes — its width in
// cells, and its colors. Nil colors are the theme's defaults.
type Glyph struct {
	Content   string
	Width     int // 1, 2 for a wide cluster, 0 for the shadow of one
	FG, BG    color.Color
	Attr      Attr
	Underline bool
}

// Cursor is where the cursor is: on the screen, and in the view, which
// counts the rows of scrollback above it.
type Cursor struct {
	X, Y    int
	ViewY   int
	Visible bool
}

// Term is the emulator and everything around it that the terminal panel
// reads. Write feeds it the program's output; the accessors read it back
// for rendering. It is safe for one writer and any number of readers.
type Term struct {
	mu         sync.Mutex
	emu        *cvt.Emulator
	cols, rows int
	onAlt      bool
	appCursor  bool
	bracketed  bool
	title      string
	// replyMu guards the replies alone, apart from mu: the pump must
	// collect them without waiting on mu, or a chunk carrying two answers
	// deadlocks the write that holds mu against the very pipe that waits
	// on the pump.
	replyMu sync.Mutex
	replies []byte
	// done closes when the pump is over, so Close ends it before the
	// emulator's own Close, whose flags the pump's Read reads unlocked.
	done chan struct{}
}

// New makes a terminal of the given size, its cursor at home.
func New(cols, rows int) *Term {
	t := &Term{cols: cols, rows: rows, done: make(chan struct{})}
	t.emu = cvt.NewEmulator(cols, rows)
	t.emu.SetCallbacks(cvt.Callbacks{
		// The callbacks run inside Write, which holds the lock, so they
		// set the state plainly: the readers wait on the same lock.
		Title:       func(s string) { t.title = s },
		AltScreen:   func(on bool) { t.onAlt = on },
		EnableMode:  func(m ansi.Mode) { t.setMode(m, true) },
		DisableMode: func(m ansi.Mode) { t.setMode(m, false) },
	})
	go t.pump()
	return t
}

// setMode tracks the modes the keyboard and the session go by.
func (t *Term) setMode(m ansi.Mode, on bool) {
	switch m {
	case ansi.ModeCursorKeys:
		t.appCursor = on
	case ansi.ModeBracketedPaste:
		t.bracketed = on
	}
}

// pump collects what the program asks the terminal to send back, as a
// cursor report, which the emulator hands over through a blocking pipe:
// a goroutine of its own reads it so Write never waits.
func (t *Term) pump() {
	defer close(t.done)
	buf := make([]byte, 0x1000)
	for {
		n, err := t.emu.Read(buf)
		if n > 0 {
			t.replyMu.Lock()
			t.replies = append(t.replies, buf[:n]...)
			t.replyMu.Unlock()
		}
		if err != nil {
			return
		}
	}
}

// Close ends the terminal, which stops the replies pump. The pump waits
// on the emulator's pipe, so the write end closes first to wake it, and
// the emulator's own Close waits for the pump to be gone: the emulator
// reads its closed flag in Read without a lock.
func (t *Term) Close() {
	t.mu.Lock()
	defer t.mu.Unlock()
	if w, ok := t.emu.InputPipe().(interface{ CloseWithError(error) error }); ok {
		w.CloseWithError(io.EOF)
		<-t.done
	}
	t.emu.Close()
}

// Write parses the bytes a program wrote, as many at a time as arrive.
func (t *Term) Write(p []byte) (int, error) {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.emu.Write(p)
}

// Resize makes the terminal the new size, keeping what it can of the
// screens, and the scrollback.
func (t *Term) Resize(cols, rows int) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.cols, t.rows = cols, rows
	t.emu.Resize(cols, rows)
}

// Size is the terminal's size in columns and rows.
func (t *Term) Size() (cols, rows int) {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.cols, t.rows
}

// Count is how many rows the view shows: the screen, plus the scrollback
// above it, of which the alternate screen has none.
func (t *Term) Count() int {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.onAlt {
		return t.rows
	}
	return t.emu.ScrollbackLen() + t.rows
}

// Scrollback is how many rows of history the screen has above it.
func (t *Term) Scrollback() int {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.onAlt {
		return 0
	}
	return t.emu.ScrollbackLen()
}

// OnAlt reports whether the alternate screen is showing.
func (t *Term) OnAlt() bool {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.onAlt
}

// Line copies view row i into buf, padded to the width, for rendering.
func (t *Term) Line(i int, buf []Glyph) []Glyph {
	t.mu.Lock()
	defer t.mu.Unlock()
	sb := 0
	if !t.onAlt {
		sb = t.emu.ScrollbackLen()
	}
	if i < 0 || i >= sb+t.rows {
		return buf[:0]
	}
	if cap(buf) < t.cols {
		buf = make([]Glyph, t.cols)
	}
	buf = buf[:t.cols]
	for x := range buf {
		var c *uv.Cell
		if i < sb {
			c = t.emu.ScrollbackCellAt(x, i)
		} else if t.onAlt {
			c = t.emu.CellAt(x, i)
		} else {
			c = t.emu.CellAt(x, i-sb)
		}
		g := Glyph{Content: " ", Width: 1}
		if c != nil {
			g.Content, g.Width = c.Content, c.Width
			if g.Content == "" {
				g.Content, g.Width = " ", 1
			}
			s := c.Style
			g.FG, g.BG = s.Fg, s.Bg
			if s.Attrs&uv.AttrBold != 0 {
				g.Attr |= AttrBold
			}
			if s.Attrs&uv.AttrFaint != 0 {
				g.Attr |= AttrDim
			}
			if s.Attrs&uv.AttrItalic != 0 {
				g.Attr |= AttrItalic
			}
			if s.Attrs&uv.AttrBlink != 0 {
				g.Attr |= AttrBlink
			}
			if s.Attrs&uv.AttrReverse != 0 {
				g.Attr |= AttrReverse
			}
			if s.Attrs&uv.AttrConceal != 0 {
				g.Attr |= AttrInvisible
			}
			if s.Attrs&uv.AttrStrikethrough != 0 {
				g.Attr |= AttrStrike
			}
			g.Underline = s.Underline != ansi.UnderlineNone
		}
		buf[x] = g
	}
	return buf
}

// Cursor reports where the cursor is, and whether it shows.
func (t *Term) Cursor() Cursor {
	t.mu.Lock()
	defer t.mu.Unlock()
	p := t.emu.CursorPosition()
	c := Cursor{X: p.X, Y: p.Y, Visible: !t.emu.CursorHidden()}
	if !t.onAlt {
		c.ViewY = t.emu.ScrollbackLen() + p.Y
	} else {
		c.ViewY = p.Y
	}
	return c
}

// Title is the window title the program last set, as in OSC 2.
func (t *Term) Title() string {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.title
}

// AppCursorKeys reports whether DECCKM has the arrow keys send their
// application sequences, which the keyboard encodes by.
func (t *Term) AppCursorKeys() bool {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.appCursor
}

// BracketedPaste reports whether pastes are wrapped in the brackets the
// program asked for.
func (t *Term) BracketedPaste() bool {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.bracketed
}

// Paste writes text the clipboard holds as if typed, in the brackets the
// program asked for when it asked.
func (t *Term) Paste(text string) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.emu.Paste(text)
}

// Dump is a debug view of the emulator's screen, as its own renderer
// sees it.
func (t *Term) Dump() string {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.emu.Render()
}

// Replies takes what the program asked the terminal to send back, as a
// cursor report, to write to its input.
func (t *Term) Replies() []byte {
	t.replyMu.Lock()
	defer t.replyMu.Unlock()
	if len(t.replies) == 0 {
		return nil
	}
	out := t.replies
	t.replies = nil
	return out
}
