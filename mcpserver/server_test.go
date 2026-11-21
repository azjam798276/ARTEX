package mcpserver

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

// fakeARTEX is a minimal stand-in for ARTEX's HTTP API: enough routes for the
// facade's tools, with the response shapes the real handlers produce.
func fakeARTEX(t *testing.T, token string) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer "+token {
			w.WriteHeader(http.StatusUnauthorized)
			_, _ = w.Write([]byte(`{"error":"token is invalid or expired"}`))
			return
		}
		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.URL.Path == "/api/tasks" && r.Method == http.MethodGet:
			_, _ = w.Write([]byte(`{"tasks":[{"id":"1","status":"running","goal":"g","findings":{"critical":1,"high":0,"medium":2,"low":0}},
				{"id":"2","status":"done","goal":"g2","findings":{"critical":0,"high":0,"medium":0,"low":0}}],"active":"1"}`))
		case r.URL.Path == "/api/tasks" && r.Method == http.MethodPost:
			var body map[string]any
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
				w.WriteHeader(http.StatusBadRequest)
				_, _ = w.Write([]byte(`{"error":"bad json"}`))
				return
			}
			if body["goal"] == nil || body["description"] == nil {
				w.WriteHeader(http.StatusBadRequest)
				_, _ = w.Write([]byte(`{"error":"description and goal are required"}`))
				return
			}
			_, _ = fmt.Fprintf(w, `{"id":"7","status":"created","goal":%q}`, body["goal"])
		case r.URL.Path == "/api/tasks/1" && r.Method == http.MethodGet:
			_, _ = w.Write([]byte(`{"id":"1","status":"running","goal":"g","goals_total":3,"goals_met":1}`))
		case r.URL.Path == "/api/tasks/1/goals" && r.Method == http.MethodGet:
			_, _ = w.Write([]byte(`{"goals":[{"id":"g1","text":"find auth bypass"}]}`))
		case r.URL.Path == "/api/tasks/1/goals" && r.Method == http.MethodPost:
			// The real handler reads "text", not "description": keep that
			// contract here so a regression in the facade is caught.
			var body struct {
				Text string `json:"text"`
			}
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil || strings.TrimSpace(body.Text) == "" {
				w.WriteHeader(http.StatusBadRequest)
				_, _ = w.Write([]byte(`{"error":"Goal text cannot be empty"}`))
				return
			}
			_, _ = fmt.Fprintf(w, `{"id":"g2","text":%q}`, body.Text)
		case r.URL.Path == "/api/tasks/1/outcome" && r.Method == http.MethodGet:
			// A running task has no outcome yet: the real API answers 404.
			w.WriteHeader(http.StatusNotFound)
			_, _ = w.Write([]byte(`{"error":"task not found"}`))
		case r.URL.Path == "/api/tasks/1/control" && r.Method == http.MethodPost:
			_, _ = w.Write([]byte(`{"ok":true,"status":"paused"}`))
		case r.URL.Path == "/api/exploration/findings" && r.Method == http.MethodGet:
			_, _ = w.Write([]byte(`{"items":[{"id":"n1","finding_id":"11","severity":"high","status":"confirmed","summary":"s"}],"total":1,"page":1,"page_size":20}`))
		case r.URL.Path == "/api/exploration/findings/11" && r.Method == http.MethodGet:
			_, _ = w.Write([]byte(`{"id":"11","severity":"high","status":"confirmed","report":"## report"}`))
		case r.URL.Path == "/api/exploration/findings/bulk-status" && r.Method == http.MethodPost:
			var body struct {
				IDs    []int64 `json:"ids"`
				Status string  `json:"status"`
			}
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil || len(body.IDs) == 0 {
				w.WriteHeader(http.StatusBadRequest)
				_, _ = w.Write([]byte(`{"error":"no finding ids"}`))
				return
			}
			_, _ = fmt.Fprintf(w, `{"updated":%d,"missing":0}`, len(body.IDs))
		case r.URL.Path == "/api/assets" && r.Method == http.MethodGet:
			_, _ = w.Write([]byte(`{"count":1,"total":1,"assets":[{"id":"a1","type":"root_domain","domain":"example.com"}]}`))
		case r.URL.Path == "/api/assets/counts" && r.Method == http.MethodGet:
			_, _ = w.Write([]byte(`{"root_domain":1,"subdomain":4}`))
		case r.URL.Path == "/api/skills" && r.Method == http.MethodGet:
			_, _ = w.Write([]byte(`{"skills":[{"name":"api-security","description":"d","mcps":[]}]}`))
		case r.URL.Path == "/api/skills/api-security/files/SKILL.md" && r.Method == http.MethodGet:
			_, _ = w.Write([]byte(`{"content":"---\nname: api-security\n---\n# body","file":"SKILL.md"}`))
		default:
			w.WriteHeader(http.StatusNotFound)
			_, _ = w.Write([]byte(`{"error":"not found"}`))
		}
	}))
	t.Cleanup(srv.Close)
	return srv
}

// newTestServer builds a Server wired to a fake instance.
func newTestServer(t *testing.T, token string) *Server {
	srv := fakeARTEX(t, token)
	c, err := NewClient(srv.URL, token, false)
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}
	return NewServer(c, "test")
}

// roundTrip sends one request line through Serve and returns the decoded reply.
func roundTrip(t *testing.T, s *Server, req string) map[string]any {
	t.Helper()
	var out bytes.Buffer
	if err := Serve(s, strings.NewReader(req+"\n"), &out); err != nil {
		t.Fatalf("Serve: %v", err)
	}
	line := strings.TrimSpace(out.String())
	if line == "" {
		t.Fatalf("no response for %s", req)
	}
	var resp map[string]any
	if err := json.Unmarshal([]byte(line), &resp); err != nil {
		t.Fatalf("response is not JSON: %v (%q)", err, line)
	}
	return resp
}

func mustResult(t *testing.T, resp map[string]any) map[string]any {
	t.Helper()
	if e, ok := resp["error"]; ok {
		t.Fatalf("unexpected error: %v", e)
	}
	res, ok := resp["result"].(map[string]any)
	if !ok {
		t.Fatalf("missing result: %v", resp)
	}
	return res
}

// resultText flattens a tools/call result's text content.
func resultText(t *testing.T, res map[string]any) string {
	t.Helper()
	content, ok := res["content"].([]any)
	if !ok || len(content) == 0 {
		t.Fatalf("no content in %v", res)
	}
	block, ok := content[0].(map[string]any)
	if !ok {
		t.Fatalf("content block is not an object: %v", content[0])
	}
	return fmt.Sprint(block["text"])
}

func TestInitializeNegotiatesProtocolVersion(t *testing.T) {
	s := newTestServer(t, "tok")
	for _, tc := range []struct{ requested, want string }{
		{"2025-11-25", "2025-11-25"},
		{"2024-11-05", "2024-11-05"}, // norma's client asks for this one
		{"2000-01-01", "2025-11-25"}, // unknown: fall back to newest
		{"", "2025-11-25"},
	} {
		req := fmt.Sprintf(`{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":%q}}`, tc.requested)
		res := mustResult(t, roundTrip(t, s, req))
		if got := res["protocolVersion"]; got != tc.want {
			t.Errorf("requested %q: got %v, want %q", tc.requested, got, tc.want)
		}
		info, _ := res["serverInfo"].(map[string]any)
		if info == nil || info["name"] != "artex" {
			t.Errorf("serverInfo should name artex, got %v", res["serverInfo"])
		}
	}
}

func TestNotificationsTakeNoReply(t *testing.T) {
	s := newTestServer(t, "tok")
	var out bytes.Buffer
	req := "{\"jsonrpc\":\"2.0\",\"method\":\"notifications/initialized\",\"params\":{}}\n" +
		"{\"jsonrpc\":\"2.0\",\"method\":\"notifications/cancelled\",\"params\":{}}\n"
	if err := Serve(s, strings.NewReader(req), &out); err != nil {
		t.Fatalf("Serve: %v", err)
	}
	if got := strings.TrimSpace(out.String()); got != "" {
		t.Fatalf("notifications must produce no output, got %q", got)
	}
}

func TestParseErrorStillAnswersWithNullID(t *testing.T) {
	s := newTestServer(t, "tok")
	resp := roundTrip(t, s, `{"jsonrpc":"2.0","id":9,"method":`)
	// A truncated line is a parse error; the id is unrecoverable, so it is null.
	if resp["error"] == nil {
		t.Fatalf("expected a parse error, got %v", resp)
	}
	if resp["id"] != nil {
		t.Errorf("unparseable request should answer id null, got %v", resp["id"])
	}
}

func TestUnknownMethodAndTool(t *testing.T) {
	s := newTestServer(t, "tok")
	resp := roundTrip(t, s, `{"jsonrpc":"2.0","id":1,"method":"no/such"}`)
	errObj, _ := resp["error"].(map[string]any)
	if errObj == nil || errObj["code"] != float64(codeMethodNotFound) {
		t.Fatalf("want method-not-found, got %v", resp)
	}

	resp = roundTrip(t, s, `{"jsonrpc":"2.0","id":2,"method":"tools/call","params":{"name":"nope","arguments":{}}}`)
	errObj, _ = resp["error"].(map[string]any)
	if errObj == nil || errObj["code"] != float64(codeInvalidParams) {
		t.Fatalf("want invalid-params for unknown tool, got %v", resp)
	}
}

func TestToolsListIsWellFormed(t *testing.T) {
	s := newTestServer(t, "tok")
	res := mustResult(t, roundTrip(t, s, `{"jsonrpc":"2.0","id":1,"method":"tools/list"}`))
	tools, ok := res["tools"].([]any)
	if !ok || len(tools) == 0 {
		t.Fatalf("no tools listed: %v", res)
	}
	for _, raw := range tools {
		tool, _ := raw.(map[string]any)
		name := fmt.Sprint(tool["name"])
		if !strings.HasPrefix(name, "artex_") {
			t.Errorf("tool %q should carry the artex_ prefix", name)
		}
		if fmt.Sprint(tool["description"]) == "" {
			t.Errorf("tool %q has no description", name)
		}
		schema, _ := tool["inputSchema"].(map[string]any)
		if schema == nil || schema["type"] != "object" {
			t.Errorf("tool %q needs an object inputSchema, got %v", name, tool["inputSchema"])
		}
	}
}

// callTool runs one tools/call and returns its text content.
func callTool(t *testing.T, s *Server, name string, argsJSON string) (string, bool) {
	t.Helper()
	if argsJSON == "" {
		argsJSON = "{}"
	}
	req := fmt.Sprintf(`{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":%q,"arguments":%s}}`, name, argsJSON)
	res := mustResult(t, roundTrip(t, s, req))
	isErr, _ := res["isError"].(bool)
	return resultText(t, res), isErr
}

func TestTaskTools(t *testing.T) {
	s := newTestServer(t, "tok")

	text, isErr := callTool(t, s, "artex_tasks_list", `{"limit":10}`)
	if isErr {
		t.Fatalf("list failed: %s", text)
	}
	if !strings.Contains(text, `"active_task_id": "1"`) {
		t.Errorf("list should report the active task: %s", text)
	}

	// The status filter is applied locally: the fake returns one running and
	// one done task.
	text, _ = callTool(t, s, "artex_tasks_list", `{"status":"done"}`)
	if strings.Contains(text, `"id": "1"`) || !strings.Contains(text, `"id": "2"`) {
		t.Errorf("status filter did not narrow to the done task: %s", text)
	}

	text, isErr = callTool(t, s, "artex_task_get", `{"task_id":"1"}`)
	if isErr || !strings.Contains(text, "find auth bypass") {
		t.Fatalf("task_get should include goals: %s", text)
	}

	// A running task has no outcome; requesting one must not fail the whole
	// call, and must not fabricate an outcome.
	text, isErr = callTool(t, s, "artex_task_get", `{"task_id":"1","include_outcome":true}`)
	if isErr {
		t.Fatalf("task_get with include_outcome failed: %s", text)
	}
	if strings.Contains(text, `"outcome":`) {
		t.Errorf("a 404 outcome must not be reported as a value: %s", text)
	}

	text, isErr = callTool(t, s, "artex_task_create", `{"description":"d","goal":"g"}`)
	if isErr || !strings.Contains(text, `"id": "7"`) {
		t.Fatalf("task_create failed: %s", text)
	}

	text, isErr = callTool(t, s, "artex_task_control", `{"task_id":"1","action":"pause"}`)
	if isErr || !strings.Contains(text, "paused") {
		t.Fatalf("task_control failed: %s", text)
	}
	// An action outside the enum is refused before any round trip.
	if _, isErr = callTool(t, s, "artex_task_control", `{"task_id":"1","action":"explode"}`); !isErr {
		t.Error("an invalid action must be a tool error")
	}
}

func TestAddGoalSendsTextNotDescription(t *testing.T) {
	// The upstream handler decodes "text"; sending "description" would return
	// 400 "Goal text cannot be empty". The fake enforces that.
	s := newTestServer(t, "tok")
	text, isErr := callTool(t, s, "artex_task_goals_add", `{"task_id":"1","description":"new goal"}`)
	if isErr {
		t.Fatalf("goals_add failed: %s", text)
	}
	if !strings.Contains(text, "new goal") {
		t.Errorf("goal text should be echoed back: %s", text)
	}
}

func TestFindingTools(t *testing.T) {
	s := newTestServer(t, "tok")

	text, isErr := callTool(t, s, "artex_findings_list", `{"severity":"high","task_id":"1"}`)
	if isErr {
		t.Fatalf("findings_list failed: %s", text)
	}
	if !strings.Contains(text, `"total": 1`) {
		t.Errorf("findings_list should keep the paged envelope: %s", text)
	}

	text, isErr = callTool(t, s, "artex_finding_get", `{"finding_id":"11"}`)
	if isErr || !strings.Contains(text, "## report") {
		t.Fatalf("finding_get should return the report: %s", text)
	}

	text, isErr = callTool(t, s, "artex_finding_set_status", `{"finding_ids":[11,12],"status":"confirmed"}`)
	if isErr || !strings.Contains(text, `"updated": 2`) {
		t.Fatalf("finding_set_status failed: %s", text)
	}
	// A status the platform does not define must be refused locally, with the
	// allowed set in the message.
	text, isErr = callTool(t, s, "artex_finding_set_status", `{"finding_ids":[11],"status":"nonsense"}`)
	if !isErr || !strings.Contains(text, "pending") {
		t.Fatalf("an invalid status must name the allowed set: %s", text)
	}
	// Empty ids must not reach the API.
	if _, isErr = callTool(t, s, "artex_finding_set_status", `{"finding_ids":[],"status":"confirmed"}`); !isErr {
		t.Error("empty finding_ids must be a tool error")
	}
}

func TestAssetAndSkillTools(t *testing.T) {
	s := newTestServer(t, "tok")

	text, isErr := callTool(t, s, "artex_assets_list", `{"type":"root_domain"}`)
	if isErr || !strings.Contains(text, "example.com") {
		t.Fatalf("assets_list failed: %s", text)
	}
	text, isErr = callTool(t, s, "artex_assets_counts", "")
	if isErr || !strings.Contains(text, `"subdomain": 4`) {
		t.Fatalf("assets_counts failed: %s", text)
	}
	text, isErr = callTool(t, s, "artex_skills_list", "")
	if isErr || !strings.Contains(text, "api-security") {
		t.Fatalf("skills_list failed: %s", text)
	}
	// A nested file path must keep its slashes rather than be escaped into one
	// segment, or the {file...} route would not match.
	text, isErr = callTool(t, s, "artex_skill_read", `{"name":"api-security","file":"SKILL.md"}`)
	if isErr || !strings.Contains(text, "# body") {
		t.Fatalf("skill_read failed: %s", text)
	}
}

func TestUnauthorizedTokenIsActionable(t *testing.T) {
	s := newTestServer(t, "tok")
	// Rebuild the client with the wrong token against the same fake instance.
	bad, err := NewClient(s.client.BaseURL, "wrong", false)
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}
	badSrv := NewServer(bad, "test")
	text, isErr := callTool(t, badSrv, "artex_tasks_list", "")
	if !isErr {
		t.Fatalf("a rejected token must be a tool error: %s", text)
	}
	if !strings.Contains(text, "401") || !strings.Contains(text, "expired") {
		t.Errorf("the 401 message should explain the token lifetime: %s", text)
	}
}

func TestTransportRequiresToken(t *testing.T) {
	if _, err := NewClient("http://127.0.0.1:8787", "", false); err == nil {
		t.Error("NewClient must refuse an empty token")
	}
	if _, err := NewClient("", "t", false); err == nil {
		t.Error("NewClient must require a URL")
	}
	if _, err := NewClient("ftp://x", "t", false); err == nil {
		t.Error("NewClient must refuse a non-http scheme")
	}
}

func TestServeHandlesMultipleSequentialRequests(t *testing.T) {
	s := newTestServer(t, "tok")
	var out bytes.Buffer
	in := strings.Join([]string{
		`{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-11-25"}}`,
		`{"jsonrpc":"2.0","method":"notifications/initialized"}`,
		`{"jsonrpc":"2.0","id":2,"method":"tools/list"}`,
		`{"jsonrpc":"2.0","id":3,"method":"ping"}`,
	}, "\n") + "\n"
	if err := Serve(s, strings.NewReader(in), &out); err != nil {
		t.Fatalf("Serve: %v", err)
	}
	lines := strings.Split(strings.TrimSpace(out.String()), "\n")
	if len(lines) != 3 {
		t.Fatalf("want 3 responses (the notification takes none), got %d: %q", len(lines), out.String())
	}
	for i, wantID := range []float64{1, 2, 3} {
		var resp map[string]any
		if err := json.Unmarshal([]byte(lines[i]), &resp); err != nil {
			t.Fatalf("line %d is not JSON: %v", i, err)
		}
		if resp["id"] != wantID {
			t.Errorf("line %d: id %v, want %v (responses must stay in order)", i, resp["id"], wantID)
		}
	}
}

// TestServeFlushesEachResponse guards the deadlock a buffered response would
// cause: an MCP client waits for its reply before sending the next request. The
// test writes one request, then requires the reply to arrive while the input
// pipe is still open and nothing else has been written. A Serve that flushed
// only at EOF would block here instead.
func TestServeFlushesEachResponse(t *testing.T) {
	s := newTestServer(t, "tok")

	// A writer that records what Serve emits as it emits it, so the test can
	// observe a response before the session ends.
	sink := &notifyingWriter{ch: make(chan string, 4)}

	inR, inW := io.Pipe()
	done := make(chan error, 1)
	go func() { done <- Serve(s, inR, sink) }()

	if _, err := inW.Write([]byte(`{"jsonrpc":"2.0","id":1,"method":"ping"}` + "\n")); err != nil {
		t.Fatalf("write request: %v", err)
	}

	select {
	case line := <-sink.ch:
		var resp map[string]any
		if err := json.Unmarshal([]byte(line), &resp); err != nil {
			t.Fatalf("first response is not JSON: %v", err)
		}
		if resp["id"] != float64(1) {
			t.Fatalf("got id %v, want 1", resp["id"])
		}
	case <-time.After(2 * time.Second):
		t.Fatal("no response arrived before EOF: Serve did not flush the reply")
	}

	_ = inW.Close()
	if err := <-done; err != nil {
		t.Fatalf("Serve: %v", err)
	}
}

// notifyingWriter forwards each complete written line to ch.
type notifyingWriter struct {
	mu  sync.Mutex
	buf bytes.Buffer
	ch  chan string
}

func (w *notifyingWriter) Write(p []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	n, err := w.buf.Write(p)
	for {
		line, rerr := w.buf.ReadString('\n')
		if rerr != nil {
			// Incomplete line: put it back for the next Write.
			w.buf.WriteString(line)
			break
		}
		w.ch <- strings.TrimSpace(line)
	}
	return n, err
}

// TestSkillDocumentsEveryTool guards the operator skill against drift: the
// skill is the brief an external agent reads before calling these tools, so a
// renamed or added tool must reach it in the same change. It is the one thing
// keeping skills/artex-mcp/SKILL.md honest, because nothing at runtime reads
// the skill and the server.
func TestSkillDocumentsEveryTool(t *testing.T) {
	s := newTestServer(t, "tok")
	raw, err := os.ReadFile(filepath.Join("..", "skills", "artex-mcp", "SKILL.md"))
	if err != nil {
		t.Fatalf("read operator skill: %v", err)
	}
	// Line wraps and CRLF must not matter: compare on collapsed whitespace.
	text := strings.Join(strings.Fields(string(raw)), " ")
	for _, name := range s.ToolNames() {
		if !strings.Contains(text, "`"+name+"`") {
			t.Errorf("skills/artex-mcp/SKILL.md does not mention the tool %q", name)
		}
	}
}
