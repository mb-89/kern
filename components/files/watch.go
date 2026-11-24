package files

import (
	"errors"
	"sync"

	"github.com/fsnotify/fsnotify"
)

// Watcher is what tells the component a path changed: the operating system's
// watcher (fsnotify) outside tests, and one a test feeds by hand, so the test
// decides what arrives and in what order and never waits for the disk to be
// noticed. It is not recursive: the component adds each folder it holds, and
// a folder made later when its creation arrives.
type Watcher interface {
	// Add watches the files straight in dir.
	Add(dir string) error
	// Events hands the absolute path of each change: a write, a creation, a
	// removal or a rename. The component reads the disk to learn which.
	Events() <-chan string
	// Errors hands what went wrong; ErrOverflow is a lost event.
	Errors() <-chan error
	Close() error
}

// deaf is the watcher where none is given: it watches nothing and hears
// nothing.
type deaf struct{}

func (deaf) Add(string) error      { return nil }
func (deaf) Events() <-chan string { return nil }
func (deaf) Errors() <-chan error  { return nil }
func (deaf) Close() error          { return nil }

// ErrOverflow is the error a watcher hands when it lost events: the answer is
// a hash reconcile of every managed folder.
var ErrOverflow = errors.New("the watcher lost events")

// watchBuffer is the size of the buffer the watcher reads events into. On
// Windows a full buffer is an overflow, so it is set well above the default
// and an overflow stays rare (quackitect v4 src/engine/indexwatch.go).
const watchBuffer = 1 << 20

// OpenWatcher opens the operating system's watcher.
func OpenWatcher() (Watcher, error) {
	w, err := fsnotify.NewBufferedWatcher(4096)
	if err != nil {
		return nil, err
	}
	o := &osWatcher{w: w, events: make(chan string, 4096), errors: make(chan error, 16), done: make(chan struct{})}
	go o.pass()
	return o, nil
}

// osWatcher is fsnotify behind Watcher: it hands each event's path, and an
// overflow as ErrOverflow.
type osWatcher struct {
	w      *fsnotify.Watcher
	events chan string
	errors chan error
	done   chan struct{}
	once   sync.Once
}

func (o *osWatcher) pass() {
	defer close(o.events)
	defer close(o.errors)
	for {
		select {
		case ev, ok := <-o.w.Events:
			if !ok {
				return
			}
			select {
			case o.events <- ev.Name:
			case <-o.done:
				return
			}
		case err, ok := <-o.w.Errors:
			if !ok {
				return
			}
			if errors.Is(err, fsnotify.ErrEventOverflow) {
				err = ErrOverflow
			}
			select {
			case o.errors <- err:
			case <-o.done:
				return
			}
		}
	}
}

func (o *osWatcher) Add(dir string) error {
	return o.w.AddWith(dir, fsnotify.WithBufferSize(watchBuffer))
}
func (o *osWatcher) Events() <-chan string { return o.events }
func (o *osWatcher) Errors() <-chan error  { return o.errors }
func (o *osWatcher) Close() (err error) {
	o.once.Do(func() {
		close(o.done)
		err = o.w.Close()
	})
	return err
}
