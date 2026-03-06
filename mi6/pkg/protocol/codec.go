package protocol

import (
	"encoding/binary"
	"errors"
	"fmt"
	"io"
)

// MaxMessageSize is the upper bound on a single framed message (1 MB).
// Any frame whose length header exceeds this value is rejected.
const MaxMessageSize = 1 << 20 // 1 MB

var (
	ErrMessageTooLarge   = errors.New("protocol: message exceeds maximum size")
	ErrInvalidMessage    = errors.New("protocol: invalid message data")
	ErrSessionIDTooLong  = errors.New("protocol: session ID exceeds maximum length")
)

// Plaintext frame layout (used pre‑auth):
//
//   Length       4 bytes  big-endian uint32 (covers everything after)
//   Type         1 byte
//   SessionID    2 bytes  big-endian uint16 length + variable UTF-8
//   Payload      4 bytes  big-endian uint32 length + variable bytes

// Encode serialises a Message into its binary wire representation
// (without the leading 4‑byte length header).
func Encode(msg *Message) ([]byte, error) {
	sidLen := len(msg.SessionID)
	if sidLen > 0xFFFF {
		return nil, ErrSessionIDTooLong
	}

	// total = 1 (type) + 2 (sid len) + sidLen + 4 (payload len) + payloadLen
	totalLen := 1 + 2 + sidLen + 4 + len(msg.Payload)
	if totalLen > MaxMessageSize {
		return nil, ErrMessageTooLarge
	}

	buf := make([]byte, totalLen)
	off := 0

	// Type
	buf[off] = byte(msg.Type)
	off++

	// SessionID length + data
	binary.BigEndian.PutUint16(buf[off:], uint16(sidLen))
	off += 2
	copy(buf[off:], msg.SessionID)
	off += sidLen

	// Payload length + data
	binary.BigEndian.PutUint32(buf[off:], uint32(len(msg.Payload)))
	off += 4
	copy(buf[off:], msg.Payload)

	return buf, nil
}

// Decode deserialises the body of a wire frame (everything after the 4‑byte
// length header) into a Message.
func Decode(data []byte) (*Message, error) {
	if len(data) < 1+2+4 {
		return nil, fmt.Errorf("%w: too short", ErrInvalidMessage)
	}

	off := 0

	// Type
	msgType := MessageType(data[off])
	off++

	// SessionID
	sidLen := int(binary.BigEndian.Uint16(data[off:]))
	off += 2
	if off+sidLen > len(data) {
		return nil, fmt.Errorf("%w: session ID length exceeds data", ErrInvalidMessage)
	}
	sessionID := string(data[off : off+sidLen])
	off += sidLen

	// Payload
	if off+4 > len(data) {
		return nil, fmt.Errorf("%w: missing payload length", ErrInvalidMessage)
	}
	payloadLen := binary.BigEndian.Uint32(data[off:])
	off += 4
	if payloadLen > MaxMessageSize || int64(off)+int64(payloadLen) != int64(len(data)) {
		return nil, fmt.Errorf("%w: payload length mismatch", ErrInvalidMessage)
	}

	payload := make([]byte, int(payloadLen))
	copy(payload, data[off:])

	return &Message{
		Type:      msgType,
		SessionID: sessionID,
		Payload:   payload,
	}, nil
}

// WriteMessage writes a length‑prefixed frame to w.
func WriteMessage(w io.Writer, msg *Message) error {
	body, err := Encode(msg)
	if err != nil {
		return err
	}

	// Write 4‑byte length header.
	var hdr [4]byte
	binary.BigEndian.PutUint32(hdr[:], uint32(len(body)))
	if _, err := w.Write(hdr[:]); err != nil {
		return fmt.Errorf("protocol: write length header: %w", err)
	}

	// Write body.
	if _, err := w.Write(body); err != nil {
		return fmt.Errorf("protocol: write body: %w", err)
	}
	return nil
}

// ReadMessage reads a length‑prefixed frame from r.
func ReadMessage(r io.Reader) (*Message, error) {
	// Read 4‑byte length header.
	var hdr [4]byte
	if _, err := io.ReadFull(r, hdr[:]); err != nil {
		return nil, fmt.Errorf("protocol: read length header: %w", err)
	}

	frameLen := binary.BigEndian.Uint32(hdr[:])
	if frameLen > MaxMessageSize {
		return nil, ErrMessageTooLarge
	}

	// Read body.
	body := make([]byte, frameLen)
	if _, err := io.ReadFull(r, body); err != nil {
		return nil, fmt.Errorf("protocol: read body: %w", err)
	}

	return Decode(body)
}
