package host_test

import (
	"os"
	"path/filepath"
	"testing"

	"kern/host"
	"kern/index"
	"kern/waiting"
)

// whereArg makes this test binary a placed process that writes, into the
// folder it runs in, the checkout its environment names (host.RootEnv).
const whereArg = "kern-test-where"

func where() int {
	wd, err := os.Getwd()
	if err != nil {
		return 1
	}
	if err := os.WriteFile(filepath.Join(wd, "where.txt"), []byte(os.Getenv(host.RootEnv)), 0o600); err != nil {
		return 1
	}
	return 0
}

// A core whose wiring came from another folder (Options.Home, an
// application's own serving a project that keeps none) runs each placed
// process there, so its command's relative paths resolve, and names the
// checkout it serves in the process's environment; without a Home it runs in
// the checkout, which it names the same way.
func TestAPlacedProcessRunsWhereItsWiringCameFromAndKnowsTheCheckout(t *testing.T) {
	t.Parallel()
	self, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	for _, withHome := range []bool{true, false} {
		root, home := t.TempDir(), t.TempDir()
		o := host.Options{Root: root, Clock: newClock(), Wiring: tank([]string{self, "-test.run=^$", whereArg}),
			Components: []*index.Component{pump, gauge}}
		ran := root
		if withHome {
			o.Home, ran = home, home
		}
		served(t, o)
		file := filepath.Join(ran, "where.txt")
		var got []byte
		waiting.Until(t, "the placed process's "+file, func() bool {
			got, err = os.ReadFile(file)
			return err == nil && len(got) > 0
		})
		if string(got) != root {
			t.Fatalf("home %v: the placed process wrote %q into %s, and wants the checkout %s", withHome, got, ran, root)
		}
	}
}
