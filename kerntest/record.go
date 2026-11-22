package kerntest

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// RecordingAge is the age past which a reader's fake says a recording is
// stale (README.md "Hot tests"): the hot test that wrote it has not run
// again against the real service since.
const RecordingAge = 30 * 24 * time.Hour

// Recording is one real answer an I/O module's hot test gave to one
// expectation of a reader: which expectation it met (its place, name, port or
// request, given and ask), which hot test met it and when, and the answer,
// trimmed to what the expectation states, so it holds the real values the
// reader relies on and nothing else. It stands beside the expectation, in the
// reader's recordings file (RecordingsFile).
type Recording struct {
	Expect  int       `json:"expect"`
	Name    string    `json:"name"`
	Port    string    `json:"port,omitempty"`
	Request string    `json:"request,omitempty"`
	Given   string    `json:"given,omitempty"`
	Ask     any       `json:"ask,omitempty"`
	Hot     string    `json:"hot"`
	At      time.Time `json:"at"`
	Value   any       `json:"value,omitempty"`
	Answer  any       `json:"answer,omitempty"`
	Refused bool      `json:"refused,omitempty"`
	// File is the reader's expectation file the recording is of.
	File string `json:"-"`
}

type recordingsFile struct {
	Recordings []Recording `json:"recordings"`
}

// RecordingsFile is where the recordings of the expectation file path stand:
// beside it, its name with .toml replaced by .recorded.json.
func RecordingsFile(path string) string {
	return strings.TrimSuffix(path, ".toml") + ".recorded.json"
}

// readRecorded reads the recordings file of the expectation file path as it
// stands, unchecked; none where there is no such file.
func readRecorded(path string) ([]Recording, error) {
	file := RecordingsFile(path)
	data, err := os.ReadFile(file)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("recordings: %w", err)
	}
	var doc recordingsFile
	dec := json.NewDecoder(strings.NewReader(string(data)))
	dec.DisallowUnknownFields()
	dec.UseNumber()
	if err := dec.Decode(&doc); err != nil {
		return nil, fmt.Errorf("%s: %v", file, err)
	}
	for i := range doc.Recordings {
		doc.Recordings[i].File = path
	}
	return doc.Recordings, nil
}

// ReadRecordings reads the recordings beside x's file, by the place of the
// expectation each is of, and refuses each that no longer fits its
// expectation: one of an expectation the file no longer holds, of another
// name, port, request, given or ask than the expectation at its place, or
// whose value or answer no longer meets it. Each refusal names the
// recordings file, the expectation and the hot test to run again.
func ReadRecordings(x Expectations) (map[int]Recording, error) {
	if x.File == "" {
		return map[int]Recording{}, nil
	}
	recs, err := readRecorded(x.File)
	if err != nil {
		return nil, err
	}
	file := RecordingsFile(x.File)
	byN := map[int]Expectation{}
	for _, e := range x.Expect {
		byN[e.N] = e
	}
	out := map[int]Recording{}
	var errs []string
	for _, r := range recs {
		if _, dup := out[r.Expect]; dup {
			errs = append(errs, fmt.Sprintf("expect %d is recorded twice", r.Expect))
			continue
		}
		e, ok := byN[r.Expect]
		if !ok {
			errs = append(errs, fmt.Sprintf("the recording of expect %d by hot test %s is of an expectation %s no longer holds: run hot test %s again", r.Expect, r.Hot, x.File, r.Hot))
			continue
		}
		if err := r.fits(e); err != nil {
			errs = append(errs, fmt.Sprintf("the recording of %s by hot test %s no longer fits it: %v: run hot test %s again", e, r.Hot, err, r.Hot))
			continue
		}
		out[r.Expect] = r
	}
	if len(errs) > 0 {
		return nil, fmt.Errorf("misfit: %s: %s", file, strings.Join(errs, "; "))
	}
	return out, nil
}

// fits says why r is no recording of e, or nil where it is.
func (r Recording) fits(e Expectation) error {
	switch {
	case r.Hot == "":
		return errors.New("it names no hot test")
	case r.At.IsZero():
		return errors.New("it names no time")
	case r.Name != e.Name:
		return fmt.Errorf("it is of name %q, and the expectation of %q", r.Name, e.Name)
	case r.Port != e.Port || r.Request != e.Request:
		return errors.New("it is of another port or request")
	case r.Given != e.Given:
		return fmt.Errorf("it is given %q, and the expectation %q", r.Given, e.Given)
	case r.Refused != e.Refused:
		return errors.New("it is refused where the expectation is answered, or answered where it is refused")
	}
	if e.Request != "" && !sameJSON(e.Ask, r.Ask) {
		return fmt.Errorf("it asks %s, and the expectation %s", show(r.Ask), show(e.Ask))
	}
	switch {
	case e.Port != "":
		return Match("value", e.Value, r.Value, e.Exact)
	case !e.Refused:
		return Match("answer", e.Answer, r.Answer, e.Exact)
	}
	return nil
}

// sameJSON says whether a and b read as the same JSON.
func sameJSON(a, b any) bool {
	ga, err := generic(plain(a))
	if err != nil {
		return false
	}
	gb, err := generic(b)
	if err != nil {
		return false
	}
	return show(ga) == show(gb)
}

// Option sets how Expecting reads the recordings beside an expectation file.
type Option func(*options)

type options struct {
	now    func() time.Time
	maxAge time.Duration
}

// Now is the clock a recording's age is read against: time.Now where unset.
func Now(now func() time.Time) Option { return func(o *options) { o.now = now } }

// MaxAge is the age past which a recording is stale: RecordingAge where unset.
func MaxAge(d time.Duration) Option { return func(o *options) { o.maxAge = d } }

// Stale answers, for each recording that answers this fake and is older than
// its set age, a line naming the expectation, its hot test and its age: the
// fake still answers from it, and says so here.
func (f *Fake) Stale() []string {
	return append([]string{}, f.stale...)
}

// StaleRecordings answers a line for each recording of recs, the recordings
// of x, that is older than maxAge at now, naming the expectation, its hot
// test, when it was recorded and its age, as Fake.Stale does.
func StaleRecordings(x Expectations, recs map[int]Recording, now time.Time, maxAge time.Duration) []string {
	return staleOf(x, recs, now, maxAge)
}

func staleOf(x Expectations, recs map[int]Recording, now time.Time, maxAge time.Duration) []string {
	var lines []string
	for _, e := range x.Expect {
		r, ok := recs[e.N]
		if !ok {
			continue
		}
		if age := now.Sub(r.At); age > maxAge {
			lines = append(lines, fmt.Sprintf("stale: %s of %s is answered from hot test %s's recording of %s, %s old, over %s: run hot test %s again",
				e, x.File, r.Hot, r.At.UTC().Format(time.RFC3339), days(age), days(maxAge), r.Hot))
		}
	}
	return lines
}

// days writes d in whole days, or "under a day".
func days(d time.Duration) string {
	if d < 24*time.Hour {
		return "under a day"
	}
	return fmt.Sprintf("%d days", int(d/(24*time.Hour)))
}

// Hot is an I/O module's hot test (README.md "Hot tests"): the module as it
// reaches the real service, met against every pooled expectation on the
// names it claims, each answer recorded beside the expectation it met.
type Hot struct {
	// Name is the hot test's name, which each recording names.
	Name string
	// Module is the I/O module as it reaches the real service: its
	// component, its expectation file with its claims, its Start against the
	// real service, and the setup of each given there.
	Module Module
	// Ask makes a reader's ask the one the service is asked, and Answer
	// makes the service's answer, as JSON reads it, the one the reader is
	// answered: where a reader names a placeholder, such as an owner, that
	// the service knows by another name. Each is left as it is where nil.
	Ask, Answer func(any) any
	// Now is the clock each recording's time comes from: time.Now where nil.
	Now func() time.Time
}

// reach is how a claimer's asks and answers are made the service's and the
// reader's: as Hot sets them, or left as they are.
type reach struct{ ask, answer func(any) any }

// Meet holds the module to every expectation in pool on the names it claims,
// as Module.Meet does, through Ask and Answer, and answers the
// recording of each one it met. It answers each one it does not meet, except
// a pending one, which it records nothing of and appends to ReportVariable's
// file, where it is set.
func (h Hot) Meet(pool Pool) ([]Recording, error) {
	x, err := h.Module.read()
	if err != nil {
		return nil, err
	}
	now := h.Now
	if now == nil {
		now = time.Now
	}
	r := reach{ask: h.Ask, answer: h.Answer}
	var recs []Recording
	var errs []error
	for _, cl := range x.Claims {
		for _, e := range pool[cl.Name] {
			got, err := h.Module.answer(cl, e, r)
			switch {
			case err != nil && e.New:
				// Pending (README.md "Pending expectations"): it fails no hot
				// run and is recorded once the module meets it.
				errs = append(errs, report(Pending{Expectation: e, Claimer: h.Module.Component.Name(), Why: err}))
				continue
			case err != nil:
				errs = append(errs, unmet(h.Module, e, err))
				continue
			}
			rec := Recording{Expect: e.N, Name: e.Name, Port: e.Port, Request: e.Request, Given: e.Given,
				Hot: h.Name, At: now().UTC().Truncate(time.Second), Refused: e.Refused, File: e.File}
			if e.Request != "" {
				rec.Ask = plain(e.Ask)
			}
			switch {
			case e.Port != "":
				rec.Value = trim("value", e.Value, got, e.Exact)
			case !e.Refused:
				rec.Answer = trim("answer", e.Answer, got, e.Exact)
			}
			recs = append(recs, rec)
		}
	}
	return recs, errors.Join(errs...)
}

// Record runs Meet and writes each recording it answers beside the
// expectation it met, in that reader's recordings file, and answers the
// files it wrote. A file keeps its earlier recordings of the expectations
// this run did not meet while they still fit, so a failed run leaves them to
// age; it drops those that no longer fit. The error is Meet's.
func (h Hot) Record(pool Pool) ([]string, error) {
	recs, meetErr := h.Meet(pool)
	byFile := map[string][]Recording{}
	for _, r := range recs {
		byFile[r.File] = append(byFile[r.File], r)
	}
	var written []string
	for _, file := range sortedKeys(byFile) {
		if err := writeRecordings(file, byFile[file]); err != nil {
			return written, errors.Join(meetErr, err)
		}
		written = append(written, RecordingsFile(file))
	}
	return written, meetErr
}

// writeRecordings writes recs into the recordings file of the expectation
// file path, beside the earlier recordings of other expectations that still fit.
func writeRecordings(path string, recs []Recording) error {
	x, err := ReadExpectations(path)
	if err != nil {
		return err
	}
	byN := map[int]Expectation{}
	for _, e := range x.Expect {
		byN[e.N] = e
	}
	earlier, err := readRecorded(path)
	if err != nil {
		return err
	}
	keep := map[int]Recording{}
	for _, r := range earlier {
		if e, ok := byN[r.Expect]; ok && r.fits(e) == nil {
			keep[r.Expect] = r
		}
	}
	for _, r := range recs {
		keep[r.Expect] = r
	}
	all := make([]Recording, 0, len(keep))
	for _, r := range keep {
		all = append(all, r)
	}
	sort.Slice(all, func(i, j int) bool { return all[i].Expect < all[j].Expect })
	data, err := json.MarshalIndent(recordingsFile{Recordings: all}, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(RecordingsFile(path), append(data, '\n'), 0o644)
}

// trim answers got, as JSON reads it, cut down to what want states: a
// table's keys want names, each list item cut down to the item of want it
// matches. A table or list want holds nothing of is kept empty, a list's
// items as many as got holds, and every number, string and bool as got
// holds it. So a recording holds the real values its reader relies on, and
// nothing of the service the reader never reads.
func trim(path string, want, got any, exact []string) any {
	ex := map[string]bool{}
	for _, p := range exact {
		ex[p] = true
	}
	return trimAt(path, plain(want), got, ex)
}

func trimAt(path string, want, got any, exact map[string]bool) any {
	switch g := got.(type) {
	case map[string]any:
		w, _ := want.(map[string]any)
		out := map[string]any{}
		for k, wv := range w {
			if gv, ok := g[k]; ok {
				out[k] = trimAt(path+"."+k, wv, gv, exact)
			}
		}
		return out
	case []any:
		w, _ := want.([]any)
		out := make([]any, len(g))
		for i, item := range g {
			var like any
			for _, wi := range w {
				if match(path+"[]", wi, item, exact) == nil {
					like = wi
					break
				}
			}
			out[i] = trimAt(path+"[]", like, item, exact)
		}
		return out
	}
	return got
}

// ExpectationFiles answers every expectation file under root, sorted:
// expect.toml and <component>.expect.toml, outside the folders .git, .se,
// .venv, __pycache__ and testdata. A hot test pools them all.
func ExpectationFiles(root string) ([]string, error) {
	skip := map[string]bool{".git": true, ".se": true, ".venv": true, "__pycache__": true, "testdata": true}
	var files []string
	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			if path != root && skip[d.Name()] {
				return filepath.SkipDir
			}
			return nil
		}
		if n := d.Name(); n == "expect.toml" || strings.HasSuffix(n, ".expect.toml") {
			files = append(files, path)
		}
		return nil
	})
	sort.Strings(files)
	return files, err
}
