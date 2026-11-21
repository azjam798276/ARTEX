# Using ARTEX from an external agent (MCP server mode)

ARTEX is normally driven from its own console: you create a task, its planner splits the goal into
intents, and its workers run the tools. That remains the primary loop.

This document covers the second loop. `artex -mcp-serve` turns an instance into a **Model Context
Protocol server over stdio**, so an agent that already speaks MCP — a DeepSeek Harness session, a
dsh-superagent worker, or any other MCP client — can commission, steer and read engagements. The
motivating case is a coding agent that notices a security question while doing ordinary work and
should be able to hand the authorized testing to ARTEX rather than attempt it with local tools.

Nothing here widens what an instance is allowed to do. The MCP server is a client of the same HTTP
API the console uses, with the same authorization: the instance's scope rules, target gate, approval
gates, argv rules and budgets apply to every intent its agents run, whoever asked for the task.

## What the server exposes

Twelve tools in four groups. Read the descriptions with `tools/list` for the argument schemas; the
shape is stable enough to summarize:

| Tool | Does |
| --- | --- |
| `artex_tasks_list` | Tasks with status, goal progress, in-flight workers, finding counts. Filter by `status`. |
| `artex_task_get` | One task plus its goals; `include_outcome` adds the closing report. |
| `artex_task_create` | Create a task from `description` + `goal`; optional `company_ids`, `seed_first_intent`, `target_gate`. |
| `artex_task_control` | `pause` or `resume`. |
| `artex_task_goals_add` | Add a goal and wake the planner to judge the task against it. |
| `artex_findings_list` | Findings by `task_id`, `severity`, `status`, `vulnclass` or free-text `query`, paged. |
| `artex_finding_get` | One finding's report, evidence, verification status and traffic bindings. |
| `artex_finding_set_status` | Triage: set `confirmed`, `false_positive`, `resolved`, … on a list of ids. |
| `artex_assets_list` | The asset graph, by `type`, `company_id`, `task_id` or a `dsl` filter. |
| `artex_assets_counts` | Counts by asset type, for one task or the whole instance. |
| `artex_skills_list` | Installed playbooks with descriptions, unlocked MCP servers and files. |
| `artex_skill_read` | One `SKILL.md` (or a `file` inside it) verbatim. |

Tools are named `mcp__artex__<tool>` once the client prefixes them with the server namespace.

## Getting a token

The server authenticates as a bearer-token caller. A request that changes state with only the
console *cookie* is refused from a foreign origin by `requireAuth`, which is why the facade needs a
token rather than a session cookie.

A token is a 7-day HS256 JWT signed with the instance's `jwt.key`, so it can be obtained two ways:

```bash
# Scriptable: uses the instance's own login endpoint.
ARTEX_PASSWORD=... ./scripts/mint-mcp-token.py --url http://127.0.0.1:8787 --print-env
export ARTEX_API_TOKEN=...            # the script prints this line with --print-env
```

or, if you are already signed in to the console, copy `localStorage.artex_token` from the browser's
developer tools. Both produce the same kind of token; neither needs the signing key.

Keep the token out of the process list. The server reads it from an environment variable
(`-mcp-token-env`, default `ARTEX_API_TOKEN`) and `-mcp-token` exists only for a one-off local check.
A token in `ps` output or a shell history is a leaked credential valid for a week.

## Running it

Start the backend as usual, then point any MCP client at the binary:

```bash
export ARTEX_API_TOKEN=...
artex -mcp-serve -mcp-api-url http://127.0.0.1:8787
```

It speaks newline-delimited JSON-RPC on stdin/stdout, negotiates protocol `2025-11-25` (and accepts
older revisions, including the `2024-11-05` that some clients still request), prints one banner line
on **stderr**, and exits 0 when the client closes the pipe. stdout carries protocol messages only.

| Flag | Meaning |
| --- | --- |
| `-mcp-serve` | Serve over stdio and exit; the backend is not started. |
| `-mcp-api-url` | Base URL of the running instance (default `http://127.0.0.1:8787`). |
| `-mcp-token-env` | Environment variable holding the token (default `ARTEX_API_TOKEN`). |
| `-mcp-token` | The token itself; prefer the variable. |
| `-mcp-insecure` | Skip TLS verification, for a self-signed HTTPS URL on loopback. |

The server opens no database connection and needs no key directory, so it can run from a small
container beside a remote instance just as well as next to a local one.

## Client configuration

Claude Desktop, Cursor and most MCP clients take a JSON block:

```json
{
  "mcpServers": {
    "artex": {
      "command": "/opt/artex/artex",
      "args": ["-mcp-serve", "-mcp-api-url", "http://127.0.0.1:8787"],
      "env": { "ARTEX_API_TOKEN": "<token>" }
    }
  }
}
```

DeepSeek Harness uses the `dsh-mcp-client` plugin; the `dsh-superagent` integration is documented in
that project's `docs/009-artex.md`, and `examples/artex.cordis.patch.yml` there is a ready fragment.

## The skill for the other side

This fork ships `skills/artex-mcp/SKILL.md`: the operator's brief for an agent driving ARTEX — read
before you write, write goals that can be proven, steer without thrashing, and never quote a finding
as verified without checking its evidence. Copy it (or symlink the `skills/` directory) into the
client's skill root; agents that support skills pick it up without further configuration, and agents
that do not can be pointed at the file directly.

## Failure modes worth knowing

- **HTTP 401** — the token is invalid or expired (7 days). The tool result says so explicitly rather
  than returning a bare status.
- **HTTP 404 on a task** — the id is wrong, or the resource does not exist yet. A task's `outcome` is
  legitimately missing while the task runs; the tool reports the task without the outcome rather
  than failing.
- **Tool results marked `isError`** — argument and upstream problems are tool-level errors the model
  is meant to read and correct. Only a malformed JSON-RPC envelope is a protocol error.
- **Slow findings pages** — a large engagement's findings page can take seconds. The client's default
  per-request timeout is 60 s; set `toolCallTimeoutMs` in the MCP client if your network is slower.
