package files_test

import (
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"kern/components/files"
	"kern/host"
	"kern/index"
	"kern/kerntest"
)

// fed is a watcher the case feeds by hand: it decides what arrives and when,
// so no case waits for the disk to be noticed.
type fed struct {
	mu     sync.Mutex
	added  []string
	events chan string
	errors chan error
}

// newFed's channels are unbuffered: a send returns once the loop took it, and
// the loop hears in order, so a second send returns once the first was handled.
func newFed() *fed { return &fed{events: make(chan string), errors: make(chan error)} }

func (w *fed) Add(dir string) error {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.added = append(w.added, dir)
	return nil
}
func (w *fed) Events() <-chan string { return w.events }
func (w *fed) Errors() <-chan error  { return w.errors }
func (w *fed) Close() error          { return nil }

// clock is the cookie's wait; the case fires it.
type clock struct{ fire chan time.Time }

func (c clock) Now() time.Time                       { return time.Time{} }
func (c clock) After(time.Duration) <-chan time.Time { return c.fire }

// setup is one started instance over root with its managed folders, and
// what the case drives it with.
type setup struct {
	t     *testing.T
	root  string
	files *files.Files
	fake  *kerntest.Fake
	w     *fed
	clock clock
}

func started(t *testing.T, root string, config map[string]any) *setup {
	t.Helper()
	s := &setup{t: t, root: root, w: newFed(), clock: clock{make(chan time.Time)}}
	s.files = files.New(files.Options{Root: root, Clock: s.clock, Watch: func() (files.Watcher, error) { return s.w, nil }})
	fake, err := kerntest.New(s.files.Component)
	if err != nil {
		t.Fatal(err)
	}
	for k, v := range config {
		if err := fake.Config(k, v); err != nil {
			t.Fatal(err)
		}
	}
	if err := fake.Start("files"); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(s.files.Stop)
	s.fake = fake
	return s
}

// put writes a file on the disk by hand, past the module, as a person's editor does.
func (s *setup) put(rel, content string) string {
	s.t.Helper()
	abs := filepath.Join(s.root, filepath.FromSlash(rel))
	if err := os.MkdirAll(filepath.Dir(abs), 0o755); err != nil {
		s.t.Fatal(err)
	}
	if err := os.WriteFile(abs, []byte(content), 0o644); err != nil {
		s.t.Fatal(err)
	}
	return abs
}

// hear feeds the watcher one path, and returns once the loop handled it.
func (s *setup) hear(abs string) {
	s.t.Helper()
	s.w.events <- abs
	s.settle()
}

// settle returns once the loop handled everything fed before: it feeds a path
// outside every managed folder, which the loop takes only after the last
// thing fed was handled, and ignores.
func (s *setup) settle() {
	s.t.Helper()
	s.w.events <- filepath.Join(s.root, "outside-every-folder")
}

// watched hears every managed folder's cookie, so the model is watched.
func (s *setup) watched(folders ...string) {
	s.t.Helper()
	for _, folder := range folders {
		s.hear(filepath.Join(s.root, filepath.FromSlash(folder), files.Own+"cookie"))
	}
	if v, _ := s.fake.Out("watching"); v.V != true {
		s.t.Fatalf("after every cookie was heard, watching is %v", v.V)
	}
}

// model answers the model's name for rel, and whether it stands.
func (s *setup) model(rel string) (files.File, bool) {
	s.t.Helper()
	v, err := s.fake.OutAt("model", files.Key(rel))
	if err != nil {
		s.t.Fatal(err)
	}
	file, _ := v.V.(files.File)
	return file, v.Provided
}

func serve[T any](s *setup, verb string, req any) (T, error) {
	s.t.Helper()
	a, err := s.fake.BeginServe(verb, req).Wait()
	v, _ := a.(T)
	return v, err
}

// The model holds every managed file as names at the start: path, hash and,
// below the limit, content; a file outside the managed folders, git's own
// folder and a binary file's content are not held.
func TestTheModelHoldsTheManagedFolders(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	s0 := &setup{t: t, root: root}
	s0.put("docs/a.md", "one\n")
	s0.put("docs/deep/b.md", "two\n")
	s0.put("docs/.git/HEAD", "ref\n")
	s0.put("docs/big.txt", strings.Repeat("x", 40))
	s0.put("docs/bin.dat", "a\x00b")
	s0.put("other/c.md", "three\n")
	s := started(t, root, map[string]any{"folders": []string{"docs"}, "content-limit": 32})

	a, ok := s.model("docs/a.md")
	if !ok || a.Path != "docs/a.md" || a.Content != "one\n" || !a.Held || a.Size != 4 || len(a.Hash) != 64 {
		t.Fatalf("docs/a.md in the model: %+v (stands: %v)", a, ok)
	}
	if b, ok := s.model("docs/deep/b.md"); !ok || b.Content != "two\n" {
		t.Fatalf("a file in a folder under a managed one: %+v (stands: %v)", b, ok)
	}
	for _, rel := range []string{"docs/big.txt", "docs/bin.dat"} {
		if f, ok := s.model(rel); !ok || f.Held || f.Content != "" || f.Hash == "" {
			t.Fatalf("%s is held by its hash alone, and the model holds %+v (stands: %v)", rel, f, ok)
		}
	}
	for _, rel := range []string{"docs/.git/HEAD", "other/c.md"} {
		if _, ok := s.model(rel); ok {
			t.Fatalf("%s stands in the model", rel)
		}
	}
	if v, _ := s.fake.Out("watching"); v.V != false {
		t.Fatalf("before its cookie is heard the model is watched: %v", v.V)
	}
	s.watched("docs")
	if _, err := os.Stat(filepath.Join(root, "docs", files.Own+"cookie")); !os.IsNotExist(err) {
		t.Fatalf("the cookie stays after it was heard: %v", err)
	}
}

// A read answers from memory: once watched, a change on the disk the watcher
// has not reported yet does not reach a read.
func TestAReadAnswersFromMemory(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	(&setup{t: t, root: root}).put("docs/a.md", "one\n")
	s := started(t, root, map[string]any{"folders": []string{"docs"}})
	s.watched("docs")
	s.put("docs/a.md", "unheard\n")

	got, err := serve[files.File](s, "read", files.Ref{Path: "docs/a.md"})
	if err != nil || got.Content != "one\n" {
		t.Fatalf("a read answers %+v, %v, and wants the model's content one", got, err)
	}
	listed, err := serve[files.Listing](s, "list", files.Folder{})
	if err != nil || len(listed.Files) != 1 || listed.Files[0].Path != "docs/a.md" || listed.Files[0].Content != "" {
		t.Fatalf("the list answers %+v, %v", listed, err)
	}
	found, err := serve[files.Found](s, "search", files.Search{Pattern: "^on", Folder: "docs"})
	if err != nil || len(found.Matches) != 1 || found.Matches[0] != (files.Match{Path: "docs/a.md", Line: 1, Text: "one"}) {
		t.Fatalf("the search answers %+v, %v, and wants the model's line one", found, err)
	}
	if _, err := serve[files.File](s, "read", files.Ref{Path: "other/c.md"}); err == nil || !strings.Contains(err.Error(), "no managed folder") {
		t.Fatalf("a read outside the managed folders answers %v, and wants a refusal naming it", err)
	}
	if _, err := serve[files.File](s, "read", files.Ref{Path: "../a.md"}); err == nil || !strings.Contains(err.Error(), "leaves the checkout's root") {
		t.Fatalf("a read of a path out of the root answers %v", err)
	}
}

// A hand edit on the disk shows in the model once the watcher reports it: a
// changed file, a new one, a new folder with a file in it, and a removed file.
func TestAHandEditShowsInTheModel(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	(&setup{t: t, root: root}).put("docs/a.md", "one\n")
	(&setup{t: t, root: root}).put("docs/gone.md", "bye\n")
	s := started(t, root, map[string]any{"folders": []string{"docs"}})
	s.watched("docs")

	s.hear(s.put("docs/a.md", "edited by hand\n"))
	if a, _ := s.model("docs/a.md"); a.Content != "edited by hand\n" {
		t.Fatalf("after a hand edit the model holds %q", a.Content)
	}
	s.hear(s.put("docs/new.md", "new\n"))
	if n, ok := s.model("docs/new.md"); !ok || n.Content != "new\n" {
		t.Fatalf("a new file in the model: %+v (stands: %v)", n, ok)
	}
	s.put("docs/sub/inner.md", "inner\n")
	s.hear(filepath.Join(root, "docs", "sub"))
	if n, ok := s.model("docs/sub/inner.md"); !ok || n.Content != "inner\n" {
		t.Fatalf("a file in a new folder: %+v (stands: %v)", n, ok)
	}
	s.w.mu.Lock()
	added := strings.Join(s.w.added, "\n")
	s.w.mu.Unlock()
	if !strings.Contains(added, filepath.Join(root, "docs", "sub")) {
		t.Fatalf("the new folder is not watched; watched: %s", added)
	}
	gone := filepath.Join(root, "docs", "gone.md")
	if err := os.Remove(gone); err != nil {
		t.Fatal(err)
	}
	s.hear(gone)
	if _, ok := s.model("docs/gone.md"); ok {
		t.Fatal("a removed file stands in the model")
	}
}

// A write names the version it read: one that names an old version is
// refused, naming re-read, also where the watcher has not reported the hand
// edit that made it old; the right version writes the disk and the model.
func TestAWriteThatNamesAnOldVersionIsRefused(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	(&setup{t: t, root: root}).put("docs/a.md", "one\n")
	s := started(t, root, map[string]any{"folders": []string{"docs"}})
	s.watched("docs")

	read, err := serve[files.File](s, "read", files.Ref{Path: "docs/a.md"})
	if err != nil {
		t.Fatal(err)
	}
	s.put("docs/a.md", "a hand edit, unheard\n")
	_, err = serve[files.File](s, "write", files.Change{Path: "docs/a.md", Content: "mine\n", Version: read.Hash})
	if err == nil || !strings.Contains(err.Error(), "re-read") || !strings.Contains(err.Error(), "docs/a.md") {
		t.Fatalf("a write naming an old version answers %v, and wants a refusal naming re-read", err)
	}
	if data, _ := os.ReadFile(filepath.Join(root, "docs", "a.md")); string(data) != "a hand edit, unheard\n" {
		t.Fatalf("the refused write changed the disk: %q", data)
	}
	again, err := serve[files.File](s, "read", files.Ref{Path: "docs/a.md"})
	if err != nil || again.Content != "a hand edit, unheard\n" {
		t.Fatalf("the re-read after the refusal answers %+v, %v, and wants the hand edit", again, err)
	}
	wrote, err := serve[files.File](s, "write", files.Change{Path: "docs/a.md", Content: "mine\n", Version: again.Hash})
	if err != nil || wrote.Hash == again.Hash {
		t.Fatalf("a write naming the version standing answers %+v, %v", wrote, err)
	}
	if data, _ := os.ReadFile(filepath.Join(root, "docs", "a.md")); string(data) != "mine\n" {
		t.Fatalf("the write left the disk at %q", data)
	}
	if a, _ := s.model("docs/a.md"); a.Content != "mine\n" || a.Hash != wrote.Hash {
		t.Fatalf("after the write the model holds %+v", a)
	}

	if _, err := serve[files.File](s, "write", files.Change{Path: "docs/new/b.md", Content: "b\n"}); err != nil {
		t.Fatalf("a new file, naming no version, is refused: %v", err)
	}
	if _, err := serve[files.File](s, "write", files.Change{Path: "docs/new/b.md", Content: "b2\n"}); err == nil || !strings.Contains(err.Error(), "re-read") {
		t.Fatalf("a write naming no version over a standing file answers %v", err)
	}
	if _, err := serve[files.Ref](s, "remove", files.Removal{Path: "docs/a.md", Version: read.Hash}); err == nil || !strings.Contains(err.Error(), "re-read") {
		t.Fatalf("a removal naming an old version answers %v", err)
	}
	if _, err := serve[files.Ref](s, "remove", files.Removal{Path: "docs/a.md", Version: wrote.Hash}); err != nil {
		t.Fatal(err)
	}
	if _, ok := s.model("docs/a.md"); ok {
		t.Fatal("a removed file stands in the model")
	}
	if entries, _ := os.ReadDir(filepath.Join(root, "docs")); len(entries) != 1 || entries[0].Name() != "new" {
		t.Fatalf("the writes left more than their files in docs: %v", entries)
	}
}

// An overflow runs a hash reconcile, which catches what the watcher missed.
func TestAnOverflowReconcilesTheModel(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	(&setup{t: t, root: root}).put("docs/a.md", "one\n")
	(&setup{t: t, root: root}).put("docs/gone.md", "bye\n")
	s := started(t, root, map[string]any{"folders": []string{"docs"}})
	s.watched("docs")
	s.put("docs/a.md", "missed\n")
	s.put("docs/also.md", "missed too\n")
	if err := os.Remove(filepath.Join(root, "docs", "gone.md")); err != nil {
		t.Fatal(err)
	}
	s.w.errors <- files.ErrOverflow
	s.settle()
	if a, _ := s.model("docs/a.md"); a.Content != "missed\n" {
		t.Fatalf("after the overflow the model holds %q", a.Content)
	}
	if _, ok := s.model("docs/also.md"); !ok {
		t.Fatal("a file the watcher missed is not in the model after the overflow")
	}
	if _, ok := s.model("docs/gone.md"); ok {
		t.Fatal("a file removed unheard stands after the overflow")
	}
}

// A watcher that does not report the cookie within its wait is deaf: the
// model is unwatched, and reads, lists and searches read the disk.
func TestADeafWatcherReadsTheDisk(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	(&setup{t: t, root: root}).put("docs/a.md", "one\n")
	s := started(t, root, map[string]any{"folders": []string{"docs"}})
	s.clock.fire <- time.Time{}
	s.settle()
	if v, _ := s.fake.Out("watching"); v.V != false {
		t.Fatalf("a watcher that missed the cookie leaves watching %v", v.V)
	}
	if _, err := os.Stat(filepath.Join(root, "docs", files.Own+"cookie")); !os.IsNotExist(err) {
		t.Fatalf("the cookie stays once its wait ended: %v", err)
	}
	s.put("docs/a.md", "read cold\n")
	s.put("docs/b.md", "also cold\n")
	if got, err := serve[files.File](s, "read", files.Ref{Path: "docs/a.md"}); err != nil || got.Content != "read cold\n" {
		t.Fatalf("an unwatched read answers %+v, %v, and wants the disk's", got, err)
	}
	if found, err := serve[files.Found](s, "search", files.Search{Pattern: "cold"}); err != nil || len(found.Matches) != 2 {
		t.Fatalf("an unwatched search answers %+v, %v, and wants both files of the disk", found, err)
	}
}

// A search answers each matching line with its path and number, names the
// files it did not read, and stops at MaxMatches.
func TestASearchAnswersLinesAndNamesWhatItSkipped(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	s0 := &setup{t: t, root: root}
	s0.put("docs/a.md", "alpha\nbeta\nalphabet\n")
	s0.put("docs/big.txt", strings.Repeat("alpha ", 2<<10)) // above the limit
	s0.put("docs/many.txt", strings.Repeat("hit\n", files.MaxMatches+5))
	s := started(t, root, map[string]any{"folders": []string{"docs"}, "content-limit": 10 << 10})
	s.watched("docs")
	found, err := serve[files.Found](s, "search", files.Search{Pattern: "^alpha"})
	if err != nil {
		t.Fatal(err)
	}
	if len(found.Matches) != 2 || found.Matches[1] != (files.Match{Path: "docs/a.md", Line: 3, Text: "alphabet"}) || found.More ||
		strings.Join(found.Unsearched, " ") != "docs/big.txt" {
		t.Fatalf("the search answers %+v", found)
	}
	hits, err := serve[files.Found](s, "search", files.Search{Pattern: "hit"})
	if err != nil || len(hits.Matches) != files.MaxMatches || !hits.More {
		t.Fatalf("a search past MaxMatches answers %d matches, more %v, %v", len(hits.Matches), hits.More, err)
	}
	if _, err := serve[files.Found](s, "search", files.Search{Pattern: "("}); err == nil || !strings.Contains(err.Error(), "regular expression") {
		t.Fatalf("a broken pattern answers %v", err)
	}
}

// The settings are checked at the start: a folder that does not stand, or
// leaves the root, refuses it, and so does a second instance.
func TestTheStartChecksTheSettings(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	for _, folder := range []string{"missing", "../up", filepath.Join(root, "abs")} {
		c := files.New(files.Options{Root: root, Watch: func() (files.Watcher, error) { return newFed(), nil }})
		fake, err := kerntest.New(c.Component)
		if err != nil {
			t.Fatal(err)
		}
		if err := fake.Config("folders", []string{folder}); err != nil {
			t.Fatal(err)
		}
		if err := fake.Start("files"); err == nil || !strings.Contains(err.Error(), "config folders") {
			t.Fatalf("folder %s starts with %v, and wants a refusal naming config folders", folder, err)
		}
	}
	s := started(t, root, nil)
	if err := s.fake.Start("again"); err == nil || !strings.Contains(err.Error(), "one instance per core") {
		t.Fatalf("a second instance starts with %v", err)
	}
}

// A key names its path and back: a slash, an underscore and any byte a family
// key may not hold are escaped, and every key is one the index takes.
func TestAKeyNamesItsPath(t *testing.T) {
	t.Parallel()
	for _, p := range []string{"docs/a_b.md", "a b/ü.txt", "x/.hidden", "plain-name.md"} {
		k := files.Key(p)
		if !index.Key(k) {
			t.Fatalf("Key(%q) = %q, which no family takes", p, k)
		}
		if back, err := files.PathOf(k); err != nil || back != p {
			t.Fatalf("PathOf(%q) = %q, %v, and wants %q", k, back, err, p)
		}
	}
	if got := files.Key("docs/a_b.md"); got != "docs_2fa_5fb.md" {
		t.Fatalf("Key(docs/a_b.md) = %q", got)
	}
	if _, err := files.PathOf("bad_2"); err == nil {
		t.Fatal("a key that ends inside an escape is taken")
	}
}

// The operating system's watcher keeps the model in step with a hand edit on
// the real disk: the core starts the module on its wiring, the cookie is
// heard, and a file written past the module shows in its name. The waits
// bound events the disk delivers at once.
func TestTheRealWatcherHearsAHandEdit(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "docs"), 0o755); err != nil {
		t.Fatal(err)
	}
	c := files.New(files.Options{Root: root, Clock: host.Wall{}})
	w, err := index.ParseWiring([]byte(`
[instances.files]
component = "files"
config = { folders = ["docs"], cookie-wait = "30s" }

[wires]
"files.model" = "files/model/<key>"
"files.watching" = "files/watching"
`))
	if err != nil {
		t.Fatal(err)
	}
	watching := make(chan any, 8)
	model := make(chan any, 8)
	ix, err := index.Start(w, c.Component)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(c.Stop)
	if _, err := ix.Subscribe("files/watching", func(v index.Value) { watching <- v.V }); err != nil {
		t.Fatal(err)
	}
	if v, err := ix.Snapshot().Read("files/watching"); err != nil || v.V != true {
		select {
		case v := <-watching:
			if v != true {
				t.Fatalf("the real watcher's cookie: watching %v", v)
			}
		case <-time.After(30 * time.Second):
			t.Fatal("the real watcher did not report the cookie")
		}
	}
	if _, err := ix.Subscribe("files/model/"+files.Key("docs/a.md"), func(v index.Value) { model <- v.V }); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "docs", "a.md"), []byte("by hand\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	deadline := time.After(30 * time.Second)
	for {
		select {
		case v := <-model:
			if f, _ := v.(files.File); f.Content == "by hand\n" {
				return
			}
		case <-deadline:
			t.Fatal("a hand edit on the real disk did not reach the model")
		}
	}
}

// The module meets every expectation the checkout's readers pool under the
// names it claims (expect.toml), each against a fresh instance over a
// temporary folder: its outside thing is the disk itself, so the normal
// suite's run is its run against the real service.
func TestTheModuleMeetsThePooledExpectations(t *testing.T) {
	t.Parallel()
	all, err := kerntest.ExpectationFiles(filepath.Join("..", ".."))
	if err != nil {
		t.Fatal(err)
	}
	p, err := kerntest.ReadPool(all...)
	if err != nil {
		t.Fatal(err)
	}
	m := kerntest.Module{
		Component: files.Component.Component,
		File:      "expect.toml",
		Fresh: func() *index.Component {
			return files.New(files.Options{Root: t.TempDir(), Watch: func() (files.Watcher, error) { return newFed(), nil }}).Component
		},
	}
	if err := m.Meet(p); err != nil {
		t.Fatal(err)
	}
}
