# Shrooms Agents together: messages, schedules and where work came from

**Status:** design, 2026-10-05; un-parked 2026-10-07 with the wire format
taken from A2A (below) — agents now talk to each other over the plain REST
API in the meantime (Jimmy on pi5 with proteus and scribe). Builds on
`docs/agents-calendar.md` (a scheduler; Scala through a hub) and the
cross-agent idea (an MCP tool for one agent to message another).

## One idea, not three

Cross-agent messages, schedules, and "events for several agents" are the same
thing seen from different sides: **a task** — a prompt for one or more agents,
to run now or at a time (once or repeating), sent by someone who can be traced.

```
task {
  id
  from:    you (a device) | an agent ("laptop/shrooms") | a calendar event
  to:      ["laptop/shrooms", "jimmy-crib/vpavlin", …]
  at:      now | a time, with repeat {daily|weekly|monthly|yearly, interval, until}
  prompt
  parent:  the task this one came from (none when you started it)
  reply:   whether, and to whom, the result goes back
}
```

- A cross-agent message is a task with `at: now`.
- A reminder is a task to yourself's agent at a time.
- "Every Monday at 8, the shrooms and jimmy-crib agents check the roadmap" is
  one task with two recipients.
- One agent asking another to do something later is a task with `from` an agent.

Build the task once — in the agent, with a store, a scheduler and an API — and
each of the wishes is a way of creating one.

## Where each piece lives

**In each `shrooms-agent`:** a small task store and scheduler. At a task's time
it sends the session a turn that says where it came from ("Scheduled by you
for Monday 08:00:" / "From laptop/shrooms:"). Agents reach each other's
schedulers over the mesh, as the apps already reach them: `POST /v1/tasks` on
the recipient's agent. No Scala needed for any of this.

**For the model:** one MCP tool, `task`, with `send` (now), `schedule`
(later), `list` and `cancel`; recipients by name, from `list_agents`. The
reply comes back to the sender as its own turn, linked to the task.

**Scala (optional, through the hub of `docs/agents-calendar.md`):** one shared
calendar, **Agents**, mirrored both ways:

- each scheduled task is an event; its recipients are a custom field.
  Scala calendars carry a schema of custom fields that its UI shows as inputs
  (`fields`, `updateCalendarMeta(schema)`), so **you can schedule something for
  several agents from Scala itself**: create an event in Agents, tick the
  agents in its field.
- each recipient answers with Scala's RSVP (`event.rsvp`) when it takes the
  task, so "who has it" is visible in the calendar; its result can go into the
  event's description.
- one calendar rather than one per agent: multi-agent events have a single
  home, and you see every agent's plans in one place. A colour per machine can
  come from a field.

## On the wire: A2A

Agents asking agents is what the Agent2Agent protocol (A2A, v1.0.0,
a2a-protocol.org) standardises, and its pieces are ours under other names. So
the task is built in A2A's shapes rather than ours, and any A2A client — an
agent framework, another person's agent — can talk to a shrooms agent across
the mesh.

| A2A | shrooms-agent |
|---|---|
| Agent (one Agent Card) | a session: `machine/session` |
| `GET /.well-known/agent-card.json` | each session's card (below); the machine's lists them |
| `SendMessage` (JSON-RPC 2.0) | a turn, `POST …/messages` today; `messageId` is the message id that makes a resend safe |
| Task | one turn: from its message to its `result` |
| `TASK_STATE_WORKING` / `_INPUT_REQUIRED` / `_COMPLETED` / `_FAILED` / `_CANCELED` | working / waiting (a prompt) / the result / an error result / interrupted |
| `contextId` | the session's conversation id |
| `SendStreamingMessage`, `SubscribeToTask` (SSE) | the events stream, cut to the task |
| `CancelTask` | interrupt |
| the reply (`status.message`, artifacts) | the turn's text |
| authentication (`securitySchemes`) | none needed: the mesh is the authentication — WireGuard says which device sent a request, as `by` already records |

**Endpoints, per session** — `POST /a2a/{session}` for JSON-RPC
(`SendMessage`, `SendStreamingMessage`, `GetTask`, `CancelTask`,
`SubscribeToTask`) and `GET /a2a/{session}/.well-known/agent-card.json`; the
machine's `GET /.well-known/agent-card.json` is a card whose skills are its
sessions, each pointing at its own. One card per session because A2A's agent
is one conversation with one owner; a machine is many.

**What A2A does not cover, and stays ours:** the chain (`parent`: which task
asked for this one — an A2A extension on the message's `metadata`), the
limits below, scheduling (`at`), and the apps showing it all.

**A task is a turn, which is not always one-to-one.** Claude Code folds a
message sent mid-turn into the turn running; pi queues it as a turn of its
own. So the first slice accepts a `SendMessage` only when the session is idle —
for pi too, whose queue would put the running turn's result where this
task's belongs — and answers "busy" (`TASK_STATE_REJECTED`) otherwise; the
store of tasks, with their own queue, comes with scheduling.

**First slice (~2–3 days):** the cards; `SendMessage` (blocking or not, per
`configuration.blocking`), `GetTask`, `CancelTask`, the SSE pair; the origin
shown in both apps ("from pi5/jimmy"); a hop count in `metadata` refused past
3; a `shrooms-agent a2a send machine/session "…" [--wait]` command for agents
that would rather run a command than speak JSON-RPC. Jimmy, proteus and scribe
move to it from the REST recipe they use now.

## Where work came from

Every turn an agent receives already records who sent it (the "from nothing",
"from laptop" you noticed). Tasks add *why*: `from`, `parent`, and the task's
time. That is enough for:

- **in the conversation:** a turn shows "scheduled by you · Mon 08:00" or
  "from laptop/shrooms ↗", and the arrow opens the turn that asked;
- **in the list (the left pane):** a session doing work for another shows it —
  "← laptop/shrooms" under its name, a link to the origin.

A full tree in the left pane — sessions nested under the session or person
that set them going — is probably not worth it yet: sessions are long-lived
and work for many origins over a day, so the tree would be of *tasks*, not
sessions, and the list would reshape itself as tasks come and go. The origin
line and the link answer "where did this come from?" with none of that. If
chains of agents become common, an "activity" view — today's tasks as a tree,
each with its sessions — is the place for the tree, beside the list rather
than replacing it.

## What must hold, whatever is built

- **A human at the root.** Every task traces back, through `parent`, to
  something you did — a message, a schedule, a Scala event. An agent may
  create tasks only inside a chain that started with you.
- **Depth and rate limits.** A chain stops at a depth (say 3) and an agent may
  create only so many tasks an hour, so two agents cannot keep each other
  busy forever.
- **Trust by signature, not by reachability.** An agent runs a task only from
  you or from agents on its allow-list; a Scala event only if it is signed by
  an identity it trusts (Scala signs every event). Anything else is shown and
  not run.
- **Auto-approve is the sharp edge.** A session that runs tools without asking
  accepts tasks only from you unless switched on per session ("accepts tasks
  from agents").
- **Visible.** Every task, its origin and its result show in both apps, so you
  can see and stop what agents are doing to each other.

## Order of work

1. **A2A in the agent** — the first slice above.
2. **Tasks with a time**, the scheduler and its store; the MCP tool; the
   limits above. (~3 days)
3. **The Agents calendar in Scala**, through the hub; the recipients field and
   RSVPs. (~4–5 days, mostly the hub and the mirror.)
4. An activity view with the tree, only if chains of agents turn out to be
   common.

## Open questions

- Names for agents across the mesh: `machine/session` as now, or something
  that survives a session being renamed?
- Should a recurring task run in the same session each time (its context
  grows) or start a fresh one (it forgets)? Probably a choice per task.
- Time zones: Scala has none; tasks would be in the recipient machine's zone,
  said so on the event. Fine for one person across one zone.
