# 042. A task is open until the worker finishes it

**Status:** accepted, built 2026-10-07 — amends ADR-041 ("a task is one
turn"); details in [docs/a2a-tasks.md](../a2a-tasks.md)

## Context

ADR-041 made an A2A task one turn: from its message to its result. A turn is
one model response, and a turn can end with the work half done — proteus ended
a review with "writing the probe" and went idle, and nothing resumed it; the
heartbeats that used to were switched off for burning tokens on nothing. A task
that ends with its turn cannot tell finished from stopped. Jimmy proposed
supervising the task instead of the session; this is that, after review.

## Decision

- **A task lasts until the worker says how it ended** — `task_update` done,
  blocked or failed, an MCP tool every session has — or it is cancelled, or it
  expires after 24 hours. That is what A2A means by a task (non-terminal states
  across many messages, ended by the serving agent); one turn per task was our
  shortcut.
- **"Idle with a task open" is the stall**, exactly, with nothing inferred from
  prose. A watchdog on the worker's machine reminds the session — a reminder
  written by the agent, with the request and the worker's last words — on a
  ladder (after 2/5 min quiet, then 1, 2, 5, 10, 30), at most 6 an hour per
  session, never while Claude Code still runs a background command, never after
  the owner stopped the session; after the ladder the task is stalled and the
  owner is told through the apps.
- **A busy session queues tasks** instead of rejecting them.
- **The asker's acknowledgement is bookkeeping** (`AckTask`, a shrooms
  extension), not what closes a task: `completed` stays final, as in A2A.
- **Kept on disk** where the worker is, and declared in the Agent Card as an
  extension.

## Consequences

- Cost follows open obligations, not the clock: an idle desk with no task open
  is never woken, and the heartbeats can stay off.
- A worker must call `task_update`. Every session is told so (its system-prompt
  note, and the header of each task); one that does not is reminded, and in the
  end the task is stalled and a person looks — which is the honest outcome.
- A blocking `SendMessage` now waits until the task is finished or needs input
  (at most 10 minutes), not merely until a turn ends.
