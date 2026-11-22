// Command expect lists the pending expectations per claimer (kern/README.md
// "Pending expectations"), so an integrator turns them into requests for the
// module that must meet them.
//
//	go run ./tools/expect pending [--base REV] [--dir DIR] [-- COMMAND...]
//
// It runs the tests against the base REV (default origin/main) with
// KERN_EXPECT_BASE set to it and KERN_PENDING naming a file of its own, each
// claimer's test and each check appending the pending expectations it finds,
// then lists them by claimer, each once. The tests are COMMAND, or by default
// `go test -count=1 ./...` and, where DIR holds a pyproject.toml, `uv run
// pytest -q`. A reader's check refuses its pending expectation, so a failing
// test is said and does not stop the listing. Run from the repository's root.
// Exit codes: 0 listed, 1 the tests could not run or report, 2 a usage error.
package main

import (
	"bufio"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"

	"kern/kerntest"
)

// Runner runs one test command in dir with env laid over the environment; a
// command that ran and failed answers an error with its ExitCode.
type Runner func(dir string, env, argv []string, stdout, stderr io.Writer) error

func main() { os.Exit(run(os.Args[1:], os.Stdout, os.Stderr, execute)) }

func execute(dir string, env, argv []string, stdout, stderr io.Writer) error {
	cmd := exec.Command(argv[0], argv[1:]...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), env...)
	cmd.Stdout, cmd.Stderr = stdout, stderr
	return cmd.Run()
}

func run(args []string, stdout, stderr io.Writer, runner Runner) int {
	if len(args) == 0 || args[0] != "pending" {
		fmt.Fprintln(stderr, "usage: go run ./tools/expect pending [--base REV] [--dir DIR] [-- COMMAND...]")
		return 2
	}
	fs := flag.NewFlagSet("pending", flag.ContinueOnError)
	fs.SetOutput(stderr)
	base := fs.String("base", "origin/main", "the git revision the expectations landed at")
	dir := fs.String("dir", ".", "the repository's root")
	if err := fs.Parse(args[1:]); err != nil {
		return 2
	}
	commands := [][]string{{"go", "test", "-count=1", "./..."}}
	if _, err := os.Stat(filepath.Join(*dir, "pyproject.toml")); err == nil {
		commands = append(commands, []string{"uv", "run", "pytest", "-q"})
	}
	if fs.NArg() > 0 {
		commands = [][]string{fs.Args()}
	}
	tmp, err := os.MkdirTemp("", "kern-pending-")
	if err != nil {
		fmt.Fprintln(stderr, "expect:", err)
		return 1
	}
	defer os.RemoveAll(tmp)
	report := filepath.Join(tmp, "pending.jsonl")
	env := []string{kerntest.BaseVariable + "=" + *base, kerntest.ReportVariable + "=" + report}
	for _, argv := range commands {
		// The tests' own output is theirs; the listing is this command's.
		err := runner(*dir, env, argv, io.Discard, io.Discard)
		var exit interface{ ExitCode() int }
		switch {
		case errors.As(err, &exit):
			fmt.Fprintf(stderr, "expect: %s failed (exit %d); a reader's check refuses its pending expectation, so the listing goes on\n", strings.Join(argv, " "), exit.ExitCode())
		case err != nil:
			fmt.Fprintf(stderr, "expect: %s: %v\n", strings.Join(argv, " "), err)
			return 1
		}
	}
	reports, err := read(report)
	if err != nil {
		fmt.Fprintln(stderr, "expect:", err)
		return 1
	}
	list(stdout, *base, *dir, reports)
	return 0
}

// read reads the report file, each pending expectation once: a claimer's test
// and the check that loads it report the same one.
func read(path string) ([]kerntest.Report, error) {
	f, err := os.Open(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	defer f.Close()
	seen := map[string]bool{}
	var out []kerntest.Report
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 0, 64*1024), 1<<20)
	for sc.Scan() {
		var r kerntest.Report
		if err := json.Unmarshal(sc.Bytes(), &r); err != nil {
			return nil, fmt.Errorf("%s: %w", path, err)
		}
		k := fmt.Sprintf("%s\x00%s\x00%d", r.Claimer, r.File, r.N)
		if !seen[k] {
			seen[k] = true
			out = append(out, r)
		}
	}
	return out, sc.Err()
}

// list prints the pending expectations of the readers dir holds by claimer,
// each with its reader's file from dir. A reader outside it is a test's own
// (kerntest's tests pool files they write to a temporary folder): the base
// cannot know such a file, so the tests' pools never find it pending.
func list(w io.Writer, base, dir string, all []kerntest.Report) {
	root, _ := filepath.Abs(dir)
	var reports []kerntest.Report
	for _, r := range all {
		if rel, err := filepath.Rel(root, r.File); err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
			r.File = filepath.ToSlash(rel)
			reports = append(reports, r)
		}
	}
	if len(reports) == 0 {
		fmt.Fprintf(w, "no expectation is pending against %s\n", base)
		return
	}
	sort.Slice(reports, func(i, j int) bool {
		a, b := reports[i], reports[j]
		if a.Claimer != b.Claimer {
			return a.Claimer < b.Claimer
		}
		if a.File != b.File {
			return a.File < b.File
		}
		return a.N < b.N
	})
	claimer := ""
	for _, r := range reports {
		if r.Claimer != claimer {
			claimer = r.Claimer
			n := 0
			for _, o := range reports {
				if o.Claimer == claimer {
					n++
				}
			}
			fmt.Fprintf(w, "%s must meet %d pending expectation(s) against %s:\n", claimer, n, base)
		}
		fmt.Fprintf(w, "  %s: %s\n", r.File, r.Line)
	}
}
