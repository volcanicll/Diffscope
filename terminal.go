package main

import (
	"image/color"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/charmbracelet/x/ansi"
	"github.com/creack/pty"
	"github.com/egoist/mygo"
	"github.com/egoist/mygo/ui"
	"github.com/volcanicll/diffscope/internal/shellenv"
	"github.com/volcanicll/diffscope/internal/vt"
)

// termSession runs a shell in the terminal panel: its pty, the emulator
// that reads it, the list that shows its screen and the selection the
// user drags over it. A window keeps any number of them, a tab each.
type termSession struct {
	w    *window
	term *vt.Term
	tty  *os.File // the pty's master, what the shell reads and writes
	cmd  *exec.Cmd
	// name is what the tab says when the shell sets no title: the
	// program it runs, as "zsh".
	name string

	// list is its screen, which keeps its place and follows the end; text
	// brings the typed keys and the input method's composition; buf holds
	// the row read back for rendering. focus asks the next frame to give
	// it the keys.
	list  ui.ListState
	text  *termTextClient
	buf   []vt.Glyph
	focus bool

	mu      sync.Mutex
	started bool // the shell has been run once
	exited  bool
	closed  bool

	// The selection, as view rows and columns, in the order chosen: a
	// before b as the user dragged, normalized when read.
	selOn      bool
	selA, selB termCell
	// altPending is set when a key with alt came whose text follows: the
	// text alone does not say alt was held, and an escape goes before it.
	altPending bool
	// ptyCols, ptyRows are the size the pty last carried, to keep its
	// sync with the emulator when the shell starts late.
	ptyCols, ptyRows int
}

// termCell is a place on the screen: a view row, counting the
// scrollback, and a column.
type termCell struct {
	row, col int
}

func newTermSession(w *window) *termSession {
	s := &termSession{w: w, term: vt.New(80, 24)}
	s.text = &termTextClient{w: w, s: s}
	s.list.FollowEnd = true
	s.list.Key = func(i int) any { return s.term.Count() - 1 - i }
	return s
}

// tabTitle is what its tab shows: the title the shell set, or the
// program's name.
func (s *termSession) tabTitle() string {
	if title := s.term.Title(); title != "" {
		return title
	}
	if s.name != "" {
		return s.name
	}
	return "Terminal"
}

// start runs the user's shell on a pty at the size the panel shows. It runs
// once, after the first frame has laid the panel out: the shell's greeting
// wraps at the width it is written in, and resizing the pty afterwards does
// not reflow what it already printed.
func (s *termSession) start() {
	s.mu.Lock()
	if s.started || s.closed || s.exited {
		s.mu.Unlock()
		return
	}
	s.started = true
	s.mu.Unlock()

	shell := os.Getenv("SHELL")
	if shell == "" {
		shell = "/bin/zsh"
	}
	cols, rows := s.term.Size()
	cmd := exec.Command(shell)
	cmd.Dir = s.w.repo.Root
	cmd.Env = append(shellenv.Environ(), "TERM=xterm-256color", "COLORTERM=truecolor")
	tty, err := pty.StartWithSize(cmd, &pty.Winsize{Rows: uint16(rows), Cols: uint16(cols)})
	if err != nil {
		s.w.update(func() {
			s.mu.Lock()
			s.exited = true
			s.mu.Unlock()
		})
		mygo.Dialog.Error("Could not start the terminal", err.Error())
		return
	}
	s.mu.Lock()
	s.tty, s.cmd, s.exited, s.closed = tty, cmd, false, false
	s.name = filepath.Base(shell)
	s.ptyCols, s.ptyRows = cols, rows
	s.mu.Unlock()
	go s.read(tty)
	go s.wait(cmd, tty)
}

// startPending runs the shell of a session that has not run yet, now that
// the panel has measured the size it shows. A window without a real screen,
// as the tests drive, starts no shell.
func (s *termSession) startPending() {
	if s.w.win == nil {
		return
	}
	s.mu.Lock()
	pending := !s.started && !s.closed && !s.exited
	s.mu.Unlock()
	if pending {
		s.w.background(s.start)
	}
}

// read pours what the shell writes into the emulator, answering what it
// asks back, as a cursor report.
func (s *termSession) read(tty *os.File) {
	buf := make([]byte, 0x4000)
	for {
		n, err := tty.Read(buf)
		if n > 0 {
			s.term.Write(buf[:n])
			if reply := s.term.Replies(); len(reply) > 0 {
				s.write(reply)
			}
			s.w.invalidate()
		}
		if err != nil {
			return
		}
	}
}

// wait marks the session over when the shell is, so the panel offers to
// restart it.
func (s *termSession) wait(cmd *exec.Cmd, tty *os.File) {
	err := cmd.Wait()
	tty.Close()
	s.mu.Lock()
	s.exited = true
	closed := s.closed
	s.mu.Unlock()
	if closed {
		return
	}
	_ = err
	s.w.update(func() {})
}

// write sends the user's keys, or the emulator's replies, to the shell.
func (s *termSession) write(b []byte) {
	s.mu.Lock()
	tty := s.tty
	s.mu.Unlock()
	if tty != nil {
		tty.Write(b)
	}
}

// resize makes the emulator and the pty the new size, which tells the
// programs on it with a SIGWINCH. The two move separately: the shell may
// not have started when the first size comes, and its size catches up
// with the next frame after.
func (s *termSession) resize(cols, rows int) {
	if cols <= 0 || rows <= 0 {
		return
	}
	c, r := s.term.Size()
	changed := c != cols || r != rows
	if changed {
		// A dragged divider changes the pane every frame; reflow once it
		// rests, rather than re-wrapping the scrollback at every step.
		if s.w.termResizing {
			return
		}
		s.term.Reflow(cols, rows)
		if c != cols {
			s.clearSelection()
		}
	}
	s.mu.Lock()
	tty := s.tty
	due := tty != nil && (s.ptyCols != cols || s.ptyRows != rows)
	if due {
		s.ptyCols, s.ptyRows = cols, rows
	}
	s.mu.Unlock()
	if due {
		pty.Setsize(tty, &pty.Winsize{Rows: uint16(rows), Cols: uint16(cols)})
	}
	if changed {
		s.w.invalidate()
	}
}

// exited reports whether the shell is over.
func (s *termSession) exitedNow() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.exited
}

// close kills the shell and its children, as a terminal closing does.
func (s *termSession) close() {
	s.mu.Lock()
	tty, cmd := s.tty, s.cmd
	s.closed = true
	s.tty, s.cmd = nil, nil
	s.mu.Unlock()
	if cmd != nil && cmd.Process != nil {
		// The pty made the shell a session leader, so its group is its
		// own: the programs it runs go with it.
		syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
	}
	if tty != nil {
		tty.Close()
	}
	if s.term != nil {
		s.term.Close()
	}
}

// restart runs a new shell, as the panel's button does, at the size the
// panel shows rather than the 80x24 a screen starts with.
func (s *termSession) restart() {
	cols, rows := s.term.Size()
	s.close()
	s.selOn = false
	s.term = vt.New(cols, rows)
	s.list.Key = func(i int) any { return s.term.Count() - 1 - i }
	s.mu.Lock()
	s.started, s.closed, s.exited = false, false, false
	s.mu.Unlock()
	s.startPending()
}

// paste writes text the clipboard holds as if typed; the emulator wraps
// it in the brackets the program asked for when it asked.
func (s *termSession) paste(text string) {
	s.clearSelection()
	s.term.Paste(text)
}

// keyDown encodes a key press as the shell's input, in xterm's way. The
// keys that type return false, for their text to come as InputText; the
// command key belongs to the app's shortcuts.
func (s *termSession) keyDown(ev ui.InputEvent) ([]byte, bool) {
	if ev.Mods&ui.Cmd != 0 {
		return nil, false
	}
	k, mods := ev.Key, ev.Mods
	if mods&ui.Ctrl != 0 && mods&ui.Shift == 0 && mods&ui.Alt == 0 {
		if b, ok := ctrlByte(k); ok {
			return []byte{b}, true
		}
	}
	switch k {
	case ui.KeyEnter:
		return []byte{'\r'}, true
	case ui.KeyTab:
		if mods&ui.Shift != 0 {
			return []byte("\x1b[Z"), true
		}
		return []byte{'\t'}, true
	case ui.KeyBackspace:
		return []byte{0x7f}, true
	case ui.KeyEscape:
		return []byte{'\x1b'}, true
	case ui.KeyUp, ui.KeyDown, ui.KeyRight, ui.KeyLeft:
		final := byte("ABCD"[map[ui.Key]int{ui.KeyUp: 0, ui.KeyDown: 1, ui.KeyRight: 2, ui.KeyLeft: 3}[k]])
		if mods&(ui.Shift|ui.Alt|ui.Ctrl) != 0 {
			return []byte("\x1b[1;" + strconv.Itoa(modParam(mods)) + string(final)), true
		}
		if s.term.AppCursorKeys() {
			return []byte("\x1bO" + string(final)), true
		}
		return []byte("\x1b[" + string(final)), true
	case ui.KeyHome, ui.KeyEnd:
		final := byte('H')
		if k == ui.KeyEnd {
			final = 'F'
		}
		if mods&(ui.Shift|ui.Alt|ui.Ctrl) != 0 {
			return []byte("\x1b[1;" + strconv.Itoa(modParam(mods)) + string(final)), true
		}
		return []byte("\x1b[" + string(final)), true
	case ui.KeyInsert, ui.KeyDelete, ui.KeyPageUp, ui.KeyPageDown:
		n := map[ui.Key]int{ui.KeyInsert: 2, ui.KeyDelete: 3, ui.KeyPageUp: 5, ui.KeyPageDown: 6}[k]
		if mods&(ui.Shift|ui.Alt|ui.Ctrl) != 0 {
			return []byte("\x1b[" + strconv.Itoa(n) + ";" + strconv.Itoa(modParam(mods)) + "~"), true
		}
		return []byte("\x1b[" + strconv.Itoa(n) + "~"), true
	case ui.KeyF1, ui.KeyF2, ui.KeyF3, ui.KeyF4:
		final := byte("PQRS"[k-ui.KeyF1])
		if mods&(ui.Shift|ui.Alt|ui.Ctrl) != 0 {
			return []byte("\x1b[1;" + strconv.Itoa(modParam(mods)) + string(final)), true
		}
		return []byte("\x1bO" + string(final)), true
	case ui.KeyF5, ui.KeyF6, ui.KeyF7, ui.KeyF8, ui.KeyF9, ui.KeyF10, ui.KeyF11, ui.KeyF12:
		n := []int{15, 17, 18, 19, 20, 21, 23, 24}[k-ui.KeyF5]
		if mods&(ui.Shift|ui.Alt|ui.Ctrl) != 0 {
			return []byte("\x1b[" + strconv.Itoa(n) + ";" + strconv.Itoa(modParam(mods)) + "~"), true
		}
		return []byte("\x1b[" + strconv.Itoa(n) + "~"), true
	}
	return nil, false
}

// textBytes encodes typed text, which an alt key puts an escape before.
// The return and the newline came with their key.
func (s *termSession) textBytes(ev ui.InputEvent) []byte {
	if ev.Text == "" || ev.Text == "\r" || ev.Text == "\n" {
		return nil
	}
	if ev.Mods&ui.Alt != 0 {
		return append([]byte{'\x1b'}, ev.Text...)
	}
	return []byte(ev.Text)
}

// ctrlByte is the control character a key with control makes: Ctrl+H is
// backspace, Ctrl+[ escape, as the terminals do.
func ctrlByte(k ui.Key) (byte, bool) {
	switch {
	case k >= ui.KeyA && k <= ui.KeyZ:
		return byte(k-ui.KeyA) + 1, true
	case k == ui.KeySpace:
		return 0, true
	case k == ui.KeyBracketLeft:
		return 0x1b, true
	case k == ui.KeyBackslash:
		return 0x1c, true
	case k == ui.KeyBracketRight:
		return 0x1d, true
	}
	return 0, false
}

// modParam is xterm's modifier parameter: 1, plus 1 for shift, 2 for
// alt and 4 for control.
func modParam(m ui.Modifiers) int {
	n := 1
	if m&ui.Shift != 0 {
		n += 1
	}
	if m&ui.Alt != 0 {
		n += 2
	}
	if m&ui.Ctrl != 0 {
		n += 4
	}
	return n
}

// input takes the terminal's keys, text and commands, and drags its
// selection. It runs on the main thread with the panel focused.
func (w *window) termInput(ev ui.InputEvent) bool {
	s := w.activeTerm()
	if s == nil {
		return false
	}
	switch ev.Kind {
	case ui.InputKeyDown:
		b, taken := s.keyDown(ev)
		if !taken {
			// The text of the key follows, as InputText or through the
			// text input's client; an alt key goes before its text.
			s.altPending = ev.Mods&ui.Alt != 0
			return false
		}
		s.altPending = false
		s.clearSelection()
		s.write(b)
		return true
	case ui.InputText:
		b := s.textBytes(ev)
		if len(b) == 0 {
			return false
		}
		s.altPending = false
		s.clearSelection()
		s.write(b)
		return true
	case ui.InputCommand:
		switch ev.Text {
		case "copy":
			if text := s.selectedText(); text != "" {
				mygo.Clipboard.WriteText(text)
				return true
			}
			return false
		case "paste", "pasteAndMatchStyle":
			if text := mygo.Clipboard.ReadText(); text != "" {
				s.paste(text)
			}
			return true
		}
		return false
	case ui.InputPointerDown:
		if ev.Button != 0 {
			return false
		}
		at := s.cellAt(ev.X, ev.Y)
		if ev.Clicks > 1 {
			s.selA, s.selB = s.wordAt(at)
		} else {
			s.selA, s.selB = at, at
		}
		s.selOn = true
		return true
	case ui.InputPointerMove:
		if !s.selOn || ev.Button < 0 {
			return s.selOn
		}
		s.selB = s.cellAt(ev.X, ev.Y)
		w.invalidate()
		return true
	case ui.InputPointerUp:
		if s.selOn {
			s.selOn = false
			return true
		}
	}
	return false
}

// cellAt maps a point in the grid to a view row and column.
func (s *termSession) cellAt(x, y float32) termCell {
	lh := s.w.lineHeight()
	cw := s.w.termCharW
	if cw <= 0 {
		cw = 1
	}
	cols, rows := s.term.Size()
	count := s.term.Count()
	first, _ := s.list.Visible()
	row := int((y + float32(first)*lh) / lh)
	row = min(max(row, 0), count-1)
	if s.term.OnAlt() {
		row = min(max(row, 0), rows-1)
	}
	col := min(max(int(x/cw), 0), cols-1)
	return termCell{row, col}
}

// wordAt is the word of letters, digits and punctuation a cell is in,
// for a double click's selection.
func (s *termSession) wordAt(at termCell) (termCell, termCell) {
	l := s.term.Line(at.row, nil)
	isWord := func(g vt.Glyph) bool {
		return g.Width != 0 && g.Content != "" && g.Content != " " && g.Content != "…"
	}
	if at.col >= len(l) || !isWord(l[at.col]) {
		return at, at
	}
	a, b := at.col, at.col
	for a > 0 && isWord(l[a-1]) {
		a--
	}
	for b < len(l)-1 && isWord(l[b+1]) {
		b++
	}
	return termCell{at.row, a}, termCell{at.row, b}
}

// selection is the selection's ends, the earlier first.
func (s *termSession) selection() (termCell, termCell, bool) {
	if !s.selOn && s.selA == s.selB {
		return termCell{}, termCell{}, false
	}
	a, b := s.selA, s.selB
	if a.row > b.row || (a.row == b.row && a.col > b.col) {
		a, b = b, a
	}
	return a, b, true
}

// clearSelection drops the selection, as a key going to the shell does.
func (s *termSession) clearSelection() {
	if s.selA == s.selB && !s.selOn {
		return
	}
	s.selOn = false
	s.selA, s.selB = termCell{}, termCell{}
	s.w.invalidate()
}

// selectedText is the selection's text, the rows separated by newlines
// and their trailing spaces gone.
func (s *termSession) selectedText() string {
	a, b, ok := s.selection()
	if !ok {
		return ""
	}
	var sb strings.Builder
	for row := a.row; row <= b.row && row < s.term.Count(); row++ {
		l := s.term.Line(row, nil)
		from, to := 0, len(l)
		if row == a.row {
			from = a.col
		}
		if row == b.row {
			to = min(b.col+1, len(l))
		}
		var text strings.Builder
		for x := from; x < to; x++ {
			if g := l[x]; g.Width != 0 {
				text.WriteString(g.Content)
			}
		}
		sb.WriteString(strings.TrimRight(text.String(), " "))
		if row < b.row {
			sb.WriteByte('\n')
		}
	}
	return sb.String()
}

// activeTerm is the session the panel shows, nil for none.
func (w *window) activeTerm() *termSession {
	if w.termActive >= 0 && w.termActive < len(w.terms) {
		return w.terms[w.termActive]
	}
	return nil
}

// ensureTerminals starts the first session when the panel shows without
// one, as the first frame of a window restored with the panel open does.
func (w *window) ensureTerminals() {
	if len(w.terms) == 0 {
		w.newTerminal()
	}
}

// newTerminal adds a tab of its own, and shows it. Its shell starts once
// the frame has measured the panel, so that its greeting fits the width.
func (w *window) newTerminal() {
	s := newTermSession(w)
	w.terms = append(w.terms, s)
	w.termActive = len(w.terms) - 1
	w.termShown, w.termFocus = true, true
	w.saveTermLayout()
	w.invalidate()
}

// closeTerminal ends the shell of session i, and shows the one after it,
// or hides the panel with the last one.
func (w *window) closeTerminal(i int) {
	if i < 0 || i >= len(w.terms) {
		return
	}
	w.terms[i].close()
	w.terms = append(w.terms[:i], w.terms[i+1:]...)
	if w.termActive >= len(w.terms) {
		w.termActive = len(w.terms) - 1
	}
	if len(w.terms) == 0 {
		w.termShown = false
	}
	w.saveTermLayout()
	w.invalidate()
}

// selectTerminal shows the screen of session i, and gives it the keys.
func (w *window) selectTerminal(i int) {
	if i < 0 || i >= len(w.terms) || i == w.termActive {
		return
	}
	w.termActive, w.termFocus = i, true
	w.invalidate()
}

// saveTermLayout keeps whether the panel shows and how much room it
// takes, for the windows opened next. It writes from a goroutine of its
// own, on values taken here: the store's file is not the frame's work.
func (w *window) saveTermLayout() {
	split, width, shown := w.termSplit, w.termWidth, w.termShown
	go state.setTermLayout(split, width, shown)
}

// toggleTerminal shows the terminal panel, or hides it with the shells
// running on, and starts a session the first time.
func (w *window) toggleTerminal() {
	w.termShown = !w.termShown
	if w.termShown {
		w.ensureTerminals()
		w.termFocus = true
	}
	w.saveTermLayout()
	w.invalidate()
}

// setTerminalPos puts the panel under the review or beside it, as the
// menu chooses.
func (w *window) setTerminalPos(pos string) {
	if pos != "bottom" && pos != "right" {
		return
	}
	cfg.Update(func(s *Settings) { s.TerminalPosition = pos })
}

// restartTerminal runs the shell of the tab showing again, after it
// exited.
func (w *window) restartTerminal() {
	if s := w.activeTerm(); s != nil {
		s.restart()
	}
	w.termFocus = true
	w.invalidate()
}

// termTextClient connects the terminal to the system's text input, which
// is what brings the typed keys, Chinese among them, in. The terminal
// keeps no text of its own: keys go to the shell as they come, and the
// input method's composition, the marked text, shows at the cursor.
type termTextClient struct {
	w      *window
	s      *termSession
	marked string // what an input method is composing, empty for none
}

func (c *termTextClient) TextForRange(r ui.TextInputRange) (string, ui.TextInputRange) {
	return "", r
}

func (c *termTextClient) Selection() ui.TextInputSelection {
	return ui.TextInputSelection{}
}

// MarkedRange reports the composition the input method is holding.
func (c *termTextClient) MarkedRange() (ui.TextInputRange, bool) {
	if c.marked == "" {
		return ui.TextInputRange{}, false
	}
	n := ui.UTF16Len(c.marked)
	return ui.TextInputRange{Start: n, End: n}, true
}

// takeAlt reports whether the key whose text is coming held alt, and
// forgets it.
func (s *termSession) takeAlt() (alt bool) {
	alt = s.altPending
	s.altPending = false
	return
}

// ReplaceText is what typed keys come through, from the system's text
// input: they go to the shell as if typed at once.
func (c *termTextClient) ReplaceText(r *ui.TextInputRange, text string) {
	s := c.s
	if s == nil || text == "" {
		return
	}
	if c.marked != "" {
		c.marked = ""
		c.w.invalidate()
	}
	if s.takeAlt() {
		text = "\x1b" + text
	}
	s.clearSelection()
	s.write([]byte(text))
}

// SetMarkedText keeps the composition for the view to draw at the cursor.
func (c *termTextClient) SetMarkedText(r *ui.TextInputRange, text string, sel ui.TextInputRange) {
	if c.marked == text {
		return
	}
	c.marked = text
	c.w.invalidate()
}

func (c *termTextClient) UnmarkText() {
	if c.marked != "" {
		c.marked = ""
		c.w.invalidate()
	}
}

// BoundsForRange puts the input methods' caret at the terminal's cursor,
// for when an input method composes anyway.
func (c *termTextClient) BoundsForRange(r ui.TextInputRange) (ui.Rect, ui.TextInputRange, bool) {
	s := c.s
	lh := c.w.lineHeight()
	cw := c.w.termCharW
	if s == nil || cw <= 0 {
		return ui.Rect{}, r, false
	}
	first, _ := s.list.Visible()
	cur := s.term.Cursor()
	return ui.Rect{
		X: float32(cur.X) * cw,
		Y: float32(cur.ViewY-first) * lh,
		W: cw, H: lh,
	}, r, true
}

func (c *termTextClient) IndexForPoint(p ui.Point) (int, bool) { return 0, false }

// termFont is the terminal's font: the code font with the system's
// Chinese face in the chain, so CJK takes it instead of whatever the
// shaper falls back to, which draws larger than the latin around it.
func (w *window) termFont() string {
	return w.codeFont() + ", PingFang SC"
}

// How wide the panel beside the review opens, and the least it takes.
const (
	termDefaultWidth = 460
	termMinWidth     = 320
)

// termBottom puts the terminal under the review, the line between them
// dragging the review's height.
func (w *window) termBottom(c *ui.Context, pal *palette, reviewBuilt *bool) {
	if w.termSplit == 0 {
		// The terminal takes the bottom 300 or so, once.
		_, height := c.Size()
		w.termSplit = max(height-titleBarHeight-300, 180)
	}
	// The split takes the room under the toolbar: grown, it gives the
	// panels beneath its line a height to fill, which the terminal's
	// screen takes its rows from.
	ui.SplitVertical(c, &w.termSplit, func() {
		*reviewBuilt = w.mainArea(c, pal)
	}, func() {
		w.termPanel(c, pal)
	}).Grow(1).MinHeight(0)
}

// termRight puts the terminal beside the review, the line between them
// dragging its width.
func (w *window) termRight(c *ui.Context, pal *palette, reviewBuilt *bool) {
	if w.termWidth == 0 {
		w.termWidth = termDefaultWidth
	}
	ui.Row(c).Grow(1).MinWidth(0).AlignItems(ui.Stretch).Children(func() {
		ui.Box(c).Grow(1).Shrink(1).MinWidth(0).Clip().Children(func() {
			*reviewBuilt = w.mainArea(c, pal)
		})
		w.termResizer(c, pal)
		ui.Box(c).Width(w.termWidth).Shrink(0).Clip().Children(func() {
			w.termPanel(c, pal)
		})
	})
}

// termResizer is the line between the review and the terminal beside it:
// dragging it widens the panel or narrows it, as the sidebar's does.
func (w *window) termResizer(c *ui.Context, pal *palette) {
	ui.Box(c).Width(1).Shrink(0).Background(pal.cardBorder).Children(func() {
		handle := ui.Box(c).Absolute().Top(0).Bottom(0).Left(-4).Width(9).Cursor(ui.CursorResizeEW)
		if dx, _, held := handle.Dragged(); held {
			w.termResizing = true
			// The panel sits on the right: dragging the line left (dx < 0)
			// widens it, the opposite of the sidebar on the left.
			w.termDrag -= dx
			total, _ := c.Size()
			w.termWidth = min(max(w.termDrag, termMinWidth), max(total-360, termMinWidth))
		} else {
			w.termResizing = false
			if w.termDrag != 0 && w.termDrag != w.termWidth {
				w.saveTermLayout()
			}
			w.termDrag = w.termWidth
		}
	})
}

// termPanel is the terminal under the review, or beside it: a strip of
// tabs, a tab a shell, and the screen of the one showing under it.
func (w *window) termPanel(c *ui.Context, pal *palette) {
	tp := &pal.term
	s := w.activeTerm()
	bar := ui.Row(c).Height(26).Shrink(0).FillWidth().Padding(0, 8).Gap(4).AlignItems(ui.Center).
		Background(tp.bg).BorderWidth(0, 0, 1, 0).BorderColor(pal.cardBorder)
	// The panel's own menu: closing, a new shell, and the position, which
	// the menu bar's View menu offers too.
	bar.ContextMenu(func(m *ui.Menu) {
		if m.Item("New Terminal").Chosen() {
			w.newTerminal()
		}
		if m.Item("Close Terminal").Chosen() {
			w.toggleTerminal()
		}
		if active := w.activeTerm(); active != nil {
			if m.Item("Close This Tab").Chosen() {
				w.closeTerminal(w.termActive)
			}
		}
		m.Separator()
		right := w.settings.TerminalPosition == "right"
		if m.Item("Terminal Position: Bottom").Checked(!right).Chosen() {
			w.setTerminalPos("bottom")
		}
		if m.Item("Terminal Position: Right").Checked(right).Chosen() {
			w.setTerminalPos("right")
		}
	})
	bar.Children(func() {
		w.termTabs(c, pal)
		ui.Spacer(c)
		if s != nil && s.exitedNow() {
			if ui.Button(c, "Restart").Clicked() {
				w.restartTerminal()
			}
		}
		if iconButton(c, iconPlus, "New Terminal (⌃⇧`)").Size(22, 22).Clicked() {
			w.newTerminal()
		}
		if iconButton(c, iconClose, "Hide Terminal (⌃`)").Size(22, 22).Clicked() {
			w.toggleTerminal()
		}
	})
	if s != nil {
		w.termGrid(c, pal, s)
	}
}

// termTabs shows a tab for each shell: a click on its name shows its
// screen, and the one showing carries the × that ends it. The two are
// buttons side by side, not one in the other, which would take its press.
func (w *window) termTabs(c *ui.Context, pal *palette) {
	t := c.Theme()
	// Closing ends the build of this strip: the tabs after it moved.
	closed := -1
	for i, s := range w.terms {
		active := i == w.termActive
		// The name and the × sit side by side: a Box lays them out in a
		// column, which put the × under the name, past the tab's height,
		// where the pointer never reached it.
		tab := ui.Row(c).Height(20).Shrink(0).Padding(0, 6).Gap(4).Radius(5)
		if active {
			tab.Background(pal.term.selection)
		} else if tab.Hovered() {
			tab.Background(pal.hover)
		}
		tab.Children(func() {
			name := ui.ButtonBase(c).Height(20).Padding(0).Radius(0).Label(w.tabLabel(i)).Tooltip(s.tabTitle())
			name.Children(func() {
				ui.Text(c, w.tabLabel(i)).FontSize(11).SingleLine().MaxWidth(150).TextColor(t.Text)
			})
			if name.Clicked() {
				w.selectTerminal(i)
			}
			if active && closeTab(c, t, w.tabLabel(i)).Clicked() {
				closed = i
			}
		})
	}
	if closed >= 0 {
		w.closeTerminal(closed)
	}
}

// tabLabel is what tab i says: its title, and the number of the tab when
// two shells call themselves the same.
func (w *window) tabLabel(i int) string {
	label := w.terms[i].tabTitle()
	for j, s := range w.terms {
		if j != i && s.tabTitle() == label {
			return label + " " + strconv.Itoa(i+1)
		}
	}
	return label
}

// closeTab is the small × of the tab showing.
func closeTab(c *ui.Context, t *ui.Theme, name string) ui.Element {
	b := ui.ButtonBase(c).Size(13, 13).Center().Radius(3).Cursor(ui.CursorPointer).
		Label("Close " + name).Tooltip("Close Terminal")
	if b.Hovered() {
		b.Background(ui.RGBA(127, 127, 127, 0.25))
	}
	b.Children(func() { ui.Icon(c, iconClose).FontSize(10) })
	return b
}

// termGrid is the emulator's screen: a list of its rows, only the ones
// in view built, following its end as output arrives.
func (w *window) termGrid(c *ui.Context, pal *palette, s *termSession) {
	lh := w.lineHeight()
	cw := w.charWidth(c)
	w.termCharW = cw
	// The pane's size comes from the list's own box, as the last frame laid
	// it out: the emulator and the pty take the columns and rows that show,
	// so programs wrap and full-screen TUIs draw where the panel ends. The
	// rows the list built are not it: it builds a few beyond its edges, and
	// counting those would leave the last ones clipped.
	if lh > 0 && cw > 0 {
		if b := s.list.Bounds(c); b.W > 0 && b.H > 0 {
			cols, rows := int(b.W/cw), int(b.H/lh)
			// The tabs it does not show take the same pane: their shells
			// follow it, rather than when the user comes back to one. A shell
			// not started yet starts here, at the size it will print in.
			for _, other := range w.terms {
				other.resize(cols, rows)
				other.startPending()
			}
		}
	}
	ui.List(c, &s.list, s.term.Count(), func(i int) { w.termRow(c, pal, s, i, lh, cw) }).
		Grow(1).MinHeight(0).Background(pal.term.bg).Font(w.termFont()).FontSize(w.codeSize()).
		HandleInput(w.termInput).
		HandleTextInput(s.text).
		Focusable().Label("Terminal")
	if s.focus {
		s.focus = false
		s.list.Focus(c)
	}
	// The cursor blinks while the terminal has the focus.
	if cur := s.term.Cursor(); cur.Visible && s.list.FocusWithin(c) {
		c.After(600 * time.Millisecond)
	}
}

// termRun is a stretch of a row of one color: for the background fill
// and for the span of its text. startCol is where it begins, so the
// drawing can place each character on its grid column.
type termRun struct {
	text      string
	startCol  int
	cols      int
	fg        ui.Color
	fill      ui.Color
	fills     bool
	cursor    bool
	bold      bool
	italic    bool
	underline bool
	strike    bool
	// wide marks a run of one wide character, which stands on its own so
	// the letter spacing that pulls its advance to the grid's width does
	// not touch the narrow runs around it.
	wide bool
	ls   float32
}

// termRow draws row i of the screen: the runs' backgrounds paint under
// their text, and each run's text is an element of its own, placed on
// the column it starts at — so a glyph's own advance, which the fonts
// make narrower than the cells a wide character takes, never moves what
// comes after it, and the box drawing of a TUI stays where the program
// put it.
func (w *window) termRow(c *ui.Context, pal *palette, s *termSession, i int, lh, cw float32) {
	// Away from the terminal, the cursor shows hollow, as the terminals
	// draw the caret of an unfocused pane.
	focused := s.list.FocusWithin(c)
	runs := w.termRuns(&pal.term, s, i, focused)
	if len(runs) == 0 {
		ui.Box(c).Height(lh).FillWidth().Shrink(0)
		return
	}
	ui.Box(c).Height(lh).FillWidth().Shrink(0).Font(w.termFont()).FontSize(w.codeSize()).
		Draw(func(p *ui.Painter, r ui.Rect) {
			for _, run := range runs {
				x := r.X + float32(run.startCol)*cw
				wid := float32(run.cols) * cw
				if run.fills {
					if run.cursor && !focused {
						// The hollow cursor: the cell's four edges.
						e := float32(1)
						p.Fill(ui.Rect{X: x, Y: r.Y, W: wid, H: e}, run.fill, 0)
						p.Fill(ui.Rect{X: x, Y: r.Y + lh - e, W: wid, H: e}, run.fill, 0)
						p.Fill(ui.Rect{X: x, Y: r.Y, W: e, H: lh}, run.fill, 0)
						p.Fill(ui.Rect{X: x + wid - e, Y: r.Y, W: e, H: lh}, run.fill, 0)
					} else {
						p.Fill(ui.Rect{X: x, Y: r.Y, W: wid, H: lh}, run.fill, 0)
					}
				}
			}
			// The input method's composition draws over the screen at the
			// cursor, underlined, as the terminals show it.
			if s.text != nil && s.text.marked != "" {
				cur := s.term.Cursor()
				if i == cur.ViewY {
					p.RichText(r.X+float32(cur.X)*cw, r.Y, r.W-float32(cur.X)*cw,
						ui.Span{Text: s.text.marked, Color: pal.term.fg, Font: w.termFont(), Size: w.codeSize(), Underline: true})
				}
			}
		}).
		Children(func() {
			for _, run := range runs {
				if run.text == "" {
					continue
				}
				sp := ui.Span{Text: run.text, Color: run.fg, Font: w.termFont(), Size: w.codeSize()}
				if run.bold {
					sp.Weight = 700
				}
				if run.italic {
					sp.Italic = true
				}
				if run.underline {
					sp.Underline = true
				}
				if run.strike {
					sp.Strikethrough = true
				}
				ui.RichText(c, sp).SingleLine().NoWrap().
					Absolute().Left(float32(run.startCol) * cw).Top(0)
			}
		})
}

// termRuns flattens row i of the emulator into runs of one color,
// merging the neighbors that agree. A focused terminal's cursor cell
// paints over its character; a hollow one lets the character show.
func (w *window) termRuns(tp *termPalette, s *termSession, i int, focused bool) []termRun {
	term := s.term
	s.buf = term.Line(i, s.buf)
	cur := term.Cursor()
	// The cursor shows in its half of the blink when the panel is idle;
	// output resets nothing, as a terminal at work hardly blinks.
	cursorOn := time.Now().UnixMilli()/600%2 == 0
	// While an input method composes, its marked text takes the cursor's
	// place, and the block cursor stands down.
	composing := s.text != nil && s.text.marked != ""
	a, b, hasSel := s.selection()

	var runs []termRun
	run := termRun{}
	var text strings.Builder
	end := func() {
		if text.Len() > 0 {
			run.text = text.String()
			runs = append(runs, run)
			text.Reset()
		}
		run = termRun{}
	}
	for x, g := range s.buf {
		if g.Width == 0 {
			continue // the shadow of a wide character
		}
		r := termRun{}
		r.fg = termColor(tp, g.FG, tp.fg)
		bg := tp.bg
		fillsBg := g.BG != nil
		if fillsBg {
			bg = termColor(tp, g.BG, tp.bg)
		}
		if g.Attr&vt.AttrReverse != 0 {
			r.fg, bg = bg, r.fg
			fillsBg = true
		}
		if g.Attr&vt.AttrInvisible != 0 {
			r.fg = bg
		}
		if g.Attr&vt.AttrDim != 0 {
			r.fg = r.fg.Mix(tp.bg, 0.4)
		}
		r.bold = g.Attr&vt.AttrBold != 0
		r.italic = g.Attr&vt.AttrItalic != 0
		r.underline = g.Underline
		r.strike = g.Attr&vt.AttrStrike != 0
		if hasSel && withinSelection(a, b, termCell{i, x}) {
			r.fill, r.fills = tp.selection, true
		} else if fillsBg {
			r.fill, r.fills = bg, true
		}
		if !composing && cur.Visible && i == cur.ViewY && x == cur.X && (cursorOn || !focused) {
			r.cursor = true
			r.fill, r.fills = tp.cursor, true
			if focused {
				r.fg = tp.cursorText
			}
		}
		same := g.Width == 1 && !run.wide &&
			r.fg == run.fg && r.fill == run.fill && r.fills == run.fills &&
			r.bold == run.bold && r.italic == run.italic &&
			r.underline == run.underline && r.strike == run.strike &&
			r.cursor == run.cursor
		if !same {
			end()
			run = r
			run.startCol = x
		}
		text.WriteString(g.Content)
		run.cols += g.Width
	}
	end()
	return runs
}

// withinSelection reports whether a cell is in the selection.
func withinSelection(a, b, at termCell) bool {
	if at.row < a.row || at.row > b.row {
		return false
	}
	if at.row == a.row && at.col < a.col {
		return false
	}
	if at.row == b.row && at.col > b.col {
		return false
	}
	return true
}

// termColor resolves the emulator's color to the theme's: nil is the
// default given, an ANSI index its 16 or its computed 240, and the rest
// a direct color.
func termColor(tp *termPalette, c color.Color, def ui.Color) ui.Color {
	switch v := c.(type) {
	case nil:
		return def
	case ansi.BasicColor:
		return tp.ansi[int(v)&15]
	case ansi.IndexedColor:
		i := int(v)
		if i < 16 {
			return tp.ansi[i]
		}
		r, g, b := xterm256(i)
		return ui.RGB(r, g, b)
	default:
		r, g, b, a := c.RGBA()
		return ui.RGB(uint8(r>>8), uint8(g>>8), uint8(b>>8)).Alpha(float32(a) / 0xffff)
	}
}

// xterm256 is the color of the palette's upper 240: a 6×6×6 cube, then
// a ramp of grays.
func xterm256(i int) (r, g, b uint8) {
	switch {
	case i < 232:
		i -= 16
		levels := [6]uint8{0, 95, 135, 175, 215, 255}
		return levels[(i/36)%6], levels[(i/6)%6], levels[i%6]
	default:
		v := uint8(8 + (i-232)*10)
		return v, v, v
	}
}
