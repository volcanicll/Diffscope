package main

import (
	"fmt"
	"strings"
	"time"

	"github.com/egoist/mygo"
	"github.com/egoist/mygo/ui"
	"github.com/volcanicll/diffscope/internal/diff"
	"github.com/volcanicll/diffscope/internal/fileicon"
	"github.com/volcanicll/diffscope/internal/highlight"
)

// browseMaxLines is how many lines of a file the browser shows.
const browseMaxLines = 10000

// browseNode is a row of the Repository tab: a file or a directory.
type browseNode struct {
	name string
	dir  bool
	// path is the slash path of the file, or of the directory.
	path string
}

// browseRow is a row of the Repository tab as it shows: a node and its
// depth.
type browseRow struct {
	key   string
	depth int
}

// switchBranch checks out a local branch, then reloads the review and the
// history of it.
func (w *window) switchBranch(branch string) {
	w.background(func() {
		err := w.repo.Checkout(branch)
		w.update(func() {
			if err != nil {
				mygo.Dialog.Error("Could not switch to "+branch, errorText(err))
				return
			}
			w.branch, w.branches = "", nil
			w.load()
			w.loadHistory()
			if w.repoLoaded {
				w.loadRepoFiles()
			}
		})
	})
}

// loadRepoFiles lists the files of the work tree for the Repository tab.
func (w *window) loadRepoFiles() {
	w.repoLoading = true
	w.background(func() {
		paths, err := w.repo.AllFiles()
		w.update(func() {
			w.repoLoading = false
			w.repoLoaded = true
			if err != nil {
				w.repoErr = err
				return
			}
			w.repoErr = nil
			w.repoFiles = paths
			w.buildRepoTree()
			w.rowsDirty = true
		})
	})
}

// repoTabIndex is the sidebar tab of the Repository, which the pull
// requests tab shifts when GitHub has no remote.
func (w *window) repoTabIndex() int {
	if w.gh.ok() {
		return 3
	}
	return 2
}

// buildRepoTree fills the rows of the Repository tab from the file paths,
// the filtered ones when a filter is on, open all the way then.
func (w *window) buildRepoTree() {
	type dirNode struct {
		dirs  map[string]*dirNode
		files map[string]string // base name to slash path
		order []string          // the names of the dirs and files, in file order
	}
	newDir := func() *dirNode { return &dirNode{dirs: map[string]*dirNode{}, files: map[string]string{}} }
	root := newDir()
	filter := strings.TrimSpace(w.repoFilter)
	for _, p := range w.repoFiles {
		if filter != "" && !fuzzyMatch(strings.ToLower(p), strings.ToLower(filter)) {
			continue
		}
		parts := strings.Split(p, "/")
		d := root
		for _, part := range parts[:len(parts)-1] {
			next := d.dirs[part]
			if next == nil {
				next = newDir()
				d.dirs[part] = next
				d.order = append(d.order, "d"+part)
			}
			d = next
		}
		name := parts[len(parts)-1]
		d.order = append(d.order, "f"+name)
		d.files[name] = p
	}
	w.repoTree, w.repoItems = nil, map[string]*browseNode{}
	var walk func(d *dirNode, path string, depth int)
	walk = func(d *dirNode, path string, depth int) {
		for _, o := range d.order {
			if o[0] == 'f' {
				p := d.files[o[1:]]
				key := "f:" + p
				w.repoItems[key] = &browseNode{name: baseName(p), path: p}
				w.repoTree = append(w.repoTree, browseRow{key: key, depth: depth})
				continue
			}
			name := o[1:]
			key := "d:" + path + name
			w.repoItems[key] = &browseNode{name: name, dir: true, path: path + name}
			w.repoTree = append(w.repoTree, browseRow{key: key, depth: depth})
			// A filter looks everywhere, else closed dirs stay closed.
			if filter != "" || !w.closedDirs[key] {
				walk(d.dirs[name], path+name+"/", depth+1)
			}
		}
	}
	walk(root, "", 0)
}

// baseName is the last part of a slash path.
func baseName(p string) string {
	if i := strings.LastIndexByte(p, '/'); i >= 0 {
		return p[i+1:]
	}
	return p
}

// repoView lists the files of the repository, to open one for viewing.
func (w *window) repoView(c *ui.Context) {
	t := c.Theme()
	switch {
	case w.repoErr != nil:
		ui.Column(c).Grow(1).Center().Padding(16).Children(func() {
			ui.Text(c, errorText(w.repoErr)).FontSize(12).TextColor(t.TextMuted).TextAlign(ui.Center)
		})
		return
	case w.repoLoading && len(w.repoTree) == 0:
		ui.Column(c).Grow(1).Center().Padding(16).Children(func() {
			ui.Text(c, "Loading…").FontSize(12).TextColor(t.TextMuted)
		})
		return
	case len(w.repoTree) == 0:
		ui.Column(c).Grow(1).Center().Padding(16).Children(func() {
			ui.Text(c, "No files").FontSize(12).TextColor(t.TextMuted)
		})
		return
	}
	rows := w.repoTree
	w.repoList.Key = func(i int) any { return rows[i].key }
	w.repoList.Label = func(i int) string {
		if n := w.repoItems[rows[i].key]; n != nil {
			return n.name
		}
		return ""
	}
	ui.List(c, &w.repoList, len(rows), func(i int) {
		key, n := rows[i].key, w.repoItems[rows[i].key]
		if n == nil {
			return
		}
		selected := !n.dir && w.browse == n.path
		row := ui.Row(c).Height(28).Padding(0, 8, 0, 6+float32(rows[i].depth)*14).Gap(5).Radius(6).MinWidth(0)
		switch {
		case selected:
			row.Background(ui.RGBA(127, 127, 127, 0.2))
		case row.Hovered():
			row.Background(ui.RGBA(127, 127, 127, 0.08))
		}
		if row.Clicked() {
			if n.dir {
				w.closedDirs[key] = !w.closedDirs[key]
				w.buildRepoTree()
				w.rowsDirty = true
			} else {
				w.showBrowse(n.path)
			}
		}
		row.TextColor(t.Text).Children(func() {
			if n.dir {
				arrow := ui.Box(c).Size(14, 14).Center().Shrink(0)
				arrow.Children(func() {
					ic := ui.Icon(c, iconChevronDown).FontSize(12).TextColor(t.TextMuted)
					target := float32(0)
					if w.closedDirs[key] {
						target = -90
					}
					ic.Rotate(ic.Animate("rot", target, 150*time.Millisecond))
				})
				ui.Icon(c, iconFolder).FontSize(14).TextColor(t.TextMuted).Shrink(0)
			} else {
				ui.Box(c).Size(14, 14).Shrink(0)
				kind := fileicon.For(n.path)
				ui.Icon(c, kind.SVG).FontSize(14).TextColor(kind.Color(t.Dark)).Shrink(0)
			}
			ui.Text(c, n.name).FontSize(12).SingleLine().MinWidth(0)
		})
	})
}

// showBrowse opens a file of the work tree in the browser.
func (w *window) showBrowse(path string) {
	w.commitOpen = false
	w.browse = path
	w.browseList = ui.ListState{}
	w.browseLoading = true
	w.browseLines, w.browseSegs, w.browseImage, w.browseSVG = nil, nil, nil, nil
	w.browseFinding, w.browseQuery, w.browseMatchesFor = false, "", ""
	w.browseMatches, w.browseMatch = nil, 0
	w.rowsDirty = true
	w.background(func() {
		data := w.repo.ReadWorkTree(path)
		// Decoding a picture takes the time of the read over, off the main
		// thread.
		var img *ui.Bitmap
		var svg *ui.SVG
		if len(data) > 0 && len(data) <= maxImage {
			if isImage(path) {
				img, _ = ui.DecodeBitmap(data)
			} else if isSVG(path) {
				svg, _ = ui.ParseSVG(data)
			}
		}
		w.update(func() {
			if w.browse != path {
				return
			}
			w.browseLoading = false
			w.loadBrowse(data, img, svg)
		})
	})
}

// loadBrowse decodes the bytes of the browsed file into highlighted lines,
// or keeps the picture it holds. An SVG that does not parse shows as text.
func (w *window) loadBrowse(data []byte, img *ui.Bitmap, svg *ui.SVG) {
	w.browseImage, w.browseSVG, w.browseSize = img, svg, len(data)
	switch {
	case img != nil || svg != nil: // the picture shows in place of the lines
	case data == nil || diff.IsBinary(data):
		w.browseBinary, w.browseLines, w.browseSegs = true, nil, nil
	default:
		w.browseBinary = false
		lines := diff.SplitLines(string(data))
		if len(lines) > browseMaxLines {
			lines, w.browseTruncated = lines[:browseMaxLines], true
		} else {
			w.browseTruncated = false
		}
		w.browseLines = lines
		w.browseSegs = highlight.Lines(w.browse, lines)
	}
	w.updateBrowseMatches()
	w.rowsDirty = true
}

// browseSeg returns the highlighted segments of a line of the browsed file,
// nil when its kind has no lexer, as for go.sum.
func (w *window) browseSeg(i int) []highlight.Seg {
	if i < len(w.browseSegs) {
		return w.browseSegs[i]
	}
	return nil
}

// browseMatch is a line of the browsed file holding the query of the find
// bar.
type browseMatch struct {
	line int32
}

// searchingBrowse reports whether the browser's find bar is open with a
// query.
func (w *window) searchingBrowse() bool {
	return w.browseFinding && strings.TrimSpace(w.browseQuery) != "" && w.browseLines != nil
}

// updateBrowseMatches finds the query in the lines of the browsed file.
func (w *window) updateBrowseMatches() {
	key := w.browseQuery
	if !w.browseFinding {
		key = ""
	}
	if key == w.browseMatchesFor {
		return
	}
	w.browseMatchesFor = key
	w.browseMatches, w.browseMatch = nil, 0
	if key == "" {
		w.browseQuery = ""
		return
	}
	for li, l := range w.browseLines {
		if len(findRanges(l, key)) > 0 {
			w.browseMatches = append(w.browseMatches, browseMatch{line: int32(li)})
		}
	}
}

// showBrowseMatch scrolls the current match into the middle of the view.
func (w *window) showBrowseMatch() {
	if w.browseMatch < 0 || w.browseMatch >= len(w.browseMatches) {
		return
	}
	w.browseList.ScrollTo(int(w.browseMatches[w.browseMatch].line), ui.Center)
}

// browseMarks are the backgrounds of the find bar's matches on a line.
func (w *window) browseMarks(i int, pal *palette) []mark {
	if !w.searchingBrowse() {
		return nil
	}
	now := -1
	if w.browseMatch < len(w.browseMatches) {
		now = int(w.browseMatches[w.browseMatch].line)
	}
	var out []mark
	for _, rg := range findRanges(w.browseLines[i], w.browseQuery) {
		color := pal.match
		if i == now {
			color = pal.matchNow
		}
		out = append(out, mark{Range: rg, color: color})
	}
	return out
}

// browseView shows the file the browser has open.
func (w *window) browseView(c *ui.Context, pal *palette) {
	t := c.Theme()
	path := w.browse
	ui.Column(c).Grow(1).Background(pal.appBg).Children(func() {
		ui.Row(c).Height(46).Padding(0, 12).Gap(8).Shrink(0).
			Background(pal.headerBg).Border(1, pal.cardBorder).Children(func() {
			if iconButton(c, iconClose, "Close the file (esc)").Clicked() {
				w.browse = ""
				w.rowsDirty = true
			}
			kind := fileicon.For(path)
			ui.Icon(c, kind.SVG).FontSize(15).TextColor(kind.Color(t.Dark)).Shrink(0)
			ui.Text(c, path).Font(w.codeFont()).FontSize(12).SingleLine().MinWidth(0).Grow(1)
			if iconButton(c, iconSearch, "Find in file (⌘F)").Clicked() {
				w.browseFinding = !w.browseFinding
			}
			if iconButton(c, iconOpen, "Open file in editor (⌘⇧O)").Clicked() {
				w.openInEditor(path, 1)
			}
		})
		switch {
		case w.browseLoading:
			thinking(c, "Reading…")
		case w.browseImage != nil || w.browseSVG != nil:
			var iw, ih float32
			if w.browseImage != nil {
				bw, bh := w.browseImage.Size()
				iw, ih = float32(bw), float32(bh)
			} else {
				iw, ih = w.browseSVG.Size()
			}
			if iw <= 0 || ih <= 0 {
				iw, ih = 256, 256
			}
			scale := min(float32(1), 480/float32(max(ih, 1)))
			ui.Column(c).Grow(1).Center().Padding(16).Gap(10).Children(func() {
				ui.Box(c).Size(iw*scale, ih*scale).MaxWidthPercent(100).Radius(4).
					Border(1, pal.cardBorder).Clip().Stripes(ui.RGBA(127, 127, 127, 0.12), 6, 6, 45).
					Children(func() {
						if w.browseImage != nil {
							ui.Image(c, w.browseImage).Fill()
						} else {
							ui.Image(c, w.browseSVG).Fill()
						}
					})
				ui.Textf(c, "%d×%d · %s", int(iw), int(ih), formatBytes(w.browseSize)).FontSize(11).TextColor(t.TextMuted)
			})
		case w.browseBinary:
			title := "Binary file"
			if isImage(path) {
				title = "The image cannot be shown"
			}
			emptyPanel(c, pal, title, path, nil)
		default:
			find := ui.FindBar(c, &w.browseFinding, &w.browseQuery, len(w.browseMatches), &w.browseMatch).Label("Find in file")
			if find.FocusWithin() {
				w.typing = true
			}
			if w.browseQuery != w.browseMatchesFor && (w.browseFinding || w.browseMatchesFor != "") {
				w.updateBrowseMatches()
			}
			if find.Changed() {
				w.showBrowseMatch()
			}
			ui.List(c, &w.browseList, len(w.browseLines), func(i int) {
				row := ui.Row(c).MinWidth(0).Padding(0, 12, 0, 8)
				if row.Hovered() {
					row.Background(pal.hover)
				}
				row.Children(func() {
					num := fmt.Sprintf("%d", i+1)
					ui.Text(c, num).Font(w.codeFont()).
						FontSize(float32(w.settings.CodeFontSize)).TextColor(t.TextMuted).
						Width(float32(28 + 8*len(fmt.Sprintf("%d", len(w.browseLines))))).
						TextAlign(ui.End).Shrink(0)
					ui.RichText(c, codeSpans(w.browseLines[i], w.browseSeg(i), w.browseMarks(i, pal), pal)...).
						Font(w.codeFont()).FontSize(float32(w.settings.CodeFontSize)).SingleLine().MinWidth(0)
				})
			})
			if w.browseTruncated {
				ui.Text(c, fmt.Sprintf("The file has more than %d lines; the rest is not shown.", browseMaxLines)).
					FontSize(11).TextColor(t.TextMuted).Padding(6, 12).Shrink(0)
			}
		}
	})
}
