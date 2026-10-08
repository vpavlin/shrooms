# Agents on the mesh

**Status (2026-10-08):** in daily use from a phone and from Basecamp since
2026-10-03, the day it was built. **Shrooms Agents** is its own app and its
own Basecamp module, beside shrooms (decided 2026-10-03, below).

Talk to the coding-agent sessions (Claude Code, pi) on every machine you own,
from the phone, over the mesh — and approve what they want to do from there.
It replaces `cl` (tmux sessions reached over SSH), whose phone interface is a
terminal.

The point is privacy and independence: nothing leaves the mesh. Claude Code's
own Remote Control does much of this through a cloud relay; this does not.

This is the reference: the shape, the access model, the whole HTTP and A2A
API, the events, the command line and the files on disk. The guides say how
to use it:

- [Using it](agents/using.md): the apps — the list, the board, a
  conversation, files and voice notes, reading aloud, usage;
- [Setting up](agents/setup.md): installing the agent, Claude Code and
  pi, voice notes, the apps, troubleshooting;
- [Agents together](agents/together.md): agents asking each other, tasks
  and the watchdog, from a shell or any A2A client;
- [Cages](agents/cages.md): sessions in containers of their own.

## Shape

```
Shrooms Agents (Android)  ─┐
Shrooms Agents (Basecamp) ─┼─ HTTP over the mesh ──▶  shrooms-agent (each machine)
other agents (A2A)        ─┤                              │ stream-json, one process per session
                           │                              ▼
                           │                    claude -p, or pi (optionally in a cage)
shrooms (VPN, the mesh) ───┘  the network all of them run over
```

- **`shrooms-agent`** is a separate binary, not part of the daemon. Shrooms is
  the network; something that runs code on request does not belong in its
  most privileged process. It runs as the user (a systemd user unit, started
  through a login shell so sessions find what a terminal finds).
- **Shrooms Agents is a separate app, and a separate Basecamp module**
  (decided 2026-10-03, ADR-036). Agents grow out of the mesh and are not the
  mesh: the two change at very different speeds — the network should be
  boring, the agents UI changed a dozen times on its first day — and an app
  that records audio and drives machines that run code is a bigger target
  bundled into a VPN. The dependency runs one way: agents need the mesh for
  addresses, access and discovery; the mesh does not need agents. Its mark is
  the mushrooms alone (assets/agents_logo.py), shrooms' is the mycelium.
  - **Android:** the "agents" build of the same code
    (xyz.vpavlin.shrooms.agents), signed with its own key, kept outside the
    repository (~/apk-signing/shrooms-agents) with its password beside it. It
    is no mesh client: the shrooms app's "agents" link opens it and hands over
    the peers it can reach, and an agent lists the mesh as its machine sees it
    (`/v1/peers`), so knowing one finds the rest.
  - **Basecamp:** `shrooms_agents`, a view of its own that shares
    `shrooms_core` — which does all the networking, since Basecamp's sandbox
    forbids it in a view, on threads of its own so no call freezes the window.
- One repository, one set of tools.
- **Not only Claude Code.** Sessions run on a pluggable harness: Claude Code,
  or pi (on any model pi is set up with, a local one included), with more to
  be added the same way — docs/agents-harnesses.md is the guide.
- **Cages.** A session can run in a rootless podman container of its own
  instead of as the user directly (docs/agents-in-cages.md,
  [Cages](agents/cages.md)).

## Access: the bind is the access control

`shrooms-agent` listens on this device's **overlay address** on each mesh it
serves, on a fixed port (**7387**), and nowhere else. Per ADR-026, only mesh
members can route to that address: the WireGuard tunnel admits configured
members only. So:

- reachable by members of those meshes, not by the LAN or the internet;
- the caller's source address *is* its device key's overlay address, so every
  request is attributed to a named device with no logins and no tokens. The
  name is the peer's as the daemon lists it, `NAME.MESH` (`laptop.default`);
  a request from an address the daemon does not name as a peer — this machine
  itself, Basecamp on it, a session's own `shrooms-agent a2a` — is recorded
  with no device (`by` empty);
- the port appears in peers' view through `announce_bound`, which is how the
  phone discovers it.

**Decision (2026-10-03): who may talk to an agent = the members of the meshes
listed in its config.** Every device on the meshes is the owner's today; the
list is the rail for the day a mesh is shared. To change later: a per-device
allowlist by key.

Another local user on the same machine can also connect to the overlay
address. That machine is already theirs to run code on, so it is not a new
exposure.

One route is narrower: `POST /v1/tasks/{id}`, a worker finishing its task, is
refused (403) to any caller the mesh names, so only this machine's own
sessions can say their tasks are done.

## Talking to Claude Code

Each session is a `claude -p` process run with `--input-format stream-json
--output-format stream-json --verbose --permission-prompt-tool stdio`, in the
session's directory. Observed on 2.1.287:

- user turns go in on stdin as `{"type":"user","message":{...}}`;
- assistant text, tool use and tool results come out as `assistant` / `user`
  messages, a turn ends with `result`;
- **a permission prompt arrives as a `control_request` with subtype
  `can_use_tool`** (tool, input, description, suggested "always allow" rules)
  and is answered with a `control_response` — `allow` (with the input) or
  `deny` (with a reason). Without `--permission-prompt-tool stdio`, prompts are
  denied automatically;
- `AskUserQuestion` reaches the host the same way, so multiple-choice
  questions can be answered from the phone too;
- every session has an id, and `--resume <id>` continues it after the process
  has gone.

The user's own Claude Code settings apply (permission rules, model, hooks), so
a session behaves as it would in a terminal. pi runs in its RPC mode; its
codec translates what it writes into the same stream-json messages, so the
apps and the event log see one shape (docs/agents-harnesses.md).

A process lives while the session is in use and is stopped after it has been
idle for 30 minutes; the next message resumes it by id. A session marked
`keep_running` is never stopped as idle and is started again within 15 s
whenever it ends. A session is a name and a directory — the same thing `cl`
keys on. A name is letters, digits, `.`, `_` and `-`, starting with a letter
or digit, at most 64 characters.

## API (HTTP + JSON, server-sent events)

On each machine's overlay addresses, port 7387. Bodies are JSON; an error is
`{"error": "…"}` with a 4xx or 5xx status. `{name}` is a session's name.

### Sessions

| Route | Request | Response and notes |
|---|---|---|
| `GET /v1/sessions` | `?tail=N` (at most 20) | `{sessions: [Info], limits, credits}`: the sessions (fields below), with where the Claude subscription stands (`limits`) and the pay-as-you-go keys (`credits`), so an app shows both at a glance without asking for usage. `?tail=N` adds each session's `tail`: its last N lines, oldest first — what was asked (`› you:`), the model's text, its tools (`▸ Bash make test`) and tasks (`◆ task …`) — for Basecamp's board |
| `POST /v1/sessions` | `{name, dir, harness?, auto_approve?, keep_running?, cage?}` | 201, the new session's Info. Claude Code unless `harness` names another (`GET /v1/harnesses`). `dir` (`~` is the agent user's home) is made if it does not exist, once the request is otherwise accepted, and refused if it is a file. `cage: {}` runs it in a container of its own; `{"image": …, "nix": true, "github": true}` for another image, the machine's nix, the owner's gh login |
| `POST /v1/sessions` | `{name, resume, dir?, harness?, cage?}` | 201: continue an existing conversation — Claude Code's, or with `harness: "pi"` pi's — by its id, in the directory it ran in (read from its transcript when `dir` is not given). Refused for one whose directory is not on this machine: a `~/.claude` copied from another machine brings its transcripts along, and those cannot be continued here, nor is their directory made |
| `DELETE /v1/sessions/{name}` | | 204: stop and forget it (its event log goes too) |
| `PATCH /v1/sessions/{name}`, `POST /v1/sessions/{name}/settings` | `{auto_approve?, starred?, keep_running?}` | 200, Info. POST is for clients that cannot send PATCH (Android's HttpURLConnection). A star is kept on the agent, so every device lists starred sessions first, above each machine's others. `keep_running`: for an agent that works on its own (a heartbeat, a chat bridge in its extensions), Jimmy on pi5 being the first. Auto-approve and keep-running changes are recorded as `setting` events |
| `POST /v1/sessions/{name}/rename` | `{name}` | 200, Info; 409 if the name is taken or not valid, 404 if there is no such session. The event log moves with it; files sent to it stay where they were kept, since its messages name them by path; its process goes on undisturbed. Recorded as a `renamed` event, on which every app following it moves what it keeps under the name (the copy, unread, auto-play, the outbox) |
| `POST /v1/sessions/{name}/restart` | | 204: end the session's process at once — killed, since a request hanging on a dropped connection (`API Error: Connection dropped (ECONNRESET)`) answers neither an interrupt nor the end of its input — and start it again on the same conversation. The turn in progress is lost; turns queued behind it go on. Only this session's process: the others on the machine, which a restart of shrooms-agent would end too, are untouched. A `restarted` event |
| `POST /v1/sessions/{name}/cage` | `{"cage": {image?, nix?, github?}}` or `{"cage": null}` | 200, Info; 409 unless the session is idle with no prompt waiting, or when the machine has no podman. Moves a session into a cage, changes its cage, or takes it out. Its process is stopped and the conversation resumes where it now runs with the next message; a cage changed or left is deleted, with what was installed in it. A `caged` event |
| `GET /v1/harnesses` | | `{harnesses: [{name, title, caps: {approve, takeover}}], cage: {available, image, images, nix, ready, building, error}}`. The coding agents this machine runs sessions of, Claude Code first. `approve`: it asks before using tools, so auto-approve means something; `takeover`: its conversations from elsewhere can be listed and continued. `cage`: `available` where the agent found podman (absent fields when it did not), `image` the machine's, `images` those offered (the machine's first, then the workbench and the desktop), `nix` when the machine has nix to give, `ready` once the image is there, `building` while it is built, `error` why the last build failed |

### A session's conversation

| Route | Request | Response and notes |
|---|---|---|
| `GET /v1/sessions/{name}/events` | `?after=N&tail=T`, or the `Last-Event-ID` header | Server-sent events: the session's events after N, then live ones as they happen. Each is `id: SEQ` and one `data:` line of the event's JSON. `Last-Event-ID`, which an SSE client sends by itself on reconnecting, wins over `after`. `tail=T` with `after=0` starts at the last T events instead of the first: both apps open a session at its last 300 and offer to load the rest; a reconnect is never trimmed. `partial` events carry no id. A comment every 20 s lets a phone on mobile data notice a dead connection; a client too slow to keep up has its stream ended and reconnects without losing anything |
| `POST /v1/sessions/{name}/messages` | `{text, id?}` | 202: a user turn, sent as from the calling device. `id`, made on the device, makes sending again harmless: one already taken (the last thousand, remembered across restarts from the log) answers 200 `{"duplicate":true}`. While a voice note is being transcribed a message waits behind it, so turns reach the model in the order they arrived. 409 when it cannot be sent |
| `POST /v1/sessions/{name}/prompts/{id}` | `{allow, message?, answers?}` | 204: answer a permission prompt; 409 when the prompt is gone (its process ended). A question from the model (Claude Code's AskUserQuestion tool) arrives as a prompt for that tool, even under `--dangerously-skip-permissions`, and is answered with `allow` and `answers` (question → chosen label, labels joined by ", ", or the person's own words), which the agent puts into the tool's input; allowed without answers it is refused (409), since the model would read it as "the user did not answer". Auto-approve never answers a question. An `answer` event |
| `POST /v1/sessions/{name}/interrupt` | | 202: stop the current turn; 409 when nothing runs. The session's open tasks get no reminders until someone writes to it again |
| `GET /v1/sessions/{name}/history` | `?limit=N&before=T` | `{history: [{time, role, text}]}`, oldest first: what was said before this agent had the conversation, from the harness's transcript (its last 4 MB). `limit` 30 by default, at most 200; `before` an RFC 3339 time |
| `GET /v1/sessions/{name}/search` | `?q=…&limit=N` | `{found: [{seq, time, role, snippet, text?}]}`, newest first (50 by default, at most 200): the turns of the whole conversation containing q — what was typed and the model's text, not tools — ignoring case and Czech diacritics. From the session's events (`seq` to jump to) and, for what came before them, the transcript (`seq` 0, with the whole `text`, up to 16 KB). About a second on a 190 MB transcript |

### Files and voice notes

| Route | Request | Response and notes |
|---|---|---|
| `POST /v1/sessions/{name}/files` | `?name=FILE`, the bytes as the body | 201 `{path}`: kept under the agent's state directory (50 MB at most), for the next message to name. The name is reduced to safe characters and prefixed with the time, so it cannot decide where the file lands |
| `POST /v1/sessions/{name}/voice` | `?name=FILE&id=ID`, the audio as the body | 202 `{path}`: a voice note as a turn. Kept like a file and answered at once; transcribed here in the background and sent as the device's message (a `message` event with `voice`, the recording's path). Its progress is a `voice` event. The same `id` again answers 200 `{"duplicate":true}`. 501 when this machine has no speech-to-text |
| `POST /v1/sessions/{name}/voice/{id}/retry` | | 202: transcribe a failed voice note again, from its kept recording; 409 if there is no such note, it did not fail, or the recording is gone |
| `POST /v1/sessions/{name}/transcribe` | `?name=FILE&lang=CODE`, the audio as the body | 200 `{path, text}`: a voice note kept like a file and transcribed on this machine, not sent. Parakeet v3 (the default) detects the language and ignores `lang`; with a Whisper model (`--stt-model`), naming it halves the time. 501 without speech-to-text |

Voice notes are on when the agent finds its model (by default
`~/.local/share/whisper/ggml-parakeet-tdt-0.6b-v3-q4_k.bin`, from
`ggml-org/parakeet-GGUF`) and the CLI that runs it (`parakeet-cli` from
whisper.cpp) on its PATH; docs/speech-to-text.md has the engines. No audio
reaches a speech service.

### The machine

| Route | Request | Response and notes |
|---|---|---|
| `GET /v1/usage` | `?since=2006-01-02` | `{machine, rows, limits, credits}`: usage, plan limits and credits (below). `machine` is the hostname |
| `GET /v1/conversations` | `?limit=N` | `{conversations: [{id, dir, modified, size, last_user, last_assistant, adopted_by, terminals: [{pid, dir, tmux, args}]}]}`, newest first (30 by default, at most 200): this machine's Claude Code conversations — where each ran, its last exchange, the session continuing it, and any terminal `claude` open in the same directory (with its tmux session). Only those whose directory is on this machine |
| `POST /v1/terminals/{pid}/stop` | | 204: end a terminal's `claude` so its conversation can be continued here; only Claude Code run by this user by hand (404 otherwise) |
| `GET /v1/peers` | | `{peers: [{name, mesh, overlay}]}`: the mesh as this machine sees it, so a client that knows one agent finds the rest. Names and mesh addresses only: what any member already sees |

### Tasks and A2A

Every session is an A2A (v1.0, JSON-RPC binding) agent (ADR-041); a message to
one is a task (ADR-042, docs/a2a-tasks.md) that stays open until the worker
finishes it. How that plays out is in [Agents together](agents/together.md)
and docs/agents-together.md ("On the wire: A2A").

| Route | Request | Response and notes |
|---|---|---|
| `GET /.well-known/agent-card.json` | | This machine's Agent Card: `name` the hostname, one interface at `/a2a`, and its sessions as skills, each naming its own card |
| `GET /a2a/{name}/.well-known/agent-card.json` | | A session's Agent Card: `name` `HOSTNAME/SESSION`, a description with its harness and directory, one JSON-RPC interface at `/a2a/{name}`, `streaming` on, push notifications off, the shrooms task extension (docs/a2a-tasks.md, not required), text in and out |
| `POST /a2a/{name}` | JSON-RPC 2.0 (below) | The session's A2A endpoint. Always HTTP 200; failures are JSON-RPC errors |
| `POST /a2a` | JSON-RPC 2.0 | The same, the session found from the message's `metadata["shrooms/session"]`, its `taskId`, or the `id` param (`SESSION:MESSAGE-ID`) |
| `GET /v1/tasks` | `?session=NAME` | `{tasks: [Task]}`: the tasks on this machine as A2A shows them (below), for the apps — Basecamp's board draws a line from asker to worker from it |
| `POST /v1/tasks/{id}` | `{state, summary, session?}` | 200, the Task: the worker's word on a task given to its session — `state` `done`, `blocked` or `failed` (also `completed`, `input-required`). Only from this machine (403 for a caller the mesh names, or when `session` is not the task's); 404 for no such task; 409 for one already closed or another state. A `task` event. What `task_update` and `shrooms-agent a2a update` call |

The JSON-RPC methods:

| Method | Params | Result and notes |
|---|---|---|
| `SendMessage` | `{message: {messageId, role, parts, metadata?, taskId?}, configuration?: {blocking}, referenceTaskIds?}` | `{task}`. Text parts only. The task id is `SESSION:MESSAGE-ID`; the same messageId again is the same task. A busy session queues it. `blocking` waits up to 10 minutes, until the task is finished or needs input. A message with `taskId` adds to an open task — the answer to "blocked". The message reaches the session as from the device the mesh names, with the sender's claim from `metadata["shrooms/from"]` after it ("pi5.default (pi5/jimmy)"). More than 30 new tasks to one session from one device in an hour are refused (-32050) |
| `SendStreamingMessage` | as SendMessage | Server-sent events of JSON-RPC responses: `{task}` first, then `{statusUpdate: {taskId, contextId, status, final}}` on each change, ending with the final one |
| `GetTask` | `{id}` | `{task}` |
| `SubscribeToTask` | `{id}` | as SendStreamingMessage |
| `CancelTask` | `{id}` | `{task}`: interrupts it; -32002 if it cannot be |
| `ListTasks` | | `{tasks}`: the session's |
| `AckTask` | `{id}` | `{task}`: the asker has its result; the extension's |

`id` is `SESSION:MESSAGE-ID`, or the message id alone at `/a2a/{name}`.
Errors: -32700 not JSON, -32600 not JSON-RPC 2.0, -32601 no such method,
-32602 bad params or no such session, -32001 no such task, -32002 not
cancelable, -32004 streaming unsupported, -32050 over the rate.

A Task is `{id, contextId, status: {state, message?, timestamp}, artifacts?,
metadata}`. `state` is A2A's (`TASK_STATE_SUBMITTED` while queued, `WORKING`,
`INPUT_REQUIRED` — also while a permission prompt waits on the worker —,
`COMPLETED`, `FAILED`, `CANCELED`). The status message is the worker's
summary or, while it works, the last thing it said; a completed task also has
it as the `result` artifact. `contextId` is the session's conversation id.
`metadata` carries the extension's fields: `shrooms/session`, `shrooms/from`,
`shrooms/acknowledged`, `shrooms/nudges`, `shrooms/last_nudge`,
`shrooms/stalled`, `shrooms/queued`, `shrooms/expired`,
`shrooms/last_worker_line`.

The watchdog (`supervise.go`, every 30 s): a session idle with a task it has
started is reminded after 5 minutes quiet (Claude Code, and not while its
background work runs) or 2 (pi), then after 1, 2, 5, 10 and 30 minutes more;
after those five it is marked stalled. At most 6 reminders a session an hour.
A task open 24 hours expires (cancelled). Closed tasks are kept a week.

### Taking over a terminal's conversation

What replaces `cl`. Resuming a conversation keeps writing to the same
transcript, so a session that continues one started in a terminal carries it
on rather than copying it. A terminal `claude` does not keep its transcript
open, so which conversation it holds cannot be known — only that one is open
in the same directory, and which tmux session it is. The list says so, with
"stop it": two writers on one conversation cannot see each other's turns, and
the transcript branches.

## A session, as listed

The Info of `GET /v1/sessions` (also returned by create, settings, rename and
cage):

| Field | What it is |
|---|---|
| `name`, `dir` | the session's name and working directory |
| `state` | `idle` (nothing running, or a turn has finished), `working` (a turn is in progress, also one the harness started itself, when background work finishes), `waiting` (a permission prompt needs an answer) |
| `pending` | permission prompts waiting |
| `running` | whether its process is up |
| `last_seq`, `last_time` | its newest event's number and time |
| `turns` | how many turns have ended: what the phone notifies on, once each, and what unread counts are made from. Events are no use for that — a session waiting on background work sends heartbeats and progress between turns |
| `auto_approve` | nothing asks, as with `--dangerously-skip-permissions` |
| `context_used`, `context_window` | how much of the model's context the conversation fills, as of the last reply |
| `preview` | the start of the last thing the model said |
| `model` | the model, as its harness names it (pi: `provider/model`) |
| `harness`, `caps` | `claude` or `pi`, and `{approve, takeover}` as in `GET /v1/harnesses` |
| `starred` | listed first, on every device |
| `keep_running` | never stopped as idle, started again when it ends |
| `tasks_open`, `tasks_stalled` | its A2A tasks, and of them those it stopped making progress on |
| `limited` | `{reason, until?}` while the session cannot work for want of quota (`limited.go`): a Claude Code session when the subscription refused a request and that window has not reset ("limit reached (5 hours)"), a pi session when the Venice key its provider uses has no DIEM and no USD left ("no DIEM left today (venice)"). `until` is when it comes back, if known. So the apps can set it apart instead of the owner finding out by sending a message |
| `cage` | `{image, nix?, github?}` for a session that runs in a container of its own |
| `tail` | with `?tail=N` only: its last lines |

## Events

Events are numbered per session (`seq`) and kept on disk, so a phone that was
away catches up from the last number it saw. Each is `{seq, time, kind, by?,
data?}`; `by` is the device that caused it, as the mesh names it.

| Kind | Data | When |
|---|---|---|
| `claude` | the harness's message, verbatim stream-json (pi's translated into the same shape) | everything the model and its tools say: `system` (init), `assistant`, `user` (tool results), `result` (a turn ended, with its usage and cost), `control_request` (a permission prompt or a question), `rate_limit_event` |
| `message` | `{text, id?, voice?, outside?, nudge?, error?}` | a user turn. `voice`: the recording it was transcribed from. `outside`: a turn the harness started itself (an extension's heartbeat, a chat bridge; `by` its source) or the watchdog's reminder (`by` `shrooms`, `nudge` the task ids). `error`: a message that was taken but could not reach the model |
| `answer` | `{prompt, allow, message, answers?}` | a permission prompt or question answered |
| `stopped` | `{reason}` | the process ended (`finished`, or why); its waiting prompts are dropped |
| `partial` | `{text}` | reply text as it is written. Live only — never kept or numbered (its `seq` is the last real event's) — since the whole message follows as a `claude` event |
| `setting` | `{auto_approve}` or `{keep_running}` | a setting changed |
| `restarted` | `{}` | the process was restarted on request |
| `renamed` | `{from, to}` | the session was renamed |
| `caged` | `{caged, image?, nix?, github?}` | moved into a cage, its cage changed, or taken out |
| `voice` | `{id, path, status, error?}` | a voice note: `transcribing`, or `failed` with why; once transcribed it becomes a `message` |
| `task` | `{id, state, summary}` | a task finished by its worker (`completed`, `input-required`, `failed`), marked `stalled` by the watchdog, or `expired` |

## Usage

`GET /v1/usage[?since=2006-01-02]` reads it out of the sessions' event logs,
history included, as they grow: `rows`, one per day (the machine's local
date), session, device and model:

`{day, session, by, model, harness, turns, input, cache_read, cache_write,
output, cost_usd, busy_ms}`

- `by` is the turn's sender: the mesh peer its request came from, which
  WireGuard makes unforgeable, or "" for this machine itself.
- The tokens are the turn's result's `usage` — fresh input, input read from
  and written to the cache, and output (thinking included).
- `cost_usd` is as the harness prices it (0 for a local model): what the turn
  took the harness's running total above its highest so far. Claude Code's
  total runs across restarts and dips on a resume; counting from the last
  figure counted every dip twice. The log's first result only sets where the
  count starts.
- `busy_ms` is from the asking to the answer; a turn left waiting over two
  hours is counted by its own duration.

The apps ask every machine and sum it by who asked, where it ran and which
model ([Using it](agents/using.md), "Usage") — the basis of sharing a
model on a machine of your own fairly, or of billing for it.

## Plan limits

Where the machine's Claude subscription stands. Claude Code reports it after
every turn (`rate_limit_event` in its stream, which the agent logs like
everything else); `GET /v1/usage` and `GET /v1/sessions` return the newest
report among the machine's sessions as `limits`, or null when Claude Code
never reported one. The agent keeps it as Claude Code reports it, and reads it
from the logs at startup, so a restart does not forget the last one.

`{at, status, window?, overage?, windows: {five_hour, seven_day, …: {utilization, resets_at, projected?, runs_out_at?, pace?}}}`

- `status`: `allowed`, `allowed_warning` (past a threshold Claude Code warns
  at — 90% of the 5-hour window, 50% of the 7-day one), `rejected` (limit
  reached); `window` is the window it is about; `overage` whether extra usage
  is being drawn on.
- `utilization` is 0 to 1; `resets_at` when the window starts again. An older
  Claude Code reports only the window the status is about.
- A reading is only as fresh as the last turn on that machine, which `at`
  says; and it is Claude Code's own stream, not a documented API, so the
  fields are read defensively.

The apps show machines whose windows reset at the same moments once, as one
account, with the newest reading; bars turn amber from half a window and red
from 80%, and the 5-hour share is on the link that opens usage.

## Forecasts

`forecast.go`: whether a limit lasts until it renews, at the pace it is being
used. The pace is measured up to now, not to the last reading, so idle hours
count.

- **Each plan window** carries `projected` (its share at the reset),
  `runs_out_at` (when it would reach all of it, if before) and `pace`:
  `recent` — from the readings of the last hour of a 5-hour window, the last
  day of a 7-day one — or `window`, the average since the window began when
  there is too little history. Readings of an earlier window of the same name
  are not its history; readings are kept 8 days.
- **A Venice key** carries `runs_out_at` (when its DIEM runs out, if before
  the refill) or `left_at_refill`, from the balances of the last two hours of
  the same day.

The apps say it under each bar: "at this pace: runs out Sat 14:00 — before it
resets" in red, or "at this pace: about 70% at the reset — it lasts".

## Credits

The same for pay-as-you-go keys. An agent that runs pi finds the Venice keys
in pi's model settings (`~/.pi/agent/models.json`, or under
`$PI_CODING_AGENT_DIR`): each provider with an `api.venice.ai` base URL, its
`apiKey` written out or `$VAR` / `${VAR}` from the agent's environment. The
file is read again each round, so a key added or changed needs no restart.
Every 5 minutes it asks Venice where each key stands — `GET
{baseUrl}/api_keys/rate_limits`, which an inference key may ask — and `GET
/v1/usage` and `GET /v1/sessions` carry it as `credits`:

`[{provider, names, key, balances: {DIEM, USD, …}, resets_at, at, error?, runs_out_at?, left_at_refill?}]`

- `key` is a fingerprint (the first 4 bytes of its SHA-256, in hex), never the
  key, so a key used by several machines is shown once with all of them:
  "Venice key 1a2b3c4d · pi5, proteus, scribe — 5.62 DIEM left today ·
  refills 02:00".
- `names` are the providers in pi's settings that use the key — one reading
  per key, however many providers share it. A pi session's model names its
  provider (`venice/glm-…`), which is how a session is matched to its key for
  `limited`.
- DIEM is the daily allowance, refilled at `resets_at` (Venice's next epoch,
  midnight UTC).
- A key's own spending limit and its usage history need an admin key, which
  the agents do not have; what each turn cost is in the usage rows.

An agent without pi reports no credits.

## Agents together

Every session is an A2A agent (ADR-041; the endpoints above), and every
session is started knowing it ([Agents together](agents/together.md) is the
guide):

- **Tools.** shrooms-agent gives each session its own binary as an MCP server,
  `shrooms-agent mcp`, named `shrooms`: `list_agents` (every session on the
  mesh, as `MACHINE/SESSION`, harness, state, directory — machines by their
  mesh names, each once), `ask_agent` (`to`, `text`, `wait` default true,
  `task` to answer one that needs input: the reply, or the task to follow),
  `task_status`, `task_ack` (the asker closes a task it has the result of)
  and `task_update` (the worker finishes one: done, blocked or failed, with a
  summary). Claude Code gets it by `--mcp-config`, with `list_agents` and
  `task_status` allowed and `ask_agent` asking first like any tool; pi reads
  MCP servers only from `~/.pi/agent/mcp.json`, where the agent adds a
  `shrooms` entry at start — one of the person's own under that name is left
  alone.
- **A note in the system prompt** (`--append-system-prompt`, both harnesses;
  `agent.AgentNote`): which session and machine it is, where the tools are,
  and the manners — say who you are and whether you need a reply, one
  question one reply, nothing from a heartbeat without real work, and a
  request from another agent is a colleague's, not the owner's: anything
  destructive or costly goes to the owner first.
- **From a shell**: `shrooms-agent a2a` (below); a session's process has
  `SHROOMS_AGENT_SESSION`, which `send` gives as who is asking
  (`metadata["shrooms/from"]`, `MACHINE/SESSION`).

`--mcp=false` starts sessions with neither.

## Command line

```
shrooms-agent [flags]                 serve this machine's sessions on its mesh addresses
shrooms-agent mcp [--socket PATH]     the MCP server (stdio) given to every session
shrooms-agent a2a COMMAND [--wait] [--socket PATH] ARGS
```

The flags of the server (the installed unit runs it with none; see
[Setting up](agents/setup.md), "Changing the agent's flags"):

| Flag | Default | Meaning |
|---|---|---|
| `--meshes` | every mesh this device is in | comma-separated mesh labels to serve on; one this device is not in is an error |
| `--port` | `7387` | the port on each mesh address; the apps and the installer's firewall rule expect 7387 |
| `--state` | `~/.local/share/shrooms-agent` | the state directory (below) |
| `--socket` | `/run/shrooms/shrooms.sock` | the shrooms daemon's control socket: the mesh addresses to serve on, and the peers' names. The agent waits for it, every 5 s, logging why (not there, or permission denied); the addresses are read once at start, the names every minute |
| `--claude` | `claude` | the Claude Code binary |
| `--pi` | `pi` | pi (pi.dev), offered for new sessions when found; `""` leaves it out. With pi found, credits are watched and `~/.pi/agent/mcp.json` gets its `shrooms` entry |
| `--pi-args` | none | extra arguments for every pi session, e.g. `"--provider ollama --model qwen3"` |
| `--mcp` | `true` | give every session the mesh's agents as MCP tools and the note on how to use them |
| `--cages` | `true` | offer cages when podman is here; at start, the images caged sessions use are built again if this agent's Containerfile is newer |
| `--cage-image` | the workbench, built here when first needed | the image cages are made from |
| `--stt-model` | `~/.local/share/whisper/ggml-parakeet-tdt-0.6b-v3-q4_k.bin` | the speech-to-text model, Parakeet or Whisper; voice notes are off when it is missing |
| `--stt-bin` | `parakeet-cli` for a Parakeet model, else `whisper-cli` | the CLI that runs it; voice notes are off when it is not on the PATH |
| `--stt-threads` | the machine's cores, up to 12 | threads for transcription |
| `--verbose` | `false` | debug logging, Claude Code's stderr included |

Run as root it warns: every session could do anything on the machine.

`shrooms-agent a2a`, a client for agents and people (machines found through
the daemon's socket; MACHINE is a peer's name as `shrooms status` shows it, or
its overlay address; a task is `MACHINE/SESSION:MESSAGE-ID`, as `send`
prints it):

| Command | What it does |
|---|---|
| `a2a list` | the agents on the mesh: `MACHINE/SESSION`, harness, state, directory; machines without an agent, or away, left out |
| `a2a send [--wait] MACHINE/SESSION TEXT` | ask a session (`SendMessage`); `--wait` waits for the task to settle and prints the reply |
| `a2a get MACHINE/TASK-ID` | where a task stands, and its reply (`GetTask`) |
| `a2a cancel MACHINE/TASK-ID` | interrupt it (`CancelTask`) |
| `a2a ack MACHINE/TASK-ID` | you have seen its result: it is closed for you (`AckTask`) |
| `a2a update TASK-ID done/blocked/failed SUMMARY` | a worker finishes a task given to its session on this machine (`POST /v1/tasks/{id}`, with `SHROOMS_AGENT_SESSION` as the session) |

`shrooms-agent mcp` speaks MCP over stdin and stdout, one JSON-RPC message per
line, with the five tools above; `--socket PATH` names another daemon socket.

## State on disk

Everything belongs to the agent's user. The state directory (`--state`,
`~/.local/share/shrooms-agent` by default) holds:

| Path | What it is |
|---|---|
| `sessions.json` | the sessions, sorted by name, each `{name, dir, harness?, claude_id?, auto_approve?, starred?, keep_running?, cage?}`. `harness` is left out for Claude Code, which every session was before there were others; `claude_id` is the harness's conversation id to resume by, named for Claude Code, the only harness when the file was first written; `cage` is `{image?, container, nix?, github?}`, `container` being podman's name for it — the session's name when it was caged and a few random letters, so a rename leaves it be and a later session of the same name gets its own. Written whole and renamed into place, 0600 |
| `events/NAME.jsonl` | each session's events, one JSON object per line, as served by `…/events`; the newest 2000 are also kept in memory (the board's `tail` reads those). Moved on rename, deleted with the session |
| `tasks.json` | the machine's A2A tasks: `{id, session, message_id, from, request, refs?, created, started?, updated, state, summary?, follow_ups?}` and the watchdog's `acked`, `acked_at`, `nudges`, `last_nudge`, `stalled`, `paused`, `expired`. Closed tasks are dropped a week after their last change |
| `uploads/NAME/` | files and voice notes sent to a session, as `YYYYMMDD-HHMMSS-NAME` (numbered when two come in the same second); mounted read-only into cages, all of them, since a renamed session's messages name files under its old name |
| `cages/CONTAINER.env` | a caged session's environment: the provider keys and settings passed through to it (`ANTHROPIC_*`, `PI_*`, `OPENAI_*`, `VENICE_*`, … and `SHROOMS_AGENT_SESSION`), 0600; deleted with its cage |

Elsewhere: Claude Code's transcripts under `~/.claude/projects/` (or
`$CLAUDE_CONFIG_DIR`), read for history, search, conversations and usage
limits; pi's under `~/.pi/agent/sessions/` (or `$PI_CODING_AGENT_DIR`,
`$PI_CODING_AGENT_SESSION_DIR`); pi's `models.json` (credits) and `mcp.json`
(the `shrooms` entry). [Setting up](agents/setup.md), "Where things
live", lists the rest.

## How the apps use it

The behaviour is in [Using it](agents/using.md); these are the
mechanics behind it, and why.

**The list** is kept between rounds of finding, and across restarts: a
machine that misses a round stays with its sessions as last seen, greyed as
"unreachable · seen …" after 25 seconds quiet, and is forgotten after a week.
It used to vanish and come back, moving the whole list. **Unread** is a
session's `turns` less those there were when it was last open on that device
with the app in front; each device keeps its own, and a session seen for the
first time starts read.

**The outbox.** Both apps write through one: a message or voice note goes
there first and is sent from there — at once if the machine answers, later if
not — in order per session, shown as *queued* and cancellable until it has
gone. On the phone it is sent by whichever runs, the conversation on screen or
the watcher in the background; in Basecamp, by a thread of the core, from
`~/.local/share/shrooms/outbox`. Each carries an id made on the device, which
the agent takes once, so sending again after a lost answer cannot send twice.
Files go with their message: copied into the outbox's folder when picked (or,
in Basecamp, pasted — which needs wl-clipboard or xclip), uploaded when the
message is sent (`POST …/files`), each recorded as soon as its path is known
so a send that fails after it does not upload it again; then the message,
naming every file by that path. Cancelling deletes the copies; a copy no
message took is deleted a day later. (Uploading on attach waited on an
unreachable machine — a minute on the phone, two in Basecamp — with sending
blocked, and then lost the file.)

**Voice notes are turns**: the recording itself is sent (`POST …/voice`), and
nothing comes back to read and confirm. Kept first, so one that fails (no
model, nothing heard) says why and can be transcribed again from the same
recording; it does not hold up what came after it.

**A conversation is kept on the device**: the newest 300 events of each one
(at most a megabyte; streamed text left out, and any string over 4 KB —
nearly always a tool's output, shown folded anyway — cut). A conversation
opens on its copy at once, marked "offline — as it was HH:MM" until the
machine answers and never shown as working, and only the events after the
copy's last are asked for. Opening by replaying the last 300 instead, tool
output and all, took tens of seconds over the mesh, and the conversation was
rebuilt under the reader as it came. With no copy, the replay is gathered and
shown once it reaches the session's newest event as listed. A copy more than
300 events behind is not caught up from (that is the same replay); one whose
numbers are above the session's was of a session deleted and made again, and
is dropped. On the phone the copies are in the app's files (`history/`), with
the 30 turns from before the agent had the conversation, kept current by the
background watcher for every session it sees; in Basecamp the core keeps them
in `~/.local/share/shrooms/history`, one file per machine address and session,
saved while a conversation is open (`agentEvents` says `"kept":MS` while the
copy is unconfirmed; a negative tail to `agentWatch` opens without it).
Deleting a session deletes its copy.

**Earlier turns** (`…/history`) are shown only above a session's own first
event: above a later one they repeated turns the agent also has. Basecamp asks
for them only then, once the machine has answered: asked first, the pane
stayed blank for a round trip (up to 5 s, the view frozen, for a machine that
was away).

**Long sessions.** Both open at the last 300 events (`tail=300`), with a link
to load the rest: replaying thousands made the phone scroll for ten seconds
before settling. The phone applies arriving events in batches every 120 ms.
In Basecamp the core keeps a session's last 4000 events, and answers the view
in pieces of about half a megabyte that the view reads on until caught up: a
whole backlog in one reply through Basecamp's IPC is the likely reason some
conversations showed empty after switching to them (not proven). The core
also runs searches, uploads and usage (`agentGather`, each machine shown as it
answers) in the background.

**Links** open in the browser. In Basecamp the core opens them (`xdg-open`,
http and https only): Basecamp's sandbox blocks every web URL inside a view,
so `Qt.openUrlExternally` there does nothing. Programs the core starts get its
environment without the AppImage's loader settings: its `LD_PRELOAD` (with
`__BUNDLE_REAL_EXE`) makes Ubuntu's multi-call coreutils refuse to run, which
silently broke `xdg-open`, a shell script.

**Reading aloud** is done by the app, sentence by sentence, so it works the
same with any engine: the model's text only, read as prose (code blocks named,
links as their text, paths as a person says them, Czech or English chosen per
reply). On the phone, Android's speech engine, also from the background
watcher, which shares one mark of what was read with the screen; in Basecamp,
which ships no Qt TextToSpeech, the core speaks — with Piper from
`~/.local/share/shrooms/piper` when it is set up, otherwise speech-dispatcher's
`spd-say` (docs/agents-voices.md).

**Basecamp's board** ("board" by AGENTS, kept as `agent_layout`; the phone
keeps its list) is built from `?tail=6` and each machine's `GET /v1/tasks`,
the asker read from the claim in `shrooms/from`. An asker that is no session
(the CLI, an app) has no card and so no line.

## Installing it

`scripts/install-agent.sh`, run with sudo by the user whose agents it serves;
[Setting up](agents/setup.md) has its options and what it changes. In
short: it takes `shrooms-agent` out of its own image,
`ghcr.io/vpavlin/shrooms-agent` (`make push-agent-image`: a static binary per
architecture, amd64 and arm64, and nothing else) — not a layer of the shrooms
image, since nodes follow `shrooms:latest` with podman auto-update and
publishing the agent there would roll a new daemon onto all of them; grants
the socket by ACL where needed; opens TCP 7387 to the machine's own mesh
addresses under firewalld or ufw; installs and starts the user unit
`/etc/systemd/user/shrooms-agent.service` with lingering on; and, with
`--voice`, builds `parakeet-cli` (pinned commit) and fetches the model.

The socket ACL needs two things to last, both learnt the hard way:

- **Across reboots**, `/etc/tmpfiles.d/shrooms-agent-USER.conf`, with the
  directory line first:

  ```
  d /run/shrooms - - - -
  a+ /run/shrooms - - - - user:USER:rx,default:user:USER:rw,default:mask::rw
  ```

  Without the `d` line the ACL is applied at boot only to a directory that
  already exists, and it does not: jimmy-crib rebooted, the container made
  the directory without the ACL, and the agent waited for a socket it could
  not read (2026-10-04; its log now says why it waits). `-` everywhere, so the
  line changes nothing about a directory that is there.
- **Across restarts of shrooms**, a drop-in,
  `/etc/systemd/system/shrooms.service.d/20-agent-access.conf`:
  `RuntimeDirectoryPreserve=restart` and the tmpfiles applied again after
  every start. shrooms' unit has `RuntimeDirectory=shrooms`, so systemd
  deleted `/run/shrooms` at every stop and made it afresh without the ACL —
  and the image's auto-update restarts it: on 2026-10-05 an update cut the
  agents on atlas and jimmy-crib off their socket. shrooms' own unit
  (packaging and `install.sh`) now keeps the directory over a restart too.

`--adopt-pi NAME` takes over a pi agent that ran on its own — a
`pi-agent.service` keeping pi in tmux with its heartbeat, as Jimmy (pi5),
proteus and scribe did: it stops and disables that service and its tmux
session first (two pi processes on one conversation would each write a branch
of it), adds `EnvironmentFile=-%h/.pi/agent/.env` — the provider keys its
start script sourced — to a drop-in, `10-pi-agent.conf` (added to, never
replaced), and continues pi's newest conversation as session NAME, kept
running. Re-run, it finds the session there and changes nothing (checked on
scribe).

Tried on jimmy-crib (Ubuntu, docker) and atlas (Fedora, podman, SELinux,
firewalld): from clean, re-run, uninstalled and reinstalled, and the boot
order replayed.

## On a server

Running: the laptop, the VPS, atlas and jimmy-crib (2026-10-04), and pi5,
proteus and scribe for their pi agents. On atlas and jimmy-crib it runs as the
owner (vpavlin), as on a desktop: they are the owner's machines, with Claude
Code and pi already logged in there. Both run the shrooms daemon in a
container, so the socket is reached by ACL. atlas also runs firewalld, whose
default zone takes the mesh interface and dropped port 7387; one rich rule
opens it to atlas's own mesh address only:
`rule family=ipv6 destination address=<its overlay>/128 port port=7387 protocol=tcp accept`.

The VPS runs one (2026-10-03), set up the way any shared or exposed server
should be ([Setting up](agents/setup.md), "On a server"):

- **Its own user, `agent`, no sudo.** Sessions run as that user, so
  auto-approve is safe to switch on and a session cannot touch the relay or
  anything else root owns; Claude Code also refuses
  `--dangerously-skip-permissions` as root. Lingering on, the user unit as on
  a desktop, Claude Code from its native installer in `~agent/.local/bin`,
  logged in once by hand.
- **The control socket by ACL.** The agent reads the daemon's status for the
  mesh addresses to serve on and the peers' names — the socket-group tier.
  The daemon there runs in a container, whose group names are not the
  host's, so `socket_group` does not fit; the tmpfiles ACL on the host's
  `/run/shrooms` (mounted into the container) is inherited by the socket each
  time the daemon makes it.
- **Voice notes:** `parakeet-cli` built from whisper.cpp in
  `~agent/.local/src`, the model in `~agent/.local/share/whisper`,
  `--stt-threads 4` for its four cores. About real time there (8.7 s for an
  8.6-second note), against 5–6× on the laptop.

## Open

- Share to Shrooms Agents from any Android app; several files at once.
- Who may talk to an agent is every member of its meshes; a per-device
  allowlist by key is the change for a shared mesh.
