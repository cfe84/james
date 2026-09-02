package agent

import "testing"

func TestWithEnvironmentReplacesInheritedValue(t *testing.T) {
	env := withEnvironment(
		[]string{"PATH=/bin", "PLAYWRIGHT_MCP_EXTENSION_TOKEN=old", "OTHER=value"},
		map[string]string{"PLAYWRIGHT_MCP_EXTENSION_TOKEN": "new"},
	)
	want := map[string]string{
		"PATH":                           "/bin",
		"PLAYWRIGHT_MCP_EXTENSION_TOKEN": "new",
		"OTHER":                          "value",
	}
	got := make(map[string]string, len(env))
	for _, item := range env {
		for i := range item {
			if item[i] == '=' {
				got[item[:i]] = item[i+1:]
				break
			}
		}
	}
	for name, value := range want {
		if got[name] != value {
			t.Errorf("%s = %q, want %q", name, got[name], value)
		}
	}
	if len(got) != len(want) {
		t.Errorf("environment length = %d, want %d", len(got), len(want))
	}
}
