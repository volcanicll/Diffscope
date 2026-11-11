# Diffscope — Redesign Specification

Status: implemented · 2026-11-11

Implementation notes: the shipped build masks AI review (`aiReviewEnabled` in
`analysis.go`), so its strips, notes and shortcuts are hidden; the AI and pull
request details expand inline under their strips rather than in floating
sheets; a mode keeps the document it shows rather than restoring a separate
source and scroll per mode (Changes always returns to the work tree); and
`sidebarPosition` is accepted but the navigator stays beside the rail.

## Context

Diffscope is functionally complete but has no experience thesis: the diff is a feed of rounded cards (62 px of chrome per file), scope is a dropdown, `Repository` is a hidden fourth tab, commit unmounts the diff, and AI/PR banners push code down. This spec reorganizes **existing capability only** — no staging model, editing, or new integrations — into one continuous surface with a fixed frame. It is grounded in `view.go`, `sidebar.go`, `diffview.go`, `commit.go`, `analysis.go`, `pullview.go`, `theme.go`, and uses only primitives the shell already has: rows/columns, virtualized `ui.List` with sticky headers, absolute boxes, 1 px rects, SVG icons, native material.

## 1. Experience thesis

**The diff is the page.** One continuous document; repository, branch and scope fixed above it; commit fixed at its edge; every other surface — history, pulls, repository, AI, terminal — waits in the margin until asked.

Implications: nothing displaces the document (panels dock, strips pin, peeks overlay); chrome is a frame, not a feed (no per-file cards); the default view is code, not metadata.

## 2. Information architecture

**Primary:** the document — the diff for the active source. Source ∈ {working tree, commit, branch compare, PR}; filter ∈ {All, Staged, Unstaged} only for the working tree. Layers: annotations (comments, PR threads, AI notes), find, source strip, AI strip.

**Secondary (never replaces the document):** rail (Changes, History, Pulls, Repository, terminal toggle); navigator (file/commit/PR/repository list + filter + commit dock); commit dock; source and AI strips with expandable sheets; repository peek overlay; terminal (bottom or right of the document column); find bar, palette, view-options popover, native menus.

**Demoted:** group chip row → navigator group control + in-document group headers; repository browser → rail mode + peek; PR description and AI summary → 36 px strips + sheets; view options → popover; settings remain in the config file.

**Removed:** sidebar text tabs; toolbar scope dropdown; rounded file cards, 12 px bands, 4 px footers; full-screen commit mode; top-of-diff AI/PR banners; hidden fourth sidebar view; section labels buried in gap rows.

**State model:** mode owns its source. Changes → worktree; History → its last commit (or worktree until one is chosen); Pulls → its last PR (or worktree until chosen); Repository → no source, peek only; AI → a layer per source, not a mode. Switching modes restores that mode's source and scroll; the titlebar names the active source; `×` on the crumb returns to Changes. AI annotates the source it describes; it is not a place you go.

## 3. Layout system

```
┌──────────────────────────────────────────────────────────────────────────────────┐
│ ●●●   ▣   Diffscope ▾   main ▾   [ All 12 │ Staged 3 │ Unstaged 9 ]   ⌕  ☰  ✦  ⋯ │ 38
├──────┬──────────────────────────┬──────────────────────────────────────────────────┤
│      │ Changes           3 files│  a1b2c3d  Fix parser overflow          × Changes│ 36
│  ◧   │ ┌──────────────────────┐ │  ✦  5 notes · 2 need care       Show notes      │ 36
│  3   │ │ ⌕ Filter files       │ │ ┌ ▾ src/parser.go    M  +42 −18     ✓ Viewed ┐  │ 38
│      │ ├──────────────────────┤ │ │ ⋯ 12 unmodified   @@ -12,7 +12,9 @@ parse() │  │ 28
│  ◷   │ │ ▾ src/               │ │ │  12  12   func parse(...) {               │  │ 20
│      │ │   parser.go   +42 −18│ │ │  13  13     if err != nil {               │  │ 20
│  ⑂   │ │   lexer.go     +8 −2 │ │ │  14      −     old code                  │  │ 20
│      │ │ ▸ tests/             │ │ │      14  +     new code             ●     │  │ 20
│  ▤   │ │   parser_test  +12   │ │ └───────────────────────────────────────────┘  │
│      │ │                      │ │                                                │
│  >_   │ ├──────────────────────┤ │                                                │
│      │ │ +62 −20    3 files   │ │                                                │
│      │ │       [ Commit ⌘↩ ]  │ │                                                │
└──────┴──────────────────────────┴──────────────────────────────────────────────────┘
   44              280                                   flex
```

| Region | Size | Boundary rationale |
|---|---|---|
| Titlebar | 38, full width | Context is global; collapsing the navigator must not lose repo/branch/scope. Full width also frees the traffic lights from the sidebar. |
| Rail | 44 | 28 px targets + 8 px gutters is the minimum comfortable rail; modes stop moving. |
| Navigator | 280 (220–420) | One path + stats per row; 420 stops squeezing split view. |
| Document | flex, no max | Code gets every remaining pixel; long lines scroll horizontally. |
| Commit dock | navigator footer | Commit belongs to the change set, not the reading surface. |
| Terminal | document column | Navigation stays visible; right position splits only the document. |
| Strips | 36 each, max two | Source/AI are document properties; both dismissible. |
| Status bar | none | Totals and commit live in the navigator footer; a global bar would duplicate and cost 26 px. |

Rhythm: 38 titlebar/file bar; 36 strips; 44 dock; 30 navigator header; 28 filter/list row; 28 hunk boundary; 20 code line (round(size·20/13)); 11–15 type. Radii 0 in the document, 6 controls, 8 sheets. Hairlines 1 px, never 2 except focus. No shadows or side borders on the document.

Window minimum becomes 860×520 — at 720 a split diff is unusable. The navigator collapses manually; scope survives in the titlebar. All elements map to existing primitives (sticky bars already use `ListState.Header`; the gutter is a fixed cell per row); no large blurs or shadows; virtualization unchanged.

## 4. Interaction model

| Keys | Action | Status |
|---|---|---|
| `⌘1–4` | Changes / History / Pulls / Repository | kept, rail |
| `⌃1–3` | All / Staged / Unstaged | new |
| `J` / `K` | next / previous change (hunk or file header) | kept |
| `⇧⌘]` / `⇧⌘[` | next / previous file | new |
| `Enter` | comment on active line/hunk | kept, now needs an active line |
| `V` | toggle viewed for current file | new |
| `N` / `⇧N` | next / previous AI note | kept |
| `⌘↩` | open commit dock / commit | kept, dock |
| `Esc` | palette → find → sheet → dock → peek → clear selection → collapse navigator | kept, extended |
| `⌘F` / `⌘P` / `⌘K` | find / filter / palette | kept |
| `⌘⇧B` / ``Ctrl+` `` | navigator / terminal | kept |
| `⌘⇧M` / `⌘⇧V` / `⌘R` | copy comments / view options / refresh | kept |

Pointer: file bar click chevron/path collapses, double-click path opens editor, right-click menu; stats and Viewed are independent 28 px targets. A diff-line click sets the **active line**; drag selects text; double-click selects a word; hover `+` or `Enter` creates a draft — a plain click never creates a comment, removing today's accidental drafts. Annotation marker hover previews (260 px), click expands. Navigator click selects and scrolls; document scroll updates selection after a 600 ms settling guard. Resize: navigator edge (snap <160 → closed), terminal divider, dock top; visible 1 px line + 9 px hit area. No swipe navigation; horizontal trackpad scroll is diff hscroll; `⌘±` changes code size.

Transitions: scope filters in place with a 100 ms list cross-fade, preserving the current file or nearest and per-scope scroll; mode swaps the navigator 120 ms (Y 4, opacity) and restores that mode's source; commit expands the dock 180 ms while the document stays put, confirms 2 s, collapses and refreshes; repository peek overlays 180 ms and Esc restores the exact scroll; AI runs idle → running → done/error with notes appearing at completion, never streamed into the document.

Persist: mode, scope per repo, navigator width/visibility, per-source scroll, viewed, terminal layout, unfinished commit body. Today scope, tab and scroll reset — fix these.

## 5. Visual language

| Role | Use | Rule |
|---|---|---|
| `canvas` | behind document and navigator | app background |
| `page` | code surface | opaque; the only pure code ground |
| `chrome` | titlebar, file bars, strips | header surface; hairlines, never shadows |
| `well` | inputs, segmented track | pill background |
| `hairline` | all separators | 1 px at 8–12% text |
| `text` / `secondary` / `tertiary` | code, labels, metadata | 4.5:1 / 4.5:1 / 3:1 |
| `accent` | selection, focus, active line, AI presence | system accent; derived tints, never fixed blue |
| `add` / `del` | diff semantics | soft bg + gutter + 4 px bar + count text + word mark; the only chromatic color in the document |
| `modify` / `conflict` / `merged` | status letters, critical notes, merged PR | amber / danger / violet tokens; replace hardcoded `#d4a72c`, `#8250df` |
| `find` | matches | yellow; current match stronger |

AI never gets its own hue — one accent sparkle. Selection highlights derive from the system accent (`pal.selected` is currently fixed blue). The syntax palette survives but holds 4.5:1 on code; `ClassName`/`Exception` lose saturation in dark. No hardcoded white badge text. Vibrancy only behind rail/navigator; document, strips and popovers are opaque.

Type: two families — system UI for chrome, configured mono for code, paths, hashes, counts. Scale 11/12/13/15; weights 400/500/600; tabular numerals; no uppercase. Code 13 (10–32), numbers 11, hunk metadata 11, filename 12.5/600 mono, directory 12/400 muted. Hierarchy by size and weight, not color.

Density: 2 px base; spacing 4/6/8/12/16/24; 20 px line height is the floor. File chrome drops 62 → 46 px (8 band + 38 bar); the pinned bar 58 → 46; removing the chip row saves 34. A ten-file review regains ~200 px.

Motion: 120 ms feedback, 180 ms disclosure/panel, 220 ms full slide; cubic-bezier(0.2,0,0,1). Only panels, disclosure and 100 ms list cross-fades animate. No load fades, stagger, hover lift, springs, parallax or code reflow. Reduce Motion → instant; progress becomes text. Terminal cursor is the only loop besides the AI progress line.

## 6. Component specifications

### 6.1 Navigation rail
44 px below the titlebar: Changes, History, Pulls, Repository; spacer; Terminal at the bottom. 28×28 buttons, 18 px icons. Active = 2×18 accent bar at the left edge + 8% accent fill + accent icon; hover 8% neutral; 4 px dot when a mode has content; tooltip carries count and shortcut. Pulls hidden without a GitHub remote; Terminal is a panel toggle, not a mode. *Why:* modes stop moving, Repository gets a home, text tabs leave the working surface.

### 6.2 Scope switcher
Titlebar, 24 px high: three content-sized segments in a pill track, 2 px padding; selected = chrome + hairline + primary text; counts 10.5 mono muted. Only for the worktree; commit/PR/branch sources replace it with a crumb (`a1b2c3d`, `#123`, `main ⇄ feature`) and `×` to return to Changes. Immediate switch, `⌃1–3`, zero counts still selectable, per-scope file/scroll memory, 100 ms list cross-fade. *Why:* scope is the most-used filter; a dropdown costs a click and hides the options, and the titlebar never scrolls or collapses.

### 6.3 File section (formerly file card)
8 px canvas band + 38 px full-bleed sticky bar, bottom hairline, zero radius, no side borders. Chevron 15 (150 ms), icon 15, directory 12 muted + name 12.5/600 mono, rename note 11, status letter 10/600, `+N −M` 11 mono, spacer, annotation count, Viewed (20 px check + 11 label; icon-only below 1000 px), overflow on hover. Current file = 2 px accent left edge; collapsed = bar only; viewed = muted + checked; updated = 4 px ref dot, no flash. Click chevron/path toggles; double-click path opens editor; right-click menu. *Why:* 16 px less chrome per file, a 12 px shallower sticky bar, and the path is the only chrome over code.

### 6.4 Diff line, hunk boundary, gap
One **28 px boundary row** is both gap and hunk header: left `⋯ 12 unmodified lines` with expand controls, right `@@ -12,7 +12,9 @@ parse()` in 11 mono tertiary. A hunk without a preceding gap gets a 24 px header-only row — no 26 px tax on every hunk. Line = 20 px: `[4 status][stable line numbers][10 marker][code, 8/10 padding][16 annotation]`. Numbers 11 mono, right-aligned, document-wide width (min 3 digits, max 5), so files never shift horizontally. Marker `+`/`−` 11 at 60%; context blank; never color-only. Split: two cells, 1 px divider, empty side; unified: two number columns. States: hover 4% neutral; active line = accent numbers + 2 px accent marker rule; hunk selection 6% accent wash; find yellow (current stronger); word-level 20/22% (on by default — the one detail core to the product); AI line gets a gutter sparkle. Precedence: find > selection > hover. Click selects, drag selects text, Enter/hover `+` comments, right-click menu.

### 6.5 Annotation gutter
16 px fixed right lane, 1 px hairline left, always reserved so code never shifts. 6 px markers at line center: draft accent, comment neutral, resolved 30%, AI sparkle 10; more than two becomes a count. Hover = 260 px preview (author, state, three lines, old/new line label); click expands the thread and scrolls; keyboard reachable via note/thread navigation. *Why:* discussions become locatable without reading every row and stop interrupting code as cards.

### 6.6 Commit dock
Navigator footer, Changes only. Collapsed 44 px: `+62 −20` semantic counts, spacer, primary Commit 24; when the navigator is collapsed the button moves to the titlebar. Expanded 180 ms (max 60% of navigator): header 28 with close; Subject 30 (counter after 40, amber after 50, non-blocking); Body 64–160; checklist collapsed to `3 of 3 files` with select-all, rows 24 when open; footer error/success + primary `Commit N files` + `⌘↩` hint. `⌘↩` opens/focuses Subject or commits; Esc closes when idle; success confirms 2 s, collapses, refreshes. The document is never unmounted. *Why:* commit belongs to the change set; today's mode swap hides the code being committed.

### 6.7 AI review strip + notes sheet
36 px pinned under the source strip, only when active or reviewed. Idle: sparkle + `Review with <agent>` + agent menu + Run + dismiss. Running: spinner + progress + elapsed + Stop + 2 px indeterminate line. Done: `5 notes · 2 need care` + Show notes + Review again + fold. Error: danger text + Try again. Notes sheet: 420 px popover, max 60% height, grouped by file, rows 36, `↑/↓/Enter`, action `Group files by this review`. Inline note: quiet row after the line, 2 px accent rule, sparkle 12, 12.5 text, max two lines + more; critical uses danger for icon/rule only. No banner, purple, glow, or streaming reflow. *Why:* AI describes the diff; it must not displace it.

### 6.8 Titlebar / toolbar
38 px. Left: traffic lights, panel toggle 28, repo button (13/600, chevron, max 160), branch chip 24 (max 140), scope segment or source crumb. Right: Find, View options, Comments (+badge), AI, More (all 28); Commit 24 only when the navigator is hidden. Drag region between; double-click zooms. Repo menu: Open Folder/Recent, Reveal, Copy Path, Config, Install CLI. Branch menu: existing list. Crumb: click → History/Pulls; `×` → Changes. 28 px targets, 6 px radius, 10% hover, 2 px accent focus. *Why:* one line of global context, one line of global action; everything else belongs to the document or navigator.

## 7. Anti-patterns — what this redesign refuses

1. **No card-feed diff.** No rounded cards, shadows, side borders, 12 px gutters or per-file footers. The diff is a page.
2. **No diff-unmounting modes.** Commit, browse, repository and AI never replace the document; they dock, strip, or peek.
3. **No AI theater.** No purple, glow, gradient, “AI-powered” badge, or token stream into code. One sparkle, neutral surfaces, an accent rule.
4. **No dashboard.** No metric tiles, review scores, donuts, hero headers.
5. **No hidden navigation.** No tabs that lie about the source, no repository view without a home, no scope behind a dropdown.
6. **No decorative motion.** No load fades, stagger, hover lift, spring or parallax; motion only explains a panel.
7. **No color-only meaning.** `+`/`−` glyphs, status letters, marker rules; add/del never rely on hue alone.
8. **No hover-only essentials.** Comment via Enter/gutter; Viewed always visible; resize handles have visible lines.
9. **No glass on content.** Native sidebar material only; document and popovers opaque.
10. **No feature creep.** No staging model, inline editing, merge resolver or review scores. This is a reorganization.

## 8. The five changes that matter most for a daily user

1. **Make the diff continuous and full-bleed.** Drop card chrome (62 → 46 px/file), stabilize the gutter, merge gap and hunk header, add `+`/`−` glyphs. More code per screen, no lateral jumps, faster scanning.
2. **Put scope in the titlebar and modes in a rail.** One click to All/Staged/Unstaged, every surface visible, context survives collapse. Ends the dropdown tax and the hidden Repository view.
3. **Turn commit into a dock.** The diff never disappears; subject, body and checklist live at the edge of the change list. Review and commit become one flow instead of two modes.
4. **Separate reading from commenting.** Click selects, Enter/`+` comments, the annotation gutter marks every discussion. Eliminates accidental drafts and gives comments a map.
5. **Compress AI and PR into 36 px strips.** No banners or banner-sized summaries; AI notes are quiet and on demand. The page always starts with code.

## Appendix A — Implementation map

`view.go`: root becomes `titlebar + Row(rail, navigator, document column)`, strips/find/empty/loading/error. `sidebar.go`: split rail + navigator, remove tabs/title row, tree restyle, commit dock. `commit.go`: compact dock form, collapsible checklist. `diffview.go`: full-bleed sections, 38 px file bar, boundary/gap row, stable gutter, marker column, annotation cell, line states. `rows.go`: boundary row kind, gutter metadata. `groups.go`: group rows restyle, delete `scopeBar`. `analysis.go`: AI strip + notes sheet, quiet inline notes. `pullview.go`: source strip, review sheet, flat threads. `theme.go`: role tokens, accent-derived selection, remove hardcoded colors. `menu.go`/`palette.go`: scope submenu/shortcuts, grouping location, labels. Tests: update `app_test.go`, `sidebar_test.go`; add click-vs-comment, scope scroll preservation, Esc ladder, dock commit without unmount. Phases: frame → document → commenting → dock → strips → theme/motion/tests.

## Appendix B — Verification

- **Walkthrough:** cold open (code starts at 46 px, no banner); scope switch preserves the current file; 500-line review with `J/K/V/N/Enter`; comment then Esc; commit from the dock with the diff visible; PR via strip + sheet; AI run → notes → group; repository peek restores scroll; terminal reflow.
- **Visual:** 1440×900 and 860×520, light/dark/system, Reduce Motion, keyboard-only.
- **Performance:** 100 files / 10k lines at frame rate; no per-frame text measurement; per-row markers/gutter; no large blurs or shadows.
- **Contract:** existing MyGo primitives only; no new dependency; no new feature surface.
