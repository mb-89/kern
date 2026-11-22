package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"kern/kerntest"
)

// exited is a test command that ran and failed.
type exited int

func (e exited) Error() string { return fmt.Sprintf("exit status %d", int(e)) }
func (e exited) ExitCode() int { return int(e) }

// reporting is the tests as the command runs them: each appends the reports
// it is given to the file KERN_PENDING names, and fails where fail is set.
func reporting(t *testing.T, fail bool, reports ...kerntest.Report) (Runner, *[][]string) {
	var ran [][]string
	return func(dir string, env, argv []string, _, _ io.Writer) error {
		ran = append(ran, append([]string{}, argv...))
		vars := map[string]string{}
		for _, e := range env {
			k, v, _ := strings.Cut(e, "=")
			vars[k] = v
		}
		if vars[kerntest.BaseVariable] == "" {
			t.Errorf("%v ran with no base: %v", argv, env)
		}
		f, err := os.OpenFile(vars[kerntest.ReportVariable], os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
		if err != nil {
			return err
		}
		defer f.Close()
		for _, r := range reports {
			b, _ := json.Marshal(r)
			f.Write(append(b, '\n'))
		}
		if fail {
			return exited(1)
		}
		return nil
	}, &ran
}

func TestItListsThePendingExpectationsPerClaimerEachOnce(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	tally := filepath.Join(dir, "index", "fixture", "tally.expect.toml")
	clerk := filepath.Join(dir, "index", "fixture", "clerk.expect.toml")
	four := kerntest.Report{Claimer: "counter", Name: "numbers", File: tally, N: 3, Line: `pending: name "numbers": expect 3 (port numbers) of tally.expect.toml waits on counter, which does not meet it yet: why`}
	post := kerntest.Report{Claimer: "ledger", Name: "lines.append", File: clerk, N: 4, Line: `pending: name "lines.append": expect 4 (request post, ask "x") of clerk.expect.toml waits on ledger, which does not meet it yet: why`}
	// counter's test and the reader's check both report four; the check
	// fails. A test's own reader, outside the repository, is no request.
	own := kerntest.Report{Claimer: "counter", Name: "numbers", File: filepath.Join(t.TempDir(), "reader.expect.toml"), N: 6, Line: "pending: a test's own"}
	runner, ran := reporting(t, true, post, four, own, four)
	var out, errs bytes.Buffer
	if code := run([]string{"pending", "--base", "origin/main", "--dir", dir}, &out, &errs, runner); code != 0 {
		t.Fatalf("exit %d: %s", code, errs.String())
	}
	want := "counter must meet 1 pending expectation(s) against origin/main:\n" +
		"  index/fixture/tally.expect.toml: " + four.Line + "\n" +
		"ledger must meet 1 pending expectation(s) against origin/main:\n" +
		"  index/fixture/clerk.expect.toml: " + post.Line + "\n"
	if out.String() != want {
		t.Errorf("it listed\n%s\nwant\n%s", out.String(), want)
	}
	if !strings.Contains(errs.String(), "go test -count=1 ./... failed (exit 1)") {
		t.Errorf("it said %q of the failing tests", errs.String())
	}
	if len(*ran) != 1 {
		t.Errorf("it ran %v; a folder with no pyproject.toml runs the Go tests alone", *ran)
	}
}

func TestItRunsThePythonTestsBesideAndTheCommandGiven(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "pyproject.toml"), nil, 0o644); err != nil {
		t.Fatal(err)
	}
	runner, ran := reporting(t, false)
	var out, errs bytes.Buffer
	if code := run([]string{"pending", "--dir", dir}, &out, &errs, runner); code != 0 || out.String() != "no expectation is pending against origin/main\n" {
		t.Fatalf("exit %d: %q %q", code, out.String(), errs.String())
	}
	if got := len(*ran); got != 2 || strings.Join((*ran)[1], " ") != "uv run pytest -q" {
		t.Errorf("it ran %v", *ran)
	}
	runner, ran = reporting(t, false)
	if code := run([]string{"pending", "--dir", dir, "--", "go", "test", "./kerntest/"}, &out, &errs, runner); code != 0 || len(*ran) != 1 || strings.Join((*ran)[0], " ") != "go test ./kerntest/" {
		t.Errorf("exit %d, ran %v", code, *ran)
	}
	if code := run([]string{"list"}, &out, &errs, runner); code != 2 {
		t.Errorf("an unknown subcommand exits %d", code)
	}
}
