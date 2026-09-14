package commands

import (
	"encoding/json"
	"testing"

	"james/hem/pkg/protocol"
	"james/hem/pkg/transport"
)

func TestEventBrokerOverflowPublishesResyncMarker(t *testing.T) {
	b := NewEventBroker()
	ch, unsubscribe := b.Subscribe()
	defer unsubscribe()
	for i := 0; i < 80; i++ {
		b.Publish(&protocol.Response{Status: protocol.StatusOK, Event: "chat_message"})
	}
	found := false
	for {
		select {
		case event := <-ch:
			if event != nil && event.Event == protocol.EventResyncRequired {
				found = true
			}
		default:
			if !found {
				t.Fatal("overflow did not publish resync marker")
			}
			return
		}
	}
}

func TestMoneypennyNotificationPublishesRevisionedHint(t *testing.T) {
	e := New(nil, "")
	ch, unsubscribe := e.Subscribe()
	defer unsubscribe()
	e.handleMoneypennyEvent("mp", &transport.Response{
		Type:      "notification",
		Event:     "chat_message",
		SessionID: "session",
		Data:      []byte(`{"role":"assistant","content":"reply","revision":7,"generation":1}`),
	})
	select {
	case got := <-ch:
		if got.Event != "chat_message" || got.SessionID != "session" {
			t.Fatalf("hint identity = %+v", got)
		}
		var data struct {
			Revision   int64 `json:"revision"`
			Generation int64 `json:"generation"`
		}
		if err := json.Unmarshal(got.Data, &data); err != nil {
			t.Fatal(err)
		}
		if data.Revision != 7 || data.Generation != 1 {
			t.Fatalf("hint watermark = %+v", data)
		}
	default:
		t.Fatal("notification was not published to event broker")
	}
}
