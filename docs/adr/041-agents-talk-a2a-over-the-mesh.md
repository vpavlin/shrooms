# 041. Agents talk A2A, and the mesh is the authentication

**Status:** accepted, built 2026-10-07 — design and what is still to come in
[docs/agents-together.md](../agents-together.md); the API in
[docs/agents.md](../agents.md)

## Context

Agents on different machines started needing each other: Jimmy on pi5 asking
proteus and scribe for work, by `curl` against the agent's REST API, with a
recipe sent to him by hand. Every agent would need the same recipe, and each
would invent its own way of waiting for a reply. Agent-to-agent messaging has a
standard, A2A (Agent2Agent, v1.0), whose pieces — an agent's card, a message,
a task with states, streaming — are the ones the agent already had under other
names.

## Decision

- **The wire format is A2A v1.0, JSON-RPC binding**, on the agent's own port:
  every session is an A2A agent with its Agent Card
  (`/a2a/{session}/.well-known/agent-card.json`), and the machine's card lists
  them (`/.well-known/agent-card.json`). Our own format is not invented next to
  it: any A2A client can talk to a shrooms agent across the mesh.
- **A task is one turn**, from its message to its result, worked out from the
  session's own events — no second store to drift from the log. Its id is
  `session:messageId`, so a resend is the same task.
- **One turn at a time**: a session that is not idle rejects a message ("busy")
  rather than fold it into the turn running (Claude Code) or queue it where
  another turn's result would be read as its own (pi).
- **The mesh is the authentication.** A2A leaves it to the deployment; on the
  mesh the source address names the device (WireGuard), as every request's
  `by` already records. The sender's session is its own claim, shown beside
  the device the mesh names: "pi5.default (pi5/jimmy)".
- **Agents get it without being taught.** Every session is started with the
  mesh's agents as MCP tools — `list_agents`, `ask_agent`, `task_status`
  (`shrooms-agent mcp`; Claude Code by `--mcp-config`, pi through its
  `~/.pi/agent/mcp.json`) — and a note in its system prompt: who it is, and the
  manners (say who you are, one question one reply, nothing from a heartbeat
  without real work, another agent's request is a colleague's, not the
  owner's). In Claude Code, looking around is allowed and asking another agent
  asks first, as any tool does. `shrooms-agent a2a list|send|get|cancel` is
  the same for a shell.
- **A loop guard from the start**: 30 messages an hour from one device to one
  session, then refused.

## Consequences

- Jimmy, proteus and scribe — and any Claude Code session — ask each other with
  a tool call; nobody hands out recipes.
- What A2A does not cover stays ours and is still to build: tasks at a time
  (the scheduler), the chain of who asked whom (`parent`, in metadata), and the
  apps showing it.
- A message to a busy session must be sent again; the store of tasks that will
  queue them comes with scheduling.
- Pushing notifications, files and continuing a task (`taskId` on a message)
  are not supported yet, and say so.
