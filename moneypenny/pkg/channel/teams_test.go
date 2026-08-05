package channel

import "testing"

// asRecords must parse a leading JSON value even when trailing non-JSON text is
// present, which happens because agency appends a telemetry trailer block that
// callTool may leave concatenated with the payload.
func TestAsRecordsToleratesTrailingText(t *testing.T) {
	payload := `{"messages":[{"id":"1","createdDateTime":"2026-08-04T19:04:18Z","from":{"id":"u1","displayName":"Charles"},"body":{"contentType":"Html","content":"<p>@bernard hi</p>"}}]}CorrelationId: abc, TimeStamp: 2026-08-04_19:14:31`
	msgs := parseMessages(payload)
	if len(msgs) != 1 {
		t.Fatalf("expected 1 message, got %d", len(msgs))
	}
	if msgs[0].ID != "1" || msgs[0].Timestamp != "2026-08-04T19:04:18Z" {
		t.Fatalf("unexpected message: %+v", msgs[0])
	}
	if msgs[0].Text != "@bernard hi" {
		t.Fatalf("unexpected text: %q", msgs[0].Text)
	}
}

func TestHTMLEscapeMarkdown(t *testing.T) {
	got := markdownToTeamsHTML("a < b & c")
	want := "<p>a &lt; b &amp; c</p>"
	if got != want {
		t.Fatalf("markdownToTeamsHTML = %q want %q", got, want)
	}
}

// parseChatActivity maps chat ids to their most recent message timestamp,
// tolerating the telemetry trailer and reading from lastMessagePreview.
func TestParseChatActivity(t *testing.T) {
	payload := `{"chats":[` +
		`{"id":"c1","topic":"A","lastMessagePreview":{"createdDateTime":"2026-08-05T10:00:00Z"}},` +
		`{"id":"c2","lastMessagePreview":{"createdDateTime":"2026-08-05T09:00:00Z"}},` +
		`{"id":"c3"}` +
		`]}CorrelationId: abc, TimeStamp: x`
	m := parseChatActivity(payload)
	if m["c1"] != "2026-08-05T10:00:00Z" {
		t.Fatalf("c1 = %q", m["c1"])
	}
	if m["c2"] != "2026-08-05T09:00:00Z" {
		t.Fatalf("c2 = %q", m["c2"])
	}
	if _, ok := m["c3"]; ok {
		t.Fatalf("c3 should be absent (no timestamp), got %q", m["c3"])
	}
}

// tsNotNewer must compare RFC3339 instants (not raw strings) so fractional
// seconds and differing zone spellings don't cause a false skip.
func TestTsNotNewer(t *testing.T) {
	cases := []struct {
		latest, since string
		want          bool
	}{
		{"2026-08-05T10:00:00Z", "2026-08-05T10:00:00Z", true},          // equal → not newer
		{"2026-08-05T10:00:01Z", "2026-08-05T10:00:00Z", false},         // newer
		{"2026-08-05T09:59:59Z", "2026-08-05T10:00:00Z", true},          // older
		{"2026-08-05T10:00:00.123Z", "2026-08-05T10:00:00Z", false},     // fractional newer (string-sorts before!)
		{"2026-08-05T10:00:00+00:00", "2026-08-05T10:00:00Z", true},     // zone spelling, equal
		{"not-a-time", "2026-08-05T10:00:00Z", false},                   // unparseable → fetch
		{"2026-08-05T10:00:00Z", "garbage", false},                      // unparseable → fetch
	}
	for _, c := range cases {
		if got := tsNotNewer(c.latest, c.since); got != c.want {
			t.Errorf("tsNotNewer(%q,%q)=%v want %v", c.latest, c.since, got, c.want)
		}
	}
}
