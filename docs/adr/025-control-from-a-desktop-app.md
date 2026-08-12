# 025. Control from a desktop app, and what the socket group may do

**Status:** accepted; settings, mesh on/off and leaving are built. Joining and
issuing a credential from the socket are not — see "What is deliberately still
manual".

## Context

The daemon holds a unix control socket, and `basecamp/core` — a Logos Basecamp
module that runs outside the QML sandbox — already speaks HTTP over it to read
`/status`. Everything else on that socket is read-only or root-only, so a
desktop view can watch a mesh and change nothing about it. Every actual
operation means a terminal.

That is a poor split for the thing people most want a UI for. An invite is a
token best delivered as a QR code on a screen; renaming a device is one field;
switching a mesh off is one toggle. All of it currently requires remembering a
command.

The obvious objection is that the socket is powerful, and the obvious answer —
"only reads for the group, writes need root" — is the one this ADR rejects.

## What the socket can actually do

The question is not "does this change something" but **what can the daemon do on
its own**, because the socket is exactly as powerful as the daemon behind it.

[ADR-018](018-credentials-instead-of-a-shared-key.md) changed the answer, and
nothing had noticed. On a mesh with `admin_keys`, membership is an admin-signed
credential naming one device's keys. The daemon does not hold the admin key: it
is a passphrase-protected file in somebody's home directory, and the daemon has
never read it. So:

- **Minting an invite hands over the network key and admits nobody.** The
  joining device gets the rendezvous secret, and every peer refuses its
  announces until the admin key signs a credential for it.
- **Removing a device needs a signature too.** A revocation is honoured because
  the authority signed it, not because it arrived over a socket.

So the socket's real power is the *control plane's confidentiality* — the
network key, the roster, everybody's endpoints — and this device's own
behaviour. Which is serious, and is not the power to decide who belongs.

## Decision

**Two tiers, drawn at what the daemon can do alone.**

**The socket group** may do everything the daemon holds by itself: read status,
change this device's name, mode and services, switch a mesh on or off, leave
one, and reload. Access is decided by the file mode — the socket is 0660 with a
configured group — so a caller who can connect is already authorised, and no
further check is needed or offered.

**Root, or the user the daemon runs as** keeps what would let the socket alone
rewrite membership: installing a credential for this device, and anything else
that decides who belongs without a signature to check. Decided by SO_PEERCRED,
which the kernel sets at connect time and no caller can forge.

**This is the Docker model, and it should be documented like it.** Membership of
the `docker` group is famously equivalent to root, and the project says so
plainly rather than pretending the permission is small. Here the grant is
smaller and still serious: the group can read your mesh's control plane and hand
your network key to a device of its choosing. It cannot make that device a
member. Both halves belong in the documentation, because a grant people
misjudge is worse than one they decline.

### Written to the config, not applied in memory

Every setting endpoint edits `config.toml` and lets the reloader apply what it
can. The config is what a restart reads, so a change applied only to the running
process is one that silently reverts — a worse failure than one that needs a
restart, because it looks like it worked.

Two consequences worth stating:

- **The file is re-read for every request.** Something else edits it — a person
  with an editor, `shrooms join`, the Android bindings — and writing back a copy
  held since startup would discard their work without a word.
- **The whole config is validated before it is written.** A config that does not
  parse is a daemon that will not start, and the next reboot is the worst
  possible time to discover it. A rejected change leaves the file untouched.

### Names are stored in the form that resolves

`Living Room NAS` is stored as `living-room-nas`, because that is what answers
to `living-room-nas.mesh`. Storing what was typed gives a device whose name
works in the roster and not in a browser, which is a bug report nobody enjoys
writing.

## What is deliberately still manual

**Joining another mesh over the socket.** The daemon has what it needs — a
rendezvous connection to redeem the invite, and the config to write — but a new
mesh is a new WireGuard device and only runs after a restart. Worth building;
not worth half-building, because a join that appears to work and does nothing
until the next reboot is exactly the kind of thing people remember about a
tool.

**Issuing a credential.** An invite is two halves (ADR-017): the daemon holds
the exchange and the admin key signs. The socket can do the first and must not
be able to do the second — that separation is the whole reason group access is a
bounded grant. So a desktop invite flow has to reach the admin key in the user's
own session, which means running the CLI with a passphrase rather than teaching
the socket to sign. The shape that fits: the UI collects the passphrase and runs
`shrooms invite`, keeping every line of signing code in one place, and later
that prompt becomes a Keycard tap ([ADR-022](022-keycard-for-the-admin-key.md)).

## Consequences

- A desktop app can drive everything except admission, without sudo and without
  a terminal.
- `socket_group` becomes a documented, deliberate grant instead of a way to
  avoid typing sudo before `status`.
- The one operation a UI most wants — an invite as a QR code — still needs a
  passphrase prompt, which is the correct place for the friction to live.
- One more surface that can misconfigure a node, so the endpoints validate the
  whole config rather than their own field, and say what they changed.
