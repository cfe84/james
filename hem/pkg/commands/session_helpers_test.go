package commands

import "testing"

func TestEnvironmentValuesMap(t *testing.T) {
	values := environmentValues{"PLAYWRIGHT_MCP_EXTENSION_TOKEN=value", "FEATURE_FLAG=true"}
	got, err := values.Map()
	if err != nil {
		t.Fatal(err)
	}
	if got["PLAYWRIGHT_MCP_EXTENSION_TOKEN"] != "value" || got["FEATURE_FLAG"] != "true" {
		t.Fatalf("unexpected environment: %#v", got)
	}
}

func TestEnvironmentValuesRejectInvalidName(t *testing.T) {
	if _, err := (environmentValues{"NOT-VALID=value"}).Map(); err == nil {
		t.Fatal("expected an invalid name error")
	}
}
