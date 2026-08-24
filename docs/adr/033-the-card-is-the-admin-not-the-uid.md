# 033. The card is the admin, not the uid

**Status:** proposed — refines the tiers in [ADR-025](025-control-from-a-desktop-app.md)

## Context

The control socket has two tiers: root, checked per-endpoint with `SO_PEERCRED`,
and the socket group, which is whoever `socket_group` names — in practice the
person's own login, so that `shrooms status` and a desktop app need no sudo.

Six endpoints are root: `/revoke`, `/grant`, `/members`, `/rotate`,
`/invite/hold`, `/invite/reply`. `SECURITY.md` explains the line as "what it
cannot do is admit anybody", and that was right when the admin key was a
passphrase-protected file in a user's session.

[ADR-022](022-keycard-for-the-admin-key.md) changed the premise and the tiers
were never revisited. Vaclav, 2026-08-24:

> when we use keycard, anything goes — invite, revoke… because whoever has the
> keycard is the admin, regardless of the device — right?

Right, and it reframes the question. A uid is not what makes somebody an admin;
possession of the card and its PIN is. Gating an admin operation on being root
protects nothing an attacker who holds the card does not already have, and
blocks a legitimate holder who happens to be running a desktop app.

## Decision

**Tier by what protects the operation, not by how consequential it sounds.**

Two kinds of endpoint, and only one of them needs root:

**Signature-gated — safe at the group tier.** The caller supplies something the
daemon *verifies* and could not forge. `/revoke` publishes an admin-signed
withdrawal that every peer checks for itself. `/grant` publishes an admin-signed
credential, likewise. `/rotate` carries a generation secret that is worthless
without the admin-signed statement committing to it, which the mesh verifies on
arrival ([revocation-and-the-network-key.md](../revocation-and-the-network-key.md)).
In each case a caller who cannot sign can publish nothing anybody will accept —
so requiring root buys nothing.

**Secret-bearing — stays root.** The daemon hands out something *it* holds and
nobody downstream can check. There is exactly one: `/invite/reply` publishes the
mesh's **network key**, because the joining device needs it. Nothing about that
message is signed by an admin, so no amount of verification downstream helps. A
group-tier caller who could reach it could mint a token, hold, reply, and hand
the network key to a device of their choosing — full membership on a
`--no-admin` mesh, and the whole control plane on any other.

**So `/invite/reply` moves to the group tier only under two conditions:** the
credential it carries must verify against this mesh's authority and name the
joining device, and that authority must be card-only (see below). The daemon cannot *make* a credential; it can *check* one. With
that check, releasing the network key stops being the caller's decision and
becomes the card holder's, which is what everybody meant by "admin" all along.

On a mesh with no authority there is nothing to verify against, so it stays
root. That is the mesh where the network key alone admits, which is exactly
where the caution belongs.

`/invite/hold` moves with no condition: it subscribes to a topic named by a
token, waits, and reports the joining device's public keys. It grants nothing.

`/members` stays root, for a different reason: it is not an admin operation at
all, it is a roster with names and expiry dates, and it is gated as metadata
rather than as authority.

### Gating on the card, which turns out to be checkable

Vaclav's follow-up: *"so we need to somehow gate it on the keycard being used —
if possible?"* It is, and the mechanism is already in the tree.

The two admin key types are distinguishable by length: 32 bytes is an ed25519
key, which is a **file** in somebody's session; 33 is a compressed secp256k1
point, which is a **Keycard's**, and whose private half has never existed
outside the card. `Authority.CardOnly()` reports whether every key in a mesh's
authority is the second kind.

That distinction is exactly the one that matters here, and it is sharper than it
first looks:

- With a **card** key, "the admin signed this" and "the caller could have signed
  this" are different questions. A group-tier caller cannot produce a signature
  without the card and its PIN, whoever they are running as.
- With a **file** key, they may be the same question. The desktop admin key is a
  passphrase-protected file in a user's session, and a group-tier caller running
  as that user can read it. Widening the tier there protects much less, because
  the thing being verified is something the caller may be able to produce.

So the widening is gated twice: the credential must verify, **and** the mesh's
authority must be card-only. `Every` key rather than `any`, because a mesh
mixing the two can be signed for with the file, and the weaker key sets the
guarantee.

**One thing this does not establish, and should not be described as if it did.**
A signature made last month verifies exactly like one made a second ago. This
proves the admin approved *this device*, not that anybody is holding a card
while the check runs. That is the right property for a credential and the wrong
word for presence — and the difference is worth keeping straight in any UI that
says "approved on card".

## Consequences

- **Basecamp can invite, revoke and renew with a card**, without sudo and
  without the daemon ever holding a key. That is the point.
- **`SECURITY.md` changes**, and the sentence is worth getting right. "The group
  cannot admit anybody" becomes "the group cannot admit anybody the admin has
  not signed for" — which is a smaller claim honestly stated, rather than a
  larger one that stopped being true when the card arrived.
- **A group-tier member gains the ability to publish things they cannot forge.**
  They could replay an old revocation, or spend the mesh's bandwidth. Both are
  already available to anyone who can read the socket, and neither admits
  anybody.
- **The check on `/invite/reply` is new code on a security boundary**, which is
  the part to write carefully and test adversarially: a credential for a
  different device, for a different mesh, expired, or signed by a key that is
  not this mesh's authority must all be refused before anything is published.

## What would change our mind

If a `--no-admin` mesh ever became the common case rather than the bootstrap
case, this splits badly: every operation there is unsigned, so everything stays
root and the desktop app is back to printing commands. That is an argument for
credentials being the default, not against this.
