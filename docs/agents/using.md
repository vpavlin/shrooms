# Using Shrooms Agents

Shrooms Agents shows you every coding-agent session on every machine you own,
and lets you talk to them. You can leave Claude Code working on a laptop or a
home server, walk away, and still see what it says, allow what it wants to
run, and answer its questions from your phone or from Basecamp.

There are two front ends, and they do the same things: the **Shrooms Agents**
Android app, and the **shrooms_agents** module in Basecamp. Where they differ,
this page says so. Getting the agent onto a machine, and the apps onto your
devices, is in [Setting up](setup.md). The agent's whole API is in
[the reference](../agents.md).

## The session list

Open the app (on the phone, from the "agents" link in the shrooms app) and it
looks for agents on every peer of your mesh. What it finds is a list by
machine: each machine's name and mesh, and under it that machine's sessions.

Each session is a card with:

- **Its name**, and its state on the right:

| State | Means |
|---|---|
| **WORKING** | a turn is in progress |
| **NEEDS YOU** | it waits on you: a permission prompt or a question. The card is outlined in amber |
| idle | its process is running, with nothing to do |
| asleep | its process was stopped after half an hour idle; the next message wakes it, on the same conversation |
| unreachable | its machine is not answering (below) |

- **An unread count**: replies that came since you last had the session open
  on this device, with the app in front. Each device counts its own.
- **A preview**: the start of its last reply, two lines.
- **A line of facts**: when it last did something, how full its context is,
  the harness (Claude Code or pi), the model, "auto-approve" if it never asks,
  and its directory.
- **"1 task" or "n tasks"** when other agents have given it tasks that are
  still open, and **"⚠ a task stalled — no progress after the reminders"**
  when one of them has stalled. See [Tasks from other agents](#tasks-from-other-agents).
- **CAGED**, a violet tag, for a session that runs in a cage. See
  [Cages](#cages).

**Starring.** The mushroom beside a card stars it. Starred sessions are listed
first, under "🍄 STARRED", across all machines, with the machine's name on
each card; the machines follow with their other sessions. A star is kept on
the agent's machine, not on the device, so every phone and every Basecamp sees
the same starred sessions.

**Unreachable machines stay.** A machine that stops answering, say because
the network dropped for a moment or the laptop went to sleep, is not removed.
It stays where it was with its sessions as last seen, greyed, and marked
"unreachable · seen 14:02". It is forgotten after a week of not answering.

**Other things in the header.** "usage" opens [Usage](#usage), and "voice" the
read-aloud settings ([Voice](#reading-aloud)). On the phone,
"refresh" looks again at once, and "+ machine" adds a machine by its mesh
name (`laptop.home.mesh`) when it is not found among the peers on its own.

## The board

Basecamp has a second layout beside the list: **the board**. "board" in the
list's header switches to it, and "list" in the board's header switches back.
Basecamp remembers which one you used last. The phone keeps its list.

On the board every session is a card, laid out in a grid that fills the
window: starred sessions first, then each machine's. A card has the session's
name, its machine, its state, unread count and CAGED tag as in the list, and
**its last six lines**: what you asked (`› you: …`), what the model said, the
tools it ran (`▸ Bash make test`) and its tasks (`◆ task …`). Under them, the
same line of facts as in the list, and the stalled-task warning when there is
one. So you can see at a glance what every agent is doing, without opening
any.

**Links between agents.** When one session has asked another for something
(a task, see [Agents together](together.md)), a dashed curve runs from the asker's
card to the worker's for as long as the task is open. Its colour says how the
task stands:

| Colour | The task is |
|---|---|
| green, moving | being worked on |
| amber | blocked: the worker needs something from the asker |
| red | stalled: the worker went quiet through all its reminders |
| grey | queued: the worker is busy with something else |

A legend at the top of the board shows these whenever there is a link. A task
asked from a shell or from another A2A client has no card to start from, so
it draws no line.

**Opening a session from the board.** Click a card and the session opens,
with the list beside it, so you can move to another session in one click. To
go back to the board, press **Esc** or click **"← board"** above the
conversation. Esc first closes whatever is nearer: a dialog, or the search.

## A conversation

Tap or click a session to open it. Its header has the name, the machine and
mesh, harness, model and a thin bar for how full the context is. Under that,
the session's switches and actions (each described below): auto-approve,
auto-play, stop, search, cage, restart and delete.

**Replies stream** as they are written, with a cursor at the end, and
"thinking…" or "writing…" while a turn is in progress. Replies are rendered as
Markdown: headings, lists, quotes, tables, code. Links open in the browser.

**Tool calls are folded.** Each tool the model runs is one line, `▸ Bash`
and the first line of the command; tap or click it for the whole of it. Its
output is folded the same way, the first line shown, the rest on a tap.

**Copying.** Tap or click a piece of inline code or a code block in a reply
and it is copied to the clipboard; the app says "copied". "copy" beside each
message copies the whole message as written, Markdown included. On the phone
you can also long-press to select part of a message.

**Permission prompts.** When Claude Code wants to run something it has not
been allowed, the conversation shows a card, "BASH WANTS TO RUN", with the
command in full and its description, and the session's state turns to NEEDS
YOU. **ALLOW** lets it run; **DENY** refuses, and the model is told so. Once
answered, the card says what was decided.

**Questions from the model.** When the model asks you something (Claude
Code's AskUserQuestion), it shows as a card of its own, "CLAUDE ASKS": each
question with the options offered, where you can pick one, or several where
it says "pick any", or write your own answer under "or in your own words".
**ANSWER** sends all the answers together; **DECLINE** declines. A question
is always asked, even in a session that auto-approves.

**Auto-approve, per session.** "asks first" in the header means permission
prompts come to you; tap it and it becomes "AUTO-APPROVE": the session runs
whatever it decides to run without asking, as `claude
--dangerously-skip-permissions` would. Tap again to go back. The change is
noted in the conversation. pi never asks for permission, so a pi session has
no such switch. Auto-approve in a cage is much safer than on the machine
itself; see [Cages](cages.md).

**Stopping a turn.** "■ stop", in the header and beside "thinking…" while it
works, interrupts the turn in progress. The session stays; your next message
starts a new turn. Stopping also pauses reminders for tasks the session has
open.

**Restart.** "restart" in the header, and "↻ restart" beside stop while it
works, ends the session's process and starts it again on the same
conversation. It is for a session that stop does not reach, such as one hung
on a dropped connection. What it was doing is lost; you are asked first.
Other sessions on the machine are not touched.

**Rename.** Tap or click the session's name in the header. Its history, its
unread count, queued messages and what it is doing go with it.

**Delete.** "delete" in the header, or on the phone a long press on the
session in the list. This stops the session and removes it from the list.
The Claude Code conversation itself stays on the machine, and can be
continued again as a new session (below).

**Earlier events.** A conversation opens at its last 300 events. At the top,
"— 1200 earlier events not loaded · load 150 more —" loads more, a step at a
time. Above a session's first event you may see "— earlier, from the
transcript —": turns of the conversation from before the agent had it, such
as ones typed in a terminal.

**Search.** "search" opens a search over the whole conversation, run on the
agent's machine: it covers what the app has not loaded, and what was said in a
terminal before the agent had it. Case and accents do not matter. Results are
the newest 100; pick one and the conversation jumps to it, loading further
back if needed, and lights it for a moment. A result from before the agent had
the conversation opens on its own.

**Offline.** Each device keeps a copy of recent conversations. A session whose
machine is away still opens, marked "offline — as it was 14:02", and catches
up when the machine answers. On the phone, the background watcher keeps the
copies current, so a conversation is there even if you never opened it on
this phone.

## Starting a session

"+ session" beside a machine's name opens "NEW SESSION ON …":

- **name**: what the session is called, such as `webapp`. Letters, digits,
  dot, dash and underscore.
- **directory**: where it works, on that machine. `~` is that machine's home.
  A directory that does not exist is made.
- **the harness**, when the machine has more than one: Claude Code or pi.
- **auto-approve**: never ask, as above. Only for a harness that asks.
- **in a cage**, when the machine has podman: run the session in a rootless
  container of its own. Root inside, and of the machine only the project, the
  files sent to it and the harness's settings. Its options: the image (the
  machine's own, the workbench, or the desktop one with Xvfb, xdotool and
  ffmpeg), **nix** (the machine's nix store and profile, when it has nix) and
  **GitHub login** (your `gh` login, read-only). The first caged session on a
  machine builds the image, which takes a few minutes. See [Cages](cages.md).

**Continue a conversation from a terminal.** Below the form, "OR CONTINUE A
CONVERSATION" lists the Claude Code conversations that ran in a directory on
that machine, newest first, with the last thing you said and the last thing
Claude said. Pick one and it becomes a session, named after its directory,
carrying on the same conversation rather than a copy of it. One already
continued says "continued here as …". This is Claude Code's only, so far.

A conversation still open in a terminal is marked **"open in a terminal: …"**
(the tmux session or the process). Stop it first, with "stop it" beside it or
in the terminal itself: if both carry on, the two write over each other's
turns.

## Files and voice notes

**Files.** 📎 picks a file to send. In Basecamp you can also drop files onto
the conversation, or paste an image from the clipboard (that needs
wl-clipboard or xclip). The file is kept on the agent's machine and named by
its path in the message it goes with, so the model can read it. Attached files
show above the message box until you send; × takes one back.

**Voice notes.** 🎤 starts recording; tap again to stop and send. The
recording is sent to the agent's machine, transcribed there (by Parakeet,
which works out the language itself), and what was said goes to the model as
your turn. No audio reaches a speech service. While it is transcribed the
conversation says "🎤 voice note — transcribing on the agent's machine…". If
it fails (no speech model on the machine, nothing heard), it says why, and
**"transcribe again"** retries from the same recording. Voice notes work only
on a machine set up for them ([Setting up](setup.md)). A message typed after a
voice note reaches the model after it, in the order you sent them.

**The outbox.** Everything you send goes into an outbox on your device first,
and is sent from there: at once if the machine answers, later if it is away
or you are offline. Until it has gone it shows at the bottom of the
conversation as "QUEUED · sending to laptop…", or "QUEUED · waiting for
laptop — " and why, with its files ticked as each is uploaded; "cancel" takes
it back. Messages to one session go in order, and a message sent again after a
lost answer is never delivered twice. On the phone, the outbox is emptied in
the background too, with the app closed.

## Reading aloud

"▶ listen" on any reply reads it aloud. **"auto-play"** in a session's header
reads its new replies as they come, in order; tap it again to stop. Only the
model's text is read, never tool calls, their output or its thinking. Code
blocks are named rather than spelled out, and paths are said as a person
would ("session.go, line 654"). Czech or English is picked per reply.

While a reply is read, it shows as its sentences with the current one lit,
and a bar above the message box has pause and resume, back and on a sentence,
stop, and "show" to scroll back to it.

On the phone, auto-play also reads replies with the app in the background, so
you can follow a session with the phone in your pocket.

**A natural voice.** "voice" in the list's header says what reads now, and
how to get a natural voice, Piper, which runs offline:

- **Phone:** it reads with Android's speech engine. Install SherpaTTS from
  F-Droid, pick an English voice in it (en_US lessac or ryan, medium or high),
  and make it the preferred engine in Android's text-to-speech settings. The
  "voice" panel links to each step, and has a test.
- **Basecamp:** **SET UP** downloads Piper with an English voice, about 90 MB,
  into `~/.local/share/shrooms/piper`; nothing is installed system-wide.
  "▶ try" plays a sample, "remove" takes it away. Without it, Basecamp reads
  with speech-dispatcher (`spd-say`), which sounds robotic.

More on the choice of voices is in [the voices](../agents-voices.md).

## Notifications

The phone app watches your sessions in the background, with a quiet,
permanent notification, "Watching your agents", which Android needs to keep it
running. It notifies you when a session:

- **needs you**: it starts waiting on a permission prompt or a question;
- **replied**: a turn ended, with the start of what it said ("finished" if
  nothing). Once per poll however many turns ended, and not for the
  heartbeats a session sends while it waits on background work;
- **has a task that stalled**: a task from another agent got no progress
  through all its reminders.

Tapping a notification opens the session. There is no notification for the
session you have open on screen, and none for a session the first time the
app sees it. Basecamp has no notifications; its list and board show the same
states.

## Usage

"usage" in the header opens a dashboard of what your sessions used, gathered
from every machine's agent. Each machine is asked on its own and shown as it
answers; "still asking" names the ones that have not yet, and "not reached"
those that are away.

**The header link at a glance.** The link itself shows how far the busiest
Claude subscription is into its 5-hour window, "usage 62%". It turns amber
from half and red from 80%, or as soon as a request was refused: time to slow
down.

**Plan limits** come first. For each Claude subscription (machines logged in
to the same account are shown once): a bar for each window, the 5-hour one
and the 7-day ones, with its share used and when it resets. Bars are amber
from half and red from 80%. Above them, what the newest request was told, when
it matters: "past 90% of 5 hours", or "limit reached (5 hours) — back at
16:00". These come from Claude Code's own reports after each turn, so a
reading is as fresh as that machine's last turn, which "as of" says. A window
that has reset since then shows "started again … · no reading since".

**Forecasts.** Under each bar, where it is heading at the pace of the last
hours: "at this pace: about 70% at the reset — it lasts", or, in red, "at this
pace: runs out Sat 14:00 — before it resets".

**Credits**, for pay-as-you-go keys. A machine whose pi uses Venice shows the
key (by a fingerprint, never the key itself) and every machine using it, with
what is left: "5.62 DIEM left today", and "refills 02:00", when the daily
allowance comes back. Under it, the same kind of forecast: "at this pace:
about 1.4 DIEM left at the refill", or when it would run out before.

**The dashboard.** Below, what was used, summed three ways:

- **WHO ASKED**: the device each turn came from, such as your phone or the
  laptop's Basecamp. Turns another agent asked for count under that agent's
  machine; reminders the agent sends itself count under "shrooms".
- **WHERE IT RAN**: the machine.
- **MODEL**: the model.

Choose the period, **today**, **7 days**, **30 days** or **all**, and what
the bars measure: **tokens out**, **turns**, **cost** or **busy** time. Each
line also shows all of them: turns, tokens out and in, cost, and hours busy.

## Cages

A session that runs in a cage carries the violet **CAGED** tag in the list
and on the board (in Basecamp, hover over it for the image and options), and
"in a cage (…)" in its header.

"cage" in an open session's header moves it in or out of a cage. For a
session on the machine, it offers the same options as a new caged session and
**MOVE IN**; for a caged one ("caged" in the header), **CHANGE** to other
options or **TAKE OUT**. The session's process is stopped and the conversation
carries on, in its new place, with your next message. A cage changed or left
is deleted, with whatever was installed in it. A working session cannot be
moved: wait until it is idle. What a cage is and what it protects is in
[Cages](cages.md).

## Tasks from other agents

Your sessions can give each other tasks ([Agents together](together.md)). In the
worker's conversation you see:

- **the task**, as a message beginning `[shrooms task …]`, with the asking
  device and the session it says it is;
- **reminders**, when the worker goes quiet with a task unfinished. They are
  labelled **SHROOMS** rather than YOU: they come from the agent, not from
  you. Other turns the agent's harness starts by itself are labelled by their
  source the same way;
- **a note** each time a task ends or stalls: "— task review:… completed —
  three problems: …", "blocked", "failed", "stalled", "expired".

On the list and the board, "n tasks" and the stalled warning, as above; on the
board, the links between cards, each with a count of its open tasks.

**Every task in one place:** the board's tasks panel in Basecamp, and the
**tasks** link on the phone (amber, with a count, while one waits on you).
Tasks are grouped as **Needs you**, **Working**, **Stalled** and **Done,
unacked**. Each one shows what was asked, who asked whom and how long it has
been quiet. A queued task, waiting for its session to be free, says
"queued". **ACK** (or **ACK all**) tells the worker's agent you have seen a
result; acknowledged tasks leave the list. Tap a task to open the worker's
session at the message where it arrived. In Basecamp, tap a link's count to
see only the tasks between those two sessions.

The apps cannot cancel a task yet; how to do it from a shell is in
[Agents together](together.md).

## Where the phone and Basecamp differ

| | Phone | Basecamp |
|---|---|---|
| Layout | the list | the list, or the board |
| Tasks | the **tasks** screen | the board's tasks panel, and link filters |
| Notifications | yes, in the background | none |
| Delete a session | long press in the list, or "delete" | "delete" in the header |
| Attach a file | 📎 | 📎, drop on the conversation, or paste an image |
| Send | ↑ | Enter (Shift+Enter for a new line), or SEND |
| Read aloud | Android's speech engine; also in the background | the core, with Piper or `spd-say` |
| Natural voice | SherpaTTS with a Piper voice | one click, SET UP |
| Find a machine by name | "+ machine" | no |

## See also

- [Setting up](setup.md): installing the agent and the apps
- [Agents together](together.md): agents giving each other tasks
- [Cages](cages.md): sessions in containers
- [the reference](../agents.md): the agent's API, the CLI and the files
