package vt

import (
	"strings"
	"testing"
	"time"

	"github.com/charmbracelet/x/ansi"
)

// newTerm makes a terminal of 10 by 4 for the tests to write to.
func newTerm() *Term {
	return New(10, 4)
}

// put writes s and returns the text of row y, as the cells show it.
func put(t *testing.T, s string, y int) string {
	t.Helper()
	term := newTerm()
	term.Write([]byte(s))
	return text(term, y)
}

// text reads row y, skipping the shadow cells of wide characters.
func text(term *Term, y int) string {
	var b strings.Builder
	for _, g := range term.Line(y, nil) {
		if g.Width != 0 {
			b.WriteString(g.Content)
		}
	}
	return b.String()
}

func TestPlainText(t *testing.T) {
	if got := put(t, "hello\r\nworld", 0); got != "hello     " {
		t.Errorf("row 0 = %q", got)
	}
	term := newTerm()
	term.Write([]byte("hello\r\nworld"))
	if c := term.Cursor(); c.Y != 1 || c.X != 5 {
		t.Errorf("cursor = %d,%d", c.X, c.Y)
	}
}

// A bare newline keeps the column, as only CRLF returns.
func TestNewlineKeepsColumn(t *testing.T) {
	if got := put(t, "abc\ndef", 1); got != "   def    " {
		t.Errorf("row 1 = %q", got)
	}
}

func TestCursorAddressing(t *testing.T) {
	term := newTerm()
	term.Write([]byte("\x1b[2;3HX"))
	if got := text(term, 1); got != "  X       " {
		t.Errorf("row 1 = %q", got)
	}
}

func TestSGRColors(t *testing.T) {
	term := newTerm()
	term.Write([]byte("\x1b[31mR\x1b[0mn\x1b[38;5;196mS"))
	l := term.Line(0, nil)
	if fg, ok := l[0].FG.(ansi.BasicColor); !ok || fg != 1 {
		t.Errorf("glyph 0 fg = %v, want basic 1", l[0].FG)
	}
	if l[1].FG != nil {
		t.Errorf("glyph 1 fg = %v, want the default", l[1].FG)
	}
	if fg, ok := l[2].FG.(ansi.IndexedColor); !ok || fg != 196 {
		t.Errorf("glyph 2 fg = %v, want indexed 196", l[2].FG)
	}
}

func TestSGRAttrs(t *testing.T) {
	term := newTerm()
	term.Write([]byte("\x1b[1;3;4;9mbold\x1b[0mplain"))
	l := term.Line(0, nil)
	want := AttrBold | AttrItalic | AttrStrike
	if l[0].Attr&want != want || !l[0].Underline {
		t.Errorf("glyph 0 = %+v", l[0])
	}
	if l[4].Attr != 0 || l[4].Underline {
		t.Errorf("glyph 4 = %+v", l[4])
	}
}

func TestAutowrap(t *testing.T) {
	term := newTerm()
	term.Write([]byte("0123456789abc"))
	if got := text(term, 0); got != "0123456789" {
		t.Errorf("row 0 = %q", got)
	}
	if got := text(term, 1); got != "abc       " {
		t.Errorf("row 1 = %q", got)
	}
}

func TestScrollback(t *testing.T) {
	term := newTerm()
	term.Write([]byte("one\r\ntwo\r\nthree\r\nfour\r\nfive"))
	if n := term.Scrollback(); n != 1 {
		t.Errorf("scrollback = %d", n)
	}
	if got := text(term, 1); got != "two       " {
		t.Errorf("row 1 = %q", got)
	}
	if got := text(term, 4); got != "five      " {
		t.Errorf("row 4 = %q", got)
	}
	if c := term.Cursor(); c.ViewY != 4 {
		t.Errorf("cursor view y = %d", c.ViewY)
	}
}

func TestAlternateScreen(t *testing.T) {
	term := newTerm()
	term.Write([]byte("main\x1b[?1049h\x1b[Halt"))
	if !term.OnAlt() {
		t.Error("the alternate screen did not show")
	}
	if got := text(term, 0); got != "alt       " {
		t.Errorf("on alt, row 0 = %q", got)
	}
	if n := term.Scrollback(); n != 0 {
		t.Errorf("on alt, scrollback = %d", n)
	}
	term.Write([]byte("\x1b[?1049l"))
	if got := text(term, 0); got != "main      " {
		t.Errorf("after leaving, row 0 = %q", got)
	}
}

// Emoji that render in their two-cell form by default take two cells,
// as the programs that write them count them; the box drawing around a
// TUI then lines up.
func TestEmojiWidth(t *testing.T) {
	term := newTerm()
	term.Write([]byte("⏺x")) // the spinner dot of agent TUIs
	l := term.Line(0, nil)
	// The dot is of ambiguous width: one cell, as the wcwidth terminals
	// draw it; the emoji that show wide by default take two.
	if l[0].Content != "⏺" || l[0].Width != 1 {
		t.Errorf("glyph 0 = %+v, want ⏺ in one cell", l[0])
	}
	if l[1].Content != "x" || l[1].Width != 1 {
		t.Errorf("glyph 1 = %+v, want x in the next cell", l[1])
	}
	term.Write([]byte("\r\n🎉y"))
	l = term.Line(1, nil)
	if l[0].Width != 2 {
		t.Errorf("🎉 width = %d, want 2", l[0].Width)
	}
	if l[2].Content != "y" {
		t.Errorf("glyph after 🎉 = %+v, want y", l[2])
	}
}

func TestTitle(t *testing.T) {
	term := newTerm()
	term.Write([]byte("\x1b]2;my title\x07"))
	if got := term.Title(); got != "my title" {
		t.Errorf("title = %q", got)
	}
	term.Write([]byte("\x1b]0;other\x1b\\"))
	if got := term.Title(); got != "other" {
		t.Errorf("title = %q", got)
	}
}

func TestReplies(t *testing.T) {
	term := newTerm()
	term.Write([]byte("\x1b[2;3H\x1b[6n"))
	// The replies come through a pump of its own: they take a moment.
	var got []byte
	for range 100 {
		if got = term.Replies(); got != nil {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if string(got) != "\x1b[2;3R" {
		t.Errorf("reply = %q", got)
	}
}

func TestModes(t *testing.T) {
	term := newTerm()
	term.Write([]byte("\x1b[?1h\x1b[?25l\x1b[?2004h"))
	if !term.AppCursorKeys() || term.Cursor().Visible || !term.BracketedPaste() {
		t.Error("the private modes did not set")
	}
	term.Write([]byte("\x1b[?1l\x1b[?25h\x1b[?2004l"))
	if term.AppCursorKeys() || !term.Cursor().Visible || term.BracketedPaste() {
		t.Error("the private modes did not clear")
	}
}

// The scroll region bounds the scrolling itself; x/vt lets the cursor
// address and move past its margins, as some terminals do.
func TestScrollRegion(t *testing.T) {
	term := newTerm()
	term.Write([]byte("a\r\nb\r\nc\r\nd\x1b[2;3r\x1b[3;1H\r\n"))
	if c := term.Cursor(); c.Y != 2 {
		t.Errorf("cursor y = %d", c.Y)
	}
}

func TestResize(t *testing.T) {
	term := New(10, 3)
	term.Write([]byte("abcdefghij\r\nk"))
	term.Resize(6, 4)
	if got := text(term, 0); got != "abcdef" {
		t.Errorf("row 0 = %q", got)
	}
	if got := text(term, 1); got != "k     " {
		t.Errorf("row 1 = %q", got)
	}
	term.Resize(8, 2)
	if got := text(term, 0); got != "abcdef  " {
		t.Errorf("row 0 = %q", got)
	}
}

func TestErase(t *testing.T) {
	term := newTerm()
	term.Write([]byte("abcdefghij\x1b[1;1H\x1b[K")) // clear to the right
	if got := text(term, 0); got != "          " {
		t.Errorf("after EL0 row 0 = %q", got)
	}
	term.Write([]byte("\x1b[2;2Hfilled\x1b[2;2H\x1b[1X")) // erase 1 at (1,1)
	if got := text(term, 1); got != "  illed   " {
		t.Errorf("after ECH row 1 = %q", got)
	}
}

// Erasing clears to the default background; the emulator leaves the pen's
// color out of erased cells, unlike the xterm's back color erase.
func TestEraseClearsToDefault(t *testing.T) {
	term := newTerm()
	term.Write([]byte("\x1b[41m\x1b[2J"))
	if bg := term.Line(0, nil)[5].BG; bg != nil {
		t.Errorf("bg = %v, want nil for the default", bg)
	}
}

func TestInsertDeleteLines(t *testing.T) {
	term := newTerm()
	term.Write([]byte("a\r\nb\r\nc\r\nd\x1b[1;1H\x1b[2L"))
	if got := text(term, 2); got != "a         " {
		t.Errorf("row 2 = %q, want a pushed down", got)
	}
	// Deleting them again leaves a at the top; x/vt keeps the deleted
	// lines in the scrollback, so the view counts two rows above.
	term.Write([]byte("\x1b[2M"))
	if got := text(term, 2); got != "a         " {
		t.Errorf("row 2 = %q, want a back at the top", got)
	}
}

func TestSaveRestoreCursor(t *testing.T) {
	term := newTerm()
	term.Write([]byte("\x1b[2;2H\x1b[31m\x1b7\x1b[Hy\x1b8z"))
	if got := text(term, 1); got != " z        " {
		t.Errorf("row 1 = %q, want z at the saved place", got)
	}
	if fg, ok := term.Line(1, nil)[1].FG.(ansi.BasicColor); !ok || fg != 1 {
		t.Errorf("the pen was not restored")
	}
}

// Writing queries while the view renders must not deadlock: the emulator
// answers a query through a blocking pipe inside Write, so the goroutine
// that drains the replies may never wait on the write's own lock — else
// a chunk with two queries stops the world.
func TestConcurrentQueriesWhileRendering(t *testing.T) {
	term := newTerm()
	done := make(chan struct{})
	go func() {
		defer close(done)
		// Two queries a chunk, with in-band resize on: the emulator then
		// answers every cursor move as well, as a busy TUI sees.
		payload := []byte("\x1b[?2048hwork\x1b[6n\x1b[c\x1b[?2026$p\x1b[2;3H\r\n")
		for i := 0; i < 20000; i++ {
			term.Write(payload)
			term.Resize(80, 24)
		}
	}()
	for {
		select {
		case <-done:
			term.Close()
			return
		default:
		}
		term.Line(0, make([]Glyph, 10))
		term.Count()
		term.Cursor()
	}
}

// Reflow re-wraps the lines the terminal already holds to the new width.
func TestReflowWidens(t *testing.T) {
	term := New(10, 4)
	term.Write([]byte("hello world!"))
	if got := text(term, 0); got != "hello worl" {
		t.Fatalf("row 0 = %q", got)
	}
	if got := text(term, 1); got != "d!        " {
		t.Fatalf("row 1 = %q", got)
	}
	term.Reflow(20, 4)
	if got := text(term, 0); got != "hello world!        " {
		t.Errorf("after widening, row 0 = %q", got)
	}
	if c := term.Cursor(); c.X != 12 || c.Y != 0 {
		t.Errorf("after widening, cursor = %d,%d", c.X, c.Y)
	}
}

// Reflow narrows a line onto the rows it wraps onto.
func TestReflowNarrows(t *testing.T) {
	term := New(20, 4)
	term.Write([]byte("hello world!"))
	term.Reflow(10, 4)
	if got := text(term, 0); got != "hello worl" {
		t.Errorf("row 0 = %q", got)
	}
	if got := text(term, 1); got != "d!        " {
		t.Errorf("row 1 = %q", got)
	}
	if c := term.Cursor(); c.X != 2 || c.Y != 1 {
		t.Errorf("after narrowing, cursor = %d,%d", c.X, c.Y)
	}
}

// Reflow keeps the colors of the cells it rewrites.
func TestReflowKeepsStyle(t *testing.T) {
	term := New(10, 4)
	term.Write([]byte("\x1b[31mhello world\x1b[0m!"))
	term.Reflow(20, 4)
	l := term.Line(0, nil)
	if fg, ok := l[0].FG.(ansi.BasicColor); !ok || fg != 1 {
		t.Errorf("glyph 0 fg = %v, want basic 1", l[0].FG)
	}
	if l[12].FG != nil {
		t.Errorf("glyph 12 fg = %v, want the default", l[12].FG)
	}
}

// Text below the cursor belongs to a program's own layout, which a reflow
// leaves alone.
func TestReflowLeavesCursorAddressedScreens(t *testing.T) {
	term := New(10, 4)
	term.Write([]byte("top\x1b[3;1Hbottom"))
	term.Reflow(20, 4)
	// The screen resized, but nothing was rejoined nor moved.
	if got := text(term, 0); got != "top                 " {
		t.Errorf("row 0 = %q", got)
	}
	if got := text(term, 2); got != "bottom              " {
		t.Errorf("row 2 = %q", got)
	}
}

// screensText is every row of the terminal, the scrollback's first, as the
// cells show them.
func screensText(term *Term) []string {
	rows := make([]string, 0, term.Count())
	for i := 0; i < term.Count(); i++ {
		rows = append(rows, strings.TrimRight(text(term, i), " "))
	}
	return rows
}

// sameText reports whether two screens say the same, however their lines
// break: a reflow moves the breaks, not the words.
func sameText(a, b []string) bool {
	flat := func(rows []string) string {
		return strings.Join(strings.Fields(strings.Join(rows, "")), "")
	}
	return flat(a) == flat(b)
}

// ReflowScreen re-wraps the screen alone, as a drag of the panel's divider
// does: the shell follows the drag at once, and the history the drag leaves
// behind re-wraps when it rests, with ReflowLog.
func TestReflowScreenKeepsHistory(t *testing.T) {
	term := New(20, 4)
	term.Write([]byte("the first line is long enough to wrap\r\nthe second line too, and long\r\nthird"))
	before := screensText(term)

	term.ReflowScreen(10, 4)
	if got := text(term, 0); got != "the first " {
		t.Errorf("the screen did not re-wrap at 10 columns: row 0 = %q", got)
	}
	term.ReflowLog()
	if got := screensText(term); !sameText(before, got) {
		t.Errorf("the history lost its text:\n before %q\n after  %q", before, got)
	}
}

// A screen re-wrapped narrow leaves history lines wider than the screen,
// which ReflowLog must read whole rather than cut at its width.
func TestReflowLogKeepsWiderHistory(t *testing.T) {
	term := New(30, 4)
	term.Write([]byte(strings.Repeat("h", 64) + "\r\n12345"))
	before := screensText(term)

	term.ReflowScreen(12, 4)
	term.ReflowLog()
	if got := screensText(term); !sameText(before, got) {
		t.Errorf("the history lost its ends:\n before %q\n after  %q", before, got)
	}
}
