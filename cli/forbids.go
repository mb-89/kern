package cli

import (
	"fmt"
	"io"
	"os"
	"sort"
	"strings"

	"kern/forbid"
	"kern/index"
)

// runForbids scans the checkout's components against the forbid lists of the
// I/O components the binary carries and those declared under components/
// (package forbid): it says each use of another I/O module's entry on stderr
// and exits 1, or says what it held the code to and exits 0. It starts no
// core: the scan runs before a commit, in the landing's suite and in CI.
func runForbids(c *Call) int {
	dir, ok := rooted(c)
	if !ok {
		return 2
	}
	code, err := Forbids(dir, index.Registered(), c.Stdout, c.Stderr)
	if err != nil {
		fmt.Fprintf(c.Stderr, "%s forbids: %v\n", c.Name, err)
		return 2
	}
	return code
}

// Forbids scans the checkout at root against the forbid lists of compiled and
// of the components declared there, as `<name> forbids` does, and answers its
// exit code: 1 where a component uses an entry of another I/O module's list,
// each use a line on stderr, else 0 with one line on stdout.
func Forbids(root string, compiled []*index.Component, stdout, stderr io.Writer) (int, error) {
	owners, err := forbid.Load(root, compiled)
	if err != nil {
		return 2, err
	}
	found, err := forbid.Scan(os.DirFS(root), owners)
	if err != nil {
		return 2, err
	}
	var names []string
	for _, o := range owners {
		names = append(names, o.Module)
	}
	sort.Strings(names)
	if len(found) > 0 {
		fmt.Fprintf(stderr, "forbids: %d use(s) of what another I/O module forbids in %s/ (README.md \"Forbid lists\"):\n", len(found), forbid.Dir)
		for _, f := range found {
			fmt.Fprintln(stderr, "  "+f.String())
		}
		return 1, nil
	}
	lists := "no forbid list is loaded"
	if len(names) > 0 {
		lists = "the lists of " + strings.Join(names, ", ")
	}
	fmt.Fprintf(stdout, "forbids: no component in %s/ uses what another I/O module forbids (%s)\n", forbid.Dir, lists)
	return 0, nil
}
