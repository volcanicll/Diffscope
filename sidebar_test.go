package main

import (
	"image/color"
	"os"
	"path/filepath"
	"slices"
	"testing"
	"time"

	"github.com/egoist/mygo/ui"
	"github.com/volcanicll/diffscope/internal/github"
)

func TestFileTreeSelectsOnPressFromOutsideSidebar(t *testing.T) {
	w, _ := newTestWindow(t, testRepo(t))
	w.treeSel = "f:main.go"
	draft := ""
	var accent ui.Color
	tt := ui.NewTester(func(c *ui.Context) {
		accent = c.Theme().Accent
		ui.Column(c).Fill().Children(func() {
			ui.Text(c, "Selected "+w.treeSel)
			w.fileTree(c)
			ui.TextInput(c, &draft).Label("Editor")
		})
	}, 300, 500)
	if err := tt.Click("Editor"); err != nil {
		t.Fatal(err)
	}
	old, ok := tt.Find("main.go")
	if !ok {
		t.Fatal("the selected file row is missing")
	}
	r, ok := tt.Find("old.txt")
	if !ok {
		t.Fatal("the file row is missing")
	}
	tt.Press(r.X+r.W/2, r.Y+r.H/2)
	if w.treeSel != "f:old.txt" || !tt.HasText("Selected f:old.txt") {
		t.Fatalf("selected %q, frame %q", w.treeSel, tt.Texts())
	}
	if !tt.Focused("Changed files") {
		t.Fatal("the file tree did not take focus on press")
	}
	pixel := func(r ui.Rect) color.RGBA {
		return tt.Image().RGBAAt(int(r.X+r.W-4), int(r.Y+r.H/2))
	}
	want := color.RGBA{R: accent.R, G: accent.G, B: accent.B, A: 255}
	if pixel(old) == want || pixel(r) != want {
		t.Fatalf("old fill %v, new fill %v, want only the new file to have %v", pixel(old), pixel(r), want)
	}
	tt.Release(r.X+r.W/2, r.Y+r.H/2)
	if w.treeSel != "f:old.txt" {
		t.Fatal("release changed the selection again")
	}
}

func TestFileTreeDirectoryDisclosureWaitsForRelease(t *testing.T) {
	w, tt := newTestWindow(t, testRepo(t))
	r, ok := tt.Find("docs")
	if !ok {
		t.Fatal("the directory row is missing")
	}
	tt.Press(r.X+r.W/2, r.Y+r.H/2)
	if w.treeSel != "d:docs" || w.closedDirs["d:docs"] {
		t.Fatal("press should select the directory and keep it open")
	}
	tt.Release(r.X+r.W/2, r.Y+r.H/2)
	if !w.closedDirs["d:docs"] {
		t.Fatal("release did not close the directory")
	}
}

func TestHistorySelectsOnPressFromOutsideSidebar(t *testing.T) {
	w, tt := newTestWindow(t, testRepo(t))
	w.tab = 1
	tt.Frame()
	if err := tt.Click("Filter history"); err != nil {
		t.Fatal(err)
	}
	r, ok := tt.Find("First commit")
	if !ok {
		t.Fatal("the commit row is missing")
	}
	tt.Press(r.X+r.W/2, r.Y+r.H/2)
	if w.source.kind != sourceCommit || w.commit == nil || w.commit.Subject != "First commit" {
		t.Fatalf("press did not select the commit: %+v", w.source)
	}
	if !tt.Focused("History") {
		t.Fatal("the history did not take focus on press")
	}
	tt.Release(r.X+r.W/2, r.Y+r.H/2)
}

func TestFileTreeContextMenuCopiesPaths(t *testing.T) {
	dir := testRepo(t)
	w, tt := newTestWindow(t, dir)
	if err := tt.RightClick("main.go"); err != nil {
		t.Fatal(err)
	}
	want := []string{"Copy Relative Path", "Copy Absolute Path", "-", "Rename…", "Delete…"}
	if got := tt.Menu(); !slices.Equal(got, want) {
		t.Fatalf("menu %q, want %q", got, want)
	}
	if w.treeSel != "f:main.go" {
		t.Fatalf("the right-click chose %q, want the file", w.treeSel)
	}
	if err := tt.ChooseMenuItem("Copy Relative Path"); err != nil {
		t.Fatal(err)
	}
	if got := tt.Clipboard(); got != "main.go" {
		t.Fatalf("the clipboard holds %q, want the relative path", got)
	}
	if err := tt.RightClick("main.go"); err != nil {
		t.Fatal(err)
	}
	if err := tt.ChooseMenuItem("Copy Absolute Path"); err != nil {
		t.Fatal(err)
	}
	if got, want := tt.Clipboard(), filepath.Join(w.repo.Root, "main.go"); got != want {
		t.Fatalf("the clipboard holds %q, want %q", got, want)
	}
}

func TestFileTreeRenamesFile(t *testing.T) {
	dir := testRepo(t)
	w, tt := newTestWindow(t, dir)
	if err := tt.RightClick("main.go"); err != nil {
		t.Fatal(err)
	}
	if err := tt.ChooseMenuItem("Rename…"); err != nil {
		t.Fatal(err)
	}
	if !w.renameOpen || !tt.HasText("Rename File") {
		t.Fatalf("the rename dialog did not open: %v", tt.Texts())
	}
	w.renameValue = "renamed.go"
	tt.Frame()
	if err := tt.Click("Rename"); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(dir, "renamed.go")); err != nil {
		t.Fatalf("the new file is missing: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dir, "main.go")); !os.IsNotExist(err) {
		t.Fatal("the old file is still there")
	}
	if w.renameOpen {
		t.Fatal("the dialog stayed open")
	}
	if _, ok := tt.Find("renamed.go"); !ok {
		t.Fatalf("the tree does not show the new name: %v", tt.Texts())
	}
}

func TestFileTreeRenameRefusesAPathOutside(t *testing.T) {
	dir := testRepo(t)
	w, tt := newTestWindow(t, dir)
	if err := tt.RightClick("main.go"); err != nil {
		t.Fatal(err)
	}
	if err := tt.ChooseMenuItem("Rename…"); err != nil {
		t.Fatal(err)
	}
	w.renameValue = "../escaped.go"
	tt.Frame()
	if err := tt.Click("Rename"); err != nil {
		t.Fatal(err)
	}
	if !w.renameOpen || w.renameErr == "" {
		t.Fatal("the dialog should stay open with an error")
	}
	if !tt.HasText("Enter a path inside the repository.") {
		t.Fatalf("the error does not show: %v", tt.Texts())
	}
	if _, err := os.Stat(filepath.Join(filepath.Dir(dir), "escaped.go")); err == nil {
		t.Fatal("the file escaped the repository")
	}
}

func TestFileTreeDeletesFile(t *testing.T) {
	dir := testRepo(t)
	w, tt := newTestWindow(t, dir)
	if err := tt.RightClick("main.go"); err != nil {
		t.Fatal(err)
	}
	if err := tt.ChooseMenuItem("Delete…"); err != nil {
		t.Fatal(err)
	}
	if w.askingDelete != "main.go" {
		t.Fatalf("the alert is for %q, want main.go", w.askingDelete)
	}
	tt.Frame()
	if err := tt.Click("Delete"); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(dir, "main.go")); !os.IsNotExist(err) {
		t.Fatal("the file is still there")
	}
	if w.askingDelete != "" {
		t.Fatal("the alert stayed open")
	}
	// The file is still reviewed, as deleted.
	if _, ok := tt.Find("main.go"); !ok {
		t.Fatalf("the tree does not show the deleted file: %v", tt.Texts())
	}
}

func TestFileTreeDeletesUntrackedFile(t *testing.T) {
	dir := testRepo(t)
	_, tt := newTestWindow(t, dir)
	if err := tt.RightClick("new.go"); err != nil {
		t.Fatal(err)
	}
	if err := tt.ChooseMenuItem("Delete…"); err != nil {
		t.Fatal(err)
	}
	tt.Frame()
	if !tt.HasText("The file is untracked, so this cannot be undone.") {
		t.Fatalf("the alert does not say the file cannot come back: %v", tt.Texts())
	}
	if err := tt.Click("Delete"); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(dir, "src/new.go")); !os.IsNotExist(err) {
		t.Fatal("the file is still there")
	}
}

func TestFileTreeMenuDisablesEditsOnDeletedFile(t *testing.T) {
	// old.txt is gone from the work tree: there is nothing to rename or
	// delete.
	_, tt := newTestWindow(t, testRepo(t))
	if err := tt.RightClick("old.txt"); err != nil {
		t.Fatal(err)
	}
	if err := tt.ChooseMenuItem("Rename…"); err == nil {
		t.Fatal("renaming a deleted file was allowed")
	}
}

func TestSidebarSlidesClosedAndOpen(t *testing.T) {
	w, tt := newTestWindow(t, testRepo(t))
	if err := tt.Click("Hide navigator (⌘⇧B)"); err != nil {
		t.Fatal(err)
	}
	// The sidebar keeps its element while it slides, so the toolbar shows
	// its toggle only once the slide is over.
	time.Sleep(sidebarSlide + 60*time.Millisecond)
	tt.Frame()
	if w.sidebarNow != 0 {
		t.Fatalf("the sidebar still shows %v wide", w.sidebarNow)
	}
	if _, ok := tt.Find("Filter files"); ok {
		t.Fatal("the closed sidebar still builds its views")
	}
	if _, ok := tt.Find("Show navigator (⌘⇧B)"); !ok {
		t.Fatalf("the toolbar does not show the toggle: %v", tt.Texts())
	}
	// And back.
	if err := tt.Click("Show navigator (⌘⇧B)"); err != nil {
		t.Fatal(err)
	}
	time.Sleep(sidebarSlide + 60*time.Millisecond)
	tt.Frame()
	if w.sidebarNow != w.sidebarWidth {
		t.Fatalf("the sidebar opened to %v, not %v", w.sidebarNow, w.sidebarWidth)
	}
	if _, ok := tt.Find("Filter files"); !ok {
		t.Fatalf("the open sidebar does not build its views: %v", tt.Texts())
	}
}

func TestSidebarToggleStaysClearOfTheViews(t *testing.T) {
	w, tt := newTestWindow(t, testRepo(t))
	// The mode rail stays beside the navigator: the toggle is in the title
	// bar, above both, clear of the traffic lights and of the views.
	w.gh = ghRemote{repo: github.Repo{Owner: "diffscope", Name: "diffscope"}, remote: "origin"}
	w.sidebarWidth = sidebarMin
	tt.SetTitleBar(ui.TitleBar{Height: titleBarHeight, Left: 74})
	tt.Frame()
	r, ok := tt.Find("Hide navigator (⌘⇧B)")
	if !ok {
		t.Fatalf("the toggle is missing: %v", tt.Texts())
	}
	if r.Y >= titleBarHeight {
		t.Fatalf("the toggle is not in the title bar: %v", r)
	}
	for _, tip := range []string{"Changes (⌘1)", "History (⌘2)", "Pull requests (⌘3)", "Repository (⌘4)"} {
		if _, ok := tt.Find(tip); !ok {
			t.Fatalf("the %q mode is missing", tip)
		}
	}
	snapshot(t, tt, "sidebar-narrow")
}
