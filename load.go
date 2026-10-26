package main

import (
	"path/filepath"
	"runtime"
	"runtime/debug"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/egoist/mygo"
	"github.com/egoist/mygo/ui"
	"github.com/volcanicll/diffscope/internal/diff"
	"github.com/volcanicll/diffscope/internal/git"
	"github.com/volcanicll/diffscope/internal/highlight"
)

// preloadBudget is how long after it starts a load may wait for the
// contents of the first files, to show them colored at once rather than
// as they come.
const preloadBudget = 80 * time.Millisecond

// load reads the changes of the source, the contents of the first files
// within preloadBudget, then the contents of the others.
func (w *window) load() {
	w.gen++
	gen := w.gen
	src := w.source
	opts := git.Options{ShowWhitespace: w.settings.ShowWhitespace}
	w.loading = true
	if w.repoLoaded {
		w.loadRepoFiles()
	}
	w.loadStart = time.Now()
	started := w.loadStart
	w.loadErr = nil
	w.changed = false
	w.genA.Store(int64(gen))
	// What is known already needs no git: the commit, from the history, and
	// the branch, which showing a commit does not change.
	known := w.commit
	if known != nil && known.Hash != src.ref {
		known = nil
	}
	branch := w.branch
	needBranch := src.kind != sourceCommit || branch == ""
	remote := w.pullRemote(src)
	w.loadStatus = ""
	// Files unchanged since the last load keep their contents.
	loadedFiles := map[string]bool{}
	for _, f := range w.files {
		if f.loaded {
			loadedFiles[f.Path+"\x00"+f.Fingerprint] = true
		}
	}
	w.background(func() {
		var (
			files  []*diff.File
			commit = known
			base   string
			pulled pulledRequest
			local  []string
			err    error
			wg     sync.WaitGroup
		)
		if needBranch {
			wg.Add(1)
			go func() {
				defer wg.Done()
				branch = w.repo.Branch()
				if src.kind == sourceWorkingTree {
					branches, _ := w.repo.Branches()
					local = branches
				}
			}()
		}
		sig := ""
		switch src.kind {
		case sourceWorkingTree:
			sig = w.repo.StatusSignature()
			switch w.stageMode {
			case stageStaged:
				files, err = w.repo.Staged(opts)
			case stageUnstaged:
				files, err = w.repo.Unstaged(opts)
			default:
				files, err = w.repo.WorkingTree(opts)
			}
		case sourceCommit:
			if commit == nil {
				var c git.Commit
				c, err = w.repo.CommitInfo(src.ref)
				commit = &c
			}
			if err == nil {
				files, err = w.repo.CommitDiff(*commit, opts)
			}
		case sourceBranch:
			sig = w.repo.StatusSignature()
			files, base, err = w.repo.Compare(src.ref, opts)
		case sourcePull:
			pulled, err = w.fetchPull(gen, src, remote, opts)
			files, base = pulled.files, pulled.mergeBase
		}
		wg.Wait()
		// The revisions of the old and the new side; "" is the work tree.
		var oldRev, newRev string
		switch src.kind {
		case sourceWorkingTree:
			switch w.stageMode {
			case stageStaged: // the index against HEAD
				oldRev, newRev = "HEAD", git.IndexRev
			case stageUnstaged: // the work tree against the index
				oldRev = git.IndexRev
			default:
				if w.repo.HasHead() {
					oldRev = "HEAD"
				}
			}
		case sourceCommit:
			if commit != nil && len(commit.Parents) > 0 {
				oldRev = commit.Parents[0]
			}
			newRev = src.ref
		case sourceBranch:
			oldRev = base
		case sourcePull:
			oldRev, newRev = base, pulled.head
		}
		var pre map[string]loaded
		if err == nil && !w.stale(gen) {
			var fresh []*diff.File
			for _, f := range files {
				if !loadedFiles[f.Path+"\x00"+f.Fingerprint] {
					fresh = append(fresh, f)
				}
			}
			if budget := preloadBudget - time.Since(started); budget > 0 {
				pre = w.preload(fresh, oldRev, newRev, budget)
			}
		}
		w.update(func() {
			if gen != w.gen {
				return
			}
			w.loading = false
			w.loadErr = err
			w.loadStatus = ""
			w.branch = branch
			if src.kind == sourceWorkingTree {
				w.branches = local
			}
			if err == nil {
				w.commit = commit
			}
			if src.kind == sourcePull && err == nil {
				w.pr.apply(pulled)
				w.loadReviews()
			}
			w.base = base
			if sig != "" {
				w.signature = sig
			}
			w.setFiles(files)
			for _, f := range w.files {
				if l, ok := pre[f.Path]; ok && !f.loaded {
					l.file = f
					l.apply()
				}
			}
			if !w.loadedOnce && len(files) == 0 && src.kind != sourceCommit && src.kind != sourcePull {
				// Nothing to review: the history shows instead, and takes
				// the keys in place of the review.
				w.tab = 1
				w.focusHistory = w.sidebarShown
				w.focusedOnce = true
			}
			w.loadedOnce = true
			if err == nil {
				var rest []*fileState
				for _, f := range w.files {
					if !f.loaded {
						rest = append(rest, f)
					}
				}
				if len(rest) > 0 {
					w.background(func() { w.loadContents(gen, oldRev, newRev, rest) })
				}
			}
		})
	})
}
func (w *window) preload(files []*diff.File, oldRev, newRev string, budget time.Duration) map[string]loaded {
	if len(files) == 0 {
		return nil
	}
	contents, err := w.repo.NewContents()
	if err != nil {
		return nil
	}
	defer boostGC()()
	results := make(chan loaded)
	var stop atomic.Bool
	go func() {
		defer close(results)
		defer contents.Close()
		for _, f := range files {
			if stop.Load() {
				return
			}
			l := w.read(contents, f, oldRev, newRev)
			l.finish()
			select {
			case results <- l:
			case <-time.After(budget):
				return // nobody waits anymore
			}
		}
	}()
	out := map[string]loaded{}
	deadline := time.NewTimer(budget)
	defer deadline.Stop()
	for {
		select {
		case l, ok := <-results:
			if !ok {
				return out
			}
			out[l.path] = l
		case <-deadline.C:
			stop.Store(true)
			return out
		}
	}
}
func (w *window) setFiles(files []*diff.File) {
	prev := map[string]*fileState{}
	for _, f := range w.files {
		prev[f.Path] = f
	}
	// A refresh marks the files that changed since, and opens them.
	refresh := len(prev) > 0
	w.reloaded = map[string]bool{}
	next := make([]*fileState, 0, len(files))
	for _, f := range files {
		fs := &fileState{File: f}
		switch p := prev[f.Path]; {
		case p != nil && p.Fingerprint == f.Fingerprint:
			fs.collapsed = p.collapsed
			fs.expanded = p.expanded
			// The contents stay until they load again.
			fs.oldLines, fs.newLines, fs.oldHL, fs.newHL = p.oldLines, p.newLines, p.oldHL, p.newHL
			fs.oldImage, fs.newImage, fs.oldSize, fs.newSize = p.oldImage, p.newImage, p.oldSize, p.newSize
			fs.loaded = p.loaded
		case refresh:
			w.reloaded[f.Path] = true
			fs.collapsed = f.Generated || f.Directory
		default:
			fs.collapsed = w.isViewed(fs) || f.Generated || f.Directory
		}
		next = append(next, fs)
	}
	w.files = next
	w.regroup()
	w.pruneComments()
}

// maxContent is the size of the files whose contents are loaded.
const maxContent = 2 << 20

type loaded struct {
	file               *fileState // where they go, once known
	oldLines, newLines []string
	oldHL, newHL       [][]highlight.Seg
	oldData, newData   []byte // of pictures
	oldImage, newImage *ui.Bitmap
	path, oldPath      string
}

func (w *window) read(contents *git.Contents, f *diff.File, oldRev, newRev string) loaded {
	l := loaded{path: f.Path, oldPath: f.OldPath}
	raw := func(rev, path string, limit int) []byte {
		var data []byte
		if rev == "" {
			data = w.repo.ReadWorkTree(path)
		} else {
			data, _ = contents.Read(rev, path)
		}
		if len(data) > limit {
			return nil
		}
		return data
	}
	hasOld := oldRev != "" && f.Status != diff.Added && f.Status != diff.Untracked
	hasNew := f.Status != diff.Deleted
	if f.Binary && isImage(f.Path) {
		if hasOld {
			l.oldData = raw(oldRev, f.OldPath, maxImage)
		}
		if hasNew {
			l.newData = raw(newRev, f.Path, maxImage)
		}
		return l
	}
	if f.Binary || f.Directory || f.TooLarge {
		return l
	}
	text := func(data []byte) []string {
		if data == nil || diff.IsBinary(data) {
			return nil
		}
		lines := diff.SplitLines(string(data))
		if lines == nil {
			lines = []string{}
		}
		return lines
	}
	if hasOld {
		l.oldLines = text(raw(oldRev, f.OldPath, maxContent))
	}
	if hasNew {
		l.newLines = text(raw(newRev, f.Path, maxContent))
	}
	return l
}
func (l *loaded) finish() {
	l.oldHL = highlight.Lines(l.oldPath, l.oldLines)
	l.newHL = highlight.Lines(l.path, l.newLines)
	if l.oldData != nil {
		l.oldImage, _ = ui.DecodeBitmap(l.oldData)
	}
	if l.newData != nil {
		l.newImage, _ = ui.DecodeBitmap(l.newData)
	}
}

// apply gives the contents to their file, on the main thread.
func (l *loaded) apply() {
	f := l.file
	f.oldLines, f.newLines = l.oldLines, l.newLines
	f.oldHL, f.newHL = l.oldHL, l.newHL
	f.oldImage, f.newImage = l.oldImage, l.newImage
	f.oldSize, f.newSize = len(l.oldData), len(l.newData)
	f.loaded = true
	f.metricsDone = false
	f.spans = nil
}
func isImage(path string) bool {
	switch strings.ToLower(filepath.Ext(path)) {
	case ".png", ".jpg", ".jpeg", ".gif", ".bmp", ".webp", ".ico":
		return true
	}
	return false
}

// isSVG reports whether a path is of an SVG picture the app shows.
func isSVG(path string) bool {
	return strings.ToLower(filepath.Ext(path)) == ".svg"
}
func (w *window) loadContents(gen int, oldRev, newRev string, files []*fileState) {
	defer boostGC()()
	contents, err := w.repo.NewContents()
	if err != nil {
		return
	}
	defer contents.Close()

	jobs := make(chan loaded)
	results := make(chan loaded)
	var wg sync.WaitGroup
	// A few workers: more would take the CPU, and the collector's pauses,
	// from the main thread, which draws the window.
	for range highlightWorkers() {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for job := range jobs {
				job.finish()
				results <- job
			}
		}()
	}
	go func() {
		defer close(jobs)
		for _, f := range files {
			if w.stale(gen) {
				return
			}
			job := w.read(contents, f.File, oldRev, newRev)
			job.file = f
			jobs <- job
		}
	}()
	go func() {
		wg.Wait()
		close(results)
	}()

	var batch []loaded
	flush := func() {
		if len(batch) == 0 {
			return
		}
		done := batch
		batch = nil
		w.update(func() {
			if gen != w.gen {
				return
			}
			for i := range done {
				done[i].apply()
			}
			w.rowsDirty = true
		})
	}
	tick := time.NewTicker(60 * time.Millisecond)
	defer tick.Stop()
	for {
		select {
		case l, ok := <-results:
			if !ok {
				flush()
				return
			}
			batch = append(batch, l)
		case <-tick.C:
			flush()
		}
	}
}

// boostGC lets the heap grow further between collections while files load:
// Chroma's lexers make much garbage, and every collection has the main
// thread, which allocates as it draws, help with it. The function it
// returns ends the boost.
func boostGC() func() {
	gcBoost.Lock()
	if gcBoost.n == 0 {
		gcBoost.old = debug.SetGCPercent(400)
	}
	gcBoost.n++
	gcBoost.Unlock()
	return func() {
		gcBoost.Lock()
		gcBoost.n--
		if gcBoost.n == 0 {
			debug.SetGCPercent(gcBoost.old)
		}
		gcBoost.Unlock()
	}
}

var gcBoost struct {
	sync.Mutex
	n, old int
}

// highlightWorkers is how many files are highlighted at once.
func highlightWorkers() int { return min(max(runtime.NumCPU()/4, 1), 2) }

func (w *window) stale(gen int) bool { return w.genA.Load() != int64(gen) }

// loadUser reads the name of the git user, which comments show, off the
// main thread: running git takes a while.
func (w *window) loadUser() {
	w.background(func() {
		name := strings.TrimSpace(w.repo.ConfigValue("user.name"))
		if name == "" {
			name = strings.TrimSpace(w.repo.ConfigValue("user.email"))
		}
		w.update(func() { w.user = name })
	})
}

// loadHistory reads the commits of the History tab.
func (w *window) loadHistory() {
	limit := w.historyLimit
	w.historyLoading = true
	w.background(func() {
		commits, err := w.repo.Log(0, limit)
		if err != nil {
			commits = nil
		}
		w.update(func() {
			w.history = commits
			w.historyMore = len(commits) >= limit
			w.historyLoading = false
		})
	})
}
func (w *window) watch(stop chan struct{}) {
	for {
		delay := 10 * time.Second
		if w.win.IsFocused() {
			delay = 2500 * time.Millisecond
		} else if !w.win.IsVisible() || w.win.IsMinimized() {
			delay = 30 * time.Second
		}
		select {
		case <-stop:
			return
		case <-time.After(delay):
		}
		w.checkChanges()
	}
}
func (w *window) checkChanges() {
	var src source
	var sig string
	var busy bool
	mygo.RunOnMain(func() { src, sig, busy = w.source, w.signature, w.loading || w.changed })
	if src.kind == sourcePull && !busy {
		w.checkPull(src)
		return
	}
	if busy || sig == "" || src.kind == sourceCommit {
		return
	}
	if now := w.repo.StatusSignature(); now != sig {
		w.win.Update(func() {
			if w.source == src && !w.loading {
				w.changed = true
			}
		})
	}
}
func (w *window) refresh() {
	w.load()
	w.loadHistory()
	if w.pullsLoaded {
		w.loadPulls()
	}
}
