package fileicon

import "testing"

func TestToken(t *testing.T) {
	for path, want := range map[string]string{
		"main.go":                   "go",
		"web/src/App.tsx":           "react",
		"web/src/app.ts":            "typescript",
		"types/global.d.ts":         "typescript",
		"lib/index.mjs":             "javascript",
		"Dockerfile":                "docker",
		"deploy/docker-compose.yml": "docker",
		".gitignore":                "git",
		"README.md":                 "markdown",
		"pnpm-lock.yaml":            "yml",
		"bun.lock":                  "bun",
		"vite.config.ts":            "vite",
		"styles/site.scss":          "sass",
		"logo.PNG":                  "image",
		"LICENSE":                   "default",
		"Makefile.unknown-kind":     "default",
	} {
		if got := Token(path); got != want {
			t.Errorf("%s: %s, want %s", path, got, want)
		}
	}
}

func TestIcons(t *testing.T) {
	for token := range symbols {
		i := For("x." + token)
		if i == nil {
			t.Fatalf("no icon for %s", token)
		}
		ic := icons[token]
		if ic.SVG == nil || ic.Light.A == 0 || ic.Dark.A == 0 {
			t.Errorf("%s: %+v", token, ic)
		}
	}
	if i := For("main.go"); i.Token != "go" || i.Color(true) == i.Color(false) {
		t.Errorf("go icon %+v", i)
	}
}

// TestEveryTokenHasIcon keeps a token of byName, byExtension or overrides
// from naming an icon the data lacks, as zip did: drawing such a file would
// dereference a nil icon.
func TestEveryTokenHasIcon(t *testing.T) {
	once.Do(load)
	for _, m := range []struct {
		name   string
		tokens map[string]string
	}{{"byName", byName}, {"byExtension", byExtension}, {"overrides", overrides}} {
		for key, token := range m.tokens {
			if icons[token] == nil {
				t.Errorf("%s[%q] is %q, which has no icon", m.name, key, token)
			}
		}
	}
}

func TestUnknownKindGetsDefault(t *testing.T) {
	if i := For("dist/dist.tar.gz"); i == nil || i.Token != "zip" {
		t.Errorf("archive icon: %+v", i)
	}
	if i := For("x.unknown-kind"); i == nil || i.Token != "default" {
		t.Errorf("unknown icon: %+v", i)
	}
}
