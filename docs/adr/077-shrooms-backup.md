# 077. Shrooms Backup lives in its own repository

**Status:** accepted 2026-11-01 by the owner ("a general shrooms backup
solution… definitely do a separate daemon"; then "should we consider moving
the backup out of Shrooms Repo?" and "You can create the repo yourself").
Supersedes ADR-052.

## Context

ADR-052 planned fleet backups on three private Logos Storage nodes and was
never built; re-checked on 2026-11-01, its `--block-ttl 0` expires blocks at
once rather than never, so it would have lost every backup. The owner then
asked for a backup any Shrooms user can set up: pick what each machine keeps
and which machines keep copies.

## Decision

Shrooms Backup is its own product, in **github.com/vpavlin/shrooms-backup**:
a daemon of its own (`shrooms-backup`), restic over Logos Storage on a
private network of our own nodes over the mesh, mirrors chosen per source,
deletions held 30 days on the mirrors, a key per source from the owner's
Keycard. Its decisions, measurements and design are there (its ADR-001,
which began here as this ADR).

It uses nothing of this repository's code. What it uses of Shrooms is the
daemon's status over the control socket (`GET /status`: this machine's
name, its meshes with their overlay addresses, the peers with their names
and addresses), the same thing Foray reads. That makes the status a contract
for applications: a change to those fields is a change to them, said in an
ADR.

## Consequences

- This repository stays the mesh, its agents and its apps; backups have their
  own CI, releases and contributors, without the delivery library or the
  Android build.
- The status fields above are kept compatible.
