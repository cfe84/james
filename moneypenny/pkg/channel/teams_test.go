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
