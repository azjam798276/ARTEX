package mcpserver

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
)

// maxMessageBytes bounds one inbound JSON-RPC message. The peer is a trusted
// local agent, but a runaway write must not exhaust the server's memory, so a
// line longer than this ends the session with an explicit error.
const maxMessageBytes = 8 << 20

// Serve reads newline-delimited JSON-RPC messages from in and writes responses
// to out until the peer closes the pipe. It returns nil on a clean EOF, which
// is how a client that finished its turn shuts the server down.
//
// stdio MCP framing is one JSON object per line with no Content-Length header
// (the `ReadBuffer`/`serializeMessage` pair in @modelcontextprotocol/client and
// norma's stdio client both do this), so a reader that splits on '\n' is
// interoperable with either SDK.
func Serve(srv *Server, in io.Reader, out io.Writer) error {
	sc := bufio.NewScanner(in)
	sc.Buffer(make([]byte, 0, 64<<10), maxMessageBytes)

	w := bufio.NewWriter(out)
	for sc.Scan() {
		line := bytes.TrimSpace(sc.Bytes())
		if len(line) == 0 {
			continue
		}
		resp := srv.handleMessage(line)
		if resp == nil {
			// A notification, or a message whose id could not be determined:
			// nothing to answer.
			continue
		}
		payload, err := json.Marshal(resp)
		if err != nil {
			payload = mustMarshal(newError(nil, codeInternalError, "encode response: "+err.Error(), nil))
		}
		if _, err := w.Write(payload); err != nil {
			return fmt.Errorf("write response: %w", err)
		}
		if err := w.WriteByte('\n'); err != nil {
			return fmt.Errorf("write response terminator: %w", err)
		}
		// Flush per message: the peer waits for this reply before it sends the
		// next request, so a buffered response would deadlock the session.
		if err := w.Flush(); err != nil {
			return fmt.Errorf("flush response: %w", err)
		}
	}
	if err := sc.Err(); err != nil {
		if errors.Is(err, bufio.ErrTooLong) {
			return fmt.Errorf("message exceeds %d bytes", maxMessageBytes)
		}
		return fmt.Errorf("read request: %w", err)
	}
	return nil
}

// mustMarshal encodes a value that cannot fail to encode, for the rare path
// where building the normal error response itself failed.
func mustMarshal(v any) []byte {
	b, err := json.Marshal(v)
	if err != nil {
		return []byte(`{"jsonrpc":"2.0","id":null,"error":{"code":-32603,"message":"response encoding failed"}}`)
	}
	return b
}
