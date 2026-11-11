package main

import (
	"github.com/egoist/mygo/ui"
)

// railWidth is the width of the strip of modes down the left edge.
const railWidth = 44

// rail is the permanent navigation: Changes, History, Pulls, Repository,
// and the terminal at the bottom. The modes never move; the navigator and
// the document to their right change with them.
func (w *window) rail(c *ui.Context, pal *palette) {
	t := c.Theme()
	repoIdx := w.repoTabIndex()
	ui.Column(c).Width(railWidth).Shrink(0).Background(w.sidebarBg(t)).
		AlignItems(ui.Center).Padding(8, 0).Gap(4).
		BorderWidth(0, 1, 0, 0).BorderColor(pal.cardBorder).Label("Modes").Children(func() {
		w.railButton(c, pal, iconFileDiff, "Changes (⌘1)", w.tab == 0, func() { w.showMode(0) })
		w.railButton(c, pal, iconHistory, "History (⌘2)", w.tab == 1, func() { w.showMode(1) })
		if w.gh.ok() {
			w.railButton(c, pal, iconPull, "Pull requests (⌘3)", w.tab == 2, func() { w.showMode(2) })
		}
		w.railButton(c, pal, iconTree, "Repository (⌘4)", w.tab == repoIdx, func() { w.showMode(repoIdx) })
		ui.Spacer(c)
		w.railButton(c, pal, iconTerminal, "Terminal (Ctrl+`)", w.termShown, func() { w.toggleTerminal() })
	})
}

// railButton is one mode in the rail: a quiet icon until chosen, with a
// short accent rule at the edge of the rail when it is.
func (w *window) railButton(c *ui.Context, pal *palette, svg *ui.SVG, tip string, active bool, onClick func()) {
	t := c.Theme()
	b := ui.ButtonBase(c).Size(28, 28).Radius(7).Center().Label(tip).Tooltip(tip).TextColor(t.TextMuted)
	switch {
	case active:
		b.Background(t.Accent.Alpha(0.12)).TextColor(t.Accent)
	case b.Hovered():
		b.Background(pal.hover).TextColor(t.Text)
	}
	if b.Clicked() {
		onClick()
	}
	b.Children(func() {
		if active {
			// The button is centered in the 44 px rail: -8 puts this at
			// the rail's edge.
			ui.Box(c).Absolute().Left(-8).Top(5).Width(2).Height(18).Radius(1).Background(t.Accent)
		}
		ui.Icon(c, svg).FontSize(18)
	})
}

// showMode shows one of the navigator's modes. Changes returns to the work
// tree; the other modes keep the document that is shown until a row is
// chosen, so that browsing never loses the review.
func (w *window) showMode(tab int) {
	w.sidebarShown = true
	w.commitOpen = false
	if tab > w.repoTabIndex() {
		tab = 0
	}
	switch tab {
	case 0:
		w.tab = 0
		if w.source != w.launchWorkTree() {
			w.setSource(w.launchWorkTree())
		}
	case 1:
		w.tab = 1
	case 2:
		w.tab = 2
	default:
		w.tab = w.repoTabIndex()
		if !w.repoLoaded && !w.repoLoading {
			w.loadRepoFiles()
		}
	}
	w.invalidate()
}
