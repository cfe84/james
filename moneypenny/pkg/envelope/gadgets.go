package envelope

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"strings"
)

type CreateAgentGadgetData struct {
	Prompt     string  `json:"prompt"`
	Name       string  `json:"name"`
	Agent      string  `json:"agent"`
	Model      string  `json:"model"`
	Path       string  `json:"path"`
	Moneypenny string  `json:"moneypenny"`
	Traits     *string `json:"traits,omitempty"`
}

func DecodeCreateAgentGadget(data json.RawMessage) (CreateAgentGadgetData, error) {
	var result CreateAgentGadgetData
	data = bytes.TrimSpace(data)
	if len(data) == 0 || data[0] != '{' {
		return result, fmt.Errorf("agent creation data must be a JSON object")
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&result); err != nil {
		return result, err
	}
	if err := decoder.Decode(new(any)); err != io.EOF {
		return result, fmt.Errorf("agent creation data must contain one JSON object")
	}
	if strings.TrimSpace(result.Prompt) == "" {
		return result, fmt.Errorf("agent prompt is required")
	}
	return result, nil
}

// TraitGadgetData is the narrow shared-definition API, not trait management.
type TraitGadgetData struct {
	ID   string  `json:"id"`
	Body *string `json:"body"`
}

func DecodeTraitGadget(method string, data json.RawMessage) (TraitGadgetData, error) {
	var result TraitGadgetData
	var get struct {
		ID string `json:"id"`
	}
	var target any
	switch method {
	case "traits.list":
		target = &struct{}{}
	case "traits.get":
		target = &get
	case "traits.edit":
		target = &result
	default:
		return result, fmt.Errorf("unsupported trait gadget method")
	}
	data = bytes.TrimSpace(data)
	if len(data) == 0 || data[0] != '{' {
		return result, fmt.Errorf("trait gadget data must be a JSON object")
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		return result, err
	}
	if err := decoder.Decode(new(any)); err != io.EOF {
		return result, fmt.Errorf("trait gadget data must contain one JSON object")
	}
	if method == "traits.get" {
		result.ID = get.ID
	}
	if method != "traits.list" && strings.TrimSpace(result.ID) == "" {
		return result, fmt.Errorf("trait ID or name is required")
	}
	if method == "traits.edit" && result.Body == nil {
		return result, fmt.Errorf("complete trait body is required")
	}
	return result, nil
}
