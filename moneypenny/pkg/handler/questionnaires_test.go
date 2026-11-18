package handler

import (
	"context"
	"encoding/json"
	"reflect"
	"strings"
	"sync"
	"testing"

	"james/moneypenny/pkg/agent"
	"james/moneypenny/pkg/envelope"
	"james/moneypenny/pkg/questionnaire"
	"james/moneypenny/pkg/store"
)

func TestQuestionnaireLifecycle(t *testing.T) {
	h, params := gadgetTestHandler(t)
	definition := questionnaire.Definition{Title: "Release decision", Questions: []questionnaire.Question{
		{Prompt: "Proceed?", Type: "yes_no"},
		{Prompt: "Which scope?", Type: "choice", Options: []string{"Small", "Large"}},
		{Prompt: "Any constraints?", Type: "text"},
	}}
	call := func(method string, data any) *envelope.Response {
		t.Helper()
		return h.Handle(context.Background(), &envelope.Command{Method: method, Data: questionnairePayload(t, data)})
	}
	pending := func() []questionnaire.Card {
		t.Helper()
		resp := call("get_session", map[string]any{"session_id": gadgetSession, "questionnaires": true})
		detail, ok := resp.Data.(envelope.SessionDetail)
		if !ok || detail.Questionnaires == nil || detail.Attention.Active() {
			t.Fatalf("questionnaire details or attention incorrect: %+v", resp)
		}
		return *detail.Questionnaires
	}
	requireGadgetError(t, gadgetCall(t, params, "questionnaire.publish",
		map[string]any{"session_id": gadgetOtherSession, "title": definition.Title, "questions": definition.Questions}), "invalid_request")
	for i := 0; i < questionnaire.MaxPending; i++ {
		if resp := gadgetCall(t, params, "questionnaire.publish", definition); !resp.Success {
			t.Fatalf("publish without grants: %+v", resp)
		}
	}
	requireGadgetError(t, gadgetCall(t, params, "questionnaire.publish", definition), "invalid_request")
	cards := pending()
	if len(cards) != questionnaire.MaxPending || !reflect.DeepEqual(cards[0].Definition, definition) {
		t.Fatalf("unexpected pending cards: %+v", cards)
	}
	first := 0
	request := questionnaire.Resolution{SessionID: gadgetSession, ID: cards[0].ID, Action: "submit",
		Answers: []questionnaire.Answer{{Text: "Only after review"}, {Choice: &first}, {Text: "No downtime"}}}
	for _, invalid := range []questionnaire.Resolution{
		{SessionID: gadgetOtherSession, ID: request.ID, Action: "submit", Answers: request.Answers},
		{SessionID: gadgetSession, ID: request.ID, Action: "submit", Answers: request.Answers[:2]},
		{SessionID: gadgetSession, ID: request.ID, Action: "submit", Answers: []questionnaire.Answer{{Text: " "}, {Choice: &first}, {Text: "Present"}}},
	} {
		if resp := call("resolve_questionnaire", invalid); resp.ErrorCode != envelope.ErrInvalidRequest {
			t.Fatalf("invalid resolution accepted: %+v", resp)
		}
	}
	if err := h.store.UpdateSessionStatus(gadgetSession, store.StateWorking); err != nil {
		t.Fatal(err)
	}
	payload := questionnairePayload(t, request)
	results := make(chan *envelope.Response, 2)
	var requests sync.WaitGroup
	for i := 0; i < 2; i++ {
		requests.Add(1)
		go func() {
			defer requests.Done()
			results <- h.Handle(context.Background(), &envelope.Command{Method: "resolve_questionnaire", Data: payload})
		}()
	}
	requests.Wait()
	close(results)
	success := 0
	for resp := range results {
		if resp.Status == envelope.StatusSuccess {
			success++
		} else if resp.ErrorCode != envelope.ErrInvalidRequest {
			t.Fatalf("unexpected duplicate response: %+v", resp)
		}
	}
	if success != 1 {
		t.Fatalf("accepted %d concurrent submissions", success)
	}
	discard := questionnaire.Resolution{SessionID: gadgetSession, ID: cards[1].ID, Action: "discard"}
	if resp := call("resolve_questionnaire", discard); resp.Status != envelope.StatusSuccess {
		t.Fatalf("discard: %+v", resp)
	}
	if got := pending(); len(got) != len(cards)-2 {
		t.Fatalf("resolved cards are still pending: %+v", got)
	}
	if err := h.store.QueuePrompt(gadgetSession, "Ordinary chat still works", "", "", "", ""); err != nil {
		t.Fatal(err)
	}
	var prompts []string
	h.runAgentFunc = func(_ context.Context, data agent.RunParams) (*agent.Result, error) {
		prompts = append(prompts, data.Prompt)
		return &agent.Result{Text: "Received."}, nil
	}
	h.continueQueuedPrompts(gadgetSession)
	h.runsWG.Wait()
	if len(prompts) != 2 || !strings.Contains(prompts[0], "Proceed?\nAnswer: Only after review") ||
		!strings.Contains(prompts[0], "Which scope?\nAnswer: Small") ||
		!strings.Contains(prompts[0], "Any constraints?\nAnswer: No downtime") || prompts[1] != "Ordinary chat still works" {
		t.Fatalf("expected one complete questionnaire round and one ordinary chat round: %v", prompts)
	}
	request.ID = cards[2].ID
	if resp := call("resolve_questionnaire", request); resp.Status != envelope.StatusSuccess {
		t.Fatalf("idle submission: %+v", resp)
	}
	h.runsWG.Wait()
	if len(prompts) != 3 || prompts[2] != prompts[0] {
		t.Fatalf("idle submission did not start a new round: %v", prompts)
	}
	conversation, err := h.store.GetConversation(gadgetSession)
	if err != nil || len(conversation) != len(cards)+6 || conversation[0].Role != questionnaire.SubmittedRole ||
		conversation[1].Role != questionnaire.DiscardedRole || conversation[len(cards)].Role != "user" {
		t.Fatalf("unexpected history after submit/discard: %+v, %v", conversation, err)
	}
}

func questionnairePayload(t *testing.T, data any) json.RawMessage {
	t.Helper()
	payload, err := json.Marshal(data)
	if err != nil {
		t.Fatal(err)
	}
	return payload
}
