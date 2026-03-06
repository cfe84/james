package hemclient

import (
	"bufio"
	"encoding/json"
	"fmt"
	"net"

	"james/hem/pkg/protocol"
)

// Send sends a request to the hem server and returns the response.
func Send(sockPath string, req *protocol.Request) (*protocol.Response, error) {
	conn, err := net.Dial("unix", sockPath)
	if err != nil {
		return nil, fmt.Errorf("connecting to hem server at %s: %w (is the server running? start it with 'hem start server')", sockPath, err)
	}
	defer conn.Close()

	data, err := json.Marshal(req)
	if err != nil {
		return nil, fmt.Errorf("marshaling request: %w", err)
	}
	data = append(data, '\n')

	if _, err := conn.Write(data); err != nil {
		return nil, fmt.Errorf("writing request: %w", err)
	}

	scanner := bufio.NewScanner(conn)
	scanner.Buffer(make([]byte, 0, 1024*1024), 1024*1024)
	if !scanner.Scan() {
		return nil, fmt.Errorf("no response from server")
	}

	var resp protocol.Response
	if err := json.Unmarshal(scanner.Bytes(), &resp); err != nil {
		return nil, fmt.Errorf("parsing response: %w", err)
	}

	return &resp, nil
}
