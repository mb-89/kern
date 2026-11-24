// Package github is kern's GitHub component: the one I/O component that
// reaches GitHub, so every other component reads GitHub through its verbs.
// It names no application. Its three backends are chosen at start:
//
//   - token, where a token is found (GH_TOKEN, GITHUB_TOKEN, else `gh auth
//     token`): GitHub's REST API, in process, with ETags, a refresh of every
//     snapshot it holds, and pacing on 403 and 429;
//   - proxy, where no token is usable, or in a cloud session, and GET
//     /rate_limit through the environment's HTTPS proxy answers 200 without
//     one: REST in process as on token, the proxy adding the credentials;
//   - relay, where neither is: a read goes out as an entry of the family outbox,
//     the session's own GitHub tool runs it, and the PostToolUse hook hands
//     the tool's answer back, which the component matches to the entry.
//
// Every answer either backend reads, and every GitHub tool answer a session
// receives, lands in a snapshot name with the time it was read. SE's
// docs/go-core.md ("GitHub in the cloud") is its design; README.md beside this
// file is its contract.
package github

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"strings"
	"sync"
	"time"

	"kern/components/clock"
	"kern/host"
	"kern/index"
)

// Name is the component's name, which the wiring's instances name.
const Name = "github"

// The backends, as the config key backend and the output backend name them.
const (
	Auto  = "auto"
	Token = "token"
	Proxy = "proxy"
	Relay = "relay"
)

// Ref is what a read asks: one issue or pull request of one repository.
type Ref struct {
	Owner  string `json:"owner"`
	Repo   string `json:"repo"`
	Number int    `json:"number"`
}

// Snapshot is one read as it stands in a snapshot name: what was read, when,
// and through which backend. Data is the answer as that backend read it:
// GitHub's REST JSON through the token, the tool's JSON through the relay.
type Snapshot struct {
	Repo   string    `json:"repo"`
	Number int       `json:"number"`
	Read   time.Time `json:"read"`
	Via    string    `json:"via"`
	Data   any       `json:"data"`
}

// ListRef is what a list read asks: a repository's issues or pull requests
// in one state (open, the default, closed or all), and for issues, only
// those carrying every label it names.
type ListRef struct {
	Owner  string   `json:"owner"`
	Repo   string   `json:"repo"`
	State  string   `json:"state,omitempty"`
	Labels []string `json:"labels,omitempty"`
}

// List is one list read as it stands in a list name: the repository, the
// state and labels it was asked with, when and through which backend it was
// read, and every item of every page, in GitHub's order. Data holds each item
// as that backend read it: GitHub's REST JSON through the token, the tool's
// JSON through the relay.
type List struct {
	Repo   string    `json:"repo"`
	State  string    `json:"state"`
	Labels []string  `json:"labels,omitempty"`
	Read   time.Time `json:"read"`
	Via    string    `json:"via"`
	Data   []any     `json:"data"`
}

// Step is one entry of the outbox: the GitHub tool call the session runs,
// with the tool's bare name and its input, and when the read was asked.
type Step struct {
	Tool  string         `json:"tool"`
	Input map[string]any `json:"input"`
	Asked time.Time      `json:"asked"`
	// Note is what the asker says the agent watches for, for a step of the verb step.
	Note string `json:"note,omitempty"`
}

// kind is one family of snapshots of one issue or pull request: its output,
// its verb, the tool call that reads it in a session and the REST path that
// reads it with a token. A paged kind is read page by page, and its
// snapshot's data is every page's items.
type kind struct {
	port, verb, help string
	tool, method     string
	numberArg        string
	paged            bool
}

var (
	issues   = kind{port: "issues", verb: "issue", help: "an issue", tool: "issue_read", method: "get", numberArg: "issue_number"}
	pulls    = kind{port: "pulls", verb: "pull", help: "a pull request", tool: "pull_request_read", method: "get", numberArg: "pullNumber"}
	checks   = kind{port: "checks", verb: "check-runs", help: "the check runs of a pull request's head", tool: "pull_request_read", method: "get_check_runs", numberArg: "pullNumber"}
	comments = kind{port: "comments", verb: "comment-list", help: "the comments of an issue or a pull request, every page", tool: "issue_read", method: "get_comments", numberArg: "issue_number", paged: true}
	kinds    = []kind{issues, pulls, checks, comments}
	// heard are the tool calls whose answers land: each kind's own, and a
	// pull request's comments read through pull_request_read.
	heard = append(append([]kind{}, kinds...), kind{port: comments.port, verb: comments.verb, tool: "pull_request_read", method: "get_comments", numberArg: "pullNumber", paged: true})
)

// Key is the key a read stands at in its family: <owner>.<repo>.<number>, in
// lower case, as GitHub reads owners and repositories regardless of case.
func Key(r Ref) string {
	return strings.ToLower(r.Owner) + "." + strings.ToLower(r.Repo) + "." + fmt.Sprint(r.Number)
}

// outboxKey is the key a read's step stands at in the outbox: <verb>.<key>.
func outboxKey(k kind, r Ref) string { return k.verb + "." + Key(r) }

// Options are what the component reaches the outside with. Their zero value
// reaches nothing: a clock no one moves, no token, no proxy and no cloud
// session, so a component built from it chooses the relay and does no I/O,
// and a test that leaves an option unset never reaches the environment.
// Environment names the environment the binary's component reaches.
type Options struct {
	// Clock is the time reads are stamped with and every wait is measured
	// against: the relay's waits, a step's wait and a tell's, and the pacing
	// of a rate limit. host.Still where none is given; the binary's is the
	// wall clock, and a test hands in a clock it moves itself.
	Clock host.Clock
	// Token answers the token, or "" where none is found: none where nil.
	Token func() (string, error)
	// Client is the REST client of the token and proxy backends: one that
	// sends through Proxy where none is given.
	Client *http.Client
	// Proxy is the HTTPS proxy for a request, nil where it names none: none
	// where nil. The proxy backend is probed only where it names one for the
	// probe.
	Proxy func(*http.Request) (*url.URL, error)
	// Sleep waits out a rate limit: a wait on Clock where none is given.
	Sleep func(time.Duration)
	// Go runs the refresh the clock's second makes due, so a slow GitHub
	// never holds the clock: a goroutine of its own where none is given.
	Go func(fn func())
	// Cloud says whether the core runs in a cloud session, where no token
	// reaches code and auto takes the proxy, else the relay: never where nil.
	Cloud func() bool
}

// Environment is the options of the binary's component, the environment
// named: the wall clock, the token FindToken finds, the environment's HTTPS
// proxy (http.ProxyFromEnvironment) and a cloud session where
// CLAUDE_CODE_REMOTE=true.
func Environment() Options {
	return Options{Clock: host.Wall{}, Token: FindToken, Proxy: http.ProxyFromEnvironment, Cloud: InCloud}
}

// InCloud says whether the core runs in a cloud session: CLAUDE_CODE_REMOTE=true.
func InCloud() bool { return os.Getenv("CLAUDE_CODE_REMOTE") == "true" }

// GitHub is the component and its state: one instance per core.
type GitHub struct {
	// Component is what the index, the wiring and the catalog see.
	Component *index.Component
	o         Options

	mu         sync.Mutex
	instance   string
	commit     index.Commit
	backend    string
	rest       *rest // the token's or the proxy's REST client; nil on the relay
	fresh      time.Duration
	wait       time.Duration
	held       map[string]func() // the reads the REST backends refresh, by outbox key
	waiting    map[string][]chan any
	outbox     map[string]Step   // the relay's open steps, by outbox key
	pages      map[string]*pages // the relay's paged reads under way, by outbox key
	last       map[string]any    // the newest Snapshot or List of each read, by outbox key
	stepWait   time.Duration
	tellWait   time.Duration
	answered   map[string]uint64       // the PostToolUse payloads that answered a step, with the asks counted then, until their tell
	steps      map[string]*waitingStep // the steps of the verb step waiting for a tool answer, by outbox key
	done       []answeredStep          // the numbered steps answered last, oldest first, up to keepAnswered
	stale      map[string]string       // the PostToolUse payloads that answered an answered step, with their refusal, until their tell
	asks       uint64                  // the steps asked so far, counted as each enters the steps
	nextAsk    chan struct{}           // closed when the next step is asked, so a tell waits for the count to pass the one it saw
	relay      clock.Relay             // the refresh's: due once refresh has passed on the clock's second
	refreshing bool                    // a refresh runs
}

// The component's outputs.
var (
	issuesOut     = index.Family("issues", Snapshot{}, "Each issue read, at <owner>.<repo>.<number>, with the time it was read.")
	pullsOut      = index.Family("pulls", Snapshot{}, "Each pull request read, at <owner>.<repo>.<number>, with the time it was read.")
	checksOut     = index.Family("checks", Snapshot{}, "The check runs of each pull request's head read, at <owner>.<repo>.<number>, with the time they were read.")
	commentsOut   = index.Family("comments", Snapshot{}, "The comments of each issue or pull request read, every page, at <owner>.<repo>.<number>, with the time they were read.")
	issueListsOut = index.Family("issue-lists", List{}, "Each list of a repository's issues read, every page, at <owner>.<repo>.<state>[.<label>...], with the time it was read.")
	pullListsOut  = index.Family("pull-lists", List{}, "Each list of a repository's pull requests read, every page, at <owner>.<repo>.<state>, with the time it was read.")
	outboxOut     = index.Family("outbox", Step{}, "The relay's reads waiting for the session's GitHub tool, at <verb>.<key>: the next page's call where a read is paged.")
	backendOut    = index.Output("backend", "", "The backend chosen at start: token, proxy or relay.")
)

func (k kind) out() *index.FamilyPort[Snapshot] {
	switch k.port {
	case issues.port:
		return issuesOut
	case pulls.port:
		return pullsOut
	case comments.port:
		return commentsOut
	}
	return checksOut
}

// New makes the component with its outside reached through o.
func New(o Options) *GitHub {
	if o.Clock == nil {
		o.Clock = host.Still{}
	}
	if o.Token == nil {
		o.Token = func() (string, error) { return "", nil }
	}
	if o.Proxy == nil {
		o.Proxy = func(*http.Request) (*url.URL, error) { return nil, nil }
	}
	if o.Client == nil {
		tr := http.DefaultTransport.(*http.Transport).Clone()
		tr.Proxy = o.Proxy
		o.Client = &http.Client{Transport: tr} // each request's limit is RequestWait, on Clock
	}
	if o.Sleep == nil {
		clk := o.Clock
		o.Sleep = func(d time.Duration) { <-clk.After(d) }
	}
	if o.Cloud == nil {
		o.Cloud = func() bool { return false }
	}
	if o.Go == nil {
		o.Go = func(fn func()) { go fn() }
	}
	g := &GitHub{o: o, held: map[string]func(){}, waiting: map[string][]chan any{}, outbox: map[string]Step{}, pages: map[string]*pages{}, last: map[string]any{},
		answered: map[string]uint64{}, stale: map[string]string{}, steps: map[string]*waitingStep{}, nextAsk: make(chan struct{})}
	parts := []index.Part{
		index.IO(), index.Forbids(Forbidden),
		index.Config("backend", Auto, "auto, token, proxy or relay: auto takes the token where one is found outside a cloud session, else the proxy where GET /rate_limit through the environment's HTTPS proxy answers 200 without a token, else the relay."),
		index.Config("api", "https://api.github.com", "The REST API the token and proxy backends read."),
		index.Config("refresh", "2m", "How often the token and proxy backends read each snapshot it holds again, with its ETag, counted on the clock's second it reads; 0s reads none again. The relay answers a snapshot younger than it without asking."),
		index.Config("relay-wait", "0s", "How long a relay read waits for the session's tool answer before it fails, naming the outbox step."),
		index.Config("pace-max", "60s", "The longest rate-limit wait the token and proxy backends sit out; a longer one fails the read."),
		index.Config("step-wait", "60m", "The longest a step of the verb step waits for the session's tool answer; a step asks its own wait up to it."),
		index.Config("tell-wait", "300ms", "How long the hook of a tool answer that answered a step waits for the next step to be asked, so it tells that one."),
		issuesOut, pullsOut, checksOut, commentsOut, issueListsOut, pullListsOut, outboxOut, backendOut,
		index.Inlet(g.start),
		index.Reacts(g.second),
		index.Hears(g.hear),
		index.Tells(g.tell),
		index.JSONVerb("step", "Runs one GitHub tool call through the relay: the step stands in the outbox until a PostToolUse of that tool with those arguments answers it, and the verb answers the tool's answer.", StepShape, StepAnswerShape, func(ctx context.Context, a StepAsk) (StepAnswer, error) {
			return g.step(ctx, a)
		}),
	}
	parts = append(parts, g.writeParts()...)
	for _, k := range kinds {
		k := k
		parts = append(parts, index.Verb(k.verb, "Reads "+k.help+": answers its snapshot and writes it to "+k.port+".", func(_ context.Context, r Ref) (Snapshot, error) {
			return g.read(k, r)
		}))
	}
	for _, l := range lists {
		l := l
		parts = append(parts, index.Verb(l.verb, "Lists "+l.help+", every page: answers the list and writes it to "+l.port+".", func(_ context.Context, r ListRef) (List, error) {
			return g.list(l, r)
		}))
	}
	g.Component = index.Declare(Name, "Reaches GitHub, with a token, through the environment's proxy or through the session's GitHub tools, and keeps what it reads as snapshots.", parts...)
	return g
}

// Forbidden is the GitHub component's forbid list (index.Forbids): the gh
// command, the REST API's host and the Go clients of GitHub, which only its
// own code may use. Any other component reads GitHub through its names and
// verbs (README.md "Forbid lists").
var Forbidden = index.Forbidden{
	Commands: []string{"gh"},
	Hosts:    []string{"api.github.com"},
	Imports:  []string{"github.com/google/go-github", "github.com/cli/go-gh"},
}

// Component is the component a binary carries, reaching the environment;
// importing this package registers it.
var Component = New(Environment())

func init() { index.Register(Component.Component) }

// FindToken answers GH_TOKEN, else GITHUB_TOKEN, else what `gh auth token`
// prints, else "".
func FindToken() (string, error) {
	for _, v := range []string{"GH_TOKEN", "GITHUB_TOKEN"} {
		if t := strings.TrimSpace(os.Getenv(v)); t != "" {
			return t, nil
		}
	}
	if _, err := exec.LookPath("gh"); err != nil {
		return "", nil
	}
	out, err := exec.Command("gh", "auth", "token").Output()
	if err != nil {
		return "", nil // gh without a login: no token
	}
	return strings.TrimSpace(string(out)), nil
}

// start chooses the backend; the REST backends' refresh runs on the clock's second.
func (g *GitHub) start(instance string, read index.Reader, commit index.Commit) error {
	str := func(key string) string { v, _ := read(index.ConfigPrefix + key).(string); return v }
	dur := func(key string) (time.Duration, error) {
		d, err := time.ParseDuration(str(key))
		if err != nil || d < 0 {
			return 0, fmt.Errorf("config %s: %q is no duration, such as 2m or 0s", key, str(key))
		}
		return d, nil
	}
	refresh, err := dur("refresh")
	if err != nil {
		return err
	}
	wait, err := dur("relay-wait")
	if err != nil {
		return err
	}
	paceMax, err := dur("pace-max")
	if err != nil {
		return err
	}
	stepWait, err := dur("step-wait")
	if err != nil {
		return err
	}
	tellWait, err := dur("tell-wait")
	if err != nil {
		return err
	}
	backend, api := str("backend"), strings.TrimSuffix(str("api"), "/")
	var token string
	switch backend {
	case Auto, Token:
		if backend == Auto && g.o.Cloud() {
			break // a cloud session's GH_TOKEN is the proxy's stand-in, which code cannot use
		}
		if token, err = g.o.Token(); err != nil {
			return fmt.Errorf("the token: %w", err)
		}
		if token == "" && backend == Token {
			return errors.New("config backend is token, and no token is found: set GH_TOKEN, or log gh in")
		}
	case Proxy, Relay:
	default:
		return fmt.Errorf("config backend: %q is none of auto, token, proxy and relay", backend)
	}
	switch {
	case token != "":
		backend = Token
	case backend == Auto || backend == Proxy:
		why := g.probe(api)
		switch {
		case why == "":
			backend = Proxy
		case backend == Proxy:
			return fmt.Errorf("config backend is proxy, and %s", why)
		default:
			backend = Relay
		}
	}
	g.mu.Lock()
	if g.instance != "" {
		g.mu.Unlock()
		return fmt.Errorf("component %s runs one instance per core, and %s is one already", Name, g.instance)
	}
	g.instance, g.commit, g.backend, g.fresh, g.wait = instance, commit, backend, refresh, wait
	g.stepWait, g.tellWait = stepWait, tellWait
	if backend == Token || backend == Proxy {
		g.rest = &rest{api: api, via: backend, token: token, client: g.o.Client, sleep: g.o.Sleep, clock: g.o.Clock, paceMax: paceMax, cache: map[string]cached{}}
	}
	g.mu.Unlock()
	if err := commit(backendOut.Set(backend)); err != nil {
		return err
	}
	return nil
}

// second is the component's reaction to the clock's second, a count that
// counts up (components/clock): its relay returns at once until refresh, in
// whole seconds and at least one, has passed in counts since the last
// refresh, and then the REST backend's refresh runs, on its own, unless the
// last one still runs. A wiring that leaves second built-in reads 0 for good,
// and refreshes nothing.
func (g *GitHub) second(_ string, in struct {
	Second int `index:"second"`
}) {
	g.mu.Lock()
	rest, refresh := g.rest, g.fresh
	g.mu.Unlock()
	every := 0
	if refresh > 0 {
		every = max(1, int(refresh/time.Second))
	}
	if rest == nil || !g.relay.Due(in.Second, every) {
		return
	}
	g.mu.Lock()
	if g.refreshing {
		g.mu.Unlock()
		return
	}
	g.refreshing = true
	g.mu.Unlock()
	g.o.Go(func() {
		g.Refresh()
		g.mu.Lock()
		g.refreshing = false
		g.mu.Unlock()
	})
}

// read answers one read through the backend chosen at start.
func (g *GitHub) read(k kind, r Ref) (Snapshot, error) {
	if r.Owner == "" || r.Repo == "" || r.Number <= 0 {
		return Snapshot{}, fmt.Errorf("github %s: a read names the owner, the repository and a number above 0, not %+v", k.verb, r)
	}
	g.mu.Lock()
	backend, rest := g.backend, g.rest
	g.mu.Unlock()
	switch backend {
	case Token, Proxy:
		return g.readToken(rest, k, r)
	case Relay:
		v, err := g.ask(k.verb, Key(r), outboxKey(k, r), stepOf(k, r, g.o.Clock.Now()))
		s, _ := v.(Snapshot)
		return s, err
	}
	return Snapshot{}, fmt.Errorf("github %s: the component has not started", k.verb)
}

// readToken reads through REST, with the token or through the proxy, writes
// the snapshot and holds it for the refresh.
func (g *GitHub) readToken(rest *rest, k kind, r Ref) (Snapshot, error) {
	data, err := rest.read(k, r)
	if err != nil {
		return Snapshot{}, fmt.Errorf("github %s %s: %w", k.verb, Key(r), err)
	}
	s := Snapshot{Repo: repoOf(r.Owner, r.Repo), Number: r.Number, Read: g.o.Clock.Now().UTC(), Via: rest.via, Data: data}
	ob := outboxKey(k, r)
	g.mu.Lock()
	g.held[ob] = func() { _, _ = g.readToken(rest, k, r) }
	g.mu.Unlock()
	return s, g.land(ob, k.out().At(Key(r), s), s)
}

// Refresh reads every snapshot and list the REST backend holds again, each
// page with its ETag, so an unchanged one costs no quota; it is what the
// refresh runs.
func (g *GitHub) Refresh() {
	g.mu.Lock()
	held := make([]func(), 0, len(g.held))
	for _, h := range g.held {
		held = append(held, h)
	}
	g.mu.Unlock()
	for _, h := range held {
		h()
	}
}

// land writes a read's snapshot or list and drops its outbox step, then
// hands it to every relay read waiting for it.
func (g *GitHub) land(ob string, w index.Write, v any) error {
	g.mu.Lock()
	commit := g.commit
	writes := []index.Write{w}
	if _, ok := g.outbox[ob]; ok {
		delete(g.outbox, ob)
		writes = append(writes, outboxOut.Drop(ob))
	}
	delete(g.pages, ob)
	g.last[ob] = v
	waiters := g.waiting[ob]
	delete(g.waiting, ob)
	g.mu.Unlock()
	if commit == nil {
		return fmt.Errorf("github: the component has not started")
	}
	err := commit(writes...)
	for _, w := range waiters {
		w <- v
	}
	return err
}

func repoOf(owner, repo string) string { return strings.ToLower(owner) + "/" + strings.ToLower(repo) }
