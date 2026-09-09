package commands

import (
	"strings"
	"testing"
)

func TestGadgetsPromptIsDaemonManaged(t *testing.T) {
	for _, parent := range []string{"", "parent"} {
		prompt := gadgetsSystemPrompt("relay.example:443/control", "SHA256:trusted", "child", parent)
		if !strings.HasPrefix(prompt, gadgetsMarker) || !strings.Contains(prompt, "daemon-managed") {
			t.Fatal("missing daemon-managed marker")
		}
		for _, forbidden := range []string{"hem --hem", "SHA256:", "relay.example", "delete", "stop", "--yolo", "schedule session"} {
			if strings.Contains(prompt, forbidden) {
				t.Fatalf("prompt advertises routing or operator command: %s", forbidden)
			}
		}
	}
}

func TestLocalGadgetsPrompt(t *testing.T) {
	prompt := gadgetsSystemPrompt("", "", "local", "")
	if strings.Contains(prompt, "--mi6-server-fingerprint") || strings.Contains(prompt, "--hem") {
		t.Fatal("local prompt contains MI6 flags")
	}

	if !strings.Contains(prompt, "Moneypenny at runtime") {
		t.Fatal("missing runtime notice")
	}
}

func TestGadgetEnvironment(t *testing.T) {
	e := &Executor{MI6Control: "relay/control", MI6ServerFingerprint: "SHA256:trusted"}
	got := e.addGadgetEnvironment(map[string]string{
		"FEATURE_FLAG":          "true",
		"JAMES_HEM_FINGERPRINT": "SHA256:untrusted",
		"JAMES_HEM_SOCKET":      "untrusted.sock",
	})
	if got["JAMES_HEM_FINGERPRINT"] != "SHA256:trusted" || got["JAMES_HEM_ADDRESS"] != "relay/control" || got["JAMES_HEM_SOCKET"] != "" {
		t.Fatalf("unexpected remote route: %v", got)
	}
	if got["FEATURE_FLAG"] != "true" {
		t.Fatal("existing environment was not preserved")
	}
	values := environmentValues{"JAMES_HEM_FINGERPRINT=SHA256:untrusted", "JAMES_HEM_SOCKET=untrusted.sock"}
	e.addGadgetEnvironmentValues(&values)
	parsed, err := values.Map()
	if err != nil {
		t.Fatal(err)
	}
	if parsed["JAMES_HEM_FINGERPRINT"] != "SHA256:trusted" || parsed["JAMES_HEM_SOCKET"] != "" {
		t.Fatalf("unexpected create route: %v", parsed)
	}
	e.MI6Control = ""
	e.HemSocket = "/configured/hem.sock"
	got = e.addGadgetEnvironment(got)
	if got["JAMES_HEM_SOCKET"] != e.HemSocket || got["JAMES_HEM_ADDRESS"] != "" || got["JAMES_HEM_FINGERPRINT"] != "" {
		t.Fatalf("unexpected local route: %v", got)
	}
}

func TestReplaceGadgetsPrompt(t *testing.T) {
	fresh := gadgetsSystemPrompt("relay.example:443/control", "SHA256:new", "child", "parent")
	base := "nick and base instructions\ntraits"
	stale := gadgetsMarker + " hem --hem relay.example:443/control command.\nLegacy instructions\n"
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
