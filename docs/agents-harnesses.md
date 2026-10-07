# Shrooms Agents: logging in, and harnesses other than Claude Code

**Status:** section 1 is for discussion; section 2 is built (2026-10-04):
harnesses are pluggable, and pi is the first one added.

## 1. A machine whose Claude Code is not logged in

Seen on the VPS: a session answers every message with "Not logged in · Please
run /login", and `/login` sent from the app answers "/login isn't available in
this environment". The agent runs `claude -p`, which has no slash commands;
logging in is something done once on the machine, outside any session.

**Today, by hand** — either of:

- `ssh` in, `su - agent`, run `claude`, then `/login`: it prints a URL to open
  in a browser anywhere, and takes the code back. Stored in `~agent/.claude`,
  refreshed by Claude Code itself.
- `claude setup-token` (on the machine, or on any machine logged in to the
  same account): prints a long-lived OAuth token (a year) for headless use,
  which goes in `CLAUDE_CODE_OAUTH_TOKEN` in the agent's environment — for the
  VPS, a line in the user unit's drop-in. Does not refresh: it has to be
  replaced when it expires.

**What the apps could do:**

- **(a) Say so.** The agent recognises "Not logged in" in a turn's result and
  reports it on the session (`/v1/sessions` → `"logged_in": false`); the apps
  show a banner on that machine saying how to log it in, instead of letting the
  person type `/login` into a box that cannot take it. Small; no secrets move.
- **(b) Log in from the app.** The agent runs `claude setup-token` (or the
  interactive login under a pseudo-terminal), hands the URL to the app, which
  opens it in the phone's browser; the person pastes the code back; the agent
  feeds it in and keeps the token. Convenient, but the agent then handles an
  account credential and drives an interactive flow whose prompts are not a
  stable interface — the first Claude Code update that rewords them breaks it.

**Recommendation:** (a) now; (b) only if logging machines in turns out to be
frequent — for a handful of machines it is a once-a-year chore.

## 2. Harnesses: Claude Code, pi, and the next one

Decided 2026-10-04: the open ones matter — pi and OpenCode — then Claude Code
and Codex; the agent is pluggable so anybody can add the one they use. Built
so far: the plug-in point, Claude Code moved behind it, and **pi**, the worked
example below. OpenCode and Codex are not built.

### How it fits together

```
apps ──HTTP/SSE──▶ shrooms-agent ── Session ── proc ──stdin/stdout──▶ claude -p
                   (one event shape)            │ Codec                pi --mode rpc
                                                └─ Harness             …
```

- A **Harness** (`internal/agent/harness.go`) names a coding agent, says how
  to start its process (`Args`), what it can do (`Caps`), and makes a
  **Codec** for each process.
- The **Codec** translates both ways: what to write for a user turn, an
  interrupt, an answer to a prompt; and what each line the process writes
  means — as zero or more messages in **the agent's event shape**.
- **The event shape is Claude Code's stream-json**, the subset listed below.
  Claude Code's codec passes lines through untouched; every other harness
  translates into it. That was chosen over a new neutral format because the
  apps, the event logs already on disk and every test speak it: a new
  harness costs one translator and no change to either app. The cost is a
  format named after one vendor; the kind of these events in a session's log
  is still `claude`, for the same reason.
- Optional abilities are separate interfaces a harness may also implement:
  **Transcripts** (its conversations on disk, for history and search of what
  was said before the agent had a session). Taking over a terminal's
  conversation is Claude Code's alone so far (`Caps.Takeover`).
- Each session records its harness (`sessions.json`, empty for Claude Code,
  so registries from before this read unchanged). A harness the machine no
  longer has leaves its sessions listed and readable; sending to one says
  what is missing.
- `GET /v1/harnesses` lists what the machine runs; `POST /v1/sessions` takes
  `"harness"`; each session's info carries `harness` and `caps`. The apps
  offer the choice in "+ session" when there is more than Claude Code, name
  the harness on its sessions, and hide auto-approve where `caps.approve` is
  false.

### The event shape a codec must produce

Everything the agent and the apps read; anything else is kept and ignored.

| message | what reads it |
|---|---|
| `{"type":"system","subtype":"init","session_id":ID,"model":M}` | `session_id` is saved as the id to resume by (`StartOptions.Resume` next time); `model` is shown. Send it at the start of every process. |
| `{"type":"stream_event","event":{"type":"content_block_delta","delta":{"type":"text_delta","text":T}}}` | reply text as it is written; shown live, never kept. |
| `{"type":"assistant","message":{"role":"assistant","model":M,"content":[…],"usage":{…}}}` | content blocks: `{"type":"text","text"}` shown as markdown; `{"type":"thinking"}` hidden; `{"type":"tool_use","id","name","input":{…}}` shown as a tool row, summarised by the first of `command`, `file_path`, `pattern`, `path`, `url`, `query`, `description`, `prompt` in its input. `usage`: `input_tokens + cache_read_input_tokens + cache_creation_input_tokens` is the context in use. |
| `{"type":"user","message":{"role":"user","content":[{"type":"tool_result","tool_use_id","content","is_error"}]}}` | a tool's output, folded; `content` a string or text blocks. |
| `{"type":"control_request","request_id":ID,"request":{"subtype":"can_use_tool","tool_name":N,"input":{…},"description":D}}` | a prompt: the session waits, the apps offer allow/deny; the answer comes back through `Codec.Respond(ID, …)`. With `tool_name` `AskUserQuestion` and `input.questions` (`[{question, header, multiSelect, options:[{label, description}]}]`) it is a question, answered with `updatedInput.answers` (question → answer). |
| `{"type":"result","subtype":S,"total_cost_usd":C,"usage":{…},"modelUsage":{M:{"contextWindow":W}}}` | the end of a turn: the session goes idle. `subtype` `success`, or anything else for a turn that failed or was stopped; the largest `contextWindow` is the window. `usage` is **this turn's** tokens (`input_tokens`, `cache_read_input_tokens`, `cache_creation_input_tokens`, `output_tokens`); `total_cost_usd` a **running total** for the conversation or the process, as Claude Code reports it. Usage (`GET /v1/usage`) reads both: a turn's cost is what it took the total above its highest so far. A harness without `usage` here has its assistant messages' `usage` summed instead. |

A harness's own errors are best shown as an assistant text block ("error:
…"), so they read as the model's turn ending badly rather than vanishing.

### Adding one, by the example of pi

pi (`@mariozechner/pi-coding-agent`, pi.dev) is driven in its RPC mode,
`pi --mode rpc`: JSON commands on stdin, events on stdout (pi's
`docs/rpc.md`). It runs on any model pi is set up with — a local one
included, which is why it is first: a machine with a GPU and no Claude
account, jimmy-crib say, can serve sessions to the whole mesh.

1. **Watch it first.** Run it by hand and keep what it writes:
   `echo '{"type":"prompt","message":"run echo hi"}' | pi --mode rpc`. pi's
   real output (pi 0.72.1 on a local qwen3.5, 2026-10-03) is what the
   adapter and its fake were written against.
2. **The harness** — `internal/agent/pi.go`, `type Pi`:
   - `Args`: `--mode rpc`, `--session <file>` to resume, plus `Extra`
     (`--pi-args`, e.g. `--provider ollama --model qwen3`). The file, found
     from the id (`TranscriptPath`), not the id: given an id, pi looks only
     among the sessions of the directory it runs in ("No session found
     matching …", pi 1.0.0) — and a conversation started with
     `--session-dir`, as Jimmy's on pi5 was, is kept elsewhere.
   - `Caps{}`: pi runs its tools without asking, so there is no auto-approve.
     A pi conversation from elsewhere can be continued through the API
     (`POST /v1/sessions` with `harness: "pi"` and `resume`), in the
     directory its file's header names; the apps do not list pi's yet.
   - `Transcripts`: pi keeps sessions as JSONL under
     `~/.pi/agent/sessions/<dir>/<time>_<id>.jsonl`
     (`PI_CODING_AGENT_DIR`, `PI_CODING_AGENT_SESSION_DIR` respected); its
     message entries are read as history. A pi session is a tree, and lines
     of an abandoned branch are read too.
3. **The codec** — `piCodec`, the whole translation:
   - start: `get_state`; its answer becomes the `init` message (session id,
     `provider/model`, and the context window, kept for the turn's `result`);
   - `prompt` for a turn, always with `streamingBehavior: "followUp"`: pi
     refuses a bare one while it works — also between a failed attempt's
     `agent_end` and its retry after a 429, when no turn seems to run — and
     starts a follow-up at once when idle (pi 1.0.0, checked on pi5); `abort`
     to interrupt;
   - `message_update` text deltas → `stream_event`; `message_end` of an
     assistant message → `assistant` (`toolCall` → `tool_use`, pi's
     `usage.input/cacheRead/cacheWrite` → Claude's names, `stopReason`
     `error` → an "error: …" text); of a `toolResult` → `user` with a
     `tool_result`; `agent_end` → `result` with the turn's summed cost;
   - an extension's dialog (`extension_ui_request` `select`, `confirm`,
     `input`, `editor`) → an `AskUserQuestion` prompt, and its answer → the
     matching `extension_ui_response` (or `cancelled`). So the question card
     in both apps answers pi's extensions too. Fire-and-forget methods
     (`notify`, `setStatus`, …) are dropped.
   - a turn pi starts itself: `message_end` of a `user` message the codec
     did not send (an extension's `sendUserMessage` — a chat bridge passing
     on what someone wrote), or of a `custom` one with `display` not false
     (an extension's `sendMessage` — a heartbeat's directives) →
     `{"type":"outside_turn","by":…,"text":…}`, which the session records as
     a `message` event from `by` (`"pi"`, or the `customType`) with
     `"outside": true`. The apps label it by that source, not "YOU". The
     codec's own prompts are told apart by their text, as sent.
4. **Register it** in `cmd/shrooms-agent/main.go`: found on PATH (`--pi`,
   default `pi`; `""` leaves it out), `m.Register(agent.Pi{…}, bin)`.
5. **A fake and tests** — `fakepi_test.go` is pi as observed, run by
   re-executing the test binary; `pi_test.go` drives the production session,
   codec and HTTP code against it: a turn with a tool round-trip, a dialog
   answered and declined (and a message sent meanwhile queued, not refused),
   an error, resuming by id, history and search from pi's own file, and the
   harness choice over HTTP. Each was checked by breaking the code it covers.
   Then once against the real pi and a local model.
6. **The apps need nothing** unless the harness can do something new.

Things pi does differently, known:

- Resuming a session from a different directory makes pi ask, on the
  terminal, whether to fork it into this one — and in RPC mode the next line
  it reads is taken as the answer. The agent always starts a session in its
  own directory, so this does not arise; a session whose directory moved
  would fail to start, and say so in its `stopped` event.
- pi's model, provider and keys are pi's own settings, as on the command
  line; the agent adds only `--pi-args`.
- Cost is pi's own figure (0 for a local model).

### Next: OpenCode and Codex (not built)

Starting points, not yet checked against the programs:

- **Codex** has a JSON-RPC app server over stdio (`codex app-server`) with
  approvals, which fits a Codec as pi's RPC mode does; `codex exec --json`
  is the simpler one-shot form, one process per turn, resuming by id.
- **OpenCode** is built around a local HTTP server (`opencode serve`, with
  server-sent events). The agent's processes are stdio only today; a harness
  that is a server needs a second kind of process — start the server, then
  speak HTTP to it — behind the same Codec idea. That is the one change to
  the plug-in point these two are likely to ask for.
