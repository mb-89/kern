// Package github is the owner of gh: its own code runs it, but reads no time.
package github

import (
	"os/exec"
	"time"
)

func token() ([]byte, error) { return exec.Command("gh", "auth", "token").Output() }

func since(t time.Time) time.Duration { return time.Now().Sub(t) }
