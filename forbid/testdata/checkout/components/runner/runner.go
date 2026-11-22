// Package runner reaches GitHub itself, past the GitHub component.
package runner

import (
	"os/exec"

	"github.com/google/go-github/v60/github"
)

var _ = github.NewClient

const api = "https://api.github.com/repos"

func token() ([]byte, error) {
	return exec.Command("gh", "auth", "token").Output()
}

func shell() *exec.Cmd { return exec.Command("sh", "-c", "gh issue list") }

// Neither is a use: a word that holds gh, and a host that only holds the name.
var _ = []string{"ghost", "notapi.github.com", "api.github.community"}
