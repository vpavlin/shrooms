# 008. A bearer network key for v1

**Status:** accepted, explicitly temporary

## Context

A single 32-byte network key currently does three jobs: derives the rendezvous
topic, encrypts payloads, and grants membership. Anyone holding it is a member,
permanently.

## Decision

Ship v1 this way. Replace the third job — authorization — with admin-signed
per-device credentials in M5.

## Why ship it

Configuration is one secret, which makes the whole system explainable in two
commands. At 3–5 machines you control, "rotate the key and re-enrol" is a chore
rather than a project.

## Why it must not stay

Five concrete failures:

1. No per-device revocation — losing a laptop means rotating everything.
2. Any device's compromise is total; the weakest machine sets the security of
   the mesh.
3. No expiry — a key that leaked two years ago still works.
4. The copy/pasted artifact stays valid forever, in shell history and clipboard
   managers.
5. No per-device capabilities.

## The replacement

Only *authorization* changes. Rendezvous genuinely needs a shared secret — every
member must independently compute the same topic with no coordination — but that
secret need not also grant membership.

- `K_rdv` keeps deriving the topic, payload key and per-pair PSKs.
- Membership becomes ~100 bytes of admin-signed CBOR over `{device_pk, wg_pk,
  name, overlay_ip, not_before, not_after, caps}`, verified against `admin_pk`
  — a *public* value in config.
- `shrooms invite` emits a **one-time, 15-minute** token. This is the
  highest-value single change: a leaked clipboard stops being worth anything.
  Built — see [ADR-017](017-invite-tokens.md).
- Revocation is gossiped and tears down live tunnels. Nebula's documented gap is
  that its blocklist is not distributed at all; ours is.

## Migration cost, paid now

`admin_pk` already exists in config and `credential` already exists in the
announce, both empty and ignored. M5 is a behaviour change rather than a
wire-format and config break across every machine simultaneously.
