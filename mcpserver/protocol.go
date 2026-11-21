// Package mcpserver exposes ARTEX to an external agent as a Model Context
// Protocol server over stdio (newline-delimited JSON-RPC 2.0).
//
// It is the mirror image of mcphttp: where that package makes ARTEX a consumer
// of remote MCP servers, this one makes an ARTEX instance a producer, so an
// agent that already speaks MCP (for example a DeepSeek Harness session through
// dsh-mcp-client, or dsh-superagent's worker preset) can create tasks, read
// findings and query the asset graph without learning ARTEX's REST dialect.
//
// The server holds no state and opens no database connection: every tool is a
// thin, typed call against the instance's own HTTP API, authenticated with the
// bearer token the server is configured with (see server.RunMCPServe). That
// keeps one authorization model — the token's — and makes the facade usable
// against a remote ARTEX just as well as a local one.
package mcpserver

import (
	"encoding/json"
	"fmt"
)

// ProtocolVersion is the MCP revision this server speaks. It is the same
// constant mcphttp negotiates, so ARTEX's client and server halves agree.
const ProtocolVersion = "2025-11-25"

// supportedVersions lists the revisions this server accepts in `initialize`.
// A client that asks for an older revision gets that one echoed back (the
// handshake requires the server to answer with a version it can speak) so both
// current SDKs — @modelcontextprotocol/client (2025-11-25) and norma's client
// (2024-11-05) — can drive the same binary.
var supportedVersions = []string{ProtocolVersion, "2025-06-18", "2025-03-26", "2024-11-05", "2024-10-07"}

// negotiateVersion returns the client's requested revision when it is one this
// server speaks, and the newest revision otherwise.
func negotiateVersion(requested string) string {
	for _, v := range supportedVersions {
		if v == requested {
			return v
		}
	}
	return ProtocolVersion
}

// JSON-RPC 2.0 error codes used here.
const (
	codeParseError     = -32700
	codeInvalidRequest = -32600
	codeMethodNotFound = -32601
	codeInvalidParams  = -32602
	codeInternalError  = -32603
)

// rpcRequest is one inbound JSON-RPC request or notification. ID is kept as a
// raw message because JSON-RPC allows string, number or null ids and the reply
// must echo exactly what arrived.
type rpcRequest struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id,omitempty"`
	Method  string          `json:"method"`
	Params  json.RawMessage `json:"params,omitempty"`
}

// isNotification reports whether the message is a JSON-RPC notification (no id)
// rather than a request. Notifications take no reply.
func (r *rpcRequest) isNotification() bool {
	return len(r.ID) == 0 || string(r.ID) == "null"
}

// rpcError is the JSON-RPC error object.
type rpcError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
	Data    any    `json:"data,omitempty"`
}

// rpcResponse is one outbound JSON-RPC response.
type rpcResponse struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id"`
	Result  any             `json:"result,omitempty"`
	Error   *rpcError       `json:"error,omitempty"`
}

func newResult(id json.RawMessage, result any) *rpcResponse {
	return &rpcResponse{JSONRPC: "2.0", ID: id, Result: result}
}

func newError(id json.RawMessage, code int, msg string, data any) *rpcResponse {
	return &rpcResponse{JSONRPC: "2.0", ID: id, Error: &rpcError{Code: code, Message: msg, Data: data}}
}

// toolDescriptor is one entry of `tools/list`. InputSchema is the JSON Schema
// the model reads, so descriptions live here rather than in a separate prompt.
type toolDescriptor struct {
	Name        string         `json:"name"`
	Title       string         `json:"title,omitempty"`
	Description string         `json:"description"`
	InputSchema map[string]any `json:"inputSchema"`
	Annotations map[string]any `json:"annotations,omitempty"`
}

// callToolResult is the `tools/call` result shape. Content carries the model's
// text; isError reports a tool-level failure (a bad argument, an upstream 4xx)
// rather than a transport failure, which lets the caller show it to the model
// instead of aborting the turn.
type callToolResult struct {
	Content any            `json:"content"`
	IsError bool           `json:"isError,omitempty"`
	Meta    map[string]any `json:"_meta,omitempty"`
}

// textContent builds the single text block most tools return.
func textContent(s string) []map[string]any {
	return []map[string]any{{"type": "text", "text": s}}
}

// jsonContent returns a text block holding pretty-printed JSON. Tool output is
// read by a model, and indented JSON is easier for it to quote back correctly
// than a single long line.
func jsonContent(v any) ([]map[string]any, error) {
	b, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return nil, fmt.Errorf("encode tool output: %w", err)
	}
	return textContent(string(b)), nil
}
