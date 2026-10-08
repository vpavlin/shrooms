# Agents on the mesh

**Status (2026-10-03):** in daily use from a phone and from Basecamp, on the
day it was built. **Shrooms Agents** is its own app and its own Basecamp
module, beside shrooms (decided 2026-10-03, below).

Talk to the Claude Code sessions on every machine you own, from the phone,
over the mesh — and approve what they want to do from there. It replaces `cl`
(tmux sessions reached over SSH), whose phone interface is a terminal.

The point is privacy and independence: nothing leaves the mesh. Claude Code's
own Remote Control does much of this through a cloud relay; this does not.

## Shape

```
Shrooms Agents (Android)  ─┐
Shrooms Agents (Basecamp) ─┼─ HTTP over the mesh ──▶  shrooms-agent (each machine)
                           │                              │ stream-json, one process per session
                           │                              ▼
                           │                           claude -p
shrooms (VPN, the mesh) ───┘  the network both run over
```

- **`shrooms-agent`** is a separate binary, not part of the daemon. Shrooms is
  the network; something that runs code on request does not belong in its
  most privileged process. It runs as the user (a systemd user unit, started
  through a login shell so sessions find what a terminal finds).
- **Shrooms Agents is a separate app, and a separate Basecamp module**
  (decided 2026-10-03). Agents grow out of the mesh and are not the mesh: the
  two change at very different speeds — the network should be boring, the
  agents UI changed a dozen times on its first day — and an app that records
  audio and drives machines that run code is a bigger target bundled into a
  VPN. The dependency runs one way: agents need the mesh for addresses, access
  and discovery; the mesh does not need agents. Its mark is the mushrooms
  alone (assets/agents_logo.py), shrooms' is the mycelium.
  - **Android:** the "agents" build of the same code
    (xyz.vpavlin.shrooms.agents), signed with its own key, kept outside the
    repository (~/apk-signing/shrooms-agents) with its password beside it. It
    is no mesh client: the shrooms app's "agents" link opens it and hands over
    the peers it can reach, and an agent lists the mesh as its machine sees it
    (/v1/peers), so knowing one finds the rest.
  - **Basecamp:** `shrooms_agents`, a view of its own that shares
    `shrooms_core` — which does all the networking, since Basecamp's sandbox
    forbids it in a view, on threads of its own so no call freezes the window.
- One repository, one set of tools.
- **Not only Claude Code.** Sessions run on a pluggable harness: Claude Code,
  or pi (on any model pi is set up with, a local one included), with more to
  be added the same way — docs/agents-harnesses.md is the guide.

## Access: the bind is the access control

`shrooms-agent` listens on this device's **overlay address** on each mesh it
serves, on a fixed port (**7387**), and nowhere else. Per ADR-026, only mesh
members can route to that address: the WireGuard tunnel admits configured
members only. So:

- reachable by members of those meshes, not by the LAN or the internet;
- the caller's source address *is* its device key's overlay address, so every
  request is attributed to a named device with no logins and no tokens;
- the port appears in peers' view through `announce_bound`, which is how the
  phone discovers it.

**Decision (2026-10-03): who may talk to an agent = the members of the meshes
listed in its config.** Every device on the meshes is the owner's today; the
list is the rail for the day a mesh is shared. To change later: a per-device
allowlist by key.

Another local user on the same machine can also connect to the overlay
address. That machine is already theirs to run code on, so it is not a new
exposure.

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
a session behaves as it would in a terminal.

A process lives while the session is in use and is stopped after it has been
idle for a while; the next message resumes it by id. A session is a name and a
directory — the same thing `cl` keys on.

## API (HTTP + JSON, server-sent events)

On each machine's overlay addresses, port 7387.

| | |
|---|---|
| `GET /v1/sessions` | list: name, directory, state (idle / working / waiting), pending prompts, context used and window, model, the last reply, auto-approve, `harness` and its `caps`, and `turns` — how many turns have ended, which the phone notifies on, once each (events are no use for that: a session waiting on background work sends heartbeats and progress between turns). A turn Claude Code starts by itself, when background work finishes, makes the session working |
| `GET /v1/harnesses` | `{"harnesses":[{name, title, caps:{approve, takeover}}]}` — the coding agents this machine runs sessions of, Claude Code first (docs/agents-harnesses.md) |
| `POST /v1/sessions` | `{name, dir, harness?, auto_approve?}` — create, with Claude Code unless `harness` names another; `dir` (`~` is the agent user's home) is made if it does not exist, once the request is otherwise accepted, and refused if it is a file; `{name, resume: id, harness?}` — continue an existing Claude Code (or, with `harness: "pi"`, pi) conversation, in the directory it ran in |
| `DELETE /v1/sessions/{name}` | stop and forget |
| `PATCH /v1/sessions/{name}`, `POST …/settings` | `{auto_approve?, starred?, keep_running?}` (POST for clients that cannot send PATCH). A star is kept on the agent, so every device lists starred sessions first, above each machine's others. `keep_running`: the process is never stopped as idle and is started again within 15 s whenever it ends, also after the agent restarts — for an agent that works on its own (a heartbeat, a chat bridge in its extensions), Jimmy on pi5 being the first. Also accepted by `POST /v1/sessions`; listed as `keep_running` |
| `GET /v1/sessions/{name}/search?q=…[&limit=N]` | `{"found":[{seq, time, role, snippet, text}]}`, newest first (50 by default, at most 200): the turns of the whole conversation containing q — what was typed and the model's text, not tools — ignoring case and Czech diacritics. From the session's events (`seq` to jump to) and, for what came before them, the transcript (`seq` 0, with the whole `text`). About a second on a 190 MB transcript |
| `GET /v1/sessions/{name}/events?after=N[&tail=T]` | the session's events, then a live stream (SSE); `partial` events carry reply text as it is written, unnumbered and never kept. `tail=T` with `after=0` starts at the last T events instead of the first: both apps open a session at its last 300 and offer to load the rest |
| `GET /v1/sessions/{name}/history?limit=N` | what was said before this agent had the conversation, from Claude Code's transcript (its last 4 MB) |
| `POST /v1/sessions/{name}/messages` | `{text}` — a user turn |
| `POST /v1/sessions/{name}/prompts/{id}` | `{allow, message?, answers?}` — answer a permission prompt. A question from the model (Claude Code's AskUserQuestion tool) arrives as a prompt for that tool, even under `--dangerously-skip-permissions`, and is answered with `allow` and `answers` (question → chosen label, labels joined by ", ", or the person's own words), which the agent puts into the tool's input; allowed without answers it is refused (409), since the model would read it as "the user did not answer". Auto-approve never answers a question |
| `POST /v1/sessions/{name}/interrupt` | stop the current turn |
| `GET /.well-known/agent-card.json` | A2A (v1.0): this machine's Agent Card — its sessions as skills, each naming its own card. Design: `docs/agents-together.md`, "On the wire: A2A" |
| `GET /a2a/{name}/.well-known/agent-card.json` | a session's Agent Card: `name` `machine/session`, one JSON-RPC interface at `/a2a/{name}`, streaming on |
| `POST /a2a/{name}`, `POST /a2a` | A2A JSON-RPC 2.0: `SendMessage` (`configuration.blocking` waits up to 10 min, until the task is finished or needs input), `SendStreamingMessage`, `GetTask`, `SubscribeToTask`, `CancelTask`, `ListTasks`, and the extension's `AckTask`. A task (ADR-042, `docs/a2a-tasks.md`) is open until the worker finishes it with `task_update` — done, blocked, failed — not when a turn ends; a busy session queues it; a quiet one is reminded on a ladder and in the end marked stalled. Its id is `session:messageId`; text parts only; a `SendMessage` with `taskId` answers a blocked task. The message is sent as from the device the mesh names, with the sender's claim from `metadata["shrooms/from"]` after it ("pi5.default (pi5/jimmy)"). More than 30 messages to one session from one device in an hour are refused (-32050). `POST /a2a` finds the session from the task id or `metadata["shrooms/session"]`. `shrooms-agent a2a list\|send\|get\|cancel\|ack\|update` is a client for agents |
| `GET /v1/tasks[?session=]`, `POST /v1/tasks/{id}` | the tasks as A2A shows them, for the apps; the worker's `{state: done\|blocked\|failed, summary, session}` — from this machine only (its sessions' `task_update`) |
| `POST /v1/sessions/{name}/rename` | `{name}` — the session is called that from now on (`409` if taken or not a valid name). Its event log moves with it; files sent to it stay where they were kept, since its messages name them by path; its process goes on undisturbed. Recorded as a `renamed` event `{from, to}` with who asked, on which every app following it moves what it keeps under the name (the copy, unread, auto-play, the outbox) and goes on under the new one. Both apps rename on a tap or click on the session's name |
| `POST /v1/sessions/{name}/restart` | end the session's process at once — killed, since a request hanging on a dropped connection (`API Error: Connection dropped (ECONNRESET)`) answers neither an interrupt nor the end of its input — and start it again on the same conversation (`--resume`). The turn in progress is lost; turns queued behind it go on. Only this session's process: the others on the machine, which a restart of shrooms-agent would end too, are untouched. Recorded as a `restarted` event with who asked; both apps offer it in the session's header and beside "■ stop", after a confirmation |
| `POST /v1/sessions/{name}/files?name=` | the bytes of a file (50 MB at most); kept under the agent's own directory; returns `{path}` for the next message to name |
| `POST /v1/sessions/{name}/voice?name=&id=` | a voice note as a turn: kept like a file, answered at once (202 `{path}`); transcribed here in the background and sent as the device's message (`message` event with `voice`: the recording's path). Its progress is a `voice` event (`transcribing`, or `failed` with `error`). The same `id` again answers 200 `{"duplicate":true}` |
| `POST /v1/sessions/{name}/voice/{id}/retry` | transcribe a failed voice note again, from its kept recording |
| `POST /v1/sessions/{name}/transcribe?name=&lang=` | a voice note, kept like a file and transcribed on this machine; returns `{path, text}`. Parakeet v3 (the default) detects the language and ignores `lang`; with a Whisper model (`--stt-model`), naming it halves the time |
| `GET /v1/conversations?limit=N` | this machine's Claude Code conversations, newest first: where each ran, its last exchange, the session continuing it, and any terminal `claude` open in the same directory (with its tmux session). Only those whose directory is on this machine: a `~/.claude` copied from another machine brings its transcripts along, and those cannot be continued here (`POST /v1/sessions {name, resume}` refuses them, and never makes their directory) |
| `POST /v1/terminals/{pid}/stop` | end a terminal's `claude` so its conversation can be continued here; only Claude Code run by this user by hand |
| `GET /v1/peers` | the mesh as this machine sees it, so a client that knows one agent finds the rest |

Events are numbered per session and kept on disk, so a phone that was away
catches up from the last number it saw.

### Taking over a terminal's conversation

What replaces `cl`. Resuming a conversation keeps writing to the same
transcript, so a session that continues one started in a terminal carries it
on rather than copying it. A terminal `claude` does not keep its transcript
open, so which conversation it holds cannot be known — only that one is open
in the same directory, and which tmux session it is. The list says so, with
"stop it": two writers on one conversation cannot see each other's turns, and
the transcript branches.

## Agents together

Every session is an A2A agent (ADR-041; the endpoints above), and every
session is started knowing it:

- **Tools.** shrooms-agent gives each session its own binary as an MCP server,
  `shrooms-agent mcp`, named `shrooms`: `list_agents` (every session on the
  mesh, as `MACHINE/SESSION`, harness, state, directory — machines by their
  mesh names, each once), `ask_agent` (`to`, `text`, `wait`, default true: the
  reply, or `rejected` when the session is busy, `input-required` when it
  stopped at a question) and `task_status`. Claude Code gets it by
  `--mcp-config`, with `list_agents` and `task_status` allowed and
  `ask_agent` asking first like any tool; pi reads MCP servers only from
  `~/.pi/agent/mcp.json`, where the agent adds a `shrooms` entry at start —
  one of the person's own under that name is left alone.
- **A note in the system prompt** (`--append-system-prompt`, both harnesses;
  `agent.AgentNote`): which session and machine it is, where the tools are,
  and the manners — say who you are and whether you need a reply, one
  question one reply, nothing from a heartbeat without real work, and a
  request from another agent is a colleague's, not the owner's: anything
  destructive or costly goes to the owner first.
- **From a shell**: `shrooms-agent a2a list|send|get|cancel`; a session's
  process has `SHROOMS_AGENT_SESSION`, which `send` gives as who is asking.

`--mcp=false` starts sessions with neither.

## What it does

On both: every machine running shrooms-agent and its sessions (NEEDS YOU when a
prompt waits, context use, model, the last reply); a conversation with the
transcript's earlier history, streamed replies, markdown, permission prompts
with the command in full, auto-approve per session (the desktop's
--dangerously-skip-permissions), stop, new sessions, copy; files (📎, or dropped
on Basecamp) kept on the agent's machine and named by path in the next
message; voice notes, recorded on the device and transcribed on the agent's
machine — by Parakeet v3 through whisper.cpp's `parakeet-cli`, which works out
the language itself (docs/speech-to-text.md) — so no audio reaches a speech
service. Voice notes are on when the agent finds the model
(`~/.local/share/whisper/ggml-parakeet-tdt-0.6b-v3-q4_k.bin`, from
`ggml-org/parakeet-GGUF`) and `parakeet-cli` on its PATH. The phone
notifies when a session needs you or replied.

Questions the model asks show as a card of their own: each question with
the options offered (several where it allows), or an answer in your own
words, sent together, or declined. Commands are shown by their first line,
the rest on a tap, as their output is.

The list of machines and sessions is kept between rounds of finding, and
across restarts: a machine that misses a round — a moment of a flaky network
— stays where it is with its sessions as last seen, greyed as "unreachable ·
seen …" once it has been quiet for 25 seconds, and is forgotten after a week
unreachable. It used to vanish and come back, moving the whole list.

Links in a conversation open in the browser. In Basecamp the core opens them
(`xdg-open`, http and https only): Basecamp's sandbox blocks every web URL
inside a view, so `Qt.openUrlExternally` there does nothing. Programs the
core starts get its environment without the AppImage's loader settings: its
`LD_PRELOAD` (with `__BUNDLE_REAL_EXE`) makes Ubuntu's multi-call coreutils
refuse to run, which silently broke `xdg-open`, a shell script. In Basecamp
the core keeps a session's last 4000 events; "load them" on a longer
session shows those, not the very first.

Both apps write through an **outbox**: a message or voice note goes there
first and is sent from there — at once if the machine answers, later if it
is unreachable or the device is offline — in order per session, shown in
the conversation as *queued* until it has gone, and cancellable until then.
On the phone it is sent by whichever runs, the conversation on screen or the
watcher in the background; in Basecamp, by a thread of the core, from
`~/.local/share/shrooms/outbox`. Each carries an id made on the device, which
the agent takes once (`POST …/messages {text, id}` answers
`{"duplicate":true}` to a repeat, from memory of the last thousand and the
log), so sending again after a lost answer cannot send twice.

**Files go with their message.** A file picked (or, in Basecamp, an image
pasted) is copied into the outbox's folder at once — nothing is asked of the
agent's machine, so attaching works with it away and holds up nothing — and
queued with the message. When it is sent, each file is uploaded first
(`POST …/files`), where the agent kept it recorded as soon as it is known, so
a send that fails after it does not upload it again; then the message, naming
every file by that path. A queued message lists its files, ticked once
uploaded. Cancelling it deletes the copies; a copy no message took is
deleted a day later. (Uploading on attach waited on an unreachable machine —
a minute on the phone, two in Basecamp — with sending blocked, and then lost
the file.)

**Voice notes are turns**: the recording itself is sent, the agent keeps it,
transcribes it on its machine and sends what was said — nothing comes back
to read and confirm. Kept first, so one that fails (no model, nothing
heard) says why and can be transcribed again from the same recording.
The agent sends a session's turns in the order they arrived: a message that
comes while a voice note is still being transcribed waits behind it (it is
taken — answered 202, its id remembered — and sent once the note has gone or
failed), so recorded-then-typed reaches the model in that order. A note that
fails does not hold up what came after it; transcribed again later, it goes
then.

**A conversation is kept on the device**: the newest 300 events of each one
(at most a megabyte; streamed text left out, and any string over 4 KB —
nearly always a tool's output, shown folded anyway — cut). A conversation
opens on its copy at once, marked "offline — as it was HH:MM" until the
machine answers and never shown as working, and only the events after the
copy's last are asked for — usually none or a few. Opening by replaying the
last 300 instead, tool output and all, took tens of seconds over the mesh,
and the conversation was rebuilt under the reader as it came: the scrolling
on every switch. With no copy, the replay is gathered and shown once it
reaches the session's newest event as listed. A copy more than those 300
events behind the session's newest as listed — it went on from another
device while nothing here watched it — is not caught up from either: that is
the same replay, of every event since, and opens at the end as without one
(Basecamp, whose copies are kept only while a conversation is open; on the
phone the watcher keeps them current). A session whose numbers are
below its copy's was deleted and made again: the copy is dropped and it is
opened without one. On the phone the copies are in the app's files
(`history/`), with the 30 turns from before the agent had the conversation,
and kept up to date by the background watcher for every session it sees,
reading only what a copy lacks, so a conversation is there offline without
having been opened first; in Basecamp the core keeps them in
`~/.local/share/shrooms/history`, one file per machine address and session,
saved while a conversation is open (`agentEvents` says `"kept":MS` while the
copy is unconfirmed; a negative tail to `agentWatch` opens without it).
The turns from the transcript ("earlier") are shown only above a session's
own first event: above a later one they repeated, out of place, turns the
agent also has. Basecamp asks for them only then, once the machine has
answered: asked first, as it was, the pane stayed blank for a round trip
over the mesh (and up to 5 s, the view frozen, for a machine that was
away). Until a conversation has anything to show, both apps say what they
are doing — reaching the machine, loading, or that it has no messages yet.
Deleting a session deletes its copy.

**Replies read aloud** (both apps): "▶ listen" on each of the model's
replies reads it, and "auto-play" in a session's header reads its new replies
as they come, in order — the model's text only, never tool calls, their
output or its thinking, and nothing from before it was switched on. It is
read sentence by sentence, by the app rather than the engine, so it works the
same with any engine: while a reply is read it shows as its sentences with
the one being read lit, and a bar above the message box has pause/resume,
back and on a sentence, stop, and "show" to scroll back to it. The text is
read as prose: code blocks named rather than spelled out, links as their
text, markdown marks dropped, and paths said as a person would
("internal/agent/session.go:654" is "session.go, line 654"); Czech or
English is chosen per reply from its letters. On the phone it is Android's
system speech engine — a natural voice is one install away
(docs/agents-voices.md) — and the background watcher reads new replies with
the phone in a pocket; screen and watcher share one mark of what was read, so
nothing is read twice. In Basecamp the core speaks — Basecamp ships no Qt
TextToSpeech — with Piper when it is set up in `~/.local/share/shrooms/piper`
— set up with one click in its "voice" section — otherwise
speech-dispatcher's `spd-say`. Both apps' "voice" section says what reads
now and how to get a natural voice (docs/agents-voices.md).

**Usage** (both apps, "usage"): who uses the agents, how much, and where —
the basis of sharing a model on a machine of your own fairly, or of billing
for it. Each agent reads it out of its session logs (`GET /v1/usage
[?since=2006-01-02]`: rows per day, session, device and model with turns,
tokens, cost and busy time), history included; the apps ask every machine and
sum it by **who asked**, **where it ran** and **which model**, each machine
asked on its own and shown as it answers ("still asking", "not reached" for
one that is away; Basecamp asks through its core in the background,
`agentGather`),  over today, 7
or 30 days or all, by tokens out, turns, cost or busy time. Who asked is the
turn's sender: the mesh peer its request came from, which WireGuard makes
unforgeable, or the agent's own machine for its local socket (Basecamp on it)
— counted under that machine's name. A turn's tokens are its result's
`usage`; its cost is what it took the harness's running total above its
highest so far (Claude Code's total runs across restarts and dips on a resume;
counting from the last figure counted every dip twice), the log's first
result only setting where the count starts; busy time is from the asking to
the answer, a turn left waiting over two hours counted by its own duration.

**Plan limits** (top of "usage", both apps): where each Claude subscription
stands — the 5-hour and 7-day windows' share used and when each starts again,
and what the newest request was told (allowed; past a threshold Claude Code warns at —
90% of the 5-hour window, 50% of the 7-day one — with the share; limit
reached, and when it is back). Claude Code reports this after every turn
(`rate_limit_event` in its stream, which the agent logs like everything else);
`GET /v1/usage` returns the newest report among the machine's sessions as
`limits` (`at`, `status`, `window`, `overage`, `windows.{five_hour,seven_day,…}
.{utilization, resets_at}`), or null when Claude Code never reported one.
Machines whose windows reset at the same moments share an account and are
shown once, with the newest reading. Bars turn amber
from half a window and red from 80%. The session quota — the 5-hour window —
is also on the link that opens usage, at a glance ("usage 62%"), coloured the
same way and red once a request was refused: time to slow down. For that,
`GET /v1/sessions` carries the same `limits`, which the agent keeps as Claude
Code reports them (and reads from the logs once at startup, so a restart does
not forget the last one). A reading is only as fresh as the last
turn on that machine, which "as of" says; and it is Claude Code's own stream,
not a documented API, so the fields are read defensively.

**Credits** are the same for pay-as-you-go keys. An agent whose pi uses Venice
(a provider in `~/.pi/agent/models.json` with an `api.venice.ai` base URL; its
`apiKey` written out or `$VAR` from the agent's environment) asks Venice where
each key stands every 5 minutes — `GET /api/v1/api_keys/rate_limits`, which an
inference key may ask — and `GET /v1/usage` and `GET /v1/sessions` carry it as
`credits`: `[{provider, key, balances: {DIEM, USD, …}, resets_at, at, error}]`.
`key` is a fingerprint (the first 4 bytes of its SHA-256), never the key, so a
key used by several machines is shown once with all of them: "Venice key
1a2b3c4d · pi5, proteus, scribe — 5.62 DIEM left today · refills 02:00". DIEM is
the daily allowance, refilled at `resets_at` (Venice's next epoch, midnight
UTC). A key's own spending limit and its usage history need an admin key, which
the agents do not have; what each turn cost is in the usage rows above.

**Forecasts** (`forecast.go`): whether a limit lasts until it renews, at the
pace it is being used. Each plan window carries `projected` (its share at the
reset), `runs_out_at` (when it would reach all of it, if before) and `pace`:
`recent` — from the readings of the last hour of a 5-hour window, the last day
of a 7-day one, measured up to now so idle hours count — or `window`, the
average since the window began when there is too little history; readings of
an earlier window of the same name are not its history. A Venice key carries
`runs_out_at` (before the refill) or `left_at_refill`, from the balances of the
last two hours of the same day. The apps say it under each bar: "at this pace:
runs out Sat 14:00 — before it resets" in red, or "at this pace: about 70% at
the reset — it lasts".

**Unread replies** show as a count on each session in both apps: the
session's turns (one per reply, counted by the agent) less those there were
when it was last open on that device with the app in front. Each device
keeps its own; a session seen for the first time starts read.

Search (both apps) finds words anywhere in a conversation, on the agent's
machine, so it covers what the app has not loaded and what was said in a
terminal before the agent had it. A result the agent has an event for jumps to
that message, loading further back if needed, and lights it for a moment; an
older one opens whole. In Basecamp the core runs the search in the background,
as it does uploads.

A conversation opens at its last 300 events, with a link to load the rest: a
long session holds thousands, and replaying them all made the phone scroll for
ten seconds before settling at the end. The phone also applies arriving events
in batches every 120 ms rather than one by one. In Basecamp, the core answers
the view in pieces of about half a megabyte and the view reads on until caught
up: a session's whole backlog in one reply through Basecamp's IPC is the likely
reason some conversations showed empty after switching to them (not proven).

## Installing it

`scripts/install-agent.sh`, run with sudo by the user whose agents it serves
(`--user NAME` otherwise), on a machine where shrooms runs:

- takes `shrooms-agent` out of its own image, `ghcr.io/vpavlin/shrooms-agent`
  (`make push-agent-image`: a static binary per architecture, amd64 and arm64,
  and nothing else), into `/usr/local/bin`, with the docker or podman shrooms
  already uses. Not a layer of the shrooms image: nodes follow
  `shrooms:latest` with podman auto-update, so publishing the agent there
  would roll a new daemon onto all of them;
- if that user cannot read the control socket, grants it by ACL, kept across
  reboots by `/etc/tmpfiles.d/shrooms-agent-USER.conf` (directory line first;
  see below for why) and across restarts of shrooms by a drop-in,
  `/etc/systemd/system/shrooms.service.d/20-agent-access.conf`:
  `RuntimeDirectoryPreserve=restart` and the ACL applied again after every
  start. shrooms' unit has `RuntimeDirectory=shrooms`, so systemd deleted
  `/run/shrooms` at every stop and made it afresh without the ACL — and the
  image's auto-update restarts it: on 2026-10-05 an update cut the agents on
  atlas and jimmy-crib off their socket. shrooms' own unit (packaging and
  `install.sh`) now keeps the directory over a restart too;
- opens TCP 7387 to this machine's own mesh addresses only, under firewalld
  or ufw;
- installs `/etc/systemd/user/shrooms-agent.service`, turns lingering on and
  starts it for that user;
- `--voice`: builds whisper.cpp's `parakeet-cli` (pinned commit) as that user
  and fetches the Parakeet model, checked by sha256;
- `--adopt-pi NAME`: takes over a pi agent that runs on its own — a
  `pi-agent.service` keeping pi in tmux with its heartbeat, as Jimmy (pi5),
  proteus and scribe did. Stops and disables that service and its tmux
  session first (two pi processes on one conversation would each write a
  branch of it), adds `EnvironmentFile=-%h/.pi/agent/.env` — the provider
  keys its start script sourced — to a drop-in, `10-pi-agent.conf` (added to,
  never replaced), and continues pi's newest conversation as session NAME,
  kept running. Re-run, it finds the session there and changes nothing
  (checked on scribe); the steps are those done by hand on proteus and scribe;
- `--uninstall` removes all of it but the voice build and the sessions.

Tried on jimmy-crib (Ubuntu, docker) and atlas (Fedora, podman, SELinux,
firewalld): from clean, re-run, uninstalled and reinstalled, and the boot
order replayed. The sections below are what it automates.

## On a server

Running: the laptop, the VPS, atlas and jimmy-crib (2026-10-04). On atlas
and jimmy-crib it runs as the owner (vpavlin), as on a desktop: they are the
owner's machines, with Claude Code and pi already logged in there. Both run
the shrooms daemon in a container, so the socket is reached by ACL as on the
VPS below — with the tmpfiles file's `d /run/shrooms 0750 root root -` line
first. Without it the ACL is applied only if the directory already exists at
boot, which it does not: jimmy-crib rebooted, the container made the
directory without it, and the agent waited for a socket it could not read
(2026-10-04; its log now says why it waits). atlas also runs firewalld, whose default zone takes the mesh
interface and dropped port 7387; one rich rule opens it to atlas's own mesh
address only:
`rule family=ipv6 destination address=<its overlay>/128 port port=7387 protocol=tcp accept`.

The VPS runs one (2026-10-03), set up the way any server would be:

- **Its own user, `agent`, no sudo.** Sessions run as that user, so
  auto-approve is safe to switch on and a session cannot touch the relay or
  anything else root owns; Claude Code also refuses
  `--dangerously-skip-permissions` as root. Lingering on
  (`loginctl enable-linger agent`), the user unit as on a desktop, Claude Code
  from its native installer in `~agent/.local/bin`, logged in once by hand.
- **The control socket by ACL.** The agent reads the daemon's status for the
  mesh addresses to serve on and the peers' names — the socket-group tier.
  The daemon there runs in a container, whose group names are not the
  host's, so `socket_group` does not fit; instead
  `/etc/tmpfiles.d/shrooms-agent.conf` puts an ACL on the host's
  `/run/shrooms` (mounted into the container) that the socket inherits each
  time the daemon makes it.
- **Voice notes:** `parakeet-cli` built from whisper.cpp in
  `~agent/.local/src`, the model in `~agent/.local/share/whisper`,
  `--stt-threads 4` for its four cores. About real time there (8.7 s for an
  8.6-second note), against 5–6× on the laptop.

## Open

- Publishing: the LAN F-Droid and Basecamp repositories live on jimmy-crib,
  which was down when this was built; the agents packages go there with the
  next shrooms release.
- Share to Shrooms Agents from any Android app; several files at once.
  (Pasting an image into Basecamp is built; it needs wl-clipboard or xclip.)
