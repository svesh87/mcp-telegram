package main

import (
	"strings"
	"testing"
)

func TestVersionFlag(t *testing.T) {
	if err := run([]string{"--version"}); err != nil {
		t.Errorf("--version: %v", err)
	}
}

func TestUnknownFlag(t *testing.T) {
	if err := run([]string{"--what-is-this"}); err == nil {
		t.Error("an unknown flag was accepted")
	}
}

// Identities are named on purpose rather than guessed from whichever credentials happen
// to be in the environment: a server that decides for itself which account it acts as is
// a server nobody can reason about.
func TestIdentitiesAreRequired(t *testing.T) {
	err := run(nil)
	if err == nil {
		t.Fatal("the server started without being told which identities to run")
	}
	if !strings.Contains(err.Error(), "--identities") {
		t.Errorf("the refusal does not say what is missing: %v", err)
	}
}

func TestLoginNeedsCredentials(t *testing.T) {
	// The login command shares the configuration with the server, so it refuses for the
	// same reasons: without the application credentials there is nothing to sign in with.
	if err := run([]string{"login", "--session-dir", t.TempDir()}); err == nil {
		t.Error("a login was attempted with no credentials at all")
	}

	if err := run([]string{"login", "--nonsense"}); err == nil {
		t.Error("an unknown flag was accepted")
	}
}
