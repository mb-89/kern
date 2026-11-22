package kerntest

import (
	"errors"
	"fmt"
	"sort"
	"strings"

	"kern/index"
)

// Check is the completeness check over a wiring: among the components the
// wiring loads (its switched-on instances), every expectation of a loaded
// component is claimed by exactly one loaded component, and met by it. It
// answers each expectation that is unclaimed, claimed twice or unmet, each
// with its own message naming the name, the expectation and the reader's
// file. Two components that claim one name pass where no wiring loads both.
// mods are the components the check may find loaded; one the wiring does not
// load is left out, its expectations and its claims with it.
func Check(w index.Wiring, mods ...Module) error {
	loaded := map[string]bool{}
	for _, in := range w.Instances {
		if in.Enabled {
			loaded[in.Component] = true
		}
	}
	byName := map[string]Module{}
	for _, m := range mods {
		n := m.Component.Name()
		if _, dup := byName[n]; dup {
			return fmt.Errorf("the check takes component %s twice", n)
		}
		if loaded[n] {
			byName[n] = m
		}
	}
	type claimer struct {
		m  Module
		cl Claim
	}
	pool := Pool{}
	claims := map[string][]claimer{}
	for _, n := range sortedKeys(byName) {
		m := byName[n]
		x, err := m.read()
		if err != nil {
			return err
		}
		pool.add(x)
		for _, cl := range x.Claims {
			claims[cl.Name] = append(claims[cl.Name], claimer{m, cl})
		}
	}
	var all []Expectation
	for _, es := range pool {
		all = append(all, es...)
	}
	sort.Slice(all, func(i, j int) bool {
		if all[i].File != all[j].File {
			return all[i].File < all[j].File
		}
		return all[i].N < all[j].N
	})
	var errs []error
	for _, e := range all {
		cs := claims[e.Name]
		switch len(cs) {
		case 0:
			errs = append(errs, fmt.Errorf("unclaimed: name %q: no loaded component claims %s of %s", e.Name, e, e.File))
		case 1:
			if err := cs[0].m.meet(cs[0].cl, e); err != nil {
				errs = append(errs, unmet(cs[0].m, e, err))
			}
		default:
			names := make([]string, len(cs))
			for i, c := range cs {
				names[i] = c.m.Component.Name()
			}
			errs = append(errs, fmt.Errorf("claimed twice: name %q: %s, all loaded, claim %s of %s: exactly one may",
				e.Name, strings.Join(names, " and "), e, e.File))
		}
	}
	return errors.Join(errs...)
}
