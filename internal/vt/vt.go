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
	"strings"
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
	// logStale is set when ReflowScreen re-wrapped the screen alone: the
	// scrollback still holds the width it had, until ReflowLog comes.
	logStale bool
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
	t.resizeLocked(cols, rows)
}

// resizeLocked is Resize with mu held.
func (t *Term) resizeLocked(cols, rows int) {
	t.cols, t.rows = cols, rows
	t.emu.Resize(cols, rows)
}

// Reflow makes the terminal the new size and re-wraps what it already holds
// to the new width — the scrollback and the screen, up to the cursor — as a
// terminal that reflows does when its window changes. The width a line
// wrapped at is not recorded, so a line filled to its last column is taken
// for a wrapped one. The alternate screen, a hidden cursor and a screen a
// program drew with the cursor moved about only resize: their layout belongs
// to the program.
func (t *Term) Reflow(cols, rows int) {
	if cols <= 0 || rows <= 0 {
		return
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	t.logStale = false
	if cols == t.cols && rows == t.rows {
		return
	}
	if _, _, ok := t.logEndLocked(); !ok {
		t.resizeLocked(cols, rows)
		return
	}
	lines, ok := t.captureLocked()
	if !ok {
		t.resizeLocked(cols, rows)
		return
	}
	t.resizeLocked(cols, rows)
	t.rewriteLocked(lines)
}

// ReflowScreen makes the terminal the new size and re-wraps the screen to
// the width, leaving the scrollback as it was. A drag of the panel's
// divider goes by it: the screen, and the prompt a shell redraws after it,
// follows the drag at once, and the history re-wraps when the drag rests,
// with ReflowLog. The alternate screen, a hidden cursor and a screen a
// program drew with the cursor moved about keep their size, which belongs
// to the program: resizing them alone would cut what no longer fits.
func (t *Term) ReflowScreen(cols, rows int) {
	if cols <= 0 || rows <= 0 {
		return
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	if cols == t.cols && rows == t.rows {
		return
	}
	cx, cy, ok := t.logEndLocked()
	if !ok {
		// A screen this cannot rebuild keeps its width: resizing it alone
		// would cut the lines that no longer fit, and a drag would cut
		// them again at every step.
		return
	}
	lines := t.captureScreenLocked(cx, cy)
	t.resizeLocked(cols, rows)
	t.logStale = true
	// Clear the screen alone — not the history — home the cursor and the
	// pen, and write the lines as Reflow does: the screen keeps to its
	// top, as a terminal's does, and the rows a narrower width adds scroll
	// into the scrollback, as a log's do.
	if _, err := t.emu.Write([]byte("\x1b[H\x1b[0J\x1b[0m")); err != nil {
		return
	}
	var b strings.Builder
	for i, line := range lines {
		if i > 0 {
			b.WriteString("\r\n")
		}
		writeCells(&b, line)
	}
	_, _ = t.emu.Write([]byte(b.String()))
}

// ReflowLog re-wraps the screen and the scrollback at the size the
// terminal has, which ReflowScreen leaves the scrollback behind at: the
// screen followed a drag, the history did not. It does nothing when the
// history is already at that width.
func (t *Term) ReflowLog() {
	t.mu.Lock()
	defer t.mu.Unlock()
	if !t.logStale {
		return
	}
	t.logStale = false
	lines, ok := t.captureLocked()
	if !ok {
		return
	}
	t.rewriteLocked(lines)
}

// rewriteLocked clears the screen and the scrollback and writes the
// logical lines at the terminal's size, their wraps following its width.
func (t *Term) rewriteLocked(lines []vtLine) {
	// Clear the screen and the scrollback, home the cursor and the pen.
	if _, err := t.emu.Write([]byte("\x1b[3J\x1b[H\x1b[0m")); err != nil {
		return
	}
	var b strings.Builder
	for i, line := range lines {
		if i > 0 {
			b.WriteString("\r\n")
		}
		writeCells(&b, line)
	}
	_, _ = t.emu.Write([]byte(b.String()))
}

// vtLine is the cells of one logical line: a physical line the program wrote,
// with the lines it wrapped onto joined to it.
type vtLine []uv.Cell

// Reflowable reports whether Reflow would re-wrap the screen and the
// scrollback at a new width, rather than resize alone. The panel asks
// before it re-wraps mid-drag: a resize alone cuts the lines that no longer
// fit, and a drag would cut them again at every step.
func (t *Term) Reflowable() bool {
	t.mu.Lock()
	defer t.mu.Unlock()
	_, _, ok := t.logEndLocked()
	return ok
}

// logEndLocked reports the cursor's column and row when the screen is a log
// that ends there: nothing right of it nor below it. A reflow can rebuild
// that; anything else, the alternate screen and a hidden cursor included,
// belongs to the program.
func (t *Term) logEndLocked() (cx, cy int, ok bool) {
	if t.onAlt || t.emu.CursorHidden() {
		return 0, 0, false
	}
	p := t.emu.CursorPosition()
	cx, cy = p.X, p.Y
	if cy < 0 || cy >= t.rows {
		return 0, 0, false
	}
	cx = min(max(cx, 0), t.cols-1)
	for x := cx + 1; x < t.cols; x++ {
		if occupied(t.emu.CellAt(x, cy)) {
			return 0, 0, false
		}
	}
	for y := cy + 1; y < t.rows; y++ {
		for x := 0; x < t.cols; x++ {
			if occupied(t.emu.CellAt(x, y)) {
				return 0, 0, false
			}
		}
	}
	return cx, cy, true
}

// captureLocked reads the screen and the scrollback as logical lines, up to
// the cursor. It reports false when the screen is not a log that ends at the
// cursor, which a reflow cannot rebuild.
func (t *Term) captureLocked() ([]vtLine, bool) {
	cx, cy, ok := t.logEndLocked()
	if !ok {
		return nil, false
	}
	sb := t.emu.ScrollbackLen()
	scroll := t.emu.Scrollback()
	// A drag re-wraps the screen alone: the scrollback may still hold the
	// wider lines it had then, and reading them at the screen's width would
	// cut the ends of them off.
	width := func(y int) int {
		if y < sb && scroll != nil {
			return max(t.cols, len(scroll.Line(y)))
		}
		return t.cols
	}
	return t.linesLocked(sb+cy+1, width, func(x, y int) *uv.Cell {
		if y < sb {
			return t.emu.ScrollbackCellAt(x, y)
		}
		return t.emu.CellAt(x, y-sb)
	}, sb+cy, cx), true
}

// captureScreenLocked reads the screen alone as logical lines, up to the
// cursor at (cx, cy).
func (t *Term) captureScreenLocked(cx, cy int) []vtLine {
	return t.linesLocked(cy+1, func(int) int { return t.cols }, t.emu.CellAt, cy, cx)
}

// linesLocked reads the first n rows through at as logical lines, joining
// the rows a line wrapped onto: a row filled to its last column continues on
// the next, where width says that column. Row curRow ends at column cx,
// where the cursor sits, so the cells it sits after stay in the line even
// where they are blank.
func (t *Term) linesLocked(n int, width func(y int) int, at func(x, y int) *uv.Cell, curRow, cx int) []vtLine {
	var lines []vtLine
	prevFull := false
	for y := 0; y < n; y++ {
		w := max(width(y), 1)
		row := make(vtLine, w)
		for x := range row {
			if c := at(x, y); c != nil {
				row[x] = *c
			}
		}
		last := lastOccupied(row)
		full := last == w-1
		end := last + 1
		if y == curRow && end < cx {
			end = cx
		}
		row = row[:end]
		switch {
		case prevFull && len(lines) > 0:
			lines[len(lines)-1] = append(lines[len(lines)-1], row...)
		default:
			lines = append(lines, append(vtLine(nil), row...))
		}
		prevFull = full
	}
	return lines
}

// occupied reports whether a cell shows anything but its background.
func occupied(c *uv.Cell) bool {
	return c != nil && c.Content != "" && c.Content != " "
}

// lastOccupied is the index of the last cell showing anything, -1 for none.
func lastOccupied(line vtLine) int {
	for x := len(line) - 1; x >= 0; x-- {
		if occupied(&line[x]) {
			return x
		}
	}
	return -1
}

// writeCells writes a logical line with the styles its cells carry.
func writeCells(b *strings.Builder, line vtLine) {
	for i := 0; i < len(line); {
		style := line[i].Style
		j := i + 1
		for j < len(line) && line[j].Style.Equal(&style) {
			j++
		}
		var text strings.Builder
		for _, c := range line[i:j] {
			switch {
			case c.Width == 0 && c.Content == "":
				// The shadow of a wide character, which the character itself
				// takes the columns of.
			case c.Content == "":
				text.WriteByte(' ')
			default:
				text.WriteString(c.Content)
			}
		}
		if text.Len() > 0 {
			b.WriteString(style.Styled(text.String()))
		}
		i = j
	}
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

// LogStale reports whether a drag re-wrapped the screen alone: the
// scrollback keeps the width it had until ReflowLog re-wraps it.
func (t *Term) LogStale() bool {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.logStale
}
