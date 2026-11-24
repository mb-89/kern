package github_test

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"kern/components/github"
	"kern/host"
	"kern/index"
)

// An I/O module's zero value does no I/O (README.md "Its options"): the
// component built from no options finds no token, probes no
// proxy and calls no API, though the environment names a token, a proxy and a
// cloud session and the proxy answers for its api, so it takes the relay. A read
// fails at once through the relay (relay-wait 0s), its time the still clock's.
func TestTheZeroValueMakesNoNetworkCall(t *testing.T) {
	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		calls.Add(1)
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()
	for k, v := range map[string]string{"GH_TOKEN": "from-the-environment", "GITHUB_TOKEN": "from-the-environment",
		"HTTPS_PROXY": srv.URL, "HTTP_PROXY": srv.URL, "CLAUDE_CODE_REMOTE": "true"} {
		t.Setenv(k, v)
	}
	g := github.New(github.Options{})
	ix, err := index.Start(wiring(t, `api = "`+api+`"`), g.Component, reader)
	if err != nil {
		t.Fatal(err)
	}
	if v, _ := ix.Read("github/backend"); v.V != github.Relay {
		t.Fatalf("the zero value's backend is %v, and wants the relay", v.V)
	}
	if _, err := ix.Call("read", "issue", "alice", "demo", "7"); err == nil || !strings.Contains(err.Error(), "asked through the relay") {
		t.Fatalf("the zero value's read answers %v, and wants the relay's failure", err)
	}
	if v, _ := ix.Read("github/outbox/issue.alice.demo.7"); !v.V.(github.Step).Asked.Equal(host.Still{}.Now()) {
		t.Fatalf("the outbox step was asked at %v, and wants the still clock's time", v.V.(github.Step).Asked)
	}
	if n := calls.Load(); n != 0 {
		t.Fatalf("the zero value made %d network calls, and wants none", n)
	}
}
