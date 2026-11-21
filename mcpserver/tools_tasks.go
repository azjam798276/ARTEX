package mcpserver

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"strconv"
	"strings"
	"time"
)

// toolTimeout bounds one tool call's upstream work. It sits under the client's
// own 60 s ceiling so a slow endpoint fails with a tool-level message rather
// than a transport error the model cannot act on.
const toolTimeout = 55 * time.Second

// args is a decoded tool argument set, kept as a map so each handler reads only
// the fields it documents and an unexpected field is ignored rather than fatal.
type args map[string]any

func decodeArgs(raw json.RawMessage) (args, error) {
	if len(strings.TrimSpace(string(raw))) == 0 {
		return args{}, nil
	}
	var a args
	if err := json.Unmarshal(raw, &a); err != nil {
		return nil, fmt.Errorf("arguments must be a JSON object: %w", err)
	}
	if a == nil {
		a = args{}
	}
	return a, nil
}

func (a args) str(key string) string {
	v, ok := a[key]
	if !ok || v == nil {
		return ""
	}
	if s, ok := v.(string); ok {
		return strings.TrimSpace(s)
	}
	return strings.TrimSpace(fmt.Sprint(v))
}

func (a args) required(key string) (string, error) {
	v := a.str(key)
	if v == "" {
		return "", fmt.Errorf("%q is required", key)
	}
	return v, nil
}

// intOr returns an integer argument, or def when absent or unparseable.
func (a args) intOr(key string, def int) int {
	v, ok := a[key]
	if !ok || v == nil {
		return def
	}
	switch n := v.(type) {
	case float64:
		return int(n)
	case int:
		return n
	case string:
		if i, err := strconv.Atoi(strings.TrimSpace(n)); err == nil {
			return i
		}
	}
	return def
}

func (a args) boolPtr(key string) *bool {
	v, ok := a[key]
	if !ok || v == nil {
		return nil
	}
	b, ok := v.(bool)
	if !ok {
		return nil
	}
	return &b
}

// errorResult renders a failure as a tool-level error the model can read. An
// expired token gets its own sentence, because the fix is a human action.
func errorResult(err error) *callToolResult {
	msg := err.Error()
	// Client methods return *APIError unwrapped (they do not use %w on it), so a
	// direct type assertion is the whole check; the messages the client builds
	// with fmt.Errorf are transport failures and keep their own text.
	if apiErr, ok := err.(*APIError); ok {
		switch {
		case apiErr.IsUnauthorized():
			msg = "ARTEX rejected the API token (HTTP 401). The configured token is invalid or expired (tokens live 7 days); " +
				"issue a new one and restart this MCP server. Underlying error: " + apiErr.Error()
		case apiErr.IsNotFound():
			msg = "not found: " + apiErr.Error()
		}
	}
	return &callToolResult{Content: textContent("Error: " + msg), IsError: true}
}

// run executes fn with the per-call timeout.
func run(fn func(ctx context.Context) (*callToolResult, error)) (*callToolResult, error) {
	ctx, cancel := context.WithTimeout(context.Background(), toolTimeout)
	defer cancel()
	res, err := fn(ctx)
	if err != nil {
		return errorResult(err), nil
	}
	return res, nil
}

// --- task tools ---------------------------------------------------------

func (s *Server) toolListTasks() tool {
	return tool{
		desc: toolDescriptor{
			Name:  "artex_tasks_list",
			Title: "List ARTEX tasks",
			Description: "List penetration-testing tasks on the ARTEX instance with their status, goal progress, in-flight workers and finding counts by severity. " +
				"Use it to find an existing engagement before creating a new one.",
			InputSchema: map[string]any{
				"type": "object",
				"properties": map[string]any{
					"status": map[string]any{
						"type":        "string",
						"description": "Only return tasks in this status: created, running, paused, done or failed.",
					},
					"limit": map[string]any{
						"type":        "integer",
						"description": "Maximum tasks to return, newest first (default 25, max 200).",
					},
				},
				"additionalProperties": false,
			},
			Annotations: map[string]any{"readOnlyHint": true, "idempotentHint": true},
		},
		handler: func(_ callContext, raw json.RawMessage) (*callToolResult, error) {
			a, err := decodeArgs(raw)
			if err != nil {
				return errorResult(err), nil
			}
			return run(func(ctx context.Context) (*callToolResult, error) {
				var resp struct {
					Tasks  []map[string]any `json:"tasks"`
					Active string           `json:"active"`
				}
				if err := s.client.Get(ctx, "/api/tasks", nil, &resp); err != nil {
					return nil, err
				}
				tasks := resp.Tasks
				if want := a.str("status"); want != "" {
					filtered := make([]map[string]any, 0, len(tasks))
					for _, t := range tasks {
						if fmt.Sprint(t["status"]) == want {
							filtered = append(filtered, t)
						}
					}
					tasks = filtered
				}
				limit := a.intOr("limit", 25)
				if limit <= 0 {
					limit = 25
				}
				if limit > 200 {
					limit = 200
				}
				if len(tasks) > limit {
					tasks = tasks[:limit]
				}
				content, err := jsonContent(map[string]any{
					"active_task_id": resp.Active,
					"count":          len(tasks),
					"tasks":          tasks,
				})
				return &callToolResult{Content: content}, err
			})
		},
	}
}

func (s *Server) toolGetTask() tool {
	return tool{
		desc: toolDescriptor{
			Name:  "artex_task_get",
			Title: "Get one ARTEX task",
			Description: "Read one task's detail: status, goal, token usage, goal progress, finding counts and its goals. " +
				"Pass include_outcome to also read the task's closing outcome report.",
			InputSchema: map[string]any{
				"type": "object",
				"properties": map[string]any{
					"task_id": map[string]any{"type": "string", "description": "The task id, as returned by artex_tasks_list."},
					"include_outcome": map[string]any{
						"type":        "boolean",
						"description": "Also return the task outcome (the written closing report), available once the task is done.",
					},
				},
				"required":             []string{"task_id"},
				"additionalProperties": false,
			},
			Annotations: map[string]any{"readOnlyHint": true, "idempotentHint": true},
		},
		handler: func(_ callContext, raw json.RawMessage) (*callToolResult, error) {
			a, err := decodeArgs(raw)
			if err != nil {
				return errorResult(err), nil
			}
			id, err := a.required("task_id")
			if err != nil {
				return errorResult(err), nil
			}
			return run(func(ctx context.Context) (*callToolResult, error) {
				var task map[string]any
				if err := s.client.Get(ctx, "/api/tasks/"+url.PathEscape(id), nil, &task); err != nil {
					return nil, err
				}
				out := map[string]any{"task": task}

				var goals struct {
					Goals []map[string]any `json:"goals"`
				}
				if err := s.client.Get(ctx, "/api/tasks/"+url.PathEscape(id)+"/goals", nil, &goals); err == nil {
					out["goals"] = goals.Goals
				}

				if v := a.boolPtr("include_outcome"); v != nil && *v {
					var outcome map[string]any
					err := s.client.Get(ctx, "/api/tasks/"+url.PathEscape(id)+"/outcome", nil, &outcome)
					if err == nil {
						out["outcome"] = outcome
					} else if apiErr, ok := err.(*APIError); !ok || !apiErr.IsNotFound() {
						// A missing outcome is normal for a running task; any
						// other failure is worth reporting alongside the task.
						out["outcome_error"] = err.Error()
					}
				}
				content, err := jsonContent(out)
				return &callToolResult{Content: content}, err
			})
		},
	}
}

func (s *Server) toolCreateTask() tool {
	return tool{
		desc: toolDescriptor{
			Name:  "artex_task_create",
			Title: "Create an ARTEX task",
			Description: "Start a new autonomous penetration-testing task. `description` says what to test (the targets and any credentials), " +
				"`goal` states the objective. ARTEX's own scope rules, target gate and approval gates still apply to every action the task takes, " +
				"so this call cannot widen what the instance is authorized to do. Returns the new task's id.",
			InputSchema: map[string]any{
				"type": "object",
				"properties": map[string]any{
					"description": map[string]any{"type": "string", "description": "What the task should test: targets, accounts, any relevant context."},
					"goal":        map[string]any{"type": "string", "description": "The objective that proves the task done, e.g. 'find and verify authentication bypasses on the API'."},
					"name":        map[string]any{"type": "string", "description": "Optional short task name shown in the console."},
					"company_ids": map[string]any{
						"type":        "array",
						"items":       map[string]any{"type": "integer"},
						"description": "Company ids whose scope and assets this task inherits (see artex_assets_list).",
					},
					"seed_first_intent": map[string]any{
						"type":        "boolean",
						"description": "Issue one seed intent at creation so a worker starts immediately, skipping the first planner round. Useful for a narrow, single-work task.",
					},
					"target_gate": map[string]any{
						"type":        "boolean",
						"description": "Require human approval for each asset target the task selects. Leave unset to use the instance default.",
					},
				},
				"required":             []string{"description", "goal"},
				"additionalProperties": false,
			},
			Annotations: map[string]any{"readOnlyHint": false, "destructiveHint": false},
		},
		handler: func(_ callContext, raw json.RawMessage) (*callToolResult, error) {
			a, err := decodeArgs(raw)
			if err != nil {
				return errorResult(err), nil
			}
			description, err := a.required("description")
			if err != nil {
				return errorResult(err), nil
			}
			goal, err := a.required("goal")
			if err != nil {
				return errorResult(err), nil
			}
			body := map[string]any{"description": description, "goal": goal}
			if v := a.str("name"); v != "" {
				body["name"] = v
			}
			if v, ok := a["company_ids"]; ok && v != nil {
				body["company_ids"] = v
			}
			if v := a.boolPtr("seed_first_intent"); v != nil {
				body["seed_first_intent"] = *v
			}
			if v := a.boolPtr("target_gate"); v != nil {
				body["target_gate"] = *v
			}
			return run(func(ctx context.Context) (*callToolResult, error) {
				var created map[string]any
				if err := s.client.Post(ctx, "/api/tasks", nil, body, &created); err != nil {
					return nil, err
				}
				content, err := jsonContent(created)
				return &callToolResult{Content: content}, err
			})
		},
	}
}

func (s *Server) toolControlTask() tool {
	return tool{
		desc: toolDescriptor{
			Name:        "artex_task_control",
			Title:       "Pause or resume an ARTEX task",
			Description: "Pause a running task, or resume a paused one. Pausing stops new intents from starting; running workers finish their current step.",
			InputSchema: map[string]any{
				"type": "object",
				"properties": map[string]any{
					"task_id": map[string]any{"type": "string", "description": "The task id."},
					"action":  map[string]any{"type": "string", "enum": []string{"pause", "resume"}, "description": "pause or resume."},
				},
				"required":             []string{"task_id", "action"},
				"additionalProperties": false,
			},
			Annotations: map[string]any{"readOnlyHint": false, "idempotentHint": true},
		},
		handler: func(_ callContext, raw json.RawMessage) (*callToolResult, error) {
			a, err := decodeArgs(raw)
			if err != nil {
				return errorResult(err), nil
			}
			id, err := a.required("task_id")
			if err != nil {
				return errorResult(err), nil
			}
			action, err := a.required("action")
			if err != nil {
				return errorResult(err), nil
			}
			if action != "pause" && action != "resume" {
				return errorResult(fmt.Errorf(`"action" must be pause or resume, got %q`, action)), nil
			}
			return run(func(ctx context.Context) (*callToolResult, error) {
				var res map[string]any
				if err := s.client.Post(ctx, "/api/tasks/"+url.PathEscape(id)+"/control", nil,
					map[string]any{"action": action}, &res); err != nil {
					return nil, err
				}
				content, err := jsonContent(res)
				return &callToolResult{Content: content}, err
			})
		},
	}
}

func (s *Server) toolTaskGoals() tool {
	return tool{
		desc: toolDescriptor{
			Name:        "artex_task_goals_add",
			Title:       "Add a goal to an ARTEX task",
			Description: "Add one goal to a task and wake its planner to judge the task against it. Adding a goal revives a finished task, so use it to extend an engagement rather than to duplicate an existing goal.",
			InputSchema: map[string]any{
				"type": "object",
				"properties": map[string]any{
					"task_id":     map[string]any{"type": "string", "description": "The task id."},
					"description": map[string]any{"type": "string", "description": "The goal statement, phrased as something the task must prove or achieve."},
					"vulnclass": map[string]any{
						"type":        "string",
						"description": "Optional vulnerability class the goal targets (e.g. sqli, authz), so the planner routes work by class.",
					},
				},
				"required":             []string{"task_id", "description"},
				"additionalProperties": false,
			},
			Annotations: map[string]any{"readOnlyHint": false},
		},
		handler: func(_ callContext, raw json.RawMessage) (*callToolResult, error) {
			a, err := decodeArgs(raw)
			if err != nil {
				return errorResult(err), nil
			}
			id, err := a.required("task_id")
			if err != nil {
				return errorResult(err), nil
			}
			desc, err := a.required("description")
			if err != nil {
				return errorResult(err), nil
			}
			return run(func(ctx context.Context) (*callToolResult, error) {
				body := map[string]any{"text": desc}
				if vc := a.str("vulnclass"); vc != "" {
					body["vulnclass"] = vc
				}
				var res map[string]any
				if err := s.client.Post(ctx, "/api/tasks/"+url.PathEscape(id)+"/goals", nil, body, &res); err != nil {
					return nil, err
				}
				content, err := jsonContent(res)
				return &callToolResult{Content: content}, err
			})
		},
	}
}
