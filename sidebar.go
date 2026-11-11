package main

import (
	"fmt"
	"log"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"time"

	"github.com/egoist/mygo/ui"
	"github.com/volcanicll/diffscope/internal/diff"
	"github.com/volcanicll/diffscope/internal/git"
)

// treeNode is a row of the file tree: a file, or a directory with the
// chain of directories holding only it compacted into one row, as
// src/app/components.
type treeNode struct {
	name     string
	dir      bool
	file     int    // the file's index in window.files
	path     string // a directory's
	group    int    // a group's index, -1 for the others
	children []string
}

// The sidebar's width: its default, its bounds, and the width below which
// dragging hides it.
const (
	sidebarDefault  = 280
	sidebarMin      = 220
	sidebarMax      = 420
	sidebarCollapse = 80
)

// sidebarSlide is how long the sidebar takes to open or close.
const sidebarSlide = 220 * time.Millisecond

// fuzzyMatch reports whether the runes of query appear in s in order.
func fuzzyMatch(s, query string) bool {
	if query == "" {
		return true
	}
	s, query = strings.ToLower(s), strings.ToLower(query)
	i := 0
	for _, r := range s {
		if i < len(query) && r == rune(query[i]) {
			i++
		}
	}
	return i >= len(query)
}

// fileVisible reports whether a file shows: it passes the filter and,
// while finding, holds a match.
func (w *window) fileVisible(i int) bool {
	f := w.files[i]
	if !w.hasContent(f) {
		return false
	}
	if !fuzzyMatch(f.Path, strings.TrimSpace(w.filter)) {
		return false
	}
	if w.finding && strings.TrimSpace(w.query) != "" {
		return w.fileMatches[i]
	}
	return true
}

// hasContent reports whether a file has something to show: files whose
// only changes were whitespace, hidden, have nothing.
func (w *window) hasContent(f *fileState) bool {
	return len(f.Hunks) > 0 || f.Binary || f.Directory || f.TooLarge || f.Note != "" ||
		f.Status != diff.Modified || f.OldPath != f.Path || f.ModeChange != ""
}

// fileNote returns why a file's lines do not show, "" when they do.
func (w *window) fileNote(f *fileState) string {
	switch {
	case f.Note != "":
		return f.Note
	case f.Binary:
		return "Binary file changed."
	case f.TooLarge:
		return "File is too large, so " + appName + " skipped rendering it."
	case len(f.Hunks) == 0 && f.OldPath != f.Path:
		return "File renamed without changes."
	case len(f.Hunks) == 0 && f.ModeChange != "":
		return "File mode changed: " + f.ModeChange + "."
	case len(f.Hunks) == 0 && f.Status == diff.Added, len(f.Hunks) == 0 && f.Status == diff.Untracked:
		return "Empty file added."
	case len(f.Hunks) == 0 && f.Status == diff.Deleted:
		return "Empty file deleted."
	case len(f.Hunks) == 0:
		return "No changes to show."
	}
	return ""
}

// buildTree lays out the tree of the files that show: under their groups,
// when the files are grouped.
func (w *window) buildTree() {
	type dirNode struct {
		dirs  map[string]*dirNode
		order []string // names of dirs and files, in the files' order
		files map[string]int
	}
	newDir := func() *dirNode { return &dirNode{dirs: map[string]*dirNode{}, files: map[string]int{}} }
	items := map[string]*treeNode{}
	// tree builds the directories of some files, their keys made unique
	// by prefix.
	tree := func(files []int, prefix string) []string {
		root := newDir()
		for _, i := range files {
			f := w.files[i]
			parts := strings.Split(f.Path, "/")
			d := root
			for _, p := range parts[:len(parts)-1] {
				next := d.dirs[p]
				if next == nil {
					next = newDir()
					d.dirs[p] = next
					d.order = append(d.order, "d"+p)
				}
				d = next
			}
			name := parts[len(parts)-1]
			d.files[name] = i
			d.order = append(d.order, "f"+name)
		}
		var walk func(d *dirNode, path string) []string
		walk = func(d *dirNode, path string) []string {
			var keys []string
			for _, o := range d.order {
				name := o[1:]
				if o[0] == 'f' {
					i := d.files[name]
					key := "f:" + w.files[i].Path
					items[key] = &treeNode{name: name, file: i, group: -1}
					keys = append(keys, key)
					continue
				}
				sub := d.dirs[name]
				label := name
				dir := path + name
				// Directories holding one directory alone join it.
				for len(sub.order) == 1 && sub.order[0][0] == 'd' {
					child := sub.order[0][1:]
					label += "/" + child
					dir += "/" + child
					sub = sub.dirs[child]
				}
				key := "d:" + prefix + dir
				items[key] = &treeNode{name: label, dir: true, file: -1, path: dir, group: -1, children: walk(sub, dir+"/")}
				keys = append(keys, key)
			}
			return keys
		}
		return walk(root, "")
	}
	byGroup := make([][]int, len(w.groups))
	var ungrouped []int
	for i, f := range w.files {
		if !w.fileVisible(i) {
			continue
		}
		if f.group >= 0 && f.group < len(w.groups) {
			byGroup[f.group] = append(byGroup[f.group], i)
		} else {
			ungrouped = append(ungrouped, i)
		}
	}
	var roots []string
	for gi, files := range byGroup {
		if len(files) == 0 {
			continue
		}
		g := w.groups[gi]
		key := "g:" + g.key
		items[key] = &treeNode{name: g.label, dir: true, file: -1, group: gi, children: tree(files, g.key+":")}
		roots = append(roots, key)
	}
	roots = append(roots, tree(ungrouped, "")...)
	w.treeRoots = roots
	w.treeItems = items
}

// statusColor is the color of a file's status letter.
func statusColor(s diff.Status, pal *palette, t *ui.Theme) ui.Color {
	switch s {
	case diff.Added, diff.Untracked:
		return pal.addBar
	case diff.Deleted:
		return pal.delBar
	case diff.Renamed, diff.Copied:
		return t.Accent
	case diff.Conflict:
		return t.Danger
	}
	return pal.modified
}

func statusLetter(f *fileState) string {
	switch f.Status {
	case diff.Untracked:
		return "U"
	case diff.Conflict:
		return "!"
	}
	return string(f.Status)
}

// compact writes a count briefly: 999, 1.2k, 15k, 1.2m.
func compact(n int) string {
	switch {
	case n < 1000:
		return fmt.Sprint(n)
	case n < 10000:
		return strings.TrimSuffix(fmt.Sprintf("%.1f", float64(n)/1000), ".0") + "k"
	case n < 1000000:
		return fmt.Sprintf("%dk", (n+500)/1000)
	}
	return strings.TrimSuffix(fmt.Sprintf("%.1f", float64(n)/1000000), ".0") + "m"
}

// thousands writes a count with separators: 1,234.
func thousands(n int) string {
	s := fmt.Sprint(n)
	for i := len(s) - 3; i > 0; i -= 3 {
		s = s[:i] + "," + s[i:]
	}
	return s
}

func lines(n int, what string) string {
	if n == 1 {
		return "1 " + what + " line"
	}
	return thousands(n) + " " + what + " lines"
}

// countable reports whether a file's lines were counted.
func countable(f *fileState) bool {
	return !f.Binary && !f.Directory && !f.TooLarge
}

func (w *window) sidebar(c *ui.Context) {
	t := c.Theme()
	pal := paletteFor(t)
	// The navigator slides in and out: its width follows the toggle, and the
	// document beside it follows the width it takes. It keeps its element as
	// it hides, so that showing it again starts from nothing.
	col := ui.Column(c.Key("sidebar")).Shrink(0).Background(w.sidebarBg(t)).ClipX()
	open := float32(0)
	if w.sidebarShown {
		open = 1
	}
	width := w.sidebarWidth * col.Animate("open", open, sidebarSlide)
	w.sidebarNow = width
	col.Width(width)
	if width <= 0 && !w.sidebarShown {
		return
	}
	// The views keep their width as the navigator slides, and are clipped to
	// the width it shows: they do not lay out anew at every width on the
	// way, which would break their lines.
	col.Children(func() {
		ui.Column(c.Key("sidebar-body")).Width(w.sidebarWidth).Grow(1).Shrink(0).Children(func() {
			if w.tab > w.repoTabIndex() {
				w.tab = 0
			}
			w.navigatorHeader(c, pal)
			ui.Column(c).Padding(4, 10, 8).Children(func() {
				switch {
				case w.tab == 0:
					if searchInput(c, &w.filter, "Filter files", &w.filterFocus, &w.typing) {
						w.matchesFor = "\x00"
						w.buildTree()
						w.rowsDirty = true
					}
				case w.tab == 1:
					searchInput(c, &w.historyFilter, "Filter history", &w.filterFocus, &w.typing)
				case w.tab == w.repoTabIndex():
					if searchInput(c, &w.repoFilter, "Find a file", &w.filterFocus, &w.typing) {
						w.buildRepoTree()
						w.rowsDirty = true
					}
				default:
					searchInput(c, &w.pullsFilter, "Filter pull requests", &w.filterFocus, &w.typing)
				}
			})
			switch {
			case w.tab == 0:
				sidebarTab(c, "files", func() { w.fileTree(c) })
			case w.tab == 1:
				sidebarTab(c, "history", func() { w.historyView(c) })
			case w.tab == w.repoTabIndex():
				if !w.repoLoaded && !w.repoLoading {
					w.loadRepoFiles()
				}
				sidebarTab(c, "repository", func() { w.repoView(c) })
			default:
				sidebarTab(c, "pulls", func() { w.pullsView(c) })
			}
			if w.commitOpen && w.source.kind == sourceWorkingTree && w.tab == 0 {
				w.commitDock(c)
			}
			w.sidebarFooter(c, pal)
		})
	})
}

// navigatorHeader names the navigator's mode and counts what it lists. The
// scope lives in the title bar; grouping lives here, next to the list it
// changes.
func (w *window) navigatorHeader(c *ui.Context, pal *palette) {
	t := c.Theme()
	title, count := "Changes", ""
	switch {
	case w.tab == 0:
		n := 0
		for i := range w.files {
			if w.fileVisible(i) {
				n++
			}
		}
		count = plural(n, "file")
	case w.tab == 1:
		title = "History"
		count = plural(len(w.history), "commit")
	case w.tab == w.repoTabIndex():
		title = "Repository"
		if w.repoLoaded {
			count = plural(len(w.repoFiles), "file")
		}
	default:
		title = "Pulls"
		if w.pullsLoaded {
			count = fmt.Sprintf("%d open", len(w.pulls))
		}
	}
	ui.Row(c).Height(30).Shrink(0).Padding(0, 10, 0, 10).Gap(6).AlignItems(ui.Center).Children(func() {
		ui.Text(c, title).FontSize(12).FontWeight(600).SingleLine().Shrink(0)
		if count != "" {
			ui.Text(c, count).FontSize(11).TextColor(t.TextMuted).SingleLine().Shrink(1).MinWidth(0)
		}
		ui.Spacer(c)
		if w.tab == 0 {
			b := iconButton(c, iconLayers, "Group files")
			b.Menu(func(m *ui.Menu) {
				modes := []groupMode{groupNone, groupKind}
				labels := []string{"Not at all", "By kind"}
				if aiReviewEnabled {
					modes = append(modes, groupAI)
					labels = append(labels, "By AI review")
				}
				for i, mode := range modes {
					if m.Item(labels[i]).Checked(w.grouping() == mode).Chosen() {
						w.chooseGrouping(mode)
					}
				}
			})
		}
	})
}

// sidebarTab is a view of the sidebar, which fades and slides in as the
// tabs change.
func sidebarTab(c *ui.Context, key string, build func()) {
	ui.Column(c.Key(key)).Grow(1).MinWidth(0).
		Transition(ui.ElementTransition{Duration: 150 * time.Millisecond, Ease: ui.EaseOut,
			Enter: &ui.Motion{Y: 6, Opacity: 0}, Exit: &ui.Motion{Y: 6, Opacity: 0}}).
		Children(build)
}

// sidebarBg is the sidebar's background: none on macOS, where the
// window's material shows through, else the theme's surface.
func (w *window) sidebarBg(t *ui.Theme) ui.Color {
	if runtime.GOOS == "darwin" && w.win != nil {
		return ui.Transparent
	}
	if t.Dark {
		return ui.Hex("#262628")
	}
	return ui.Hex("#ebebed")
}

// titleBarHeight is the height of the bars along the top, which centers
// their controls on the traffic lights of a window with an inset title bar.
const titleBarHeight = 38

// sidebarToggle shows and hides the sidebar.
func (w *window) sidebarToggle(c *ui.Context) {
	tip := "Hide navigator (⌘⇧B)"
	if !w.sidebarShown {
		tip = "Show navigator (⌘⇧B)"
	}
	if iconButton(c, iconSidebar, tip).Clicked() {
		w.toggleSidebar()
	}
}

func (w *window) toggleSidebar() {
	w.sidebarShown = !w.sidebarShown
	if w.sidebarShown && w.sidebarWidth < sidebarMin {
		w.sidebarWidth = sidebarDefault
	}
	go state.setLayout(w.sidebarWidth, w.sidebarShown)
}

// searchInput is a field filtering a list, as the search fields of
// macOS: a magnifying glass, the text, and a button clearing it, as Escape
// does. It reports a change.
func searchInput(c *ui.Context, query *string, placeholder string, focus *bool, typing *bool) bool {
	t := c.Theme()
	changed := false
	box := ui.Row(c).Height(28).Padding(0, 6, 0, 8).Gap(6).Radius(7).Background(ui.RGBA(127, 127, 127, 0.12))
	box.Children(func() {
		ui.Icon(c, iconSearch).FontSize(13).TextColor(t.TextMuted)
		in := ui.TextInputBase(c, query).Placeholder(placeholder).Label(placeholder).FontSize(13).Grow(1).MinWidth(0)
		if *focus {
			in.Focus()
			*focus = false
		}
		if in.Changed() {
			changed = true
		}
		if in.Focused() {
			box.Shadow(0, 0, 0, 3, t.Focus.Alpha(0.45))
			*typing = true
		}
		if *query != "" {
			clear := ui.ButtonBase(c).Size(16, 16).Radius(8).Center().Background(t.TextMuted.Alpha(0.45)).Label("Clear").FocusRing(false)
			clear.Children(func() { ui.Icon(c, iconClose).FontSize(10).TextColor(t.Background) })
			if clear.Clicked() || in.Shortcut(0, ui.KeyEscape) {
				*query = ""
				changed = true
			}
		}
	})
	return changed
}

// iconButton is a button showing an icon alone, as in a toolbar.
func iconButton(c *ui.Context, svg *ui.SVG, tip string) ui.Element {
	t := c.Theme()
	b := ui.ButtonBase(c).Size(28, 28).Radius(7).Center().Label(tip).Tooltip(tip).TextColor(t.TextMuted)
	if b.Pressed() {
		b.Background(ui.RGBA(127, 127, 127, 0.22))
	} else if b.Hovered() {
		b.Background(ui.RGBA(127, 127, 127, 0.13))
	}
	b.Children(func() { ui.Icon(c, svg).FontSize(16) })
	return b
}

// treeRow is a row of the file tree as it shows: an item and its depth.
type treeRow struct {
	key   string
	depth int
}

// visibleTree lists the rows of the tree, inside the directories open.
func (w *window) visibleTree() []treeRow {
	var rows []treeRow
	var walk func(keys []string, depth int)
	walk = func(keys []string, depth int) {
		for _, k := range keys {
			rows = append(rows, treeRow{k, depth})
			if n := w.treeItems[k]; n != nil && n.dir && !w.closedDirs[k] {
				walk(n.children, depth+1)
			}
		}
	}
	walk(w.treeRoots, 0)
	return rows
}

func (w *window) fileTree(c *ui.Context) {
	t := c.Theme()
	pal := paletteFor(t)
	if len(w.treeRoots) == 0 {
		ui.Column(c).Grow(1).Center().Padding(16).Children(func() {
			msg := "No changed files"
			switch {
			case w.loading && len(w.files) == 0:
				msg = "Loading…"
			case strings.TrimSpace(w.filter) != "":
				msg = "No matching files"
			}
			ui.Text(c, msg).FontSize(12).TextColor(t.TextMuted)
		})
		return
	}
	rows := w.visibleTree()
	w.treeRows = rows
	choose := func(key string) {
		w.treeSel = key
		if n := w.treeItems[key]; n != nil && !n.dir {
			w.revealFile(n.file)
		}
	}
	w.treeList.Key = func(i int) any { return rows[i].key }
	w.treeList.Label = func(i int) string {
		if n := w.treeItems[rows[i].key]; n != nil {
			return n.name
		}
		return ""
	}
	ui.List(c, &w.treeList, len(rows), func(i int) {
		focused := w.treeList.FocusWithin(c)
		key := rows[i].key
		n := w.treeItems[key]
		if n == nil {
			return
		}
		selected := key == w.treeSel
		row := ui.Row(c).Height(28).Padding(0, 8, 0, 6+float32(rows[i].depth)*14).Gap(5).Radius(6).MinWidth(0)
		textColor := t.Text
		switch {
		case selected && focused:
			row.Background(t.Accent)
			textColor = t.AccentText
		case selected:
			row.Background(t.Accent.Alpha(0.10))
		case row.Hovered():
			row.Background(ui.RGBA(127, 127, 127, 0.08))
		}
		if row.Pressed() && (!selected || w.commitOpen) {
			choose(key)
		}
		if n.dir {
			if row.Clicked() {
				w.closedDirs[key] = !w.closedDirs[key]
				choose(key)
			}
		} else {
			f := w.files[n.file]
			// The menu's file is the one chosen, as the menu opens.
			row.ContextMenu(func(m *ui.Menu) {
				w.treeSel = key
				w.fileMenu(c, m, f.Path, w.canEdit(f))
			})
			if row.Clicked() {
				choose(key)
			}
		}
		muted := t.TextMuted
		if selected && focused {
			muted = t.AccentText.Alpha(0.8)
		}
		row.TextColor(textColor).Children(func() {
			arrow := ui.Box(c).Size(14, 14).Center().Shrink(0)
			if n.dir {
				arrow.Children(func() {
					ic := ui.Icon(c, iconChevronDown).FontSize(12).TextColor(muted)
					target := float32(0)
					if w.closedDirs[key] {
						target = -90
					}
					ic.Rotate(ic.Animate("rot", target, 150*time.Millisecond))
				})
				if n.group >= 0 && n.group < len(w.groups) {
					g := &w.groups[n.group]
					icon := ui.Icon(c, groupIcon(g)).FontSize(14).TextColor(muted)
					if g.critical && !(selected && focused) {
						icon.TextColor(t.Danger)
					}
					ui.Text(c, n.name).FontSize(13).FontWeight(600).SingleLine().Grow(1).Shrink(1).MinWidth(0).Tooltip(g.summary)
					ui.Textf(c, "%d", w.treeCount(key)).Font(w.codeFont()).FontSize(10).FontWeight(600).TextColor(muted).Shrink(0)
					return
				}
				ui.Icon(c, iconFolder).FontSize(14).TextColor(muted)
				ui.Text(c, n.name).FontSize(13).SingleLine().Grow(1).Shrink(1).MinWidth(0)
				return
			}
			f := w.files[n.file]
			viewed := w.isViewed(f)
			icon := fileIcon(c, f, 14)
			if selected && focused {
				icon.TextColor(muted)
			}
			name := ui.Text(c, n.name).FontSize(13).SingleLine().Grow(1).Shrink(1).MinWidth(0)
			if viewed && !(selected && focused) {
				name.TextColor(t.TextMuted)
			}
			if notes := w.notes[f.Path]; len(notes) > 0 {
				// The file has notes of the agent's review.
				color := t.Accent
				if slices.ContainsFunc(notes, func(n *aiNote) bool { return n.critical }) {
					color = t.Danger
				}
				if selected && focused {
					color = t.AccentText
				}
				ui.Icon(c, iconSparkle).FontSize(11).TextColor(color).Shrink(0).Tooltip(plural(len(notes), "AI note"))
			}
			if countable(f) && (f.Additions > 0 || f.Deletions > 0) {
				ui.Textf(c, "+%s -%s", compact(f.Additions), compact(f.Deletions)).
					Font(w.codeFont()).FontSize(10).FontWeight(600).TextColor(muted).Shrink(0).
					Tooltip(lines(f.Additions, "added") + ", " + lines(f.Deletions, "removed"))
			}
			letter := statusColor(f.Status, pal, t)
			switch {
			case selected && focused:
				letter = t.AccentText
			case viewed:
				letter = t.TextMuted
			case w.reloaded[f.Path]:
				letter = pal.ref
			}
			ui.Text(c, statusLetter(f)).Font(w.codeFont()).FontSize(11).FontWeight(700).TextColor(letter).
				Width(12).TextAlign(ui.Center).Shrink(0).Tooltip(f.Status.Label())
		})
	}).Grow(1).Padding(2, 8).Gap(1).Focusable().FocusRing(false).Label("Changed files")

	// The keys move the choice, open and close directories.
	at := -1
	for i, r := range rows {
		if r.key == w.treeSel {
			at = i
		}
	}
	move := func(i int) {
		if i >= 0 && i < len(rows) {
			choose(rows[i].key)
			w.treeList.ScrollIntoView(i)
		}
	}
	if w.treeList.Shortcut(c, 0, ui.KeyDown) {
		move(at + 1)
	}
	if w.treeList.Shortcut(c, 0, ui.KeyUp) {
		move(max(at-1, 0))
	}
	if w.treeList.Shortcut(c, 0, ui.KeyHome) {
		move(0)
	}
	if w.treeList.Shortcut(c, 0, ui.KeyEnd) {
		move(len(rows) - 1)
	}
	if at >= 0 {
		n := w.treeItems[rows[at].key]
		if w.treeList.Shortcut(c, 0, ui.KeyRight) && n != nil && n.dir {
			if w.closedDirs[rows[at].key] {
				w.closedDirs[rows[at].key] = false
			} else {
				move(at + 1)
			}
		}
		if w.treeList.Shortcut(c, 0, ui.KeyLeft) {
			if n != nil && n.dir && !w.closedDirs[rows[at].key] {
				w.closedDirs[rows[at].key] = true
			} else {
				// To the directory holding it.
				for j := at - 1; j >= 0; j-- {
					if rows[j].depth < rows[at].depth {
						move(j)
						break
					}
				}
			}
		}
	}
}

// filePath is where a file of the work tree lives.
func (w *window) filePath(path string) string {
	return filepath.Join(w.repo.Root, filepath.FromSlash(path))
}

// canEdit reports whether the file tree's actions may change a file of the
// review: only the work tree's views show files that are there.
func (w *window) canEdit(f *fileState) bool {
	if f.Directory || f.Status == diff.Deleted {
		return false
	}
	return w.source.kind == sourceWorkingTree || w.source.kind == sourceBranch
}

// fileMenu adds the actions of a file of the work tree to a menu, as its
// row of the tree, its row of the repository and its card show them.
// editable reports whether the actions may change the file.
func (w *window) fileMenu(c *ui.Context, m *ui.Menu, path string, editable bool) {
	if m.Item("Copy Relative Path").Chosen() {
		c.WriteClipboard(path)
		c.Toast("Copied " + path)
	}
	if m.Item("Copy Absolute Path").Chosen() {
		c.WriteClipboard(w.filePath(path))
		c.Toast("Copied " + path)
	}
	m.Separator()
	if m.Item("Rename…").Disabled(!editable).Chosen() {
		w.startRename(path)
	}
	if m.Item("Delete…").Disabled(!editable).Chosen() {
		w.askingDelete = path
	}
}

// startRename asks for the new path of a file of the work tree.
func (w *window) startRename(path string) {
	w.renamePath = path
	w.renameValue = path
	w.renameErr = ""
	w.renameBusy = false
	w.renameOpen = true
}

// renameDialog asks for a file's new path, relative to the repository.
func (w *window) renameDialog(c *ui.Context) {
	t := c.Theme()
	if !w.renameOpen {
		return
	}
	rename := func() {
		if w.renameBusy {
			return
		}
		to := filepath.ToSlash(filepath.Clean(filepath.FromSlash(strings.TrimSpace(w.renameValue))))
		if !filepath.IsLocal(filepath.FromSlash(to)) {
			w.renameErr = "Enter a path inside the repository."
			return
		}
		if to == w.renamePath {
			w.renameOpen = false
			return
		}
		from, dest := w.filePath(w.renamePath), w.filePath(to)
		// Renaming a file to its own name in other letters only changes
		// its case, as the file system may not tell the two apart.
		if info, err := os.Stat(dest); err == nil {
			same := false
			if old, err := os.Stat(from); err == nil {
				same = os.SameFile(info, old)
			}
			if !same {
				w.renameErr = "A file already exists at " + to + "."
				return
			}
		}
		old := w.renamePath
		w.renameBusy = true
		w.background(func() {
			err := os.Rename(from, dest)
			w.update(func() {
				w.renameBusy = false
				if err != nil {
					w.renameErr = err.Error()
					return
				}
				w.renameOpen = false
				w.closeBrowse(old)
				w.refresh()
				c.Toast("Renamed to " + to)
			})
		})
	}
	ui.Modal(c, &w.renameOpen, func() {
		ui.Column(c).Width(440).Gap(14).Children(func() {
			ui.Column(c).Gap(4).Children(func() {
				ui.Text(c, "Rename File").FontSize(16).Bold()
				ui.Text(c, "The path is relative to the repository's root.").FontSize(13).TextColor(t.TextMuted)
			})
			ui.Field(c, "New path", func() {
				if ui.TextInput(c, &w.renameValue).Font(w.codeFont()).AutoFocus().Submitted() {
					rename()
				}
			}).Error(w.renameErr)
			ui.Row(c).Gap(8).Justify(ui.End).Children(func() {
				if ui.Button(c, "Cancel").Clicked() {
					w.renameOpen = false
				}
				label := "Rename"
				if w.renameBusy {
					label = "Renaming…"
				}
				if ui.PrimaryButton(c, label).Disabled(w.renameBusy).Clicked() {
					rename()
				}
			})
		})
	})
}

// deleteDialog asks to delete the file the menu chose.
func (w *window) deleteDialog(c *ui.Context) {
	path := w.askingDelete
	if path == "" {
		return
	}
	name := filepath.Base(filepath.FromSlash(path))
	message := "The file is deleted from the working tree. Git can restore a tracked file."
	for _, f := range w.files {
		if f.Path == path && f.Status == diff.Untracked {
			message = "The file is untracked, so this cannot be undone."
		}
	}
	open := true
	if choice := ui.AlertDialog(c, &open, "Delete "+name+"?", message, "Cancel", "Delete"); choice == 1 {
		w.askingDelete = ""
		w.background(func() {
			err := os.Remove(w.filePath(path))
			w.update(func() {
				if err != nil {
					c.Toast("Could not delete " + name + ": " + err.Error())
					return
				}
				w.closeBrowse(path)
				w.refresh()
				c.Toast("Deleted " + name)
			})
		})
	} else if !open {
		w.askingDelete = ""
	}
}

// closeBrowse closes the file the browser shows when path is it.
func (w *window) closeBrowse(path string) {
	if w.browse == path {
		w.browse = ""
		w.rowsDirty = true
	}
}

// treeCount counts the files under a row of the tree.
func (w *window) treeCount(key string) int {
	n := w.treeItems[key]
	if n == nil {
		return 0
	}
	if !n.dir {
		return 1
	}
	total := 0
	for _, k := range n.children {
		total += w.treeCount(k)
	}
	return total
}

// selectTreeFile chooses a file's row in the tree, as the surface
// scrolls to it.
func (w *window) selectTreeFile(i int) {
	key := "f:" + w.files[i].Path
	if w.treeSel == key {
		return
	}
	w.treeSel = key
	// The directories holding it open, and its group.
	for k, n := range w.treeItems {
		if !n.dir || !w.closedDirs[k] {
			continue
		}
		if (n.group >= 0 && n.group == w.files[i].group) || (n.path != "" && strings.HasPrefix(w.files[i].Path, n.path+"/")) {
			w.closedDirs[k] = false
		}
	}
	for r, row := range w.visibleTree() {
		if row.key == key {
			w.treeList.ScrollIntoView(r)
			return
		}
	}
}

// historyView lists the commits of the History tab.
func (w *window) historyView(c *ui.Context) {
	t := c.Theme()
	pal := paletteFor(t)
	q := strings.ToLower(strings.TrimSpace(w.historyFilter))
	type entry struct {
		local  bool
		commit *git.Commit
	}
	var entries []entry
	if q == "" {
		entries = append(entries, entry{local: true})
	}
	for i := range w.history {
		cm := &w.history[i]
		if q == "" || strings.Contains(strings.ToLower(cm.Subject), q) || strings.HasPrefix(cm.Hash, q) || strings.Contains(strings.ToLower(cm.Author), q) {
			entries = append(entries, entry{commit: cm})
		}
	}
	target := w.source
	current := -1
	for i, e := range entries {
		if (e.local && (target.kind == sourceWorkingTree || target.kind == sourceBranch)) || (e.commit != nil && target.kind == sourceCommit && target.ref == e.commit.Hash) {
			current = i
		}
	}
	open := func(i int) {
		if i < 0 || i >= len(entries) {
			return
		}
		w.commitOpen = false
		w.historyList.ScrollIntoView(i)
		if entries[i].local {
			w.setSource(w.launchWorkTree())
		} else {
			w.setSource(source{kind: sourceCommit, ref: entries[i].commit.Hash})
		}
	}
	w.historyList.Key = func(i int) any {
		if entries[i].local {
			return "local"
		}
		return entries[i].commit.Hash
	}
	w.historyList.Label = func(i int) string {
		if entries[i].local {
			return "Uncommitted changes"
		}
		return entries[i].commit.Subject
	}
	now := time.Now()
	list := ui.List(c, &w.historyList, len(entries), func(i int) {
		focused := w.historyList.FocusWithin(c) || w.focusHistory
		e := entries[i]
		row := ui.Row(c).Gap(8).Padding(5, 8).Radius(6).AlignItems(ui.Start).Role(ui.RoleButton)
		if e.local {
			row.Label("Uncommitted changes")
		} else {
			row.Label(e.commit.Subject)
		}
		muted, ref := t.TextMuted, pal.ref
		switch {
		case i == current && focused:
			row.Background(t.Accent).TextColor(t.AccentText)
			muted, ref = t.AccentText.Alpha(0.75), t.AccentText
		case i == current:
			row.Background(t.Accent.Alpha(0.10))
		case row.Hovered():
			row.Background(ui.RGBA(127, 127, 127, 0.08))
		}
		if (row.Pressed() || row.Clicked()) && i != current {
			if debugFrames {
				log.Printf("history row %d clicked", i)
			}
			open(i)
		}
		row.Children(func() {
			if e.local {
				ui.Text(c, "local").Font(w.codeFont()).FontSize(12).TextColor(ref).Width(56).Shrink(0)
				ui.Text(c, "Uncommitted changes").FontSize(12).SingleLine().Grow(1)
				return
			}
			cm := e.commit
			when := w.commitTimes[cm.Hash]
			if when.at != now.Truncate(time.Minute) {
				// Formatted once a minute, rather than every frame.
				when = commitTime{at: now.Truncate(time.Minute), ago: relativeTime(now, cm.Time), full: cm.Time.Format("Mon Jan 2 15:04:05 2006")}
				w.commitTimes[cm.Hash] = when
			}
			ui.Text(c, cm.Short).Font(w.codeFont()).FontSize(12).TextColor(ref).Width(56).Shrink(0)
			ui.Column(c).Grow(1).MinWidth(0).Gap(2).Children(func() {
				ui.Text(c, cm.Subject).FontSize(12).SingleLine().Tooltip(cm.Subject)
				ui.Row(c).Gap(6).Children(func() {
					ui.Text(c, cm.Author).FontSize(10).SingleLine().TextColor(muted).Grow(1).MinWidth(0)
					ui.Text(c, when.ago).FontSize(10).TextColor(muted).Shrink(0).Tooltip(when.full)
				})
			})
		})
	}).Grow(1).Padding(2, 8).Gap(1).Focusable().FocusRing(false).Label("History")
	if w.focusHistory && w.historyList.Focus(c) {
		w.focusHistory = false
	}
	list.Children(func() {
		if len(entries) == 0 {
			ui.Text(c, "No matching commits").FontSize(12).TextColor(t.TextMuted).Padding(12)
		}
	})
	if w.historyList.Shortcut(c, 0, ui.KeyDown) {
		open(current + 1)
	}
	if w.historyList.Shortcut(c, 0, ui.KeyUp) {
		open(max(current-1, 0))
	}
	// More commits load well before the end comes into view, so that
	// scrolling does not stop there.
	if q == "" && w.historyMore && !w.historyLoading {
		if _, last := w.historyList.Visible(); last >= len(entries)-historyPage/2 {
			w.historyLimit += historyPage
			w.loadHistory()
		}
	}
}

// commitTime is how a commit's time shows, as of a minute.
type commitTime struct {
	at        time.Time
	ago, full string
}

// launchWorkTree is the source of uncommitted changes: the work tree, or
// its comparison with a branch when the window was opened on one.
func (w *window) launchWorkTree() source {
	if w.launch.kind == sourceBranch {
		return w.launch
	}
	return source{kind: sourceWorkingTree}
}

// relativeTime writes how long ago t was: just now, 5m ago, 3d ago; ""
// for no time.
func relativeTime(now, t time.Time) string {
	if t.IsZero() {
		return ""
	}
	d := now.Sub(t)
	switch {
	case d < time.Minute:
		return "just now"
	case d < time.Hour:
		return fmt.Sprintf("%dm ago", int(d/time.Minute))
	case d < 24*time.Hour:
		return fmt.Sprintf("%dh ago", int(d/time.Hour))
	case d < 30*24*time.Hour:
		return fmt.Sprintf("%dd ago", int(d/(24*time.Hour)))
	case d < 365*24*time.Hour:
		return fmt.Sprintf("%dmo ago", int(d/(30*24*time.Hour)))
	}
	return fmt.Sprintf("%dy ago", int(d/(365*24*time.Hour)))
}

// sidebarFooter shows the total of the changes, and the Commit button.
func (w *window) sidebarFooter(c *ui.Context, pal *palette) {
	t := c.Theme()
	adds, dels, counted := 0, 0, false
	for i, f := range w.files {
		if w.fileVisible(i) && countable(f) {
			adds += f.Additions
			dels += f.Deletions
			counted = true
		}
	}
	canCommit := w.tab == 0 && w.source.kind == sourceWorkingTree && len(w.files) > 0
	if w.commitOpen && canCommit {
		// The dock above carries the totals and the action while it is open.
		return
	}
	if !(counted && w.tab == 0) && !canCommit {
		return
	}
	ui.Row(c).MinHeight(40).Padding(6, 10).Gap(8).BorderWidth(1, 0, 0, 0).BorderColor(pal.cardBorder).Children(func() {
		if counted && w.tab == 0 {
			ui.Row(c).Gap(6).Tooltip("Total change: " + lines(adds, "added") + ", " + lines(dels, "removed")).Children(func() {
				ui.Text(c, "Total:").FontSize(11).FontWeight(600).TextColor(t.TextMuted)
				ui.Text(c, "+"+thousands(adds)).Font(w.codeFont()).FontSize(11).FontWeight(600).TextColor(pal.addText)
				ui.Text(c, "-"+thousands(dels)).Font(w.codeFont()).FontSize(11).FontWeight(600).TextColor(pal.delText)
			})
		}
		ui.Spacer(c)
		if canCommit {
			label := "Commit"
			if w.commitOpen {
				label = "Review"
			}
			b := ui.Button(c, "").Children(func() {
				ui.Icon(c, iconCommit).FontSize(14)
				ui.Text(c, label).SingleLine()
			})
			if b.Clicked() {
				w.toggleCommit()
			}
		}
	})
}

// sidebarResizer is the edge of the sidebar the user drags.
func (w *window) sidebarResizer(c *ui.Context, pal *palette) {
	ui.Box(c).Width(1).Shrink(0).Background(pal.cardBorder).Children(func() {
		handle := ui.Box(c).Absolute().Top(0).Bottom(0).Left(-4).Width(9).Cursor(ui.CursorResizeEW)
		if dx, _, held := handle.Dragged(); held {
			w.dragWidth += dx
			switch {
			case w.dragWidth < sidebarCollapse:
				w.sidebarShown = false
			default:
				w.sidebarShown = true
				w.sidebarWidth = min(max(w.dragWidth, sidebarMin), sidebarMax)
			}
		} else {
			if w.dragWidth != 0 && w.dragWidth != w.sidebarWidth {
				go state.setLayout(w.sidebarWidth, w.sidebarShown)
			}
			w.dragWidth = w.sidebarWidth
		}
	})
}
