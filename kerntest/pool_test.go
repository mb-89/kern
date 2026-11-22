package kerntest_test

import (
	"context"
	"errors"
	"strings"
	"testing"

	"kern/index"
	"kern/kerntest"
)

// counter writes numbers: 1 to its config upto.
func counter(name string) *index.Component {
	type in struct {
		Upto int `index:"config/upto"`
	}
	return index.Declare(name, "Counts up.",
		index.Config("upto", 0, "How far it counts."),
		index.Derived("numbers", []int{}, "1 to upto.", func(i in) ([]int, error) {
			out := []int{}
			for n := 1; n <= i.Upto; n++ {
				out = append(out, n)
			}
			return out, nil
		}),
	)
}

// lines answers append: it keeps each line, refuses an empty one, and
// answers how many lines stand.
func lines(kept *[]string) *index.Component {
	return index.Declare("lines", "Keeps lines.", index.IO(),
		index.Verb("append", "Appends a line.", func(_ context.Context, line string) (int, error) {
			if line == "" {
				return 0, errors.New("no empty line")
			}
			*kept = append(*kept, line)
			return len(*kept), nil
		}),
	)
}

const counterClaims = `
[[claim]]
name = "numbers"
port = "numbers"
`

const linesClaims = `
[[claim]]
name = "lines.append"
verb = "append"
`

func modules(t *testing.T) (reader, count, ledger kerntest.Module) {
	t.Helper()
	var kept []string
	reader = kerntest.Module{Component: readerComponent(), File: writeFile(t, "reader.expect.toml", readerExpects)}
	count = kerntest.Module{Component: counter("counter"), File: writeFile(t, "counter.expect.toml", counterClaims),
		Given: map[string]kerntest.Setup{"numbers stand": func(f *kerntest.Fake) error { return f.Config("upto", 3) }}}
	ledger = kerntest.Module{Component: lines(&kept), File: writeFile(t, "lines.expect.toml", linesClaims),
		Given: map[string]kerntest.Setup{"two lines stand": func(*kerntest.Fake) error { kept = []string{"one", "two"}; return nil }}}
	return reader, count, ledger
}

func TestAWritersTestMeetsThePooledExpectationsOfItsName(t *testing.T) {
	t.Parallel()
	reader, count, ledger := modules(t)
	pool, err := kerntest.ReadPool(reader.File)
	if err != nil {
		t.Fatal(err)
	}
	if len(pool["numbers"]) != 2 || len(pool["lines.append"]) != 3 {
		t.Fatalf("the pool holds %v", pool)
	}
	if _, err := count.Meet(pool); err != nil {
		t.Errorf("counter meets numbers: %v", err)
	}
	if _, err := ledger.Meet(pool); err != nil {
		t.Errorf("lines meets lines.append: %v", err)
	}
}

func TestAWriterThatDoesNotMeetAnExpectationFailsItsOwnTest(t *testing.T) {
	t.Parallel()
	reader, count, _ := modules(t)
	pool, err := kerntest.ReadPool(reader.File)
	if err != nil {
		t.Fatal(err)
	}
	count.Given = nil // it sets up no given, and counts nothing without one
	_, err = count.Meet(pool)
	for _, want := range []string{
		`unmet: name "numbers": counter does not meet expect 2 (port numbers, given "numbers stand") of ` + reader.File + `: it sets up no given "numbers stand"`,
	} {
		if err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("Meet answered %v, want %q", err, want)
		}
	}
	count.Given = map[string]kerntest.Setup{"numbers stand": func(f *kerntest.Fake) error { return nil }}
	if _, err := count.Meet(pool); err == nil || !strings.Contains(err.Error(), "value is an empty list, want at least one item") {
		t.Errorf("a counter that counts nothing met numbers stand: %v", err)
	}
}
