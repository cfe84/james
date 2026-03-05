package cli

import (
	"fmt"
	"strings"
)

// Command represents a parsed CLI command.
type Command struct {
	Verb       string   // e.g. "add", "list", "create"
	Noun       string   // normalized to singular, e.g. "moneypenny", "session"
	Args       []string // remaining args after verb+noun
	OutputType string   // from --output-type / -o flag (json, text, table, tsv)
}

// Noun aliases: map plural to singular.
var nounAliases = map[string]string{
	"moneypennies": "moneypenny",
	"mp":           "moneypenny",
	"sessions":     "session",
}

// Verb aliases: map synonyms.
var verbAliases = map[string]string{
	"remove": "delete",
}

// noNounVerbs lists verbs that take no noun.
var noNounVerbs = map[string]bool{
	"show-public-key": true,
	"server":          true,
}

// Parse parses os.Args[1:] into a Command.
// It extracts global flags (--output-type/-o) first, then verb and noun.
// Returns error if verb is missing.
func Parse(args []string) (*Command, error) {
	outputType := "text"
	var remaining []string

	// First pass: extract --output-type VALUE and -o VALUE from args.
	for i := 0; i < len(args); i++ {
		a := args[i]
		if (a == "--output-type" || a == "-o") && i+1 < len(args) {
			outputType = args[i+1]
			i++ // skip the value
		} else if strings.HasPrefix(a, "--output-type=") {
			outputType = strings.TrimPrefix(a, "--output-type=")
		} else if strings.HasPrefix(a, "-o=") {
			outputType = strings.TrimPrefix(a, "-o=")
		} else {
			remaining = append(remaining, a)
		}
	}

	if len(remaining) == 0 {
		return nil, fmt.Errorf("missing verb")
	}

	verb := remaining[0]
	remaining = remaining[1:]

	// Normalize verb through aliases.
	if alias, ok := verbAliases[verb]; ok {
		verb = alias
	}

	var noun string
	// If the verb does not require a noun, skip noun extraction.
	if !noNounVerbs[verb] && len(remaining) > 0 {
		// The next non-flag argument is the noun.
		noun = remaining[0]
		remaining = remaining[1:]

		// Normalize noun through aliases.
		if alias, ok := nounAliases[noun]; ok {
			noun = alias
		}
	}

	return &Command{
		Verb:       verb,
		Noun:       noun,
		Args:       remaining,
		OutputType: outputType,
	}, nil
}
