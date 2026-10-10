# 048. Files between agents

**Status:** accepted 2026-10-10, built the same day — [docs/agents-files.md](../agents-files.md)

## Context

Agents kept needing to hand each other files: a build to review, a repo for a
sealed reviewer that cannot reach the mesh (ADR-045), a report. There was no
way to do it except through a person, or through whatever file server two
machines happened to share.

## Decision

**Push, into per-sender folders, from allowed senders only.**

- A session sends a file to MACHINE/SESSION (the `send_file` MCP tool, or
  `a2a send-file`). The receiver's agent keeps it under
  `uploads/<session>/drop/from-<machine>_<session>/`. One sender cannot touch
  another's files.
- **The allow list starts empty.** The owner allows a sender, or a whole
  machine with `MACHINE/*`, from the apps, the CLI or the settings API. A
  refused send is kept as a request the apps offer to allow.
- **The receiver is told,** as a turn, with the path and the sender's note.
- **Limits:** 100 MB a file, 1 GB a sender.
- **Cages:** caged senders go through their own agent's proxy, which names
  them, and need the receiver's `accept_caged` too. Sealed cages can receive,
  through the read-only uploads mount they already have, and cannot send.

**Not pull.** Publishing a folder for allowed sessions to fetch was
considered. Push is simpler, nothing sits exposed for others to read, and the
receiver always knows what arrived and from whom.

## Consequences

- A sealed reviewer can now be given code to review without opening its seal.
- The sender's session is the one its agent names. That is the same trust as
  A2A: the mesh is the boundary (ADR-041).
- A file costs the receiver a turn when it arrives. A sender that will say
  what to do with the file in a task anyway can skip that (`tell=0` on the
  API).
- **Amended 2026-10-10:** a sealed session's results are carried out of its
  outbox by its machine's agent, to the task's asker or by hand, packed
  without following links. The cage still sends nothing (docs/agents-files.md,
  "From a sealed cage").
