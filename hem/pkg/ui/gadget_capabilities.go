package ui

import (
	"strconv"
	"strings"

	"james/moneypenny/pkg/envelope"
)

const gadgetNotificationHint = "  Gadget notifications to you are always available.\n"

// formViewport keeps the selected field visible, including multiline inputs.
func formViewport(rows []string, cursor, height int) string {
	if height < 1 {
		height = 1
	}
	var lines []string
	selectedStart, selectedEnd := 0, 0
	for i, row := range rows {
		if i == cursor {
			selectedStart = len(lines)
		}
		lines = append(lines, strings.Split(strings.TrimSuffix(row, "\n"), "\n")...)
		if i == cursor {
			selectedEnd = len(lines)
		}
	}
	start := max(0, selectedEnd-height)
	if selectedEnd-selectedStart > height {
		start = selectedStart
	}
	end := min(len(lines), start+height)
	return strings.Join(lines[start:end], "\n") + "\n"
}

func gadgetCapabilityFields(capabilities *envelope.GadgetCapabilities) []formField {
	c := envelope.DefaultGadgetCapabilities()
	if capabilities != nil {
		c = *capabilities
	}
	return []formField{
		{label: "Gadget: memory", flag: "--gadget-memory", isBool: true, explicitBool: true, value: strconv.FormatBool(c.Memory)},
		{label: "Gadget: own subagents / replies", flag: "--gadget-subagents", isBool: true, explicitBool: true, value: strconv.FormatBool(c.Subagents)},
		{label: "Gadget: discover / message agents", flag: "--gadget-agents", isBool: true, explicitBool: true, value: strconv.FormatBool(c.Agents)},
		{label: "Gadget: shared traits (future use)", flag: "--gadget-traits", isBool: true, explicitBool: true, value: strconv.FormatBool(c.Traits)},
		{label: "Gadget: scheduling", flag: "--gadget-scheduling", isBool: true, explicitBool: true, value: strconv.FormatBool(c.Scheduling)},
	}
}

func setGadgetCapabilityFields(fields []formField, capabilities *envelope.GadgetCapabilities) {
	for _, capability := range gadgetCapabilityFields(capabilities) {
		for i := range fields {
			if fields[i].flag == capability.flag {
				fields[i].value = capability.value
				break
			}
		}
	}
}
