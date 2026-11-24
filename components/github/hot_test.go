package github_test

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"

	"kern/components/github"
	"kern/git"
	"kern/home"
	"kern/index"
	"kern/index/fixture"
	"kern/kerntest"
)

// The GitHub component's hot test (README.md "Hot tests"): every expectation
// the checkout's readers pool under the names it claims (expect.toml), met
// against GitHub itself and recorded beside the expectation it met. Its run:
//
//	KERN_HOT=1 go test -count=1 -run '^TestHot$' ./components/github/
//
// Beside it, the normal suite meets the same pool against the fake REST server.

// root is the checkout this package lies in.
var root = filepath.Join("..", "..")

// module is the component as it reaches api with tok, the given "issue 1
// is read" reading issue 1 of owner/repo there.
func module(api string, tok func() (string, error), owner, repo string, now func() time.Time) kerntest.Module {
	return kerntest.Module{Component: github.Component.Component, File: "expect.toml",
		Fresh: func() *index.Component {
			return github.New(github.Options{Proxy: noProxy, Clock: nowClock(now), Token: tok, Cloud: notCloud}).Component
		},
		Start: func(f *kerntest.Fake) error {
			for k, v := range map[string]string{"backend": github.Token, "api": api, "refresh": "0s"} {
				if err := f.Config(k, v); err != nil {
					return err
				}
			}
			return f.Start("github")
		},
		Given: map[string]kerntest.Setup{
			"issue 1 is read": func(f *kerntest.Fake) error {
				_, err := f.BeginServe("issue", github.Ref{Owner: owner, Repo: repo, Number: 1}).Wait()
				return err
			},
		}}
}

// hotOf is the component's hot test reaching api with tok: the readers'
// placeholders owner and repo are asked as the home's owner and repository,
// and the home is written back as the placeholders in every answer.
func hotOf(api string, tok func() (string, error), owner, repo string, now func() time.Time) kerntest.Hot {
	return kerntest.Hot{Name: "github", Module: module(api, tok, owner, repo, now), Now: now,
		Ask:    placeholders(map[string]string{"owner": owner, "repo": repo}),
		Answer: homeless(owner, repo),
	}
}

// placeholders makes a reader's ask the one GitHub is asked: a table's owner
// and repo that name the placeholder owner or repo name the home's instead.
func placeholders(home map[string]string) func(any) any {
	return func(v any) any {
		m, ok := v.(map[string]any)
		if !ok {
			return v
		}
		out := map[string]any{}
		for k, x := range m {
			if s, ok := x.(string); ok && home[k] != "" && s == k {
				x = home[k]
			}
			out[k] = x
		}
		return out
	}
}

// homeless makes GitHub's answer the one a reader is answered: the home's
// <owner>/<repo> and <owner>.<repo>, in any case, written owner/repo and
// owner.repo in every text and key, and a text that is the owner or the
// repository alone written owner or repo. So no recording names the home.
func homeless(owner, repo string) func(any) any {
	pairs := regexp.MustCompile(`(?i)\b` + regexp.QuoteMeta(owner) + `([/.])` + regexp.QuoteMeta(repo) + `\b`)
	text := func(s string) string {
		switch {
		case strings.EqualFold(s, owner):
			return "owner"
		case strings.EqualFold(s, repo):
			return "repo"
		}
		return pairs.ReplaceAllString(s, "owner${1}repo")
	}
	var walk func(any) any
	walk = func(v any) any {
		switch x := v.(type) {
		case string:
			return text(x)
		case map[string]any:
			out := map[string]any{}
			for k, item := range x {
				out[text(k)] = walk(item)
			}
			return out
		case []any:
			out := make([]any, len(x))
			for i, item := range x {
				out[i] = walk(item)
			}
			return out
		}
		return v
	}
	return walk
}

// pool is every expectation file of the checkout, pooled by name.
func pool(t *testing.T) kerntest.Pool {
	t.Helper()
	files, err := kerntest.ExpectationFiles(root)
	if err != nil {
		t.Fatal(err)
	}
	p, err := kerntest.ReadPool(files...)
	if err != nil {
		t.Fatal(err)
	}
	if len(p["github.issue"]) == 0 || len(p["github.issues"]) == 0 {
		t.Fatalf("no reader under %s expects github.issue and github.issues", root)
	}
	return p
}

// TestHot is the hot test: it runs where KERN_HOT=1 alone, against GitHub's
// REST API (KERN_HOT_GITHUB_API, else api.github.com) with the token the
// component finds, reading the checkout's home from its origin, and writes
// each reader's recordings.
func TestHot(t *testing.T) {
	if os.Getenv("KERN_HOT") != "1" {
		t.Skip("the hot test reaches GitHub: run it with KERN_HOT=1")
	}
	restore := git.Use(git.Real{})
	h, err := home.Of(context.Background(), root)
	restore()
	if err != nil {
		t.Fatal(err)
	}
	api := os.Getenv("KERN_HOT_GITHUB_API")
	if api == "" {
		api = "https://api.github.com"
	}
	written, err := hotOf(api, github.FindToken, h.Owner, h.Repository, time.Now).Record(pool(t))
	for _, w := range written {
		t.Logf("recorded %s", filepath.ToSlash(w))
	}
	if err != nil {
		t.Fatal(err)
	}
}

// The normal suite's leg: the component meets the readers' pooled
// expectations against the fake REST server, which holds the readers'
// placeholder repository owner/repo, and a wiring that loads it and the
// watcher is complete. Through the hot test's asks and answers it meets them
// for another home, Alice/Demo, and no recording names that home.
func TestTheComponentMeetsThePooledExpectationsAgainstItsFake(t *testing.T) {
	t.Parallel()
	f, srv := newFake(t)
	f.mu.Lock()
	for _, home := range []string{"owner/repo", "alice/demo"} {
		f.bodies["/repos/"+home+"/issues/1"] = `{"number": 1, "title": "The first", "state": "closed", "html_url": "https://github.com/` + home + `/issues/1", "user": {"login": "alice"}}`
	}
	f.mu.Unlock()
	now := func() time.Time { return nine }
	p := pool(t)
	if err := module(srv.URL, token(""), "owner", "repo", now).Meet(p); err != nil {
		t.Fatal(err)
	}
	w, err := index.ParseWiring([]byte(`
[instances.github]
component = "github"

[instances.watcher]
component = "watcher"

[wires]
"github.second" = "built-in"
"github.issues" = "github/issues/<key>"
"watcher.issues" = "github/issues/<key>"
"watcher.issue" = "github.issue"
`))
	if err != nil {
		t.Fatal(err)
	}
	watcher := kerntest.Module{Component: fixture.Watcher, File: filepath.Join(root, "index", "fixture", "watcher.expect.toml")}
	if err := kerntest.Check(w, watcher); err == nil || !strings.Contains(err.Error(), `unclaimed: name "github.issue"`) {
		t.Fatalf("a wiring whose watcher's names no one claims is complete: %v", err)
	}
	if err := kerntest.Check(w, watcher, module(srv.URL, token(""), "owner", "repo", now)); err != nil {
		t.Fatalf("a wiring that loads the component and the watcher: %v", err)
	}

	recs, err := hotOf(srv.URL, token(""), "Alice", "Demo", now).Meet(p)
	if err != nil {
		t.Fatal(err)
	}
	byName := map[string]kerntest.Recording{}
	for _, r := range recs {
		byName[r.Name] = r
	}
	got, _ := json.Marshal(byName["github.issue"].Answer)
	if want := `{"data":{"state":"closed","title":"The first"},"number":1,"read":"2026-11-13T09:00:00Z","repo":"owner/repo"}`; string(got) != want {
		t.Errorf("the issue's recording holds %s, want %s", got, want)
	}
	got, _ = json.Marshal(byName["github.issues"].Value)
	if want := `{"owner.repo.1":{"data":{"state":"closed","title":"The first"},"number":1,"read":"2026-11-13T09:00:00Z","repo":"owner/repo"}}`; string(got) != want {
		t.Errorf("the issues name's recording holds %s, want %s", got, want)
	}
	if text := strings.ToLower(fmt.Sprint(recs)); strings.Contains(text, "alice") || strings.Contains(text, "demo") {
		t.Errorf("a recording names the home: %s", text)
	}
}
