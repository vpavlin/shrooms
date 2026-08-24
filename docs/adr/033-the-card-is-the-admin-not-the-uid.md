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

**So `/invite/reply` moves to the group tier only under three conditions:** the
credential must verify against this mesh's authority, it must name the device
**this exchange** is admitting, and that authority must be card-only. Its signed
timestamp gives a fourth check for nothing, though the second condition already
does the work — see below. The daemon cannot *make* a credential; it can *check* one. With
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

### Which step is being gated, and what that simplifies

Worth being exact, because the answer removes most of the difficulty. Vaclav:
*"are we talking about invite or about actually issuing the credential after the
invitee called join?"*

The credential is signed at the **end**. The exchange runs: mint a token, hold
it open, the joining device redeems and sends its per-mesh keys, `/invite/hold`
returns those keys, **the card signs a credential for them**, `/invite/reply`
publishes it along with the network key.

So an invite credential **cannot pre-exist**. It names keys nobody knew until
the joiner called in. That makes it inherently fresh, and makes the timestamp
check below a second line rather than the first.

**The first line is that the credential must name the device from *this*
exchange** — the keys this daemon itself received and handed out. That defeats
the attack that matters: a group-tier caller can mint their own token whenever
they like (`MintInvite` needs no daemon), run their own device through the
exchange, and call `/invite/reply` with any credential they can lay hands on.
Every one of those credentials names some other device. Requiring a match means
they cannot obtain the network key without the card signing for the device
actually joining.

**This needs one small change.** `HoldInvite` deletes the held entry as it
returns, so the daemon forgets the request the moment it hands it over and
cannot match anything against it later. It would have to keep the request for
the token's remaining life — which is bounded already, since `DefaultTTL` is
fifteen minutes.

**And it puts the fifteen minutes in its place.** The TTL protects a legitimate
invite from being scanned an hour later. It does not bound an attacker, who
mints their own token. What bounds them is whether the daemon will hand over the
network key, which is the whole subject here.

### The timestamp is signed, so freshness is checkable too

An earlier version of this section said a signature made last month verifies
exactly like one made a second ago, and therefore that none of this establishes
presence. Vaclav: *"but don't we have a timestamp in the message represented by
the digest?"* We do, and it changes the conclusion.

`signedBytes` appends `Serial`, `NotBefore` and `NotAfter` before the digest is
taken, so all three are covered by the signature. `IssueFor` sets `NotBefore` to
the moment of issue less a minute of clock slack, and a `Serial` of zero becomes
unix seconds. A credential signed a month ago says so, and **editing it to say
otherwise breaks the signature** — which is the whole point of it being inside
the digest.

So the gate gains a third condition worth having: the credential's `NotBefore`
must be recent. That closes the replay case directly. A group-tier caller
without the card can only present credentials the admin signed at some earlier
point, and every one of those carries an old timestamp it cannot rewrite.

What remains, stated precisely rather than waved at:

- **The signer stamps the time.** The card holder could backdate or postdate
  deliberately. They are the admin; that is their prerogative and not a hole.
- **Clocks differ**, which is why `IssueFor` already allows a minute either way,
  and why the freshness window should be minutes rather than seconds.
- **A fresh credential names one device.** Intercepting one and using it admits
  exactly the device the admin has just approved, which gains nothing.

So the honest claim is stronger than the earlier draft allowed: with a card-only
authority and a recent signed timestamp, the exchange was authorised by somebody
holding the card, for this device, now. "Approved on card, a moment ago" is
defensible. "The card is present" still is not, and the difference stops
mattering once the window is short.

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
