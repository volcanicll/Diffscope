package main

import (
	"errors"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/egoist/mygo/ui"
	"github.com/volcanicll/diffscope/internal/git"
)

func (w *window) view(c *ui.Context) {
	if debugFrames {
		start := time.Now()
		defer func() {
			d := time.Since(start)
			if since := start.Sub(w.switchedAt); since < 3*time.Second {
				log.Printf("frame %v after the switch: view %v (files %d, rows %d, loading %v)", since, d, len(w.files), len(w.rows), w.loading)
			} else if d > 4*time.Millisecond {
				log.Printf("slow view: %v (rows %d, comments %d)", d, len(w.rows), len(w.comments))
			}
		}()
	}
	t := c.Theme()
	pal := paletteFor(t)
	w.now = c.Now()
	if w.regroupPending {
		w.regroupPending = false
		w.regroup()
	}
	// On macOS, the window shows the sidebar's material where nothing is
	// drawn. Elsewhere it would show black, as under the translucent edge
	// of the sidebar, so the sidebar's color is drawn there instead.
	c.Root().Background(w.sidebarBg(t))

	reviewBuilt := false
	ui.Column(c).Fill().Children(func() {
		// The title bar spans the window: the repository, the branch and
		// the scope stay put while the navigator opens and closes.
		w.toolbar(c, pal)
		ui.Row(c).Grow(1).AlignItems(ui.Stretch).Children(func() {
			w.rail(c, pal)
			// The navigator is built whether it shows or not: it slides, so
			// the review beside it follows its width as it opens and closes.
			w.sidebar(c)
			if w.sidebarShown {
				w.sidebarResizer(c, pal)
			}
			ui.Column(c.Key("main")).Grow(1).MinWidth(0).Background(pal.appBg).Children(func() {
				// The terminal's layout is always built: the panel slides open
				// and closed inside it, the review taking the room it leaves.
				if w.termShown {
					w.ensureTerminals()
				}
				if w.settings.TerminalPosition == "right" {
					w.termRight(c, pal, &reviewBuilt)
				} else {
					w.termBottom(c, pal, &reviewBuilt)
				}
				// The repository's file opens over the document, not in place
				// of it: closing it returns to the same review, same scroll.
				if w.browse != "" {
					ui.Box(c).Absolute().Top(0).Bottom(0).Left(0).Right(0).Background(pal.codeBg).
						Children(func() { w.browseView(c, pal) })
				}
			})
		})
	})
	w.shortcuts(c, reviewBuilt)
	w.palette(c)
	w.sourceDialog(c)
	w.renameDialog(c)
	w.deleteDialog(c)
	w.shortcutsHelp(c)
	w.reviewDialog(c)
	if w.discarding != nil {
		open := true
		if choice := ui.AlertDialog(c, &open, "Discard this review comment?", "Its text will be lost.", "Cancel", "Discard"); choice == 1 {
			w.deleteComment(w.discarding)
			w.discarding = nil
		} else if !open {
			w.discarding = nil
		}
	}
	if !w.focusedOnce && reviewBuilt {
		// The review takes the keys as the window opens.
		w.focusedOnce = true
		w.focusList = true
	}
	if w.focusList && w.list.Focus(c) {
		w.focusList = false
	}
}

// debugFrames logs the views that take long to build.
var debugFrames = os.Getenv("DIFFSCOPE_DEBUG") != ""

// toolbar is the bar along the top of the window, which drags it. It spans
// the whole width: the repository, the branch and the scope are global.
func (w *window) toolbar(c *ui.Context, pal *palette) {
	t := c.Theme()
	bar := c.TitleBar()
	ui.Row(c).Height(titleBarHeight).Padding(0, bar.Right+12, 0, bar.Left+8).Gap(7).AlignItems(ui.Center).Shrink(0).DragWindow().
		BorderWidth(0, 0, 1, 0).BorderColor(pal.cardBorder).Background(pal.headerBg).Children(func() {
		w.sidebarToggle(c)
		// The repository: its name, and the actions that belong to it.
		repo := ui.ButtonBase(c).Height(26).Padding(0, 6).Gap(5).Radius(6).Shrink(1).MinWidth(0).
			Label("Repository menu").Tooltip(abbreviateHome(w.repo.Root))
		if repo.Hovered() {
			repo.Background(pal.hover)
		}
		repo.Menu(func(m *ui.Menu) {
			if m.Item("Open Folder…").Shortcut(ui.Cmd, ui.KeyO).Chosen() {
				openFolderFrom(w)
			}
			if m.Item("Copy Path").Chosen() {
				c.WriteClipboard(w.repo.Root)
				c.Toast("Copied the repository path")
			}
			if m.Item("Open Config File…").Chosen() {
				openConfig()
			}
		})
		repo.Children(func() {
			ui.Text(c, filepath.Base(w.repo.Root)).FontSize(13).Bold().SingleLine().Ellipsis("…").MaxWidth(150)
			ui.Icon(c, iconChevronDown).FontSize(11).TextColor(t.TextMuted)
		})
		if w.branch != "" && w.source.kind != sourcePull {
			branch := chip(c, pal, iconBranch, w.branch, t.TextMuted).Tooltip("Branch " + w.branch)
			if w.source.kind == sourceWorkingTree && len(w.branches) > 0 {
				branch.Menu(func(m *ui.Menu) {
					for _, b := range w.branches {
						if m.Item(b).Checked(b == w.branch).Chosen() {
							w.switchBranch(b)
						}
					}
				})
			}
		}
		back := func(el ui.Element) ui.Element {
			el.Menu(func(m *ui.Menu) {
				if m.Item("Back to Changes").Chosen() {
					w.showMode(0)
				}
			})
			return el
		}
		switch w.source.kind {
		case sourceCommit:
			label := shortHash(w.source.ref)
			back(chip(c, pal, iconCommit, label, pal.ref).Font(w.codeFont()).Tooltip(w.source.ref))
		case sourceBranch:
			back(chip(c, pal, iconBranch, "vs "+w.source.ref, pal.ref).Tooltip("The work tree since it branched from " + w.source.ref))
		case sourcePull:
			repo, n := w.source.pull()
			tip := repo.String()
			if w.pr != nil && w.pr.meta != nil {
				tip = w.pr.meta.Title
				back(chip(c, pal, iconPull, fmt.Sprintf("#%d", n), pal.ref).Tooltip(tip).Shrink(0))
				chip(c, pal, iconBranch, w.pr.meta.Base.Ref+" ← "+w.pr.meta.Head.Ref, t.TextMuted).Tooltip(w.pr.meta.Base.Label + " ← " + w.pr.meta.Head.Label)
			} else {
				back(chip(c, pal, iconPull, fmt.Sprintf("#%d", n), pal.ref).Tooltip(tip).Shrink(0))
			}
		}
		if w.source.kind == sourceWorkingTree {
			w.scopeSegment(c, pal, t)
		}
		if w.loading && len(w.files) > 0 {
			ui.Spinner(c).Label("Loading").Size(14, 14)
		}
		ui.Spacer(c)
		// From here on: what the review is doing, and how it shows.
		ui.Row(c).Gap(2).AlignItems(ui.Center).Children(func() {
			if iconButton(c, iconSearch, "Find in diffs (⌘F)").Clicked() {
				w.finding = !w.finding
			}
			w.commentsButton(c, pal, t)
			w.aiButton(c, pal)
			w.viewOptions(c, pal, t)
			if !w.sidebarShown && w.source.kind == sourceWorkingTree && len(w.files) > 0 {
				w.toolbarCommit(c)
			}
			w.moreButton(c)
		})
	})
}

// chip is a label in a pill, as the branch.
func chip(c *ui.Context, pal *palette, svg *ui.SVG, label string, color ui.Color) ui.Element {
	return ui.Row(c).Gap(5).Padding(3, 8).Radius(12).Background(ui.RGBA(127, 127, 127, 0.08)).
		Border(1, ui.RGBA(127, 127, 127, 0.12)).TextColor(color).Shrink(1).MinWidth(0).MaxWidth(240).Children(func() {
		ui.Icon(c, svg).FontSize(12)
		ui.Text(c, label).FontSize(12).FontWeight(600).SingleLine()
	})
}

// scopeSegment chooses what part of the work tree's changes the review
// shows: all of them, the staged ones, or the unstaged ones. It sits in
// the title bar, in the one line of context that never scrolls.
func (w *window) scopeSegment(c *ui.Context, pal *palette, t *ui.Theme) {
	var counts [3]int
	if w.stageMode == stageAll {
		for _, f := range w.files {
			if f.Staged {
				counts[stageStaged]++
			}
			if f.Unstaged {
				counts[stageUnstaged]++
			}
		}
		counts[stageAll] = len(w.files)
	} else {
		counts[w.stageMode] = len(w.files)
	}
	ui.Row(c).Height(26).Padding(2).Gap(2).Radius(7).Background(pal.pill).Shrink(0).
		Label("Change scope").Children(func() {
		for i, name := range []string{"All", "Staged", "Unstaged"} {
			active := i == w.stageMode
			b := ui.ButtonBase(c).Height(22).Padding(0, 8).Gap(5).Radius(5).Label(name + " changes").Tooltip(name + " changes")
			if active {
				b.Background(pal.headerBg).TextColor(t.Text).Shadow(0, 1, 2, 0, ui.RGBA(0, 0, 0, 0.08))
			} else {
				b.TextColor(t.TextMuted)
				if b.Hovered() {
					b.Background(pal.hover).TextColor(t.Text)
				}
			}
			if b.Clicked() {
				w.setStage(i)
			}
			b.Children(func() {
				ui.Text(c, name).FontSize(12).FontWeight(map[bool]int{true: 600, false: 400}[active])
				// The other scopes' counts are known only while all the
				// changes are shown.
				if w.stageMode == stageAll || active {
					ui.Textf(c, "%d", counts[i]).Font(w.codeFont()).FontSize(10.5).FontWeight(400).TextColor(t.TextMuted)
				}
			})
		}
	})
}

// setStage shows another part of the work tree's changes.
func (w *window) setStage(mode int) {
	if mode == w.stageMode {
		return
	}
	w.stageMode = mode
	w.load()
}

// commentsButton copies the pending review comments. It shows only when
// there are some: an empty count has nothing to do in the toolbar.
func (w *window) commentsButton(c *ui.Context, pal *palette, t *ui.Theme) {
	n := w.pendingComments()
	if n == 0 {
		return
	}
	copied := time.Since(w.copiedAt) < 2*time.Second
	cb := ui.ButtonBase(c).Height(26).Padding(0, 7).Gap(4).Radius(7).TextColor(t.TextMuted).
		Label("Copy review comments").Tooltip("Copy review comments as Markdown")
	if cb.Hovered() {
		cb.Background(ui.RGBA(127, 127, 127, 0.13))
	}
	if cb.Clicked() {
		w.copyComments()
		c.After(2 * time.Second)
	}
	cb.Children(func() {
		if copied {
			ui.Icon(c, iconCheck).FontSize(14).TextColor(pal.viewed)
		} else {
			ui.Icon(c, iconComment).FontSize(14)
		}
		ui.Textf(c, "%d", n).Font(w.codeFont()).FontSize(10.5).FontWeight(700)
	})
}

// aiButton opens the agent's review, once an agent is installed: the
// sparkle is the one entry to the review and the notes.
func (w *window) aiButton(c *ui.Context, pal *palette) {
	if !aiReviewEnabled {
		return
	}
	installed, known := installedAgents()
	if !known || len(installed) == 0 {
		return
	}
	tip := "Review with AI (⌘⇧I)"
	st := w.analyses[w.source]
	running := st != nil && st.running
	b := ui.ButtonBase(c).Size(28, 28).Radius(7).Center().Label(tip).Tooltip(tip).TextColor(c.Theme().Accent)
	if b.Pressed() {
		b.Background(ui.RGBA(127, 127, 127, 0.22))
	} else if b.Hovered() {
		b.Background(ui.RGBA(127, 127, 127, 0.13))
	}
	b.Children(func() {
		if running {
			ui.Spinner(c).Size(14, 14)
			return
		}
		ui.Icon(c, iconSparkle).FontSize(16)
	})
	if b.Clicked() {
		w.reviewWithAI()
	}
}

// viewOptions is the toolbar's sliders and the panel they open: the
// settings of the diff, the grouping and the terminal, out of the bar.
func (w *window) viewOptions(c *ui.Context, pal *palette, t *ui.Theme) {
	btn := iconButton(c, iconSliders, "View options (⌘⇧V)")
	if btn.Clicked() {
		w.viewOptionsOpen = !w.viewOptionsOpen
	}
	ui.PopoverBase(c, btn, &w.viewOptionsOpen, func(panel ui.Element) {
		panel.Margin(6, 0, 0, 0).Width(276).Padding(9).Radius(10).Background(pal.headerBg).
			Border(1, pal.cardBorder).Shadow(0, 10, 30, 0, ui.RGBA(0, 0, 0, 0.18))
		ui.Column(c).Gap(2).Children(func() {
			ui.Row(c).Gap(10).AlignItems(ui.Center).Children(func() {
				ui.Text(c, "Diff").FontSize(11).FontWeight(600).TextColor(t.TextMuted).Grow(1)
				w.diffChoice(c, pal, t)
			})
			ui.Box(c).Height(1).Background(pal.cardBorder).Margin(6, 4)
			wrap := w.settings.WordWrap
			if ui.Checkbox(c, &wrap, "Word wrap").Changed() {
				cfg.Update(func(s *Settings) { s.WordWrap = wrap })
			}
			whitespace := w.settings.ShowWhitespace
			if ui.Checkbox(c, &whitespace, "Show whitespace").Changed() {
				cfg.Update(func(s *Settings) { s.ShowWhitespace = whitespace })
			}
			ui.Row(c).Gap(10).AlignItems(ui.Center).Children(func() {
				ui.Text(c, "Terminal").FontSize(11).FontWeight(600).TextColor(t.TextMuted).Grow(1)
				w.terminalChoice(c, pal, t)
			})
		})
	})
}

// diffChoice switches between split and unified diffs.
func (w *window) diffChoice(c *ui.Context, pal *palette, t *ui.Theme) {
	choice := 0
	if !w.split() {
		choice = 1
	}
	seg := ui.SegmentedBase(c, &choice, 2)
	seg.Track.Padding(2).Gap(2).Radius(8).Background(ui.RGBA(127, 127, 127, 0.10)).Label("Diff layout").Children(func() {
		for i, name := range []string{"Split", "Unified"} {
			s := seg.Segment(i).Height(24).Padding(0, 8).Radius(6).Center().Label(name).Tooltip(name).TextColor(t.TextMuted)
			if i == choice {
				s.Background(pal.headerBg).Shadow(0, 1, 2, 0, ui.RGBA(0, 0, 0, 0.12)).TextColor(t.Text)
			}
			s.OnClick(func() {
				split := i == 0
				if split != w.split() {
					toggleLayout()
					w.settings.DiffStyle = map[bool]string{true: "split", false: "unified"}[split]
					w.rowsDirty = true
				}
			})
			s.Children(func() { ui.Text(c, name).FontSize(11).FontWeight(600) })
		}
	})
}

// terminalChoice puts the terminal below the review or beside it.
func (w *window) terminalChoice(c *ui.Context, pal *palette, t *ui.Theme) {
	choice := 0
	if w.settings.TerminalPosition == "right" {
		choice = 1
	}
	seg := ui.SegmentedBase(c, &choice, 2)
	seg.Track.Padding(2).Gap(2).Radius(8).Background(ui.RGBA(127, 127, 127, 0.10)).Label("Terminal position").Children(func() {
		for i, name := range []string{"Bottom", "Right"} {
			s := seg.Segment(i).Height(24).Padding(0, 8).Radius(6).Center().Label(name).Tooltip(name).TextColor(t.TextMuted)
			if i == choice {
				s.Background(pal.headerBg).Shadow(0, 1, 2, 0, ui.RGBA(0, 0, 0, 0.12)).TextColor(t.Text)
			}
			s.OnClick(func() {
				pos := map[int]string{0: "bottom", 1: "right"}[i]
				if pos != w.settings.TerminalPosition {
					w.setTerminalPos(pos)
				}
			})
			s.Children(func() { ui.Text(c, name).FontSize(11).FontWeight(600) })
		}
	})
}

// moreButton holds the actions that need no place in the bar.
func (w *window) moreButton(c *ui.Context) {
	b := iconButton(c, iconMore, "More")
	b.Menu(func(m *ui.Menu) {
		if m.Item("Open Folder…").Shortcut(ui.Cmd, ui.KeyO).Chosen() {
			openFolderFrom(w)
		}
		if m.Item("Browse Repository").Shortcut(ui.Cmd, ui.Key4).Chosen() {
			w.tab, w.sidebarShown = w.repoTabIndex(), true
		}
		if m.Item("Terminal").Checked(w.termShown).Shortcut(ui.Ctrl, ui.KeyBackquote).Chosen() {
			w.toggleTerminal()
		}
		if m.Item("New Terminal").Shortcut(ui.Ctrl|ui.Shift, ui.KeyBackquote).Chosen() {
			w.newTerminal()
		}
		if w.pendingComments() > 0 {
			m.Separator()
			if m.Item("Copy Review Comments").Chosen() {
				w.copyComments()
			}
		}
	})
}

// toolbarCommit opens the commit sheet when the sidebar, which holds the
// same button, is closed.
func (w *window) toolbarCommit(c *ui.Context) {
	b := ui.Button(c, "").Height(28).Tooltip("Commit the changes (⌘⏎)").Children(func() {
		ui.Icon(c, iconCommit).FontSize(14)
		ui.Text(c, "Commit").SingleLine()
	})
	if b.Clicked() {
		w.toggleCommit()
	}
}

// mainArea shows the review, the commit view, or why there is nothing,
// and reports whether it built the review list in this pass. The review,
// a commit's sheet and a browsed file fade into one another.
func (w *window) mainArea(c *ui.Context, pal *palette) bool {
	key := "review"
	built := false
	ui.Column(c.Key(key)).Grow(1).MinWidth(0).
		Transition(ui.ElementTransition{Duration: 180 * time.Millisecond, Ease: ui.EaseOut,
			Enter: &ui.Motion{Y: 6, Opacity: 0}, Exit: &ui.Motion{Y: 6, Opacity: 0}}).
		Children(func() { built = w.mainAreaBody(c, pal) })
	return built
}

func (w *window) mainAreaBody(c *ui.Context, pal *palette) bool {
	t := c.Theme()
	if w.changed {
		ui.Row(c).Justify(ui.Center).Padding(8, 12, 0).Children(func() {
			ui.Row(c).Gap(6).Padding(5, 6, 5, 12).Radius(16).Background(pal.viewed.Alpha(0.1).Over(pal.codeBg)).
				Border(1, pal.viewed.Alpha(0.2)).Children(func() {
				what := "Local changes detected,"
				if w.source.kind == sourcePull {
					what = "New commits were pushed,"
				}
				ui.Text(c, what).FontSize(13).FontWeight(600).TextColor(pal.viewed.Mix(t.Text, 0.6))
				ref := ui.ButtonBase(c).FocusRing(true).Children(func() {
					ui.Text(c, "refresh to see them.").FontSize(13).FontWeight(600).Underline().TextColor(pal.viewed.Mix(t.Text, 0.6))
				})
				if ref.Clicked() {
					w.refresh()
				}
				if iconButton(c, iconClose, "Dismiss").Size(22, 22).Clicked() {
					w.changed = false
				}
			})
		})
	}
	find := ui.FindBar(c, &w.finding, &w.query, len(w.matches), &w.match).Label("Find in diffs")
	if find.FocusWithin() {
		w.typing = true
	}
	if find.Changed() {
		w.showMatch()
	}
	if w.query != w.matchesFor && (w.finding || w.matchesFor != "") {
		w.rowsDirty = true
	}
	if !w.finding && w.matchesFor != "" {
		w.query = ""
		w.rowsDirty = true
	}

	// A load shows "Thinking…" once it takes long: until then the window
	// keeps what it showed, so that quick loads change it once.
	waiting := w.loading && len(w.files) == 0
	slow := false
	if waiting {
		if left := thinkingDelay - time.Since(w.loadStart); left > 0 {
			c.After(left)
		} else {
			slow = true
		}
	}
	// The commit's message shows at once, from the history, until it
	// shows atop the changes.
	if w.source.kind == sourceCommit && w.commit != nil && w.loadErr == nil && len(w.files) == 0 {
		w.commitMessage(c, pal).Margin(11, 12, 0)
	}
	switch {
	case w.loadErr != nil && w.source.kind == sourcePull:
		emptyPanel(c, pal, "Unable to open the pull request", errorText(w.loadErr), func() {
			if ui.Button(c, "Try Again").Clicked() {
				w.load()
			}
		})
	case w.loadErr != nil:
		emptyPanel(c, pal, "Unable to read repository", errorText(w.loadErr), nil)
	case slow:
		thinking(c, w.loadStatus)
	case waiting && len(w.files) == 0:
		ui.Box(c).Grow(1)
	case len(w.files) == 0:
		title, detail := "No local changes", abbreviateHome(w.repo.Root)
		switch w.source.kind {
		case sourceCommit:
			title, detail = "No changes in commit", shortHash(w.source.ref)
		case sourceBranch:
			title, detail = "No changes", w.source.ref
		case sourcePull:
			title, detail = "No changes in pull request", w.source.ref
		}
		emptyPanel(c, pal, title, detail, func() {
			if w.source.kind == sourceWorkingTree && len(w.history) > 0 {
				if ui.Button(c, "Show History").Clicked() {
					w.tab, w.sidebarShown = 1, true
				}
			}
		})
	default:
		if w.rowsDirty {
			w.updateMatches()
			w.buildRows()
		}
		if len(w.rows) == 0 {
			title, detail := "No matching files", strings.TrimSpace(w.filter)
			if w.searching() {
				title, detail = "No matches in diffs", strings.TrimSpace(w.query)
			}
			if detail == "" {
				detail = "Whitespace-only changes hidden"
			}
			emptyPanel(c, pal, title, detail, nil)
			return false
		}
		w.diffList(c)
		return true
	}
	return false
}

// commitMessage is the commit's source strip: its hash, subject, author and
// time, with the body folded under it until asked for.
func (w *window) commitMessage(c *ui.Context, pal *palette) ui.Element {
	t := c.Theme()
	cm := w.commit
	return ui.Column(c).Children(func() {
		strip := ui.Row(c).Height(36).Shrink(0).Padding(0, 12).Gap(8).Background(pal.headerBg).
			BorderWidth(0, 0, 1, 0).BorderColor(pal.cardBorder)
		strip.Children(func() {
			ui.Text(c, shortHash(cm.Hash)).Font(w.codeFont()).FontSize(11).TextColor(pal.ref).Selectable().
				Tooltip(cm.Hash)
			ui.Text(c, cm.Subject).FontSize(12.5).FontWeight(600).SingleLine().Grow(1).Shrink(1).MinWidth(0).Selectable()
			ui.Textf(c, "%s · %s", cm.Author, relativeTime(w.now, cm.Time)).FontSize(10.5).TextColor(t.TextMuted).Shrink(0)
			if iconButton(c, iconCopy, "Copy commit hash").Size(24, 24).Clicked() {
				c.WriteClipboard(cm.Hash)
				c.Toast("Copied " + shortHash(cm.Hash))
			}
			if cm.Body != "" {
				tip := "Show the commit message"
				icon := iconChevronRight
				if w.commitMsgOpen {
					tip, icon = "Hide the commit message", iconChevronDown
				}
				if iconButton(c, icon, tip).Size(24, 24).Clicked() {
					w.commitMsgOpen = !w.commitMsgOpen
				}
			}
		})
		if w.commitMsgOpen && cm.Body != "" {
			ui.Row(c).Background(pal.headerBg).Padding(7, 16, 10).BorderWidth(0, 0, 1, 0).BorderColor(pal.cardBorder).Children(func() {
				ui.Text(c, cm.Body).FontSize(12.5).Selectable().TextColor(t.Text.Alpha(0.9))
			})
		}
	})
}

// emptyCard is the panel that says why there is nothing to show.
func emptyCard(c *ui.Context, pal *palette, title, detail string, actions func()) {
	t := c.Theme()
	ui.Column(c).MaxWidth(460).Padding(20).Gap(8).AlignItems(ui.Center).Children(func() {
		ui.Icon(c, iconFileDiff).FontSize(24).TextColor(t.TextMuted)
		ui.Text(c, title).FontSize(15).Bold().TextAlign(ui.Center)
		if detail != "" {
			ui.Text(c, detail).FontSize(12.5).Font("SF Mono, Menlo, monospace").TextColor(t.TextMuted).TextAlign(ui.Center).Selectable()
		}
		if actions != nil {
			ui.Row(c).Gap(8).Margin(6, 0, 0).Children(actions)
		}
	})
}

// emptyPanel says why there is nothing to show, centered in the space it
// takes.
func emptyPanel(c *ui.Context, pal *palette, title, detail string, actions func()) {
	ui.Column(c).Grow(1).Center().Padding(24).Children(func() {
		emptyCard(c, pal, title, detail, actions)
	})
}

// thinkingDelay is how long a load goes before the window says so.
const thinkingDelay = 200 * time.Millisecond

// thinking shows that the changes are loading, and what the load does
// when it says.
func thinking(c *ui.Context, status string) {
	t := c.Theme()
	if status == "" {
		status = "Thinking…"
	}
	ui.Column(c).Grow(1).Center().Children(func() {
		label := ui.Text(c, status).Italic().FontSize(13).Font("SF Mono, Menlo, monospace").TextColor(t.TextMuted)
		label.Opacity(0.5 + 0.5*label.Loop("pulse", 1600*time.Millisecond, ui.Bounce(ui.EaseInOut)))
	})
}

// shortcuts handles the window's keys that the menus do not.
func (w *window) shortcuts(c *ui.Context, reviewBuilt bool) {
	if c.Shortcut(ui.Cmd|ui.Shift, ui.KeyP) {
		w.paletteOpen = !w.paletteOpen
		w.paletteRow = 0
	}
	// The terminal opens and closes wherever the focus is, and a new one
	// opens with shift.
	if c.Shortcut(ui.Ctrl|ui.Shift, ui.KeyBackquote) {
		w.newTerminal()
	} else if c.Shortcut(ui.Ctrl, ui.KeyBackquote) {
		w.toggleTerminal()
	}
	// Keys that type text belong to the fields: these work while no field
	// had the focus in the last frame.
	typing := w.typing
	w.typing = false
	if w.commitOpen && !typing && c.Shortcut(0, ui.KeyEscape) {
		w.toggleCommit()
		return
	}
	if !typing && c.Shortcut(0, ui.KeyEscape) {
		// One key, one rung of the ladder.
		if st := w.analyses[w.source]; st != nil && st.result != nil && !st.folded {
			st.folded = true
			w.rowsDirty = true
			return
		}
		if w.pr != nil && w.pr.descOpen {
			w.pr.descOpen = false
			return
		}
		if w.commitMsgOpen {
			w.commitMsgOpen = false
			return
		}
		if w.browse != "" {
			w.browse = ""
			w.rowsDirty = true
			return
		}
		if w.selHunk >= 0 || w.noteSel != nil || w.activeLine > 0 {
			w.selFile, w.selHunk, w.noteSel = -1, -1, nil
			w.activePath, w.activeLine = "", 0
			return
		}
	}
	if w.paletteOpen || w.dialogOpen || w.renameOpen || w.askingDelete != "" || w.help || typing || !reviewBuilt || (w.pr != nil && w.pr.submitOpen) {
		return
	}
	if c.Shortcut(ui.Cmd|ui.Shift, ui.KeyV) {
		w.viewOptionsOpen = !w.viewOptionsOpen
	}
	if c.Shortcut(ui.Cmd, ui.KeyEnter) && w.source.kind == sourceWorkingTree && len(w.files) > 0 {
		if w.commitOpen {
			if w.commitCan() {
				w.makeCommit()
			}
		} else {
			w.toggleCommit()
		}
	}
	if c.Shortcut(0, ui.KeyJ) || c.Shortcut(ui.Ctrl, ui.KeyDown) {
		w.nextHunk(1)
	}
	if c.Shortcut(0, ui.KeyK) || c.Shortcut(ui.Ctrl, ui.KeyUp) {
		w.nextHunk(-1)
	}
	if c.Shortcut(ui.Cmd|ui.Shift, ui.KeyBracketRight) {
		w.nextFile(1)
	}
	if c.Shortcut(ui.Cmd|ui.Shift, ui.KeyBracketLeft) {
		w.nextFile(-1)
	}
	if w.list.Shortcut(c, 0, ui.KeyEnter) || ((w.selHunk >= 0 || w.activeLine > 0) && c.Shortcut(0, ui.KeyEnter)) {
		w.commentOnSelection()
	}
	if c.Shortcut(0, ui.KeyN) {
		w.nextNote(1)
	}
	if c.Shortcut(ui.Shift, ui.KeyN) {
		w.nextNote(-1)
	}
	if c.Shortcut(0, ui.KeyV) && w.current >= 0 && w.current < len(w.files) {
		f := w.files[w.current]
		w.setViewed(f, !w.isViewed(f))
	}
	if c.Shortcut(ui.Alt, ui.KeyZ) {
		toggleWrap()
	}
	if c.Shortcut(ui.Shift, ui.KeySlash) {
		w.help = true
	}
}

// welcome is the window shown without a repository.
type welcome struct {
	err error
}

func (v *welcome) view(c *ui.Context) {
	t := c.Theme()
	pal := paletteFor(t)
	c.Root().Background(pal.appBg)
	bar := c.TitleBar()
	ui.Box(c).Height(max(bar.Height, 40)).DragWindow().FillWidth()
	title := "Open a Git repository"
	detail := "Run diffscope from a Git repository in Terminal, or choose File → Open Folder… to open one. You can also drop a folder on this window."
	if v.err != nil && !errors.Is(v.err, git.ErrNotRepository) {
		title, detail = "Unable to read repository", errorText(v.err)
	} else if v.err != nil {
		title = "No Git repository found"
		detail = appName + " was opened outside a Git repository. Run " + cliName + " from inside a repository, or choose File → Open Folder… to open one. You can also drop a folder on this window."
	}
	ui.Scroll(c).Grow(1).Children(func() {
		ui.Column(c).FillWidth().Margin(0, ui.Auto).MaxWidth(568).Padding(24, 24, 24).Gap(16).Children(func() {
			emptyCard(c, pal, title, detail, func() {
				if ui.PrimaryButton(c, "Open Folder…").Clicked() {
					openFolder()
				}
			})
			welcomeRecent(c, t)
		})
	})
}

// welcomeRecent lists the repositories of File → Open Recent under the
// welcome panel, to open one without the menu.
func welcomeRecent(c *ui.Context, t *ui.Theme) {
	recent := state.recent()
	if len(recent) == 0 {
		return
	}
	ui.Column(c).Gap(2).Children(func() {
		ui.Text(c, "Recent repositories").FontSize(11).TextColor(t.TextMuted).Padding(0, 10, 4)
		for _, root := range recent {
			row := ui.Row(c).Height(30).Padding(0, 10).Gap(8).Radius(6).MinWidth(0)
			if row.Hovered() {
				row.Background(ui.RGBA(127, 127, 127, 0.08))
			}
			if row.Clicked() {
				go openRecent(root)
			}
			row.Children(func() {
				ui.Icon(c, iconFolder).FontSize(14).TextColor(t.TextMuted)
				ui.Text(c, abbreviateHome(root)).FontSize(13).SingleLine().Ellipsis("…").MinWidth(0).Grow(1)
			})
		}
	})
}
