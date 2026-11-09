package main

import (
	"bytes"
	"errors"
	"fmt"
	"image"
	"image/color"
	"image/png"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/egoist/mygo/ui"
	"github.com/volcanicll/diffscope/internal/git"
)

func TestMain(m *testing.M) {
	// Keep the user's settings out of the tests, and the tests' out of them.
	dir, _ := os.MkdirTemp("", "diffscope-config")
	cfg.path = filepath.Join(dir, "diffscope.jsonc")
	cfg.settings = defaultSettings()
	code := m.Run()
	os.RemoveAll(dir)
	os.Exit(code)
}

func gitIn(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), "GIT_AUTHOR_NAME=Ada", "GIT_AUTHOR_EMAIL=ada@example.com", "GIT_COMMITTER_NAME=Ada", "GIT_COMMITTER_EMAIL=ada@example.com")
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
	return string(out)
}

func writeFile(t *testing.T, dir, path, content string) {
	t.Helper()
	full := filepath.Join(dir, path)
	os.MkdirAll(filepath.Dir(full), 0o755)
	if err := os.WriteFile(full, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

const mainGo = `package main

import "fmt"

func greet(name string) string {
	return "Hello, " + name
}

func main() {
	fmt.Println(greet("world"))
}
`

// testRepo makes a repository with a commit and changes on top of it.
func testRepo(t *testing.T) string {
	dir := t.TempDir()
	gitIn(t, dir, "init", "-q", "-b", "main")
	gitIn(t, dir, "config", "user.name", "Ada")
	gitIn(t, dir, "config", "user.email", "ada@example.com")
	var long strings.Builder
	for i := range 60 {
		long.WriteString("line ")
		long.WriteString(strings.Repeat("x", i%7))
		long.WriteString("\n")
	}
	writeFile(t, dir, "main.go", mainGo)
	writeFile(t, dir, "docs/long.txt", long.String())
	writeFile(t, dir, "old.txt", "bye\n")
	gitIn(t, dir, "add", ".")
	gitIn(t, dir, "commit", "-q", "-m", "First commit")

	writeFile(t, dir, "main.go", strings.Replace(mainGo, `"Hello, " + name`, `"Hi, " + name + "!"`, 1))
	writeFile(t, dir, "docs/long.txt", strings.Replace(long.String(), "line xxxxx\n", "line changed\n", 1))
	writeFile(t, dir, "src/new.go", "package src\n\n// New is new.\nfunc New() {}\n")
	os.Remove(filepath.Join(dir, "old.txt"))
	return dir
}

// newTestWindow opens a window's state on a repository, loaded.
func newTestWindow(t *testing.T, dir string) (*window, *ui.Tester) {
	t.Helper()
	repo, err := git.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	w := newWindow(repo, source{})
	w.settings = defaultSettings()
	w.sidebarShown, w.sidebarWidth = true, sidebarDefault
	w.load()
	w.loadHistory()
	w.loadUser()
	tt := ui.NewTester(w.view, 1280, 860)
	tt.Frame()
	return w, tt
}

// snapshot saves the tester's frame in $DIFFSCOPE_SNAPSHOTS, to look at.
func snapshot(t *testing.T, tt *ui.Tester, name string) {
	dir := os.Getenv("DIFFSCOPE_SNAPSHOTS")
	if dir == "" {
		return
	}
	f, err := os.Create(filepath.Join(dir, name+".png"))
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	png.Encode(f, tt.Image())
}

func TestReview(t *testing.T) {
	w, tt := newTestWindow(t, testRepo(t))
	if len(w.files) != 4 {
		t.Fatalf("files: %d", len(w.files))
	}
	for _, f := range w.files {
		if !f.loaded {
			t.Errorf("%s not loaded", f.Path)
		}
	}
	for _, want := range []string{"main.go", "new.go", "old.txt", "long.txt", "Total:"} {
		if !tt.HasText(want) {
			t.Errorf("no %q in %q", want, tt.Texts())
		}
	}
	snapshot(t, tt, "review")

	// The gap of unchanged lines shows them.
	if !tt.HasText("51 unmodified lines") {
		t.Fatalf("no gap in %q", tt.Texts())
	}
	if err := tt.Click("51 unmodified lines"); err != nil {
		t.Fatal(err)
	}
	if tt.HasText("51 unmodified lines") {
		t.Error("the gap did not expand")
	}

	// Viewed collapses a file.
	f := w.files[0]
	if err := tt.Click("Viewed"); err != nil {
		t.Fatal(err)
	}
	tt.Frame()
	if !w.isViewed(f) || !f.collapsed {
		t.Errorf("%s: viewed %v, collapsed %v", f.Path, w.isViewed(f), f.collapsed)
	}
}

func TestViewOptionsShortcut(t *testing.T) {
	w, tt := newTestWindow(t, testRepo(t))
	tt.Key(ui.Cmd|ui.Shift, ui.KeyV)
	tt.Frame()
	if !w.viewOptionsOpen {
		t.Fatal("the view options popover did not open")
	}
	if _, ok := tt.Find("Diff layout"); !ok {
		t.Fatalf("the popover did not build: %v", tt.Texts())
	}
	tt.Key(ui.Cmd|ui.Shift, ui.KeyV)
	tt.Frame()
	if w.viewOptionsOpen {
		t.Fatal("the view options popover did not close")
	}
}

func TestScopeMenuSwitchesStage(t *testing.T) {
	w, tt := newTestWindow(t, testRepo(t))
	if err := tt.Click("All changes"); err != nil {
		t.Fatal(err)
	}
	var staged string
	for _, item := range tt.Menu() {
		if strings.HasPrefix(item, "Staged changes") {
			staged = item
		}
	}
	if staged == "" {
		t.Fatalf("menu: %q", tt.Menu())
	}
	if err := tt.ChooseMenuItem(staged); err != nil {
		t.Fatal(err)
	}
	tt.Frame()
	if w.stageMode != stageStaged {
		t.Fatalf("stage mode %d", w.stageMode)
	}
	if !tt.HasText("Staged changes") {
		t.Fatalf("the chip does not show the scope: %v", tt.Texts())
	}
}

func TestMoreMenuBrowsesRepository(t *testing.T) {
	w, tt := newTestWindow(t, testRepo(t))
	if err := tt.Click("More"); err != nil {
		t.Fatal(err)
	}
	if got := tt.Menu(); !slices.Contains(got, "Browse Repository") {
		t.Fatalf("menu: %q", got)
	}
	if err := tt.ChooseMenuItem("Browse Repository"); err != nil {
		t.Fatal(err)
	}
	tt.Frame()
	if !w.sidebarShown || w.tab != w.repoTabIndex() {
		t.Fatalf("tab %d, shown %v", w.tab, w.sidebarShown)
	}
	if _, ok := tt.Find("Find a file"); !ok {
		t.Fatalf("the repository view is not shown: %v", tt.Texts())
	}
}

func TestUnified(t *testing.T) {
	w, tt := newTestWindow(t, testRepo(t))
	if err := tt.Click("View options (⌘⇧V)"); err != nil {
		t.Fatal(err)
	}
	tt.Frame()
	if err := tt.Click("Unified"); err != nil {
		t.Fatal(err)
	}
	tt.Frame()
	if w.split() {
		t.Error("still split")
	}
	snapshot(t, tt, "unified")
}

func TestComments(t *testing.T) {
	w, tt := newTestWindow(t, testRepo(t))
	// j chooses the first hunk, Enter comments on it.
	w.focusList = true
	tt.Frame()
	tt.Key(0, ui.KeyJ)
	tt.Key(0, ui.KeyEnter)
	tt.Frame()
	if len(w.comments) != 1 {
		t.Fatalf("comments: %d", len(w.comments))
	}
	tt.Type("Rename this.")
	tt.Key(ui.Cmd, ui.KeyEnter)
	tt.Frame()
	snapshot(t, tt, "comment")
	if !w.comments[0].pending() {
		t.Fatalf("comment %+v", w.comments[0])
	}
	md := w.commentsMarkdown()
	if !strings.HasPrefix(md, "# Address these Review Comments\n\n1. **docs/long.txt** (New line ") || !strings.Contains(md, "   Rename this.") || !strings.Contains(md, "   ```diff\n   @@ ") {
		t.Errorf("markdown:\n%s", md)
	}
}

func TestReviewShortcutsFromSidebar(t *testing.T) {
	w, tt := newTestWindow(t, testRepo(t))
	if err := tt.Click("old.txt"); err != nil {
		t.Fatal(err)
	}
	if !tt.Focused("Changed files") {
		t.Fatal("the sidebar did not take focus")
	}
	tt.Key(0, ui.KeyJ)
	tt.Key(0, ui.KeyEnter)
	if len(w.comments) != 1 {
		t.Fatalf("review shortcuts from the sidebar: %d comments", len(w.comments))
	}
}

func TestReviewListReturnsAfterEmptyView(t *testing.T) {
	for _, name := range []string{"filter", "load error", "commit"} {
		t.Run(name, func(t *testing.T) {
			w, tt := newTestWindow(t, testRepo(t))
			switch name {
			case "filter":
				w.filter = "no-such-file"
				w.buildTree()
				w.rowsDirty = true
			case "load error":
				w.loadErr = errors.New("unable to load changes")
			case "commit":
				w.commitOpen = true
			}
			// A request to focus the review waits until its list returns.
			w.focusList = true
			tt.Frame()
			tt.Frame()
			if _, ok := tt.Find("Changes"); ok || !w.focusList {
				t.Fatal("the hidden review was built or took focus")
			}
			if _, ok := tt.Find("Changed files"); name == "filter" && ok {
				t.Fatal("the empty file tree was built")
			}
			tt.Key(0, ui.KeyJ)
			tt.Key(0, ui.KeyEnter)
			if w.selHunk >= 0 || len(w.comments) != 0 {
				t.Fatal("review shortcuts ran while its list was hidden")
			}

			w.filter, w.loadErr, w.commitOpen = "", nil, false
			w.buildTree()
			w.rowsDirty = true
			tt.Frame()
			if !tt.Focused("Changes") || w.focusList {
				t.Fatal("the returning review did not take focus")
			}
			tt.Key(0, ui.KeyJ)
			tt.Key(0, ui.KeyEnter)
			if len(w.comments) != 1 {
				t.Fatalf("review shortcuts did not return: %d comments", len(w.comments))
			}
		})
	}
}

func TestFind(t *testing.T) {
	w, tt := newTestWindow(t, testRepo(t))
	w.finding = true
	tt.Frame()
	tt.Type("hi")
	tt.Frame()
	if len(w.matches) != 1 {
		t.Fatalf("matches %+v for %q", w.matches, w.query)
	}
	snapshot(t, tt, "find")
	if w.files[w.matches[0].file].Path != "main.go" {
		t.Errorf("match in %s", w.files[w.matches[0].file].Path)
	}
	// Only the files with a match show.
	if tt.HasText("new.go") {
		t.Error("new.go shows while finding")
	}
}

func TestCommit(t *testing.T) {
	dir := testRepo(t)
	w, tt := newTestWindow(t, dir)
	if err := tt.Click("Commit"); err != nil {
		t.Fatal(err)
	}
	tt.Frame()
	if !w.commitOpen {
		t.Fatal("commit view closed")
	}
	tt.Type("Greet louder")
	tt.Frame()
	snapshot(t, tt, "commit")
	// Leave new.go out.
	if err := tt.Click("src/new.go"); err != nil {
		t.Fatal(err)
	}
	tt.Key(ui.Cmd, ui.KeyEnter)
	tt.Frame()
	if w.commitDone == "" {
		t.Fatalf("not committed: %s %s", w.commitErr, w.commitOutput)
	}
	log := gitIn(t, dir, "log", "--format=%s", "-n", "1", "--name-status")
	if !strings.Contains(log, "Greet louder") || strings.Contains(log, "new.go") || !strings.Contains(log, "D\told.txt") {
		t.Errorf("log:\n%s", log)
	}
	if !strings.Contains(gitIn(t, dir, "status", "--porcelain"), "?? src/") {
		t.Error("new.go was committed")
	}
}

func TestHistory(t *testing.T) {
	w, tt := newTestWindow(t, testRepo(t))
	if err := tt.Click("History (⌘2)"); err != nil {
		t.Fatal(err)
	}
	tt.Frame()
	if !tt.HasText("Uncommitted changes") || !tt.HasText("First commit") {
		t.Fatalf("texts %q", tt.Texts())
	}
	if err := tt.Click("First commit"); err != nil {
		t.Fatal(err)
	}
	tt.Frame()
	tt.Frame()
	if w.source.kind != sourceCommit || len(w.files) != 3 {
		t.Fatalf("source %+v, %d files", w.source, len(w.files))
	}
	snapshot(t, tt, "history")
}

func TestHistoryTakesFocus(t *testing.T) {
	// A clean work tree: the review has no rows to take the focus as the
	// window opens, so the history shows and takes it.
	dir := testRepo(t)
	gitIn(t, dir, "add", ".")
	gitIn(t, dir, "commit", "-q", "-m", "Second commit")
	w, tt := newTestWindow(t, dir)
	if w.tab != 1 || !tt.Focused("History") {
		t.Fatalf("tab %d: the history did not take the focus", w.tab)
	}
	// Down goes to the commit under the uncommitted changes.
	tt.Key(0, ui.KeyDown)
	tt.Frame()
	if w.source.kind != sourceCommit || w.commit == nil || w.commit.Subject != "Second commit" {
		t.Fatalf("after Down: source %+v", w.source)
	}
	if err := tt.Click("First commit"); err != nil {
		t.Fatal(err)
	}
	tt.Frame()
	tt.Frame()
	if w.source.kind != sourceCommit || len(w.rows) == 0 {
		t.Fatalf("source %+v, %d rows", w.source, len(w.rows))
	}
	// The commit's rows came, but the history keeps the focus it took.
	if !tt.Focused("History") {
		t.Error("the history lost the focus")
	}
}

func TestHistoryArchive(t *testing.T) {
	// A commit with an archive in it: the file's icon was nil, which crashed
	// the window as it drew the file tree.
	dir := testRepo(t)
	blob := make([]byte, 4096)
	for i := range blob {
		blob[i] = byte(i * 7 % 251)
	}
	blob[0], blob[1] = 0x1f, 0x8b // a gzip header
	writeFile(t, dir, "dist/dist.tar.gz", string(blob))
	gitIn(t, dir, "add", ".")
	gitIn(t, dir, "commit", "-q", "-m", "Ship dist.tar.gz")
	hash := strings.TrimSpace(gitIn(t, dir, "rev-parse", "HEAD"))

	w, tt := newTestWindow(t, dir)
	w.setSource(source{kind: sourceCommit, ref: hash})
	tt.Frame()
	tt.Frame()
	if w.commit == nil || w.commit.Subject != "Ship dist.tar.gz" {
		t.Fatalf("commit %+v", w.commit)
	}
	var found bool
	for _, f := range w.files {
		if f.Path == "dist/dist.tar.gz" {
			found = true
		}
	}
	if !found {
		t.Fatalf("files %d, no dist.tar.gz", len(w.files))
	}
	if !tt.HasText("dist.tar.gz") || !tt.HasText("Binary file changed.") {
		t.Errorf("texts %q", tt.Texts())
	}
}

// TestSameRepo reports whether a window tells the repository it shows from
// another.
func TestSameRepo(t *testing.T) {
	dir := testRepo(t)
	other := testRepo(t)
	repo, err := git.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	w := newWindow(repo, source{})
	if !sameRepo(w, dir) {
		t.Error("the repository it shows is another")
	}
	if sameRepo(w, other) {
		t.Error("another repository is the one it shows")
	}
	if !sameRepo(w, filepath.Join(dir, "docs")) {
		t.Error("a folder of it is another repository")
	}
}

func TestPalette(t *testing.T) {
	cfg.Update(func(s *Settings) { s.DiffStyle = "split" })
	w, tt := newTestWindow(t, testRepo(t))
	w.paletteOpen = true
	tt.Frame()
	tt.Type("toggle diff layout")
	tt.Frame()
	snapshot(t, tt, "palette")
	tt.Key(0, ui.KeyEnter)
	tt.Frame()
	if w.paletteOpen || cfg.Get().DiffStyle != "unified" {
		t.Errorf("open %v, style %s", w.paletteOpen, cfg.Get().DiffStyle)
	}
	cfg.Update(func(s *Settings) { s.DiffStyle = "split" })
}

func TestTree(t *testing.T) {
	w, tt := newTestWindow(t, testRepo(t))
	// Clicking a file in the tree scrolls the review to it.
	box, ok := tt.Find("old.txt")
	if !ok {
		t.Fatal("no old.txt")
	}
	tt.ClickAt(box.X+4, box.Y+box.H/2)
	tt.Frame()
	if w.files[w.current].Path != "old.txt" || w.treeSel != "f:old.txt" {
		t.Fatalf("current %s, chosen %q", w.files[w.current].Path, w.treeSel)
	}
	if !tt.Focused("Changed files") {
		t.Error("the tree did not take the focus")
	}
	// Up goes to the file above.
	tt.Key(0, ui.KeyUp)
	tt.Frame()
	if w.files[w.current].Path != "main.go" {
		t.Errorf("after Up: %s", w.files[w.current].Path)
	}
	snapshot(t, tt, "tree")
	// Left on a directory closes it.
	w.treeSel = "d:docs"
	tt.Key(0, ui.KeyLeft)
	tt.Frame()
	if !w.closedDirs["d:docs"] || len(w.visibleTree()) != 5 {
		t.Errorf("docs not closed: %v", w.visibleTree())
	}
}

func TestDark(t *testing.T) {
	w, tt := newTestWindow(t, testRepo(t))
	tt.SetDark(true)
	w.nextHunk(1)
	tt.Frame()
	snapshot(t, tt, "dark")
}

func TestWelcomeRecent(t *testing.T) {
	state.mu.Lock()
	old := state.data.Recent
	state.data.Recent = []string{t.TempDir(), t.TempDir()}
	state.mu.Unlock()
	t.Cleanup(func() {
		state.mu.Lock()
		state.data.Recent = old
		state.mu.Unlock()
	})
	tt := ui.NewTester((&welcome{}).view, 640, 420)
	tt.Frame()
	if !tt.HasText("Recent repositories") {
		t.Errorf("no recents in %q", tt.Texts())
	}
	if !tt.HasText(state.data.Recent[0]) {
		t.Errorf("no %q in %q", state.data.Recent[0], tt.Texts())
	}
	snapshot(t, tt, "welcome-recent")

	// Without recents the list is absent.
	state.mu.Lock()
	state.data.Recent = nil
	state.mu.Unlock()
	tt.Frame()
	if tt.HasText("Recent repositories") {
		t.Error("recents shown without any")
	}
}

// TestWelcomeText checks the welcome panel's words are built, not left as
// the literals of a concatenation.
func TestWelcomeText(t *testing.T) {
	state.mu.Lock()
	old := state.data.Recent
	state.data.Recent = nil
	state.mu.Unlock()
	t.Cleanup(func() {
		state.mu.Lock()
		state.data.Recent = old
		state.mu.Unlock()
	})

	tt := ui.NewTester((&welcome{err: git.ErrNotRepository}).view, 640, 420)
	tt.Frame()
	texts := strings.Join(tt.Texts(), "\n")
	if !strings.Contains(texts, appName+" was opened outside a Git repository") {
		t.Errorf("no %q in %q", appName+" was opened outside", texts)
	}
	if strings.Contains(texts, "' + ") {
		t.Errorf("unbuilt concatenation in %q", texts)
	}
}

// TestWelcomeRecentMany checks a list longer than the window scrolls
// rather than drawing over the panel.
func TestWelcomeRecentMany(t *testing.T) {
	state.mu.Lock()
	old := state.data.Recent
	recent := make([]string, 0, maxRecent)
	for i := range maxRecent {
		recent = append(recent, fmt.Sprintf("/repo/%d", i))
	}
	state.data.Recent = recent
	state.mu.Unlock()
	t.Cleanup(func() {
		state.mu.Lock()
		state.data.Recent = old
		state.mu.Unlock()
	})

	tt := ui.NewTester((&welcome{err: git.ErrNotRepository}).view, 640, 420)
	tt.Frame()
	if !tt.HasText("Recent repositories") {
		t.Fatalf("no recents in %q", tt.Texts())
	}
	snapshot(t, tt, "welcome-recent-many")
}

func TestHorizontalScroll(t *testing.T) {
	dir := testRepo(t)
	writeFile(t, dir, "main.go", strings.Replace(mainGo, `"Hello, " + name`, `"Hello, " + name + "`+strings.Repeat("long ", 80)+`"`, 1))
	w, tt := newTestWindow(t, dir)
	box, ok := tt.Find("package main")
	if !ok {
		t.Fatal("no main.go")
	}
	tt.Scroll(box.X+40, box.Y+box.H/2, 120, 0)
	tt.Frame()
	if w.hscroll["main.go"] <= 0 {
		t.Errorf("hscroll %v", w.hscroll)
	}
	tt.Scroll(box.X+40, box.Y+box.H/2, 0, 40)
	tt.Frame()
	if w.hscroll["main.go"] <= 0 {
		t.Errorf("vertical scroll changed hscroll %v", w.hscroll)
	}
	snapshot(t, tt, "hscroll")
}

func pngOf(t *testing.T, w, h int, c color.Color) string {
	img := image.NewNRGBA(image.Rect(0, 0, w, h))
	for y := range h {
		for x := range w {
			if (x/8+y/8)%2 == 0 {
				img.Set(x, y, c)
			}
		}
	}
	var b bytes.Buffer
	png.Encode(&b, img)
	return b.String()
}

func TestImage(t *testing.T) {
	dir := testRepo(t)
	writeFile(t, dir, "logo.png", pngOf(t, 64, 48, color.NRGBA{200, 40, 40, 255}))
	gitIn(t, dir, "add", "logo.png")
	gitIn(t, dir, "commit", "-q", "-m", "Add a logo")
	writeFile(t, dir, "logo.png", pngOf(t, 96, 48, color.NRGBA{40, 90, 220, 255}))
	w, tt := newTestWindow(t, dir)
	var logo *fileState
	for _, f := range w.files {
		if f.Path == "logo.png" {
			logo = f
		}
	}
	if logo == nil || logo.oldImage == nil || logo.newImage == nil {
		t.Fatalf("logo %+v", logo)
	}
	w.revealFile(slices.Index(w.files, logo))
	tt.Frame()
	if !tt.HasText("New · 96×48 · " + formatBytes(logo.newSize)) {
		t.Errorf("texts %q", tt.Texts())
	}
	snapshot(t, tt, "image")
}

func TestParseArgs(t *testing.T) {
	dir := testRepo(t)
	gitIn(t, dir, "branch", "feature")
	for _, tc := range []struct {
		args []string
		want source
		dir  string
	}{
		{nil, source{}, dir},
		{[]string{"HEAD"}, source{kind: sourceCommit, ref: "HEAD"}, dir},
		{[]string{"feature"}, source{kind: sourceBranch, ref: "feature"}, dir},
		{[]string{"--commit", "main"}, source{kind: sourceCommit, ref: "main"}, dir},
		{[]string{"docs"}, source{}, filepath.Join(dir, "docs")},
		{[]string{"HEAD~0", "./docs"}, source{kind: sourceCommit, ref: "HEAD~0"}, filepath.Join(dir, "docs")},
	} {
		req, err := parseArgs(tc.args, dir)
		if err != nil {
			t.Errorf("%v: %v", tc.args, err)
			continue
		}
		if req.src != tc.want || req.dir != tc.dir {
			t.Errorf("%v: got %+v in %s", tc.args, req.src, req.dir)
		}
	}
	if _, err := parseArgs([]string{"nonexistent-ref"}, dir); err == nil {
		t.Error("no error for an unknown ref")
	}
}

func TestBranchCompare(t *testing.T) {
	dir := testRepo(t)
	gitIn(t, dir, "add", "-A")
	gitIn(t, dir, "commit", "-q", "-m", "Second")
	writeFile(t, dir, "local.txt", "uncommitted\n")
	repo, err := git.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	first := strings.TrimSpace(gitIn(t, dir, "rev-list", "--max-parents=0", "HEAD"))
	gitIn(t, dir, "branch", "base", first)
	w := newWindow(repo, source{kind: sourceBranch, ref: "base"})
	w.settings = defaultSettings()
	w.load()
	paths := map[string]bool{}
	for _, f := range w.files {
		paths[f.Path] = true
	}
	// The commit on top of base and the uncommitted file.
	for _, p := range []string{"main.go", "src/new.go", "old.txt", "local.txt"} {
		if !paths[p] {
			t.Errorf("no %s in %v", p, paths)
		}
	}
	tt := ui.NewTester(w.view, 1280, 860)
	tt.Frame()
	if !tt.HasText("vs base") {
		t.Errorf("texts %q", tt.Texts())
	}
}

func TestFileIcons(t *testing.T) {
	dir := testRepo(t)
	for _, p := range []string{"web/App.tsx", "web/api.ts", "web/site.css", "package.json", "Dockerfile", ".gitignore", "README.md", "ci.yml", "lib.rs", "tool.py"} {
		writeFile(t, dir, p, "x\n")
	}
	w, tt := newTestWindow(t, dir)
	tt.SetDark(true)
	tt.Frame()
	snapshot(t, tt, "file-icons")
	want := map[string]string{"web/App.tsx": "react", "web/api.ts": "typescript", "Dockerfile": "docker", ".gitignore": "git", "main.go": "go", "docs/long.txt": "text"}
	for _, f := range w.files {
		if token, ok := want[f.Path]; ok && (f.kind == nil || f.kind.Token != token) {
			t.Errorf("%s: icon %+v, want %s", f.Path, f.kind, token)
		}
	}
}

func TestBrowseFind(t *testing.T) {
	w, tt := newTestWindow(t, testRepo(t))
	w.showBrowse("main.go")
	tt.Frame()
	if w.browseLoading || len(w.browseLines) == 0 {
		t.Fatalf("browse not loaded: %d lines", len(w.browseLines))
	}
	w.browseFinding = true
	tt.Frame()
	tt.Type("greet")
	tt.Frame()
	// "greet" is on two lines: the definition and the call in main.
	if len(w.browseMatches) != 2 {
		t.Fatalf("matches %d for %q", len(w.browseMatches), w.browseQuery)
	}
	line := int(w.browseMatches[w.browseMatch].line)
	marks := w.browseMarks(line, &lightPalette)
	if len(marks) != 1 || w.browseLines[line][marks[0].Start:marks[0].End] != "greet" {
		t.Errorf("marks %+v on %q", marks, w.browseLines[line])
	}
	// Other lines show theirs dimmed, not as the current match.
	w.showBrowseMatch()
	if w.browseMatch != 0 {
		t.Errorf("match %d", w.browseMatch)
	}
	// Closing the find clears the query and the matches.
	w.browseFinding = false
	w.updateBrowseMatches()
	if w.browseQuery != "" || w.browseMatches != nil {
		t.Errorf("find left open: %q, %d matches", w.browseQuery, len(w.browseMatches))
	}
}

// TestBrowsePlainFile browses a file no lexer knows, as go.sum: it has no
// segments, and showing its lines must not index them.
func TestBrowsePlainFile(t *testing.T) {
	dir := testRepo(t)
	writeFile(t, dir, "go.sum", "golang.org/x/text v0.43.0 h1:abc=\n")
	w, tt := newTestWindow(t, dir)
	w.showBrowse("go.sum")
	tt.Frame()
	if w.browseLoading || len(w.browseLines) != 1 || w.browseSegs != nil {
		t.Fatalf("browse: %d lines, %d segment lists, loading %v", len(w.browseLines), len(w.browseSegs), w.browseLoading)
	}
}

func TestBrowseImage(t *testing.T) {
	dir := testRepo(t)
	writeFile(t, dir, "logo.png", pngOf(t, 64, 48, color.NRGBA{200, 40, 40, 255}))
	w, tt := newTestWindow(t, dir)
	w.showBrowse("logo.png")
	tt.Frame()
	if w.browseLoading {
		t.Fatal("browse still loading")
	}
	if w.browseImage == nil || w.browseBinary {
		t.Fatalf("no picture: %+v, binary %v", w.browseImage, w.browseBinary)
	}
	tt.Frame()
	iw, ih := w.browseImage.Size()
	want := fmt.Sprintf("%d×%d · %s", iw, ih, formatBytes(w.browseSize))
	if !tt.HasText(want) {
		t.Errorf("no %q in %q", want, tt.Texts())
	}
	snapshot(t, tt, "browse-image")

	// An SVG shows in its own colors, at its viewBox size.
	writeFile(t, dir, "banner.svg", `<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 120 60"><rect width="120" height="60" fill="#33691e"/></svg>`)
	w.showBrowse("banner.svg")
	tt.Frame()
	if w.browseSVG == nil || w.browseImage != nil || w.browseBinary {
		t.Fatalf("no svg: %+v, binary %v", w.browseSVG, w.browseBinary)
	}
	tt.Frame()
	sw, sh := w.browseSVG.Size()
	want = fmt.Sprintf("%d×%d · %s", int(sw), int(sh), formatBytes(w.browseSize))
	if !tt.HasText(want) {
		t.Errorf("no %q in %q", want, tt.Texts())
	}
	snapshot(t, tt, "browse-svg")

	// An SVG that does not parse falls back to its text.
	writeFile(t, dir, "bad.svg", `<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0`)
	w.showBrowse("bad.svg")
	tt.Frame()
	if w.browseSVG != nil || w.browseBinary || len(w.browseLines) == 0 {
		t.Fatalf("bad svg: svg %v, binary %v, %d lines", w.browseSVG, w.browseBinary, len(w.browseLines))
	}
}

func TestRepoTree(t *testing.T) {
	w, _ := newTestWindow(t, testRepo(t))
	// The directory a holds files after its subtree, which once indexed
	// the files of every directory with one counter.
	w.repoFiles = []string{"a/b/c.txt", "a/b/d.txt", "a/e.txt", "f.go", "g.md"}
	w.buildRepoTree()
	var names []string
	for _, r := range w.repoTree {
		names = append(names, w.repoItems[r.key].name)
	}
	want := []string{"a", "b", "c.txt", "d.txt", "e.txt", "f.go", "g.md"}
	if !slices.Equal(names, want) {
		t.Errorf("rows %q, want %q", names, want)
	}
	// The filter finds files in closed directories, with their dirs.
	w.repoFilter = "d.txt"
	w.buildRepoTree()
	names = nil
	for _, r := range w.repoTree {
		names = append(names, w.repoItems[r.key].name)
	}
	if want := []string{"a", "b", "d.txt"}; !slices.Equal(names, want) {
		t.Errorf("filtered rows %q, want %q", names, want)
	}
}
