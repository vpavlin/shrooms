# 047. The tasks board lives in the agents' own view, read from the agents

**Status:** accepted 2026-10-09, being built — the first slices are in
`basecamp-agents/Main.qml`; reviewed by laptop/shrooms

## Context

Shrooms Agents already draws the fleet: a card per session, and a dashed link
from an agent to another working on a task it asked for. **Those links ARE
tasks.** The board was reading them the long way round: a separate hub copied
each task's state out of the agents' stores on a timer, and the board read the
hub's copy.

That copy was the problem, three times over:

- **It lagged.** A task could start and finish inside one poll, and the board
  never showed it at all.
- **It was a second authority.** Two copies of "what state is this task in" is
  one more than there needs to be, and the two can disagree.
- **It could not open a session.** The one thing a person wants from a task is
  the conversation it arrived in, and only the view that draws sessions can go
  there.

Meanwhile each machine's agent answers `GET /v1/tasks`, and the core that
already polls every agent **already fetches it** — in the same pass as the
session list (`basecamp/core/src/shrooms_agents.cpp`). The view has had the
tasks in hand all along as `host.tasks`.

## Decision

**The tasks board is the agents' own view, read from the agents.**

- **One fetch, where it already happens.** The core fetches each machine's
  tasks alongside its sessions. No new poll, no new cadence, no new port.
- **A link carries its tasks.** One entry per pair of sessions: how many open
  tasks are on it, and the tone of the most urgent — a task waiting on a person
  wins the colour, then a stalled one, then working.
- **A card carries its load.** What that session owes (tasks it is working on)
  and what it is waiting for (tasks it asked others for).
- **A panel, grouped as a person needs it.** Needs you (input-required),
  Working, Stalled, then Done unacked. Each row: the title, asker → worker, the
  latest line, and the age. Acked tasks are not listed: the list is what is
  still owed.
- **A task's name, by precedence:** the asker's `shrooms/title`, else the first
  line of the request (the A2A history's ROLE_USER message), else the worker's
  summary. An agent version that carries none of them still gets a row.
- **ACK goes to the worker's agent** (A2A `AckTask`), because the task lives on
  the machine that ran it. The view is the surface, not the authority.
- **A tap opens the worker's session**, not the asker's: that is where the task
  arrived.

- **A task row is a door, not a label**: tapping it opens the worker's session
  and jumps to the message the task arrived in. No new data is needed — the
  task's message event already carries the task's message id as its `pid`, so
  the match is on the id, not on a sequence, and the view's own search is the
  fallback for a message outside the loaded tail. One jump path, not two.
- **A link badge is a filter, not a second list**: tapping the count between two
  sessions filters the panel to that pair, with an explicit way to clear it. The
  panel already had the tasks, so nothing is fetched to answer it.

## Consequences

- **No lag and one authority.** What the panel shows is what the agent's own
  store says, at the moment it was asked.
- **The hub keeps only what is the board's own** — cards, lists, links, and the
  humans' own boards. Its copy of task state, and the bridge that maintained it,
  are redundant once this is merged.
- **The view does more work, not the network**: the tasks were already in the
  payload.
- **What this does not do:** it does not make the agents' stores authoritative
  for anything but their own tasks, and it does not write task state from the
  board except by an explicit ACK — the same rule as ADR-042, where a task is
  open until the worker finishes it and the asker's ack closes it for them.
- **The phone keeps its list** (ADR-039's division): the same grouping and ACK
  belong there too, as its own change.
