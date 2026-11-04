package handler

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"james/moneypenny/pkg/envelope"
)

func TestNotifyUserTag(t *testing.T) {
	content := "Checking credentials. <NOTIFY_USER>Sign in to GitHub in the browser window.</NOTIFY_USER> Continuing."
	matches := notifyUserTagRe.FindAllStringSubmatch(content, -1)
	if len(matches) != 1 || matches[0][1] != "Sign in to GitHub in the browser window." {
		t.Fatalf("matches = %#v", matches)
	}
	if got := strings.TrimSpace(notifyUserTagRe.ReplaceAllString(content, "")); got != "Checking credentials.  Continuing." {
		t.Fatalf("cleaned content = %q", got)
	}
}

func TestDurableAttentionGadgetsAndCompatibility(t *testing.T) {
	h, params := gadgetTestHandler(t)
	for _, data := range []map[string]any{
		{"session_id": gadgetOtherSession, "kind": "needs_action", "reason": "Forged"},
		{"kind": "failed", "reason": "Agent forged execution failure"},
		{"kind": "needs_answer", "reason": ""},
		{"kind": "needs_action", "reason": strings.Repeat("x", 1001)},
		{"summary": strings.Repeat("界", 241)},
		{"kind": "", "revision": -1},
		{"kind": ""},
		{"unknown": true},
	} {
		if response := gadgetCall(t, params, "attention.set", data); response.Success {
			t.Fatalf("invalid attention accepted: %+v", data)
		}
	}
	question := map[string]string{"summary": "Checking access", "kind": "needs_answer", "reason": "Which account?"}
	if response := gadgetCall(t, params, "attention.set", question); !response.Success {
		t.Fatalf("question failed: %+v", response)
	}
	for _, method := range []string{"get_session", "get_session_conversation", "reconcile_session"} {
		data, _ := json.Marshal(map[string]string{"session_id": gadgetSession})
		resp := h.Handle(context.Background(), &envelope.Command{Method: method, Data: data})
		var report envelope.SessionReport
		payload, err := json.Marshal(resp.Data)
		if resp.Status != envelope.StatusSuccess || err != nil || json.Unmarshal(payload, &report) != nil || !report.Attention.Active() || report.Attention.Kind != "needs_answer" {
			t.Fatalf("%s lost attention: %+v", method, resp)
		}
	}
	tag := "<NOTIFY_USER>Sign in.</NOTIFY_USER>"
	if got := h.extractAttentionTags(gadgetSession, "Before "+tag+" After"); got != "Before  After" {
		t.Fatalf("notification marker was not stripped: %q", got)
	}
	// A provider may emit the same marker in its stream and final result.
	h.extractAttentionTags(gadgetSession, tag)
	if response := gadgetCall(t, params, "notify", map[string]string{"text": "Sign in."}); !response.Success {
		t.Fatalf("legacy notify failed: %+v", response)
	}
	session, _ := h.store.GetSession(gadgetSession)
	turns, _ := h.store.GetConversation(gadgetSession)
	if session.Attention.Kind != "needs_action" || session.Attention.Revision != 2 || len(turns) != 1 {
		t.Fatalf("compatibility notifications did not deduplicate: report=%+v turns=%+v", session.SessionReport, turns)
	}
	if response := gadgetCall(t, params, "attention.set", map[string]any{"kind": "", "revision": 1}); response.Success {
		t.Fatal("stale gadget resolution accepted")
	}
	if response := gadgetCall(t, params, "attention.set", map[string]any{"kind": "", "revision": 2}); !response.Success {
		t.Fatalf("current gadget resolution failed: %+v", response)
	}
	session, _ = h.store.GetSession(gadgetSession)
	data, _ := json.Marshal(map[string]any{"session_id": gadgetSession, "revision": session.Revision, "generation": session.Generation})
	resp := h.Handle(context.Background(), &envelope.Command{Method: "reconcile_session", Data: data})
	var metadata map[string]json.RawMessage
	payload, err := json.Marshal(resp.Data)
	if err != nil {
		t.Fatal(err)
	}
	_ = json.Unmarshal(payload, &metadata)
	var report envelope.SessionReport
	if resp.Status != envelope.StatusSuccess || json.Unmarshal(payload, &report) != nil || report.Attention == nil || report.Attention.Active() || string(metadata["conversation"]) != "null" {
		t.Fatalf("unchanged reconcile did not include resolved report: %+v", resp)
	}
	other, _ := h.store.GetSession(gadgetOtherSession)
	if other.Attention != nil {
		t.Fatal("gadget modified another session")
	}
}

func TestNotifyUserTagRejectsOversizedMessage(t *testing.T) {
	content := "<NOTIFY_USER>" + strings.Repeat("x", 1001) + "</NOTIFY_USER>"
	if notifyUserTagRe.MatchString(content) {
		t.Fatal("oversized notification was accepted")
	}
}

func TestNotificationInstructionsReserveTagsForHumanIntervention(t *testing.T) {
	for _, text := range []string{
		"Never use this tag to report progress, completion, review results, or any\nmessage to another agent",
		"Report to a parent or child through\ngadgets subagents message instead",
	} {
		if !strings.Contains(notifyUserSystemPromptSuffix, text) {
			t.Fatalf("missing notification boundary: %q", text)
		}
	}
}
