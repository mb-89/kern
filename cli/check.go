package cli

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"runtime/debug"
	"strings"
	"syscall"
	"time"

	"kern/host"
	"kern/lint"
	"kern/lsp"
	"kern/mcp"
)

// LintWait is how long `lint` waits for the core it starts or attaches to.
const LintWait = 10 * time.Second

func lintFlags(fs *flag.FlagSet) {
	coreFlags(fs)
	fs.Duration("wait", LintWait, "how long to wait for the core to run, a start included")
	fs.Bool("json", false, "print the answer as JSON: {\"findings\", \"checked\"}")
}

// runLint starts the core or attaches to it, asks the lsp component's
// command for every finding over the warm model, and prints each as
// path:line:col: rule: message; exit 1 where one is an error, never for a
// notice alone.
func runLint(c *Call) int {
	root, ok := rooted(c)
	if !ok {
		return 2
	}
	bin, err := os.Executable()
	if err != nil {
		fmt.Fprintf(c.Stderr, "%s lint: no path to this binary: %v\n", c.Name, err)
		return 2
	}
	if _, err := host.Start(host.Wall{}, root, bin, serveArgs(c.String("home"), c.Duration("grace"), c.Duration("idle"))...); err != nil {
		fmt.Fprintf(c.Stderr, "%s lint: %v\n", c.Name, err)
		return 2
	}
	st, err := host.WaitRunning(host.Wall{}, root, c.Duration("wait"))
	switch {
	case err != nil:
		fmt.Fprintf(c.Stderr, "%s lint: %v\n", c.Name, err)
		return 2
	case st.State != host.StateRunning:
		why := fmt.Sprintf("the core is %s after %s", st.State, c.Duration("wait"))
		if st.Died != "" {
			why = "the core ended at its start; it said:\n" + st.Died
		}
		fmt.Fprintf(c.Stderr, "%s lint: %s\n", c.Name, why)
		return 2
	}
	ctx, cancel := host.Within(context.Background(), host.Wall{}, lsp.CallWait)
	defer cancel()
	a, err := mcp.Host{Root: root}.Call(ctx, lsp.Command, nil)
	if err == nil && a.Error != "" {
		err = fmt.Errorf("%s", a.Error)
	}
	var got struct {
		Findings []lint.Finding `json:"findings"`
		Checked  int            `json:"checked"`
	}
	if err == nil {
		err = json.Unmarshal([]byte(a.Text), &got)
	}
	if err != nil {
		fmt.Fprintf(c.Stderr, "%s lint: %s: %v; the checker runs where the wiring places a files and an lsp instance (README.md \"Language server\")\n", c.Name, lsp.Command, err)
		return 2
	}
	if c.Bool("json") {
		fmt.Fprintln(c.Stdout, strings.TrimSpace(a.Text))
	} else {
		for _, f := range got.Findings {
			fmt.Fprintln(c.Stdout, f)
		}
		errs := len(lint.Errors(got.Findings))
		notices := ""
		if n := len(got.Findings) - errs; n > 0 {
			notices = fmt.Sprintf(" and %d notices", n)
		}
		fmt.Fprintf(c.Stderr, "%s lint: %d findings%s in %d managed files\n", c.Name, errs, notices, got.Checked)
	}
	if len(lint.Errors(got.Findings)) > 0 {
		return 1
	}
	return 0
}

// runLSP serves the language server over stdio until the editor says exit
// or hangs up: it starts the core with the grace and idle given, or attaches
// to it, beside the protocol, and holds a session lease while it serves.
func runLSP(c *Call) int {
	root, err := rootOf(c.String("root"))
	if err != nil {
		fmt.Fprintf(c.Stderr, "%s lsp: %v\n", c.Name, err)
		return 2
	}
	home := c.String("home")
	version := Version
	if info, ok := debug.ReadBuildInfo(); ok && version == "" {
		version = info.Main.Version
	}
	o := lsp.Options{App: c.Name, Version: version, Root: root, Log: c.Stderr, Findings: findingsName(root, home)}
	bin, err := os.Executable()
	if err != nil {
		o.NoStart = fmt.Sprintf("no path to this binary: %v", err)
	}
	o.Core = lsp.Host{Host: mcp.Host{Root: root, Bin: bin, Args: serveArgs(home, c.Duration("grace"), c.Duration("idle"))}}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	if err := lsp.Serve(ctx, o, c.Stdin, c.Stdout); err != nil {
		fmt.Fprintf(c.Stderr, "%s lsp: %v\n", c.Name, err)
		return 2
	}
	return 0
}

// findingsName is the name the wiring served at root binds an lsp
// instance's findings to, "" where it binds none.
func findingsName(root, home string) string {
	w, err := Wiring(Source(root, home))
	if err != nil {
		return ""
	}
	for _, in := range w.Instances {
		if in.Component != "lsp" || !in.Enabled {
			continue
		}
		for _, wr := range w.Wires {
			if wr.Port == in.Name+".findings" {
				return wr.To
			}
		}
	}
	return ""
}
