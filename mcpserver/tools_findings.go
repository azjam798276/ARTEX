package mcpserver

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"strconv"
	"strings"
)

// validFindingStatuses mirrors db.ValidFindingStatus. It is duplicated here so
// the model gets a rejection naming the allowed set before an upstream round
// trip, and so a status ARTEX later adds does not silently pass through.
var validFindingStatuses = []string{
	"pending", "in_progress", "confirmed", "resolved", "fixed",
	"false_positive", "ignored", "duplicate", "risk_accepted",
}

// --- finding tools ------------------------------------------------------

func (s *Server) toolListFindings() tool {
	return tool{
		desc: toolDescriptor{
			Name:  "artex_findings_list",
			Title: "List ARTEX findings",
			Description: "List vulnerability findings with severity, status, evidence and the assets each is anchored to. " +
				"Filter by task, severity, status or a free-text query. Findings are the platform's result; each carries the captured request/response that justifies it.",
			InputSchema: map[string]any{
				"type": "object",
				"properties": map[string]any{
					"task_id": map[string]any{
						"type":        "string",
						"description": "Only findings of this task. Omit to read the global findings table, which survives task deletion.",
					},
					"severity": map[string]any{
						"type":        "string",
						"enum":        []string{"critical", "high", "medium", "low"},
						"description": "Only findings of this severity.",
					},
					"status": map[string]any{
						"type":        "string",
						"enum":        validFindingStatuses,
						"description": "Only findings in this triage status.",
					},
					"vulnclass": map[string]any{"type": "string", "description": "Only findings of this vulnerability class, e.g. sqli or authz."},
					"query":     map[string]any{"type": "string", "description": "Free-text search over the finding summary, name and evidence."},
					"limit":     map[string]any{"type": "integer", "description": "Page size (default 20, max 200)."},
					"page":      map[string]any{"type": "integer", "description": "1-based page number (default 1)."},
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
				// The task-scoped branch answers a bare array and ignores paging;
				// the global branch answers {items,total,page,page_size} only
				// when page or limit is present. Always send both, so one shape
				// comes back and the caller can page either branch.
				limit := a.intOr("limit", 20)
				if limit <= 0 {
					limit = 20
				}
				if limit > 200 {
					limit = 200
				}
				page := a.intOr("page", 1)
				if page < 1 {
					page = 1
				}
				q := url.Values{}
				q.Set("page", strconv.Itoa(page))
				q.Set("limit", strconv.Itoa(limit))
				if v := a.str("task_id"); v != "" {
					// `task` selects the per-task node branch, which returns a
					// bare array; `task_id` filters the global table and keeps
					// the paged envelope. Prefer the paged one.
					q.Set("task_id", v)
				}
				for _, k := range []string{"severity", "status", "vulnclass"} {
					if v := a.str(k); v != "" {
						q.Set(k, v)
					}
				}
				if v := a.str("query"); v != "" {
					q.Set("q", v)
				}

				var out map[string]any
				if err := s.client.Get(ctx, "/api/exploration/findings", q, &out); err != nil {
					return nil, err
				}
				content, err := jsonContent(out)
				return &callToolResult{Content: content}, err
			})
		},
	}
}

func (s *Server) toolGetFinding() tool {
	return tool{
		desc: toolDescriptor{
			Name:        "artex_finding_get",
			Title:       "Read one ARTEX finding",
			Description: "Read one finding in full: the detailed Markdown report, the evidence quote, verification status and the traffic bindings that point at the captured request/response. Use this before quoting a finding to a person.",
			InputSchema: map[string]any{
				"type": "object",
				"properties": map[string]any{
					"finding_id": map[string]any{
						"type":        "string",
						"description": "The node id (or standalone finding id) as returned in a finding's `id` / `finding_id` field.",
					},
					"include_lineage": map[string]any{
						"type":        "boolean",
						"description": "Also return the finding's lineage in the exploration graph (what it descends from and what it led to).",
					},
				},
				"required":             []string{"finding_id"},
				"additionalProperties": false,
			},
			Annotations: map[string]any{"readOnlyHint": true, "idempotentHint": true},
		},
		handler: func(_ callContext, raw json.RawMessage) (*callToolResult, error) {
			a, err := decodeArgs(raw)
			if err != nil {
				return errorResult(err), nil
			}
			id, err := a.required("finding_id")
			if err != nil {
				return errorResult(err), nil
			}
			return run(func(ctx context.Context) (*callToolResult, error) {
				path := "/api/exploration/findings/" + url.PathEscape(id)
				var finding map[string]any
				if err := s.client.Get(ctx, path, nil, &finding); err != nil {
					return nil, err
				}
				out := map[string]any{"finding": finding}
				if v := a.boolPtr("include_lineage"); v != nil && *v {
					var lineage map[string]any
					if err := s.client.Get(ctx, path+"/lineage", nil, &lineage); err == nil {
						out["lineage"] = lineage
					} else {
						out["lineage_error"] = err.Error()
					}
				}
				content, err := jsonContent(out)
				return &callToolResult{Content: content}, err
			})
		},
	}
}

func (s *Server) toolSetFindingStatus() tool {
	return tool{
		desc: toolDescriptor{
			Name:  "artex_finding_set_status",
			Title: "Set ARTEX finding status",
			Description: "Triage one finding: set its status (confirmed, false_positive, resolved, …). " +
				"Setting false_positive or duplicate is a claim about the evidence, so read the finding first and say why in your reply. " +
				"Returns how many rows changed and which ids were missing.",
			InputSchema: map[string]any{
				"type": "object",
				"properties": map[string]any{
					"finding_ids": map[string]any{
						"type":        "array",
						"items":       map[string]any{"type": "integer"},
						"description": "Standalone finding ids to update (the `finding_id` field, which is an integer, not the node `id`).",
					},
					"status": map[string]any{
						"type":        "string",
						"enum":        validFindingStatuses,
						"description": "The new status.",
					},
				},
				"required":             []string{"finding_ids", "status"},
				"additionalProperties": false,
			},
			Annotations: map[string]any{"readOnlyHint": false, "idempotentHint": true},
		},
		handler: func(_ callContext, raw json.RawMessage) (*callToolResult, error) {
			a, err := decodeArgs(raw)
			if err != nil {
				return errorResult(err), nil
			}
			status, err := a.required("status")
			if err != nil {
				return errorResult(err), nil
			}
			if !isFindingStatus(status) {
				return errorResult(fmt.Errorf(`"status" must be one of %s, got %q`,
					strings.Join(validFindingStatuses, ", "), status)), nil
			}
			ids, err := intList(a["finding_ids"])
			if err != nil {
				return errorResult(err), nil
			}
			if len(ids) == 0 {
				return errorResult(fmt.Errorf(`"finding_ids" must contain at least one positive integer`)), nil
			}
			return run(func(ctx context.Context) (*callToolResult, error) {
				var res map[string]any
				if err := s.client.Post(ctx, "/api/exploration/findings/bulk-status", nil,
					map[string]any{"ids": ids, "status": status}, &res); err != nil {
					return nil, err
				}
				content, err := jsonContent(res)
				return &callToolResult{Content: content}, err
			})
		},
	}
}

func isFindingStatus(s string) bool {
	for _, v := range validFindingStatuses {
		if v == s {
			return true
		}
	}
	return false
}

// intList decodes a JSON array of integers. JSON numbers arrive as float64
// through encoding/json's default path, and a model occasionally sends "12",
// so both are accepted; a non-integral number is rejected rather than truncated.
func intList(v any) ([]int64, error) {
	if v == nil {
		return nil, nil
	}
	arr, ok := v.([]any)
	if !ok {
		return nil, fmt.Errorf(`expected an array of integers`)
	}
	out := make([]int64, 0, len(arr))
	for _, item := range arr {
		switch n := item.(type) {
		case float64:
			if n != float64(int64(n)) {
				return nil, fmt.Errorf("expected integers, got %v", n)
			}
			out = append(out, int64(n))
		case string:
			i, err := strconv.ParseInt(strings.TrimSpace(n), 10, 64)
			if err != nil {
				return nil, fmt.Errorf("expected integers, got %q", n)
			}
			out = append(out, i)
		default:
			return nil, fmt.Errorf("expected integers, got %T", item)
		}
	}
	return out, nil
}
