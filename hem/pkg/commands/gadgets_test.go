package commands

import "testing"

func TestGadgetEnvironment(t *testing.T) {
	e := &Executor{MI6Control: "relay/control", MI6ServerFingerprint: "SHA256:trusted"}
	got := e.addGadgetEnvironment(map[string]string{
		"FEATURE_FLAG":          "true",
		"JAMES_HEM_FINGERPRINT": "SHA256:untrusted",
		"JAMES_HEM_SOCKET":      "untrusted.sock",
	})
	if got["JAMES_HEM_FINGERPRINT"] != "" || got["JAMES_HEM_ADDRESS"] != "" || got["JAMES_HEM_SOCKET"] != "" {
		t.Fatalf("operator route leaked into agent environment: %v", got)
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
	if parsed["JAMES_HEM_FINGERPRINT"] != "" || parsed["JAMES_HEM_SOCKET"] != "" {
		t.Fatalf("operator route leaked into create environment: %v", parsed)
	}
	route := e.gadgetRoute()
	if route["JAMES_HEM_ADDRESS"] != "relay/control" || route["JAMES_HEM_FINGERPRINT"] != "SHA256:trusted" {
		t.Fatalf("missing internal remote route: %v", route)
	}
	e.MI6Control, e.HemSocket = "", "/configured/hem.sock"
	if route = e.gadgetRoute(); route["JAMES_HEM_SOCKET"] != e.HemSocket {
		t.Fatalf("missing internal local route: %v", route)
	}
}
