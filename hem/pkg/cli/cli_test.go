package cli

import (
	"strings"
	"testing"
)

func TestParseAddMoneypenny(t *testing.T) {
	cmd, err := Parse([]string{"add", "moneypenny", "-n", "local"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if cmd.Verb != "add" {
		t.Errorf("verb = %q, want %q", cmd.Verb, "add")
	}
	if cmd.Noun != "moneypenny" {
		t.Errorf("noun = %q, want %q", cmd.Noun, "moneypenny")
	}
	if len(cmd.Args) != 2 || cmd.Args[0] != "-n" || cmd.Args[1] != "local" {
		t.Errorf("args = %v, want [-n local]", cmd.Args)
	}
	if cmd.OutputType != "text" {
		t.Errorf("outputType = %q, want %q", cmd.OutputType, "text")
	}
}

func TestParseListMoneypenniesNormalized(t *testing.T) {
	cmd, err := Parse([]string{"list", "moneypennies"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if cmd.Verb != "list" {
		t.Errorf("verb = %q, want %q", cmd.Verb, "list")
	}
	if cmd.Noun != "moneypenny" {
		t.Errorf("noun = %q, want %q", cmd.Noun, "moneypenny")
	}
}

func TestParseCreateSession(t *testing.T) {
	cmd, err := Parse([]string{"create", "session", "-m", "foo"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if cmd.Verb != "create" {
		t.Errorf("verb = %q, want %q", cmd.Verb, "create")
	}
	if cmd.Noun != "session" {
		t.Errorf("noun = %q, want %q", cmd.Noun, "session")
	}
	if len(cmd.Args) != 2 || cmd.Args[0] != "-m" || cmd.Args[1] != "foo" {
		t.Errorf("args = %v, want [-m foo]", cmd.Args)
	}
}

func TestParseRemoveAlias(t *testing.T) {
	cmd, err := Parse([]string{"remove", "moneypenny", "-n", "x"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if cmd.Verb != "delete" {
		t.Errorf("verb = %q, want %q (alias of remove)", cmd.Verb, "delete")
	}
	if cmd.Noun != "moneypenny" {
		t.Errorf("noun = %q, want %q", cmd.Noun, "moneypenny")
	}
}

func TestParseOutputTypeShortFlag(t *testing.T) {
	cmd, err := Parse([]string{"list", "sessions", "-o", "json"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if cmd.OutputType != "json" {
		t.Errorf("outputType = %q, want %q", cmd.OutputType, "json")
	}
	if cmd.Verb != "list" {
		t.Errorf("verb = %q, want %q", cmd.Verb, "list")
	}
	if cmd.Noun != "session" {
		t.Errorf("noun = %q, want %q", cmd.Noun, "session")
	}
}

func TestParseOutputTypeLongFlagBeforeVerb(t *testing.T) {
	cmd, err := Parse([]string{"--output-type", "tsv", "list", "sessions"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if cmd.OutputType != "tsv" {
		t.Errorf("outputType = %q, want %q", cmd.OutputType, "tsv")
	}
	if cmd.Verb != "list" {
		t.Errorf("verb = %q, want %q", cmd.Verb, "list")
	}
	if cmd.Noun != "session" {
		t.Errorf("noun = %q, want %q", cmd.Noun, "session")
	}
}

func TestParseShowPublicKeyNoNoun(t *testing.T) {
	cmd, err := Parse([]string{"show-public-key"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if cmd.Verb != "show-public-key" {
		t.Errorf("verb = %q, want %q", cmd.Verb, "show-public-key")
	}
	if cmd.Noun != "" {
		t.Errorf("noun = %q, want empty", cmd.Noun)
	}
}

func TestParseMissingVerbReturnsError(t *testing.T) {
	_, err := Parse([]string{})
	if err == nil {
		t.Fatal("expected error for missing verb, got nil")
	}
	if !strings.Contains(err.Error(), "missing verb") {
		t.Errorf("error = %q, want it to contain 'missing verb'", err.Error())
	}
}
