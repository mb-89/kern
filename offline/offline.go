// Package offline cuts a test binary off the network, so kern's suite runs
// with the network cut (README.md "Tests"). Every package of kern with tests
// imports it in a test file for its effect alone, and
// kerntest/offline_test.go refuses one that does not.
//
// At init, in a test binary (testing.Testing) and outside a hot test's run
// (KERN_HOT, README.md "Hot tests"), it:
//
//   - drops the environment's tokens and the cloud session's mark (GH_TOKEN,
//     GITHUB_TOKEN, CLAUDE_CODE_REMOTE), so no code under test and no process
//     a test starts finds them;
//   - points every proxy variable at Closed, a port nothing listens on, and
//     names loopback alone as reached without it, so a request that honours
//     the environment's proxy is refused at once, in the binary and in every
//     process a test starts, and loopback, where the fakes listen, is reached
//     as before; and turns the go command's module proxy off (GOPROXY=off),
//     as a test's build reads the modules the suite's build fetched;
//   - makes the binary's resolver answer every host name outside the hosts
//     file with Refusal, so a dial by name reaches nothing, through a proxy
//     or without one.
//
// A dial to an address that is no loopback one, given as numbers, is not cut
// in process: kern names its outside services by name.
package offline

import (
	"context"
	"errors"
	"net"
	"os"
	"testing"
)

// Closed is the proxy every proxy variable names in a cut test binary: the
// discard port of loopback, on which nothing listens.
const Closed = "http://127.0.0.1:9"

// Loopback is what the cut binary's NO_PROXY reaches without the proxy.
const Loopback = "localhost,127.0.0.1,::1"

// Refusal is the error a host name's lookup answers in a cut test binary.
var Refusal = errors.New("kern's tests run with the network cut (package offline): no host name outside the hosts file resolves")

// Hot is the variable that runs a hot test against the real service, and
// leaves the network whole (README.md "Hot tests").
const Hot = "KERN_HOT"

// dropped are the variables a cut binary unsets.
var dropped = []string{"GH_TOKEN", "GITHUB_TOKEN", "CLAUDE_CODE_REMOTE"}

// proxies are the variables a cut binary points at Closed.
var proxies = []string{"HTTPS_PROXY", "https_proxy", "HTTP_PROXY", "http_proxy", "ALL_PROXY", "all_proxy"}

func init() {
	if testing.Testing() && os.Getenv(Hot) == "" {
		Cut()
	}
}

// Cut cuts the process off the network as the init does in a test binary.
func Cut() {
	for _, k := range dropped {
		os.Unsetenv(k)
	}
	for _, k := range proxies {
		os.Setenv(k, Closed)
	}
	os.Setenv("NO_PROXY", Loopback)
	os.Setenv("no_proxy", Loopback)
	os.Setenv("GOPROXY", "off")
	net.DefaultResolver = &net.Resolver{PreferGo: true, Dial: func(context.Context, string, string) (net.Conn, error) {
		return nil, Refusal
	}}
}
