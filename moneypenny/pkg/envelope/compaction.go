package envelope

import "fmt"

// Custom compaction thresholds are intentionally bounded so a setting cannot
// disable compaction or leave too little headroom.
const (
	DefaultCompactionThresholdTokens     = 150_000
	LongContextCompactionThresholdTokens = 800_000
	MinCompactionThresholdTokens         = 10_000
	MaxCompactionThresholdTokens         = 900_000
)

// DefaultCompactionThresholdTokensForContext returns the default custom
// compaction threshold for a context tier. Empty and "default" use the normal
// context default; only explicit long_context uses the 1M-tier default.
func DefaultCompactionThresholdTokensForContext(contextTier string) int {
	if contextTier == "long_context" {
		return LongContextCompactionThresholdTokens
	}
	return DefaultCompactionThresholdTokens
}

// ValidateCompactionThresholdTokens validates a caller-provided custom
// compaction threshold. A zero value is reserved for omitted optional fields
// and must be resolved to the context-tier default before validation.
func ValidateCompactionThresholdTokens(value int) error {
	if value < MinCompactionThresholdTokens || value > MaxCompactionThresholdTokens {
		return fmt.Errorf("compaction threshold must be between %d and %d tokens", MinCompactionThresholdTokens, MaxCompactionThresholdTokens)
	}
	return nil
}

// EffectiveCompactionThresholdTokens resolves an omitted threshold to the
// context-tier default. Persisted and externally supplied non-zero values must
// be validated before calling this helper.
func EffectiveCompactionThresholdTokens(value int, contextTier string) int {
	if value == 0 {
		return DefaultCompactionThresholdTokensForContext(contextTier)
	}
	return value
}
