# 051. A card lets the socket group renew and revoke

**Status:** accepted 2026-10-10, built the same day. A real-card run is still
owed (docs/testing-the-keycard.md, stages 4 and 5).

## Context

`/grant` (deliver a renewed credential), `/revoke` (publish a withdrawal) and
`/members` (who to renew) were root's. The comment beside each already gave
the real reason they are safe: *the socket decides who may ask; the admin
signature inside decides whether anything happens.*

ADR-033 made that argument for replying to an invite, and drew the line where
it holds. On a mesh whose admin keys are all on cards, only a card could have
signed, whoever asks. On a mesh with a key file, anyone who runs as that
file's owner can sign. So "it is signed" proves nothing about who asked.

Basecamp runs as the user, in the socket group. After ADR-050 it could admit
a device with a card but not renew one or remove one. Removing is the
operation that matters when a phone is lost.

## Decision

**On a card-only mesh, the socket group may renew and revoke**, through drafts
it has the card sign:

- `/renew/draft` lists what is due, or every member with `all`. For each it
  gives the credential that would renew it, unsigned, and its digest.
- `/revoke/draft` gives the withdrawal of one device and its digest. It is
  kept as long as `shrooms admin revoke` keeps one by default.
- `/grant` and `/revoke` accept `{"draft","signature"}`. The daemon finishes
  the draft (`cred.Finish` / `cred.FinishRevocation`: r‖s, r‖s‖v or DER, high
  s allowed) and verifies it against admin_keys before the mesh sees it.
- `/members` and the drafts answer the group only on a card-only mesh.

**Root is unchanged.** The bare base64 bodies the CLI sends still need root.
On a mesh with a key file, everything here still needs root.

**Not included:** rotating the announce generation after a revocation
(`admin revoke --rotate`). It carries a new secret, which is not a signed
statement the daemon can check the way it checks these. The view says to use
the CLI for it.

Found on the way and fixed: `/members` did not report a member's sealing key,
so every renewal (the CLI's too) issued a version 1 credential. The renewed
device could then not be sent the next announce generation. Members now carry
it.

## Consequences

- Basecamp renews a mesh with one card approval per member that is due, and
  revokes a device with one. Every approval is in keycard-ui, with the PIN
  there.
- A group member on a card-only mesh can list members' public keys and names.
  Peers announce both anyway.
- A group member can ask for drafts all day. A draft is unsigned, and every
  node refuses it.
