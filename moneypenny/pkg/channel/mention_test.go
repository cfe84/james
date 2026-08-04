package channel

import "testing"

func TestMatchAndStripMention(t *testing.T) {
	cases := []struct {
		text, mention, want string
		match               bool
	}{
		{"@james what's the weather", "james", "what's the weather", true},
		{"@james what's the weather", "@james", "what's the weather", true},
		{"James can you help", "james", "can you help", true},
		{"hey JAMES and @james again", "james", "hey and again", true},
		{"no mention here", "james", "no mention here", false},
		{"jameson is not james", "james", "jameson is not", true},
		{"email me at foo@james.com", "james", "email me at foo@james.com", false},
		{"anything goes", "", "anything goes", true},
	}
	for _, c := range cases {
		got, ok := MatchAndStripMention(c.text, c.mention)
		if ok != c.match {
			t.Errorf("MatchAndStripMention(%q,%q) match=%v want %v", c.text, c.mention, ok, c.match)
		}
		if ok && got != c.want {
			t.Errorf("MatchAndStripMention(%q,%q) = %q want %q", c.text, c.mention, got, c.want)
		}
	}
}

func TestNormalizeMention(t *testing.T) {
	for in, want := range map[string]string{
		"@James": "james",
		" james ": "james",
		"JAMES":   "james",
		"":        "",
	} {
		if got := NormalizeMention(in); got != want {
			t.Errorf("NormalizeMention(%q) = %q want %q", in, got, want)
		}
	}
}
