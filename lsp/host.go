package lsp

import (
	"github.com/nats-io/nats.go"
	"kern/bus"
	"kern/host"
	"kern/mcp"
)

// Host is the real core in the checkout at Root, reached as the MCP door
// reaches it (mcp.Host), started as `<Bin> serve` with Args where none runs.
type Host struct {
	mcp.Host
}

// Watch subscribes to the name's pushes on the core's bus (val.<name>),
// which the core publishes on each change, and calls changed on each.
func (h Host) Watch(name string, changed func()) (func(), error) {
	s, err := host.ReadStanding(h.Root)
	if err != nil {
		return nil, err
	}
	nc, err := bus.Dial(s.Bus, bus.Client, s.Users[bus.Client], nats.Timeout(host.HealthWait))
	if err != nil {
		return nil, err
	}
	if _, err := nc.Subscribe(bus.Val+bus.Subject(name), func(*nats.Msg) { changed() }); err != nil {
		nc.Close()
		return nil, err
	}
	if err := nc.Flush(); err != nil {
		nc.Close()
		return nil, err
	}
	return nc.Close, nil
}
