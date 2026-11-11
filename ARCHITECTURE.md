# Architecture

Diffscope is one Go program on [MyGo](https://mygo.egoist.dev/), a retained
UI toolkit that draws with the GPU. There is no webview and no server: a
window is a tree of UI elements rebuilt, in part, as state changes. The
packages `internal/*` hold what the window needs; the `main` package holds
the window.

```
git command ──▶ internal/git ──▶ internal/diff ▲
                                             │ load.go ──▶ window state ──▶ views
github API ──▶ internal/github ──────────────┘                    (view.go, rail.go,
agent CLI ───▶ internal/agent ──▶ analysis.go                     sidebar.go, diffview.go,
                                 (masked)                          rows.go, browse.go,
                 pty ──▶ internal/vt ──▶ terminal.go               commit.go, terminal.go…)
```

## The window and its state

`window` (window.go) holds everything a review is: the `source` it reviews
(the work tree, a commit, a branch comparison or a pull request), its
`*git.Repo`, the files `load()` produced, the viewed marks, the comments,
the navigator's mode and selection, the commit dock, and the tool state
(find, filter, terminal). One window reviews one repository; a second
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
     covers `staged` or not, which the file bar's badge shows;
   - a commit: `CommitDiff` against the first parent;
   - a branch comparison: `Compare` against the merge base;
   - a pull request: `fetchPull` (pull.go) fetches `refs/pull/N/head` of
     the repository's remote, then reads it as a branch comparison;

3. preloads the first files' contents within `preloadBudget` (80 ms) so
   highlighting arrives early, then streams the rest;
4. stores the result on the window and marks the rows dirty.

`rows.go` turns each file's patch into the flat list of rows the document
draws: source and AI strips, file headers, gap and hunk-boundary rows,
changed and context lines, image rows, comments, review threads and notes.
`diffview.go` lays those rows into one continuous document — split (two
columns sharing one row) or unified — with a sticky 38 px file bar over
each file, a line-number gutter measured once for the whole document, the
annotation lane at its right edge, and syntax highlighting from
`internal/highlight` (Chroma), word-level highlights from `internal/diff`,
image previews, and the comment anchors. `view.go` builds the frame: the
full-width title bar, the rail, the navigator, the document with its
source strips, the terminal and the browser overlay, and the shortcuts.

The window rebuilds only what a change touched: `rowsDirty` rebuilds the
rows, a file's contents arriving rebuilds that file's lines, and everything
else stays.

## The frame

The window is one frame around one document:

- `view.go` builds a `Column` of the title bar and a `Row` of the rail,
  the navigator (with its resizer) and the document column. The title bar
  is full width, so the repository, branch and scope stay visible when the
  navigator closes. The document column holds the review, the terminal
  (under or beside it), and the Repository view's file as an absolute
  overlay.
- `rail.go` is the 44 px column of modes: Changes, History, Pull
  requests (when the repository has a GitHub remote), Repository, and the
  terminal at the bottom. A mode is an icon and a short accent rule; the
  modes never move as the document changes.
- The document pins a source strip for a commit (hash, subject, author,
  date, message disclosure) or a pull request (state, number, title,
  author, branches, stats, actions) at its top, 36 px high. The AI review,
  when enabled, pins a 36 px strip under it. Nothing else displaces code.
- The navigator's header names its mode and count, and holds the grouping
  menu; its footer totals the changes and opens the commit dock. The
  navigator slides between `sidebarShown` and zero width (`Animate`), the
  document following it, while its views keep the width they lay out at
  and are clipped to what shows.
- A browsed file is an absolute box over the document column, not a
  replacement of it: closing it returns to the same review at the same
  scroll.
- The Esc ladder (view.go's `shortcuts`) closes one layer at a time: the
  commit dock, the AI sheet, the pull request description, the commit
  message, the browsed file, then clears the selected line or hunk.

## The navigator and the views

- `sidebar.go`: the navigator. It lists the mode's rows — the file tree,
  the history, the pull requests or the work tree's files — under a
  filter field, and its selection drives the document. The file tree
  carries status letters (M, A, D, U) and change counts, its disclosure
  opens on release, and a file's context menu (`fileMenu`) copies its
  paths, renames it or deletes it in the work tree; the file's bar offers
  the same. A mode's list fades and slides in as it changes. The history
  list calls `repo.Log()`; the pull requests list calls
  `internal/github` for the open pulls of the remote (`upstream`, else
  `origin`). Its footer totals the change and opens the commit dock.
- `commit.go`: the commit dock, at the bottom of the navigator while the
  Changes mode shows. It takes the files whose checkboxes are on, writes
  their paths to the index (`git add` / `git restore --staged` for the
  rest), and commits with the subject and description given; other staged
  work stays staged. The document is never unmounted: the dock takes 252 px
  of the navigator, not of the code.
- `diffview.go`: the document. A file header is an 8 px canvas band and a
  38 px sticky bar with the path, status, stats, annotation count and the
  Viewed button. A gap row is one 28 px boundary carrying both "⋯ N
  unmodified lines" and the hunk's `@@` coordinates; a short gap with no
  boundary gets a 24 px hunk row. Lines show stable numbers, a `+`/`−`
  marker and the annotation lane. A click selects a line; Enter or the
  hovered `+` comments on it. Word-level highlights, the find marks and
  the selection wash share the same line. Split and unified share the
  row model.
- `browse.go`: the Repository view, the files of the work tree. Choosing a
  file opens it over the document: source up to `browseMaxLines` lines,
  with the find bar (`updateBrowseMatches`), an image's bitmap, or an SVG
  drawn directly. It is a viewer, not an editor.
- `palette.go`: the command bar (<kbd>⌘K</kbd>), the file filter
  (<kbd>⌘P</kbd>) and the help sheet.
- `comments.go`: the review comments, per file and line, held on the
  window, copied out as Markdown with the diff around them.
- `groups.go`: the file kinds — code, tests, docs, config, dependencies,
  generated — their rules, and the groupings (not at all, by kind, and by
  AI review when it is enabled).
- `analysis.go`: the AI review. It runs the window's agent over the
  change's files, updates its progress as lines arrive, keeps the result
  until asked for another, and fingerprints the files it saw so a changed
  file can be marked as no longer covered. This build masks it:
  `aiReviewEnabled` is false, so the sparkle, the strip, the notes, the
  shortcuts and the AI grouping all return early; the code and
  `internal/agent` stay for a later build.
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
  the navigator dragged or sliding, the window resized — the screen
  re-wraps at once, every frame, with `Term.ReflowScreen`, so the text
  follows the pointer; the pty hears of the width some times a second, so
  the prompt a shell redraws follows too without flitting. The history
  keeps the width it had until the resize rests, and `Term.ReflowLog` then
  re-wraps it whole: a line filled to its width is taken for a wrapped
  one, and joining those again and again at passing widths would garble
  the text. A change of height alone only resizes.

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
the notes that `analysis.go` keeps. This build masks the review, so no
command line starts from the window.

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
asserts on the elements of the frame — clicks in the navigator, comments
added, a commit made from the dock — and `terminal_test.go` drives the
terminal panel's tabs, hide button, divider and reflow.
`DIFFSCOPE_SNAPSHOTS=<dir> go test .` writes each frame as a PNG to look
at, which is how the palettes are checked by eye.

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
