package protocol

// MessageType identifies the kind of wire‑protocol message.
type MessageType uint8

const (
	MsgAuth          MessageType = iota + 1 // Client sends public key
	MsgAuthChallenge                        // Server sends challenge + ephemeral ECDH pub
	MsgAuthResponse                         // Client sends signature + ephemeral ECDH pub
	MsgAuthOK                               // Server confirms auth
	MsgAuthFail                             // Server rejects auth
	MsgJoinSession                          // Client requests to join session (payload = session ID)
	MsgJoinSessionOK                        // Server confirms join
	MsgData                                 // Relay data (payload = raw bytes)
	MsgLeaveSession                         // Client leaves session
	MsgPing                                 // Keepalive
	MsgPong                                 // Keepalive response
)

// Message is the unit of communication on the wire.
type Message struct {
	Type      MessageType
	SessionID string
	Payload   []byte
}
