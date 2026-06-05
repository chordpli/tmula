package main

import "testing"

func TestRunVersion(t *testing.T) {
	if err := run([]string{"--version"}); err != nil {
		t.Fatalf("run --version returned error: %v", err)
	}
}

func TestRunInvalidRole(t *testing.T) {
	if err := run([]string{"--role", "bogus"}); err == nil {
		t.Fatal("expected error for invalid role, got nil")
	}
}
