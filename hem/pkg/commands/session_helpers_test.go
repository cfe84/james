package commands

import (
	"testing"

	"james/moneypenny/pkg/envelope"
)

func TestParseCompactionThresholdTokensResponseTrustBoundary(t *testing.T) {
	for _, test := range []struct {
		name  string
		value interface{}
		want  int
		ok    bool
	}{
		{name: "minimum", value: float64(envelope.MinCompactionThresholdTokens), want: envelope.MinCompactionThresholdTokens, ok: true},
		{name: "maximum", value: float64(envelope.MaxCompactionThresholdTokens), want: envelope.MaxCompactionThresholdTokens, ok: true},
		{name: "below minimum", value: float64(envelope.MinCompactionThresholdTokens - 1)},
		{name: "above maximum", value: float64(envelope.MaxCompactionThresholdTokens + 1)},
		{name: "fraction", value: float64(envelope.MinCompactionThresholdTokens) + 0.5},
		{name: "wrong type", value: "150000"},
	} {
		t.Run(test.name, func(t *testing.T) {
			got, err := parseCompactionThresholdTokensResponse(test.value)
			if test.ok {
				if err != nil || got != test.want {
					t.Fatalf("parse = %d, %v; want %d", got, err, test.want)
				}
			} else if err == nil {
				t.Fatalf("parse(%v) succeeded, want error", test.value)
			}
		})
	}
}

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

func TestBuildCreateSessionDataIncludesCompactionThreshold(t *testing.T) {
	threshold := envelope.MinCompactionThresholdTokens
	data, err := buildCreateSessionData(&sessionParams{
		Agent:                     "claude",
		Path:                      ".",
		CompactionMode:            "custom",
		CompactionThresholdTokens: &threshold,
	}, "session-id", "prompt")
	if err != nil {
		t.Fatal(err)
	}
	if got, ok := data["compaction_threshold_tokens"].(int); !ok || got != threshold {
		t.Fatalf("compaction_threshold_tokens = %#v, want %d", data["compaction_threshold_tokens"], threshold)
	}
}
