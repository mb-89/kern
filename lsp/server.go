// Package lsp is kern's language server, `<name> lsp`: a server of the
// Language Server Protocol over stdio, which an editor starts and speaks to.
// It shows the checker's findings (package lint) as diagnostics on every
// managed file. It asks the core's lsp component (components/lsp) for them
// with the command `lsp lint`, the one `<name> lint` and the tests ask, so
// the editor shows exactly what they find. Reused from quackitect v5
// (`spec/design_output/lsp.md`): one checker, and the redraw roads open,
// type, save, close and disk change. kern/README.md "Language server" is its
// contract.
package lsp

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/textproto"
	"net/url"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"kern/host"
	"kern/lint"
	"kern/mcp"
)

// Command is the core's command the server asks (components/lsp.Command).
const Command = "lsp lint"

// Source is the server's name in each diagnostic, as the editor shows it.
const Source = "kern"

// CallWait bounds one ask of the core.
const CallWait = 10 * time.Second

// Core is the core as the server reaches it: the real one (Host) or a test's.
type Core interface {
	// Start, Status, Lease and Call are the MCP door's (mcp.Core).
	Start() (host.Started, error)
	Status() (host.Status, error)
	Lease(session string, end bool) error
	Call(ctx context.Context, command string, args []string) (host.CallAnswer, error)
	// Watch calls changed each time the name's value changes, until stop is called.
	Watch(name string, changed func()) (stop func(), err error)
}

// Options are what a server serves.
type Options struct {
	// App is the application's name, the binary's: se, kern.
	App, Version string
	// Root is the checkout, whose managed files the diagnostics are on.
	Root string
	Core Core
	// Findings is the name the lsp instance's findings stand at
	// (`"lsp.findings" = "lsp/findings"`): its change, a change on the disk
	// the model heard, redraws. "" is none, and only the editor's roads do.
	Findings string
	// NoStart is why the server starts no core, such as no path to its
	// binary: it attaches to one that runs, and says why where none does.
	NoStart string
	// Session is the lease's session id; empty for one of its own.
	Session string
	// Look and Renew replace the supervisor's spans where set: its look
	// while a start is in flight, and the lease's renewal (mcp.Renew).
	Look, Renew time.Duration
	// Clock is what the supervisor waits on: the wall clock where none is given.
	Clock host.Clock
	// Log takes what the server says beside the protocol: stderr.
	Log io.Writer
}

// server is one served connection.
type server struct {
	o   Options
	out *bufio.Writer

	wmu sync.Mutex // the writer's

	mu        sync.Mutex        // what follows
	open      map[string]string // each open buffer's text, by path from the root
	published map[string]bool   // each path the last publish gave diagnostics
	said      string            // the last failure told the editor, so it is told once
	ready     bool              // the client said initialized

	// redraw is the one signal that a publish is due; publishes run one at a time.
	redraw chan struct{}
}

// Serve serves the language server on in and out until the client says
// exit, hangs up (EOF on in) or ctx ends. It answers the handshake at once
// and reaches the core beside the protocol, as the MCP door does.
func Serve(ctx context.Context, o Options, in io.Reader, out io.Writer) error {
	if o.Log == nil {
		o.Log = io.Discard
	}
	if o.Clock == nil {
		o.Clock = host.Wall{}
	}
	if o.Look == 0 {
		o.Look = 20 * time.Millisecond
	}
	if o.Renew == 0 {
		o.Renew = mcp.Renew
	}
	if o.Session == "" {
		o.Session = fmt.Sprintf("lsp-%d-%d", os.Getpid(), time.Now().UnixNano())
	}
	s := &server{o: o, out: bufio.NewWriter(out), open: map[string]string{}, published: map[string]bool{}, redraw: make(chan struct{}, 1)}
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	var wg sync.WaitGroup
	wg.Add(2)
	go func() { defer wg.Done(); s.supervise(ctx) }()
	go func() { defer wg.Done(); s.publisher(ctx) }()
	err := s.read(ctx, bufio.NewReader(in))
	cancel()
	wg.Wait()
	return err
}

// message is one JSON-RPC 2.0 message: a request, a notification or a response.
type message struct {
	JSONRPC string           `json:"jsonrpc"`
	ID      *json.RawMessage `json:"id,omitempty"`
	Method  string           `json:"method,omitempty"`
	Params  json.RawMessage  `json:"params,omitempty"`
	Result  any              `json:"result,omitempty"`
	Error   *rpcError        `json:"error,omitempty"`
}

type rpcError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

// The protocol's error codes the server answers.
const (
	parseError     = -32700
	methodNotFound = -32601
	invalidParams  = -32602
)

// read reads the client's messages, each a header with its Content-Length
// and a JSON body, and handles them in order.
func (s *server) read(ctx context.Context, r *bufio.Reader) error {
	tp := textproto.NewReader(r)
	for {
		if ctx.Err() != nil {
			return nil
		}
		header, err := tp.ReadMIMEHeader()
		if err != nil {
			if errors.Is(err, io.EOF) {
				return nil // the client hung up
			}
			return fmt.Errorf("%s lsp: reading a header: %w", s.o.App, err)
		}
		n, err := strconv.Atoi(header.Get("Content-Length"))
		if err != nil || n < 0 {
			return fmt.Errorf("%s lsp: a message without a Content-Length: %v", s.o.App, header)
		}
		body := make([]byte, n)
		if _, err := io.ReadFull(r, body); err != nil {
			return fmt.Errorf("%s lsp: reading a body of %d bytes: %w", s.o.App, n, err)
		}
		var m message
		if err := json.Unmarshal(body, &m); err != nil {
			s.reply(nil, nil, &rpcError{Code: parseError, Message: err.Error()})
			continue
		}
		if done := s.handle(m); done {
			return nil
		}
	}
}

// handle handles one message and says whether the client said exit.
func (s *server) handle(m message) bool {
	switch m.Method {
	case "initialize":
		s.reply(m.ID, map[string]any{
			"capabilities": map[string]any{
				"positionEncoding": "utf-16",
				"textDocumentSync": map[string]any{"openClose": true, "change": 1, "save": map[string]any{"includeText": false}},
			},
			"serverInfo": map[string]any{"name": s.o.App + " lsp", "version": s.o.Version},
		}, nil)
	case "initialized":
		s.mu.Lock()
		s.ready = true
		s.mu.Unlock()
		s.due()
	case "shutdown":
		s.reply(m.ID, nil, nil)
	case "exit":
		return true
	case "textDocument/didOpen":
		var p struct {
			TextDocument struct {
				URI  string `json:"uri"`
				Text string `json:"text"`
			} `json:"textDocument"`
		}
		if s.params(m, &p) {
			s.buffer(p.TextDocument.URI, p.TextDocument.Text, true)
		}
	case "textDocument/didChange":
		var p struct {
			TextDocument struct {
				URI string `json:"uri"`
			} `json:"textDocument"`
			ContentChanges []struct {
				Text string `json:"text"`
			} `json:"contentChanges"`
		}
		if s.params(m, &p) && len(p.ContentChanges) > 0 {
			s.buffer(p.TextDocument.URI, p.ContentChanges[len(p.ContentChanges)-1].Text, true)
		}
	case "textDocument/didSave":
		s.due() // the model hears the disk; the buffer stands until it closes
	case "textDocument/didClose":
		var p struct {
			TextDocument struct {
				URI string `json:"uri"`
			} `json:"textDocument"`
		}
		if s.params(m, &p) {
			s.buffer(p.TextDocument.URI, "", false)
		}
	default:
		if m.ID != nil && m.Method != "" {
			s.reply(m.ID, nil, &rpcError{Code: methodNotFound, Message: fmt.Sprintf("%s lsp serves no %s", s.o.App, m.Method)})
		}
	}
	return false
}

// params reads a message's params into v, answering a request whose params it cannot read.
func (s *server) params(m message, v any) bool {
	if err := json.Unmarshal(m.Params, v); err != nil {
		if m.ID != nil {
			s.reply(m.ID, nil, &rpcError{Code: invalidParams, Message: err.Error()})
		}
		return false
	}
	return true
}

// buffer sets the open buffer at uri to text, or drops it where it closed,
// and redraws. A document outside the root is the server's not.
func (s *server) buffer(uri, text string, open bool) {
	p, ok := s.pathOf(uri)
	if !ok {
		return
	}
	s.mu.Lock()
	if open {
		s.open[p] = text
	} else {
		delete(s.open, p)
	}
	s.mu.Unlock()
	s.due()
}

// due asks for a publish; asks that come while one runs make one more.
func (s *server) due() {
	select {
	case s.redraw <- struct{}{}:
	default:
	}
}

// publisher publishes each time a redraw is due, one publish at a time.
func (s *server) publisher(ctx context.Context) {
	for {
		select {
		case <-ctx.Done():
			return
		case <-s.redraw:
			s.publish(ctx)
		}
	}
}

// publish asks the core for the findings over the model with the open
// buffers in place, and sends each managed file with findings its
// diagnostics, and each that had some and has none now an empty list.
func (s *server) publish(ctx context.Context) {
	s.mu.Lock()
	if !s.ready {
		s.mu.Unlock()
		return
	}
	open, _ := json.Marshal(s.open)
	s.mu.Unlock()
	ctx, cancel := host.Within(ctx, s.o.Clock, CallWait)
	defer cancel()
	a, err := s.o.Core.Call(ctx, Command, []string{string(open)})
	if err == nil && a.Error != "" {
		err = errors.New(a.Error)
	}
	var got struct {
		Findings []lint.Finding `json:"findings"`
	}
	if err == nil {
		if err = json.Unmarshal([]byte(a.Text), &got); err != nil {
			err = fmt.Errorf("%s answers no findings: %w", Command, err)
		}
	}
	if err != nil {
		s.tell(fmt.Sprintf("%s lsp: %s: %v", s.o.App, Command, err))
		return
	}
	s.tell("")
	byPath := map[string][]map[string]any{}
	for _, f := range got.Findings {
		byPath[f.Path] = append(byPath[f.Path], Diagnostic(f))
	}
	s.mu.Lock()
	var paths []string
	for p := range s.published {
		if _, still := byPath[p]; !still {
			paths = append(paths, p)
		}
	}
	for p := range byPath {
		paths = append(paths, p)
	}
	s.published = map[string]bool{}
	for p := range byPath {
		s.published[p] = true
	}
	s.mu.Unlock()
	sort.Strings(paths)
	for _, p := range paths {
		diags := byPath[p]
		if diags == nil {
			diags = []map[string]any{}
		}
		s.notify("textDocument/publishDiagnostics", map[string]any{"uri": s.uriOf(p), "diagnostics": diags})
	}
}

// Diagnostic is a finding as the protocol carries it: its range on its line
// (to the line's end where it names no end), an error, the rule as its code.
func Diagnostic(f lint.Finding) map[string]any {
	start := map[string]int{"line": f.Line - 1, "character": f.Col - 1}
	end := map[string]int{"line": f.Line, "character": 0}
	if f.EndCol > 0 {
		end = map[string]int{"line": f.Line - 1, "character": f.EndCol - 1}
	}
	return map[string]any{
		"range":    map[string]any{"start": start, "end": end},
		"severity": 1,
		"source":   Source,
		"code":     f.Rule,
		"message":  f.Message,
	}
}

// tell says why the findings cannot be had in the editor's log, once per
// cause; "" clears it, so the next failure is told again.
func (s *server) tell(why string) {
	s.mu.Lock()
	same := why == s.said
	s.said = why
	s.mu.Unlock()
	if why == "" || same {
		return
	}
	fmt.Fprintln(s.o.Log, why)
	s.notify("window/logMessage", map[string]any{"type": 1, "message": why})
}

// supervise starts the core or attaches to it and holds the session's lease,
// with the MCP door's steps (mcp.Next); on each core it leases anew it
// watches the findings, so a change on the disk redraws.
func (s *server) supervise(ctx context.Context) {
	spans := mcp.Spans{Look: s.o.Look, Renew: s.o.Renew}
	leased, inFlight, pid := false, false, 0
	stop := func() {}
	defer func() {
		stop()
		if leased {
			_ = s.o.Core.Lease(s.o.Session, true)
		}
	}()
	for {
		st, err := s.o.Core.Status()
		state := mcp.StateOf(st, err)
		step := mcp.Next(mcp.Look{Core: state, Leased: leased, InFlight: inFlight}, spans)
		switch step.Do {
		case mcp.Lease:
			if err := s.o.Core.Lease(s.o.Session, false); err != nil {
				leased = false
				step = mcp.Refused(mcp.Lease, spans)
				break
			}
			if !leased || st.Health.Pid != pid {
				stop()
				stop = func() {}
				if s.o.Findings != "" {
					if cancel, err := s.o.Core.Watch(s.o.Findings, s.due); err == nil {
						stop = cancel
					} else {
						fmt.Fprintf(s.o.Log, "%s lsp: watching %s: %v\n", s.o.App, s.o.Findings, err)
					}
				}
				s.due()
			}
			leased, inFlight, pid = true, false, st.Health.Pid
		case mcp.Fail:
			s.tell(fmt.Sprintf("%s lsp: the core ended at its start; it said:\n%s", s.o.App, st.Died))
			inFlight = false
		case mcp.Start:
			leased = false
			if s.o.NoStart != "" {
				s.tell(fmt.Sprintf("%s lsp: no core runs, and the server starts none: %s", s.o.App, s.o.NoStart))
				step = mcp.Step{Do: mcp.Wait, Wait: spans.Renew}
				break
			}
			if _, err := s.o.Core.Start(); err != nil {
				s.tell(fmt.Sprintf("%s lsp: starting the core: %v", s.o.App, err))
				step = mcp.Refused(mcp.Start, spans)
				break
			}
			inFlight = true
		}
		select {
		case <-ctx.Done():
			return
		case <-s.o.Clock.After(step.Wait):
		}
	}
}

func (s *server) reply(id *json.RawMessage, result any, e *rpcError) {
	m := map[string]any{"jsonrpc": "2.0", "id": id}
	if e != nil {
		m["error"] = e
	} else {
		m["result"] = result
	}
	s.send(m)
}

func (s *server) notify(method string, params any) {
	s.send(map[string]any{"jsonrpc": "2.0", "method": method, "params": params})
}

// send writes one message with its header.
func (s *server) send(v any) {
	data, err := json.Marshal(v)
	if err != nil {
		fmt.Fprintf(s.o.Log, "%s lsp: %v\n", s.o.App, err)
		return
	}
	s.wmu.Lock()
	defer s.wmu.Unlock()
	fmt.Fprintf(s.out, "Content-Length: %d\r\n\r\n", len(data))
	s.out.Write(data)
	s.out.Flush()
}

// pathOf answers the path from the root, slash-separated, of a file:// URI
// under it.
func (s *server) pathOf(uri string) (string, bool) {
	u, err := url.Parse(uri)
	if err != nil || u.Scheme != "file" {
		return "", false
	}
	p := u.Path
	if runtime.GOOS == "windows" {
		p = strings.TrimPrefix(p, "/") // /C:/x is C:/x
	}
	rel, err := filepath.Rel(s.o.Root, filepath.FromSlash(p))
	if err != nil {
		return "", false
	}
	rel = filepath.ToSlash(rel)
	if rel == "." || rel == ".." || strings.HasPrefix(rel, "../") {
		return "", false
	}
	return rel, true
}

// uriOf answers the file:// URI of a path from the root.
func (s *server) uriOf(p string) string {
	abs := filepath.ToSlash(filepath.Join(s.o.Root, filepath.FromSlash(p)))
	if !strings.HasPrefix(abs, "/") {
		abs = "/" + abs // C:/x is /C:/x
	}
	return (&url.URL{Scheme: "file", Path: abs}).String()
}
