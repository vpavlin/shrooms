# 081. A machine's Claude Code is logged in from the apps

**Status:** accepted 2026-11-08 by the owner ("Could we integrate claude login
into shrooms agents?", then "That would be great"); built. Amends
`docs/agents-harnesses.md` §1, which recommended only saying so.

## Context

A session on a machine whose Claude Code is not logged in answers every
message with "Not logged in · Please run /login", and `/login` typed into it
answers that it is not available: the agent runs `claude -p`, which has no
slash commands. Logging the machine in meant `ssh`, `su` to the agent's user,
`claude`, `/login`; or `claude setup-token` and a token pasted into a unit's
environment. In October that was judged a once-a-year chore, and driving the
login from the agent was turned down because its prompts were not a stable
interface.

Two things changed. Claude Code now has `claude auth status --json`, a
machine-readable answer (`{"loggedIn": …, "authMethod": …, "email": …}`),
and `claude auth login`, which runs without a terminal: it prints the URL to
sign in at, then waits on its input for the code the sign-in page shows
(checked with Claude Code 2.1.296, in an empty home). And machines are added
more often than once a year: k11 in November, with the owner setting it up
from the docs himself.

The login is a machine's, not a session's: it lives in the agent user's
`~/.claude`, every session there runs on it, and caged and sealed sessions
reach the model through the agent's gateway on the same login (ADR-059).

## Decision

- **The agent reports the login.** `GET /v1/claude/login`, and
  `claude_login` in `GET /v1/harnesses`, is `claude auth status --json` as
  `{logged_in, method, email, error}`, read at most every 30 s. `error` is
  set when the status could not be read (no Claude Code there): not a login
  to offer.
- **The apps log it in from "+ session".** On a machine whose Claude Code is
  not logged in, "+ session" with Claude Code chosen says so and offers to log
  it in. `POST /v1/claude/login` makes the agent start `claude auth login
  --claudeai` (with `BROWSER=true`: there is no browser there) and returns the
  URL as soon as it is printed. The app opens it. The person signs in and
  pastes the code the page shows; `POST /v1/claude/login/{id}/code` writes it
  to the waiting login and returns at once, "checking"; the app asks
  `GET /v1/claude/login` until the flow is "done" or "failed".
  `DELETE /v1/claude/login` gives up.
- **Success is read from `claude auth status`, not from what the login
  prints.** A reworded prompt cannot make a failed login look done. The only
  thing taken from its output is the URL, found by pattern, and the last line
  of a failure, shown as its reason.
- **One login at a time.** Starting another replaces the one waiting: whoever
  asks again has the newer URL. A login waits ten minutes for its code.
- **Only the one-time code crosses the mesh**, from the person's app to that
  machine's agent. The login, its refresh token included, stays in that
  machine's `~/.claude`, renewed by Claude Code itself.
- **A cage cannot log the machine in or out:** none of `/v1/claude/login` is
  in what a cage's proxy forwards (`proxyAllowed`).
- **`claude setup-token` is not offered.** It needs a terminal, and printed
  nothing without one. Sealed cages run on the machine's login when there is
  no sealed token (ADR-059), so logging the machine in covers them too.

## Consequences

- A new machine's agent is usable from the phone or Basecamp without a shell
  on it.
- The agent now runs `claude auth status` every half minute at most while an
  app shows "+ session", and that renews a login near its end, as the
  gateway's own call to it does (gateway.go).
- An agent older than this does not report the login; the apps then offer
  nothing, as before.
- If Claude Code stops printing a URL without a terminal, the login fails
  with "printed no URL to sign in at within 15 s" rather than hanging; the
  15 s keeps it under Basecamp's 20 s for a call to its core.
