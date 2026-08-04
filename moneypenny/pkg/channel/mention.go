package channel

import (
	"regexp"
	"strings"
)

// NormalizeMention canonicalizes a configured mention: it strips a leading '@'
// and surrounding whitespace and lowercases the result. The stored form has no
// '@' so matching can treat the '@' as optional.
func NormalizeMention(mention string) string {
	return strings.ToLower(strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(mention), "@")))
}

// MatchAndStripMention reports whether text addresses the given mention name and
// returns text with the mention token(s) removed. The '@' is optional so both a
// user-typed "@james ..." and a Teams-native mention (which arrives cleaned to
// the bare display name "james") match. Matching and stripping are
// case-insensitive and bounded to whole words. When mention is empty the text is
// returned unchanged with matched=true (no gating). All occurrences are stripped
// and leftover whitespace is collapsed.
func MatchAndStripMention(text, mention string) (stripped string, matched bool) {
	name := NormalizeMention(mention)
	if name == "" {
		return text, true
	}
	// Require a real left boundary (start of string or a non-word, non-'@'
	// character) before the optional '@', so an '@' embedded in a larger token
	// (e.g. "foo@james.com") does not satisfy the match.
	re := regexp.MustCompile(`(?i)(^|[^\w@])@?` + regexp.QuoteMeta(name) + `\b`)
	if !re.MatchString(text) {
		return text, false
	}
	// Preserve the captured left-boundary character (group 1) while removing the
	// mention token itself.
	out := re.ReplaceAllString(text, "$1 ")
	// Collapse the whitespace left where mentions were removed.
	out = regexp.MustCompile(`[ \t]{2,}`).ReplaceAllString(out, " ")
	return strings.TrimSpace(out), true
}
