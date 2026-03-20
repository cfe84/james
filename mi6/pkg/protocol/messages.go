package protocol

// MessageType identifies the kind of wire‑protocol message.
type MessageType uint8

const (
	MsgAuth          MessageType = iota + 1 // Legacy: Client sends public key (deprecated)
	MsgAuthChallenge                        // Legacy: Server sends challenge + ECDH pub (deprecated)
	MsgAuthResponse                         // Legacy: Client sends signature + ECDH pub (deprecated)
	MsgAuthOK                               // Server confirms auth
	MsgAuthFail                             // Server rejects auth
	MsgJoinSession                          // Client requests to join session (payload = session ID)
	MsgJoinSessionOK                        // Server confirms join
	MsgData                                 // Relay data (payload = raw bytes)
	MsgLeaveSession                         // Client leaves session
	MsgPing                                 // Keepalive
	MsgPong                                 // Keepalive response
	MsgHello                                // Client sends ECDH pub (32 bytes)
	MsgServerHello                          // Server sends ECDH pub (32 bytes)
	MsgServerAuth                           // Server sends SSH pubkey + signature (encrypted)
	MsgClientAuth                           // Client sends SSH pubkey + signature (encrypted)
	MsgJoinSessionExclusive                 // Client requests exclusive join (payload = session ID); rejected if another exclusive client is already in the session
)

// Message is the unit of communication on the wire.
type Message struct {
	Type      MessageType
	SessionID string
	Payload   []byte
}
