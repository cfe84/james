package commands

import (
	"strings"
	"testing"
)

func TestGadgetsCommandsIncludeFingerprint(t *testing.T) {
	const prefix = "hem --hem relay.example:443/control --mi6-server-fingerprint SHA256:trusted"
	for _, parent := range []string{"", "parent"} {
		prompt := gadgetsSystemPrompt("relay.example:443/control", "SHA256:trusted", "child", parent)
		if !strings.Contains(prompt, "MI6 relay server fingerprint: SHA256:trusted") {
			t.Fatal("missing explicit fingerprint")
		}
		for _, line := range strings.Split(prompt, "\n") {
			// Every executable example must retain the complete connection prefix.
			if strings.Contains(line, "hem --hem") &&
				strings.Count(line, "hem --hem") != strings.Count(line, prefix) {
				t.Fatalf("command is missing its fingerprint: %s", line)
			}
		}
		if parent != "" && !strings.Contains(prompt, prefix+" callback session parent --from child") {
			t.Fatal("callback is missing its connection flags or provenance")
		}
	}
}

func TestLocalGadgetsPrompt(t *testing.T) {
	prompt := gadgetsSystemPrompt("", "", "local", "")
	if strings.Contains(prompt, "--mi6-server-fingerprint") || strings.Contains(prompt, "--hem") {
		t.Fatal("local prompt contains MI6 flags")
	}
	if !strings.Contains(prompt, "hem schedule session local") {
		t.Fatal("missing local command")
	}
}

func TestGadgetEnvironment(t *testing.T) {
	e := &Executor{MI6Control: "relay/control", MI6ServerFingerprint: "SHA256:trusted"}
	got := e.addGadgetEnvironment(map[string]string{
		"FEATURE_FLAG":           "true",
		"MI6_SERVER_FINGERPRINT": "SHA256:untrusted",
	})
	if got["MI6_SERVER_FINGERPRINT"] != "SHA256:trusted" {
		t.Fatalf("fingerprint = %q, want trusted relay pin", got["MI6_SERVER_FINGERPRINT"])
	}
	if got["FEATURE_FLAG"] != "true" {
		t.Fatal("existing environment was not preserved")
	}
	values := environmentValues{"MI6_SERVER_FINGERPRINT=SHA256:untrusted"}
	e.addGadgetEnvironmentValues(&values)
	parsed, err := values.Map()
	if err != nil {
		t.Fatal(err)
	}
	if parsed["MI6_SERVER_FINGERPRINT"] != "SHA256:trusted" {
		t.Fatalf("create fingerprint = %q, want trusted relay pin", parsed["MI6_SERVER_FINGERPRINT"])
	}
}

func TestReplaceGadgetsPrompt(t *testing.T) {
	fresh := gadgetsSystemPrompt("relay.example:443/control", "SHA256:new", "child", "parent")
	base := "nick and base instructions\ntraits"
	stale := gadgetsSystemPrompt("relay.example:443/control", "SHA256:old", "child", "parent")
	for _, memory := range []string{"", memoryMarker + "\nkeep memory", memoryMarkerLegacy + "\nkeep memory"} {
		for _, old := range []string{"", stale} {
			got := replaceGadgetsPrompt(base+old+memory, fresh)
			if got != base+fresh+memory {
				t.Fatal("refresh did not preserve base/traits/memory or replace old gadgets")
			}
			if again := replaceGadgetsPrompt(got, fresh); again != got {
				t.Fatal("refresh is not idempotent")
			}
			if removed := replaceGadgetsPrompt(got, ""); removed != base+memory {
				t.Fatal("disabling gadgets did not preserve base/traits/memory")
			}
		}
	}
	if got := replaceGadgetsPrompt(strings.TrimPrefix(stale, "\n"), fresh); got != fresh {
		t.Fatal("failed to refresh a gadgets-only prompt without leading newline")
	}
}
