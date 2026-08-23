# 032. A suffix that cannot be taken away

**Status:** accepted — supersedes the suffix choice in [ADR-013](013-name-resolution.md)

## Context

Mesh names have been served under **`.mesh`** since name resolution was built.
That string was chosen because it reads well and nobody appeared to be using it.
It is not, and never was, a standard:

- Not in [RFC 6761](https://www.rfc-editor.org/rfc/rfc6761)'s special-use names
  registry, which reserves `.test`, `.example`, `.invalid` and `.localhost`.
- Not `.local` ([RFC 6762](https://www.rfc-editor.org/rfc/rfc6762)), `.onion`
  (RFC 7686), `.alt` (RFC 9476) or `.home.arpa` (RFC 8375).
- Not delegated, and not reserved against being delegated.

It was a plausible-looking string nobody owned, which is a different thing from
a string nobody can take.

**What changed.** ICANN's second new gTLD application round — the first since
2012 — opened on 30 April 2026 and closed on **12 August 2026** with more than
1,600 applications. The list is not public until Reveal Day, expected around
**October 2026**. So as of this decision, whether somebody applied for `.mesh`
is a settled fact that nobody outside ICANN can look up.

If it were delegated, `.mesh` names would also exist in the public DNS. The
failure is not hypothetical: a device that falls back to its DHCP resolver, a
browser that treats a bare name as a search, a container with its own
`resolv.conf` — each becomes a query for a real name, leaking device names to
whoever runs the TLD and possibly getting an answer where there used to be none.

**What also changed, and helps.** In July 2024 ICANN's Board resolved that
**`.internal` will never be delegated in the root zone**, implementing SSAC's
SAC113 recommendation from 2020. There is now a string reserved for exactly this
purpose. There was not when `.mesh` was chosen.

## Decision

**Serve `mesh.internal` by default, and keep answering `.mesh` alongside it.**

A label *under* `.internal`, not `.internal` itself. That space is shared
private space: a network already using `.internal` for its own names must not
find this resolver authoritative for all of it. Claiming one label takes what we
need and nothing else — and answering for a whole reserved space we do not own
is precisely the "a VPN that quietly becomes the system resolver" failure
[ADR-013](013-name-resolution.md) refuses.

Names change shape as little as possible:

    vps.mesh          ->  vps.mesh.internal
    vps.home.mesh     ->  vps.home.mesh.internal

The resolver takes a list of suffixes, longest matched first, so both work.
`hosts_suffix` was already configurable, so this is a change of default rather
than a rewrite.

## Why not the alternatives

**`.shrooms`.** Considered and rejected. It fixes nothing `.mesh` gets wrong:
equally unreserved, equally delegatable, and the 2026 round is equally closed to
finding out. It also couples the namespace to the project's name, so a rename or
a fork inherits a suffix that no longer means anything.

**`.home.arpa`** (RFC 8375) is safe and correct, and is scoped by HNCP to home
networks. `vps.home.home.arpa` is unfortunate, and this is not only a home
network.

**A domain we own**, as Tailscale does with `.ts.net`. Zero collision risk
because it is real. It also puts the operator's identity into every hostname,
makes the namespace depend on a renewal somebody has to remember, and requires
owning a domain to run a mesh. Wrong shape for a project whose point is not
depending on infrastructure somebody else runs.

**Keeping `.mesh` and hoping.** Defensible until August 2026 and much less so
now, because the cost of moving only grows: every day adds another `.mesh` name
in an ssh config, a bookmark, and somebody's fingers.

## Consequences

- Existing names keep working, indefinitely for now. `LegacySuffix` is answered
  alongside the default, and dropping it is a separate decision with its own
  deprecation.
- `hosts_suffix` in an existing config is untouched, so nothing changes on a
  running node until somebody changes it. The default applies to new
  installations and to configs that never set it.
- The search domain handed to Android's `VpnService.Builder` follows the same
  value, so `ping laptop` keeps working there.
- If Reveal Day shows `.mesh` was applied for, nothing further is needed. If it
  shows nobody wanted it, this was still right: the guarantee is what matters,
  not the outcome of one round.

## What would change our mind

Nothing about `.internal` — it cannot be un-reserved. The open question is
whether `mesh.internal` is the right label under it, and the honest answer is
that it is a compromise for continuity: it keeps the word people already type.
`shrooms.internal` would be more accurate and less familiar. Worth revisiting
only before there is a body of names to migrate, which is now.
