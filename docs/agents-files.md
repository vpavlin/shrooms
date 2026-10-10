# Files between agents

An agent's session can send a file to another session, on any machine on the
mesh. The receiver keeps it in a folder of its own for that sender, and only
takes files from senders it allows. Decided in
[ADR-048](adr/048-files-between-agents.md).

## Sending

- **From an agent:** the MCP tool `send_file`, with `to` (MACHINE/SESSION),
  `path` (a file on the sender's machine) and `note` (what it is and what to
  do with it).
- **From a shell:** `shrooms-agent a2a send-file [--note N] MACHINE/SESSION FILE`.

The file goes from the sender's machine to the receiver's agent, at most
100 MB. Nothing is published or offered for others to fetch: this is push only.

## Receiving

A file from `jimmy-crib/vpavlin` to the session `reviewer` is kept at

    <agent state>/uploads/reviewer/drop/from-jimmy-crib_vpavlin/<name>

under its own name, numbered if one of that name is already there. One sender
cannot overwrite or read another's files. A sender's folder holds at most
1 GB.

The session is told, as a turn: `[shrooms file from jimmy-crib/vpavlin:
frequencies.lgx, 2.1 MB, kept at …]`, followed by the sender's note.

**Cages see their files.** The folder is under the session's uploads, which a
cage mounts read-only at the same path, sealed cages included. So a sealed
reviewer is handed code without being able to reach for it.

## Who may send

Each session has an allow list, `accept_files_from`. It starts empty: nobody
can send a session files until you allow them. An entry is `MACHINE/SESSION`,
or `MACHINE/*` for every session on that machine.

- **When a sender is refused,** the refusal tells it how to be allowed, and
  the session keeps the request. The apps show it as "wants to send files:
  allow / ignore".
- **In the apps,** the session's settings have a "files from" line to add or
  remove senders.
- **From a shell, on the receiver's machine:**

      shrooms-agent files list  reviewer
      shrooms-agent files allow reviewer jimmy-crib/vpavlin
      shrooms-agent files deny  reviewer jimmy-crib/vpavlin

- **Over HTTP:** `POST /v1/sessions/{name}/settings` with
  `{"accept_files_from": [...]}`, which replaces the whole list.

**Cages.** A file from a caged session comes through its own agent's proxy,
under the name that agent gives it. The receiver must allow that sender *and*
take from caged agents (`accept_caged`, ADR-044). A cage cannot change any
session's allow list. A **sealed** cage cannot send files at all: its results
go to its outbox (ADR-045).

**Who a sender is.** The machine is the one the mesh says the request came
from. The session is the one its agent names: the mesh is the boundary, as
for A2A (ADR-041).
