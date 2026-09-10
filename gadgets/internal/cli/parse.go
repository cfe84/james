package cli

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strconv"
	"strings"
)

func command(args []string) (string, []string, error) {
	if len(args) == 0 {
		return "", nil, errors.New("expected a command; run gadgets help")
	}
	if args[0] == "notify" {
		return "notify", args[1:], nil
	}
	if len(args) < 2 {
		return "", nil, errors.New("expected a subcommand; run gadgets help")
	}
	method := args[0] + "." + args[1]
	switch method {
	case "memory.get", "memory.list", "memory.search", "memory.set", "memory.batch", "memory.delete", "memory.revisions",
		"agents.list", "agents.message", "subagents.list", "subagents.create", "subagents.message",
		"traits.list", "traits.get", "traits.edit",
		"schedule.list", "schedule.create", "schedule.delete":
		return method, args[2:], nil
	}
	return "", nil, fmt.Errorf("unknown command %q; run gadgets help", strings.Join(args[:2], " "))
}

func Parse(args []string, stdin io.Reader) (Request, error) {
	method, rest, err := command(args)
	if err != nil {
		return Request{}, err
	}
	allowed := map[string]bool{}
	switch method {
	case "memory.get":
		allowed["offset"], allowed["limit"] = true, true
	case "memory.set", "agents.message", "subagents.message":
		allowed["body"] = true
	case "memory.delete":
		allowed["recursive"] = true
	case "subagents.create":
		for _, key := range []string{"name", "agent", "model", "path"} {
			allowed[key] = true
		}
	case "schedule.create":
		for _, key := range []string{"cron", "at", "prompt"} {
			allowed[key] = true
		}
	}
	flags, pos, err := parseFlags(rest, allowed)
	if err != nil {
		return Request{}, err
	}
	data := map[string]any{}
	for key, value := range flags {
		data[key] = value
		if key == "offset" || key == "limit" {
			n, err := strconv.Atoi(value)
			if err != nil || n < 0 || (key == "limit" && (n == 0 || n > 64000)) {
				return Request{}, fmt.Errorf("--%s must be a nonnegative integer; limit must be 1–64000", key)
			}
			data[key] = n
		}
	}
	arity := func(min, max int) error {
		if len(pos) < min || len(pos) > max {
			return fmt.Errorf("%s expects %d–%d positional arguments; run gadgets help", method, min, max)
		}
		return nil
	}
	body := func(key string, nonempty bool) error {
		value, present := flags[key]
		if !present {
			b, err := readBounded(stdin, MaxRequestBytes)
			if err != nil {
				return fmt.Errorf("cannot read stdin (maximum 1 MiB): %w", err)
			}
			value = string(b)
		}
		if nonempty && strings.TrimSpace(value) == "" {
			return fmt.Errorf("%s requires nonempty %s", method, key)
		}
		data[key] = value
		return nil
	}
	switch method {
	case "memory.get", "memory.list", "memory.revisions", "memory.set":
		if err = arity(0, 1); err == nil {
			data["path"] = ""
			if len(pos) == 1 {
				data["path"] = pos[0]
			}
			if method == "memory.set" {
				err = body("body", false)
			}
		}
	case "memory.search":
		if err = arity(1, 1); err == nil {
			data["query"] = pos[0]
		}
	case "memory.delete":
		if err = arity(1, 1); err == nil {
			data["path"] = pos[0]
			data["recursive"] = flags["recursive"] == "true"
		}
	case "memory.batch":
		if err = arity(0, 0); err == nil {
			var b []byte
			b, err = readBounded(stdin, MaxRequestBytes)
			if err == nil {
				var entries []map[string]json.RawMessage
				err = json.Unmarshal(b, &entries)
				if err == nil && entries == nil {
					err = errors.New("batch input must be a JSON array of {path,body} objects")
				}
				for _, entry := range entries {
					var path, text string
					if len(entry) != 2 || entry["path"] == nil || entry["body"] == nil ||
						string(entry["path"]) == "null" || string(entry["body"]) == "null" ||
						json.Unmarshal(entry["path"], &path) != nil || json.Unmarshal(entry["body"], &text) != nil {
						err = errors.New("batch entries must contain only string path and body fields")
						break
					}
				}
				data["entries"] = entries
			}
		}
	case "agents.list", "subagents.list", "schedule.list", "traits.list":
		err = arity(0, 0)
	case "traits.get", "traits.edit":
		if err = arity(1, 1); err == nil {
			if strings.TrimSpace(pos[0]) == "" {
				err = errors.New("trait ID or name is required")
				break
			}
			data["id"] = pos[0]
			if method == "traits.edit" {
				err = body("body", false)
			}
		}
	case "agents.message", "subagents.message":
		if err = arity(1, 1); err == nil {
			data["id"] = pos[0]
			err = body("body", true)
		}
	case "subagents.create":
		if err = arity(0, 0); err == nil {
			err = body("prompt", true)
		}
	case "schedule.create":
		err = arity(0, 0)
		_, hasCron := flags["cron"]
		_, hasAt := flags["at"]
		if err == nil && (hasCron == hasAt || (hasCron && strings.TrimSpace(flags["cron"]) == "") || (hasAt && strings.TrimSpace(flags["at"]) == "")) {
			err = errors.New("schedule create requires exactly one of --cron or --at")
		}
		if err == nil && strings.TrimSpace(flags["prompt"]) == "" {
			err = errors.New("schedule create requires --prompt")
		}
	case "schedule.delete":
		if err = arity(1, 1); err == nil {
			data["id"] = pos[0]
		}
	case "notify":
		if err = arity(0, 1); err == nil {
			if len(pos) == 1 {
				flags["text"] = pos[0]
			}
			err = body("text", true)
		}
	}
	return Request{Method: method, Data: data}, err
}

func parseFlags(args []string, allowed map[string]bool) (map[string]string, []string, error) {
	flags := map[string]string{}
	var positional []string
	for i := 0; i < len(args); i++ {
		arg := args[i]
		if arg == "--" {
			positional = append(positional, args[i+1:]...)
			break
		}
		if !strings.HasPrefix(arg, "-") || arg == "-" {
			positional = append(positional, arg)
			continue
		}
		name, value, hasValue := strings.Cut(strings.TrimPrefix(arg, "--"), "=")
		if !strings.HasPrefix(arg, "--") || !allowed[name] {
			return nil, nil, fmt.Errorf("unknown flag %q", arg)
		}
		if _, exists := flags[name]; exists {
			return nil, nil, fmt.Errorf("duplicate flag --%s", name)
		}
		if name == "recursive" {
			if !hasValue {
				value = "true"
			}
			if value != "true" && value != "false" {
				return nil, nil, errors.New("--recursive expects true or false")
			}
		} else if !hasValue {
			i++
			if i >= len(args) {
				return nil, nil, fmt.Errorf("--%s requires a value", name)
			}
			value = args[i]
		}
		flags[name] = value
	}
	return flags, positional, nil
}
