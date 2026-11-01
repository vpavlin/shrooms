# 078. Files sent to sessions are kept two weeks

**Status:** accepted 2026-11-01 by the owner ("files all older than two weeks
should be automatically cleaned up"; "do the upload cleaner, yes"); built.

## Context

Everything sent to a session stays in `uploads/<session>/`: what a phone or
Basecamp uploads (photos, voice notes) and what other agents send with
`send_file` (ADR-…: `uploads/<session>/drop/from-…`). Nothing removed any of
it. On 2026-11-01 five machines held 1.1 GB of it (jimmy-crib 433 MB, the
laptop 287 MB, pi5 191 MB, atlas 106 MB, proteus 80 MB), a third older than
two weeks, and it only grew. Backing machines up (Shrooms Backup) made the
question pressing: these files are transport, not records.

## Decision

The agent removes files in `uploads/` 14 days after they arrived (their
modification time), at start and then every hour, and the folders they leave
empty. `--uploads-keep` changes the keeping; `0` keeps them for good. The same
shape as the conversation archive's 30 days (ADR-072).

## Consequences

- A conversation that names a file older than two weeks finds it gone; a file
  worth keeping is the receiver's to move elsewhere.
- Shrooms Backup's agents preset leaves `uploads/` out.
