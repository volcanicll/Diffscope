# Architecture

Diffscope is one Go program on [MyGo](https://mygo.egoist.dev/), a retained
UI toolkit that draws with the GPU. There is no webview and no server: a
window is a tree of UI elements rebuilt, in part, as state changes. The
packages `internal/*` hold what the window needs; the `main` package holds
the window.

```
git command ──▶ internal/git ──▶ internal/diff ▲
                                             │ load.go ──▶ window state ──▶ views
github API ──▶ internal/github ──────────────┘                    (view.go, sidebar.go,
agent CLI ───▶ internal/agent ──▶ analysis.go                      diffview.go, rows.go,
                                                                   browse.go, commit.go,
                 pty ──▶ internal/vt ──▶ terminal.go               terminal.go…)
```

## The window and its state

`window` (window.go) holds everything a review is: the `source` it reviews
(the work tree, a commit, a branch comparison or a pull request), its
`*git.Repo`, the files `load()` produced, the viewed marks, the comments,
the AI analysis, the sidebar's tab and selection, and the tool state (find,
filter, commit sheet). One window reviews one repository; a second
repository opens a second window (or the same one, when **File → Open
Folder…** is asked to).

Two helpers drive every state change:

- `w.background(fn)` runs fn off the main thread, for work that can wait:
  git commands, file reads, agent runs.
- `w.update(fn)` applies fn on the main thread, which is when the UI may
  change.

A load bumps a generation counter (`w.gen`); a `w.update` that arrives late
compares its generation and drops itself, so a slow `git` command never
paints stale files over fresh ones.

## From a git command to the screen

`load.go` is the spine. Given the window's source, it:

1. computes a change signature of the source, so that a later refresh can
   tell whether anything changed at all (`StatusSignature` for the work
   tree, the commit's hash elsewhere);
2. asks `internal/git` for the files:

   - work tree, **All**: `WorkingTree` — the changes against `HEAD`;
     **Staged**: `Staged` — the index against `HEAD`; **Unstaged**:
     `Unstaged` — the work tree against the index. Each marks the files it
     covers `staged` or not, which the sidebar's letters show;
   - a commit: `CommitDiff` against the first parent;
   - a branch comparison: `Compare` against the merge base;
   - a pull request: `fetchPull` (pull.go) fetches `refs/pull/N/head` of
     the repository's remote, then reads it as a branch comparison;

3. preloads the first files' contents within `preloadBudget` (80 ms) so
   highlighting arrives early, then streams the rest;
4. stores the result on the window and marks the rows dirty.

`rows.go` turns each file's patch into the flat list of rows the diff
surface draws: file headers, hunk headers, changed and context lines,
collapsed-fill rows, comments. `diffview.go` lays those rows into cards —
split (two columns sharing one row) or unified — and attaches syntax
highlighting from `internal/highlight` (Chroma), word-level highlights from
`internal/diff`, image previews, and the comment anchors. `view.go` builds
the whole review: toolbar, banners, the scroll of file cards, shortcuts;
`sidebar.go` builds the four tabs of the sidebar (files, history, pull
requests, repository) beside it.

The window rebuilds only what a change touched: `rowsDirty` rebuilds the
rows, a file's contents arriving rebuilds that file's card, and everything
else stays.

## The sidebar and the views

- `sidebar.go`: the master–detail. The file tree carries status letters
  (M, A, D, U) and change counts, its disclosure opens on release, and its
  selection drives which cards open. A file's context menu (`fileMenu`)
  copies its paths, renames it or deletes it in the work tree; the file's
  card and the repository tab offer the same. The sidebar is built even
  when hidden: `Animate` slides its width to zero and back (`sidebarNow`),
  the review following it, while its views keep the width they lay out at
  and are clipped to what shows. Its tabs, and the review, the commit
  sheet and a browsed file, swap with `Transition` enters and exits. The
  history tab lists
  `repo.Log()`'s commits. The pull requests tab lists
  `internal/github`'s open pulls of the remote (`upstream`, else
  `origin`).
- `browse.go`: the Repository tab, the files of the work tree. Choosing a
  file opens it over the review: source up to `browseMaxLines` lines, with
  the find bar (`updateBrowseMatches`), an image's bitmap, or an SVG drawn
  directly. It is a viewer, not an editor.
- `commit.go`: the commit sheet. It takes the files whose checkboxes are
  on, writes their paths to the index (`git add` / `git restore --staged`
  for the rest), and commits with the subject and summary given; other
  staged work stays staged.
- `palette.go`: the command bar (<kbd>⌘K</kbd>), the file filter
  (<kbd>⌘P</kbd>) and the help sheet.
- `comments.go`: the review comments, per file and line, held on the
  window, copied out as Markdown with the diff around them.
- `groups.go`: the file kinds — code, tests, docs, config, dependencies,
  generated — their rules, and the groupings (not at all, by kind, by AI
  review).
- `analysis.go`: the AI review. It runs the window's agent over the
  change's files, updates its progress as lines arrive, keeps the result
  until asked for another, and fingerprints the files it saw so a changed
  file can be marked as no longer covered.
- `terminal.go`: the terminal panel. A session runs the user's shell on a
  pty with the login environment (`internal/shellenv`), reads its output
  into `internal/vt` and draws the rows as a list; ending it kills the
  shell's process group, children included. Tabs hold the sessions; the
  panel sits under the review or beside it, sliding open and closed as
  `termFactor` animates its share while the review takes the room it
  leaves. Beside the review the panel keeps the width it lays out at and
  is clipped to the animated one, so its lines do not wrap anew on every
  frame of the slide. The shell starts once the first frame has measured
  the panel, so its greeting wraps at the width it is read at — and
  `Term.Reflow` re-wraps the screen and the scrollback when the width
  changes later. While the pane is being resized — its divider dragged,
  the sidebar dragged or sliding, the window resized — the screen re-wraps
  at once, every frame, with `Term.ReflowScreen`, so the text follows the
  pointer; the pty hears of the width some times a second, so the prompt a
  shell redraws follows too without flitting. The history keeps the width
  it had until the resize rests, and `Term.ReflowLog` then re-wraps it
  whole: a line filled to its width is taken for a wrapped one, and
  joining those again and again at passing widths would garble the text.
  A change of height alone only resizes.

## The packages

### internal/git

One type, `*Repo`, rooted at a repository's directory, that runs `git` and
parses the porcelain: status (all, staged or unstaged), a commit's diff,
the log and the branches, a comparison of two revisions, and checking out
a branch. Generated
files carry their own rules (generated.go): lockfiles, `*.min.js`,
protobufs, snapshots, `linguist`-style generated paths, dependency folders.
File contents come from `git cat-file --batch`, a single long-lived
process, which the browser and the diff view share.

### internal/diff

Parses a patch into `*File`, with hunks, lines and their pairs, and
computes word-level differences for the changed lines of a hunk, which the
view draws as spans (spans.go).

### internal/highlight

Chroma's lexers, themed to Diffscope's Licht and Dunkel palettes. It
highlights a file's whole contents once, so both sides of a split row, and
the context rows around changes, come from the same pass.

### internal/github

The pull requests: their list, their commits, files and comments, and the
submission of a review. It authenticates with the GitHub CLI, else with
`githubToken` in the settings, else with `$DIFFSCOPE_GITHUB_TOKEN`,
`$GH_TOKEN`, `$GITHUB_TOKEN`; public pulls read without any.

### internal/agent

The coding agents' command lines (claude, codex, opencode, pi), the models
each of them lists, and one run of a review: a prompt on the change's
files, read-only, whose streamed output is the summary, the file groups and
the notes that `analysis.go` keeps.

### internal/fileicon

The file tree's icons, generated from [@pierre/trees][] by
`go run ./tools/genfileicons`, each kind of file in its color.

### internal/vt

`charmbracelet/x/vt`'s emulator, wrapped for the panel: the bytes a
program writes become rows of cells the view renders, and the state the
keyboard and the session ask about (title, cursor, modes, scrollback,
alternate screen). The wrapper adds `Reflow`, which rewrites the screen
and scrollback at a new width from their cells — the width a line wrapped
at is not recorded, so a line filled to its last column counts as a
continued one. `ReflowScreen` re-wraps the screen alone, which a dragged
divider goes by, and `ReflowLog` then re-wraps the history whole, at the
width the screen took. Each leaves the alternate screen, a hidden cursor
and cursor-addressed layouts alone, because those belong to the program.

### internal/proc, internal/shellenv

`proc` prepares the commands the app runs: it keeps a console from opening
on Windows, and starts a command in a process group of its own, which
cancelling kills whole. `shellenv` finds the programs of the user's shell —
an app opened from Finder sees only the system's PATH — and gives the
terminal its login environment, so commands run as they do in Terminal.

## Testing

`go test ./...` drives the packages, and the views too: `app_test.go` builds
a window over a real temporary repository, without a real screen, and
asserts on the elements of the frame — clicks in the sidebar, comments
added, a commit made — and `terminal_test.go` drives the terminal panel's
tabs, hide button, divider and reflow. `DIFFSCOPE_SNAPSHOTS=<dir> go test .`
writes each frame as a PNG to look at, which is how the palettes are checked
by eye.

## Environment

| Variable | |
|---|---|
| `DIFFSCOPE_CAPTURE` | write a PNG of the window once loaded, then quit |
| `DIFFSCOPE_CAPTURE_SETUP` | run a git setup script before the capture |
| `DIFFSCOPE_CPUPROFILE` | write a CPU profile |
| `DIFFSCOPE_DEBUG` | log git commands, slow frames, main-thread stalls |
| `DIFFSCOPE_EDITOR` | the editor command, over the settings |
| `DIFFSCOPE_NAME` | run under another app name, apart from the installed app |
| `DIFFSCOPE_SNAPSHOTS` | where the tests save their frames |
| `DIFFSCOPE_STARTUP_PROFILE` | write a CPU profile of the first seconds |
| `DIFFSCOPE_GITHUB_TOKEN`, `GH_TOKEN`, `GITHUB_TOKEN` | a GitHub token, after the settings |

[@pierre/trees]: https://www.npmjs.com/package/@pierre/trees
