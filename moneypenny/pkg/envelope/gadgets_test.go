package envelope

import (
	"encoding/json"
	"testing"
)

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
