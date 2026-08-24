# Testing the Keycard, for the first time

Vaclav, 2026-08-24: *"we have not tested the Keycard integration at all — so,
what is the plan?"*

None of this has met hardware. The conversions are tested against vectors
(`internal/cred/card.go`), which proves the arithmetic and nothing about a card.
What follows is ordered so that each step is worth doing on its own and the
cheap failures happen first.

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

## Stage 1 — the card signs, and we can check it

**Nothing to build. Do this first.** The app already has a Keycard screen that
runs three steps against a real card: pair, read the key, self-test.

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
| signs but does not verify | the conversions in `internal/cred/card.go` — the failure this test exists to find |
| signature is not 64 bytes | `CompactSig` padding, or keycard-go returning a different shape |

A fixed digest rather than a random one, deliberately, so a failure that only
happens for some inputs can be repeated.

**If stage 1 passes, ADR-022's central claim is proven** and the rest is
plumbing. If it fails, everything below is moot.

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
