# A mesh whose admin key is a Keycard

The admin key is what admits devices to a mesh. Normally it is a
passphrase-protected file in your home directory; on a Keycard it is a key that
never leaves the card, and admitting a device means the card has to be present.

This is how to set one up and admit a phone to it.

## What you need

- A **Keycard**. Applet 3.1 or later; `shrooms keycard status` will say.
- A **USB smartcard reader**, or a phone with NFC. Both work; this guide uses
  the reader because everything can be done from one place.
- **pcsc-lite** on the machine, which most desktop Linux installs have:

      sudo apt install pcscd libpcsclite1     # Debian, Ubuntu
      sudo dnf install pcsc-lite              # Fedora, RHEL
      sudo pacman -S pcsclite                 # Arch

  No special build. Reader support is in every binary: the library is opened
  the first time you reach for a card rather than linked, so a machine without
  it runs the same binary and the card commands name the package to install.

  It used to be `TAGS=pcsc`, which linked libpcsclite and so needed a Go
  toolchain and the development headers to get a card-capable build — and put
  the library in DT_NEEDED, meaning a machine without it could not start the
  binary at all, daemon included.

There is **no service to start**. `pcscd` is socket-activated: it starts when
something asks for a reader and stops when nothing is using one.

## 1. Look at the card first

    shrooms keycard status

This costs nothing — no pairing slot, no PIN attempt, no password, and nothing
on the card changes. Do it first on any card you have not used here. It answers
three of the four ways setting one up can fail before anything has been spent:

    applet       3.1
    initialised  true
    key          yes (1839b7db)
    pairing      4 of 5 slots free
    can do       secure-channel, key-management, credentials, ndef, factory-reset

**`pairing 0 of 5 slots free` is the one to watch for.** A card has five slots,
they are consumed permanently, and freeing one needs a device that already holds
one — so a card whose slots are all gone can only be recovered by wiping it.
Every slot is a device that has ever paired, including failed attempts.

## 2. Initialise it, if it is blank

Skip this if `initialised true` and `key yes`.

    shrooms keycard init

It asks for a PIN (six digits), a PUK (twelve, which unblocks a locked PIN) and
a pairing password — press enter for the factory default, which is what a card
set up with the Keycard app has.

It prints a **mnemonic**. Write it down.

> That phrase is the only way back to this key. A mesh minted against a key that
> exists nowhere else dies with the card. It is an ordinary BIP-39 phrase, so it
> restores into any wallet — which also means keeping it beside crypto backups
> is keeping your mesh's root key there.

`--restore` loads a phrase you already have instead of making one.

## 3. Pair this machine

    shrooms keycard pair

One of the five slots, once. The pairing is stored beside the admin key and
reused forever after; it is not a secret that admits anybody, because the PIN is
still needed to sign.

## 4. Make the mesh

    sudo shrooms init --keycard

That is the whole thing: it reads the card's public key, makes that the mesh's
authority, writes the config, and enrols this machine with a credential the card
signs.

    mesh id     KIXKWYU4JTLL46IFC3IOVTTGOI
    prefix      fd06:507d:b6e0::/48
    authority   02372cd5…
    this device is enrolled

**Nothing secret is written.** The admin file holds the card's *public* half,
which is what every node checks against anyway.

**One admin key, not the usual two.** A file authority mints a second key as a
paper way back, because losing the file ends the mesh. A card's key already has
one — the mnemonic — so a second would be another thing to lose, and it could
not itself be a card key: `Authority.CardOnly()` is every key or none, and one
file key would disable the widening in
[ADR-033](adr/033-the-card-is-the-admin-not-the-uid.md).

Start it:

    sudo systemctl enable --now shrooms

## 5. Admit the phone

On the machine with the card:

    sudo shrooms invite

It asks for the card's PIN, prints a token and a QR code, and waits. On the
phone, scan it or paste the token into **Join a mesh**.

When the phone answers, the card signs its credential and the invite completes.
An invite is good for **one device and fifteen minutes**, and only the node that
issued it can answer — leave the command running until it says `Admitted`.

The phone can then admit devices itself, if you enrol the card with it too
(Settings → Keycard → Set up a card). That takes a second pairing slot, and it
is what makes a phone a full admin rather than a member: the key is on the card,
not on either device.

## Afterwards

    shrooms admin show              # every authority this machine holds
    shrooms admin issue --name X …  # enrol a device by its keys, no invite
    shrooms admin renew --all       # reissue before credentials expire
    shrooms admin revoke --device X # withdraw one

All of them reach for the card on their own when the mesh's authority is
card-only. There is no passphrase to type, because there is no file to unlock.

## When something goes wrong

| what you see | what it means |
|---|---|
| `0 of 5 slots free` | every slot is taken. Free them from a device that holds one — the phone's Keycard screen, or `shrooms keycard free-slots` — or wipe the card |
| `6a84` | the same thing, as the card says it |
| `6d00` on pairing | the applet has no secure channel; a Cash card is the usual reason. It cannot be paired at all |
| `6985` reading the key | no PIN verified |
| `wrong PIN. 2 attempts left` | count them. Three wrong and the card blocks and needs its PUK |
| `Sharing violation` | something else is holding the reader |
| `no PC/SC library on this machine` | install pcsc-lite; the message names the package for your distribution |
| `the PC/SC service is not running` | plug the reader in — pcscd starts on demand — or `sudo systemctl start pcscd` |

**`shrooms keycard reset`** is the last resort: it wipes the card completely —
key, PIN, PUK and every pairing — and is the only way back from a card with no
free slots and no device holding one. The key returns only from the mnemonic.

## Testing it

    make shrooms
    SHROOMS_CARD_PIN=nnnnnn make e2e-keycard        # mint, issue, revoke
    SHROOMS_CARD_PIN=nnnnnn make e2e-keycard-mesh   # and invite, join, renew

The second runs two nodes in containers and needs no root. Both pair at most
once and reuse it, so running them does not eat the card.

## One card, one minting machine

**Mint each mesh once, on one machine, and invite the others to it.** This is the
one rule the tooling cannot fully enforce, and the failure is quiet.

A card's admin key is derived, not generated: `m/64265'/<account>'/0'`. The
account is chosen from what the LOCAL machine has already used, and **nothing on
the card records which accounts are spent**. So two machines that have never
minted from this card both pick account 0, derive the same key, and produce:

- the **same admin key**, and therefore the **same mesh id** —
  `Authority.ID()` is a hash over the admin keys and nothing else
- **different network keys**, because each `init` generates a fresh one

Which is the worst pair of properties available. The two meshes **cannot reach
each other**, since the network key decides who can read the control plane. And
they **share an identity**, so a credential or a revocation issued for one
verifies against the other — revoking a device on one mesh produces a revocation
valid on the other, and a member of one holds a credential a peer on the other
will accept.

Neither mesh looks wrong on its own. `shrooms admin show` on either machine
prints a mesh id, a prefix and an admin key that are all exactly what they
should be.

### What protects against it

**Minting refuses when this node already knows the id.** `init --keycard` asks
the running daemon which mesh ids it is on, and stops if the new authority would
duplicate one. That catches the realistic case — two devices in one fleet, where
the second is already a member of the mesh the first minted.

It catches nothing when the other mesh is somewhere this node has never been.
There is no way to check from here, and the card cannot be asked.

**`shrooms admin show` reports it if it has already happened**, whenever one
machine holds two authorities with the same id.

**Minting says the rule out loud**, every time, because both protections above
are partial.

A file authority cannot collide this way: its key is random. This is specific to
deriving a key from something two machines share.

## Re-minting the same mesh — decided 2026-08-26

**An earlier version of this section was wrong about where the guard is, and
the correction is most of the answer.**

It said `admin init --keycard` has no overwrite check. It does: `cmdAdminInit`
stats the admin file and refuses *before* it branches on `--keycard`, so both
`admin init` and `admin init --keycard` refuse when `admin-<label>.json`
already exists.

The unguarded write is in `mintCardAuthorityFull`, which `init` reaches, and
there the guards sit upstream instead: `init` refuses when the config already
exists, and `init --mesh X` refuses when the config already names X. So
**minting cannot replace a mesh this device is still on** — which is the rule
worth having, and it was already true.

What is left is the leftover-file case: `mesh remove kc` leaves
`admin-kc.json` behind on purpose (the authority belongs to the mesh, not the
device), and a later `init --mesh kc --keycard` overwrites it.

**The argument for leaving it.** A mesh id is a hash over the trusted keys, so
re-minting from the same card reproduces the same mesh id and the same
`/48` prefix. For a file authority the private key is *in* the file and
overwriting destroys it — unrecoverable, hence the guard. For a card the
private key never left the card, so the file is derived data and rewriting it
costs nothing.

**The argument against.** The mesh id being identical is exactly what makes it
dangerous. The **network key is freshly generated**, and the network key is the
membership — so a re-mint produces a mesh that *looks* identical in
`shrooms admin show`, same id, same prefix, same admin key, and which no
previously-admitted device can join. Every credential issued under the old
network key is orphaned, and nothing in the output distinguishes the new mesh
from the old one.

That failure is silent and arrives late: the phone that joined yesterday simply
stops being a member, and the id it would be checked against still matches.

**Decided: allowed, and said out loud.**

Allowed because the card holds the private key, so the file is derived data —
unlike the file mint, where overwriting destroys the only copy of the admin key
and ends the mesh. That is why the two paths differ, and the difference is
correct rather than an oversight.

Said out loud because the dangerous part is invisible. Re-minting from the same
card produces the **same mesh id** — the id is a hash over the trusted keys —
and a **new network key**, which is the membership. So the new mesh looks
byte-identical in `shrooms admin show` and admits none of the devices the old
one did. Minting over an existing authority now prints:

    Replacing /home/you/.config/shrooms/admin-kc.json.
    The card's key is unchanged, so this mesh has the same id as the one
    that file described — but a new network key, which is the membership.
    Any device admitted to the old mesh is not a member of this one.

Not `--force`: refusing would block `mesh remove` followed by `init --mesh`,
which is the ordinary way to rebuild a mesh, and the case it would be
protecting against — replacing a mesh this device is still on — is already
refused upstream.
