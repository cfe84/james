package handler

import (
	"context"
	"encoding/json"
	"sync/atomic"
	"testing"
	"time"

	"james/moneypenny/pkg/envelope"
)

func resetCopilotModelsForTest(discover func(context.Context) ([]envelope.ModelInfo, error)) func() {
	copilotModelMu.Lock()
	oldDiscover := copilotDiscover
	copilotDiscover = discover
	copilotModelCache = nil
	copilotModelCacheTime = time.Time{}
	copilotModelRefresh = nil
	copilotModelMu.Unlock()
	return func() {
		copilotModelMu.Lock()
		copilotDiscover = oldDiscover
		copilotModelCache = nil
		copilotModelCacheTime = time.Time{}
		copilotModelRefresh = nil
		copilotModelMu.Unlock()
	}
}

func listModelsForTest(t *testing.T, ctx context.Context, refresh bool) *envelope.Response {
	t.Helper()
	data, err := json.Marshal(envelope.ListModelsData{Agent: "copilot", Refresh: refresh})
	if err != nil {
		t.Fatal(err)
	}
	return (&Handler{}).Handle(ctx, &envelope.Command{Method: "list_models", Data: data})
}

func TestCopilotModelRefreshDoesNotBlockNormalList(t *testing.T) {
	started := make(chan struct{})
	release := make(chan struct{})
	var discoveryCalls atomic.Int32
	models := []envelope.ModelInfo{{Name: "gpt-5.3-codex", Value: "gpt-5.3-codex"}}
	restore := resetCopilotModelsForTest(func(ctx context.Context) ([]envelope.ModelInfo, error) {
		discoveryCalls.Add(1)
		close(started)
		select {
		case <-release:
			return models, nil
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	})
	defer restore()

	begin := time.Now()
	response := listModelsForTest(t, context.Background(), false)
	if elapsed := time.Since(begin); elapsed > 100*time.Millisecond {
		t.Fatalf("cold-cache list_models waited for discovery: %v", elapsed)
	}
	if response.Status != envelope.StatusSuccess {
		t.Fatalf("cold-cache response failed: %+v", response)
	}
	if got := response.Data.(envelope.ListModelsResponse).Models; len(got) != 0 {
		t.Fatalf("cold-cache response returned models: %+v", got)
	}
	select {
	case <-started:
	case <-time.After(time.Second):
		t.Fatal("background discovery did not start")
	}

	refreshDone := make(chan *envelope.Response, 1)
	go func() {
		refreshDone <- listModelsForTest(t, context.Background(), true)
	}()
	select {
	case response := <-refreshDone:
		t.Fatalf("explicit refresh returned before discovery release: %+v", response)
	case <-time.After(50 * time.Millisecond):
	}

	close(release)
	select {
	case response := <-refreshDone:
		if response.Status != envelope.StatusSuccess {
			t.Fatalf("explicit refresh failed: %+v", response)
		}
	case <-time.After(time.Second):
		t.Fatal("explicit refresh did not complete after release")
	}
	if got := discoveryCalls.Load(); got != 1 {
		t.Fatalf("discovery calls = %d, want one shared refresh", got)
	}

	response = listModelsForTest(t, context.Background(), false)
	got := response.Data.(envelope.ListModelsResponse).Models
	if len(got) != 1 || got[0].Value != models[0].Value {
		t.Fatalf("eventual cache contents = %+v, want %+v", got, models)
	}
}

func TestCopilotExplicitRefreshHonorsCancellation(t *testing.T) {
	started := make(chan struct{})
	restore := resetCopilotModelsForTest(func(ctx context.Context) ([]envelope.ModelInfo, error) {
		close(started)
		<-ctx.Done()
		return nil, ctx.Err()
	})
	defer restore()

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Millisecond)
	defer cancel()
	response := listModelsForTest(t, ctx, true)
	if response.Status == envelope.StatusSuccess || response.ErrorCode != envelope.ErrInternalError {
		t.Fatalf("cancellation response = %+v", response)
	}

	select {
	case <-started:
	case <-time.After(time.Second):
		t.Fatal("discovery did not start")
	}
	copilotModelMu.Lock()
	refresh := copilotModelRefresh
	copilotModelMu.Unlock()
	if refresh != nil {
		<-refresh.done
	}
}

func TestCopilotExplicitRefreshRejectsEmptyDiscovery(t *testing.T) {
	restore := resetCopilotModelsForTest(func(context.Context) ([]envelope.ModelInfo, error) {
		return nil, nil
	})
	defer restore()

	response := listModelsForTest(t, context.Background(), true)
	if response.Status == envelope.StatusSuccess || response.ErrorCode != envelope.ErrInternalError {
		t.Fatalf("empty discovery response = %+v", response)
	}
}
