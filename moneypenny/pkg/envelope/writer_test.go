package envelope

import (
	"bytes"
	"encoding/json"
	"strings"
	"sync"
	"testing"
)

func TestNotificationWriterSerializesResponsesAndNotifications(t *testing.T) {
	var output bytes.Buffer
	writer := NewNotificationWriter(&output)
	var wg sync.WaitGroup
	for i := 0; i < 50; i++ {
		wg.Add(2)
		go func() {
			defer wg.Done()
			if err := writer.Send("activity", "session", strings.Repeat("n", 10000)); err != nil {
				t.Error(err)
			}
		}()
		go func() {
			defer wg.Done()
			b, err := SuccessResponse("request", strings.Repeat("r", 10000)).Marshal()
			if err != nil {
				t.Error(err)
				return
			}
			if _, err := writer.Write(b); err != nil {
				t.Error(err)
			}
		}()
	}
	wg.Wait()
	lines := bytes.Split(bytes.TrimSpace(output.Bytes()), []byte("\n"))
	if len(lines) != 100 {
		t.Fatalf("got %d messages, want 100", len(lines))
	}
	for _, line := range lines {
		if !json.Valid(line) {
			t.Fatal("concurrent messages interleaved")
		}
	}
	var next bytes.Buffer
	writer.SetWriter(&next)
	if err := writer.Send("activity", "session", "new transport"); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(next.String(), "new transport") {
		t.Fatal("writer did not follow transport replacement")
	}
}

func TestNotificationWriterReconnectDuringActivity(t *testing.T) {
	var oldTransport, newTransport bytes.Buffer
	writer := NewNotificationWriter(&oldTransport)
	var wg sync.WaitGroup
	for range 50 {
		wg.Add(2)
		go func() {
			defer wg.Done()
			if err := writer.Send("activity", "session", strings.Repeat("activity", 1000)); err != nil {
				t.Error(err)
			}
		}()
		go func() {
			defer wg.Done()
			writer.SetWriter(&newTransport)
		}()
	}
	wg.Wait()
	messages := 0
	for _, data := range [][]byte{oldTransport.Bytes(), newTransport.Bytes()} {
		for _, line := range bytes.Split(bytes.TrimSpace(data), []byte("\n")) {
			if len(line) == 0 {
				continue
			}
			if !json.Valid(line) {
				t.Fatal("reconnect split a protocol message")
			}
			messages++
		}
	}
	if messages != 50 {
		t.Fatalf("got %d notifications across reconnect, want 50", messages)
	}
}
