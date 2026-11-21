package mcpserver

import (
	"context"
	"encoding/json"
	"net/url"
	"strconv"
)

// --- asset tools -------------------------------------------------------

func (s *Server) toolListAssets() tool {
	return tool{
		desc: toolDescriptor{
			Name:  "artex_assets_list",
			Title: "List ARTEX assets",
			Description: "Query the shared asset graph: domains, subdomains, IPs, ports, sites and endpoints the instance knows about, optionally scoped to a task or company. " +
				"Use it to find what a task may test before proposing targets.",
			InputSchema: map[string]any{
				"type": "object",
				"properties": map[string]any{
					"type": map[string]any{
						"type":        "string",
						"description": "Asset type, e.g. root_domain, subdomain, ip, port, site, endpoint. Defaults to root_domain when no dsl is given.",
					},
					"company_id": map[string]any{"type": "integer", "description": "Only assets in this company's scope."},
					"task_id":    map[string]any{"type": "integer", "description": "Only assets attached to this task."},
					"dsl": map[string]any{
						"type":        "string",
						"description": "Optional ARTEX asset DSL filter, e.g. `domain ~= \"example.com\"`. When set it takes precedence over type scoping.",
					},
					"limit":  map[string]any{"type": "integer", "description": "Page size (default 50)."},
					"offset": map[string]any{"type": "integer", "description": "Rows to skip (default 0)."},
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
				q := url.Values{}
				if v := a.str("type"); v != "" {
					q.Set("type", v)
				}
				if v := a.str("dsl"); v != "" {
					q.Set("dsl", v)
				}
				if v := a.intOr("company_id", 0); v > 0 {
					q.Set("company_id", strconv.Itoa(v))
				}
				if v := a.intOr("task_id", 0); v > 0 {
					q.Set("task_id", strconv.Itoa(v))
				}
				if v := a.intOr("limit", 50); v > 0 {
					q.Set("limit", strconv.Itoa(v))
				}
				if v := a.intOr("offset", 0); v > 0 {
					q.Set("offset", strconv.Itoa(v))
				}
				var out map[string]any
				if err := s.client.Get(ctx, "/api/assets", q, &out); err != nil {
					return nil, err
				}
				content, err := jsonContent(out)
				return &callToolResult{Content: content}, err
			})
		},
	}
}

func (s *Server) toolAssetCounts() tool {
	return tool{
		desc: toolDescriptor{
			Name:        "artex_assets_counts",
			Title:       "Count ARTEX assets by type",
			Description: "Return how many assets of each type the instance holds, optionally for one task. Cheaper than paging artex_assets_list to count.",
			InputSchema: map[string]any{
				"type": "object",
				"properties": map[string]any{
					"task_id": map[string]any{"type": "integer", "description": "Count only this task's assets. Omit for the whole instance."},
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
				q := url.Values{}
				if v := a.intOr("task_id", 0); v > 0 {
					q.Set("task_id", strconv.Itoa(v))
				}
				var out map[string]any
				if err := s.client.Get(ctx, "/api/assets/counts", q, &out); err != nil {
					return nil, err
				}
				content, err := jsonContent(out)
				return &callToolResult{Content: content}, err
			})
		},
	}
}

// --- skill tools -------------------------------------------------------

// maxSkillFileBytes bounds one skill file read. A skill is prompt material, and
// a megabyte of it would crowd out the conversation it is meant to serve.
const maxSkillFileBytes = 256 << 10

func (s *Server) toolListSkills() tool {
	return tool{
		desc: toolDescriptor{
			Name:  "artex_skills_list",
			Title: "List ARTEX skills",
			Description: "List the skills installed on the instance: reusable pentest playbooks (SKILL.md bundles with scripts and references). " +
				"Each entry names its description, the MCP servers it unlocks and the files it carries.",
			InputSchema: map[string]any{
				"type":                 "object",
				"properties":           map[string]any{},
				"additionalProperties": false,
			},
			Annotations: map[string]any{"readOnlyHint": true, "idempotentHint": true},
		},
		handler: func(_ callContext, raw json.RawMessage) (*callToolResult, error) {
			if _, err := decodeArgs(raw); err != nil {
				return errorResult(err), nil
			}
			return run(func(ctx context.Context) (*callToolResult, error) {
				var out map[string]any
				if err := s.client.Get(ctx, "/api/skills", nil, &out); err != nil {
					return nil, err
				}
				content, err := jsonContent(out)
				return &callToolResult{Content: content}, err
			})
		},
	}
}

func (s *Server) toolGetSkill() tool {
	return tool{
		desc: toolDescriptor{
			Name:  "artex_skill_read",
			Title: "Read one ARTEX skill",
			Description: "Read a skill's SKILL.md (or one of its reference/script files) verbatim, so its procedure can be followed here. " +
				"Pass `file` to read something other than SKILL.md.",
			InputSchema: map[string]any{
				"type": "object",
				"properties": map[string]any{
					"name": map[string]any{"type": "string", "description": "The skill's directory name, as listed by artex_skills_list."},
					"file": map[string]any{
						"type":        "string",
						"description": "Relative path inside the skill, e.g. `reference.md` or `scripts/extract.py`. Defaults to SKILL.md.",
					},
				},
				"required":             []string{"name"},
				"additionalProperties": false,
			},
			Annotations: map[string]any{"readOnlyHint": true, "idempotentHint": true},
		},
		handler: func(_ callContext, raw json.RawMessage) (*callToolResult, error) {
			a, err := decodeArgs(raw)
			if err != nil {
				return errorResult(err), nil
			}
			name, err := a.required("name")
			if err != nil {
				return errorResult(err), nil
			}
			file := a.str("file")
			if file == "" {
				file = "SKILL.md"
			}
			return run(func(ctx context.Context) (*callToolResult, error) {
				path := "/api/skills/" + url.PathEscape(name) + "/files/" + escapePathSegments(file)
				var out struct {
					Content string `json:"content"`
					File    string `json:"file"`
				}
				if err := s.client.Get(ctx, path, nil, &out); err != nil {
					return nil, err
				}
				if len(out.Content) > maxSkillFileBytes {
					return &callToolResult{
						Content: textContent(out.Content[:maxSkillFileBytes] +
							"\n\n[truncated: this skill file exceeds 256 KiB; read it in pieces or rely on its SKILL.md]"),
					}, nil
				}
				return &callToolResult{Content: textContent(out.Content)}, nil
			})
		},
	}
}

// escapePathSegments percent-escapes each segment of a slash-separated skill
// file path, keeping the slashes. The route is `{file...}`, so a nested path
// must stay one path, but a segment carrying its own separator must not split.
func escapePathSegments(p string) string {
	out := ""
	for i, seg := range splitSlash(p) {
		if i > 0 {
			out += "/"
		}
		out += url.PathEscape(seg)
	}
	return out
}

func splitSlash(p string) []string {
	segs := []string{}
	cur := ""
	for _, r := range p {
		if r == '/' {
			segs = append(segs, cur)
			cur = ""
			continue
		}
		cur += string(r)
	}
	segs = append(segs, cur)
	return segs
}
