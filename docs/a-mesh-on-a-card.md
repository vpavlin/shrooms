# A mesh whose admin key is a Keycard

The admin key is what admits devices to a mesh. Normally it is a
passphrase-protected file in your home directory; on a Keycard it is a key that
never leaves the card, and admitting a device means the card has to be present.

This is how to set one up and admit a phone to it.

## What you need

- A **Keycard**. Applet 3.1 or later; `shrooms keycard status` will say.
- A **USB smartcard reader**, or a phone with NFC. Both work; this guide uses
  the reader because everything can be done from one place.
- A build with reader support, which is not the default:

      sudo apt install libpcsclite-dev      # or your distribution's equivalent
      make install TAGS=pcsc

  It is off by default because it links libpcsclite through cgo, and the daemon
  never touches a card. A build without it says so and says how to get it.

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
| `this build has no smartcard reader support` | rebuild with `TAGS=pcsc` |

**`shrooms keycard reset`** is the last resort: it wipes the card completely —
key, PIN, PUK and every pairing — and is the only way back from a card with no
free slots and no device holding one. The key returns only from the mnemonic.

## Testing it

    make shrooms TAGS=pcsc
    SHROOMS_CARD_PIN=nnnnnn make e2e-keycard        # mint, issue, revoke
    SHROOMS_CARD_PIN=nnnnnn make e2e-keycard-mesh   # and invite, join, renew

The second runs two nodes in containers and needs no root. Both pair at most
once and reuse it, so running them does not eat the card.
