# Security review, August 2026

Prompted by a plain question: *"what if someone figures out a hole and hacks me
bad?"* The honest answer needed a review rather than reassurance, particularly
because blind relays had just put an untrusted party in the traffic path and
none of that code had been read since it was written.

**Scope.** The relay protocol and its server, the tag derivation, the framing
split, the MSS clamping path, the invite tokens, and the threat model in
SECURITY.md. Mostly code written on 2026-08-21 and 2026-08-22.

**Reviewer.** Me, and that is the review's largest weakness. I wrote almost
everything examined here. An author re-reading their own work finds the bugs
they can see, which is a strict subset of the bugs that are there — the ones
that come from an assumption held while writing survive re-reading, because the
assumption is doing the reading. Nothing below substitutes for someone else.

---

## Found and fixed

### 1. Unbounded state for any sender — remote memory exhaustion

A register frame's signature covers the frame and **not the address it arrived
from**. So one keypair and one signed register can be replayed from as many
spoofed sources as an attacker's network permits, and each arrival allocated a
pending-challenge entry held for the challenge lifetime. On an open relay the
frame key is public by construction, so nothing had to be stolen first.

**Fixed** by putting the state in the nonce, as TCP does against SYN floods: a
challenge is a keyed hash over the address, the handle, the device key and
roughly when. Answering proves receipt; recomputing proves we issued it; nothing
is remembered. Bound to all three, so a challenge collected for one handle
cannot answer for another, and one collected at one address cannot be replayed
from elsewhere — which is the reflection defence itself.

### 2. Control answers were not rate limited

Only forwarding passed through the operator's ceilings, so challenges and MTU
echoes could be emitted without limit. Neither amplifies — 33 bytes against a
153-byte register, 27 against a probe of up to 65535 — so this was never a
weapon to aim at somebody else. It is the operator's uplink, which is what they
pay for. **Fixed:** control answers get a twentieth of the total.

### 3. A device wore the same tag on every relay — privacy claim not delivered

The comments stated that two relay operators comparing notes see unrelated
values. `Tag` was derived from the mesh key and the tunnel key alone, with
nothing about the relay in it, so a device presented **the same handle
everywhere it went**. Two operators could have linked a device, and by extension
a whole mesh, across their relays.

**Fixed:** the relay's address is part of the derivation. Both ends already know
it and must already agree on it, so this costs no negotiation.

This is the finding worth dwelling on. Nothing failed, no test caught it, and it
would have read as true indefinitely — a privacy property asserted in a comment
and enforced nowhere. It is the sentence somebody reads before deciding to trust
a stranger's relay.

### 4. A panic reachable from the packet path

The MSS clamp sliced a buffer using a length reported by the layer beneath it,
without bounding it. It should never be wrong; it runs on every packet, and the
cost of being wrong there is the daemon and the tunnel with it. **Fixed:**
bounded, and a zero or negative length is skipped.

---

## Examined and found sound

- **Tag derivation.** HKDF with the mesh relay key as the secret input. One-way,
  so a relay cannot recover a tunnel key from a handle; and an outsider holds
  neither input. Distinct meshes and now distinct relays give unrelated values.
- **Invite tokens.** 128 bits from `crypto/rand`. The rendezvous topic and the
  payload key are derived separately, so the public topic reveals nothing about
  the token, and a response can only be sealed by somebody holding it.
- **Amplification.** Every relay answer is smaller than what prompts it, and
  forwarding is one packet in, one packet out. There is no ratio here worth an
  attacker's bandwidth.
- **Framing split.** `ctrl.Unwrap` bounds-checks before slicing; a frame from an
  unconfigured address is read under the mesh key, which only a member can
  produce.
- **Challenge replay.** A cookie is bound to the address it was sent to, so
  capturing one and answering from elsewhere fails. An attacker who can both
  spoof a victim's address *and* observe the reply is on-path at the victim,
  which is a position with larger powers than this.

---

## Accepted, and written down rather than fixed

**An open relay authenticates nothing at the frame layer.** Deliberate: its key
is public by construction, and every real property comes from the registrant's
signature, first-claim-wins and the routability check. One consequence reaches
the client — a device using an open relay will unwrap frames from anybody who
knows its address and hand the payload to WireGuard, which rejects it. A few
cycles per forged packet rather than an injection, but before blind relays only
a mesh member could make a node do even that.

**First claim wins is weaker than a roster.** An attacker who claims a handle
before its owner ever does keeps it while the entry lives. A stranger cannot
compute the handle at all, so this bites a hostile mesh member who is quick,
not the public.

**A relay can drop traffic** selectively and silently, and it presents as a slow
network. Keeping several configured is the mitigation, and a device registers
with at most two.

---

## Not reviewed

Named because a review's silence is otherwise read as approval:

- The **credential and revocation** paths (ADR-018), beyond noticing that the
  Keycard issuing added this month verifies what it signed before publishing.
- The **announce encryption and replay guard**, which SECURITY.md covers and
  this did not re-derive.
- **wireguard-go** and **liblogosdelivery**, which are dependencies rather than
  this project's code, and are much larger than it.
- The **Android app**, other than the settings added this month.
- Anything about **supply chain** beyond pinning the gomobile toolchain, which
  was fixed this month because it broke rather than because it was reviewed.

---

## Recommendations

1. **An outside review before v1.0.0.** The cryptography here is standard and
   the compositions are ordinary, but nobody independent has read them, and
   finding 3 is what that costs: a property everybody believed, written down
   twice, true nowhere. A version number invites people to rely on this.
2. **Keep the relay boring.** Its safety comes from holding nothing — no keys,
   no roster, no state that survives a restart. Every feature that gives it
   memory is a feature that can be exhausted, as finding 1 was.
3. **Re-read claims against code, not only code against itself.** Three of the
   four findings were visible from the comments alone once the question was
   "does it actually do this?" rather than "does this look right?".
