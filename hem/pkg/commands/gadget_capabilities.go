package commands

import (
	"encoding/json"
	"flag"
	"fmt"

	"james/moneypenny/pkg/envelope"
)

type gadgetCapabilityFlags map[string]bool

func (g *gadgetCapabilityFlags) register(fs *flag.FlagSet) {
	for _, name := range []string{"memory", "subagents", "agents", "create-agents", "edit-sessions", "edit-own-session", "edit-own-subagents", "traits", "scheduling"} {
		name := name
		fs.Func("gadget-"+name, "allow gadget "+name+" (true/false)", func(value string) error {
			if value != "true" && value != "false" {
				return fmt.Errorf("--gadget-%s must be true or false", name)
			}
			if *g == nil {
				*g = make(gadgetCapabilityFlags)
			}
			(*g)[name] = value == "true"
			return nil
		})
	}
}

func (g gadgetCapabilityFlags) apply(base *envelope.GadgetCapabilities) *envelope.GadgetCapabilities {
	if base == nil && len(g) == 0 {
		return nil
	}
	result := envelope.DefaultGadgetCapabilities()
	if base != nil {
		result = *base
	}
	for name, value := range g {
		switch name {
		case "memory":
			result.Memory = value
		case "subagents":
			result.Subagents = value
		case "agents":
			result.Agents = value
		case "create-agents":
			result.CreateAgents = value
		case "edit-sessions":
			result.EditSessions = value
		case "edit-own-session":
			result.EditOwnSession = value
		case "edit-own-subagents":
			result.EditOwnSubagents = value
		case "traits":
			result.Traits = value
		case "scheduling":
			result.Scheduling = value
		}
	}
	return &result
}

func sessionGadgetCapabilities(data json.RawMessage) (envelope.GadgetCapabilities, error) {
	var detail struct {
		GadgetCapabilities *envelope.GadgetCapabilities `json:"gadget_capabilities"`
	}
	if err := json.Unmarshal(data, &detail); err != nil {
		return envelope.GadgetCapabilities{}, err
	}
	if detail.GadgetCapabilities == nil {
		return envelope.DefaultGadgetCapabilities(), nil
	}
	return *detail.GadgetCapabilities, nil
}
