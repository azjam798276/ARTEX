---
name: artex-mcp
description: "Operate an ARTEX autonomous penetration-testing instance through its MCP server: create and control engagement tasks, read findings and their evidence, and query the shared asset graph. Invoke when the task is to start, steer, monitor or report an authorized security engagement that ARTEX runs, rather than to test a target directly with local tools."
---

# ARTEX over MCP

ARTEX is a self-hosted, LLM-driven penetration-testing platform: a planner splits a goal into
intents, workers execute them with real tools, and every finding stays bound to the captured
request and response that justify it. This skill drives one instance through its MCP server
(`artex -mcp-serve`), whose tools are named `mcp__artex__<tool>`.

You do not test targets here. You commission, steer, read and report work that ARTEX performs. The
instance's scope rules, target gate, approval gates and budgets still apply to every action its
agents take, so nothing in this skill widens what the deployment is authorized to do.

## 1. Read before you write

Start every engagement by reading the current state. Calls are cheap and the answers carry the ids
the write tools need.

- `artex_tasks_list` — tasks with status, goal progress, in-flight workers and finding counts by
  severity. Takes `status` and `limit`.
- `artex_task_get` — one task in full, plus its goals. Pass `include_outcome: true` to read the
  closing report once the task is done.
- `artex_findings_list` — findings filtered by `task_id`, `severity`, `status`, `vulnclass` or a
  free-text `query`. Returns a paged envelope with `total`.
- `artex_finding_get` — one finding's detailed report, evidence, verification status and traffic
  bindings.
- `artex_assets_list` / `artex_assets_counts` — the asset graph, optionally scoped by `company_id`
  or `task_id`. Counts first; page the list only when you need the rows.

An `isError: true` result is a tool-level failure you are expected to read and act on, not a crash:
a rejected token says so in its text, and a 404 usually means the id was wrong or the resource does
not exist yet (for example, the outcome of a task that is still running).

## 2. Commission work

`artex_task_create` takes `description` (what to test: targets, accounts, context) and `goal` (the
objective that proves the task done). Both are required.

- Write the goal as something that can be proven, not as an activity: "find and verify
  authentication bypasses on the customer API" rather than "test the API".
- Put credentials and target lists in `description`; ARTEX tracks them as task credentials.
- Pass `company_ids` to inherit a company's scope and a snapshot of its assets instead of retyping
  targets. Read `artex_assets_counts` first when you need to choose.
- `seed_first_intent: true` issues one seed intent at creation and skips the first planner round.
  Use it for a narrow task one work can finish; leave it unset for a broad engagement.
- `target_gate: true` requires a person to approve each asset target the task selects. Choose it
  when the scope is new or ambiguous rather than assuming approval.

The call returns the new task, including its `id`. Report that id to whoever asked; it is the handle
for everything that follows.

## 3. Steer without thrashing

- `artex_task_control` with `pause` or `resume`. Pausing stops new intents from starting; running
  workers finish their current step. Prefer pause over tearing a task down: a paused task keeps its
  graphs and resumes where it stopped.
- `artex_task_goals_add` extends a task with one more goal and wakes its planner to judge the task
  against it. Adding a goal revives a finished task, so use it to widen an engagement — never to
  restate a goal that already exists, and never in a loop.
- Do not poll in a tight loop. A task advances on its own; check `artex_task_get` between other
  steps rather than sleeping on it.

## 4. Report honestly

A finding's `severity` is ARTEX's, and its `status` is a triage verdict, not a proof. Before
quoting a finding to a person:

1. `artex_finding_get` it and read the report and evidence.
2. Confirm the traffic binding actually shows what the report claims.
3. Say which part is verified and which part is still open.

`artex_finding_set_status` changes the triage state. `false_positive` and `duplicate` are claims
about evidence: only set them after reading the finding, and say why in the reply that reports the
change. Use `confirmed` for a finding whose evidence you re-checked, and `resolved` only once the
fix is verified — the two are different claims.

Quote counts as counts, not as coverage: three confirmed high findings do not mean the target is
free of high findings.

## 5. Skills on the instance

ARTEX also stores reusable playbooks. `artex_skills_list` names them with their descriptions and the
MCP servers each unlocks; `artex_skill_read` returns one `SKILL.md` (or a `file` inside it) verbatim.
Read one when its procedure matches the engagement — for example `api-security` for an authorized API
assessment, or `patch-diff` when the goal is recovering a vulnerability from a vendor fix. Do not
assume a skill's tooling exists locally: each playbook names the tools it expects, and the instance,
not this session, is where they run.
