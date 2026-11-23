package github

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"time"

	"kern/filled"
	"kern/host"
	"kern/index"
)

// The named JSON shapes of the write verbs' requests and their answer, so a
// component in another language asks them as a Go one does.
const (
	IssueCreateShape  = "github.issue-create"
	IssueCommentShape = "github.issue-comment"
	WrittenShape      = "github.written"
)

// NewIssue is what the verb issue-create asks: the repository, the labels and
// a filled template, as the application's template component made it; never
// a title or a body, which the template component answers for it.
type NewIssue struct {
	Owner  string   `json:"owner"`
	Repo   string   `json:"repo"`
	Labels []string `json:"labels,omitempty"`
	Filled any      `json:"filled"`
}

// NewComment is what the verb issue-comment asks: the issue or pull request
// and a filled template; never a body.
type NewComment struct {
	Owner  string `json:"owner"`
	Repo   string `json:"repo"`
	Number int    `json:"number"`
	Filled any    `json:"filled"`
}

// Written is what a write verb answers: the issue it filed or commented on,
// the link to what it wrote, when and through which backend, and GitHub's
// answer as that backend read it.
type Written struct {
	Repo   string    `json:"repo"`
	Number int       `json:"number"`
	URL    string    `json:"url"`
	Read   time.Time `json:"read"`
	Via    string    `json:"via"`
	Data   any       `json:"data"`
}

// The commands' request ports, bound to the component's own write verbs
// where the wiring binds them to nothing (index.RequestPort.Self), and the
// port the write verbs ask whether the application's template component
// accepts the filled template (package filled), which the application's
// wiring binds to that component's verb accept.
var (
	createPort  = index.JSONRequest("file-issue", "Files an issue through the verb issue-create.", IssueCreateShape, WrittenShape).Self("issue-create")
	commentPort = index.JSONRequest("file-comment", "Comments on an issue through the verb issue-comment.", IssueCommentShape, WrittenShape).Self("issue-comment")
	acceptPort  = filled.NewPort("Asks the application's template component whether it accepts the filled template a write was handed, and answers its title and document; the wiring binds it to that component's verb accept.").Optional()
)

// writeParts are the write verbs and the writing commands that ask them.
func (g *GitHub) writeParts() []index.Part {
	return []index.Part{
		index.JSONVerb("issue-create", "Files an issue from a filled template: asks the application's template component whether it accepts it, and files the title and the document it answers as they stand; a value it refuses reaches GitHub not at all.", IssueCreateShape, WrittenShape,
			func(ctx context.Context, req map[string]any) (Written, error) {
				var a NewIssue
				if err := strict(req, &a); err != nil {
					return Written{}, fmt.Errorf("github issue-create: %w", err)
				}
				return g.createIssue(ctx, a)
			}),
		index.JSONVerb("issue-comment", "Comments on an issue or a pull request from a filled template: asks the application's template component whether it accepts it, and posts the document it answers as it stands; a value it refuses reaches GitHub not at all.", IssueCommentShape, WrittenShape,
			func(ctx context.Context, req map[string]any) (Written, error) {
				var a NewComment
				if err := strict(req, &a); err != nil {
					return Written{}, fmt.Errorf("github issue-comment: %w", err)
				}
				return g.comment(ctx, a)
			}),
		createPort, commentPort, acceptPort,
		index.Command(index.CommandDecl{
			Name: "github issue-create", Usage: "github issue-create --repo OWNER/REPO --filled JSON [--label LABEL]...",
			Summary: "File an issue from a filled template.", Group: "GitHub", Class: index.Batch, Writes: true,
			Help: "Files an issue in OWNER/REPO: --filled is the filled template as the application's template component made it, one JSON value; that component accepts it before anything reaches GitHub, and its title and document are filed as they stand. A value it refuses answers its refusal.",
			MCP:  "Files a GitHub issue from a filled template, never free text: fill the template with the application's template component and pass its answer, as it stands, as --filled; a value it refuses answers its refusal, and nothing is filed.",
		}, func(_ struct{}, args []string) (index.Result, error) {
			fs, repo, value := writeFlags("github issue-create")
			var labels many
			fs.Var(&labels, "label", "a label the issue carries; repeatable")
			a, err := parseWrite(fs, args, repo, value)
			if err != nil {
				return index.Result{}, err
			}
			ask := NewIssue{Owner: a.owner, Repo: a.repo, Labels: labels, Filled: a.filled}
			return index.Result{Requests: []index.Ask{createPort.Ask(ask)}, Then: func(ans []any) (index.Result, error) {
				var w Written
				if err := index.Recode(ans[0], &w); err != nil {
					return index.Result{}, err
				}
				return index.Result{Text: fmt.Sprintf("filed %s#%d: %s", w.Repo, w.Number, w.URL)}, nil
			}}, nil
		}),
		index.Command(index.CommandDecl{
			Name: "github issue-comment", Usage: "github issue-comment --repo OWNER/REPO --number N --filled JSON",
			Summary: "Comment on an issue from a filled template.", Group: "GitHub", Class: index.Batch, Writes: true,
			Help: "Comments on issue or pull request N of OWNER/REPO: --filled is the filled template as the application's template component made it, one JSON value; that component accepts it before anything reaches GitHub, and its document is posted as it stands. A value it refuses answers its refusal.",
			MCP:  "Comments on a GitHub issue or pull request from a filled template, never free text: fill the template with the application's template component and pass its answer, as it stands, as --filled; a value it refuses answers its refusal, and nothing is posted.",
		}, func(_ struct{}, args []string) (index.Result, error) {
			fs, repo, value := writeFlags("github issue-comment")
			number := fs.Int("number", 0, "the issue or pull request")
			a, err := parseWrite(fs, args, repo, value)
			if err != nil {
				return index.Result{}, err
			}
			ask := NewComment{Owner: a.owner, Repo: a.repo, Number: *number, Filled: a.filled}
			return index.Result{Requests: []index.Ask{commentPort.Ask(ask)}, Then: func(ans []any) (index.Result, error) {
				var w Written
				if err := index.Recode(ans[0], &w); err != nil {
					return index.Result{}, err
				}
				return index.Result{Text: fmt.Sprintf("commented on %s#%d: %s", w.Repo, w.Number, w.URL)}, nil
			}}, nil
		}),
	}
}

// strict decodes a write's request into a, refusing a key it does not take:
// a body or a title is free text, which a write never carries.
func strict(req map[string]any, a any) error {
	data, err := json.Marshal(req)
	if err != nil {
		return err
	}
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.DisallowUnknownFields()
	if err := dec.Decode(a); err != nil {
		return fmt.Errorf("%v: a write takes a filled template, never free text", err)
	}
	return nil
}

type many []string

func (m *many) String() string     { return strings.Join(*m, ",") }
func (m *many) Set(v string) error { *m = append(*m, v); return nil }

func writeFlags(name string) (fs *flag.FlagSet, repo, value *string) {
	fs = flag.NewFlagSet(name, flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	return fs, fs.String("repo", "", "OWNER/REPO"), fs.String("filled", "", "the filled template, one JSON value, as the template component made it")
}

type writeArgs struct {
	owner, repo string
	filled      any
}

func parseWrite(fs *flag.FlagSet, args []string, repo, value *string) (writeArgs, error) {
	if err := fs.Parse(args); err != nil {
		return writeArgs{}, fmt.Errorf("%s: %v", fs.Name(), err)
	}
	if fs.NArg() > 0 {
		return writeArgs{}, fmt.Errorf("%s takes no argument %q: a write takes a filled template, never free text", fs.Name(), fs.Arg(0))
	}
	owner, name, ok := strings.Cut(*repo, "/")
	if !ok || owner == "" || name == "" || *value == "" {
		return writeArgs{}, fmt.Errorf("%s names --repo OWNER/REPO and --filled JSON", fs.Name())
	}
	var f any
	if err := json.Unmarshal([]byte(*value), &f); err != nil {
		return writeArgs{}, fmt.Errorf("%s: --filled is no JSON value, as the template component's fill answers one: %q", fs.Name(), *value)
	}
	return writeArgs{owner: owner, repo: name, filled: f}, nil
}

// accept asks the application's template component, on the port
// template-accept, whether it accepts the filled template the write verb was
// handed, and answers what it accepted: its refusal, or the port left
// unbound, answers the verb's error, and nothing reaches GitHub. A write
// takes no free text, so an answer that the value is free is refused too; an
// issue needs the title the template answers.
func (g *GitHub) accept(ctx context.Context, verb string, value any, titled bool) (filled.Accepted, error) {
	ans, err := acceptPort.Within(ctx, filled.Request{Target: "github " + verb, Filled: value})
	if err != nil {
		return filled.Accepted{}, fmt.Errorf("github %s: %w; nothing reached GitHub", verb, err)
	}
	a, err := filled.Answer(ans)
	if err != nil {
		return filled.Accepted{}, fmt.Errorf("github %s: %w; nothing reached GitHub", verb, err)
	}
	switch {
	case a.Free || a.Document == "":
		return filled.Accepted{}, fmt.Errorf("github %s: the template component accepts no filled template, and a write takes no free text: fill a template with it; nothing reached GitHub", verb)
	case titled && (a.Title == nil || strings.TrimSpace(*a.Title) == ""):
		return filled.Accepted{}, fmt.Errorf("github %s: the filled template of %s has no title, which an issue needs: fill a template that takes one; nothing reached GitHub", verb, a.Template)
	}
	return a, nil
}

// createIssue files an issue from a filled template the template component
// accepts, its title and document as they stand, through the backend.
func (g *GitHub) createIssue(ctx context.Context, a NewIssue) (Written, error) {
	if a.Owner == "" || a.Repo == "" {
		return Written{}, fmt.Errorf("github issue-create: a write names the owner and the repository, not %+v", a)
	}
	accepted, err := g.accept(ctx, "issue-create", a.Filled, true)
	if err != nil {
		return Written{}, err
	}
	title, body := *accepted.Title, accepted.Document
	g.mu.Lock()
	backend, rest := g.backend, g.rest
	g.mu.Unlock()
	w := Written{Repo: repoOf(a.Owner, a.Repo), Via: backend}
	switch backend {
	case Token:
		payload := map[string]any{"title": title, "body": body}
		if len(a.Labels) > 0 {
			payload["labels"] = a.Labels
		}
		data, err := rest.post(repoPath(a.Owner, a.Repo)+"/issues", payload)
		if err != nil {
			return Written{}, fmt.Errorf("github issue-create: %w", err)
		}
		w.Data = data
	case Relay:
		input := map[string]any{"method": "create", "owner": a.Owner, "repo": a.Repo, "title": title, "body": body}
		if len(a.Labels) > 0 {
			input["labels"] = toAny(a.Labels)
		}
		s, err := g.step(ctx, StepAsk{Tool: "issue_write", Input: input, Note: fmt.Sprintf("file the issue %q in %s/%s, from template %s", title, a.Owner, a.Repo, accepted.Template)})
		if err != nil {
			return Written{}, fmt.Errorf("github issue-create: %w", err)
		}
		w.Data = s.Data
	default:
		return Written{}, errors.New("github issue-create: the component has not started")
	}
	w.Read = g.o.Clock.Now().UTC()
	w.Number, w.URL = filed(w.Data)
	return w, nil
}

// comment posts a comment from a filled template the template component
// accepts, its document as it stands, through the backend.
func (g *GitHub) comment(ctx context.Context, a NewComment) (Written, error) {
	if a.Owner == "" || a.Repo == "" || a.Number <= 0 {
		return Written{}, fmt.Errorf("github issue-comment: a write names the owner, the repository and a number above 0, not %+v", a)
	}
	accepted, err := g.accept(ctx, "issue-comment", a.Filled, false)
	if err != nil {
		return Written{}, err
	}
	body := accepted.Document
	g.mu.Lock()
	backend, rest := g.backend, g.rest
	g.mu.Unlock()
	w := Written{Repo: repoOf(a.Owner, a.Repo), Number: a.Number, Via: backend}
	switch backend {
	case Token:
		data, err := rest.post(fmt.Sprintf("%s/issues/%d/comments", repoPath(a.Owner, a.Repo), a.Number), map[string]any{"body": body})
		if err != nil {
			return Written{}, fmt.Errorf("github issue-comment: %w", err)
		}
		w.Data = data
	case Relay:
		input := map[string]any{"owner": a.Owner, "repo": a.Repo, "issue_number": a.Number, "body": body}
		s, err := g.step(ctx, StepAsk{Tool: "add_issue_comment", Input: input, Note: fmt.Sprintf("comment on %s/%s#%d, from template %s", a.Owner, a.Repo, a.Number, accepted.Template)})
		if err != nil {
			return Written{}, fmt.Errorf("github issue-comment: %w", err)
		}
		w.Data = s.Data
	default:
		return Written{}, errors.New("github issue-comment: the component has not started")
	}
	w.Read = g.o.Clock.Now().UTC()
	_, w.URL = filed(w.Data)
	return w, nil
}

func toAny(list []string) []any {
	out := make([]any, len(list))
	for i, s := range list {
		out[i] = s
	}
	return out
}

// issueURL ends in the issue's number: .../issues/<n>, a comment's anchor after it.
var issueURL = regexp.MustCompile(`/(?:issues|pull)/(\d+)(?:#.*)?$`)

// filed reads the number and the link of what a write wrote from GitHub's
// answer: REST's number and html_url, or a tool's url.
func filed(data any) (int, string) {
	m, _ := data.(map[string]any)
	link, _ := m["html_url"].(string)
	if link == "" {
		link, _ = m["url"].(string)
	}
	if n, ok := number(m["number"]); ok {
		return n, link
	}
	if sub := issueURL.FindStringSubmatch(link); sub != nil {
		n, _ := strconv.Atoi(sub[1])
		return n, link
	}
	return 0, link
}

// post sends one write to path, waiting out the pace as a read does, each
// request bounded by RequestWait on the component's clock; a rate
// limit refuses the write before GitHub takes it, so it is sent again, up to
// attempts times. It answers GitHub's JSON.
func (c *rest) post(path string, payload any) (any, error) {
	data, err := json.Marshal(payload)
	if err != nil {
		return nil, err
	}
	for attempt := 1; ; attempt++ {
		if err := c.paced("POST " + path); err != nil {
			return nil, err
		}
		ctx, cancel := host.Within(context.Background(), c.clock, RequestWait)
		req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.api+path, bytes.NewReader(data))
		if err != nil {
			cancel()
			return nil, err
		}
		req.Header.Set("Accept", "application/vnd.github+json")
		req.Header.Set("X-GitHub-Api-Version", "2022-11-28")
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Authorization", "Bearer "+c.token)
		resp, err := c.client.Do(req)
		if err != nil {
			cancel()
			return nil, fmt.Errorf("POST %s: %w", path, err)
		}
		body, err := io.ReadAll(resp.Body)
		resp.Body.Close()
		cancel()
		if err != nil {
			return nil, fmt.Errorf("POST %s: %w", path, err)
		}
		if resp.StatusCode == http.StatusCreated || resp.StatusCode == http.StatusOK {
			var v any
			if err := json.Unmarshal(body, &v); err != nil {
				return nil, fmt.Errorf("POST %s: the answer is no JSON: %w", path, err)
			}
			return v, nil
		}
		pace, limited := c.limit(resp)
		if !limited {
			return nil, fmt.Errorf("POST %s: %s: %.200s", path, resp.Status, body)
		}
		if attempt == attempts {
			return nil, fmt.Errorf("POST %s: %s, rate limited %d times", path, resp.Status, attempts)
		}
		c.mu.Lock()
		if until := c.clock.Now().Add(pace); until.After(c.until) {
			c.until = until
		}
		c.mu.Unlock()
	}
}
