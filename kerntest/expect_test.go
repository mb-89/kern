package kerntest_test

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/pelletier/go-toml/v2"

	"kern/index"
	"kern/kerntest"
)

func TestAnExpectationFileReadsItsExpectationsAndClaims(t *testing.T) {
	t.Parallel()
	x, err := kerntest.ParseExpectations("reader.expect.toml", []byte(`
[[expect]]
name = "numbers"
port = "numbers"
given = "numbers stand"
value = [1, 2, 3]

[[expect]]
name = "lines.append"
request = "post"
ask = ""
refused = true
exact = ["ask"]

[[expect]]
name = "lines.append"
request = "post"
ask = "a word"
answer = 1
match = "exact"

[[claim]]
name = "note"
port = "note"
`))
	if err != nil {
		t.Fatal(err)
	}
	if len(x.Expect) != 3 || len(x.Claims) != 1 || x.Claims[0] != (kerntest.Claim{Name: "note", Port: "note"}) {
		t.Fatalf("read %+v", x)
	}
	if got := x.Expect[0].String(); got != `expect 1 (port numbers, given "numbers stand")` {
		t.Errorf("the first expectation reads %s", got)
	}
	if got := x.Expect[1].String(); got != `expect 2 (request post, ask "")` {
		t.Errorf("the second expectation reads %s", got)
	}
	if got := x.Expect[2].Exact; strings.Join(got, ",") != "value,ask,answer" {
		t.Errorf("match = exact holds %v exact", got)
	}
}

// expectCases is kerntest/testdata/expect-cases.json, which python/tests
// holds kern.expect to as well.
type expectCases struct {
	Refused []struct{ File, Refusal string }
	Matches []struct {
		Want    string
		Got     any
		Exact   []string
		Refusal string
	}
}

func readExpectCases(t *testing.T) expectCases {
	t.Helper()
	data, err := os.ReadFile(filepath.Join("testdata", "expect-cases.json"))
	if err != nil {
		t.Fatal(err)
	}
	var c expectCases
	if err := json.Unmarshal(data, &c); err != nil {
		t.Fatal(err)
	}
	return c
}

func TestAnExpectationFileRefusesWhatIsMalformed(t *testing.T) {
	t.Parallel()
	for _, c := range readExpectCases(t).Refused {
		_, err := kerntest.ParseExpectations("bad.expect.toml", []byte(c.File))
		if err == nil || !strings.Contains(err.Error(), c.Refusal) || !strings.HasPrefix(err.Error(), "bad.expect.toml") {
			t.Errorf("%q: refused with %v, want %q naming the file", c.File, err, c.Refusal)
		}
	}
}

func TestAnExpectationMatchesByTypeAndOnItsExactPathsByEquality(t *testing.T) {
	t.Parallel()
	for _, c := range readExpectCases(t).Matches {
		var want struct{ Value any }
		if err := toml.Unmarshal([]byte(c.Want), &want); err != nil {
			t.Fatal(err)
		}
		err := kerntest.Match("value", want.Value, c.Got, c.Exact)
		switch {
		case c.Refusal == "" && err != nil:
			t.Errorf("%s against %v: refused with %v", c.Want, c.Got, err)
		case c.Refusal != "" && (err == nil || err.Error() != c.Refusal):
			t.Errorf("%s against %v: refused with %v, want %q", c.Want, c.Got, err, c.Refusal)
		}
	}
	type issue struct {
		Number int      `json:"number"`
		Labels []string `json:"labels"`
	}
	if err := kerntest.Match("value", map[string]any{"number": int64(1), "labels": []any{"work"}}, issue{8, []string{"work"}}, nil); err != nil {
		t.Errorf("a Go struct matches as its JSON: %v", err)
	}
}

type checkIn struct {
	Numbers []int `index:"numbers"`
}

func TestAnExpectationFileIsHeldToItsComponentsDeclaration(t *testing.T) {
	t.Parallel()
	c := index.Declare("reader", "Reads numbers and posts lines.",
		index.Request[string, int]("post", "Posts a line."),
		index.Derived("sum", 0, "The sum.", func(in checkIn) (int, error) { return len(in.Numbers), nil }),
	)
	x, err := kerntest.ParseExpectations("reader.expect.toml", []byte(`
[[expect]]
name = "numbers"
port = "numbers"
value = ["one"]

[[expect]]
name = "numbers"
port = "elsewhere"
value = [1]

[[expect]]
name = "lines.append"
request = "post"
ask = 1
answer = "one"

[[expect]]
name = "lines.append"
request = "mail"
ask = "x"
answer = 1

[[claim]]
name = "total"
port = "total"

[[claim]]
name = "lines.append"
verb = "append"
`))
	if err != nil {
		t.Fatal(err)
	}
	err = x.Check(c)
	for _, want := range []string{
		`expect 1 (port numbers): value: ["one"] is no []int`,
		`expect 2 (port elsewhere): component reader reads no input "elsewhere"`,
		`expect 3 (request post, ask 1): ask: 1 is no string`,
		`answer: "one" is no int`,
		`component reader has no request port "mail"`,
		`claim 1: component reader writes no output "total"`,
		`claim 2: component reader accepts no verb "append"`,
	} {
		if err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("the check answered %v, want %q", err, want)
		}
	}
}

// reader is the component the fake answers from its expectations: it sums
// the numbers it reads, and posts each word, keeping the last answer.
func readerComponent() *index.Component {
	post := index.Request[string, int]("post", "Posts a line.")
	return index.Declare("reader", "Sums numbers and posts lines.",
		post, index.Output("filed", 0, "The last line's number."),
		index.Derived("sum", 0, "The sum.", func(in checkIn) (int, error) {
			s := 0
			for _, n := range in.Numbers {
				s += n
			}
			return s, nil
		}),
		index.Command(index.CommandDecl{Name: "file", Usage: "file WORDS...", Summary: "File words.", Help: "Posts each word.", Group: "Tests", Class: index.Batch},
			func(_ struct{}, args []string) (index.Result, error) {
				asks := make([]index.Ask, len(args))
				for i, a := range args {
					asks[i] = post.Ask(a)
				}
				return index.Result{Requests: asks, Then: func(answers []any) (index.Result, error) {
					return index.Result{Text: fmt.Sprintf("filed as line %d", post.Answer(answers[len(answers)-1]))}, nil
				}}, nil
			}),
	)
}

const readerExpects = `
[[expect]]
name = "numbers"
port = "numbers"
value = []
match = "exact"

[[expect]]
name = "numbers"
port = "numbers"
given = "numbers stand"
value = [1, 2, 3]

[[expect]]
name = "lines.append"
request = "post"
ask = ""
exact = ["ask"]
refused = true

[[expect]]
name = "lines.append"
request = "post"
ask = "a word"
answer = 1

[[expect]]
name = "lines.append"
request = "post"
given = "two lines stand"
ask = "a word"
answer = 3
`

func writeFile(t *testing.T, name, text string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), name)
	if err := os.WriteFile(path, []byte(text), 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestAReadersTestIsAnsweredFromItsOwnExpectations(t *testing.T) {
	t.Parallel()
	path := writeFile(t, "reader.expect.toml", readerExpects)
	f, err := kerntest.Expecting(readerComponent(), path)
	if err != nil {
		t.Fatal(err)
	}
	must := func(err error) {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
	}
	// No given: numbers reads the expectation with none, the empty list.
	must(f.Derive("sum"))
	if got, _ := f.Out("sum"); got != (index.Value{V: 0, Provided: true}) {
		t.Fatalf("sum with no given is %+v", got)
	}
	must(f.Given("numbers stand"))
	must(f.Derive("sum"))
	if got, _ := f.Out("sum"); got != (index.Value{V: 6, Provided: true}) {
		t.Fatalf("sum given numbers stand is %+v", got)
	}
	// A request is answered by the expectation whose ask it matches.
	if text, err := f.Call("file", "x", "y"); err != nil || text != "filed as line 1" {
		t.Fatalf("file answered %q, %v", text, err)
	}
	if _, err := f.Call("file", ""); err == nil || !strings.Contains(err.Error(), `refused, as expect 3 (request post, ask "") of `+path+" expects") {
		t.Fatalf("an empty line was answered: %v", err)
	}
	// Its givens' expectations answer first; the others still stand.
	must(f.Given("two lines stand"))
	if text, err := f.Call("file", "z"); err != nil || text != "filed as line 3" {
		t.Fatalf("file given two lines stand answered %q, %v", text, err)
	}
	must(f.Derive("sum"))
	if got, _ := f.Out("sum"); got != (index.Value{V: 0, Provided: true}) {
		t.Fatalf("sum, numbers stand no longer given, is %+v", got)
	}
	// The test states nothing itself: no seed, no answer, no unknown given.
	if err := f.Seed("numbers", []int{9}); err == nil || !strings.Contains(err.Error(), "is answered from "+path) {
		t.Errorf("the fake took a seed: %v", err)
	}
	if err := f.Answer("post", func(any) (any, error) { return 9, nil }); err == nil || !strings.Contains(err.Error(), "is answered from "+path) {
		t.Errorf("the fake took an answerer: %v", err)
	}
	if err := f.Given("the ledger burned"); err == nil || !strings.Contains(err.Error(), `no expectation is given "the ledger burned"`) {
		t.Errorf("the fake took an unknown given: %v", err)
	}
}

func TestARequestNoExpectationAnswersFailsTheReadersTest(t *testing.T) {
	t.Parallel()
	path := writeFile(t, "reader.expect.toml", `
[[expect]]
name = "lines.append"
request = "post"
ask = "only this"
exact = ["ask"]
answer = 1
`)
	f, err := kerntest.Expecting(readerComponent(), path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.Call("file", "that"); err == nil || !strings.Contains(err.Error(), path+`: no expectation answers the request "that" on port post: state it there`) {
		t.Fatalf("an unexpected request was answered: %v", err)
	}
	if _, err := kerntest.Expecting(readerComponent(), writeFile(t, "bad.expect.toml", "[[expect]]\nname = \"n\"\nport = \"nowhere\"\nvalue = 1\n")); err == nil || !strings.Contains(err.Error(), `reads no input "nowhere"`) {
		t.Fatalf("the fake took an expectation of a port the reader lacks: %v", err)
	}
}
