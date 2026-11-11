# 083. A clipboard between my devices, on the mesh

**Status:** accepted 2026-11-11 by the owner ("let's do it. It would be super
useful to have this thing"); built in the daemon and CLI. The phone, the
Android app and Basecamp follow.

## Context

The owner often needs a short string on another of his devices: a URL, an ID,
a key. Nothing carries it. There is no chat between a person and himself, and
mailing a key to yourself puts it somewhere it should not be. An earlier
project of his, WakuLink, paired devices over a Waku topic for exactly this.

His devices are already paired, encrypted to each other and authenticated:
that is what a mesh is. What was missing was the few hundred lines that move a
string.

He asked whether it belongs in Shrooms or in Shrooms Agents, which already
moves messages and files between his machines and his phone.

## Decision

**In Shrooms.** In Agents the phone is a client: it calls machines and cannot
be called, so a clip from the laptop to the phone would mean the phone polling
every machine, and the week before this the Agents app's polling was found
draining the phone (issue #56). In Shrooms every device, the phone included,
is a peer with an address of its own. And a clipboard has nothing to do with
coding sessions: someone who runs Shrooms alone should have it.

**A clip names its recipients, and only they receive it.** One device, several,
or all of one mesh (`shrooms clipboard copy --to phone`, `--to laptop,phone`,
`--all --mesh home`). There is no broadcast filtered on arrival, so a key meant
for the phone never touches the VPS. "All" never spans meshes, and with
several meshes it must say which: the office mesh has other people's machines
on it. A name may carry its mesh (`phone.office`); a clip goes on one mesh, the
one named or the first that has every recipient.

**It rides the tunnel and nothing else.** Each device takes clips on TCP 9181 on
each of its overlay addresses, one HTTP request per clip. The recipient knows
the sender by the address the request came from, which WireGuard accepts only
from the peer that holds it; a sender's own claim about its name is never read.
A request from an address that is not a member of the mesh it arrived on is
refused. Nothing goes over Waku.

**A recipient that is away gets it when it is back.** The sender keeps the clip
and tries each recipient that is online, at once and every 15 seconds, and as
soon as a recipient comes online. A clip lives a day by default (`--ttl`, at
most a week); the recipient keeps it no longer than that, and no longer than a
week whatever the sender asks. Each side keeps the last 50.

**A secret is for keys and passwords.** `--secret`: an hour by default, never
written to disk on either side, gone five minutes after it is first copied, and
dropped from the sender once every recipient has it. Lists show it as
"(secret)".

**Who may read clips here** is whoever may use the daemon's socket (its
group's tier, like the settings): the person at this machine.

## The phone

The Android app is excluded from its own tunnel (`addDisallowedApplication`),
so neither its Go core nor Kotlin can reach an overlay address through the
kernel. The phone's core will take and send clips through a small userspace
TCP stack instead, fed from the tunnel's write path for TCP to port 9181 (and
replies to its own ports) and injecting into it. Its own connections use ports
61000 and up, outside Linux's ephemeral range, so they cannot collide with
other apps' kernel connections from the same address. Peers accept only the
phone's /128, so the stack must send from that address and not a second one.
Taking an incoming clip is a notification with Copy; sending is the share
sheet, because no Android app can read the clipboard in the background.

## Consequences

- `shrooms clipboard` with `copy`, `paste`, `list` and `remove` (`clip` for
  short): one command with verbs, not three more at the top of the CLI. `/clips` on the socket for
  the apps, with the devices a clip can go to.
- `clips.json` in the state directory, 0600, without secrets.
- Port 9181 on the overlay addresses is the clipboard's.
- The two-node end-to-end test runs node B in its own network namespace. On one
  kernel the two overlay addresses were both local, so nothing the tunnel had
  to carry could be tested; it now checks a clip each way through it.
- Sharing with someone who is not on the mesh (the owner's wife) is not this. It
  would be WakuLink's shape (pairing by QR, an encrypted topic) in an app of its
  own, and waits until this one is in daily use.
