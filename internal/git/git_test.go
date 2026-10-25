package git

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/egoist/godiff/internal/diff"
)

func gitIn(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), "GIT_AUTHOR_NAME=T", "GIT_AUTHOR_EMAIL=t@example.com", "GIT_COMMITTER_NAME=T", "GIT_COMMITTER_EMAIL=t@example.com")
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
	return string(out)
}

func write(t *testing.T, dir, path, content string) {
	t.Helper()
	full := filepath.Join(dir, path)
	if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(full, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func newRepo(t *testing.T) (string, *Repo) {
	dir := t.TempDir()
	gitIn(t, dir, "init", "-q", "-b", "main")
	gitIn(t, dir, "config", "user.name", "T")
	gitIn(t, dir, "config", "user.email", "t@example.com")
	write(t, dir, "a.txt", "one\ntwo\nthree\n")
	write(t, dir, "src/b.go", "package b\n")
	gitIn(t, dir, "add", ".")
	gitIn(t, dir, "commit", "-q", "-m", "first")
	r, err := Open(filepath.Join(dir, "src"))
	if err != nil {
		t.Fatal(err)
	}
	return dir, r
}

func TestWorkingTreeAndCommit(t *testing.T) {
	dir, r := newRepo(t)
	write(t, dir, "a.txt", "one\n2\nthree\n")
	write(t, dir, "new.md", "hello\n")
	write(t, dir, "node_modules/x/index.js", "x\n")
	write(t, dir, "pnpm-lock.yaml", "lock\n")
	os.Remove(filepath.Join(dir, "src/b.go"))

	files, err := r.WorkingTree(Options{})
	if err != nil {
		t.Fatal(err)
	}
	got := map[string]*diff.File{}
	for _, f := range files {
		got[f.Path] = f
	}
	if f := got["a.txt"]; f == nil || f.Status != diff.Modified || f.Additions != 1 || f.Deletions != 1 {
		t.Errorf("a.txt: %+v", f)
	}
	if f := got["new.md"]; f == nil || f.Status != diff.Untracked || f.Additions != 1 {
		t.Errorf("new.md: %+v", f)
	}
	if f := got["src/b.go"]; f == nil || f.Status != diff.Deleted {
		t.Errorf("src/b.go: %+v", f)
	}
	if f := got["node_modules"]; f == nil || !f.Directory {
		t.Errorf("node_modules: %+v", f)
	}
	if f := got["pnpm-lock.yaml"]; f == nil || !f.Generated {
		t.Errorf("pnpm-lock.yaml: %+v", f)
	}
	if files[0].Path != "src/b.go" || files[1].Path != "a.txt" {
		t.Errorf("order: %s, %s", files[0].Path, files[1].Path)
	}

	sig := r.StatusSignature()
	write(t, dir, "a.txt", "one\n22\nthree\n")
	if r.StatusSignature() == sig {
		t.Error("signature unchanged after an edit")
	}

	hash, err := r.CommitChanges("Change a\n\nDetails.\n", []string{"a.txt", "src/b.go", "new.md"})
	if err != nil {
		t.Fatal(err)
	}
	if hash == "" {
		t.Fatal("no hash")
	}
	log, err := r.Log(0, 10)
	if err != nil || len(log) != 2 || log[0].Subject != "Change a" || log[0].Body != "Details." {
		t.Fatalf("log %+v %v", log, err)
	}
	changed, err := r.CommitDiff(log[0], Options{})
	if err != nil || len(changed) != 3 {
		t.Fatalf("commit diff %d %v", len(changed), err)
	}
	status := gitIn(t, dir, "status", "--porcelain")
	if !strings.Contains(status, "?? pnpm-lock.yaml") || strings.Contains(status, "a.txt") {
		t.Errorf("status after commit:\n%s", status)
	}

	c, err := r.NewContents()
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	data, err := c.Read(log[1].Hash, "a.txt")
	if err != nil || string(data) != "one\ntwo\nthree\n" {
		t.Errorf("cat-file %q %v", data, err)
	}
	data, err = c.Read("HEAD", "missing.txt")
	if err != nil || data != nil {
		t.Errorf("missing %q %v", data, err)
	}
}

func TestStagedAndUnstaged(t *testing.T) {
	dir, r := newRepo(t)
	// a.txt staged alone, b.go staged with more in the work tree, c.go
	// unstaged alone, and d.md untracked.
	write(t, dir, "a.txt", "staged\n")
	write(t, dir, "src/b.go", "package b\nstaged\n")
	gitIn(t, dir, "add", ".")
	write(t, dir, "src/b.go", "package b\nstaged\nunstaged\n")
	write(t, dir, "c.txt", "unstaged\n")
	write(t, dir, "d.md", "new\n")

	working, err := r.WorkingTree(Options{})
	if err != nil {
		t.Fatal(err)
	}
	byPath := map[string]*diff.File{}
	for _, f := range working {
		byPath[f.Path] = f
	}
	if f := byPath["a.txt"]; !f.Staged || f.Unstaged {
		t.Errorf("a.txt: staged %v, unstaged %v", f.Staged, f.Unstaged)
	}
	if f := byPath["src/b.go"]; !f.Staged || !f.Unstaged {
		t.Errorf("src/b.go: staged %v, unstaged %v", f.Staged, f.Unstaged)
	}
	if f := byPath["c.txt"]; f.Staged || !f.Unstaged {
		t.Errorf("c.txt: staged %v, unstaged %v", f.Staged, f.Unstaged)
	}

	staged, err := r.Staged(Options{})
	if err != nil {
		t.Fatal(err)
	}
	got := map[string]bool{}
	for _, f := range staged {
		got[f.Path] = true
	}
	if !got["a.txt"] || !got["src/b.go"] || got["c.txt"] || got["d.md"] {
		t.Errorf("staged: %v", got)
	}

	unstaged, err := r.Unstaged(Options{})
	if err != nil {
		t.Fatal(err)
	}
	got = map[string]bool{}
	for _, f := range unstaged {
		got[f.Path] = true
	}
	if got["a.txt"] || !got["src/b.go"] || !got["c.txt"] || !got["d.md"] {
		t.Errorf("unstaged: %v", got)
	}

	// The index holds the staged version of b.go, not the work tree's.
	c, err := r.NewContents()
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	data, err := c.Read(IndexRev, "src/b.go")
	if err != nil || string(data) != "package b\nstaged\n" {
		t.Errorf("index version %q %v", data, err)
	}
}
