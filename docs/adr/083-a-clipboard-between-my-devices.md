# 083. A clipboard between my devices, on the mesh

**Status:** accepted 2026-11-11 by the owner ("let's do it. It would be super
useful to have this thing"); built: the daemon and CLI, the phone's core,
the Android app and the Basecamp view.

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
soon as a recipient comes online. A recipient that is online but does not
answer (a host firewall in the way, say) is tried again after 30 seconds,
doubling to every 5 minutes, until it comes online anew. A clip lives a day by default (`--ttl`, at
most a week); the recipient keeps it no longer than that, and no longer than a
week whatever the sender asks. Each side keeps the last 50.

**A secret is for keys and passwords.** `--secret`: an hour by default, never
written to disk on either side, gone five minutes after it is first copied, and
dropped from the sender once every recipient has it. Lists show it as
"(secret)".

**Anyone on a mesh may send to you.** The review (jimmy, 2026-11-11) suggested a
per-mesh list of who may; the owner kept it open ("Everyone in the mesh"): a
clip says who sent it, and the office mesh's members are known. What bounds
it instead: 20 clips a minute from one sender (more are refused, 429), a
clip may not be dated ahead of its arrival, and a sender whose name its admin
did not sign is shown as "(unsigned)", since another member could be using
that name while the device that has it is away (audit F-43).

**Who may read clips here** is whoever may use the daemon's socket (its
group's tier, like the settings): the person at this machine.

## The phone

The Android app is excluded from its own tunnel (`addDisallowedApplication`),
so neither its Go core nor Kotlin can reach an overlay address through the
kernel. The phone's core takes and sends clips through a small userspace TCP
stack instead (`internal/ustack`, `mobile/clip.go`), fed from the tunnel's write path for TCP to port 9181 (and
replies to its own ports) and injecting into it. Its own connections use ports
above the kernel's ephemeral range as the phone sets it (61000 and up with
Linux's default; read from `/proc` at start, since a maker may widen it), so
they cannot collide with other apps' kernel connections from the same
address. Peers accept only the phone's /128, so the stack sends from that
address and not a second one.
Taking an incoming clip is a notification with Copy; sending is the share
sheet, because no Android app can read the clipboard in the background.

## Consequences

- `shrooms clipboard` with `copy`, `paste`, `list` and `remove` (`clip` for
  short): one command with verbs, not three more at the top of the CLI. `/clips` on the socket for
  the apps, with the devices a clip can go to.
- `clips.json` in the state directory, 0600, without secrets.
- Port 9181 on the overlay addresses is the clipboard's. A host firewall that
  filters the mesh interface port by port (atlas: firewalld, the interface in
  `public`, a rich rule per overlay /128) must open it; the installers and the
  README say so. Found in review: no laptop or phone in the first run had such
  a firewall.
- The two-node end-to-end test runs node B in its own network namespace. On one
  kernel the two overlay addresses were both local, so nothing the tunnel had
  to carry could be tested; it now checks a clip each way through it.
- Sharing with someone who is not on the mesh (the owner's wife) is not this. It
  would be WakuLink's shape (pairing by QR, an encrypted topic) in an app of its
  own, and waits until this one is in daily use.
