# Tasks between agents: open until finished, supervised, acknowledged

The shrooms extension to A2A's tasks (ADR-042), named in every Agent Card by
`https://github.com/vpavlin/shrooms/blob/master/docs/a2a-tasks.md`. Proposed by
Jimmy (pi5) after proteus stopped halfway through a review and nothing resumed
it; reviewed and built 2026-10-07.

## A task is not a turn

A turn is one model response; it can end with the work half done. A task is
open from the message that asks for it until the **worker says** how it ended:

| The worker calls `task_update` with | A2A state | The asker reads |
|---|---|---|
| `done` + summary | `TASK_STATE_COMPLETED` | the summary (also an artifact) |
| `blocked` + what it needs | `TASK_STATE_INPUT_REQUIRED` | what it needs; answered with a `SendMessage` whose `taskId` is the task |
| `failed` + why | `TASK_STATE_FAILED` | why |

Until then it is `TASK_STATE_WORKING` (a permission prompt waiting on the
worker's machine shows as `TASK_STATE_INPUT_REQUIRED` too), or
`TASK_STATE_SUBMITTED` while it waits in the queue. `CancelTask` interrupts it;
nobody finishing it in 24 hours expires it (`TASK_STATE_CANCELED`,
`shrooms/expired`). The worker's own session sees each task as a message
beginning `[shrooms task SESSION:MESSAGE-ID from DEVICE (MACHINE/SESSION)]`, with
how to finish it.

`task_update` is a tool of the `shrooms` MCP server every session has (also
`shrooms-agent a2a update ID done|blocked|failed SUMMARY`), and goes to
`POST /v1/tasks/{id}` on the worker's own agent — which takes it only from its
own machine (a source address that names no other device) and for the session
the task belongs to. Not a reserved line in the reply: models paraphrase,
quote and translate, and a parser that guesses closes unfinished work.

## The queue

A session works on one task at a time. A task sent to a session that is busy —
working, a prompt waiting, or another task still open (idle between turns is
not free) — waits, `TASK_STATE_SUBMITTED` with `shrooms/queued`, and starts when
the one before is finished, blocked or stalled. Nobody is told "busy" any more.

## Supervision

On the worker's machine, where the session is, every 30 s, for a session that
is idle, with no prompt waiting and a task open (working, started, not stalled,
not paused):

- **First reminder** after the session has been quiet 2 min (pi) or 5 min
  (Claude Code — and never while a command it started in the background is
  still running under it, which ends a turn but not the work).
- **Then** after 1, 2, 5, 10 and 30 min more; after the last, the task is
  **stalled**: a note in the session, `shrooms/stalled`, the session list's
  "⚠ a task stalled", and a phone notification. The owner, not the asker's
  model, is woken.
- **At most 6 reminders an hour per session**, however many tasks it has; one
  reminder names every task due, oldest first.
- **The owner stops it** ("■ stop" in an app): its tasks are not reminded about
  until someone writes to the session again.

A reminder is written by the agent, the same every time — no model — and
carries each task's request (the conversation may have been compacted since)
and the worker's last words, so it resumes rather than starts over. It is a
message from `shrooms`, marked outside (`"outside": true, "nudge": [ids]`), and
the apps label it SHROOMS, not YOU. Its cost is a turn of the worker's — about
$0.21 at a 700k-token context on Venice without cache, so a full ladder is
about $1; the usage dashboard shows it under "shrooms" as who asked.

Cost follows open obligations, not the clock: a session with no open task is
never woken.

## Acknowledgement

`AckTask` (the extension's method; `task_ack` in MCP, `a2a ack` in the CLI) is
the asker's: it has what it needed. It is bookkeeping — `shrooms/acknowledged`
— not what closes a task: A2A's `completed` is final, and an asker that is not
satisfied opens a new task with `referenceTaskIds` pointing at the old one.

## In a task's metadata

`shrooms/session`, `shrooms/from`, `shrooms/queued`, `shrooms/nudges`,
`shrooms/last_nudge`, `shrooms/stalled`, `shrooms/expired`,
`shrooms/acknowledged`, `shrooms/last_worker_line`, `shrooms/title`. `ListTasks` lists a
session's tasks; `GET /v1/tasks[?session=]` is the apps' list.

What was asked is the task's A2A `history`: one `ROLE_USER` message with the
request, so a board or a list can title a task by its question rather than
by its answer. A request is often paragraphs that start with who is asking,
so the asker may also name it: `shrooms/title` in the message's metadata
(`title` in `ask_agent`, `--title` in `a2a send`), a few words, kept on one
line and cut at 120 characters, and returned in the task's metadata. A task
without one has none; a list falls back to the start of the request.

## Kept

`tasks.json` in the agent's state directory; finished tasks are dropped a week
after they ended. A restart of the agent resumes the ladder where it was. A
renamed session keeps its tasks.
