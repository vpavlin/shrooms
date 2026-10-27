# 072. A session can start fresh: by its owner always, by other agents only where allowed

**Status:** accepted 2026-10-27 by the owner ("the button for me, and then a way
for other agents tasking an agent"; "never allow another agent to wipe this
session, only I can"; archived work pruned); built the same day.

## Context

proteus/auditor runs one audit after another in a single conversation. On
2026-10-27 it carried about 560,000 tokens of earlier audits into each turn:

- **Cost:** every turn paid for all of them.
- **Bias:** one audit's findings and assumptions colouring the next.
- **Confidentiality:** one project's details sat in context while it read
  another's code.

Its working directory held the clones and reports of four earlier audits
(1.1 GB), beside what it needs every time: its audit-lab keys (`.lab`) and
scripts (`lab`).

The only way to start over was to delete the session and make it again under
the same name. That lost its settings, role and event log, and had to be done
by hand.

## Decision

**A session can be started on a fresh conversation: the same name, cage,
model, settings and role, none of the old context. Its owner can always do it.
Another agent can ask for it with a new task, only on a session whose owner
allowed that, and only while nothing else is open there.**

- **The owner:** "new conversation" in both apps, `POST /v1/sessions/{name}/new`.
  It's refused while the session works, has messages waiting, or has a task
  open, so nothing in flight is cut off. A caged caller is refused; it asks
  the other way.
- **Another agent:** a new A2A task whose message carries `shrooms/fresh: true`
  (`ask_agent`'s `fresh`, `shrooms-agent a2a send --fresh`). It's honoured
  only when the session's `fresh_by_agents` is on. That setting is off by
  default and only its owner sets it; a caged agent can't change settings
  (ADR-044). Otherwise the task is refused with why (`-32052`), never run in
  the old conversation instead.
  - **Not on every task.** Agents also just talk. A task without the mark
    goes on in the current conversation, and replies on an open task always
    go to the conversation it started in. So the caller decides per task,
    and only where the owner opened the session to it.
  - **Not for a held task** (ADR-070): those wait for the owner.
- **The conversation:** the session forgets its harness's conversation id, so
  its next message starts a new one. The old one stays in the harness's
  transcripts and in the session's events, where a `new_conversation` event
  marks the break.
- **The working directory**, for a caged session only. An uncaged session's
  directory may be its owner's home (proteus/proteus runs in
  `/home/vpavlin`), and is never touched.
  - **What goes:** everything is moved to
    `~/.local/share/shrooms-agent/archive/<session>/<UTC time>/`, outside
    every cage.
  - **What stays:** dot-entries (`.lab`, `.venv`, `.cache`: where tools and
    their settings live), `AGENTS.md`, `CLAUDE.md`, `.keep`, and what `.keep`
    lists, one name a line.
  - **The cage is not touched**, so whatever was installed in it stays.
  - What a cage wrote belongs to a subordinate uid that the agent's user can't
    move to another directory; such entries are moved inside podman's user
    namespace.
- **Archives are pruned after 30 days,** at each fresh start and each time
  the agent starts.

## Consequences

- An auditor can be tasked "fresh" per audit by whoever asks for audits, once
  its owner ticks the box. A session nobody opened, this one included, can't
  be wiped by another agent.
- The owner's own button needs no setting. Over the mesh, "the owner" is any
  device the API serves that isn't a cage: another machine's uncaged agent
  can call the endpoint too, as it can already delete a session. Telling the
  owner's devices from other agents is the open question in the secrets
  review, not settled here.
- `.keep` is per session, in its own directory. A session that keeps
  something big outside a dot-entry has to list it, or lose it to the archive
  for 30 days.
