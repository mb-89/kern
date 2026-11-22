package cli_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"kern/cli"
	"kern/index"
)

// `forbids` says each use of another I/O module's entry on stderr and exits 1,
// naming the file, the line, the component's folder and the owner; a checkout
// whose components use none says what it held them to and exits 0.
func TestForbidsRefusesAComponentThatReachesPastItsIOModule(t *testing.T) {
	t.Parallel()
	gh := index.Declare("gh-door", "Runs gh.", index.IO(), index.Forbids(index.Forbidden{Commands: []string{"gh"}}))
	var out, errs strings.Builder
	code, err := cli.Forbids(filepath.Join("..", "forbid", "testdata", "checkout"), []*index.Component{gh}, &out, &errs)
	if err != nil || code != 1 || out.Len() > 0 {
		t.Fatalf("forbids answers %d, %v with stdout %q, and wants 1 with none", code, err, out.String())
	}
	for _, want := range []string{
		"components/runner/runner.go:15: components/runner uses the command gh, which the I/O module gh-door forbids",
		"components/pyrunner/__init__.py:20: components/pyrunner uses the call time.monotonic, which the I/O module pyclock forbids",
	} {
		if !strings.Contains(errs.String(), want) {
			t.Errorf("forbids says\n%s\nand names no %q", errs.String(), want)
		}
	}
	clean := t.TempDir()
	if err := os.MkdirAll(filepath.Join(clean, "components", "plain"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(clean, "components", "plain", "plain.go"), []byte("package plain\n\nconst x = \"gh-less\"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	out.Reset()
	errs.Reset()
	code, err = cli.Forbids(clean, []*index.Component{gh}, &out, &errs)
	if err != nil || code != 0 || errs.Len() > 0 || !strings.Contains(out.String(), "the lists of gh-door") {
		t.Fatalf("a clean checkout answers %d, %v with %q and %q, and wants 0 naming the list it held them to", code, err, out.String(), errs.String())
	}
}
