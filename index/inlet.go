package index

import (
	"errors"
	"fmt"
)

// Commit is an instance's own commit: an I/O component writes what comes in
// from its outside thing with it, held to its declaration as Index.Commit is.
type Commit func(writes ...Write) error

type inletPart struct {
	fn func(instance string, read Reader, commit Commit) error
}

func (p inletPart) declare(c *Component) {
	if c.inlet != nil {
		c.fail("component %s declares two inlets", c.name)
	}
	c.inlet = p.fn
}

// Inlet declares that an I/O component writes what comes in from its outside
// thing on its own, not only from its commands: the start hands fn each
// switched-on instance's name, a reader of its config, and its commit. An
// error refuses the start.
func Inlet(fn func(instance string, read Reader, commit Commit) error) Part { return inletPart{fn} }

// Start runs the component's inlet for one instance, as the index's start
// does, with read as its config reader and commit as its own commit: a fake
// core starts an I/O component so. A component that declares no inlet starts
// with nothing to do.
func (c *Component) Start(instance string, read Reader, commit Commit) error {
	if c.inlet == nil {
		return nil
	}
	return c.inlet(instance, read, commit)
}

type hearsPart struct {
	fn func(instance, event string, payload []byte) error
}

func (p hearsPart) declare(c *Component) {
	if c.hears != nil {
		c.fail("component %s hears the host's events twice", c.name)
	}
	c.hears = p.fn
}

// Hears declares that an I/O component hears the host's events: each event
// the core is handed, such as a session's PostToolUse, with its payload as the
// host gave it. Its outside thing is what the host reports.
func Hears(fn func(instance, event string, payload []byte) error) Part { return hearsPart{fn} }

type tellsPart struct {
	fn func(instance, event string, payload []byte) string
}

func (p tellsPart) declare(c *Component) {
	if c.tells != nil {
		c.fail("component %s tells the agent twice", c.name)
	}
	c.tells = p.fn
}

// Tells declares that an I/O component has a word for the agent on the host's
// events: after the event is heard, fn answers the text the hook hands the
// agent as context, or "" for none. The GitHub component tells the steps its
// relay waits for.
func Tells(fn func(instance, event string, payload []byte) string) Part { return tellsPart{fn} }

type guardsPart struct {
	fn func(instance, event string, payload []byte) string
}

func (p guardsPart) declare(c *Component) {
	if c.guards != nil {
		c.fail("component %s guards the host's events twice", c.name)
	}
	c.guards = p.fn
}

// Guards declares that an I/O component may refuse a host's event, such as a
// session's PreToolUse that writes a file it owns: after the event is heard,
// fn answers the reason the hook denies it with, which tells the agent what
// to do instead, or "" to let it pass. The files component refuses a write to
// a managed file, naming its own tool, and answers a search over a managed
// folder from its model as the reason, so the native tool does not run.
func Guards(fn func(instance, event string, payload []byte) string) Part { return guardsPart{fn} }

// Guard answers, for one instance of the component, the reason its guard
// denies a host's event with, "" where it passes or the component declares
// no guard: a fake core asks a component's guard so, as the index's Guard does.
func (c *Component) Guard(instance, event string, payload []byte) string {
	if c.guards == nil {
		return ""
	}
	return c.guards(instance, event, payload)
}

// inlets hands each switched-on instance whose component declares an inlet its commit.
func (ix *Index) inlets() error {
	var errs []error
	for _, in := range ix.order {
		if !in.enabled || in.comp.inlet == nil {
			continue
		}
		in := in
		commit := func(writes ...Write) error { return ix.Commit(in.name, writes...) }
		if err := in.comp.inlet(in.name, in.reader(ix.Snapshot(), ""), commit); err != nil {
			errs = append(errs, fmt.Errorf("index: instance %s (wiring line %d): %w", in.name, in.line, err))
		}
	}
	return errors.Join(errs...)
}

// Hear hands a host's event to every switched-on instance whose component
// hears them, in the instances' order, and answers what each refused. A
// switched-off instance hears nothing.
func (ix *Index) Hear(event string, payload []byte) error {
	var errs []error
	for _, in := range ix.order {
		if !in.enabled || in.comp.hears == nil {
			continue
		}
		if err := in.comp.hears(in.name, event, payload); err != nil {
			errs = append(errs, fmt.Errorf("index: instance %s hears %s: %w", in.name, event, err))
		}
	}
	return errors.Join(errs...)
}

// Tell answers what every switched-on instance whose component tells the
// agent has to say on a host's event, in the instances' order, each word on
// its own; an instance with nothing to say adds none.
func (ix *Index) Tell(event string, payload []byte) []string {
	var said []string
	for _, in := range ix.order {
		if !in.enabled || in.comp.tells == nil {
			continue
		}
		if text := in.comp.tells(in.name, event, payload); text != "" {
			said = append(said, text)
		}
	}
	return said
}

// Guard answers the reason the first switched-on instance whose component
// guards the host's events denies the event with, in the instances' order,
// and "" where every one lets it pass. A switched-off instance guards nothing.
func (ix *Index) Guard(event string, payload []byte) string {
	for _, in := range ix.order {
		if !in.enabled || in.comp.guards == nil {
			continue
		}
		if reason := in.comp.guards(in.name, event, payload); reason != "" {
			return reason
		}
	}
	return ""
}
