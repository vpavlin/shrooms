# Testing the Keycard, for the first time

Vaclav, 2026-08-24: *"we have not tested the Keycard integration at all — so,
what is the plan?"*

**Updated 2026-08-24, after the first contact with a card.** What follows is
ordered so the cheap failures happen first, and now records what each wall
actually looked like — every one of them presented as something other than what
it was.

The headline: **the card was never the problem.** Four separate failures, in
order, and none of them meant a bad card:

| what it said | what it was |
|---|---|
| `6a84` on pairing | all five pairing slots taken |
| `6d00` on pairing | the applet had no secure-channel capability |
| `6985` reading the key | no PIN verified — EXPORT KEY needs a session |
| "signed, but does not verify" | **keycard-go corrupts `s`** — see below |

The last one is the interesting one and had been sitting in ADR-022 for months
as a defect blamed on `keycard-cli` and left for the Keycard developers. It is a
slice-aliasing bug in `keycard-go`'s own signature parser, reproducible with no
hardware, and worked around in `cred.RepairCardSignature`.

## Before touching anything

**A card has a small, fixed number of pairing slots** — five on a Status
Keycard. `CardEnrol` consumes one and the pairing is written to
`keycard-pairing` in the config directory. Pair the same card from several
devices, or reinstall the app without unpairing, and they run out. There is no
unpair path in this codebase yet, which is worth knowing before pairing three
times to see if it works.

**The PIN blocks after three wrong attempts** and needs the PUK; the PUK bricks
the card after ten. This is not a place to guess.

**Use a throwaway mesh.** A mesh's authority is fixed when it is minted and
cannot be changed afterwards, so anything done here is permanent for that mesh.
Multi-mesh support ([ADR-015](adr/015-multiple-meshes-one-daemon.md)) exists
precisely so this need not touch the mesh you rely on.

## The dev-card defaults, since the example app offers them

For a card initialised without anybody choosing its secrets — which is what a
development card is — the factory values are:

| | |
|---|---|
| pairing password | `KeycardDefaultPairing` |
| PIN | `123456` |
| PUK | `123456789012` |

`keycard-cli` defines the first as a constant in `internal/secrets.go` and its
examples use all three together. A card somebody has set up properly will have
its own, and these are worth trying only on a card that has never been
initialised for real.

**The pairing password is a version 1 concept.** A card running applet 4.0 or
later authenticates the secure channel with an X.509 certificate against the
Status CA and needs no password at all. `CardEnrol` used to call the V1 pairing
directly, which would have failed on such a card whatever was typed — reading as
"wrong password" while consuming attempts. It now uses the version-agnostic
calls, as keycard-go's own README recommends.

## Stage 0 — ask the card what it is. Costs nothing.

**"Check this card"** runs `SELECT` and nothing else: no pairing slot, no PIN
attempt, no password. It reports whether the applet is initialised, whether it
holds a key, how many pairing slots are free, and what it is capable of.

Do this first on any unfamiliar card. It answers three of the four failures in
the table above in one tap, and it is the step whose absence cost the most time.

**A card needs two one-time acts before shrooms is any use, and shrooms performs
neither** — deliberately, because they decide what a card *is* and are
irreversible:

1. **INIT** — sets the PIN, PUK and pairing password. Before this there is no
   password that works, because there is no password.
2. **Generate or load a key.** A card can be initialised and *empty*. INIT does
   not create a key, and without one there is nothing to sign with.

Both with `keycard-cli` or the Keycard app.

## Stage 1 — the card signs, and we can check it

**Nothing to build. Do this first after stage 0.** The app's Keycard screen
pairs and reads the authority key.

`CardSelfTest` is the one that matters. It opens a session with the PIN, signs a
fixed digest on the card, and verifies the result **with the same function a
peer uses on a credential**. So it exercises the whole path — secure channel,
PIN, on-card signing, the uncompressed-to-compressed point conversion, and the
minimal-length `r`/`s` to fixed-64 conversion — and nothing is published, no
credential is issued, and no mesh is touched.

What each failure would mean:

| symptom | what it points at |
|---|---|
| card not detected | NFC, the applet, or the transport — not this codebase |
| pairing fails | wrong pairing password, or slots exhausted |
| PIN rejected | the PIN, and **count the attempts** |
| signs but does not verify | keycard-go's mangled `s`, repaired automatically; if it survives the repair, then the conversions in `internal/cred/card.go` |
| signature is not 64 bytes | `CompactSig` padding, or keycard-go returning a different shape |

A fixed digest rather than a random one, deliberately, so a failure that only
happens for some inputs can be repeated.

Reading the key no longer exports it. `EXPORT KEY` needs conditions a freshly
initialised card may not grant, and a signature response already carries the
public key — so enrolment signs a fixed digest and takes the key from the
answer, which is how `loam-keycard` (extracted from scala) does it. One exchange
proves pairing, PIN, on-card signing and both conversions.

**Stage 1 passed on 2026-08-24.** A digest signed on the card verified against
the card's own key, which settles ADR-022's central claim: the admin key can
live on a smartcard and this project can check what it signs. Everything below
is plumbing on top of a path that is now known to work.

## Stage 2 — a mesh whose admin is the card. **Blocked; needs a small change.**

There is currently **no way to create one.** `admin init` always mints a fresh
ed25519 authority — `mintAuthorityAt` calls `cred.NewAdmin()` twice, for a
primary and a recovery key — and no flag accepts an existing public key. The
comment in `internal/cred/secp256k1.go` that credits `admin init --keycard` with
compressing a point describes a flag that has never existed.

So the phone can read its card's authority key (`CardPublicKey` returns the
compressed 33 bytes as hex), and nothing will take it.

What is needed is small: a way to mint a mesh whose authority is a given public
key. `shrooms admin init --admin-key <hex>` is the obvious shape, and
`cred.NewAuthority` already accepts any key `knownKeySize` allows.

**And one thing to decide while building it, which matters more than the flag.**
`mintAuthorityAt` mints a **recovery key** alongside the primary, and a
card-minted mesh has no equivalent unless somebody arranges one. A mesh whose
only authority is a single card dies with that card: no more credentials, no
renewals, no revocations, for ever. Either the card path mints an ed25519
recovery key to be stored offline, or the documentation has to say plainly that
losing the card ends the mesh. The first is better and neither is the default
today.

## Stage 3 — admit a device with the card

Once stage 2 exists: on the throwaway mesh, run the app's invite screen, scan
from a second device, approve with the card. That exercises `AdmitWithCard`,
which is the real path — `cred.IssueFor` through the `Signer` seam, verified
before it leaves, then published.

Watch for the thing stage 1 cannot catch: a credential that verifies on the
issuing phone and is refused by the joining device. That would mean the mesh id
or the key encoding differs between what was signed and what is checked.

## Stage 4 — revoke with the card

`shrooms admin revoke --external-signer` prints a digest and reads a signature
back, so it can be driven by a card without the desktop knowing what a card is.
Worth doing because revocation is the operation whose failure is silent: a
revocation that does not verify is simply ignored by every peer.

## Stage 5 — the group-tier gate. **Not testable yet.**

[ADR-033](adr/033-the-card-is-the-admin-not-the-uid.md) lets Basecamp complete
an invite the card signed. Its tests cover the gate with a software key of the
right shape, which is honest as far as it goes: they prove the *decision*, not
the card.

Testing it end to end needs the desktop card path, which does not exist —
`keycard-basecamp` has `requestSign`, and nothing in shrooms calls it yet. So
this waits on that wiring rather than on hardware.

## What this plan does not cover

- **The card as a device identity**, only as a mesh authority. Nothing here
  signs announces with a card and nothing should.
- **Losing the card**, which is stage 2's open question and the most likely way
  somebody gets hurt.
- **Two cards for one mesh.** `NewAuthority` takes several keys, so a mesh can
  have two card admins, and nothing has ever tried it.
