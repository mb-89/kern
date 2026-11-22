package kerntest

import (
	"bytes"
	"encoding/json"
	"fmt"
	"math"
	"os"
	"reflect"
	"regexp"
	"slices"
	"sort"
	"strings"
	"time"

	"github.com/pelletier/go-toml/v2"

	"kern/index"
)

// Expectations is one component's expectation file, kept beside its tests: what
// its tests expect of each name it reads and each request it sends, and the
// names and requests it claims, whose pooled expectations its own tests meet.
// No line of it names the component on the other side (README.md "Expectations").
//
//	[[expect]]                   # a value it reads
//	name = "numbers"             # the name it pools under
//	port = "numbers"             # its own input port, which the fake seeds
//	given = "numbers stand"      # optional: the precondition its claimer sets up
//	value = [1, 2, 3]            # the least the reader relies on, matched by type
//
//	[[expect]]                   # a request it sends
//	name = "lines.append"
//	request = "post"             # its own request port, which the fake answers
//	ask = ""                     # what it sends
//	refused = true               # the answer it relies on: refused, or answer = <value>
//	exact = ["ask"]              # the paths matched by equality; match = "exact" is all of them
//
//	[[claim]]                    # a name it writes, on its output port
//	name = "numbers"
//	port = "numbers"
//
//	[[claim]]                    # a request it answers, with its verb
//	name = "lines.append"
//	verb = "append"
type Expectations struct {
	File   string
	Expect []Expectation
	Claims []Claim
}

// Expectation is one thing a reader's tests rely on of a name or a request.
type Expectation struct {
	// Name is the name it pools under, never the component that writes or answers it.
	Name string
	// Port is the reader's own input port the value is read on; Request is
	// its own request port the request goes out on. Exactly one is set.
	Port, Request string
	// Given is the precondition the claimer sets up before it meets this
	// expectation, and the reader's test names to be answered from it; "" is
	// none, the state it starts in.
	Given string
	// Value is the value read; Ask is the request sent and Answer its answer,
	// or Refused where the reader relies on its refusal.
	Value, Ask, Answer any
	Refused            bool
	// Exact are the paths matched by equality, rooted at value, ask or
	// answer: "value", "answer.title", "value[].number" for each item of a
	// list. The rest is matched by type: a number, a string, a bool, a list
	// with at least one item where the expectation's has one, each item like
	// one of the expectation's, and a table with at least the expectation's
	// keys, each like its value.
	Exact []string
	// File is the reader's expectation file and N the expectation's place in it, from 1.
	File string
	N    int
	// New is set where the pool's base holds no expectation with these terms
	// in the reader's file (pending.go); no file sets it.
	New bool
}

// Claim is a name a component writes on one of its output ports, or a request
// it answers with one of its verbs: its tests meet every expectation pooled
// under the name.
type Claim struct {
	Name, Port, Verb string
}

// String names the expectation as every message does: its place, its port or
// request, and its given.
func (e Expectation) String() string {
	s := fmt.Sprintf("expect %d (", e.N)
	if e.Port != "" {
		s += "port " + e.Port
	} else {
		s += "request " + e.Request + ", ask " + show(e.Ask)
	}
	if e.Given != "" {
		s += fmt.Sprintf(", given %q", e.Given)
	}
	return s + ")"
}

var (
	expectKeys = []string{"name", "port", "request", "given", "value", "ask", "answer", "refused", "match", "exact"}
	claimKeys  = []string{"name", "port", "verb"}
)

var poolName = regexp.MustCompile(`^[a-z][a-z0-9-]*(\.[a-z][a-z0-9-]*)*$`)

type expectFile struct {
	Expect []struct {
		Name    string   `toml:"name"`
		Port    string   `toml:"port"`
		Request string   `toml:"request"`
		Given   string   `toml:"given"`
		Value   any      `toml:"value"`
		Ask     any      `toml:"ask"`
		Answer  any      `toml:"answer"`
		Refused bool     `toml:"refused"`
		Match   string   `toml:"match"`
		Exact   []string `toml:"exact"`
	} `toml:"expect"`
	Claim []struct {
		Name string `toml:"name"`
		Port string `toml:"port"`
		Verb string `toml:"verb"`
	} `toml:"claim"`
}

// ReadExpectations reads a component's expectation file, and refuses one that
// is malformed: each refusal names the file and the expectation.
func ReadExpectations(path string) (Expectations, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return Expectations{}, fmt.Errorf("expectations: %w", err)
	}
	return ParseExpectations(path, data)
}

// ParseExpectations reads an expectation file's text; file names it in every message.
func ParseExpectations(file string, data []byte) (Expectations, error) {
	var raw map[string]any
	if err := toml.Unmarshal(data, &raw); err != nil {
		return Expectations{}, fmt.Errorf("%s: %v", file, err)
	}
	var errs []string
	fail := func(format string, a ...any) { errs = append(errs, fmt.Sprintf(format, a...)) }
	for _, k := range sortedKeys(raw) {
		if k != "expect" && k != "claim" {
			fail("no key %s: an expectation file holds [[expect]] and [[claim]]", k)
		}
	}
	for _, kind := range []string{"expect", "claim"} {
		tables, _ := raw[kind].([]any)
		for i, t := range tables {
			m, _ := t.(map[string]any)
			for _, k := range sortedKeys(m) {
				switch {
				case kind == "expect" && !slices.Contains(expectKeys, k):
					fail("expect %d: no key %s: an expectation holds name, port or request, given, value, ask, answer or refused, match and exact", i+1, k)
				case kind == "claim" && !slices.Contains(claimKeys, k):
					fail("claim %d: no key %s: a claim holds name, and port or verb", i+1, k)
				}
			}
		}
	}
	var doc expectFile
	if err := toml.Unmarshal(data, &doc); err != nil {
		return Expectations{}, fmt.Errorf("%s: %v", file, err)
	}
	x := Expectations{File: file}
	for i, d := range doc.Expect {
		e := Expectation{
			Name: d.Name, Port: d.Port, Request: d.Request, Given: d.Given,
			Value: d.Value, Ask: d.Ask, Answer: d.Answer, Refused: d.Refused, Exact: d.Exact,
			File: file, N: i + 1,
		}
		where := fmt.Sprintf("expect %d", e.N)
		if !poolName.MatchString(e.Name) {
			fail("%s: name %q is no name: lowercase words of letters, digits and dashes, joined by dots", where, e.Name)
		}
		switch {
		case (e.Port == "") == (e.Request == ""):
			fail("%s: an expectation sets port, a value it reads, or request, a request it sends, and not both", where)
		case e.Port != "":
			if e.Value == nil {
				fail("%s: port %s reads no value: set value", where, e.Port)
			}
			if e.Ask != nil || e.Answer != nil || e.Refused {
				fail("%s: port %s reads a value: ask, answer and refused belong to a request", where, e.Port)
			}
		default:
			if e.Ask == nil {
				fail("%s: request %s sends nothing: set ask", where, e.Request)
			}
			if e.Value != nil {
				fail("%s: request %s: value belongs to a port; a request has ask and answer", where, e.Request)
			}
			if (e.Answer == nil) == !e.Refused {
				fail("%s: request %s relies on an answer or on its refusal: set answer, or refused = true, and not both", where, e.Request)
			}
		}
		switch d.Match {
		case "", "type":
		case "exact":
			e.Exact = append([]string{"value", "ask", "answer"}, e.Exact...)
		default:
			fail("%s: match is type or exact, not %q", where, d.Match)
		}
		for _, p := range d.Exact {
			if root, _, _ := strings.Cut(strings.SplitN(p, "[", 2)[0], "."); root != "value" && root != "ask" && root != "answer" {
				fail("%s: exact path %q starts at value, ask or answer", where, p)
			}
		}
		x.Expect = append(x.Expect, e)
	}
	seen := map[string]int{}
	for _, e := range x.Expect {
		if e.Port == "" {
			continue
		}
		k := e.Port + "\x00" + e.Given
		if n, ok := seen[k]; ok {
			fail("expect %d: expect %d already reads port %s given %q: one value per port and given", e.N, n, e.Port, e.Given)
		}
		seen[k] = e.N
	}
	for i, d := range doc.Claim {
		c := Claim{Name: d.Name, Port: d.Port, Verb: d.Verb}
		where := fmt.Sprintf("claim %d", i+1)
		if !poolName.MatchString(c.Name) {
			fail("%s: name %q is no name: lowercase words of letters, digits and dashes, joined by dots", where, c.Name)
		}
		if (c.Port == "") == (c.Verb == "") {
			fail("%s: a claim sets port, the output it writes the name on, or verb, the verb that answers the request, and not both", where)
		}
		for j, prev := range x.Claims {
			if prev.Name == c.Name {
				fail("%s: claim %d already claims name %q", where, j+1, c.Name)
			}
		}
		x.Claims = append(x.Claims, c)
	}
	if len(errs) > 0 {
		return Expectations{}, fmt.Errorf("%s: %s", file, strings.Join(errs, "; "))
	}
	return x, nil
}

// Check refuses expectations and claims c does not declare: a port it does
// not read, a request port it lacks, a value, ask or answer its port's type
// cannot hold, and a claim on an output it does not write or a verb it lacks.
func (x Expectations) Check(c *index.Component) error {
	inputs := map[string]reflect.Type{}
	for _, p := range c.Inputs() {
		if !strings.HasPrefix(p.Port, index.ConfigPrefix) {
			inputs[p.Port] = p.Type
		}
	}
	requests := map[string]index.RequestDecl{}
	for _, r := range c.Requests() {
		requests[r.Port] = r
	}
	outputs := map[string]bool{}
	for _, p := range c.Outputs() {
		outputs[p.Port] = true
	}
	verbs := map[string]bool{}
	for _, v := range c.Verbs() {
		verbs[v.Verb] = true
	}
	var errs []string
	for _, e := range x.Expect {
		if e.Port != "" {
			t, ok := inputs[e.Port]
			if !ok {
				errs = append(errs, fmt.Sprintf("%s: component %s reads no input %q", e, c.Name(), e.Port))
				continue
			}
			if _, err := convert(e.Value, t); err != nil {
				errs = append(errs, fmt.Sprintf("%s: value: %v", e, err))
			}
			continue
		}
		r, ok := requests[e.Request]
		if !ok {
			errs = append(errs, fmt.Sprintf("%s: component %s has no request port %q", e, c.Name(), e.Request))
			continue
		}
		if _, err := convert(e.Ask, r.Req); err != nil {
			errs = append(errs, fmt.Sprintf("%s: ask: %v", e, err))
		}
		if e.Answer != nil {
			if _, err := convert(e.Answer, r.Ans); err != nil {
				errs = append(errs, fmt.Sprintf("%s: answer: %v", e, err))
			}
		}
	}
	for i, cl := range x.Claims {
		switch {
		case cl.Port != "" && !outputs[cl.Port]:
			errs = append(errs, fmt.Sprintf("claim %d: component %s writes no output %q", i+1, c.Name(), cl.Port))
		case cl.Verb != "" && !verbs[cl.Verb]:
			errs = append(errs, fmt.Sprintf("claim %d: component %s accepts no verb %q", i+1, c.Name(), cl.Verb))
		}
	}
	if len(errs) > 0 {
		return fmt.Errorf("%s: %s", x.File, strings.Join(errs, "; "))
	}
	return nil
}

// convert makes an expectation's value into a value of type t, as a seed or a
// request: by way of JSON, refusing what t cannot hold.
func convert(v any, t reflect.Type) (any, error) {
	data, err := json.Marshal(plain(v))
	if err != nil {
		return nil, err
	}
	p := reflect.New(t)
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.DisallowUnknownFields()
	if err := dec.Decode(p.Interface()); err != nil {
		return nil, fmt.Errorf("%s is no %s: %v", show(v), t, err)
	}
	out := p.Elem().Interface()
	if !index.Fits(out, t) {
		return nil, fmt.Errorf("%s is no %s", show(v), t)
	}
	return out, nil
}

// plain makes a TOML value JSON's: its dates and times as their text.
func plain(v any) any {
	switch x := v.(type) {
	case []any:
		out := make([]any, len(x))
		for i, item := range x {
			out[i] = plain(item)
		}
		return out
	case map[string]any:
		out := make(map[string]any, len(x))
		for k, item := range x {
			out[k] = plain(item)
		}
		return out
	case time.Time:
		return x.Format(time.RFC3339Nano)
	case toml.LocalDate, toml.LocalTime, toml.LocalDateTime:
		return fmt.Sprint(x)
	}
	return v
}

// generic answers v as JSON reads it, its numbers kept as written.
func generic(v any) (any, error) {
	data, err := json.Marshal(v)
	if err != nil {
		return nil, err
	}
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.UseNumber()
	var out any
	return out, dec.Decode(&out)
}

// Match says whether got, a value read or an answer given, meets the
// expectation's want at root ("value", "ask" or "answer"): by type, but on
// the paths exact names, by equality. It answers the first difference,
// naming its path.
func Match(root string, want, got any, exact []string) error {
	g, err := generic(got)
	if err != nil {
		return fmt.Errorf("%s: %v", root, err)
	}
	ex := map[string]bool{}
	for _, p := range exact {
		ex[p] = true
	}
	return match(root, plain(want), g, ex)
}

func match(path string, want, got any, exact map[string]bool) error {
	if exact[path] {
		if !equal(want, got) {
			return fmt.Errorf("%s is %s, want %s", path, show(got), show(want))
		}
		return nil
	}
	switch w := want.(type) {
	case int64:
		if n, ok := got.(json.Number); !ok || !integral(n) {
			return fmt.Errorf("%s is %s, want an integer", path, show(got))
		}
	case float64:
		if _, ok := got.(json.Number); !ok {
			return fmt.Errorf("%s is %s, want a number", path, show(got))
		}
	case string:
		if _, ok := got.(string); !ok {
			return fmt.Errorf("%s is %s, want a string", path, show(got))
		}
	case bool:
		if _, ok := got.(bool); !ok {
			return fmt.Errorf("%s is %s, want true or false", path, show(got))
		}
	case []any:
		g, ok := got.([]any)
		switch {
		case !ok:
			return fmt.Errorf("%s is %s, want a list", path, show(got))
		case len(w) > 0 && len(g) == 0:
			return fmt.Errorf("%s is an empty list, want at least one item", path)
		}
		for i, item := range g {
			var first error
			for _, wi := range w {
				err := match(path+"[]", wi, item, exact)
				if err == nil {
					first = nil
					break
				}
				if first == nil {
					first = err
				}
			}
			if first != nil {
				return fmt.Errorf("item %d: %w", i, first)
			}
		}
	case map[string]any:
		g, ok := got.(map[string]any)
		if !ok {
			return fmt.Errorf("%s is %s, want a table", path, show(got))
		}
		for _, k := range sortedKeys(w) {
			gv, ok := g[k]
			if !ok {
				return fmt.Errorf("%s.%s is missing", path, k)
			}
			if err := match(path+"."+k, w[k], gv, exact); err != nil {
				return err
			}
		}
	default:
		return fmt.Errorf("%s: the expectation holds %T, which no value matches", path, want)
	}
	return nil
}

// equal says whether got, as JSON reads it, is want: numbers by their value,
// lists item by item, tables key by key.
func equal(want, got any) bool {
	switch w := want.(type) {
	case int64:
		n, ok := got.(json.Number)
		f, err := n.Float64()
		return ok && err == nil && f == float64(w)
	case float64:
		n, ok := got.(json.Number)
		f, err := n.Float64()
		return ok && err == nil && f == w
	case []any:
		g, ok := got.([]any)
		if !ok || len(g) != len(w) {
			return false
		}
		for i := range w {
			if !equal(w[i], g[i]) {
				return false
			}
		}
		return true
	case map[string]any:
		g, ok := got.(map[string]any)
		if !ok || len(g) != len(w) {
			return false
		}
		for k, wv := range w {
			gv, ok := g[k]
			if !ok || !equal(wv, gv) {
				return false
			}
		}
		return true
	}
	return want == got
}

func show(v any) string {
	data, err := json.Marshal(plain(v))
	if err != nil {
		return fmt.Sprint(v)
	}
	return string(data)
}

func sortedKeys[V any](m map[string]V) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

// integral says whether n is a whole number, written as one or not.
func integral(n json.Number) bool {
	if _, err := n.Int64(); err == nil {
		return true
	}
	f, err := n.Float64()
	return err == nil && f == math.Trunc(f)
}
