package transport

import (
	"bytes"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/rsa"
	"net"
	"sync"
	"testing"

	"golang.org/x/crypto/ssh"

	"james/mi6/pkg/protocol"
)

// generateRSAKey creates a test RSA key pair and returns the signer and ssh.PublicKey.
func generateRSAKey(t *testing.T) (*rsa.PrivateKey, ssh.PublicKey) {
	t.Helper()
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	pub, err := ssh.NewPublicKey(&key.PublicKey)
	if err != nil {
		t.Fatal(err)
	}
	return key, pub
}

// generateECDSAKey creates a test ECDSA key pair and returns the signer and ssh.PublicKey.
func generateECDSAKey(t *testing.T) (*ecdsa.PrivateKey, ssh.PublicKey) {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	pub, err := ssh.NewPublicKey(&key.PublicKey)
	if err != nil {
		t.Fatal(err)
	}
	return key, pub
}

func TestHandshakeRSA(t *testing.T) {
	clientKey, clientPub := generateRSAKey(t)
	authorizedKeys := []ssh.PublicKey{clientPub}

	clientConn, serverConn := net.Pipe()

	var (
		wg        sync.WaitGroup
		clientSC  *SecureConn
		serverSC  *SecureConn
		authedKey ssh.PublicKey
		clientErr error
		serverErr error
	)

	wg.Add(2)
	go func() {
		defer wg.Done()
		clientSC, clientErr = ClientHandshake(clientConn, clientKey, clientPub)
	}()
	go func() {
		defer wg.Done()
		serverSC, authedKey, serverErr = ServerHandshake(serverConn, authorizedKeys)
	}()
	wg.Wait()

	if clientErr != nil {
		t.Fatalf("client handshake error: %v", clientErr)
	}
	if serverErr != nil {
		t.Fatalf("server handshake error: %v", serverErr)
	}

	if !bytes.Equal(authedKey.Marshal(), clientPub.Marshal()) {
		t.Fatal("server did not identify the correct client key")
	}

	clientSC.Close()
	serverSC.Close()
}

func TestHandshakeECDSA(t *testing.T) {
	clientKey, clientPub := generateECDSAKey(t)
	authorizedKeys := []ssh.PublicKey{clientPub}

	clientConn, serverConn := net.Pipe()

	var (
		wg        sync.WaitGroup
		clientSC  *SecureConn
		serverSC  *SecureConn
		authedKey ssh.PublicKey
		clientErr error
		serverErr error
	)

	wg.Add(2)
	go func() {
		defer wg.Done()
		clientSC, clientErr = ClientHandshake(clientConn, clientKey, clientPub)
	}()
	go func() {
		defer wg.Done()
		serverSC, authedKey, serverErr = ServerHandshake(serverConn, authorizedKeys)
	}()
	wg.Wait()

	if clientErr != nil {
		t.Fatalf("client handshake error: %v", clientErr)
	}
	if serverErr != nil {
		t.Fatalf("server handshake error: %v", serverErr)
	}

	if !bytes.Equal(authedKey.Marshal(), clientPub.Marshal()) {
		t.Fatal("server did not identify the correct client key")
	}

	clientSC.Close()
	serverSC.Close()
}

func TestSendReceiveAfterHandshake(t *testing.T) {
	clientKey, clientPub := generateRSAKey(t)
	authorizedKeys := []ssh.PublicKey{clientPub}

	clientConn, serverConn := net.Pipe()

	var (
		wg       sync.WaitGroup
		clientSC *SecureConn
		serverSC *SecureConn
	)

	wg.Add(2)
	go func() {
		defer wg.Done()
		var err error
		clientSC, err = ClientHandshake(clientConn, clientKey, clientPub)
		if err != nil {
			t.Errorf("client handshake: %v", err)
		}
	}()
	go func() {
		defer wg.Done()
		var err error
		serverSC, _, err = ServerHandshake(serverConn, authorizedKeys)
		if err != nil {
			t.Errorf("server handshake: %v", err)
		}
	}()
	wg.Wait()

	if t.Failed() {
		return
	}
	defer clientSC.Close()
	defer serverSC.Close()

	// Client sends a message to server. net.Pipe() is synchronous, so
	// send and receive must happen concurrently.
	sent := &protocol.Message{
		Type:      protocol.MsgData,
		SessionID: "test-session-42",
		Payload:   []byte("Hello from client"),
	}

	var (
		received *protocol.Message
		sendErr  error
		recvErr  error
	)

	wg.Add(2)
	go func() {
		defer wg.Done()
		sendErr = clientSC.Send(sent)
	}()
	go func() {
		defer wg.Done()
		received, recvErr = serverSC.Receive()
	}()
	wg.Wait()

	if sendErr != nil {
		t.Fatalf("client send: %v", sendErr)
	}
	if recvErr != nil {
		t.Fatalf("server receive: %v", recvErr)
	}
	assertMessageEqual(t, sent, received)

	// Server sends a message back to client.
	reply := &protocol.Message{
		Type:      protocol.MsgData,
		SessionID: "test-session-42",
		Payload:   []byte("Hello from server"),
	}

	var receivedReply *protocol.Message

	wg.Add(2)
	go func() {
		defer wg.Done()
		sendErr = serverSC.Send(reply)
	}()
	go func() {
		defer wg.Done()
		receivedReply, recvErr = clientSC.Receive()
	}()
	wg.Wait()

	if sendErr != nil {
		t.Fatalf("server send: %v", sendErr)
	}
	if recvErr != nil {
		t.Fatalf("client receive: %v", recvErr)
	}
	assertMessageEqual(t, reply, receivedReply)
}

func TestUnauthorizedKeyRejected(t *testing.T) {
	clientKey, clientPub := generateRSAKey(t)
	_, otherPub := generateRSAKey(t)

	// Only the "other" key is authorized; the client key is not.
	authorizedKeys := []ssh.PublicKey{otherPub}

	clientConn, serverConn := net.Pipe()

	var (
		wg        sync.WaitGroup
		clientErr error
		serverErr error
	)

	wg.Add(2)
	go func() {
		defer wg.Done()
		_, clientErr = ClientHandshake(clientConn, clientKey, clientPub)
	}()
	go func() {
		defer wg.Done()
		_, _, serverErr = ServerHandshake(serverConn, authorizedKeys)
	}()
	wg.Wait()

	if serverErr == nil {
		t.Fatal("expected server to reject unauthorized key")
	}
	if clientErr == nil {
		t.Fatal("expected client to receive rejection")
	}
}

func TestMultipleMessagesInSequence(t *testing.T) {
	clientKey, clientPub := generateECDSAKey(t)
	authorizedKeys := []ssh.PublicKey{clientPub}

	clientConn, serverConn := net.Pipe()

	var (
		wg       sync.WaitGroup
		clientSC *SecureConn
		serverSC *SecureConn
	)

	wg.Add(2)
	go func() {
		defer wg.Done()
		var err error
		clientSC, err = ClientHandshake(clientConn, clientKey, clientPub)
		if err != nil {
			t.Errorf("client handshake: %v", err)
		}
	}()
	go func() {
		defer wg.Done()
		var err error
		serverSC, _, err = ServerHandshake(serverConn, authorizedKeys)
		if err != nil {
			t.Errorf("server handshake: %v", err)
		}
	}()
	wg.Wait()

	if t.Failed() {
		return
	}
	defer clientSC.Close()
	defer serverSC.Close()

	messages := []*protocol.Message{
		{Type: protocol.MsgData, SessionID: "s1", Payload: []byte("message one")},
		{Type: protocol.MsgData, SessionID: "s1", Payload: []byte("message two")},
		{Type: protocol.MsgPing},
		{Type: protocol.MsgData, SessionID: "s2", Payload: []byte("message three on different session")},
		{Type: protocol.MsgPong},
	}

	// Send all from client to server concurrently.
	received := make([]*protocol.Message, len(messages))
	var sendErr, recvErr error

	wg.Add(2)
	go func() {
		defer wg.Done()
		for i, msg := range messages {
			if err := clientSC.Send(msg); err != nil {
				sendErr = err
				t.Errorf("send message %d: %v", i, err)
				return
			}
		}
	}()
	go func() {
		defer wg.Done()
		for i := range messages {
			msg, err := serverSC.Receive()
			if err != nil {
				recvErr = err
				t.Errorf("receive message %d: %v", i, err)
				return
			}
			received[i] = msg
		}
	}()
	wg.Wait()

	if sendErr != nil || recvErr != nil {
		t.FailNow()
	}

	for i, want := range messages {
		assertMessageEqual(t, want, received[i])
	}

	// Now send messages from server to client.
	receivedBack := make([]*protocol.Message, len(messages))
	sendErr = nil
	recvErr = nil

	wg.Add(2)
	go func() {
		defer wg.Done()
		for i, msg := range messages {
			if err := serverSC.Send(msg); err != nil {
				sendErr = err
				t.Errorf("server send message %d: %v", i, err)
				return
			}
		}
	}()
	go func() {
		defer wg.Done()
		for i := range messages {
			msg, err := clientSC.Receive()
			if err != nil {
				recvErr = err
				t.Errorf("client receive message %d: %v", i, err)
				return
			}
			receivedBack[i] = msg
		}
	}()
	wg.Wait()

	if sendErr != nil || recvErr != nil {
		t.FailNow()
	}

	for i, want := range messages {
		assertMessageEqual(t, want, receivedBack[i])
	}
}

// assertMessageEqual compares two protocol messages field by field.
func assertMessageEqual(t *testing.T, want, got *protocol.Message) {
	t.Helper()
	if got.Type != want.Type {
		t.Errorf("Type: got %d, want %d", got.Type, want.Type)
	}
	if got.SessionID != want.SessionID {
		t.Errorf("SessionID: got %q, want %q", got.SessionID, want.SessionID)
	}
	wantPayload := want.Payload
	if wantPayload == nil {
		wantPayload = []byte{}
	}
	gotPayload := got.Payload
	if gotPayload == nil {
		gotPayload = []byte{}
	}
	if !bytes.Equal(gotPayload, wantPayload) {
		t.Errorf("Payload: got %q, want %q", gotPayload, wantPayload)
	}
}
