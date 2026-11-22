package kerntest

import (
	"errors"
	"fmt"
	"strings"

	"kern/index"
)

// Pool is the expectations of every reader whose file it read, by the name
// each pools under: no key names a component.
type Pool map[string][]Expectation

// ReadPool reads the expectation files at paths into one pool, each against
// the base BaseVariable names, if any: an expectation that base does not hold
// is new, and pending until its claimer meets it.
func ReadPool(paths ...string) (Pool, error) {
	return ReadPoolSince(environmentBase(), paths...)
}

// ReadPoolSince is ReadPool against base; a nil base holds every expectation.
func ReadPoolSince(base Base, paths ...string) (Pool, error) {
	p := Pool{}
	for _, path := range paths {
		x, err := ReadExpectations(path)
		if err != nil {
			return nil, err
		}
		if x, err = since(base, x); err != nil {
			return nil, err
		}
		p.add(x)
	}
	return p, nil
}

func (p Pool) add(x Expectations) {
	for _, e := range x.Expect {
		p[e.Name] = append(p[e.Name], e)
	}
}

// Setup sets up one given precondition in the claimer's own fake, as its own
// tests set its state up: it seeds the claimer's inputs and config, runs its
// commands, or prepares what its verbs reach.
type Setup func(f *Fake) error

// Module is a component as the pool sees it: its expectation file, which
// holds what it expects and what it claims, and the setup of each given
// precondition of an expectation it claims.
type Module struct {
	Component *index.Component
	// File is its expectation file; "" where it expects and claims nothing.
	File  string
	Given map[string]Setup
}

func (m Module) read() (Expectations, error) {
	if m.File == "" {
		return Expectations{}, nil
	}
	x, err := ReadExpectations(m.File)
	if err != nil {
		return Expectations{}, err
	}
	return x, x.Check(m.Component)
}

// Meet holds m to every expectation in pool on the names it claims: each in a
// fake of its own, after the setup of its given. It fails on each one m does
// not meet, naming the name, the expectation and the reader's file, except a
// new one, which is pending: it answers those apart, for its test to report,
// and appends them to ReportVariable's file, where it is set.
func (m Module) Meet(pool Pool) ([]Pending, error) {
	x, err := m.read()
	if err != nil {
		return nil, err
	}
	var pending []Pending
	var errs []error
	for _, cl := range x.Claims {
		for _, e := range pool[cl.Name] {
			err := m.meet(cl, e)
			switch {
			case err == nil:
			case e.New:
				p := Pending{Expectation: e, Claimer: m.Component.Name(), Why: err}
				pending = append(pending, p)
				errs = append(errs, report(p))
			default:
				errs = append(errs, unmet(m, e, err))
			}
		}
	}
	return pending, errors.Join(errs...)
}

func unmet(m Module, e Expectation, err error) error {
	return fmt.Errorf("unmet: name %q: %s does not meet %s of %s: %w", e.Name, m.Component.Name(), e, e.File, err)
}

// meet runs one expectation against the claim that claims it.
func (m Module) meet(cl Claim, e Expectation) error {
	f, err := New(m.Component)
	if err != nil {
		return err
	}
	if e.Given != "" {
		setup, ok := m.Given[e.Given]
		if !ok {
			return fmt.Errorf("it sets up no given %q", e.Given)
		}
		if err := setup(f); err != nil {
			return fmt.Errorf("setting up given %q: %w", e.Given, err)
		}
	}
	if e.Port != "" {
		if cl.Port == "" {
			return fmt.Errorf("it claims the name with verb %s, and the expectation is a value read", cl.Verb)
		}
		got, err := f.written(cl.Port)
		if err != nil {
			return err
		}
		return Match("value", e.Value, got, e.Exact)
	}
	if cl.Verb == "" {
		return fmt.Errorf("it claims the name on output %s, and the expectation is a request", cl.Port)
	}
	var verb index.VerbDecl
	for _, v := range m.Component.Verbs() {
		if v.Verb == cl.Verb {
			verb = v
		}
	}
	ask, err := convert(e.Ask, verb.Req)
	if err != nil {
		return fmt.Errorf("its verb %s cannot take the ask: %w", cl.Verb, err)
	}
	ans, err := f.BeginServe(cl.Verb, ask).Wait()
	switch {
	case e.Refused && err == nil:
		return fmt.Errorf("its verb %s answers %s, and the expectation is a refusal", cl.Verb, show(ans))
	case e.Refused:
		return nil
	case err != nil:
		return fmt.Errorf("its verb %s refuses: %w", cl.Verb, err)
	}
	return Match("answer", e.Answer, ans, e.Exact)
}

// written answers what output port reads once the setup ran: a derived
// output derived on the fake's inputs, a family as every key it holds.
func (f *Fake) written(port string) (any, error) {
	p, ok := f.outputs[port]
	if !ok {
		return nil, fmt.Errorf("component %s writes no output %q", f.c.Name(), port)
	}
	if p.Derived {
		if err := f.Derive(port); err != nil {
			return nil, err
		}
	}
	if !p.Family {
		v, err := f.Out(port)
		return v.V, err
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	keys := map[string]any{}
	for at, v := range f.out {
		if key, ok := strings.CutPrefix(at, port+"/"); ok {
			keys[key] = v
		}
	}
	return keys, nil
}
