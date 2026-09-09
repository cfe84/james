package batch

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"strings"
	"testing"
	"time"

	"james/mi6/pkg/protocol"
	"james/mi6/pkg/session"
)

func TestLinesPreserveLargeJSONRecords(t *testing.T) {
	first := `{"type":"response","data":"` + strings.Repeat("long conversation ", 10000) + `"}` + "\n"
	second := "{\"type\":\"notification\"}\n"
	var frames [][]byte
	err := RunLines(context.Background(), strings.NewReader(first+second), protocol.MaxMessageSize-64, func(b []byte) error {
		frames = append(frames, b)
		return nil
	})
	if err != nil || len(frames) != 2 || string(frames[0]) != first || string(frames[1]) != second {
		t.Fatalf("records split or mutated: frames=%d err=%v", len(frames), err)
	}
	for _, frame := range frames {
		if !json.Valid(frame) {
			t.Fatal("invalid JSON frame")
		}
	}
}

func TestLinesNeverFlushPartialRecords(t *testing.T) {
	for _, tc := range []struct {
		name, input string
		limit       int
		wantError   bool
	}{
		{"exact boundary", strings.Repeat("x", 8191) + "\n", 8192, false},
		{"oversized", strings.Repeat("x", 8192) + "\n", 8192, true},
		{"partial EOF", strings.Repeat("x", 5000), 8192, true},
		{"invalid limit", "a\n", 0, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			count := 0
			err := RunLines(context.Background(), strings.NewReader(tc.input), tc.limit, func([]byte) error {
				count++
				return nil
			})
			if (err != nil) != tc.wantError || (tc.wantError && count != 0) || (!tc.wantError && count != 1) {
				t.Fatalf("flush count=%d err=%v", count, err)
			}
		})
	}
	want := errors.New("transport failed")
	if err := RunLines(context.Background(), strings.NewReader("record\n"), 100, func([]byte) error { return want }); !errors.Is(err, want) {
		t.Fatalf("lost transport error: %v", err)
	}
}

func TestLineModePreventsInterleavingAndMidRecordJoin(t *testing.T) {
	m := session.NewManager()
	sender := m.Join("test")
	other := m.Join("test")
	early := m.Join("test")
	input, writer := io.Pipe()
	defer input.Close()
	defer writer.Close()
	done := make(chan error, 1)
	go func() {
		done <- RunLines(context.Background(), input, protocol.MaxMessageSize-64, func(b []byte) error {
			m.Broadcast("test", sender.ID, b)
			return nil
		})
	}()
	first := `{"type":"response","data":"` + strings.Repeat("x", 10000)
	if _, err := io.WriteString(writer, first); err != nil {
		t.Fatal(err)
	}
	// Beyond the raw batcher's idle threshold; no partial frame may escape.
	select {
	case <-early.WriteCh:
		t.Fatal("partial record broadcast before its newline")
	case <-time.After(150 * time.Millisecond):
	}
	late := m.Join("test")
	request := []byte("{\"type\":\"request\",\"method\":\"get_session\"}\n")
	m.Broadcast("test", other.ID, request)
	if _, err := io.WriteString(writer, "\"}\n"); err != nil {
		t.Fatal(err)
	}
	writer.Close()
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	for _, client := range []*session.Client{early, late} {
		var stream bytes.Buffer
		for range 2 {
			select {
			case frame := <-client.WriteCh:
				if !json.Valid(frame) {
					t.Fatal("relay delivered a partial JSON record")
				}
				stream.Write(frame)
			case <-time.After(time.Second):
				t.Fatal("missing frame")
			}
		}
		if stream.String() != string(request)+first+"\"}\n" {
			t.Fatal("another sender spliced the response or late reader missed its beginning")
		}
	}
}

func TestLineLimitFitsMI6Protocol(t *testing.T) {
	line := strings.Repeat("x", protocol.MaxMessageSize-65) + "\n"
	err := RunLines(context.Background(), strings.NewReader(line), protocol.MaxMessageSize-64, func(b []byte) error {
		_, err := protocol.Encode(&protocol.Message{Type: protocol.MsgData, Payload: b})
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
}

func TestRawBatchingReproducesMixedJSON(t *testing.T) {
	response := `{"type":"response","data":"` + strings.Repeat("x", 10000) + `"}` + "\n"
	request := "{\"type\":\"request\"}\n"
	var stream bytes.Buffer
	frames := 0
	err := New(4096, time.Second).Run(context.Background(), strings.NewReader(response), func(b []byte) error {
		stream.Write(b)
		frames++
		if frames == 1 {
			// Another relay participant broadcasts while this response is in flight.
			stream.WriteString(request)
		}
		return nil
	})
	if err != nil || frames < 2 {
		t.Fatalf("could not reproduce raw fragmentation: frames=%d err=%v", frames, err)
	}
	first, _, _ := bytes.Cut(stream.Bytes(), []byte("\n"))
	if json.Valid(first) {
		t.Fatal("expected raw relay interleaving to corrupt JSON")
	}
}

func TestLinesCancellationDoesNotFlush(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	flushes := 0
	err := RunLines(ctx, strings.NewReader("record\n"), 100, func([]byte) error {
		flushes++
		return nil
	})
	if !errors.Is(err, context.Canceled) || flushes != 0 {
		t.Fatalf("cancellation flushed data: count=%d err=%v", flushes, err)
	}
}
