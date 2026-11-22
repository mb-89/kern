package kerntest_test

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"kern/index"
	"kern/kerntest"
)

// The hot tests of README.md "Hot tests": an I/O module met against the real
// service records each answer beside the expectation it met, and a reader's
// fake answers from the recording, says when it is stale, and refuses one
// that no longer fits. The real service here is a component of the test's
// own, so no case waits on the world.

var recorded = time.Date(2026, 11, 22, 9, 0, 0, 0, time.UTC)

func at(t time.Time) func() time.Time { return func() time.Time { return t } }

// world is the lines service as it stands outside: four lines already, so
// its first answer is 5 where the reader states only some whole number.
func world(t *testing.T) (kerntest.Module, *[]string) {
	t.Helper()
	kept := []string{"a", "b", "c", "d"}
	return kerntest.Module{Component: lines(&kept), File: writeFile(t, "lines.expect.toml", linesClaims),
		Given: map[string]kerntest.Setup{"two lines stand": func(*kerntest.Fake) error { kept = []string{"one", "two"}; return nil }}}, &kept
}

func recordReader(t *testing.T) (path string, recs map[string]any) {
	t.Helper()
	path = writeFile(t, "reader.expect.toml", readerExpects)
	pool, err := kerntest.ReadPool(path)
	if err != nil {
		t.Fatal(err)
	}
	m, _ := world(t)
	written, err := kerntest.Hot{Name: "lines", Module: m, Now: at(recorded)}.Record(pool)
	if err != nil {
		t.Fatal(err)
	}
	want := filepath.Join(filepath.Dir(path), "reader.expect.recorded.json")
	if len(written) != 1 || filepath.ToSlash(written[0]) != filepath.ToSlash(want) {
		t.Fatalf("the hot test wrote %v, want %s", written, want)
	}
	data, err := os.ReadFile(want)
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(data, &recs); err != nil {
		t.Fatal(err)
	}
	return path, recs
}

func TestAHotTestRecordsEachRealAnswerBesideTheExpectationItMet(t *testing.T) {
	t.Parallel()
	_, recs := recordReader(t)
	got, _ := json.Marshal(recs["recordings"])
	want := `[` +
		`{"ask":"","at":"2026-11-22T09:00:00Z","expect":3,"hot":"lines","name":"lines.append","refused":true,"request":"post"},` +
		`{"answer":5,"ask":"a word","at":"2026-11-22T09:00:00Z","expect":4,"hot":"lines","name":"lines.append","request":"post"},` +
		`{"answer":3,"ask":"a word","at":"2026-11-22T09:00:00Z","expect":5,"given":"two lines stand","hot":"lines","name":"lines.append","request":"post"}]`
	if string(got) != want {
		t.Fatalf("the recordings are\n%s\nwant\n%s", got, want)
	}
}

func TestAReadersFakeAnswersFromTheRecordingAndSaysWhereItIsStale(t *testing.T) {
	t.Parallel()
	path, _ := recordReader(t)
	f, err := kerntest.Expecting(readerComponent(), path, kerntest.Now(at(recorded.Add(time.Hour))))
	if err != nil {
		t.Fatal(err)
	}
	// The file states some whole number; the world answered 5, and the fake answers it.
	if text, err := f.Call("file", "x"); err != nil || text != "filed as line 5" {
		t.Fatalf("file answered %q, %v", text, err)
	}
	if _, err := f.Call("file", ""); err == nil || !strings.Contains(err.Error(), "refused, as expect 3") {
		t.Fatalf("an empty line was answered: %v", err)
	}
	// numbers has no recording: its port reads what the file states.
	if err := f.Given("numbers stand"); err != nil {
		t.Fatal(err)
	}
	if err := f.Derive("sum"); err != nil {
		t.Fatal(err)
	}
	if got, _ := f.Out("sum"); got != (index.Value{V: 6, Provided: true}) {
		t.Fatalf("sum given numbers stand is %+v", got)
	}
	if s := f.Stale(); len(s) != 0 {
		t.Fatalf("an hour-old recording is stale: %v", s)
	}
	old, err := kerntest.Expecting(readerComponent(), path, kerntest.Now(at(recorded.Add(31*24*time.Hour))))
	if err != nil {
		t.Fatal(err)
	}
	stale := old.Stale()
	want := `stale: expect 4 (request post, ask "a word") of ` + path + ` is answered from hot test lines's recording of 2026-11-22T09:00:00Z, 31 days old, over 30 days: run hot test lines again`
	if len(stale) != 3 || stale[1] != want {
		t.Fatalf("a month-old recording reads stale as %q, want 3 lines, the second %q", stale, want)
	}
	if text, err := old.Call("file", "x"); err != nil || text != "filed as line 5" {
		t.Fatalf("a stale recording no longer answers: %q, %v", text, err)
	}
	lax, err := kerntest.Expecting(readerComponent(), path, kerntest.Now(at(recorded.Add(31*24*time.Hour))), kerntest.MaxAge(40*24*time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	if s := lax.Stale(); len(s) != 0 {
		t.Fatalf("under a set age of 40 days, a 31-day-old recording is stale: %v", s)
	}
}

func TestARecordingThatNoLongerFitsItsExpectationIsRefused(t *testing.T) {
	t.Parallel()
	rec := func(*testing.T) string { return "reader.expect.recorded.json" }
	for _, c := range []struct {
		name, from, to, refusal string
	}{
		{"an answer the expectation now holds exact", "ask = \"a word\"\nanswer = 1\n", "ask = \"a word\"\nanswer = 1\nexact = [\"answer\"]\n",
			`the recording of expect 4 (request post, ask "a word") by hot test lines no longer fits it: answer is 5, want 1: run hot test lines again`},
		{"another ask", "ask = \"a word\"\nanswer = 1\n", "ask = \"two words\"\nanswer = 1\n",
			`the recording of expect 4 (request post, ask "two words") by hot test lines no longer fits it: it asks "a word", and the expectation "two words"`},
		{"an expectation the file no longer holds", "\n[[expect]]\nname = \"lines.append\"\nrequest = \"post\"\ngiven = \"two lines stand\"\nask = \"a word\"\nanswer = 3\n", "",
			`the recording of expect 5 by hot test lines is of an expectation `},
	} {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			path, _ := recordReader(t)
			text := strings.Replace(readerExpects, c.from, c.to, 1)
			if text == readerExpects {
				t.Fatalf("the case changes nothing in the reader's file")
			}
			if err := os.WriteFile(path, []byte(text), 0o644); err != nil {
				t.Fatal(err)
			}
			_, err := kerntest.Expecting(readerComponent(), path)
			wantFile := filepath.Join(filepath.Dir(path), rec(t))
			if err == nil || !strings.Contains(err.Error(), "misfit: "+wantFile+": ") || !strings.Contains(err.Error(), c.refusal) {
				t.Fatalf("the fake took the recording: %v, want %q", err, c.refusal)
			}
			// The completeness check fails on it too.
			_, count, ledger := modules(t)
			reader := kerntest.Module{Component: readerComponent(), File: path}
			err = kerntest.Check(wiringOf(t, "reader", "counter", "lines"), reader, count, ledger)
			if err == nil || !strings.Contains(err.Error(), c.refusal) {
				t.Fatalf("the check passed a misfit recording: %v", err)
			}
		})
	}
}

// wiringOf is a wiring that loads one instance of each component named.
func wiringOf(t *testing.T, components ...string) index.Wiring {
	t.Helper()
	var text strings.Builder
	for _, c := range components {
		text.WriteString("[instances." + c + "]\ncomponent = \"" + c + "\"\n\n")
	}
	w, err := index.ParseWiring([]byte(text.String()))
	if err != nil {
		t.Fatal(err)
	}
	return w
}

func TestAFailedHotRunKeepsTheEarlierRecordingsToAge(t *testing.T) {
	t.Parallel()
	path, before := recordReader(t)
	pool, err := kerntest.ReadPool(path)
	if err != nil {
		t.Fatal(err)
	}
	m, _ := world(t)
	m.Given = nil // the world no longer sets up two lines: expect 5 goes unmet
	_, err = kerntest.Hot{Name: "lines", Module: m, Now: at(recorded.Add(24 * time.Hour))}.Record(pool)
	if err == nil || !strings.Contains(err.Error(), `unmet: name "lines.append": lines does not meet expect 5`) {
		t.Fatalf("the failed run answered %v", err)
	}
	var after map[string]any
	data, _ := os.ReadFile(kerntest.RecordingsFile(path))
	if err := json.Unmarshal(data, &after); err != nil {
		t.Fatal(err)
	}
	recs := after["recordings"].([]any)
	if len(recs) != 3 {
		t.Fatalf("the failed run left %d recordings", len(recs))
	}
	if got := recs[1].(map[string]any)["at"]; got != "2026-11-23T09:00:00Z" {
		t.Errorf("expect 4, met again, was recorded at %v", got)
	}
	if got, was := recs[2].(map[string]any)["at"], before["recordings"].([]any)[2].(map[string]any)["at"]; got != was {
		t.Errorf("expect 5, unmet, kept no earlier recording: at %v, was %v", got, was)
	}
}

// service is an I/O module that reaches a service once started: its inlet
// reads the config world and commits reached; its verb issue answers what
// the service holds of an owner, more than any reader reads.
func service() *index.Component {
	type started struct {
		sync.Mutex
		world string
	}
	s := &started{}
	reached := index.Output("reached", "", "The world it reached.")
	type Issue struct {
		Repo   string           `json:"repo"`
		Title  string           `json:"title"`
		Node   string           `json:"node"`
		Labels []map[string]any `json:"labels"`
	}
	return index.Declare("service", "Reaches a service.", index.IO(),
		index.Config("world", "", "The world it reaches."),
		reached,
		index.Inlet(func(_ string, read index.Reader, commit index.Commit) error {
			w, _ := read(index.ConfigPrefix + "world").(string)
			if w == "" {
				return errors.New("no world to reach")
			}
			s.Lock()
			s.world = w
			s.Unlock()
			return commit(reached.Set(w))
		}),
		index.Verb("issue", "Reads an owner's issue.", func(_ context.Context, owner string) (Issue, error) {
			s.Lock()
			defer s.Unlock()
			if s.world == "" {
				return Issue{}, errors.New("not started")
			}
			if owner != "alice" {
				return Issue{}, errors.New("no such owner: " + owner)
			}
			return Issue{Repo: owner + "/demo", Title: "In " + s.world, Node: "I_secret",
				Labels: []map[string]any{{"name": "work", "id": 7}, {"name": "ready", "id": 8}}}, nil
		}),
	)
}

func TestAHotTestReachesTheServiceAndRecordsOnlyWhatTheReaderStates(t *testing.T) {
	t.Parallel()
	path := writeFile(t, "watcher.expect.toml", `
[[expect]]
name = "issues.read"
request = "issue"
ask = "owner"
exact = ["ask", "answer.repo"]
answer = { repo = "owner/demo", title = "a title", labels = [{ name = "a label" }] }
`)
	pool, err := kerntest.ReadPool(path)
	if err != nil {
		t.Fatal(err)
	}
	m := kerntest.Module{Component: service(), File: writeFile(t, "service.expect.toml", "[[claim]]\nname = \"issues.read\"\nverb = \"issue\"\n")}
	swap := func(from, to string) func(any) any {
		var walk func(any) any
		walk = func(v any) any {
			switch x := v.(type) {
			case string:
				return strings.ReplaceAll(x, from, to)
			case map[string]any:
				out := map[string]any{}
				for k, item := range x {
					out[k] = walk(item)
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
	hot := kerntest.Hot{Name: "service", Module: m, Now: at(recorded),
		Ask: swap("owner", "alice"), Answer: swap("alice", "owner")}
	if _, err := hot.Meet(pool); err == nil || !strings.Contains(err.Error(), "reaching") && !strings.Contains(err.Error(), "not started") {
		t.Fatalf("a service never started met the expectation: %v", err)
	}
	hot.Reach = func(f *kerntest.Fake) error {
		if err := f.Config("world", "the world"); err != nil {
			return err
		}
		if err := f.Start("service"); err != nil {
			return err
		}
		if v, _ := f.Out("reached"); v.V != "the world" {
			return errors.New("the start committed nothing")
		}
		return nil
	}
	recs, err := hot.Meet(pool)
	if err != nil {
		t.Fatal(err)
	}
	got, _ := json.Marshal(recs[0].Answer)
	if want := `{"labels":[{"name":"work"},{"name":"ready"}],"repo":"owner/demo","title":"In the world"}`; string(got) != want {
		t.Fatalf("the recording holds %s, want %s", got, want)
	}
	if recs[0].Ask != "owner" || recs[0].Hot != "service" || !recs[0].At.Equal(recorded) {
		t.Fatalf("the recording names %+v", recs[0])
	}
}

func TestExpectationFilesFindsEveryExpectationFileOutsideTestdata(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	for _, f := range []string{"a/expect.toml", "b/two.expect.toml", "b/other.toml", "testdata/expect.toml", ".se/expect.toml"} {
		p := filepath.Join(root, filepath.FromSlash(f))
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, nil, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	files, err := kerntest.ExpectationFiles(root)
	if err != nil {
		t.Fatal(err)
	}
	var rel []string
	for _, f := range files {
		r, _ := filepath.Rel(root, f)
		rel = append(rel, filepath.ToSlash(r))
	}
	if strings.Join(rel, " ") != "a/expect.toml b/two.expect.toml" {
		t.Fatalf("found %v", rel)
	}
}
