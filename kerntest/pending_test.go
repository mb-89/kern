package kerntest_test

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"kern/git"
	"kern/kerntest"
)

// fourNumbers is the reader's new wish: four numbers, given a precondition
// counter sets up nowhere, so counter does not meet it yet.
const fourNumbers = `
[[expect]]
name = "numbers"
port = "numbers"
given = "four numbers stand"
value = [1, 2, 3, 4]
match = "exact"
`

// landedAs is the base that holds the reader's file as text: what the
// integration branch holds, where each expectation met its claimer.
func landedAs(path, text string) kerntest.Base {
	return func(p string) (kerntest.Expectations, bool, error) {
		if p != path {
			return kerntest.Expectations{}, false, nil
		}
		x, err := kerntest.ParseExpectations(p, []byte(text))
		return x, true, err
	}
}

// newWish is the reader with fourNumbers added after the base it landed as.
func newWish(t *testing.T) (reader, count, ledger kerntest.Module, base kerntest.Base) {
	t.Helper()
	reader, count, ledger = modules(t)
	if err := os.WriteFile(reader.File, []byte(readerExpects+fourNumbers), 0o644); err != nil {
		t.Fatal(err)
	}
	return reader, count, ledger, landedAs(reader.File, readerExpects)
}

func TestANewUnmetExpectationIsPendingInItsClaimersTest(t *testing.T) {
	t.Parallel()
	reader, count, ledger, base := newWish(t)
	pool, err := kerntest.ReadPoolSince(base, reader.File)
	if err != nil {
		t.Fatal(err)
	}
	pending, err := count.MeetPending(pool)
	if err != nil {
		t.Fatalf("a new expectation failed its claimer's test: %v", err)
	}
	want := `pending: name "numbers": expect 6 (port numbers, given "four numbers stand") of ` + reader.File +
		` waits on counter, which does not meet it yet: it sets up no given "four numbers stand"`
	if len(pending) != 1 || pending[0].String() != want {
		t.Fatalf("counter's test reported %v, want one line %q", pending, want)
	}
	if err := count.Meet(pool); err != nil {
		t.Errorf("Meet failed on a pending expectation: %v", err)
	}
	if pending, err := ledger.MeetPending(pool); err != nil || len(pending) != 0 {
		t.Errorf("ledger, which claims no numbers, answered %v, %v", pending, err)
	}
}

func TestANewUnmetExpectationFailsItsReadersCheckNamingTheClaimer(t *testing.T) {
	t.Parallel()
	reader, count, ledger, base := newWish(t)
	err := kerntest.CheckSince(base, wiring(t, "reader", "counter", "lines"), reader, count, ledger)
	want := `pending: name "numbers": expect 6 (port numbers, given "four numbers stand") of ` + reader.File +
		` waits on counter, which does not meet it yet`
	if err == nil || !strings.Contains(err.Error(), want) {
		t.Fatalf("the reader's check answered %v, want %q", err, want)
	}
	var p kerntest.Pending
	if !errors.As(err, &p) || p.Claimer != "counter" {
		t.Errorf("the refusal holds no pending expectation of counter: %#v", p)
	}
	if strings.Contains(err.Error(), "unmet:") {
		t.Errorf("the check refused a landed expectation too: %v", err)
	}
}

func TestOnceItsClaimerMeetsItFailingItFailsTheClaimer(t *testing.T) {
	t.Parallel()
	reader, count, _, _ := newWish(t)
	count.Given["four numbers stand"] = func(f *kerntest.Fake) error { return f.Config("upto", 4) }
	// counter meets the new wish, so its change and the reader's land together:
	// pending ends with no flag to flip.
	pool, err := kerntest.ReadPoolSince(landedAs(reader.File, readerExpects), reader.File)
	if err != nil {
		t.Fatal(err)
	}
	if pending, err := count.MeetPending(pool); err != nil || len(pending) != 0 {
		t.Fatalf("a counter that meets the new wish answered %v, %v", pending, err)
	}
	// Landed, it is an ordinary expectation: a counter that stops meeting it fails.
	landed := landedAs(reader.File, readerExpects+fourNumbers)
	pool, err = kerntest.ReadPoolSince(landed, reader.File)
	if err != nil {
		t.Fatal(err)
	}
	count.Given["four numbers stand"] = func(f *kerntest.Fake) error { return f.Config("upto", 5) }
	pending, err := count.MeetPending(pool)
	if want := `unmet: name "numbers": counter does not meet expect 6 (port numbers, given "four numbers stand") of ` + reader.File; err == nil || !strings.Contains(err.Error(), want) || len(pending) != 0 {
		t.Fatalf("a counter that broke a landed expectation answered %v, %v; want %q", pending, err, want)
	}
}

func TestAnExpectationStaysLandedWhereItMovesInItsFile(t *testing.T) {
	t.Parallel()
	reader, count, _ := modules(t)
	moved := readerExpects[strings.Index(readerExpects, "[[expect]]\nname = \"lines.append\""):] + readerExpects[:strings.Index(readerExpects, "[[expect]]\nname = \"lines.append\"")]
	if err := os.WriteFile(reader.File, []byte(moved), 0o644); err != nil {
		t.Fatal(err)
	}
	pool, err := kerntest.ReadPoolSince(landedAs(reader.File, readerExpects), reader.File)
	if err != nil {
		t.Fatal(err)
	}
	for _, es := range pool {
		for _, e := range es {
			if e.New {
				t.Errorf("%s moved and counts as new", e)
			}
		}
	}
	count.Given = nil
	if pending, err := count.MeetPending(pool); err == nil || len(pending) != 0 {
		t.Errorf("a moved expectation counter breaks is pending: %v, %v", pending, err)
	}
}

func TestWithNoBaseEveryExpectationCountsAsLanded(t *testing.T) {
	t.Parallel()
	reader, count, _, _ := newWish(t)
	pool, err := kerntest.ReadPoolSince(nil, reader.File)
	if err != nil {
		t.Fatal(err)
	}
	if pending, err := count.MeetPending(pool); err == nil || len(pending) != 0 {
		t.Errorf("with no base, an unmet expectation is pending: %v, %v", pending, err)
	}
}

// The report file is the process's, so this test sets it alone.
func TestAPendingExpectationIsAppendedToTheReportFile(t *testing.T) {
	report := filepath.Join(t.TempDir(), "pending.jsonl")
	t.Setenv(kerntest.ReportVariable, report)
	reader, count, ledger, base := newWish(t)
	pool, err := kerntest.ReadPoolSince(base, reader.File)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := count.MeetPending(pool); err != nil {
		t.Fatal(err)
	}
	if err := kerntest.CheckSince(base, wiring(t, "reader", "counter", "lines"), reader, count, ledger); err == nil {
		t.Fatal("the reader's check passed")
	}
	data, err := os.ReadFile(report)
	if err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(strings.TrimSpace(string(data)), "\n")
	if len(lines) != 2 {
		t.Fatalf("the report holds %q, want counter's test's line and the check's", data)
	}
	for _, l := range lines {
		var r kerntest.Report
		if err := json.Unmarshal([]byte(l), &r); err != nil {
			t.Fatal(err)
		}
		if r.Claimer != "counter" || r.Name != "numbers" || filepath.ToSlash(r.File) != filepath.ToSlash(reader.File) || r.N != 6 || !strings.HasPrefix(r.Line, "pending: ") {
			t.Errorf("the report holds %+v", r)
		}
	}
}

// gitAnswers is git as GitBase reaches it: each command's answer, by its arguments.
type gitAnswers map[string]string

func (g gitAnswers) Run(_ context.Context, c git.Command) (string, error) {
	out, ok := g[strings.Join(c.Args, " ")]
	if !ok {
		return "", &git.Error{Command: c, Code: 1, Err: errors.New("exit status 1")}
	}
	return out, nil
}

func TestTheGitBaseIsTheReadersFileAsTheRevisionHoldsIt(t *testing.T) {
	t.Parallel()
	path := filepath.Join(t.TempDir(), "reader.expect.toml")
	repo := gitAnswers{"rev-parse --show-toplevel": filepath.Dir(path) + "\n", "rev-parse --verify --quiet origin/main^{commit}": "abc\n"}
	if _, known, err := kerntest.GitBase(gitAnswers{}, "origin/main")(path); known || err != nil {
		t.Errorf("a file no repository holds: known %v, %v", known, err)
	}
	if _, _, err := kerntest.GitBase(repo, "origin/gone")(path); err == nil || !strings.Contains(err.Error(), "KERN_EXPECT_BASE=origin/gone: no commit origin/gone") {
		t.Errorf("a revision that is none answered %v", err)
	}
	repo["ls-tree --name-only origin/main -- reader.expect.toml"] = ""
	if x, known, err := kerntest.GitBase(repo, "origin/main")(path); !known || err != nil || len(x.Expect) != 0 {
		t.Errorf("a file the revision lacks: %v, known %v, %v", x, known, err)
	}
	repo["ls-tree --name-only origin/main -- reader.expect.toml"] = "reader.expect.toml\n"
	repo["show origin/main:./reader.expect.toml"] = readerExpects
	if x, known, err := kerntest.GitBase(repo, "origin/main")(path); !known || err != nil || len(x.Expect) != 5 || x.File != path {
		t.Errorf("a file the revision holds: %v, known %v, %v", x, known, err)
	}
}

func TestAHotRunRecordsNothingOfAPendingExpectationAndFailsNot(t *testing.T) {
	t.Parallel()
	reader, count, _, base := newWish(t)
	pool, err := kerntest.ReadPoolSince(base, reader.File)
	if err != nil {
		t.Fatal(err)
	}
	hot := kerntest.Hot{Name: "TestHot", Module: count}
	recs, err := hot.Meet(pool)
	if err != nil {
		t.Fatalf("a pending expectation failed the hot run: %v", err)
	}
	if len(recs) != 2 {
		t.Errorf("the hot run recorded %+v, want the two landed expectations of numbers", recs)
	}
	for _, r := range recs {
		if r.Expect == 6 {
			t.Errorf("the hot run recorded the pending expectation: %+v", r)
		}
	}
	// Landed, the same expectation fails the hot run.
	pool, err = kerntest.ReadPoolSince(landedAs(reader.File, readerExpects+fourNumbers), reader.File)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := hot.Meet(pool); err == nil || !strings.Contains(err.Error(), `unmet: name "numbers": counter does not meet expect 6`) {
		t.Errorf("a landed expectation the hot run does not meet: %v", err)
	}
}
