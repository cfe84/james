package envelope

import (
	"encoding/json"
	"testing"
)

func TestCreateAgentTraits(t *testing.T) {
	for _, tc := range []struct {
		data    string
		present bool
		want    string
	}{
		{`{"prompt":"task"}`, false, ""},
		{`{"prompt":"task","traits":""}`, true, ""},
		{`{"prompt":"task","traits":"Clean code,test-id"}`, true, "Clean code,test-id"},
	} {
		got, err := DecodeCreateAgentGadget(json.RawMessage(tc.data))
		if err != nil || (got.Traits != nil) != tc.present {
			t.Fatalf("decode %s: %+v %v", tc.data, got, err)
		}
		if got, err := DecodeCreateAgentGadget(json.RawMessage(`{"prompt":"task","moneypenny":"chfeval-dev"}`)); err != nil || got.Moneypenny != "chfeval-dev" {
			t.Fatalf("moneypenny changed: %+v %v", got, err)
		}
		if got.Traits != nil && *got.Traits != tc.want {
			t.Fatalf("traits changed: %q", *got.Traits)
		}
	}
	for _, data := range []string{`{"prompt":"task","traits":[]}`, `{"prompt":"task","traits":42}`} {
		if _, err := DecodeCreateAgentGadget(json.RawMessage(data)); err == nil {
			t.Fatalf("invalid traits accepted: %s", data)
		}
	}
}

func TestTraitGadgetStrictData(t *testing.T) {
	for _, tc := range []struct{ method, data string }{
		{"traits.list", ""},
		{"traits.list", "{"},
		{"traits.list", "{} {}"},
		{"traits.list", "null"},
		{"traits.list", "[]"},
		{"traits.list", `{"id":"unexpected"}`},
		{"traits.get", `{"id":null}`},
		{"traits.get", `{"id":42}`},
		{"traits.get", `{"id":" "}`},
		{"traits.edit", `{"id":"id"}`},
		{"traits.edit", `{"id":"id","body":null}`},
		{"traits.edit", `{"id":"id","body":[]}`},
		{"traits.edit", `{"id":"id","body":"text","prompt":"forged"}`},
		{"traits.delete", `{"id":"id"}`},
	} {
		if _, err := DecodeTraitGadget(tc.method, json.RawMessage(tc.data)); err == nil {
			t.Errorf("accepted %s %s", tc.method, tc.data)
		}
	}
	got, err := DecodeTraitGadget("traits.edit", json.RawMessage(`{"id":"--default","body":""}`))
	if err != nil || got.ID != "--default" || got.Body == nil || *got.Body != "" {
		t.Fatalf("explicit empty replacement must work: %+v %v", got, err)
	}
}

func TestTraitCapabilityDefaultsAndSerialization(t *testing.T) {
	defaults := DefaultGadgetCapabilities()
	if defaults.Traits {
		t.Fatal("traits must be opt-in")
	}
	for _, enabled := range []bool{false, true} {
		defaults.Traits = enabled
		raw, err := json.Marshal(CreateSessionData{GadgetCapabilities: &defaults})
		if err != nil {
			t.Fatal(err)
		}
		var update UpdateSessionData
		if err := json.Unmarshal(raw, &update); err != nil || update.GadgetCapabilities == nil || *update.GadgetCapabilities != defaults {
			t.Fatalf("capabilities round trip: %s %v", raw, err)
		}
		var wire map[string]map[string]json.RawMessage
		capsRaw, _ := json.Marshal(map[string]any{"gadget_capabilities": defaults})
		if err := json.Unmarshal(capsRaw, &wire); err != nil || wire["gadget_capabilities"]["traits"] == nil {
			t.Fatalf("explicit traits=false must not be omitted: %s", capsRaw)
		}
	}
}
