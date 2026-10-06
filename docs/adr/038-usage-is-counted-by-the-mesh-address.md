# 038. Usage is counted by the mesh address

**Status:** accepted, built 2026-10-05 — details in [docs/agents.md](../agents.md)
(`GET /v1/usage`)

## Context

With models running on machines of one's own, the question becomes who uses
whose machine, and how much: to share a GPU fairly, or to bill for it. The
agents already log every turn — the message that asked, and a result with the
turn's tokens and, from the harness, a cost — and every request reaches the
agent from a mesh address derived from the asking device's key (ADR-005,
ADR-036).

## Decision

- **Who asked is the device whose mesh address the request came from**, named
  as the mesh names it. WireGuard makes that address unforgeable, so no account,
  token or self-reported name is needed. `""` is the machine's own socket
  (Basecamp on it), counted by the apps under that machine's name.
- **Usage is read from the session logs**, history included, incrementally per
  session, by day, session, device and model: turns, tokens (fresh input, cache
  read and write, output), cost and busy time. Nothing new is recorded.
- **Cost is a high-water mark over the harness's running total.** Claude Code's
  `total_cost_usd` runs across restarts and dips on a resume; counting each rise
  counted the dips twice ($6,600 against $131 real). A turn's cost is what it
  took the total above its highest so far, and the log's first result only sets
  the mark, so a conversation taken over from a terminal does not charge its
  whole past to whoever asked first.
- **Busy time** is from the message to its result, capped at two hours (a
  session left waiting on an unanswered prompt is not a model at work).

## Consequences

- The figures are as good as the mesh's membership: a device is who its key
  says, and a person with several devices shows as several.
- Cost is the harness's own pricing; a local model costs 0, and its use is
  measured in tokens and time.
- A counter that truly starts again (a new pi process) counts nothing until it
  passes its old peak — zero for a local model anyway.
- Each agent answers for itself; the apps ask every machine and sum.

## What would change our mind

A mesh shared with people who are not the owner and a need to bill them for
real, which would want signed records the asker cannot dispute rather than the
machine's own logs.
