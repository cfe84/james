package protocol

import (
	"bytes"
	"crypto/rand"
	"testing"
)

func TestRoundTripEncodeDecode(t *testing.T) {
	tests := []struct {
		name string
		msg  Message
	}{
		{
			name: "auth with empty payload",
			msg:  Message{Type: MsgAuth, Payload: []byte{}},
		},
		{
			name: "auth challenge with payload",
			msg:  Message{Type: MsgAuthChallenge, Payload: []byte("challenge-data")},
		},
		{
			name: "auth response with payload",
			msg:  Message{Type: MsgAuthResponse, Payload: []byte("signature-data")},
		},
		{
			name: "auth OK no payload",
			msg:  Message{Type: MsgAuthOK},
		},
		{
			name: "auth fail with reason",
			msg:  Message{Type: MsgAuthFail, Payload: []byte("bad credentials")},
		},
		{
			name: "join session with session ID",
			msg:  Message{Type: MsgJoinSession, SessionID: "session-abc-123"},
		},
		{
			name: "join session OK with session ID",
			msg:  Message{Type: MsgJoinSessionOK, SessionID: "session-abc-123"},
		},
		{
			name: "data with session ID and payload",
			msg: Message{
				Type:      MsgData,
				SessionID: "relay-session-42",
				Payload:   []byte("hello, world"),
			},
		},
		{
			name: "leave session",
			msg:  Message{Type: MsgLeaveSession, SessionID: "session-abc-123"},
		},
		{
			name: "ping",
			msg:  Message{Type: MsgPing},
		},
		{
			name: "pong",
			msg:  Message{Type: MsgPong},
		},
		{
			name: "empty session ID and empty payload",
			msg:  Message{Type: MsgData},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			encoded, err := Encode(&tc.msg)
			if err != nil {
				t.Fatalf("Encode: %v", err)
			}

			decoded, err := Decode(encoded)
			if err != nil {
				t.Fatalf("Decode: %v", err)
			}

			assertMessageEqual(t, &tc.msg, decoded)
		})
	}
}

func TestRoundTripWriteRead(t *testing.T) {
	msgs := []Message{
		{Type: MsgAuth, Payload: []byte("pub-key-bytes")},
		{Type: MsgData, SessionID: "sess-1", Payload: []byte("frame-data")},
		{Type: MsgPing},
		{Type: MsgPong},
	}

	var buf bytes.Buffer
	for i := range msgs {
		if err := WriteMessage(&buf, &msgs[i]); err != nil {
			t.Fatalf("WriteMessage[%d]: %v", i, err)
		}
	}

	for i := range msgs {
		got, err := ReadMessage(&buf)
		if err != nil {
			t.Fatalf("ReadMessage[%d]: %v", i, err)
		}
		assertMessageEqual(t, &msgs[i], got)
	}
}

func TestLargePayload(t *testing.T) {
	// Just under the 1 MB limit (accounting for header overhead).
	payload := make([]byte, MaxMessageSize-100)
	if _, err := rand.Read(payload); err != nil {
		t.Fatalf("rand.Read: %v", err)
	}

	msg := Message{Type: MsgData, Payload: payload}

	var buf bytes.Buffer
	if err := WriteMessage(&buf, &msg); err != nil {
		t.Fatalf("WriteMessage: %v", err)
	}

	got, err := ReadMessage(&buf)
	if err != nil {
		t.Fatalf("ReadMessage: %v", err)
	}

	assertMessageEqual(t, &msg, got)
}

func TestOversizedMessageRejected(t *testing.T) {
	payload := make([]byte, MaxMessageSize+1)
	msg := Message{Type: MsgData, Payload: payload}

	_, err := Encode(&msg)
	if err == nil {
		t.Fatal("expected error for oversized message, got nil")
	}
}

func TestDecodeInvalidData(t *testing.T) {
	tests := []struct {
		name string
		data []byte
	}{
		{"too short", []byte{0x01}},
		{"truncated session ID", []byte{0x01, 0x00, 0x0A}},
		{"truncated payload length", []byte{0x01, 0x00, 0x00, 0x00}},
		{"payload length mismatch", []byte{0x01, 0x00, 0x00, 0x00, 0x00, 0x00, 0x05, 0xFF}},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			_, err := Decode(tc.data)
			if err == nil {
				t.Fatal("expected decode error, got nil")
			}
		})
	}
}

func TestReadMessageTooLarge(t *testing.T) {
	// Craft a frame header that claims a size larger than MaxMessageSize.
	var buf bytes.Buffer
	hdr := []byte{0x00, 0x20, 0x00, 0x00} // ~2 MB
	buf.Write(hdr)

	_, err := ReadMessage(&buf)
	if err == nil {
		t.Fatal("expected error for oversized frame, got nil")
	}
}

// assertMessageEqual compares two messages field by field.
func assertMessageEqual(t *testing.T, want, got *Message) {
	t.Helper()
	if got.Type != want.Type {
		t.Errorf("Type: got %d, want %d", got.Type, want.Type)
	}
	if got.SessionID != want.SessionID {
		t.Errorf("SessionID: got %q, want %q", got.SessionID, want.SessionID)
	}
	// Normalise nil vs empty for comparison.
	wantPayload := want.Payload
	if wantPayload == nil {
		wantPayload = []byte{}
	}
	gotPayload := got.Payload
	if gotPayload == nil {
		gotPayload = []byte{}
	}
	if !bytes.Equal(gotPayload, wantPayload) {
		t.Errorf("Payload: got %d bytes, want %d bytes", len(gotPayload), len(wantPayload))
	}
}
