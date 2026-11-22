package main

import (
	"path/filepath"
	"strings"
	"testing"

	"kern/cli"
	"kern/components/clock"
	"kern/components/github"
	"kern/index"
)

// The clock and the GitHub component declare their forbid lists, and kern's
// own components use no entry of another's: the host's clock (host.Wall) is
// the clock's only other reader of the system time. A change that breaks it
// fails go test ./..., and so the landing's suite and CI's check.
func TestKernsComponentsUseNothingAnotherIOModuleForbids(t *testing.T) {
	t.Parallel()
	for _, c := range []*index.Component{clock.Component.Component, github.Component.Component} {
		if c.Forbids().Empty() {
			t.Errorf("the I/O component %s declares no forbid list", c.Name())
		}
	}
	var out, errs strings.Builder
	code, err := cli.Forbids(filepath.Join("..", ".."), index.Registered(), &out, &errs)
	if err != nil || code != 0 {
		t.Fatalf("kern forbids answers %d, %v:\n%s", code, err, errs.String())
	}
	if !strings.Contains(out.String(), "the lists of clock, github") {
		t.Fatalf("kern forbids held kern's components to no list of clock and github: %s", out.String())
	}
}

// A fixture component that calls time.Now is refused, naming the clock as the
// owner, and one that runs gh is refused, naming GitHub, by the lists kern's
// binary carries.
func TestAComponentThatReadsTheTimeOrRunsGhIsRefused(t *testing.T) {
	t.Parallel()
	var out, errs strings.Builder
	code, err := cli.Forbids(filepath.Join("..", "..", "forbid", "testdata", "checkout"), index.Registered(), &out, &errs)
	if err != nil || code != 1 {
		t.Fatalf("kern forbids answers %d, %v, and wants 1", code, err)
	}
	for _, want := range []string{
		"components/poller/poller.go:13: components/poller uses the call time.Now, which the I/O module clock forbids: only clock may use it, so reach it through clock's names and verbs",
		"components/runner/runner.go:15: components/runner uses the command gh, which the I/O module github forbids: only github may use it, so reach it through github's names and verbs",
		"components/pyrunner/__init__.py:15: components/pyrunner uses the command gh, which the I/O module github forbids",
		"components/pyrunner/__init__.py:13: components/pyrunner uses the call time.sleep, which the I/O module clock forbids",
		// an entry two lists hold, the clock's and the declared pyclock's, names both
		"components/pyrunner/__init__.py:20: components/pyrunner uses the call time.monotonic, which the I/O modules clock and pyclock forbid: only clock and pyclock may use it, so reach it through clock's or pyclock's names and verbs",
	} {
		if !strings.Contains(errs.String(), want) {
			t.Errorf("kern forbids says\n%s\nand names no %q", errs.String(), want)
		}
	}
}
