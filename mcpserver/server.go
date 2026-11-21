package mcpserver

import (
	"encoding/json"
	"fmt"
	"log"
	"strings"
)

// serverName and serverVersion identify this implementation in the handshake.
const serverName = "artex"

// Server answers MCP requests using a Client pointed at one ARTEX instance.
//
// It is safe for sequential use, which is all a stdio session ever needs: the
// transport loop handles one message at a time.
type Server struct {
	client  *Client
	version string
	tools   []tool
}

// tool is one callable tool: its descriptor plus the handler.
type tool struct {
	desc    toolDescriptor
	handler func(ctx callContext, args json.RawMessage) (*callToolResult, error)
}

// callContext carries what a handler needs from the request envelope.
type callContext struct {
	// progress reports long-running work. MCP clients may ignore it; the field
	// exists so a handler can stream a note without a second code path.
	progress func(message string)
}

// NewServer builds the tool table for client. version is the ARTEX build
// version, reported in serverInfo so a client can log what it connected to.
func NewServer(client *Client, version string) *Server {
	s := &Server{client: client, version: version}
	s.tools = s.buildTools()
	return s
}

// ToolNames lists the registered tool names in table order. Used by tests and
// by the `mcp-serve --list-tools` self-check.
func (s *Server) ToolNames() []string {
	out := make([]string, 0, len(s.tools))
	for _, t := range s.tools {
		out = append(out, t.desc.Name)
	}
	return out
}

func (s *Server) buildTools() []tool {
	return []tool{
		s.toolListTasks(),
		s.toolGetTask(),
		s.toolCreateTask(),
		s.toolControlTask(),
		s.toolTaskGoals(),
		s.toolListFindings(),
		s.toolGetFinding(),
		s.toolSetFindingStatus(),
		s.toolListAssets(),
		s.toolAssetCounts(),
		s.toolListSkills(),
		s.toolGetSkill(),
	}
}

// handleMessage parses and dispatches one inbound line. A nil response means
// the message was a notification (or unparseable with no recoverable id), so
// there is nobody to answer.
func (s *Server) handleMessage(line []byte) *rpcResponse {
	var req rpcRequest
	if err := json.Unmarshal(line, &req); err != nil {
		return newError(nil, codeParseError, "invalid JSON: "+err.Error(), nil)
	}
	if req.JSONRPC != "" && req.JSONRPC != "2.0" {
		return newError(req.ID, codeInvalidRequest, "unsupported jsonrpc version "+req.JSONRPC, nil)
	}

	// A message with an id is a request and must be answered, even when the
	// method is unknown; one without is a notification and must not be.
	notification := req.isNotification()

	switch req.Method {
	case "initialize":
		return newResult(req.ID, s.initialize(req.Params))
	case "notifications/initialized", "notifications/cancelled":
		// Lifecycle notifications. Nothing to do, and nothing to answer.
		return nil
	case "ping":
		if notification {
			return nil
		}
		return newResult(req.ID, map[string]any{})
	case "tools/list":
		return newResult(req.ID, s.listTools())
	case "tools/call":
		return s.callTool(req.ID, req.Params)
	case "resources/list":
		return newResult(req.ID, map[string]any{"resources": []any{}})
	case "prompts/list":
		return newResult(req.ID, map[string]any{"prompts": []any{}})
	case "":
		return newError(req.ID, codeInvalidRequest, "missing method", nil)
	default:
		if notification {
			return nil
		}
		return newError(req.ID, codeMethodNotFound, "method not found: "+req.Method, nil)
	}
}

// initialize answers the handshake. The negotiated revision is echoed back, as
// the specification requires; capabilities advertise only what this server
// implements, so a client will not look for resources or prompts.
func (s *Server) initialize(params json.RawMessage) map[string]any {
	var p struct {
		ProtocolVersion string `json:"protocolVersion"`
	}
	if len(params) > 0 {
		// A malformed params object is not fatal: answer with the newest
		// revision rather than dropping a client that only got the field wrong.
		_ = json.Unmarshal(params, &p)
	}
	return map[string]any{
		"protocolVersion": negotiateVersion(p.ProtocolVersion),
		"capabilities": map[string]any{
			"tools": map[string]any{"listChanged": false},
		},
		"serverInfo": map[string]any{
			"name":    serverName,
			"version": s.version,
			// A short hint shown by clients that surface instructions.
			"instructions": "ARTEX autonomous penetration-testing platform. Create and control tasks, read findings and the asset graph. " +
				"Every action is additionally gated inside ARTEX by its scope rules, approval gates and budgets; this server does not bypass them.",
		},
	}
}

func (s *Server) listTools() map[string]any {
	out := make([]toolDescriptor, 0, len(s.tools))
	for _, t := range s.tools {
		out = append(out, t.desc)
	}
	return map[string]any{"tools": out}
}

// callTool dispatches `tools/call`. Argument and upstream failures are
// returned as isError results so the model can correct itself, while protocol
// mistakes (no name, bad params envelope) are JSON-RPC errors.
func (s *Server) callTool(id json.RawMessage, params json.RawMessage) *rpcResponse {
	var p struct {
		Name      string          `json:"name"`
		Arguments json.RawMessage `json:"arguments"`
	}
	if err := json.Unmarshal(params, &p); err != nil {
		return newError(id, codeInvalidParams, "invalid params: "+err.Error(), nil)
	}
	if strings.TrimSpace(p.Name) == "" {
		return newError(id, codeInvalidParams, "missing tool name", nil)
	}

	for _, t := range s.tools {
		if t.desc.Name != p.Name {
			continue
		}
		res, err := t.handler(callContext{}, p.Arguments)
		if err != nil {
			// Handler bugs are protocol-level; a caller cannot fix them by
			// changing arguments, so surface them as an internal error and log
			// the detail for the operator.
			log.Printf("[mcp] tool %s failed: %v", p.Name, err)
			return newError(id, codeInternalError, fmt.Sprintf("tool %s failed: %v", p.Name, err), nil)
		}
		return newResult(id, res)
	}
	return newError(id, codeInvalidParams, "unknown tool: "+p.Name, nil)
}
