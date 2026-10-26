package main

import (
	"errors"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"runtime/debug"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/egoist/mygo"
	"github.com/egoist/mygo/ui"
	"github.com/volcanicll/diffscope/internal/git"
	"github.com/volcanicll/diffscope/internal/github"
	"github.com/volcanicll/diffscope/internal/highlight"
)

// sourceKind is what a window reviews.
type sourceKind uint8

const (
	sourceWorkingTree sourceKind = iota // uncommitted changes
	sourceCommit                        // a commit, against its first parent
	sourceBranch                        // the work tree since it branched from a branch
	sourcePull                          // a pull request on GitHub, since it branched
)

type source struct {
	kind sourceKind
	ref  string // the commit, the branch, or the pull request as owner/repo#123
}

// What part of the work tree's changes the review shows.
const (
	stageAll      = iota // the index and the work tree against HEAD
	stageStaged          // the index against HEAD
	stageUnstaged        // the work tree against the index
)

// historyPage is how many commits the History tab loads at a time: git
// lists hundreds in a few milliseconds, and the list builds only the rows
// in view.
const historyPage = 200

// session is the review of one source, kept while the window shows
// another.
type session struct {
	comments []*comment
	viewed   map[string]string
}

// window is a window reviewing a repository.
type window struct {
	win      *mygo.Window
	repo     *git.Repo
	launch   source // what the window was opened on
	source   source
	settings Settings
	// stop ends the watch of the repository shown.
	stop chan struct{}

	branch     string
	files      []*fileState
	commit     *git.Commit // the commit reviewed, for sourceCommit
	base       string      // the merge base, for sourceBranch and sourcePull
	loading    bool
	loadedOnce bool
	loadErr    error
	gen        int // counts loads, to drop the results of older ones
	genA       atomic.Int64
	// loadStart is when the load started.
	loadStart time.Time
	viewed    map[string]string
	// loadStatus says what a slow load does, as fetching a pull request.
	loadStatus string

	// The sidebar.
	sidebarShown bool
	sidebarWidth float32
	tab          int // 0 files, 1 history
	filter       string
	filterFocus  bool
	treeList     ui.ListState
	treeSel      string // the key of the row chosen
	treeRows     []treeRow
	closedDirs   map[string]bool
	treeItems    map[string]*treeNode
	treeRoots    []string
	history      []git.Commit
	historyLimit int
	historyMore  bool
	historyList  ui.ListState
	commitTimes  map[string]commitTime
	// historyFilter filters the History tab, apart from the files'.
	historyFilter  string
	historyLoading bool
	// reloaded are the files that changed in the last refresh.
	reloaded  map[string]bool
	dragWidth float32

	// The diff surface.
	rows       []row
	rowsDirty  bool
	list       ui.ListState
	current    int // the file shown at the top of the surface
	hscroll    map[string]float32
	revealedAt time.Time
	now        time.Time
	charW      float32
	charKey    string
	// The hunk chosen with j and k.
	selFile, selHunk int
	focusList        bool
	focusedOnce      bool
	focusHistory     bool
	switchedAt       time.Time // when the source last changed, for DIFFSCOPE_DEBUG
	debugPressed     bool
	// hold keeps the background work of tests in held, to run frames
	// while it waits.
	hold bool
	held []func()
	// typing is set while a field has the focus, whose keys are its own.
	typing bool

	// Find in diffs.
	finding     bool
	query       string
	matches     []match
	match       int
	matchesFor  string
	fileMatches map[int]bool

	// The reviews of the other sources.
	sessions map[source]*session

	// GitHub: the repository's remote there, its open pull requests, and
	// the pull requests reviewed, the one shown in pr.
	gh           ghRemote
	pulls        []github.Summary
	pullsLoaded  bool
	pullsLoading bool
	pullsErr     error
	pullsList    ui.ListState
	pullsFilter  string
	pr           *pullState
	prs          map[source]*pullState

	// The groups of the files: by kind, or as an agent's review has them.
	groupPref groupMode
	groups    []fileGroup
	// stageMode is what part of the work tree's changes the review shows:
	// all of them, the staged ones, or the unstaged ones.
	stageMode int
	// branches are the local branches, for the switcher on the branch chip.
	branches []string
	// The repository browser: the files of the work tree, and the one open.
	repoFiles       []string
	repoTree        []browseRow
	repoItems       map[string]*browseNode
	repoList        ui.ListState
	repoFilter      string
	repoLoading     bool
	repoLoaded      bool
	repoErr         error
	browse          string
	browseList      ui.ListState
	browseLoading   bool
	browseBinary    bool
	browseTruncated bool
	browseLines     []string
	browseSegs      [][]highlight.Seg
	browseImage     *ui.Bitmap
	browseSVG       *ui.SVG
	browseSize      int // of the browsed picture, in bytes
	// The find bar of the file browser.
	browseFinding    bool
	browseQuery      string
	browseMatches    []browseMatch
	browseMatch      int
	browseMatchesFor string
	// notes are the notes of the agent's review, by path, and noteSel the
	// one gone to last.
	notes   map[string][]*aiNote
	noteSel *aiNote
	// regroupPending groups the files again as the next frame starts.
	regroupPending bool
	// placed are the threads and notes the rows show under their lines,
	// as the rows are built: the others show at the end of their files.
	placed map[any]bool
	// The agents' reviews, by source.
	analyses map[source]*analysisState
	// Review comments, the one asked about discarding, and the git user.
	comments   []*comment
	discarding *comment
	user       string
	copied     string
	copiedAt   time.Time

	// Committing.
	commitOpen     bool
	subject        string
	body           string
	commitPaths    map[string]bool
	commitBusy     bool
	commitErr      string
	commitOutput   string
	commitDone     string // the hash committed
	committedFiles int
	commitFocus    bool

	// The command bar.
	paletteOpen  bool
	paletteQuery string
	paletteRow   int
	paletteList  ui.ListState
	// palettePointer is where the pointer was over the list.
	palettePointer [2]float32

	// The dialog opening a commit or a branch.
	dialog      dialogKind
	dialogOpen  bool
	dialogValue string
	dialogErr   string
	dialogBusy  bool

	// changed is set when the work tree changed since the last load.
	changed   bool
	signature string
	help      bool
}

var (
	windowsMu sync.Mutex
	windows   []*window
)

// existingWindow returns the window reviewing a source of the repository
// at root, nil for none.
func existingWindow(root string, src source) *window {
	windowsMu.Lock()
	defer windowsMu.Unlock()
	for _, w := range windows {
		if w.repo.Root == root && w.launch == src {
			return w
		}
	}
	return nil
}

// openRepo opens the repository holding dir, with the source resolved:
// a commit's hash, a pull request's head.
func openRepo(dir string, src source) (*git.Repo, source, error) {
	repo, err := git.Open(dir)
	if err != nil {
		return nil, src, err
	}
	if src.kind == sourceCommit {
		hash, err := repo.Resolve(src.ref)
		if err != nil {
			return nil, src, err
		}
		src.ref = hash
	}
	if src.kind == sourcePull {
		ref, err := resolvePull(repo, src.ref)
		if err != nil {
			return nil, src, err
		}
		src.ref = ref
	}
	return repo, src, nil
}

// openWindow opens a window on the repository holding dir.
func openWindow(dir string, src source) error {
	return openWindowIn(dir, src, nil)
}

// openWindowIn opens the repository holding dir in a new window, or in
// the window given when it is not nil: that window closes once the new one
// shows, in its place and its size.
func openWindowIn(dir string, src source, into *window) error {
	repo, src, err := openRepo(dir, src)
	if err != nil {
		return err
	}
	// A repository already open comes to the front.
	if w := existingWindow(repo.Root, src); w != nil {
		w.win.Show()
		w.win.Focus()
		return nil
	}

	w := newWindow(repo, src)
	w.stop = make(chan struct{})
	// The window it replaces keeps the frame, the bounds and the state of
	// the window shown: the state key is the same.
	w.win = mygo.NewWindow(mygo.WindowOptions{
		Title:          windowTitle(repo.Root, src),
		Width:          1280,
		Height:         860,
		MinWidth:       720,
		MinHeight:      420,
		StateKey:       "main",
		TitleBarStyle:  mygo.TitleBarHiddenInset,
		TitleBarHeight: 52,
		Vibrancy:       mygo.VibrancySidebar,
		Content:        ui.View(w.view),
	})
	windowsMu.Lock()
	windows = append(windows, w)
	windowsMu.Unlock()
	w.win.OnClose(func(*mygo.CloseEvent) {
		if w.settings.CopyCommentsOnClose && len(w.comments) > 0 {
			mygo.Clipboard.WriteText(w.commentsMarkdown())
		}
	})
	offSettings := cfg.OnChange(func(s Settings) { w.win.Update(func() { w.applySettings(s) }) })
	w.win.OnClosed(func() {
		close(w.stop)
		offSettings()
		windowsMu.Lock()
		for i, o := range windows {
			if o == w {
				windows = append(windows[:i], windows[i+1:]...)
				break
			}
		}
		windowsMu.Unlock()
	})
	w.win.OnFocus(func() { go w.checkChanges() })
	// A folder dropped on a review window opens in place of the review it
	// shows: the window is the one the user aimed at. Its comments would go
	// with that review, so the window asks first when there are some.
	w.win.OnFileDrop(func(e *mygo.FileDropEvent) {
		for _, p := range e.Paths {
			if info, err := os.Stat(p); err != nil || !info.IsDir() {
				continue
			}
			go func(p string) {
				if len(w.comments) > 0 && !sameRepo(w, p) && askWhere(w) != openHere {
					return
				}
				if err := openWindowIn(p, source{kind: sourceWorkingTree}, w); err != nil {
					mygo.Dialog.Error("Could not open the folder", errorText(err))
				}
			}(p)
			return
		}
	})
	go noteRecent(repo.Root)
	w.load()
	w.loadHistory()
	w.loadUser()
	go w.watch(w.stop)
	w.captureIfAsked()
	if into != nil && into.win != nil && !into.win.IsDestroyed() {
		// The window it replaces goes once the new one shows: the app has
		// a window all along.
		w.win.Show()
		w.win.Focus()
		into.win.Close()
	}
	return nil
}

// newWindow makes the state of a window reviewing a source of a
// repository.
func newWindow(repo *git.Repo, src source) *window {
	width, shown := state.layout()
	w := &window{
		repo:         repo,
		launch:       src,
		source:       src,
		settings:     cfg.Get(),
		sidebarShown: shown,
		sidebarWidth: width,
		historyLimit: historyPage,
		viewed:       state.viewed(viewedKey(repo.Root, src)),
		hscroll:      map[string]float32{},
		closedDirs:   map[string]bool{},
		commitTimes:  map[string]commitTime{},
		reloaded:     map[string]bool{},
		fileMatches:  map[int]bool{},
		selFile:      -1,
		selHunk:      -1,
		prs:          map[source]*pullState{},
		analyses:     map[source]*analysisState{},
	}
	w.gh, _ = findGitHubRemote(repo, github.Repo{})
	if src.kind == sourcePull {
		w.pr = w.pullFor(src)
	}
	w.loadAnalysis(src)
	w.dragWidth = w.sidebarWidth
	w.list.Key = func(i int) any { return w.key(&w.rows[i]) }
	w.list.Header = func(i int) bool { return w.rows[i].kind == rowHeader }
	return w
}

// windowTitle is "<repository>[/<source>] · Diffscope".
func windowTitle(root string, src source) string {
	name := filepath.Base(root)
	switch src.kind {
	case sourceCommit:
		name += "/" + shortHash(src.ref)
	case sourceBranch:
		name += "/" + src.ref
	case sourcePull:
		_, n := src.pull()
		name += fmt.Sprintf("/#%d", n)
	}
	return name + " · Diffscope"
}

func shortHash(h string) string {
	if len(h) > 7 {
		return h[:7]
	}
	return h
}

// applySettings takes settings changed in the file or the menus.
func (w *window) applySettings(s Settings) {
	old := w.settings
	w.settings = s
	if old.ShowWhitespace != s.ShowWhitespace {
		w.load()
	}
	if old.DiffStyle != s.DiffStyle || old.WordWrap != s.WordWrap || old.CodeFontSize != s.CodeFontSize {
		w.rowsDirty = true
	}
}

// background runs fn on a goroutine of its own, or at once in tests,
// which have no window.
func (w *window) background(fn func()) {
	if w.win == nil {
		if w.hold {
			// Tests run frames while the work waits.
			w.held = append(w.held, fn)
			return
		}
		fn()
		return
	}
	go fn()
}

// update runs fn on the main thread, then draws a frame.
func (w *window) update(fn func()) {
	if w.win == nil {
		fn()
		return
	}
	if debugFrames {
		inner := fn
		fn = func() {
			start := time.Now()
			inner()
			if d := time.Since(start); d > 2*time.Millisecond {
				log.Printf("slow update: %v", d)
			}
		}
	}
	w.win.Update(fn)
}

func (w *window) invalidate() {
	if w.win != nil {
		w.win.Invalidate()
	}
}

func (w *window) split() bool { return w.settings.DiffStyle != "unified" }

// splitFile reports whether a file shows side by side: files that are
// all new or all gone show in one column.
func (w *window) splitFile(f *fileState) bool {
	if !w.split() {
		return false
	}
	return !f.oneSided()
}

// setSource shows another source in the window: at once, with what is
// known of it, as the commit's message from the history; its changes come
// as they load.
func (w *window) setSource(src source) {
	if w.source == src {
		return
	}
	if debugFrames {
		w.switchedAt = time.Now()
		log.Printf("switch to %+v", src)
		go probeMainThread(time.Second)
	}
	w.commitOpen = false
	w.browse = ""
	// The review takes the focus as the window opens only: the user, who
	// chose this source, keeps the focus where they put it, as on the
	// history.
	w.focusedOnce = true
	w.switchTo(src)
	if src.kind == sourceCommit {
		for i := range w.history {
			if w.history[i].Hash == src.ref {
				c := w.history[i]
				w.commit = &c
			}
		}
	}
	w.load()
}

// switchTo makes a source the one shown, its review in place of the
// other's.
func (w *window) switchTo(src source) {
	// Each source keeps its review: the comments, and what was viewed of a
	// commit.
	if w.sessions == nil {
		w.sessions = map[source]*session{}
	}
	w.sessions[w.source] = &session{comments: w.comments, viewed: w.viewed}
	w.comments, w.viewed = nil, nil
	if s := w.sessions[src]; s != nil {
		w.comments, w.viewed = s.comments, s.viewed
	}
	if w.viewed == nil {
		w.viewed = map[string]string{}
		if key := viewedKey(w.repo.Root, src); key != "" {
			w.viewed = state.viewed(key)
		}
	}
	w.source = src
	w.files, w.rows, w.commit, w.groups = nil, nil, nil, nil
	w.pr = nil
	if src.kind == sourcePull {
		w.pr = w.pullFor(src)
	}
	w.loadAnalysis(src)
	// The tree refers to the files by their index: it goes with them.
	w.buildTree()
	w.loadErr = nil
	w.list = ui.ListState{Key: w.list.Key, Header: w.list.Header}
	w.selFile, w.selHunk = -1, -1
	w.finding, w.query = false, ""
	w.hscroll = map[string]float32{}
	w.current = 0
	if w.win != nil {
		w.win.SetTitle(windowTitle(w.repo.Root, src))
	}
}

// isViewed reports whether the user marked the file viewed, as it is now.
func (w *window) isViewed(f *fileState) bool {
	return f.Fingerprint != "" && w.viewed[f.Path] == f.Fingerprint
}

func (w *window) setViewed(f *fileState, viewed bool) {
	fp := ""
	if viewed {
		fp = f.Fingerprint
		w.viewed[f.Path] = fp
	} else {
		delete(w.viewed, f.Path)
	}
	// Only the work tree's and pull requests' files are remembered:
	// commits do not change.
	if key := viewedKey(w.repo.Root, w.source); key != "" {
		go state.setViewed(key, f.Path, fp)
	}
	f.collapsed = viewed
	w.rowsDirty = true
}

// maxImage is the size of the pictures shown.
const maxImage = 32 << 20

// openInEditor opens a file of the repository in the user's editor, at a
// line.
func (w *window) openInEditor(path string, line int) {
	abs := filepath.Join(w.repo.Root, filepath.FromSlash(path))
	if _, err := os.Stat(abs); err != nil {
		abs = w.repo.Root
		line = 0
	}
	go func() {
		if err := openEditor(w.settings.EditorCommand, w.repo.Root, abs, line); err != nil {
			mygo.Dialog.Error("Could not open the file", err.Error())
		}
	}()
}

// abbreviateHome writes the home directory as ~.
func abbreviateHome(p string) string {
	home, err := os.UserHomeDir()
	if err == nil && (p == home || strings.HasPrefix(p, home+string(filepath.Separator))) {
		return "~" + p[len(home):]
	}
	return p
}

// shortPath abbreviates the directories of a path to their first letter,
// as fish does: ~/P/fate.
func shortPath(p string) string {
	p = abbreviateHome(p)
	parts := strings.Split(p, "/")
	for i := 1; i < len(parts)-1; i++ {
		part := parts[i]
		if part == "" || part == "~" {
			continue
		}
		n := 1
		if strings.HasPrefix(part, ".") && len(part) > 1 {
			n = 2
		}
		r := []rune(part)
		parts[i] = string(r[:min(n, len(r))])
	}
	return strings.Join(parts, "/")
}

// errorText is the text of an error for the window.
func errorText(err error) string {
	var ge *git.Error
	if errors.As(err, &ge) && strings.TrimSpace(ge.Stderr) != "" {
		return strings.TrimSpace(ge.Stderr)
	}
	return fmt.Sprint(err)
}

// probeMainThread logs how long the main thread kept a function waiting,
// at worst, over a while: how long the window could not respond.
func probeMainThread(d time.Duration) {
	var worst, total time.Duration
	var slow, n int
	for end := time.Now().Add(d); time.Now().Before(end); n++ {
		start := time.Now()
		mygo.RunOnMain(func() {})
		wait := time.Since(start)
		total += wait
		worst = max(worst, wait)
		if wait > 16*time.Millisecond {
			slow++
		}
		time.Sleep(2 * time.Millisecond)
	}
	log.Printf("main thread: worst wait %v, average %v, %d of %d waits over 16ms", worst, total/time.Duration(max(n, 1)), slow, n)
}

// watchMainThread logs, for DIFFSCOPE_DEBUG, whenever the main thread keeps
// a function waiting over 30ms: when the window cannot respond.
func watchMainThread() {
	limit := 30 * time.Millisecond
	if ms, err := strconv.Atoi(os.Getenv("DIFFSCOPE_DEBUG_BLOCK_MS")); err == nil {
		limit = time.Duration(ms) * time.Millisecond
	}
	var gc debug.GCStats
	for {
		start := time.Now()
		mygo.RunOnMain(func() {})
		if wait := time.Since(start); wait > limit {
			n := gc.NumGC
			debug.ReadGCStats(&gc)
			pause := time.Duration(0)
			if len(gc.Pause) > 0 {
				pause = gc.Pause[0]
			}
			log.Printf("main thread blocked %v (GC cycles since last: %d, last pause %v)", wait, gc.NumGC-n, pause)
		}
		time.Sleep(5 * time.Millisecond)
	}
}
