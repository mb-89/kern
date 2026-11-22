package github_test

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"kern/bus"
	"kern/components/github"
	"kern/host"
	"kern/index"
)

// clock is the component's clock; the case moves it. A wait on it ends when
// the case moves it past the wait's end, a wait of 0 or less at once: no case
// waits on the wall clock for one.
type clock struct {
	mu    sync.Mutex
	now   time.Time
	waits []wait
}

type wait struct {
	end time.Time
	c   chan time.Time
}

func (c *clock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.now
}

func (c *clock) After(d time.Duration) <-chan time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	ch := make(chan time.Time, 1)
	if d <= 0 {
		ch <- c.now
		return ch
	}
	c.waits = append(c.waits, wait{c.now.Add(d), ch})
	return ch
}

// pending answers how many waits on the clock have not ended.
func (c *clock) pending() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return len(c.waits)
}

func (c *clock) move(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.now = c.now.Add(d)
	kept := c.waits[:0]
	for _, w := range c.waits {
		if c.now.Before(w.end) {
			kept = append(kept, w)
			continue
		}
		w.c <- c.now
	}
	c.waits = kept
}

// nowClock is the hot test's clock: its time from now, its waits the wall clock's.
type nowClock func() time.Time

func (n nowClock) Now() time.Time                       { return n() }
func (nowClock) After(d time.Duration) <-chan time.Time { return time.After(d) }

// beat is the clock's second as the case writes it (components/clock), a
// count that counts up: an I/O component whose one output the wiring binds
// to beat/second.
type beat struct {
	component *index.Component
	commit    index.Commit
}

var secondOut = index.Output("second", 0, "The clock's second, a count, as the case writes it.")

func newBeat() *beat {
	b := &beat{}
	b.component = index.Declare("beat", "Writes the clock's second as the case moves it.", index.IO(), secondOut,
		index.Inlet(func(_ string, _ index.Reader, commit index.Commit) error { b.commit = commit; return nil }))
	return b
}

// at commits the second's count.
func (b *beat) at(t *testing.T, count int) {
	t.Helper()
	if err := b.commit(secondOut.Set(count)); err != nil {
		t.Fatal(err)
	}
}

// beating makes a wiring source's second the beat's: its instance, and the wires
// of beat/second, in place of the built-in second.
func beating(t *testing.T, src string) index.Wiring {
	t.Helper()
	text := strings.Replace(src, `"github.second" = "built-in"`, `"github.second" = "beat/second"
"beat.second" = "beat/second"`, 1)
	out, err := index.ParseWiring([]byte("[instances.beat]\ncomponent = \"beat\"\n\n" + text))
	if err != nil {
		t.Fatal(err)
	}
	return out
}

var nine = time.Date(2026, 11, 13, 9, 0, 0, 0, time.UTC)

// reader is another component, which reads GitHub through the component's
// verbs alone: its command `read <verb> <owner> <repo> <n>` shows the title or
// the count of check runs it is answered.
var reader = func() *index.Component {
	issue := index.Request[github.Ref, github.Snapshot]("issue", "Reads an issue.")
	pull := index.Request[github.Ref, github.Snapshot]("pull", "Reads a pull request.")
	checks := index.Request[github.Ref, github.Snapshot]("checks", "Reads a pull request's check runs.")
	ports := map[string]*index.RequestPort[github.Ref, github.Snapshot]{"issue": issue, "pull": pull, "checks": checks}
	return index.Declare("reader", "Reads GitHub through the github component.", issue, pull, checks,
		index.Command(index.CommandDecl{
			Name: "read", Usage: "read VERB OWNER REPO N", Summary: "Read one thing.", Help: "Shows what it reads.",
			Group: "Fixtures", Class: index.Batch,
		}, func(_ struct{}, args []string) (index.Result, error) {
			var n int
			fmt.Sscanf(args[3], "%d", &n)
			p := ports[args[0]]
			return index.Result{Requests: []index.Ask{p.Ask(github.Ref{Owner: args[1], Repo: args[2], Number: n})}, Then: func(a []any) (index.Result, error) {
				s := p.Answer(a[0])
				data, _ := s.Data.(map[string]any)
				if args[0] == "checks" {
					return index.Result{Text: fmt.Sprintf("%v check runs, read %s via %s", data["total_count"], s.Read.Format(time.RFC3339), s.Via)}, nil
				}
				return index.Result{Text: fmt.Sprintf("%v, read %s via %s", data["title"], s.Read.Format(time.RFC3339), s.Via)}, nil
			}}, nil
		}),
	)
}()

// wiring is a checkout's wiring with the component and the reader.
func wiring(t *testing.T, config string) index.Wiring {
	t.Helper()
	w, err := index.ParseWiring([]byte(wiringSrc(config)))
	if err != nil {
		t.Fatal(err)
	}
	return w
}

func wiringSrc(config string) string {
	return fmt.Sprintf(`
[instances.github]
component = "github"
config = { %s }

[instances.reader]
component = "reader"

[wires]
"github.second" = "built-in"
"github.issues" = "github/issues/<key>"
"github.pulls" = "github/pulls/<key>"
"github.checks" = "github/checks/<key>"
"github.outbox" = "github/outbox/<key>"
"reader.issue" = "github.issue"
"reader.pull" = "github.pull"
"reader.checks" = "github.check-runs"
`, config)
}

// fakeGitHub is GitHub's REST API as the token backend reads it: an issue,
// a pull request and its check runs, each with an ETag, and the refusals a
// case queues before them. A list's page stands at its path and query, and
// links holds the Link header of each page that has a next.
type fakeGitHub struct {
	mu       sync.Mutex
	bodies   map[string]string
	links    map[string]string
	refusals []func(http.ResponseWriter)
	refuseAt map[string][]func(http.ResponseWriter) // refusals queued before one path's answer
	asked    []string                               // each request: its path, and its If-None-Match where it sent one
	answered []int
	created  map[string]string // the answer to a POST, by its path
	posted   []string          // each POST: its path and its JSON body
}

func newFake(t *testing.T) (*fakeGitHub, *httptest.Server) {
	f := &fakeGitHub{bodies: map[string]string{
		"/repos/alice/demo/issues/7":                  `{"number": 7, "title": "One issue", "state": "open"}`,
		"/repos/alice/demo/pulls/8":                   `{"number": 8, "title": "One pull", "head": {"sha": "abc123"}}`,
		"/repos/alice/demo/commits/abc123/check-runs": `{"total_count": 2, "check_runs": [{"name": "check"}, {"name": "kern-windows"}]}`,
	}, links: map[string]string{}, refuseAt: map[string][]func(http.ResponseWriter){}, created: map[string]string{}}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		defer f.mu.Unlock()
		path := r.URL.Path
		if r.URL.RawQuery != "" {
			path += "?" + r.URL.RawQuery
		}
		f.asked = append(f.asked, strings.TrimSpace(path+" "+r.Header.Get("If-None-Match")))
		if r.Header.Get("Authorization") != "Bearer test-token" {
			w.WriteHeader(http.StatusUnauthorized)
			f.answered = append(f.answered, http.StatusUnauthorized)
			return
		}
		if len(f.refusals) > 0 {
			f.refusals[0](w)
			f.refusals = f.refusals[1:]
			f.answered = append(f.answered, -1)
			return
		}
		if q := f.refuseAt[path]; len(q) > 0 {
			q[0](w)
			f.refuseAt[path] = q[1:]
			f.answered = append(f.answered, -1)
			return
		}
		if r.Method == http.MethodPost {
			data, _ := io.ReadAll(r.Body)
			f.posted = append(f.posted, path+" "+string(data))
			answer, ok := f.created[path]
			if !ok {
				w.WriteHeader(http.StatusNotFound)
				f.answered = append(f.answered, http.StatusNotFound)
				return
			}
			w.WriteHeader(http.StatusCreated)
			fmt.Fprint(w, answer)
			f.answered = append(f.answered, http.StatusCreated)
			return
		}
		body, ok := f.bodies[strings.ToLower(path)] // GitHub reads names regardless of case
		if !ok {
			w.WriteHeader(http.StatusNotFound)
			f.answered = append(f.answered, http.StatusNotFound)
			return
		}
		etag := fmt.Sprintf(`"%x"`, len(body))
		if r.Header.Get("If-None-Match") == etag {
			w.WriteHeader(http.StatusNotModified)
			f.answered = append(f.answered, http.StatusNotModified)
			return
		}
		w.Header().Set("ETag", etag)
		if link := f.links[path]; link != "" {
			w.Header().Set("Link", link)
		}
		fmt.Fprint(w, body)
		f.answered = append(f.answered, http.StatusOK)
	}))
	t.Cleanup(srv.Close)
	return f, srv
}

func (f *fakeGitHub) log() ([]string, []int) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string{}, f.asked...), append([]int{}, f.answered...)
}

// started starts an index with a component made of o and the reader.
func started(t *testing.T, o github.Options, w index.Wiring) (*github.GitHub, *index.Index) {
	t.Helper()
	g := github.New(o)
	ix, err := index.Start(w, g.Component, reader)
	if err != nil {
		t.Fatal(err)
	}
	return g, ix
}

func snapshot(t *testing.T, ix *index.Index, name string) github.Snapshot {
	t.Helper()
	v, err := ix.Read(name)
	if err != nil {
		t.Fatal(err)
	}
	if !v.Provided {
		t.Fatalf("%s reads its built-in value, and wants a snapshot", name)
	}
	return v.V.(github.Snapshot)
}

func token(string) func() (string, error) {
	return func() (string, error) { return "test-token", nil }
}

func noToken() (string, error) { return "", nil }

func notCloud() bool { return false }

// The token backend reads through REST: another component's command reads
// an issue through the verb, the snapshot name holds it with its read time,
// and a second read sends the ETag and is answered 304 from what it holds.
func TestTheTokenBackendReadsWithETags(t *testing.T) {
	t.Parallel()
	f, srv := newFake(t)
	clk := &clock{now: nine}
	_, ix := started(t, github.Options{Clock: clk, Token: token(""), Cloud: notCloud},
		wiring(t, fmt.Sprintf(`backend = "auto", api = %q`, srv.URL)))
	if v, _ := ix.Read("github/backend"); v.V != github.Token {
		t.Fatalf("github/backend reads %v, and wants token where a token is found", v.V)
	}
	r, err := ix.Call("read", "issue", "Alice", "demo", "7")
	if err != nil || r.Text != "One issue, read 2026-11-13T09:00:00Z via token" {
		t.Fatalf("read answers %q, %v", r.Text, err)
	}
	s := snapshot(t, ix, "github/issues/alice.demo.7")
	if s.Read != nine || s.Via != github.Token || s.Repo != "alice/demo" || s.Number != 7 {
		t.Fatalf("the snapshot is %+v", s)
	}
	clk.move(time.Minute)
	if r, err = ix.Call("read", "issue", "alice", "demo", "7"); err != nil || !strings.Contains(r.Text, "09:01:00Z") {
		t.Fatalf("the second read answers %q, %v", r.Text, err)
	}
	asked, answered := f.log()
	want := []string{"/repos/alice/demo/issues/7", `/repos/alice/demo/issues/7 "34"`}
	if fmt.Sprint(asked) != fmt.Sprint(want) || fmt.Sprint(answered) != "[200 304]" {
		t.Fatalf("GitHub was asked %q and answered %v, and wants %q answered [200 304]", asked, answered, want)
	}
}

// A pull request's check runs are read at its head, and land beside the pull request.
func TestTheTokenBackendReadsAPullsCheckRuns(t *testing.T) {
	t.Parallel()
	_, srv := newFake(t)
	_, ix := started(t, github.Options{Clock: &clock{now: nine}, Token: token(""), Cloud: notCloud},
		wiring(t, fmt.Sprintf(`backend = "token", api = %q`, srv.URL)))
	r, err := ix.Call("read", "checks", "alice", "demo", "8")
	if err != nil || r.Text != "2 check runs, read 2026-11-13T09:00:00Z via token" {
		t.Fatalf("read checks answers %q, %v", r.Text, err)
	}
	snapshot(t, ix, "github/checks/alice.demo.8")
	if r, err = ix.Call("read", "pull", "alice", "demo", "8"); err != nil || !strings.HasPrefix(r.Text, "One pull,") {
		t.Fatalf("read pull answers %q, %v", r.Text, err)
	}
	snapshot(t, ix, "github/pulls/alice.demo.8")
}

// The refresh reads each snapshot the token holds again, with its ETag, and
// a changed answer moves the snapshot. It runs on the clock's second, a
// count: the case writes the count itself, and the relay holds the refresh
// back until refresh, 2m, has passed in counts since the first one, 120; no
// case waits on the wall clock.
func TestTheRefreshReadsEverySnapshotAgain(t *testing.T) {
	t.Parallel()
	f, srv := newFake(t)
	clk := &clock{now: nine}
	b := newBeat()
	g := github.New(github.Options{Clock: clk, Token: token(""), Cloud: notCloud, Go: func(fn func()) { fn() }})
	ix, err := index.Start(beating(t, wiringSrc(fmt.Sprintf(`api = %q`, srv.URL))), g.Component, reader, b.component)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := ix.Call("read", "issue", "alice", "demo", "7"); err != nil {
		t.Fatal(err)
	}
	f.mu.Lock()
	f.bodies["/repos/alice/demo/issues/7"] = `{"number": 7, "title": "One issue, renamed", "state": "closed"}`
	f.mu.Unlock()
	reads := func() string { _, answered := f.log(); return fmt.Sprint(answered) }
	for _, count := range []int{5, 6, 65, 124} {
		b.at(t, count)
		if got := reads(); got != "[200]" {
			t.Fatalf("at the second %d GitHub answered %v, and wants no refresh before 120 counts from 5: [200]", count, got)
		}
	}
	clk.move(2 * time.Minute)
	b.at(t, 125)
	s := snapshot(t, ix, "github/issues/alice.demo.7")
	if s.Read != nine.Add(2*time.Minute) || s.Data.(map[string]any)["title"] != "One issue, renamed" {
		t.Fatalf("after the refresh the snapshot is %+v", s)
	}
	if got := reads(); got != "[200 200]" {
		t.Fatalf("GitHub answered %v, and wants the changed issue read again: [200 200]", got)
	}
	b.at(t, 185)
	if got := reads(); got != "[200 200]" {
		t.Fatalf("a second 60 counts after the refresh refreshed again: GitHub answered %v", got)
	}
}

// A wiring that leaves the second built-in, or a refresh of 0s, refreshes nothing.
func TestNoSecondOrNoRefreshRefreshesNothing(t *testing.T) {
	t.Parallel()
	f, srv := newFake(t)
	b := newBeat()
	g := github.New(github.Options{Clock: &clock{now: nine}, Token: token(""), Cloud: notCloud, Go: func(fn func()) { fn() }})
	ix, err := index.Start(beating(t, wiringSrc(fmt.Sprintf(`api = %q, refresh = "0s"`, srv.URL))), g.Component, reader, b.component)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := ix.Call("read", "issue", "alice", "demo", "7"); err != nil {
		t.Fatal(err)
	}
	for _, count := range []int{0, 3600, 7200} {
		b.at(t, count)
	}
	if _, answered := f.log(); fmt.Sprint(answered) != "[200]" {
		t.Fatalf("a refresh of 0s refreshed: GitHub answered %v", answered)
	}
}

// The token backend paces on 403 and 429: it waits what Retry-After or the
// quota's reset asks, and every read waits with it; a 403 that is no rate
// limit fails at once, and a wait over pace-max fails the read.
func TestTheTokenBackendPacesOnRateLimits(t *testing.T) {
	t.Parallel()
	for _, c := range []struct {
		name    string
		refusal func(w http.ResponseWriter)
		slept   string
		err     string
	}{
		{"429 with Retry-After", func(w http.ResponseWriter) {
			w.Header().Set("Retry-After", "3")
			w.WriteHeader(http.StatusTooManyRequests)
		}, "[3s]", ""},
		{"403 with the quota spent", func(w http.ResponseWriter) {
			w.Header().Set("X-RateLimit-Remaining", "0")
			w.Header().Set("X-RateLimit-Reset", fmt.Sprint(nine.Add(40*time.Second).Unix()))
			w.WriteHeader(http.StatusForbidden)
		}, "[40s]", ""},
		{"403 of a secondary limit", func(w http.ResponseWriter) {
			w.Header().Set("Retry-After", "20")
			w.WriteHeader(http.StatusForbidden)
		}, "[20s]", ""},
		{"403 that is no limit", func(w http.ResponseWriter) {
			w.WriteHeader(http.StatusForbidden)
			fmt.Fprint(w, `{"message": "Resource not accessible"}`)
		}, "[]", "403 Forbidden"},
		{"a wait over pace-max", func(w http.ResponseWriter) {
			w.Header().Set("Retry-After", "600")
			w.WriteHeader(http.StatusTooManyRequests)
		}, "[]", "over pace-max 1m0s"},
	} {
		t.Run(c.name, func(t *testing.T) {
			f, srv := newFake(t)
			f.refusals = append(f.refusals, c.refusal)
			clk := &clock{now: nine}
			var slept []time.Duration
			_, ix := started(t, github.Options{Clock: clk, Token: token(""), Cloud: notCloud,
				Sleep: func(d time.Duration) { slept = append(slept, d); clk.move(d) }},
				wiring(t, fmt.Sprintf(`api = %q`, srv.URL)))
			r, err := ix.Call("read", "issue", "alice", "demo", "7")
			if fmt.Sprint(slept) != c.slept {
				t.Fatalf("the backend slept %v, and wants %s", slept, c.slept)
			}
			if c.err == "" && (err != nil || !strings.HasPrefix(r.Text, "One issue")) {
				t.Fatalf("read answers %q, %v, and wants the issue after the wait", r.Text, err)
			}
			if c.err != "" && (err == nil || !strings.Contains(err.Error(), c.err)) {
				t.Fatalf("read fails with %v, and wants %q", err, c.err)
			}
		})
	}
}

// In a cloud session auto takes the relay, though a token stands in the
// environment: there it is the proxy's stand-in, which code cannot use. A
// backend named token still takes it.
func TestAutoTakesTheRelayInACloudSession(t *testing.T) {
	t.Parallel()
	for backend, want := range map[string]string{"auto": github.Relay, "token": github.Token} {
		_, ix := started(t, github.Options{Token: token(""), Cloud: func() bool { return true }},
			wiring(t, fmt.Sprintf(`backend = %q`, backend)))
		if v, _ := ix.Read("github/backend"); v.V != want {
			t.Fatalf("backend %s in a cloud session reads %v, and wants %s", backend, v.V, want)
		}
	}
}

// A start that asks for the token where none is found is refused.
func TestTheTokenBackendWithoutATokenIsRefused(t *testing.T) {
	t.Parallel()
	_, err := index.Start(wiring(t, `backend = "token"`), github.New(github.Options{Token: noToken}).Component, reader)
	if err == nil || !strings.Contains(err.Error(), "no token is found") {
		t.Fatalf("the start answers %v, and wants it refused: no token is found", err)
	}
}

// toolUse is a PostToolUse payload as a cloud session's hook hands it
// (docs/go-core.md, "What the hooks carry, probed in a cloud session").
func toolUse(tool string, input map[string]any, answer string) []byte {
	text, _ := json.Marshal(answer)
	in, _ := json.Marshal(input)
	return []byte(fmt.Sprintf(`{"session_id": "s1", "hook_event_name": "PostToolUse", "tool_name": %q, "tool_input": %s,
		"tool_use_id": "toolu_1", "duration_ms": 412, "mcp_server": {"name": "github", "source": "dynamic"},
		"tool_response": [{"type": "text", "text": %s}]}`, tool, in, text))
}

var issueRead = map[string]any{"method": "get", "owner": "alice", "repo": "demo", "issue_number": 7}

// Without a token the relay answers: a read goes out as an outbox step with
// the tool call that answers it, and fails naming it; the session's tool
// answer, handed over by PostToolUse, lands in the snapshot, drops the step,
// and the next read answers from the snapshot without asking.
func TestTheRelayAsksOnTheOutboxAndPostToolUseAnswers(t *testing.T) {
	t.Parallel()
	clk := &clock{now: nine}
	_, ix := started(t, github.Options{Clock: clk, Token: noToken}, wiring(t, ``))
	if v, _ := ix.Read("github/backend"); v.V != github.Relay {
		t.Fatalf("github/backend reads %v, and wants relay where no token is found", v.V)
	}
	_, err := ix.Call("read", "issue", "alice", "demo", "7")
	if err == nil || !strings.Contains(err.Error(), `run the GitHub tool issue_read with {"issue_number":7,"method":"get","owner":"alice","repo":"demo"}; outbox step issue.alice.demo.7`) {
		t.Fatalf("the read answers %v, and wants it to name the tool call it waits for", err)
	}
	v, _ := ix.Read("github/outbox/issue.alice.demo.7")
	if step := v.V.(github.Step); step.Tool != "issue_read" || step.Asked != nine {
		t.Fatalf("the outbox holds %+v", v.V)
	}
	clk.move(5 * time.Second)
	if err := ix.Hear(github.PostToolUse, toolUse("mcp__github__issue_read", issueRead, `{"number": 7, "title": "One issue"}`)); err != nil {
		t.Fatal(err)
	}
	s := snapshot(t, ix, "github/issues/alice.demo.7")
	if s.Read != nine.Add(5*time.Second) || s.Via != github.Relay {
		t.Fatalf("the snapshot is %+v, and wants it read at 09:00:05 via relay", s)
	}
	if v, _ := ix.Read("github/outbox/issue.alice.demo.7"); v.Provided {
		t.Fatalf("the answered step stands in the outbox: %+v", v.V)
	}
	clk.move(time.Minute)
	r, err := ix.Call("read", "issue", "alice", "demo", "7")
	if err != nil || r.Text != "One issue, read 2026-11-13T09:00:05Z via relay" {
		t.Fatalf("the next read answers %q, %v, and wants the snapshot", r.Text, err)
	}
	clk.move(2 * time.Minute)
	if _, err := ix.Call("read", "issue", "alice", "demo", "7"); err == nil {
		t.Fatal("a read after the snapshot aged past refresh answers, and wants it asked again")
	}
}

// With relay-wait, a read waits for the session's answer and is answered by it.
func TestARelayReadWaitsForTheToolAnswer(t *testing.T) {
	t.Parallel()
	_, ix := started(t, github.Options{Clock: &clock{now: nine}, Token: noToken}, wiring(t, `relay-wait = "10s"`))
	go func() {
		for {
			if v, _ := ix.Read("github/outbox/check-runs.alice.demo.8"); v.Provided {
				_ = ix.Hear(github.PostToolUse, toolUse("mcp__0f1e2d3c__pull_request_read",
					map[string]any{"method": "get_check_runs", "owner": "alice", "repo": "demo", "pullNumber": 8},
					`{"total_count": 2, "check_runs": []}`))
				return
			}
			time.Sleep(time.Millisecond)
		}
	}()
	r, err := ix.Call("read", "checks", "alice", "demo", "8")
	if err != nil || r.Text != "2 check runs, read 2026-11-13T09:00:00Z via relay" {
		t.Fatalf("the waiting read answers %q, %v", r.Text, err)
	}
}

// relay-wait measures against the component's clock: a read that no answer
// reaches fails once the clock moves past its wait, naming the step, and not
// before; the case moves the clock and never waits the 10s out.
func TestARelayReadFailsWhenItsClockPassesItsWait(t *testing.T) {
	t.Parallel()
	clk := &clock{now: nine}
	_, ix := started(t, github.Options{Clock: clk, Token: noToken}, wiring(t, `relay-wait = "10s"`))
	failed := make(chan error, 1)
	go func() {
		_, err := ix.Call("read", "issue", "alice", "demo", "7")
		failed <- err
	}()
	for clk.pending() == 0 {
		time.Sleep(time.Millisecond) // the read asks its step and waits at once
	}
	if v, _ := ix.Read("github/outbox/issue.alice.demo.7"); !v.Provided {
		t.Fatal("the waiting read stands in no outbox step")
	}
	clk.move(9 * time.Second)
	select {
	case err := <-failed:
		t.Fatalf("the read failed at 9s of a 10s wait: %v", err)
	default:
	}
	clk.move(time.Second)
	select {
	case err := <-failed:
		if err == nil || !strings.Contains(err.Error(), "outbox step issue.alice.demo.7") {
			t.Fatalf("the read past its wait answers %v, and wants it failed naming its outbox step", err)
		}
	case <-time.After(10 * time.Second): // bounds a failure that comes at once
		t.Fatal("the read did not fail once its clock passed its wait")
	}
}

// Every GitHub tool answer lands, asked or not; other tools, other methods,
// other events and error answers write nothing.
func TestEveryGitHubToolAnswerLandsAndNothingElse(t *testing.T) {
	t.Parallel()
	_, ix := started(t, github.Options{Clock: &clock{now: nine}, Token: noToken}, wiring(t, ``))
	before := ix.Snapshot().Revision()
	for _, p := range [][]byte{
		toolUse("Bash", map[string]any{"command": "ls"}, "{}"),
		toolUse("mcp__github__issue_read", map[string]any{"method": "get_labels", "owner": "alice", "repo": "demo", "issue_number": 7}, "[]"),
		[]byte(`not json`),
	} {
		if err := ix.Hear(github.PostToolUse, p); err != nil {
			t.Fatal(err)
		}
	}
	if err := ix.Hear("PreToolUse", toolUse("mcp__github__issue_read", issueRead, `{"number": 7}`)); err != nil {
		t.Fatal(err)
	}
	if err := ix.Hear(github.PostToolUse, []byte(`{"tool_name": "mcp__github__issue_read", "tool_input": {"method": "get", "owner": "alice", "repo": "demo", "issue_number": 7},
		"tool_response": {"content": [{"type": "text", "text": "Not Found"}], "isError": true}}`)); err == nil {
		t.Fatal("an error answer is heard without a word, and wants it named")
	}
	if rev := ix.Snapshot().Revision(); rev != before {
		t.Fatalf("the store moved from revision %d to %d", before, rev)
	}
	if err := ix.Hear(github.PostToolUse, toolUse("mcp__github__pull_request_read", map[string]any{"method": "get", "owner": "Alice", "repo": "Demo", "pullNumber": "8"}, `{"number": 8}`)); err != nil {
		t.Fatal(err)
	}
	snapshot(t, ix, "github/pulls/alice.demo.8")
}

// Switched off, the component reads no token, hears nothing, and its names
// read their built-in values.
func TestTheComponentSwitchesOff(t *testing.T) {
	t.Parallel()
	asked := false
	w, err := index.ParseWiring([]byte(`
[instances.github]
component = "github"
enabled = false

[wires]
"github.second" = "built-in"
`))
	if err != nil {
		t.Fatal(err)
	}
	_, ix := started(t, github.Options{Token: func() (string, error) { asked = true; return "", nil }}, w)
	if asked {
		t.Fatal("a switched-off component looked for a token")
	}
	if err := ix.Hear(github.PostToolUse, toolUse("mcp__github__issue_read", issueRead, `{"number": 7}`)); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"github/backend", "github/issues/alice.demo.7"} {
		if v, _ := ix.Read(name); v.Provided {
			t.Fatalf("%s reads %v, and wants its built-in value", name, v.V)
		}
	}
}

// Through the real core: the hook's post of a PostToolUse lands the issue in
// its snapshot name, which a client reads with its read time.
func TestAHookPostLandsASnapshotInTheCore(t *testing.T) {
	t.Parallel()
	g := github.New(github.Options{Clock: &clock{now: nine}, Token: noToken})
	w, err := index.ParseWiring([]byte(`
[instances.github]
component = "github"

[wires]
"github.second" = "built-in"
"github.issues" = "github/issues/<key>"
`))
	if err != nil {
		t.Fatal(err)
	}
	c, err := host.Serve(host.Options{Root: t.TempDir(), Wiring: w, Components: []*index.Component{g.Component}})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { c.Stop("the case ends"); <-c.Ended() })
	s, _ := c.Standing()
	if _, err := host.Post(host.Wall{}, s.Root, github.PostToolUse, toolUse("mcp__github__issue_read", issueRead, `{"number": 7, "title": "One issue"}`), 10*time.Second); err != nil {
		t.Fatal(err)
	}
	nc, err := bus.Dial(s.Bus, bus.Client, s.Users[bus.Client])
	if err != nil {
		t.Fatal(err)
	}
	defer nc.Close()
	m, err := nc.Request(bus.Get+"github", []byte(`{"name": "github/issues/alice.demo.7"}`), 10*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	var got struct {
		Value    github.Snapshot `json:"value"`
		Provided bool            `json:"provided"`
	}
	if err := json.Unmarshal(m.Data, &got); err != nil || !got.Provided || got.Value.Read != nine || got.Value.Via != github.Relay {
		t.Fatalf("get answers %s, and wants the snapshot read at 09:00 via relay", m.Data)
	}
}
