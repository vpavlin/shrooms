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

## Publishing

To give people (or any agent) a file as a link, a session publishes it
([ADR-079](adr/079-sessions-publish-files.md)): the MCP tool `publish_file`,
with `path`, `area` and optionally `folder` and `name`.

- **Areas:** `projects` (work on something), `reviews`, `releases` (folder
  `APP/VERSION`), `notes`, `tmp` (removed after two weeks). Never the top
  level; the folder defaults to the session's name.
- **Where it goes:** the machine's file server when the agent has one
  (`--files-url`, any server that takes an HTTP PUT, such as dufs), checked
  to have arrived whole; else the agent keeps it and serves it on its mesh
  addresses at `/files/`.
- **Who reads it:** everyone on the mesh, with no password. Never publish
  secrets.
- A sealed session cannot publish; its outbox is the way out. At most 100 MB.

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

## From a sealed cage

A sealed session can't send anything (ADR-045). Its results go to its outbox,
`~/shrooms-outbox/<session>` on its machine. **This machine's agent carries
them out**, never the cage:

- **When a task finishes:** if an agent's session asked for it, the agent
  packs what the task wrote to the outbox while it ran into
  `<session>-<task>.tar.gz`, and sends it to the asker as
  `MACHINE/<sealed session>`. The asker's allow list decides, as for any
  sender. The asker's note about the task says where the results went, or why
  they didn't.
- **By hand:** `shrooms-agent outbox send reviewer jimmy-crib/vpavlin` on the
  sealed session's machine, or the outbox's **send to…** in the apps' files
  dialog, sends the whole outbox; `--only FOLDER` sends one folder of it
  (say, this review and not the last one), opened without following links.

A caged session, sealed or not, reaches nothing local: not the machine's
other services, not a LAN file drop by name or by address. Files it makes
leave through the routes here, and a session outside the cage takes them on
from there.

The outbox is the cage's to fill, so nothing in it is trusted, including what
it *points at*. A link in it could name this machine's keys. The packer opens
every name relative to the folder it found it in and never follows a link, so
a name swapped for a link mid-walk is refused rather than read. It takes
regular files only, at most 2,000 files and 100 MB. What it left out, and
why, is listed in the package's `README-UNTRUSTED.txt`.

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

**They are kept two weeks.** A received file is removed 14 days after it
arrives, with everything else in `uploads/` (`--uploads-keep`; ADR-078).
Something worth keeping longer is the receiver's to move out of there.
