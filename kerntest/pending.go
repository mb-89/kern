package kerntest

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"

	"kern/git"
)

// A new expectation is pending until its claimer first meets it (README.md
// "Pending expectations"). Nothing stores that state: the reader's check
// refuses to land a change whose expectation its claimer does not meet, so
// every expectation the integration branch holds is one its claimer met when
// it landed. An expectation is new where the reader's file as the base holds
// none with the same terms, and pending where it is new and its claimer's
// test does not meet it now.

// BaseVariable names the base a process reads its pools against: a git
// revision, such as origin/main. Unset, there is no base, and every
// expectation counts as landed.
const BaseVariable = "KERN_EXPECT_BASE"

// ReportVariable names a file each pending expectation a claimer's test or a
// check finds is appended to, one JSON line each, which `go run
// ./tools/expect pending` reads.
const ReportVariable = "KERN_PENDING"

// Base answers an expectation file as the base holds it: its expectations,
// and known false where the base cannot know the file (one outside the
// repository), whose expectations then all count as landed. A file the base
// lacks is known and holds none, so each of its expectations is new.
type Base func(path string) (landed Expectations, known bool, err error)

// GitBase is the base the revision ref holds, read through b: the file path
// names, as ref holds it.
func GitBase(b git.Backend, ref string) Base {
	return func(path string) (Expectations, bool, error) {
		abs, err := filepath.Abs(path)
		if err != nil {
			return Expectations{}, false, err
		}
		dir, name := filepath.Dir(abs), filepath.Base(abs)
		ctx := context.Background()
		run := func(args ...string) (string, error) { return b.Run(ctx, git.Command{Dir: dir, Args: args}) }
		if _, err := run("rev-parse", "--show-toplevel"); err != nil {
			return Expectations{}, false, nil // no repository holds the file
		}
		if _, err := run("rev-parse", "--verify", "--quiet", ref+"^{commit}"); err != nil {
			return Expectations{}, false, fmt.Errorf("%s=%s: no commit %s: %w", BaseVariable, ref, ref, err)
		}
		held, err := run("ls-tree", "--name-only", ref, "--", name)
		if err != nil {
			return Expectations{}, false, err
		}
		if strings.TrimSpace(held) == "" {
			return Expectations{}, true, nil
		}
		text, err := run("show", ref+":./"+name)
		if err != nil {
			return Expectations{}, false, err
		}
		x, err := ParseExpectations(path, []byte(text))
		if err != nil {
			return Expectations{}, false, fmt.Errorf("as %s holds it: %w", ref, err)
		}
		return x, true, nil
	}
}

// environmentBase is the base BaseVariable names, read with real git: the
// variable is the explicit choice the git door asks of a test binary, and the
// read only shows files. Unset, it is nil.
func environmentBase() Base {
	ref := os.Getenv(BaseVariable)
	if ref == "" {
		return nil
	}
	return GitBase(git.Real{}, ref)
}

// since marks each expectation of x the base does not hold as new.
func since(base Base, x Expectations) (Expectations, error) {
	if base == nil || len(x.Expect) == 0 {
		return x, nil
	}
	landed, known, err := base(x.File)
	if err != nil || !known {
		return x, err
	}
	for i := range x.Expect {
		x.Expect[i].New = !holds(landed, x.Expect[i])
	}
	return x, nil
}

// holds reports whether landed holds an expectation with e's terms, wherever
// it stands in the file.
func holds(landed Expectations, e Expectation) bool {
	for _, l := range landed.Expect {
		if terms(l) == terms(e) {
			return true
		}
	}
	return false
}

func terms(e Expectation) string {
	b, _ := json.Marshal(struct {
		Name, Port, Request, Given string
		Value, Ask, Answer         any
		Refused                    bool
		Exact                      []string
	}{e.Name, e.Port, e.Request, e.Given, e.Value, e.Ask, e.Answer, e.Refused, e.Exact})
	return string(b)
}

// Pending is a new expectation its claimer does not meet yet: it blocks its
// reader's check, and its claimer's test reports it without failing.
type Pending struct {
	Expectation Expectation
	// Claimer is the component that claims its name and must meet it.
	Claimer string
	// Why is what the claimer's test answered.
	Why error
}

func (p Pending) String() string {
	return fmt.Sprintf("pending: name %q: %s of %s waits on %s, which does not meet it yet: %v",
		p.Expectation.Name, p.Expectation, p.Expectation.File, p.Claimer, p.Why)
}

func (p Pending) Error() string { return p.String() }

// Report is one pending expectation as ReportVariable's file holds it.
type Report struct {
	Claimer string `json:"claimer"`
	Name    string `json:"name"`
	File    string `json:"file"`
	N       int    `json:"n"`
	Line    string `json:"line"`
}

var reportMu sync.Mutex

// report appends p to ReportVariable's file, where it is set.
func report(p Pending) error {
	path := os.Getenv(ReportVariable)
	if path == "" {
		return nil
	}
	file, err := filepath.Abs(p.Expectation.File)
	if err != nil {
		return err
	}
	line, err := json.Marshal(Report{Claimer: p.Claimer, Name: p.Expectation.Name, File: file, N: p.Expectation.N, Line: p.String()})
	if err != nil {
		return err
	}
	reportMu.Lock()
	defer reportMu.Unlock()
	f, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		return fmt.Errorf("%s: %w", ReportVariable, err)
	}
	_, err = f.Write(append(line, '\n'))
	return errors.Join(err, f.Close())
}
