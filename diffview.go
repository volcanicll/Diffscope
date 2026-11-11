package main

import (
	"fmt"
	"math"
	"strings"
	"time"

	"github.com/egoist/mygo/ui"
	"github.com/volcanicll/diffscope/internal/diff"
	"github.com/volcanicll/diffscope/internal/github"
	"github.com/volcanicll/diffscope/internal/highlight"
)

// codeFont is the family of the code.
func (w *window) codeFont() string {
	if w.settings.CodeFontFamily != "" {
		return w.settings.CodeFontFamily + ", monospace"
	}
	return "SF Mono, Menlo, monospace"
}

func (w *window) codeSize() float32 { return float32(w.settings.CodeFontSize) }

// lineHeight is the height of a line of code: 20 at 13.
func (w *window) lineHeight() float32 {
	return float32(math.Round(float64(w.codeSize()) * 20 / 13))
}

// charWidth is the width of a column of code.
func (w *window) charWidth(c *ui.Context) float32 {
	key := fmt.Sprint(w.codeFont(), w.codeSize())
	if w.charKey != key {
		width, _ := c.MeasureText(0, ui.Span{Text: strings.Repeat("0", 20), Font: w.codeFont(), Size: w.codeSize()})
		w.charW, w.charKey = width/20, key
	}
	return w.charW
}

// fileMetrics are the widths a file's rows share: of its line numbers,
// and how far its lines go.
type fileMetrics struct {
	digits  int
	maxCols int
}

func (f *fileState) metrics() fileMetrics {
	if f.metricsDone {
		return f.metric
	}
	maxNum, maxCols := 0, 0
	for _, h := range f.Hunks {
		maxNum = max(maxNum, h.OldStart+h.OldLines, h.NewStart+h.NewLines)
		for _, l := range h.Lines {
			maxCols = max(maxCols, columns(l.Text))
		}
	}
	maxNum = max(maxNum, len(f.oldLines), len(f.newLines))
	if f.canExpand() {
		for _, l := range f.newLines {
			maxCols = max(maxCols, columns(l))
		}
		for _, l := range f.oldLines {
			maxCols = max(maxCols, columns(l))
		}
	}
	f.metric = fileMetrics{digits: max(len(fmt.Sprint(maxNum)), 2), maxCols: maxCols}
	f.metricsDone = f.loaded
	return f.metric
}

// diffList shows the files as one continuous document, building the rows
// in view only.
func (w *window) diffList(c *ui.Context) {
	t := c.Theme()
	pal := paletteFor(t)
	if w.rowsDirty {
		w.updateMatches()
		w.buildRows()
	}
	w.measureGutter()
	ui.List(c, &w.list, len(w.rows), func(i int) { w.diffRow(c, pal, i, w.list.FocusWithin(c)) }).
		Grow(1).Padding(0, 0, 24).Background(pal.codeBg).Label("Changes")

	// The file at the top follows the scrolling, and the tree with it.
	first, _ := w.list.Visible()
	for first >= 0 && first < len(w.rows) && w.rows[first].banner() {
		first++
	}
	if first >= 0 && first < len(w.rows) {
		file := int(w.rows[first].file)
		if file != w.current && time.Since(w.revealedAt) > 1200*time.Millisecond {
			w.current = file
			w.selectTreeFile(file)
		}
	}
}

// nextFile moves the reading position to the next or previous visible
// file, the way an editor walks its buffers.
func (w *window) nextFile(dir int) {
	var visible []int
	for i := range w.files {
		if w.fileVisible(i) {
			visible = append(visible, i)
		}
	}
	if len(visible) == 0 {
		return
	}
	at := 0
	for i, fi := range visible {
		if fi == w.current {
			at = i
		}
	}
	at = min(max(at+dir, 0), len(visible)-1)
	w.revealFile(visible[at])
}

// revealFile scrolls the surface to a file's card.
func (w *window) revealFile(i int) {
	w.browse = ""
	if w.rowsDirty {
		w.buildRows()
	}
	for r := range w.rows {
		if w.rows[r].kind == rowHeader && int(w.rows[r].file) == i {
			w.list.ScrollTo(r, ui.Start)
			break
		}
	}
	w.current = i
	w.revealedAt = time.Now()
}

func (w *window) diffRow(c *ui.Context, pal *palette, i int, focused bool) {
	r := &w.rows[i]
	switch r.kind {
	case rowCommit:
		w.commitMessage(c, pal)
		return
	case rowPull:
		w.pullHeader(c, pal)
		return
	case rowSummary:
		w.summaryCard(c, pal)
		return
	case rowGroup:
		w.groupRow(c, pal, int(r.gap))
		return
	}
	f := w.files[r.file]
	switch r.kind {
	case rowThread:
		w.threadRow(c, pal, f, r.thread)
	case rowAINote:
		w.noteRow(c, pal, r.note)
	case rowHeader:
		w.fileHeader(c, pal, int(r.file), f, focused)
	case rowNote:
		w.card(c, pal).Padding(10, 16).Background(pal.gapBg).Children(func() {
			ui.Text(c, w.fileNote(f)).FontSize(12).TextColor(pal.gapText)
		})
	case rowGap:
		w.gapRow(c, pal, f, r)
	case rowHunk:
		w.hunkHeaderRow(c, pal, f, int(r.gap))
	case rowImage:
		w.imageRow(c, pal, f)
	case rowLine:
		w.lineRow(c, pal, f, r)
	case rowComment:
		w.commentRow(c, pal, f, r.comment)
	case rowEnd:
		ui.Box(c).Height(0)
	}
}

// card is a row inside the document. The document is one surface: the
// rows have no sides of their own.
func (w *window) card(c *ui.Context, pal *palette) ui.Element {
	return ui.Row(c).Background(pal.codeBg)
}

// forceOpen reports whether a collapsed file shows its lines anyway, as
// the files holding matches of the find bar do.
func (w *window) forceOpen(i int) bool {
	return w.searching() && w.fileMatches[i]
}

func (w *window) fileHeader(c *ui.Context, pal *palette, idx int, f *fileState, focused bool) {
	t := c.Theme()
	collapsed := f.collapsed && !w.forceOpen(idx)
	viewed := w.isViewed(f)
	// The band above the bar spaces the files, and hides the lines scrolling
	// under the bar while it is pinned.
	band := ui.Column(c).Padding(8, 0, 0, 0).Background(pal.appBg)
	var h ui.Element
	band.Children(func() {
		h = ui.Row(c).Height(38).Padding(0, 8, 0, 8).Gap(6).Background(pal.headerBg).
			BorderWidth(0, 0, 1, 0).BorderColor(pal.cardBorder)
	})
	h.ContextMenu(func(m *ui.Menu) { w.fileHeaderMenu(c, m, f, viewed) })
	h.Children(func() {
		if idx == w.current {
			// The file at the top of the document keeps a quiet edge.
			ui.Box(c).Absolute().Left(0).Top(0).Bottom(0).Width(2).Background(t.Accent)
		}
		toggle := ui.ButtonBase(c).Gap(6).Grow(1).Shrink(1).MinWidth(0).Height(38).Justify(ui.Start).Label(f.Path).Tooltip(f.Path)
		if toggle.Clicked() {
			f.collapsed = !collapsed
			if !f.collapsed && w.searching() {
				w.fileMatches[idx] = true
			}
			w.rowsDirty = true
		}
		toggle.FocusRing(false).Children(func() {
			chev := ui.Box(c).Size(20, 20).Radius(4).Center().Shrink(0)
			if toggle.Hovered() {
				chev.Background(pal.hover)
			}
			chev.Children(func() {
				target := float32(0)
				if collapsed {
					target = -90
				}
				ic := ui.Icon(c, iconChevronDown).FontSize(14).TextColor(t.TextMuted)
				ic.Rotate(ic.Animate("rot", target, 150*time.Millisecond))
			})
			pathColor := t.Text
			if collapsed || viewed {
				pathColor = t.TextMuted
			}
			fileIcon(c, f, 15)
			ui.Column(c).Shrink(1).MinWidth(0).Children(func() {
				ui.RichText(c,
					ui.Span{Text: f.Dir(), Color: t.TextMuted, Size: 12},
					ui.Span{Text: f.Name(), Color: pathColor, Weight: 600, Size: 12.5},
				).Font(w.codeFont()).SingleLine()
				if f.OldPath != f.Path {
					ui.Text(c, "from "+f.OldPath).Font(w.codeFont()).FontSize(10.5).TextColor(t.TextMuted).SingleLine()
				}
			})
			ui.Text(c, statusLetter(f)).Font(w.codeFont()).FontSize(10).FontWeight(700).
				TextColor(statusColor(f.Status, pal, t)).Width(10).TextAlign(ui.Center).Shrink(0).Tooltip(f.Status.Label())
		})
		// The copy button shows while the pointer is over the bar; it is
		// always built, so that the elements after it keep their state.
		cp := iconButton(c, iconCopy, "Copy path")
		if !h.Hovered() && !cp.FocusVisible() {
			cp.Opacity(0)
		}
		if cp.Clicked() {
			c.WriteClipboard(f.Path)
			c.Toast("Copied " + f.Name())
		}
		open := iconButton(c, iconOpen, "Open file in editor").Disabled(f.Status == diff.Deleted || f.Directory)
		if !h.Hovered() && !open.FocusVisible() {
			open.Opacity(0)
		}
		if f.Status == diff.Deleted {
			open.Tooltip("Deleted files cannot be opened")
		}
		if open.Clicked() {
			w.openInEditor(f.Path, firstLine(f))
		}
		if countable(f) && (f.Additions > 0 || f.Deletions > 0) {
			ui.Row(c).Gap(4).Shrink(0).
				Tooltip(lines(f.Additions, "added") + ", " + lines(f.Deletions, "removed")).Children(func() {
				ui.Text(c, "+"+thousands(f.Additions)).Font(w.codeFont()).FontSize(11).FontWeight(600).TextColor(pal.addText)
				ui.Text(c, "-"+thousands(f.Deletions)).Font(w.codeFont()).FontSize(11).FontWeight(600).TextColor(pal.delText)
			})
		}
		if f.Generated {
			ui.Text(c, "Generated").FontSize(10.5).FontWeight(600).Padding(2, 6).Radius(8).
				Background(pal.ref.Alpha(0.13)).TextColor(pal.ref).Shrink(0)
		}
		// What the index holds, when the review shows all the changes:
		// a file staged alone, or one with more changes in the work tree.
		if w.source.kind == sourceWorkingTree && w.stageMode == stageAll && f.Staged {
			label := "Staged"
			if f.Unstaged {
				label = "Staged + Unstaged"
			}
			ui.Text(c, label).FontSize(10.5).FontWeight(600).Padding(2, 6).Radius(8).
				Background(pal.viewed.Alpha(0.13)).TextColor(pal.viewed).Shrink(0).
				Tooltip("The index holds changes of this file" + map[bool]string{true: ", and so does the work tree", false: ""}[f.Unstaged])
		}
		if n := w.annotationCount(f); n > 0 {
			ui.Row(c).Gap(3).TextColor(t.TextMuted).Shrink(0).
				Tooltip(plural(n, "annotation")).Children(func() {
				ui.Icon(c, iconComment).FontSize(12)
				ui.Text(c, itoa(n)).Font(w.codeFont()).FontSize(10.5)
			})
		}
		w.viewedButton(c, pal, f, viewed)
		more := iconButton(c, iconMore, "File actions")
		if !h.Hovered() && !more.FocusVisible() {
			more.Opacity(0)
		}
		more.Menu(func(m *ui.Menu) { w.fileHeaderMenu(c, m, f, viewed) })
	})
}

// fileHeaderMenu adds the actions of a file's bar to a menu.
func (w *window) fileHeaderMenu(c *ui.Context, m *ui.Menu, f *fileState, viewed bool) {
	if m.Item("Open in Editor").Disabled(f.Status == diff.Deleted).Chosen() {
		w.openInEditor(f.Path, firstLine(f))
	}
	w.fileMenu(c, m, f.Path, w.canEdit(f))
	if m.Item("Viewed").Checked(viewed).Chosen() {
		w.setViewed(f, !viewed)
	}
	m.Separator()
	if m.Item("Collapse All").Chosen() {
		w.setAllCollapsed(true)
	}
	if m.Item("Expand All").Chosen() {
		w.setAllCollapsed(false)
	}
}

// annotationCount is how many comments, threads and AI notes a file's bar
// reports.
func (w *window) annotationCount(f *fileState) int {
	n := len(w.notes[f.Path])
	for _, cm := range w.comments {
		if cm.path == f.Path {
			n++
		}
	}
	for _, t := range w.threads(f.Path) {
		if !t.Outdated {
			n++
		}
	}
	return n
}

// viewedButton marks a file viewed, which collapses it.
func (w *window) viewedButton(c *ui.Context, pal *palette, f *fileState, viewed bool) {
	t := c.Theme()
	b := ui.ButtonBase(c).Gap(6).Height(26).Padding(0, 6).Radius(13).Shrink(0).Label("Viewed")
	b.OnClick(func() { w.setViewed(f, !viewed) })
	if b.Hovered() {
		b.Background(pal.hover)
	}
	if viewed {
		b.TextColor(pal.viewed)
	}
	b.Children(func() {
		box := ui.Box(c).Size(15, 15).Radius(4).Center()
		if viewed {
			box.Background(pal.viewed).Children(func() {
				ui.Icon(c, iconCheck).FontSize(10).TextColor(pal.codeBg)
			})
		} else {
			box.Border(1.5, ui.RGBA(127, 127, 127, 0.45))
		}
		ui.Text(c, "Viewed").FontSize(11).FontWeight(600).TextColor(map[bool]ui.Color{true: pal.viewed, false: t.Text}[viewed])
	})
}

// setAllCollapsed collapses or expands every file.
func (w *window) setAllCollapsed(collapsed bool) {
	for _, f := range w.files {
		f.collapsed = collapsed
	}
	w.rowsDirty = true
}

// firstLine is the first changed line of a file, to open it there.
func firstLine(f *fileState) int {
	for _, h := range f.Hunks {
		for _, l := range h.Lines {
			if l.Kind == diff.Add {
				return l.New
			}
		}
		if h.NewStart > 0 {
			return h.NewStart
		}
	}
	return 0
}

// measureGutter finds the widest line number the document shows, so that
// every file's numbers share one column.
func (w *window) measureGutter() {
	digits := 3
	for i := range w.files {
		if !w.fileVisible(i) {
			continue
		}
		digits = max(digits, w.files[i].metrics().digits)
	}
	w.gutterDigits = min(digits, 5)
}

// gutterWidth is the width of a column of line numbers.
func (w *window) gutterWidth(c *ui.Context, f *fileState) float32 {
	digits := max(w.gutterDigits, f.metrics().digits)
	return float32(digits)*w.charWidth(c) + 18
}

// hunkHeaderRow is a hunk's coordinates, when the gap before it is short
// enough to show in full and so has no row to carry them.
func (w *window) hunkHeaderRow(c *ui.Context, pal *palette, f *fileState, hi int) {
	if hi < 0 || hi >= len(f.Hunks) {
		return
	}
	h := f.Hunks[hi]
	header := fmt.Sprintf("@@ -%d,%d +%d,%d @@", h.OldStart, h.OldLines, h.NewStart, h.NewLines)
	if h.Section != "" {
		header += " " + h.Section
	}
	ui.Row(c).Height(24).Grow(1).Padding(0, 10).Background(pal.appBg).
		BorderWidth(0, 0, 1, 0).BorderColor(pal.cardBorder).Justify(ui.Start).Children(func() {
		ui.Text(c, header).Font(w.codeFont()).FontSize(11).TextColor(pal.gapText).
			SingleLine().Grow(1).Shrink(1).MinWidth(0)
	})
}

func (w *window) gapRow(c *ui.Context, pal *palette, f *fileState, r *row) {
	t := c.Theme()
	gutter := w.gutterWidth(c, f)
	if !w.splitFile(f) && !f.oneSided() {
		gutter = 2*gutter - 4
	}
	first, last := r.gap == 0, int(r.gap) == len(f.Hunks)
	n := int(r.count)
	expand := func(top, bottom int) {
		if f.expanded == nil {
			f.expanded = map[int]gapShown{}
		}
		s := f.expanded[int(r.gap)]
		s.top += top
		s.bottom += bottom
		f.expanded[int(r.gap)] = s
		f.metricsDone = false
		w.rowsDirty = true
	}
	all := func() { expand(n, 0) }
	can := f.canExpand()
	ui.Row(c).Height(28).Background(pal.appBg).Children(func() {
		ui.Row(c).Width(gutter + 4).Height(28).Shrink(0).Children(func() {
			button := func(svg *ui.SVG, tip string, action func()) {
				b := ui.ButtonBase(c).Grow(1).Height(28).Center().Label(tip).Tooltip(tip).TextColor(pal.gapText).Disabled(!can)
				b.BorderWidth(0, 1, 0, 0).BorderColor(pal.codeBg)
				if b.Hovered() {
					b.TextColor(t.Text).Background(pal.hover)
				}
				b.Children(func() { ui.Icon(c, svg).FontSize(13) })
				if b.Clicked() {
					action()
				}
			}
			switch {
			case n <= expandStep && first:
				button(iconArrowUp, "Show the lines above", all)
			case n <= expandStep && last:
				button(iconArrowDown, "Show the lines below", all)
			case n <= expandStep:
				button(iconExpand, "Show the lines", all)
			default:
				if !first {
					button(iconArrowDown, fmt.Sprintf("Show %d more lines", expandStep), func() { expand(expandStep, 0) })
				}
				if !last {
					button(iconArrowUp, fmt.Sprintf("Show %d more lines", expandStep), func() { expand(0, expandStep) })
				}
			}
		})
		label := ui.ButtonBase(c).Padding(0, 10).Height(28).FocusRing(false).Disabled(!can).Children(func() {
			text := fmt.Sprintf("%d unmodified lines", n)
			if n == 1 {
				text = "1 unmodified line"
			}
			ui.Text(c, text).FontSize(11.5).TextColor(pal.gapText).SingleLine()
		})
		if label.Hovered() && can {
			label.Underline()
		}
		if label.Clicked() {
			switch {
			case n <= expandStep:
				all()
			case first:
				expand(0, expandStep)
			case last:
				expand(expandStep, 0)
			default:
				expand(expandStep, expandStep)
			}
		}
		if n > expandStep {
			b := ui.ButtonBase(c).Padding(2, 7).Radius(5).Disabled(!can).Children(func() {
				ui.Text(c, "Expand all").FontSize(11).FontWeight(600).TextColor(pal.gapText)
			})
			if b.Hovered() {
				b.Background(pal.hover)
			}
			if b.Clicked() {
				all()
			}
		}
		ui.Spacer(c)
		// The gap is also the hunk's boundary: its coordinates and the
		// function it belongs to sit at the right, in one row.
		if int(r.gap) < len(f.Hunks) {
			h := f.Hunks[r.gap]
			header := fmt.Sprintf("@@ -%d,%d +%d,%d @@", h.OldStart, h.OldLines, h.NewStart, h.NewLines)
			if h.Section != "" {
				header += " " + h.Section
			}
			ui.Text(c, header).Font(w.codeFont()).FontSize(11).TextColor(pal.gapText).
				SingleLine().Shrink(1).MinWidth(0).Padding(0, 10).TextAlign(ui.End)
		}
	})
}

// lineSide is what one side of a line row shows.
type lineSide struct {
	present bool
	kind    diff.LineKind
	num     int
	text    string
	segs    []highlight.Seg
	words   []diff.Range
	hunk    int32
	index   int32 // the line's index in its hunk, -1 for expanded context
}

// sideOf returns a line of a row for the old or the new side.
func (w *window) sideOf(f *fileState, r *row, s side) lineSide {
	if r.hunk < 0 {
		// Expanded context.
		ls := lineSide{present: true, kind: diff.Context, hunk: -1, index: -1}
		if s == sideOld {
			ls.num = int(r.old)
			ls.text = f.contextText(r.old, r.new)
			if int(r.old) <= len(f.oldHL) {
				ls.segs = f.oldHL[r.old-1]
			} else if int(r.new) <= len(f.newHL) {
				ls.segs = f.newHL[r.new-1]
			}
		} else {
			ls.num = int(r.new)
			ls.text = f.contextText(r.old, r.new)
			if int(r.new) <= len(f.newHL) {
				ls.segs = f.newHL[r.new-1]
			}
		}
		return ls
	}
	idx := r.a
	if s == sideNew && w.splitFile(f) {
		idx = r.b
	}
	l := f.lineAt(r.hunk, idx)
	if l == nil {
		return lineSide{}
	}
	ls := lineSide{present: true, kind: l.Kind, text: l.Text, hunk: r.hunk, index: idx}
	if l.Kind == diff.Del {
		ls.num = l.Old
		if l.Old <= len(f.oldHL) {
			ls.segs = f.oldHL[l.Old-1]
		}
	} else {
		ls.num = l.New
		if s == sideOld && l.Kind == diff.Context {
			ls.num = l.Old
		}
		if l.New > 0 && l.New <= len(f.newHL) {
			ls.segs = f.newHL[l.New-1]
		}
	}
	ls.words = f.words[[2]int{int(r.hunk), int(idx)}]
	return ls
}

func (w *window) lineRow(c *ui.Context, pal *palette, f *fileState, r *row) {
	gutter := w.gutterWidth(c, f)
	lh := w.lineHeight()
	e := w.card(c, pal).AlignItems(ui.Stretch).MinHeight(lh)
	hs := w.hscroll[f.Path]
	if !w.settings.WordWrap {
		handle := ui.Local(e, "input-handle", func() ui.Handle { return ui.Handle{} })
		e.Bind(handle)
		charWidth := w.charWidth(c)
		e.HandleInput(func(ev ui.InputEvent) bool {
			if ev.Kind != ui.InputScroll || math.Abs(float64(ev.DX)) <= math.Abs(float64(ev.DY)) {
				return false
			}
			b := handle.Bounds(c)
			code := b.W - 2*gutter - 40
			if w.splitFile(f) {
				code = b.W/2 - gutter - 30
			}
			limit := max(float32(f.metrics().maxCols)*charWidth-code, 0)
			w.hscroll[f.Path] = min(max(w.hscroll[f.Path]+ev.DX, 0), limit)
			w.invalidate()
			return true
		})
	}
	if !w.splitFile(f) {
		s := w.sideOf(f, r, sideNew)
		sd := sideNew
		if s.kind == diff.Del {
			sd = sideOld
		}
		e.Children(func() {
			w.lineCell(c, pal, f, r, s, sd, gutter, hs, !f.oneSided())
			w.annotationGutter(c, pal, f, r)
		})
		return
	}
	e.Children(func() {
		left := w.sideOf(f, r, sideOld)
		right := w.sideOf(f, r, sideNew)
		ui.Row(c).Grow(1).Basis(0).MinWidth(0).AlignItems(ui.Stretch).Children(func() {
			w.lineCell(c, pal, f, r, left, sideOld, gutter, hs, false)
		})
		ui.Box(c).Width(1).Shrink(0).Background(pal.cardBorder)
		ui.Row(c).Grow(1).Basis(0).MinWidth(0).AlignItems(ui.Stretch).Children(func() {
			w.lineCell(c, pal, f, r, right, sideNew, gutter, hs, false)
		})
		w.annotationGutter(c, pal, f, r)
	})
}

// rowAnnotationInfo counts the comments, threads and notes on a row and
// says what its marker should be.
func (w *window) rowAnnotationInfo(f *fileState, r *row) (n int, critical, ai bool) {
	check := func(sd side, num int) {
		if num == 0 {
			return
		}
		for _, cm := range w.comments {
			if cm.path == f.Path && cm.side == sd && cm.line == num {
				n++
			}
		}
		for _, nt := range w.notes[f.Path] {
			if !nt.file && nt.side == sd && nt.line == num {
				n++
				ai = true
				if nt.critical {
					critical = true
				}
			}
		}
		for _, t := range w.threads(f.Path) {
			if t.Outdated || t.Line != num {
				continue
			}
			tsd := sideNew
			if t.Side == github.Left {
				tsd = sideOld
			}
			if tsd == sd {
				n++
			}
		}
	}
	if w.splitFile(f) {
		if s := w.sideOf(f, r, sideOld); s.present {
			check(sideOld, s.num)
		}
		if s := w.sideOf(f, r, sideNew); s.present {
			check(sideNew, s.num)
		}
	} else {
		s := w.sideOf(f, r, sideNew)
		sd := sideNew
		if s.kind == diff.Del {
			sd = sideOld
		}
		if s.present {
			check(sd, s.num)
		}
	}
	return n, critical, ai
}

// annotationGutter is the quiet lane at the right of every line: comments
// and notes leave a dot here, and the lane keeps its width even when they
// do not, so the code never shifts.
func (w *window) annotationGutter(c *ui.Context, pal *palette, f *fileState, r *row) {
	t := c.Theme()
	n, critical, ai := w.rowAnnotationInfo(f, r)
	g := ui.Row(c).Width(16).Shrink(0).AlignSelf(ui.Stretch).Background(pal.codeBg).
		BorderWidth(0, 0, 0, 1).BorderColor(pal.cardBorder).Center()
	if n == 0 {
		return
	}
	g.Tooltip(plural(n, "annotation"))
	color := t.TextMuted
	switch {
	case critical:
		color = t.Danger
	case ai:
		color = t.Accent
	}
	g.Children(func() {
		dot := ui.Box(c).Size(6, 6).Radius(3).Background(color)
		if n > 1 {
			dot.Border(1, pal.codeBg)
		}
	})
}

// lineCell shows a line: its numbers and its code.
func (w *window) lineCell(c *ui.Context, pal *palette, f *fileState, r *row, s lineSide, sd side, gutter, hs float32, unified bool) {
	lh := w.lineHeight()
	if !s.present {
		ui.Box(c).Grow(1).Background(pal.emptySide).MinHeight(lh)
		return
	}
	bg, gutterBg, bar, numColor := pal.codeBg, pal.codeBg, ui.Transparent, pal.lineNumber
	var wordColor ui.Color
	switch s.kind {
	case diff.Add:
		bg, gutterBg, bar, numColor, wordColor = pal.addBg, pal.addGutter, pal.addBar, pal.addBar, pal.addWord
	case diff.Del:
		bg, gutterBg, bar, numColor, wordColor = pal.delBg, pal.delGutter, pal.delBar, pal.delBar, pal.delWord
	}
	active := w.activeLine > 0 && w.activePath == f.Path && w.activeSide == sd && w.activeLine == s.num
	if active {
		numColor = c.Theme().Accent
	}
	cell := ui.Row(c).Grow(1).MinWidth(0).AlignItems(ui.Stretch).Background(bg)
	if w.isSelectedLine(f, s) {
		sel := c.Theme().Accent.Alpha(0.13)
		cell.Background(blend(bg, sel))
		gutterBg = blend(gutterBg, sel)
	} else if cell.Hovered() {
		cell.Background(blend(bg, pal.hover.Alpha(0.5)))
		gutterBg = blend(gutterBg, pal.hover.Alpha(0.5))
	}
	if cell.Clicked() {
		// A click chooses the line; Enter or the + comments on it.
		w.activePath, w.activeSide, w.activeLine = f.Path, sd, s.num
		w.selFile, w.selHunk, w.noteSel = -1, -1, nil
	}
	cell.ContextMenu(func(m *ui.Menu) {
		if m.Item(fmt.Sprintf("Comment on Line %d", s.num)).Chosen() {
			w.addComment(f.Path, sd, s.num, s.num)
		}
		if m.Item("Copy Line").Chosen() {
			c.WriteClipboard(s.text)
		}
		m.Separator()
		line := s.num
		if sd == sideOld {
			line = 0
		}
		if m.Item("Open in Editor").Disabled(f.Status == diff.Deleted).Chosen() {
			w.openInEditor(f.Path, line)
		}
		if m.Item("Copy Relative Path").Chosen() {
			c.WriteClipboard(f.Path)
		}
	})
	cell.Children(func() {
		num := func(n int, width float32) {
			txt := ""
			if n > 0 {
				txt = fmt.Sprint(n)
			}
			ui.Text(c, txt).Font(w.codeFont()).FontSize(w.codeSize()-1).FixedLineHeight(lh).TextColor(numColor).
				Width(width).TextAlign(ui.End).Padding(0, 8, 0, 0).Shrink(0)
		}
		hovered := cell.Hovered()
		ui.Row(c).Shrink(0).AlignItems(ui.Start).Background(gutterBg).Children(func() {
			ui.Box(c).Width(4).AlignSelf(ui.Stretch).Background(bar)
			if unified {
				old, new := 0, 0
				if r.hunk < 0 {
					old, new = int(r.old), int(r.new)
				} else if l := f.lineAt(r.hunk, s.index); l != nil {
					old, new = l.Old, l.New
				}
				num(old, gutter-4)
				num(new, gutter-4)
			} else {
				num(s.num, gutter-4)
			}
			// The familiar + and - grammar, beside the numbers.
			mark := ""
			switch s.kind {
			case diff.Add:
				mark = "+"
			case diff.Del:
				mark = "−"
			}
			ui.Text(c, mark).Font(w.codeFont()).FontSize(w.codeSize() - 2).FixedLineHeight(lh).
				TextColor(numColor).Width(10).TextAlign(ui.Center).Shrink(0).Opacity(0.8)
		})
		spans := w.lineSpans(f, s, sd, pal, wordColor)
		// The button commenting on the line, at the edge of the numbers
		// while the pointer is over the line; always built, so that the
		// elements after it keep their state.
		numbers := gutter + 10
		if unified {
			numbers = 2*gutter - 4 + 10
		}
		if active {
			// The chosen line keeps a short accent rule beside its numbers.
			ui.Box(c).Absolute().Left(numbers - 10).Top(2).Bottom(2).Width(2).Radius(1).Background(c.Theme().Accent)
		}
		plus := ui.ButtonBase(c).Absolute().Top((lh-18)/2).Left(numbers-9).Size(18, 18).Radius(5).Center().
			Background(c.Theme().Accent).TextColor(c.Theme().AccentText).Label("Comment").Tooltip("Comment on this line").FocusRing(false)
		if !hovered {
			plus.Opacity(0)
		}
		plus.Children(func() { ui.Icon(c, iconPlus).FontSize(13) })
		if plus.Clicked() {
			w.addComment(f.Path, sd, s.num, s.num)
		}
		// The margin keeps the clipped code clear of the line numbers.
		code := ui.Box(c).Grow(1).Basis(0).MinWidth(0).ClipX().Margin(0, 10)
		code.Children(func() {
			text := ui.RichText(c, spans...).Font(w.codeFont()).
				FontSize(w.codeSize()).FixedLineHeight(lh).TextColor(pal.code)
			if !w.settings.WordWrap {
				text.NoWrap().AlignSelf(ui.Start).Left(-hs)
			}
		})
	})
}

// lineSpans styles the code of a line: kept from frame to frame, apart from
// the lines holding matches of the find bar, whose marks move.
func (w *window) lineSpans(f *fileState, s lineSide, sd side, pal *palette, wordColor ui.Color) []ui.Span {
	var marks []mark
	for _, rg := range s.words {
		marks = append(marks, mark{Range: rg, color: wordColor})
	}
	if w.searching() {
		if found := findRanges(s.text, w.query); found != nil {
			active := w.activeMatch(f, s)
			for _, rg := range found {
				color := pal.match
				if active {
					color = pal.matchNow
				}
				marks = append(marks, mark{Range: rg, color: color})
			}
			return codeSpans(s.text, s.segs, marks, pal)
		}
	}
	key := spanKey{hunk: s.hunk, index: s.index, num: int32(s.num), side: sd, dark: pal == &darkPalette}
	if spans, ok := f.spans[key]; ok {
		return spans
	}
	spans := codeSpans(s.text, s.segs, marks, pal)
	if f.spans == nil {
		f.spans = map[spanKey][]ui.Span{}
	}
	f.spans[key] = spans
	return spans
}

// blend lays a translucent color over another.
func blend(base, over ui.Color) ui.Color { return over.Over(base) }

// commentRow shows a comment, under the line it is on. It is flat: an
// accent rule, the author, and the field, with no card around it.
func (w *window) commentRow(c *ui.Context, pal *palette, f *fileState, cm *comment) {
	t := c.Theme()
	ui.Row(c).Background(pal.codeBg).Padding(7, 14, 8, 16).Gap(8).AlignItems(ui.Start).
		BorderWidth(0, 0, 0, 2).BorderColor(t.Accent).Children(func() {
		ui.Avatar(c, w.userName(), nil).Size(20, 20).Shrink(0)
		ui.Column(c).Grow(1).MinWidth(0).Gap(3).Children(func() {
			ui.Row(c).Height(20).Gap(6).Children(func() {
				ui.Text(c, w.userName()).FontSize(11.5).Bold()
				ui.Text(c, cm.label()).FontSize(10.5).TextColor(t.TextMuted)
				ui.Spacer(c)
				if iconButton(c, iconClose, "Delete comment").Size(20, 20).Clicked() {
					w.deleteComment(cm)
				}
			})
			area := ui.TextAreaBase(c, &cm.text).Placeholder("Write a review comment…").MinHeight(56).Padding(6, 8).
				Radius(6).Background(pal.headerBg).Border(1, pal.cardBorder).FontSize(13).Label("Review comment")
			if cm.focus {
				area.Focus()
				cm.focus = false
			}
			if area.Focused() {
				area.Border(1, t.Accent.Alpha(0.6))
				w.typing = true
			}
			if area.Shortcut(ui.Cmd, ui.KeyEnter) {
				if w.source.kind == sourcePull && w.pr.canWrite() {
					w.postComment(cm, true)
				} else {
					w.focusList = true
				}
			}
			if area.Shortcut(0, ui.KeyEscape) {
				if cm.pending() {
					w.discarding = cm
				} else {
					w.deleteComment(cm)
					w.focusList = true
				}
			}
			focused := area.Focused()
			if cm.wasFocused && !focused && !cm.pending() {
				// An empty comment the user left goes.
				w.deleteComment(cm)
			}
			cm.wasFocused = focused
			if w.source.kind == sourcePull {
				w.prCommentActions(c, pal, cm)
			}
		})
	})
}

// userName is the name of the git user, for comments, which loadUser
// reads as the window opens.
func (w *window) userName() string {
	if w.user == "" {
		return "Git user"
	}
	return w.user
}

// imageRow shows a changed picture: the old one and the new one, side by
// side, on a checkerboard for their transparency.
func (w *window) imageRow(c *ui.Context, pal *palette, f *fileState) {
	t := c.Theme()
	pane := func(label string, img *ui.Bitmap, size int, bar ui.Color) {
		ui.Column(c).Grow(1).Basis(0).MinWidth(0).Padding(14).Gap(8).AlignItems(ui.Center).Children(func() {
			if img == nil {
				ui.Text(c, "No "+strings.ToLower(label)+" image").FontSize(12).TextColor(t.TextMuted)
				return
			}
			iw, ih := img.Size()
			scale := min(float32(1), 320/float32(max(ih, 1)))
			ui.Box(c).Size(float32(iw)*scale, float32(ih)*scale).MaxWidthPercent(100).Radius(4).Border(1, pal.cardBorder).Clip().
				Stripes(ui.RGBA(127, 127, 127, 0.12), 6, 6, 45).Children(func() {
				ui.Image(c, img).Fill()
			})
			ui.Row(c).Gap(6).Children(func() {
				ui.Box(c).Size(8, 8).Radius(4).Background(bar)
				ui.Textf(c, "%s · %d×%d · %s", label, iw, ih, formatBytes(size)).FontSize(11).TextColor(t.TextMuted)
			})
		})
	}
	w.card(c, pal).AlignItems(ui.Stretch).Children(func() {
		if f.Status != diff.Added && f.Status != diff.Untracked {
			pane("Old", f.oldImage, f.oldSize, pal.delBar)
		}
		if f.Status != diff.Deleted && f.Status != diff.Added && f.Status != diff.Untracked {
			ui.Box(c).Width(1).Background(pal.cardBorder)
		}
		if f.Status != diff.Deleted {
			pane("New", f.newImage, f.newSize, pal.addBar)
		}
	})
}

// formatBytes writes a size: 512 B, 1.5 KiB, 12 MiB.
func formatBytes(n int) string {
	if n < 1024 {
		return fmt.Sprintf("%d B", n)
	}
	v, unit := float64(n)/1024, "KiB"
	for _, u := range []string{"MiB", "GiB"} {
		if v < 1024 {
			break
		}
		v, unit = v/1024, u
	}
	if v < 10 {
		return fmt.Sprintf("%.1f %s", v, unit)
	}
	return fmt.Sprintf("%.0f %s", v, unit)
}
