package github_test

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"

	"kern/components/github"
	"kern/index"
)

// noProxy is an environment that names no HTTPS proxy: the proxy backend is
// never probed in it.
func noProxy(*http.Request) (*url.URL, error) { return nil, nil }

// hermetic is o in an environment without an HTTPS proxy where o names none,
// so no case probes the proxy of the container it runs in.
func hermetic(o github.Options) github.Options {
	if o.Proxy == nil {
		o.Proxy = noProxy
	}
	return o
}

// api is the REST API the proxy cases read: plain HTTP, so the fake proxy is
// handed each request whole, as a cloud session's proxy sees it once it ends
// the TLS of api.github.com.
const api = "http://api.github.test"

// fakeProxy is a cloud session's HTTPS proxy, faked: it adds the credentials
// to each REST request and hands it to the fake GitHub, and refuses GraphQL,
// as the session's proxy does. A request that carries an Authorization of its
// own is refused, since no token reaches the session. Where rest is false it
// refuses REST too.
type fakeProxy struct {
	mu   sync.Mutex
	seen []string // each request: its method, host and path, and "own Authorization" where it sent one
}

func newProxy(t *testing.T, gitHub http.Handler, rest bool) (*fakeProxy, func(*http.Request) (*url.URL, error)) {
	t.Helper()
	p := &fakeProxy{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		line := r.Method + " " + r.URL.Host + r.URL.Path
		own := r.Header.Get("Authorization") != ""
		if own {
			line += " own Authorization"
		}
		p.mu.Lock()
		p.seen = append(p.seen, line)
		p.mu.Unlock()
		switch {
		case own:
			http.Error(w, "the session holds no token", http.StatusBadRequest)
		case r.URL.Path == "/graphql" || !rest:
			http.Error(w, "refused by the session's proxy", http.StatusForbidden)
		default:
			r.Header.Set("Authorization", "Bearer test-token")
			gitHub.ServeHTTP(w, r)
		}
	}))
	t.Cleanup(srv.Close)
	to, err := url.Parse(srv.URL)
	if err != nil {
		t.Fatal(err)
	}
	return p, http.ProxyURL(to)
}

func (p *fakeProxy) log() []string {
	p.mu.Lock()
	defer p.mu.Unlock()
	return append([]string{}, p.seen...)
}

// proxied is the fake GitHub behind a proxy that answers REST: its
// rate_limit answers too.
func proxied(t *testing.T) (*fakeGitHub, *fakeProxy, func(*http.Request) (*url.URL, error)) {
	t.Helper()
	f, srv := newFake(t)
	f.bodies["/rate_limit"] = `{"resources": {"core": {"limit": 5000, "remaining": 4999}}}`
	p, proxy := newProxy(t, srv.Config.Handler, true)
	return f, p, proxy
}

// Where GET /rate_limit through the environment's proxy answers 200 without a
// token, auto takes the proxy, in a cloud session (whose token is the proxy's
// stand-in) and outside one where no token is found: a read runs in process
// over REST, with no Authorization of kern's own and its ETag the second
// time, the snapshot names the proxy, and no outbox entry is written.
func TestAutoTakesTheProxyWhereRESTAnswersThroughIt(t *testing.T) {
	t.Parallel()
	for name, o := range map[string]github.Options{
		"a cloud session":            {Token: token(""), Cloud: func() bool { return true }},
		"no token outside the cloud": {Token: noToken, Cloud: notCloud},
	} {
		t.Run(name, func(t *testing.T) {
			f, p, proxy := proxied(t)
			clk := &clock{now: nine}
			o.Clock, o.Proxy = clk, proxy
			g, ix := started(t, o, wiring(t, fmt.Sprintf(`api = %q`, api)))
			if v, _ := ix.Read("github/backend"); v.V != github.Proxy {
				t.Fatalf("github/backend reads %v, and wants proxy where REST answers through it", v.V)
			}
			outbox := steps(t, ix, "github/outbox/issue.alice.demo.7")
			r, err := ix.Call("read", "issue", "alice", "demo", "7")
			if err != nil || r.Text != "One issue, read 2026-11-13T09:00:00Z via proxy" {
				t.Fatalf("read answers %q, %v, and wants the issue read in process via proxy", r.Text, err)
			}
			clk.move(time.Minute)
			if r, err = ix.Call("read", "issue", "alice", "demo", "7"); err != nil || !strings.Contains(r.Text, "09:01:00Z via proxy") {
				t.Fatalf("the second read answers %q, %v", r.Text, err)
			}
			select {
			case s := <-outbox:
				t.Fatalf("a read on the proxy wrote the outbox entry %+v, and wants none", s)
			default:
			}
			if v, _ := ix.Read("github/outbox/issue.alice.demo.7"); v.Provided || g.Outbox() != "" {
				t.Fatalf("the outbox holds %+v (%q), and wants nothing", v.V, g.Outbox())
			}
			asked, answered := f.log()
			want := []string{"/rate_limit", "/repos/alice/demo/issues/7", `/repos/alice/demo/issues/7 "34"`}
			if fmt.Sprint(asked) != fmt.Sprint(want) || fmt.Sprint(answered) != "[200 200 304]" {
				t.Fatalf("GitHub was asked %q and answered %v, and wants %q answered [200 200 304]", asked, answered, want)
			}
			seen := p.log()
			if len(seen) != 3 || seen[0] != "GET api.github.test/rate_limit" {
				t.Fatalf("the proxy saw %q, and wants the probe first and the two reads, each without an Authorization of kern's own", seen)
			}
			for _, s := range seen {
				if strings.Contains(s, "own Authorization") || strings.Contains(s, "graphql") {
					t.Fatalf("the proxy saw %q, and wants REST alone, without an Authorization of kern's own", seen)
				}
			}
		})
	}
}

// Where the proxy refuses REST, auto takes the relay, as it does without a
// proxy: a read goes out as an outbox step. A backend named proxy is refused
// at start, naming the probe's answer.
func TestWhereTheProxyRefusesRESTAutoTakesTheRelay(t *testing.T) {
	t.Parallel()
	f, srv := newFake(t)
	f.bodies["/rate_limit"] = `{}`
	p, proxy := newProxy(t, srv.Config.Handler, false)
	cloud := func() bool { return true }
	_, ix := started(t, github.Options{Clock: &clock{now: nine}, Token: token(""), Cloud: cloud, Proxy: proxy},
		wiring(t, fmt.Sprintf(`api = %q`, api)))
	if v, _ := ix.Read("github/backend"); v.V != github.Relay {
		t.Fatalf("github/backend reads %v, and wants relay where the proxy refuses REST", v.V)
	}
	if _, err := ix.Call("read", "issue", "alice", "demo", "7"); err == nil || !strings.Contains(err.Error(), "outbox step issue.alice.demo.7") {
		t.Fatalf("the read answers %v, and wants the relay's outbox step", err)
	}
	if v, _ := ix.Read("github/outbox/issue.alice.demo.7"); !v.Provided {
		t.Fatal("the relay's read wrote no outbox entry")
	}
	if seen := p.log(); fmt.Sprint(seen) != "[GET api.github.test/rate_limit]" {
		t.Fatalf("the proxy saw %q, and wants the probe alone", seen)
	}
	for name, proxy := range map[string]func(*http.Request) (*url.URL, error){"a proxy that refuses REST": proxy, "no proxy": noProxy} {
		g := github.New(github.Options{Clock: &clock{now: nine}, Token: noToken, Cloud: cloud, Proxy: proxy})
		_, err := index.Start(wiring(t, fmt.Sprintf(`backend = "proxy", api = %q`, api)), g.Component, reader)
		if err == nil || !strings.Contains(err.Error(), "config backend is proxy, and ") {
			t.Fatalf("with %s a start on backend proxy answers %v, and wants it refused naming the probe", name, err)
		}
	}
}

// On the proxy every read and write runs in process as on the token: a
// rate limit is sat out, a list is read page by page, a write posts to
// REST, and no outbox entry or step is asked.
func TestTheProxyReadsListsWritesAndPacesInProcess(t *testing.T) {
	t.Parallel()
	f, srv := newFake(t)
	f.bodies["/rate_limit"] = `{}`
	f.bodies["/repos/alice/demo/pulls?page=1&per_page=100&state=open"] = `[{"number": 8}]`
	f.created["/repos/alice/demo/issues/7/comments"] = `{"id": 99, "html_url": "https://github.com/alice/demo/issues/7#issuecomment-99"}`
	f.refuseAt["/repos/alice/demo/issues/7"] = []func(http.ResponseWriter){func(w http.ResponseWriter) {
		w.Header().Set("Retry-After", "3")
		w.WriteHeader(http.StatusTooManyRequests)
	}}
	p, proxy := newProxy(t, srv.Config.Handler, true)
	clk := &clock{now: nine}
	var slept []time.Duration
	o := github.Options{Clock: clk, Token: noToken, Cloud: notCloud, Proxy: proxy,
		Sleep: func(d time.Duration) { slept = append(slept, d); clk.move(d) }}

	ix := startedLists(t, o, listWiring(t, fmt.Sprintf(`api = %q`, api)))
	if r, err := ix.Call("list", "pulls", "alice", "demo", "open"); err != nil || r.Text != "[8], read 2026-11-13T09:00:00Z via proxy" {
		t.Fatalf("list pulls answers %q, %v, and wants the list read in process via proxy", r.Text, err)
	}

	g, wix := writing(t, o, fmt.Sprintf(`api = %q`, api), &acceptor{})
	if r, err := wix.Call("github issue-comment", "--repo", "alice/demo", "--number", "7", "--filled", jsonOf(t, fillOf("note", "", "Read."))); err != nil || !strings.Contains(r.Text, "issuecomment-99") {
		t.Fatalf("github issue-comment answers %q, %v, and wants the comment posted through the proxy", r.Text, err)
	}
	if paths, sent := posts(t, f); len(paths) != 1 || paths[0] != "/repos/alice/demo/issues/7/comments" || sent[0]["body"] != "Read." {
		t.Fatalf("GitHub was sent %v %v, and wants the comment", paths, sent)
	}
	if g.Outbox() != "" {
		t.Fatalf("the write asked %q, and wants no step", g.Outbox())
	}

	_, rix := started(t, o, wiring(t, fmt.Sprintf(`api = %q`, api)))
	if r, err := rix.Call("read", "issue", "alice", "demo", "7"); err != nil || !strings.HasPrefix(r.Text, "One issue") || fmt.Sprint(slept) != "[3s]" {
		t.Fatalf("read answers %q, %v after sleeping %v, and wants the issue after the 3s Retry-After asks", r.Text, err, slept)
	}
	for _, s := range p.log() {
		if strings.Contains(s, "own Authorization") {
			t.Fatalf("the proxy saw %q, and wants no Authorization of kern's own", p.log())
		}
	}
}

// On the proxy a session start tells the agent that no GitHub tool call waits
// on kern's outbox; on the relay with nothing waiting it tells nothing.
func TestTheProxyTellsTheAgentThereIsNoOutboxStep(t *testing.T) {
	t.Parallel()
	_, _, proxy := proxied(t)
	_, ix := started(t, github.Options{Clock: &clock{now: nine}, Token: noToken, Cloud: notCloud, Proxy: proxy},
		wiring(t, fmt.Sprintf(`api = %q`, api)))
	if v, _ := ix.Read("github/backend"); v.V != github.Proxy {
		t.Fatalf("github/backend reads %v, and wants proxy", v.V)
	}
	told := strings.Join(ix.Tell("SessionStart", []byte(`{"session_id": "s1"}`)), "\n")
	if told != github.ProxyTell || !strings.Contains(told, "backend proxy") || !strings.Contains(told, "no GitHub tool call") {
		t.Fatalf("a session start on the proxy tells %q, and wants it to say there is no outbox step", told)
	}
	if again := ix.Tell(github.PostToolUse, toolUse("mcp__github__issue_read", issueRead, `{"number": 7}`)); len(again) != 0 {
		t.Fatalf("a tool answer on the proxy tells %q, and wants nothing", again)
	}
	_, rix := started(t, github.Options{Clock: &clock{now: nine}, Token: noToken, Cloud: notCloud}, wiring(t, ``))
	if told := rix.Tell("SessionStart", []byte(`{"session_id": "s1"}`)); len(told) != 0 {
		t.Fatalf("a session start on the relay with nothing waiting tells %q, and wants nothing", told)
	}
}
