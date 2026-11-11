package main

import (
	"fmt"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/egoist/mygo/ui"
	"github.com/volcanicll/diffscope/internal/diff"
)

// toggleCommit shows the commit view, or the review again.
func (w *window) toggleCommit() {
	w.commitOpen = !w.commitOpen
	if w.commitOpen {
		w.commitDone = ""
		w.commitErr = ""
		w.commitFocus = true
		w.reconcileCommitPaths()
	}
}

// reconcileCommitPaths chooses the files new to the list, and forgets
// those gone from it.
func (w *window) reconcileCommitPaths() {
	if w.commitPaths == nil {
		w.commitPaths = map[string]bool{}
	}
	present := map[string]bool{}
	for _, f := range w.files {
		if f.Directory && strings.HasPrefix(f.Path, "Untracked files not shown") {
			continue
		}
		present[f.Path] = true
		if _, ok := w.commitPaths[f.Path]; !ok {
			w.commitPaths[f.Path] = true
		}
	}
	for p := range w.commitPaths {
		if !present[p] {
			delete(w.commitPaths, p)
		}
	}
}

// commitFiles are the files the commit view lists.
func (w *window) commitFiles() []*fileState {
	var out []*fileState
	for _, f := range w.files {
		if _, ok := w.commitPaths[f.Path]; ok {
			out = append(out, f)
		}
	}
	return out
}

// commitCan reports whether the dock can make a commit: a subject, at
// least one file, and no commit under way.
func (w *window) commitCan() bool {
	if w.commitBusy || w.commitDone != "" || strings.TrimSpace(w.subject) == "" {
		return false
	}
	for _, f := range w.commitFiles() {
		if w.commitPaths[f.Path] {
			return true
		}
	}
	return false
}

// commitDock composes a commit at the bottom of the navigator, while the
// document stays where it is. It is the one place a commit happens; it
// never replaces the diff.
func (w *window) commitDock(c *ui.Context) {
	t := c.Theme()
	pal := paletteFor(t)
	w.reconcileCommitPaths()
	files := w.commitFiles()
	chosen, adds, dels := 0, 0, 0
	for _, f := range files {
		if w.commitPaths[f.Path] {
			chosen++
		}
		adds += f.Additions
		dels += f.Deletions
	}
	can := w.commitCan()
	ui.Column(c).Key("commit-dock").Height(252).Shrink(0).Background(pal.appBg).
		BorderWidth(1, 0, 1, 0).BorderColor(pal.cardBorder).Children(func() {
		ui.Row(c).Height(28).Padding(0, 10).Gap(6).AlignItems(ui.Center).Children(func() {
			ui.Text(c, "Commit").FontSize(12).FontWeight(600)
			ui.RichText(c,
				ui.Span{Text: "+" + thousands(adds), Color: pal.addText},
				ui.Span{Text: " -" + thousands(dels), Color: pal.delText},
			).Font(w.codeFont()).FontSize(10.5)
			ui.Spacer(c)
			if iconButton(c, iconClose, "Close commit").Size(22, 22).Clicked() {
				w.toggleCommit()
			}
		})
		ui.Scroll(c).Grow(1).Children(func() {
			ui.Column(c).Padding(0, 10, 8).Gap(6).Children(func() {
				subj := ui.TextInputBase(c, &w.subject).Placeholder("Summary").Label("Subject").FontSize(13).
					Padding(6, 8).Radius(6).Background(pal.codeBg).Border(1, pal.cardBorder)
				if w.commitFocus {
					subj.Focus()
					w.commitFocus = false
				}
				if subj.Focused() {
					subj.Border(1, t.Accent.Alpha(0.6))
					w.typing = true
				}
				if subj.Submitted() && w.commitCan() {
					w.makeCommit()
				}
				if n := utf8.RuneCountInString(w.subject); n > 40 {
					counter := ui.Textf(c, "%d/50", n).Font(w.codeFont()).FontSize(9.5).AlignSelf(ui.End).TextColor(t.TextMuted)
					if n > 50 {
						counter.TextColor(pal.ref)
					}
				}
				body := ui.TextAreaBase(c, &w.body).Placeholder("Description (optional)").Label("Description").
					FontSize(12.5).MinHeight(56).Padding(6, 8).Radius(6).Background(pal.codeBg).Border(1, pal.cardBorder)
				if body.Focused() {
					body.Border(1, t.Accent.Alpha(0.6))
					w.typing = true
				}
				if body.Shortcut(ui.Cmd, ui.KeyEnter) && w.commitCan() {
					w.makeCommit()
				}
				ui.Row(c).Height(22).Gap(4).AlignItems(ui.Center).Children(func() {
					chev := iconButton(c, iconChevronRight, "Show the files")
					if w.commitListOpen {
						chev = iconButton(c, iconChevronDown, "Hide the files")
					}
					if chev.Clicked() {
						w.commitListOpen = !w.commitListOpen
					}
					ui.Textf(c, "%d of %d files", chosen, len(files)).FontSize(11).TextColor(t.TextMuted)
					ui.Spacer(c)
					label := "Select all"
					if chosen == len(files) && len(files) > 0 {
						label = "Clear all"
					}
					b := ui.ButtonBase(c).Children(func() {
						ui.Text(c, label).FontSize(10.5).TextColor(t.Accent)
					})
					if b.Clicked() {
						all := chosen == len(files) && len(files) > 0
						for _, f := range files {
							w.commitPaths[f.Path] = !all
						}
					}
				})
				if w.commitListOpen {
					ui.Column(c).Gap(1).Children(func() {
						for _, f := range files {
							on := w.commitPaths[f.Path]
							row := ui.ButtonBase(c.Key(f.Path)).Height(22).Padding(0, 4).Gap(5).Radius(4).Label(f.Path)
							if row.Hovered() {
								row.Background(pal.hover)
							}
							if row.Clicked() {
								w.commitPaths[f.Path] = !on
							}
							row.Children(func() {
								checkMark(c, pal, on, false)
								fileIcon(c, f, 12)
								ui.Text(c, f.Path).Font(w.codeFont()).FontSize(11).SingleLine().Ellipsis("…").
									Grow(1).Shrink(1).MinWidth(0).TextColor(t.Text)
								ui.Text(c, statusLetter(f)).Font(w.codeFont()).FontSize(9.5).FontWeight(700).
									TextColor(statusColor(f.Status, pal, t))
							})
						}
					})
				}
			})
		})
		ui.Row(c).Height(36).Padding(0, 10).Gap(8).AlignItems(ui.Center).
			BorderWidth(1, 0, 0, 0).BorderColor(pal.cardBorder).Children(func() {
			msg, color := "⌘↩ commits", t.TextMuted
			switch {
			case w.commitErr != "":
				msg, color = w.commitErr, t.Danger
			case w.commitDone != "":
				msg, color = "Committed "+shortHash(w.commitDone), pal.viewed
			case w.commitOutput != "":
				msg, color = "Commit failed — see the output", pal.ref
			}
			ui.Text(c, msg).FontSize(10.5).TextColor(color).SingleLine().Grow(1).Shrink(1).MinWidth(0).
				Tooltip(w.commitOutput)
			label := "Commit"
			switch {
			case w.commitBusy:
				label = "Committing…"
			case w.commitDone != "":
				label = "Committed"
			}
			b := ui.PrimaryButton(c, "").Height(24).Disabled(!can).Children(func() {
				ui.Text(c, label).FontSize(11.5).FontWeight(600)
			})
			if b.Clicked() && can {
				w.makeCommit()
			}
		})
	})
	if c.Shortcut(ui.Cmd, ui.KeyEnter) && w.commitCan() {
		w.makeCommit()
	}
}

// checkMark draws a check box: checked, mixed or empty.
func checkMark(c *ui.Context, pal *palette, on, mixed bool) {
	t := c.Theme()
	box := ui.Box(c).Size(16, 16).Radius(4).Center().Shrink(0)
	switch {
	case on:
		box.Background(t.Accent).Children(func() { ui.Icon(c, iconCheck).FontSize(11).TextColor(t.AccentText) })
	case mixed:
		box.Background(t.Accent).Children(func() { ui.Box(c).Size(8, 2).Radius(1).Background(t.AccentText) })
	default:
		box.Border(1.5, ui.RGBA(127, 127, 127, 0.45)).Background(pal.codeBg)
	}
}

func plural(n int, what string) string {
	if n == 1 {
		return "1 " + what
	}
	return fmt.Sprintf("%s %ss", thousands(n), what)
}

// makeCommit commits the files chosen.
func (w *window) makeCommit() {
	if w.commitBusy {
		return
	}
	var paths []string
	count := 0
	for _, f := range w.commitFiles() {
		if !w.commitPaths[f.Path] {
			continue
		}
		count++
		paths = append(paths, f.Path)
		if f.OldPath != f.Path && f.Status == diff.Renamed {
			paths = append(paths, f.OldPath)
		}
	}
	msg := strings.TrimSpace(w.subject) + "\n"
	if body := strings.TrimSpace(w.body); body != "" {
		msg = strings.TrimSpace(w.subject) + "\n\n" + body + "\n"
	}
	w.commitBusy = true
	w.commitErr = ""
	w.commitOutput = ""
	w.background(func() {
		hash, err := w.repo.CommitChanges(msg, paths)
		w.update(func() {
			w.commitBusy = false
			if err != nil {
				w.commitErr = "The commit failed — see the output above."
				w.commitOutput = errorText(err)
				return
			}
			w.commitDone = hash
			w.committedFiles = count
			w.subject, w.body = "", ""
			w.loadHistory()
			if w.win != nil {
				go w.checkChanges()
				// The dock confirms the commit for a moment, then folds
				// away; the document stays.
				go func() {
					time.Sleep(2 * time.Second)
					w.win.Update(func() {
						if w.commitDone != "" {
							w.commitOpen = false
							w.commitDone = ""
							w.rowsDirty = true
						}
					})
				}()
			} else {
				w.changed = true
			}
		})
	})
}
