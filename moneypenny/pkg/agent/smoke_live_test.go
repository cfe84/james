package agent

import (
"context"
"crypto/rand"
"fmt"
"io"
"log"
"os"
"strings"
"testing"
)

// randUUIDv4 returns a random RFC-4122 v4 UUID string (test helper only).
func randUUIDv4() string {
var b [16]byte
_, _ = rand.Read(b[:])
b[6] = (b[6] & 0x0f) | 0x40
b[8] = (b[8] & 0x3f) | 0x80
return fmt.Sprintf("%x-%x-%x-%x-%x", b[0:4], b[4:6], b[6:8], b[8:10], b[10:16])
}

// TestLiveCopilotMultilineStdin runs the real copilot CLI through Runner.Run
// with a multi-line prompt and asserts every line round-trips. This guards the
// fix that routes copilot prompts via stdin (inline `-p` truncates multi-line
// prompts on Windows, where copilot.cmd runs through cmd.exe). Gated behind
// SMOKE=1 because it spends copilot credits and needs network.
func TestLiveCopilotMultilineStdin(t *testing.T) {
if os.Getenv("SMOKE") == "" {
t.Skip("set SMOKE=1 to run live copilot test")
}
r := New(log.New(io.Discard, "", 0))
sid := randUUIDv4()
ctx := context.Background()
if _, err := r.Run(ctx, RunParams{
SessionID: sid, Agent: "copilot", Yolo: true,
Prompt: "Reply with just: OK",
}); err != nil {
t.Fatalf("create: %v", err)
}
res, err := r.Run(ctx, RunParams{
SessionID: sid, Agent: "copilot", Yolo: true, Resume: true,
Prompt: "Echo back EVERY line verbatim:\nLINE_ALPHA first\nLINE_BETA the body content\nLINE_GAMMA third",
})
if err != nil {
t.Fatalf("resume: %v", err)
}
for _, want := range []string{"LINE_ALPHA", "LINE_BETA", "LINE_GAMMA"} {
if !strings.Contains(res.Text, want) {
t.Errorf("response missing %q; got:\n%s", want, res.Text)
}
}
}

// TestLiveCopilotMultilineOneShot guards the one-shot stdin path
// (RunOneShot -> buildCopilotOneShotArgs). Gated behind SMOKE=1.
func TestLiveCopilotMultilineOneShot(t *testing.T) {
if os.Getenv("SMOKE") == "" {
t.Skip("set SMOKE=1 to run live copilot test")
}
r := New(log.New(io.Discard, "", 0))
res, err := r.RunOneShot(context.Background(), RunParams{
Agent: "copilot", Yolo: true,
Prompt: "Echo back EVERY line verbatim:\nONE_ALPHA\nTWO_BETA the body\nTHREE_GAMMA",
})
if err != nil {
t.Fatalf("oneshot: %v", err)
}
for _, want := range []string{"ONE_ALPHA", "TWO_BETA", "THREE_GAMMA"} {
if !strings.Contains(res, want) {
t.Errorf("response missing %q; got:\n%s", want, res)
}
}
}
