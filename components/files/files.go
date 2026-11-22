// Package files is kern's files component: the I/O component whose outside
// thing is the disk under the managed folders an application names in its
// settings. It holds every managed file in memory as names in the index (its
// path, its content hash and, below a size limit, its content), answers reads
// and searches from that model, and writes through compare-and-swap: a write
// names the version it read and is refused, naming "re-read", once the file
// changed since. A watcher and a hash reconcile keep the model in step with
// what writes the disk past it, a hand edit among them. This is the person's
// decision of 22 Nov (deck "The warm model", option B). README.md beside this
// file is its contract.
package files

import (
	"bufio"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"

	"kern/host"
	"kern/index"
)

// Name is the component's name, which the wiring's instances name.
const Name = "files"

// The config keys' built-in values.
const (
	// ContentLimit is the largest file, in bytes, whose content the model holds.
	ContentLimit = 256 << 10
	// CookieWait is how long the watcher has to report the cookie at start.
	CookieWait = "2s"
)

// Own is the prefix of the files the component writes itself in a managed
// folder, the start's cookie and a write's temporary file: the model never
// holds one.
const Own = ".kern-files-"

// cookie is the cookie's file name in each managed folder.
const cookie = Own + "cookie"

// MaxMatches is the most matches a search answers; More says there were more.
const MaxMatches = 1000

// seedBatch is the most content bytes one commit carries, so one message
// stays under the bus's cap (quackitect v5 src/modules/files/watch.go).
const seedBatch = 8 << 20

// Forbids is the files module's forbid list (kern#18): the direct file
// writes that only it may make on a managed folder, Go's and Python's. Since
// the folders are a setting, another component's use of one is a write past
// the model wherever it reaches a managed folder; kern#18's check reads the
// list once it lands.
var Forbids = []string{
	"os.WriteFile", "os.Create", "os.CreateTemp", "os.OpenFile", "os.Rename", "os.Remove", "os.RemoveAll",
	"os.Mkdir", "os.MkdirAll", "os.Truncate", "os.Chmod", "os.Symlink", "os.Link",
	"pathlib.Path.write_text", "pathlib.Path.write_bytes", "pathlib.Path.unlink", "os.replace", "shutil.move",
}

// skipped are the folders inside a managed folder the model never enters.
var skipped = map[string]bool{".git": true, "node_modules": true}

// File is one managed file as the model holds it.
type File struct {
	// Path is its path relative to the root, slash-separated.
	Path string `json:"path"`
	// Hash is its content's SHA-256 in hex: the version a write names.
	Hash string `json:"hash"`
	// Size is its length in bytes.
	Size int64 `json:"size"`
	// Held says the model holds its content: a text file no larger than
	// content-limit. A binary file, or a larger one, is held by its hash alone.
	Held bool `json:"held"`
	// Content is its content where held, and in the answer of read always.
	Content string `json:"content,omitempty"`
}

// Ref names one managed file by its path, relative to the root.
type Ref struct {
	Path string `json:"path"`
}

// Change is a write through the module: the content to stand at Path, and
// Version, the hash the writer read, "" for a file that does not stand yet.
type Change struct {
	Path    string `json:"path"`
	Content string `json:"content"`
	Version string `json:"version"`
}

// Removal removes a managed file: Version is the hash the remover read.
type Removal struct {
	Path    string `json:"path"`
	Version string `json:"version"`
}

// Folder names a managed folder, or a folder under one, by its path; "" is every managed folder.
type Folder struct {
	Folder string `json:"folder"`
}

// Listing is every managed file under a folder, by path, without content.
type Listing struct {
	Files []File `json:"files"`
}

// Search asks for every line of the held files under Folder that Pattern, a
// Go regular expression, matches.
type Search struct {
	Pattern string `json:"pattern"`
	Folder  string `json:"folder"`
}

// Match is one line a search found: its file, its number from 1, and its text.
type Match struct {
	Path string `json:"path"`
	Line int    `json:"line"`
	Text string `json:"text"`
}

// Found is a search's answer: its matches, by path and line, MaxMatches at
// most; More where there were more; and Unsearched, the files under the
// folder the model holds by hash alone, which the search did not read.
type Found struct {
	Matches    []Match  `json:"matches"`
	More       bool     `json:"more"`
	Unsearched []string `json:"unsearched"`
}

// Options are what the component reaches the outside with; each has a
// default, and a test replaces it.
type Options struct {
	// Root is the folder the managed folders are named relative to: the
	// core's working directory, which the start makes the checkout's root,
	// where empty.
	Root string
	// Clock is what the cookie's wait measures against: the wall clock where none is given.
	Clock host.Clock
	// Watch opens the watcher: the operating system's (OpenWatcher) where none is given.
	Watch func() (Watcher, error)
}

// Files is the component and its model: one instance per core.
type Files struct {
	// Component is what the index, the wiring and the catalog see.
	Component *index.Component
	o         Options

	mu       sync.Mutex
	instance string
	root     string
	folders  []string // slash-separated, relative to root
	limit    int
	commit   index.Commit
	model    map[string]File // by path
	watching bool
	w        Watcher
	cookies  map[string]bool // each cookie's absolute path, and whether it was heard
	done     chan struct{}
	ended    chan struct{}
}

// The component's outputs.
var (
	modelOut    = index.Family("model", File{}, "Each managed file at its path's key (files.Key): its path, its content hash and, below content-limit, its content.")
	watchingOut = index.Output("watching", false, "Whether the watcher reported the start's cookie in every managed folder: while false, reads, lists and searches read the disk.")
)

// New makes the component with its outside reached through o.
func New(o Options) *Files {
	if o.Clock == nil {
		o.Clock = host.Wall{}
	}
	if o.Watch == nil {
		o.Watch = OpenWatcher
	}
	f := &Files{o: o, model: map[string]File{}}
	f.Component = index.Declare(Name, "Holds the managed folders in memory: reads and searches answer from the model, and a write names the version it read.",
		index.IO(), modelOut, watchingOut,
		index.Config("folders", []string{}, "The managed folders, relative to the checkout's root: each must stand at the start."),
		index.Config("content-limit", ContentLimit, "The largest file, in bytes, whose content the model holds; a larger one is held by its hash alone."),
		index.Config("cookie-wait", CookieWait, "How long the watcher has to report the cookie the start writes in each managed folder; one it misses marks the model unwatched, and reads read the disk."),
		index.Inlet(f.start),
		index.Verb("read", "Reads one managed file from the model: its path, hash and content. A file the model holds by hash alone, or any file while unwatched, is read from the disk.", f.read),
		index.Verb("list", "Lists the managed files under a folder from the model, without content.", f.list),
		index.Verb("search", "Searches the held files under a folder for a Go regular expression, line by line, from the model.", f.search),
		index.Verb("write", "Writes one managed file through the model, naming the version it read (\"\" for a new file); a file changed since is refused, naming re-read.", f.write),
		index.Verb("remove", "Removes one managed file, naming the version it read; a file changed since is refused, naming re-read.", f.remove),
	)
	return f
}

// Component is the component a binary carries; importing this package
// registers it, and only a wiring's enabled instance runs it.
var Component = New(Options{})

func init() { index.Register(Component.Component) }

// start reads the settings, opens the watcher on every managed folder,
// reconciles the model with the disk, writes the cookies and runs the loop.
func (f *Files) start(instance string, read index.Reader, commit index.Commit) error {
	folders, _ := read(index.ConfigPrefix + "folders").([]string)
	limit, ok := read(index.ConfigPrefix + "content-limit").(int)
	if !ok || limit < 0 {
		return fmt.Errorf("config content-limit: %v is no size in bytes of 0 or more", read(index.ConfigPrefix+"content-limit"))
	}
	waitText, _ := read(index.ConfigPrefix + "cookie-wait").(string)
	wait, err := time.ParseDuration(waitText)
	if err != nil || wait <= 0 {
		return fmt.Errorf("config cookie-wait: %q is no duration above 0, such as \"2s\"", waitText)
	}
	root := f.o.Root
	if root == "" {
		if root, err = os.Getwd(); err != nil {
			return fmt.Errorf("files: the root: %w", err)
		}
	}
	var clean []string
	for _, folder := range folders {
		rel, err := relative(folder)
		if err != nil {
			return fmt.Errorf("config folders: %w", err)
		}
		if info, err := os.Stat(filepath.Join(root, filepath.FromSlash(rel))); err != nil || !info.IsDir() {
			return fmt.Errorf("config folders: %s is no folder under %s: create it, or drop it from folders", folder, root)
		}
		clean = append(clean, rel)
	}

	f.mu.Lock()
	defer f.mu.Unlock()
	if f.instance != "" {
		return fmt.Errorf("component %s runs one instance per core, and %s is one already", Name, f.instance)
	}
	w, err := f.o.Watch()
	if err != nil {
		return fmt.Errorf("files: the watcher cannot open: %w", err)
	}
	f.instance, f.root, f.folders, f.limit, f.commit, f.w = instance, root, clean, limit, commit, w
	f.done, f.ended = make(chan struct{}), make(chan struct{})
	for _, folder := range clean {
		if err := f.watchTree(f.abs(folder)); err != nil {
			w.Close()
			return fmt.Errorf("files: folder %s cannot be watched: %w", folder, err)
		}
	}
	// The watcher opens before the reconcile, so a change during it is heard after it.
	if err := f.reconcile(); err != nil {
		w.Close()
		return err
	}
	f.cookies = map[string]bool{}
	for _, folder := range clean {
		c := filepath.Join(f.abs(folder), cookie)
		if err := os.WriteFile(c, []byte(instance), 0o644); err == nil {
			f.cookies[c] = false
		}
	}
	if len(clean) > 0 && len(f.cookies) < len(clean) {
		f.deaf() // a folder the cookie cannot be written in cannot be proven watched
	} else if len(f.cookies) == 0 {
		f.watching = true // no folder: nothing to watch
		_ = f.commit(watchingOut.Set(true))
	}
	go f.loop(f.o.Clock.After(wait))
	return nil
}

// Stop ends the instance's loop and closes its watcher; a test stops so.
func (f *Files) Stop() {
	f.mu.Lock()
	done, ended := f.done, f.ended
	f.mu.Unlock()
	if done == nil {
		return
	}
	select {
	case <-done:
	default:
		close(done)
	}
	<-ended
}

// loop hears the watcher: each path it hands is brought into step, an
// overflow runs a hash reconcile, and the cookie's wait ending before every
// cookie was heard marks the model unwatched. It ends when Stop is called,
// the watcher closes, or a commit fails, as once its core is gone.
func (f *Files) loop(cookieWait <-chan time.Time) {
	defer close(f.ended)
	defer f.w.Close()
	for {
		var err error
		select {
		case <-f.done:
			return
		case <-cookieWait:
			cookieWait = nil
			f.mu.Lock()
			if !f.watching {
				f.deaf()
			}
			f.mu.Unlock()
		case p, ok := <-f.w.Events():
			if !ok {
				return
			}
			f.mu.Lock()
			err = f.heard(p)
			f.mu.Unlock()
		case e, ok := <-f.w.Errors():
			if !ok {
				return
			}
			if errors.Is(e, ErrOverflow) {
				f.mu.Lock()
				err = f.reconcile()
				f.mu.Unlock()
			}
		}
		if err != nil {
			return // its core is gone
		}
	}
}

// heard brings the path the watcher handed into step; a cookie heard marks
// its folder watched. The caller holds the lock.
func (f *Files) heard(abs string) error {
	abs = filepath.Clean(abs)
	if heard, isCookie := f.cookies[abs]; isCookie {
		if !heard {
			f.cookies[abs] = true
			_ = os.Remove(abs)
			if f.allHeard() && !f.watching {
				f.watching = true
				return f.commit(watchingOut.Set(true))
			}
		}
		return nil
	}
	rel, ok := f.relOf(abs)
	if !ok || !f.managed(rel) {
		return nil
	}
	return f.commitAll(f.sync(rel))
}

func (f *Files) allHeard() bool {
	for _, heard := range f.cookies {
		if !heard {
			return false
		}
	}
	return true
}

// deaf marks the model unwatched: a watcher that did not report the cookie
// delivers no events, so every read reads the disk. The caller holds the lock.
func (f *Files) deaf() {
	for c := range f.cookies {
		_ = os.Remove(c)
	}
	f.watching = false
	_ = f.commit(watchingOut.Set(false))
}

// sync reads one managed path from the disk and answers the writes that bring
// the model in step with it: a file read again, a folder watched and walked,
// and a path gone dropped with everything under it. The caller holds the lock.
func (f *Files) sync(rel string) []index.Write {
	abs := f.abs(rel)
	info, err := os.Lstat(abs)
	switch {
	case err != nil:
		return f.dropUnder(rel)
	case info.IsDir():
		if !f.managedDir(rel) {
			return nil
		}
		_ = f.watchTree(abs) // a folder it cannot watch is caught by the next reconcile
		var writes []index.Write
		_ = filepath.WalkDir(abs, func(p string, d fs.DirEntry, err error) error {
			if err != nil {
				return nil
			}
			r, ok := f.relOf(p)
			if d.IsDir() {
				if p != abs && (!ok || !f.managedDir(r)) {
					return filepath.SkipDir
				}
				return nil
			}
			if ok && d.Type().IsRegular() && f.managed(r) {
				writes = append(writes, f.load(r)...)
			}
			return nil
		})
		return writes
	case !info.Mode().IsRegular():
		return f.dropUnder(rel)
	}
	return f.load(rel)
}

// load reads one file and answers its write where the model holds another
// version, or its drop where it is gone. The caller holds the lock.
func (f *Files) load(rel string) []index.Write {
	data, err := os.ReadFile(f.abs(rel))
	if err != nil {
		return f.dropUnder(rel)
	}
	return f.put(f.fileOf(rel, data))
}

// put sets one file in the model and answers its write, none where the model
// holds it already. The caller holds the lock.
func (f *Files) put(file File) []index.Write {
	if have, ok := f.model[file.Path]; ok && have.Hash == file.Hash && have.Held == file.Held {
		return nil
	}
	f.model[file.Path] = file
	return []index.Write{modelOut.At(Key(file.Path), file)}
}

// dropUnder drops rel and every file under it from the model. The caller holds the lock.
func (f *Files) dropUnder(rel string) []index.Write {
	var writes []index.Write
	for p := range f.model {
		if p == rel || strings.HasPrefix(p, rel+"/") {
			delete(f.model, p)
			writes = append(writes, modelOut.Drop(Key(p)))
		}
	}
	return writes
}

// reconcile hashes every file of every managed folder and brings the model in
// step: what changed is written, what is gone is dropped. It runs at the
// start and when the watcher lost events, and before each read while the
// model is unwatched. The caller holds the lock.
func (f *Files) reconcile() error {
	seen := map[string]bool{}
	var writes []index.Write
	for _, folder := range f.folders {
		top := f.abs(folder)
		err := filepath.WalkDir(top, func(p string, d fs.DirEntry, err error) error {
			if err != nil {
				if p == top {
					return err
				}
				return nil // a path it cannot read costs that path alone
			}
			rel, ok := f.relOf(p)
			if d.IsDir() {
				if p != top && (!ok || !f.managedDir(rel)) {
					return filepath.SkipDir
				}
				return nil
			}
			if !ok || !d.Type().IsRegular() || !f.managed(rel) {
				return nil
			}
			data, err := os.ReadFile(p)
			if err != nil {
				return nil
			}
			seen[rel] = true
			writes = append(writes, f.put(f.fileOf(rel, data))...)
			return nil
		})
		if err != nil {
			return fmt.Errorf("files: folder %s cannot be read: %w", folder, err)
		}
	}
	for p := range f.model {
		if !seen[p] {
			delete(f.model, p)
			writes = append(writes, modelOut.Drop(Key(p)))
		}
	}
	return f.commitAll(writes)
}

// commitAll commits writes in batches of seedBatch content bytes at most.
// The caller holds the lock.
func (f *Files) commitAll(writes []index.Write) error {
	for len(writes) > 0 {
		n, size := 0, 0
		for n < len(writes) {
			if file, ok := writes[n].Value.(File); ok {
				if n > 0 && size+len(file.Content) > seedBatch {
					break
				}
				size += len(file.Content)
			}
			n++
		}
		if err := f.commit(writes[:n]...); err != nil {
			return err
		}
		writes = writes[n:]
	}
	return nil
}

// watchTree adds dir and every managed folder under it to the watcher.
func (f *Files) watchTree(dir string) error {
	return filepath.WalkDir(dir, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			if p == dir {
				return err
			}
			return nil
		}
		if !d.IsDir() {
			return nil
		}
		if rel, ok := f.relOf(p); p != dir && (!ok || !f.managedDir(rel)) {
			return filepath.SkipDir
		}
		return f.w.Add(p)
	})
}

// fileOf is a file of the model from its content.
func (f *Files) fileOf(rel string, data []byte) File {
	sum := sha256.Sum256(data)
	file := File{Path: rel, Hash: hex.EncodeToString(sum[:]), Size: int64(len(data))}
	if len(data) <= f.limit && !bytes.ContainsRune(data, 0) {
		file.Held, file.Content = true, string(data)
	}
	return file
}

func (f *Files) abs(rel string) string { return filepath.Join(f.root, filepath.FromSlash(rel)) }

// relOf answers abs relative to the root, slash-separated, and false for a path outside it.
func (f *Files) relOf(abs string) (string, bool) {
	rel, err := filepath.Rel(f.root, abs)
	if err != nil {
		return "", false
	}
	rel = filepath.ToSlash(rel)
	if rel == "." || rel == ".." || strings.HasPrefix(rel, "../") {
		return "", false
	}
	return rel, true
}

// managed says whether rel is a file the model holds: under a managed folder,
// in no skipped folder, and none of the component's own files.
func (f *Files) managed(rel string) bool {
	return f.under(rel) && !strings.HasPrefix(path.Base(rel), Own) && f.managedDir(path.Dir(rel))
}

// managedDir says whether the folder rel lies under a managed folder, or is
// one, and in no skipped folder below it.
func (f *Files) managedDir(rel string) bool {
	for _, folder := range f.folders {
		if rel != folder && !strings.HasPrefix(rel, folder+"/") {
			continue
		}
		inside := strings.TrimPrefix(strings.TrimPrefix(rel, folder), "/")
		ok := true
		for _, seg := range strings.Split(inside, "/") {
			if skipped[seg] {
				ok = false
			}
		}
		if ok {
			return true
		}
	}
	return false
}

func (f *Files) under(rel string) bool {
	for _, folder := range f.folders {
		if strings.HasPrefix(rel, folder+"/") {
			return true
		}
	}
	return false
}

// relative is a path a request or the settings name, made clean and
// slash-separated, and refused where it is absolute or leaves the root.
func relative(p string) (string, error) {
	if p == "" {
		return "", errors.New("the path is empty")
	}
	if filepath.IsAbs(p) || strings.HasPrefix(p, "/") || strings.HasPrefix(p, `\`) {
		return "", fmt.Errorf("%s is absolute: name it relative to the checkout's root", p)
	}
	rel := path.Clean(filepath.ToSlash(p))
	if rel == "." || rel == ".." || strings.HasPrefix(rel, "../") {
		return "", fmt.Errorf("%s leaves the checkout's root: name a path under it", p)
	}
	return rel, nil
}

// file answers the managed path a request names, and refuses one outside
// every managed folder, which the module does not reach.
func (f *Files) file(verb, p string) (string, error) {
	rel, err := relative(p)
	if err != nil {
		return "", fmt.Errorf("files %s: %w", verb, err)
	}
	if !f.managed(rel) {
		return "", fmt.Errorf("files %s %s: no managed folder holds it (folders: %s): read and write it on the disk, or add its folder to the setting folders", verb, rel, strings.Join(f.folders, ", "))
	}
	return rel, nil
}

func (f *Files) begun(verb string) error {
	if f.instance == "" {
		return fmt.Errorf("files %s: no instance of the files component runs: switch one on in the wiring", verb)
	}
	return nil
}

// read answers one managed file from the model: one held by hash alone, and
// any while the model is unwatched, is read from the disk, which brings the
// model in step.
func (f *Files) read(_ context.Context, r Ref) (File, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if err := f.begun("read"); err != nil {
		return File{}, err
	}
	rel, err := f.file("read", r.Path)
	if err != nil {
		return File{}, err
	}
	file, ok := f.model[rel]
	if f.watching && ok && file.Held {
		return file, nil
	}
	data, err := os.ReadFile(f.abs(rel))
	if err != nil {
		_ = f.commitAll(f.dropUnder(rel))
		return File{}, fmt.Errorf("files read %s: no such file stands: %w", rel, err)
	}
	file = f.fileOf(rel, data)
	if err := f.commitAll(f.put(file)); err != nil {
		return File{}, fmt.Errorf("files read %s: %w", rel, err)
	}
	file.Content = string(data)
	return file, nil
}

// fresh brings the model in step with the disk where it is unwatched, before
// a list or a search reads it. The caller holds the lock.
func (f *Files) fresh(verb string) error {
	if err := f.begun(verb); err != nil {
		return err
	}
	if !f.watching {
		return f.reconcile()
	}
	return nil
}

// folder answers the folder a list or a search names, "" for every managed one.
func (f *Files) folder(verb, p string) (string, error) {
	if p == "" {
		return "", nil
	}
	rel, err := relative(p)
	if err != nil {
		return "", fmt.Errorf("files %s: %w", verb, err)
	}
	if !f.managedDir(rel) {
		return "", fmt.Errorf("files %s %s: no managed folder holds it (folders: %s)", verb, rel, strings.Join(f.folders, ", "))
	}
	return rel, nil
}

// inFolder answers the model's paths under folder, sorted.
func (f *Files) inFolder(folder string) []string {
	var paths []string
	for p := range f.model {
		if folder == "" || strings.HasPrefix(p, folder+"/") {
			paths = append(paths, p)
		}
	}
	sort.Strings(paths)
	return paths
}

// list answers the managed files under a folder from the model, without content.
func (f *Files) list(_ context.Context, r Folder) (Listing, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if err := f.fresh("list"); err != nil {
		return Listing{}, err
	}
	folder, err := f.folder("list", r.Folder)
	if err != nil {
		return Listing{}, err
	}
	out := Listing{Files: []File{}}
	for _, p := range f.inFolder(folder) {
		file := f.model[p]
		file.Content = ""
		out.Files = append(out.Files, file)
	}
	return out, nil
}

// search answers every line of the held files under a folder that the
// pattern matches, from the model.
func (f *Files) search(_ context.Context, s Search) (Found, error) {
	re, err := regexp.Compile(s.Pattern)
	if err != nil {
		return Found{}, fmt.Errorf("files search: pattern %q is no Go regular expression: %w", s.Pattern, err)
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if err := f.fresh("search"); err != nil {
		return Found{}, err
	}
	folder, err := f.folder("search", s.Folder)
	if err != nil {
		return Found{}, err
	}
	out := Found{Matches: []Match{}, Unsearched: []string{}}
	for _, p := range f.inFolder(folder) {
		file := f.model[p]
		if !file.Held {
			out.Unsearched = append(out.Unsearched, p)
			continue
		}
		sc := bufio.NewScanner(strings.NewReader(file.Content))
		sc.Buffer(make([]byte, 64<<10), len(file.Content)+1)
		for n := 1; sc.Scan(); n++ {
			if !re.MatchString(sc.Text()) {
				continue
			}
			if len(out.Matches) == MaxMatches {
				out.More = true
				break
			}
			out.Matches = append(out.Matches, Match{Path: p, Line: n, Text: sc.Text()})
		}
	}
	return out, nil
}

// current answers the version standing on the disk now, "" where none
// stands, and brings the model in step with it. The caller holds the lock.
func (f *Files) current(rel string) (string, error) {
	data, err := os.ReadFile(f.abs(rel))
	if errors.Is(err, fs.ErrNotExist) {
		return "", f.commitAll(f.dropUnder(rel))
	}
	if err != nil {
		return "", err
	}
	file := f.fileOf(rel, data)
	return file.Hash, f.commitAll(f.put(file))
}

// stale is the refusal of a write or a removal that names another version
// than the one standing.
func stale(verb, rel, named, now string) error {
	switch {
	case named == "":
		return fmt.Errorf("files %s %s: the file stands at version %s, and the %s names none: re-read it and %s again", verb, rel, now, verb, verb)
	case now == "":
		return fmt.Errorf("files %s %s: the file is gone since version %s was read: re-read it and %s again", verb, rel, named, verb)
	}
	return fmt.Errorf("files %s %s: the file changed since version %s was read, and is %s now: re-read it and %s again", verb, rel, named, now, verb)
}

// write writes one managed file through the model: it compares the version
// the writer read with the one standing on the disk now, so a hand edit the
// watcher has not reported yet refuses it too, then writes a temporary file
// beside it and renames it into place.
func (f *Files) write(_ context.Context, c Change) (File, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if err := f.begun("write"); err != nil {
		return File{}, err
	}
	rel, err := f.file("write", c.Path)
	if err != nil {
		return File{}, err
	}
	now, err := f.current(rel)
	if err != nil {
		return File{}, fmt.Errorf("files write %s: %w", rel, err)
	}
	if now != c.Version {
		return File{}, stale("write", rel, c.Version, now)
	}
	abs := f.abs(rel)
	mode := fs.FileMode(0o644)
	if info, err := os.Stat(abs); err == nil {
		mode = info.Mode().Perm()
	}
	if err := os.MkdirAll(filepath.Dir(abs), 0o755); err != nil {
		return File{}, fmt.Errorf("files write %s: %w", rel, err)
	}
	tmp, err := os.CreateTemp(filepath.Dir(abs), Own+"*.tmp")
	if err != nil {
		return File{}, fmt.Errorf("files write %s: %w", rel, err)
	}
	_, werr := tmp.WriteString(c.Content)
	cerr := tmp.Close()
	if err := errors.Join(werr, cerr, os.Chmod(tmp.Name(), mode)); err != nil {
		os.Remove(tmp.Name())
		return File{}, fmt.Errorf("files write %s: %w", rel, err)
	}
	if err := os.Rename(tmp.Name(), abs); err != nil {
		os.Remove(tmp.Name())
		return File{}, fmt.Errorf("files write %s: %w", rel, err)
	}
	_ = f.watchTree(filepath.Dir(abs)) // a folder the write made
	file := f.fileOf(rel, []byte(c.Content))
	if err := f.commitAll(f.put(file)); err != nil {
		return File{}, fmt.Errorf("files write %s: %w", rel, err)
	}
	file.Content = ""
	return file, nil
}

// remove removes one managed file, compared with the disk as write is.
func (f *Files) remove(_ context.Context, r Removal) (Ref, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if err := f.begun("remove"); err != nil {
		return Ref{}, err
	}
	rel, err := f.file("remove", r.Path)
	if err != nil {
		return Ref{}, err
	}
	now, err := f.current(rel)
	if err != nil {
		return Ref{}, fmt.Errorf("files remove %s: %w", rel, err)
	}
	if now == "" {
		return Ref{}, fmt.Errorf("files remove %s: no such file stands: re-read the folder, which no longer lists it", rel)
	}
	if now != r.Version {
		return Ref{}, stale("remove", rel, r.Version, now)
	}
	if err := os.Remove(f.abs(rel)); err != nil {
		return Ref{}, fmt.Errorf("files remove %s: %w", rel, err)
	}
	if err := f.commitAll(f.dropUnder(rel)); err != nil {
		return Ref{}, fmt.Errorf("files remove %s: %w", rel, err)
	}
	return Ref{Path: rel}, nil
}
