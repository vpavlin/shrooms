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

**The socket group** may do everything the daemon holds by itself: read status
and the recent log, change this device's name, mode and services, switch a mesh
on or off, join one with an invite token, leave one, reload, and restart the
daemon. Access is decided by the file mode — the socket is 0660 with a
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

### The log tail, because there is no journal to read

A `ui_qml` app cannot read a file, cannot run `journalctl`, and cannot open a
socket. So on the desktop the answer to "why has nothing connected" was a
terminal, while the Android app has had a log pane since its first build — and
that pane is how nearly every failure in this project was actually diagnosed.

The daemon therefore keeps its last two hundred lines in memory and serves them
over the socket (`internal/logtail`). A tail, not a log: it is bounded, it is
gone when the process ends, and the journal remains the record. It carries what
stderr carries — peer names, addresses, why a tunnel failed — every bit of which
`/status` already discloses to the same caller, and no secret, because the
daemon logs none. That is why it sits in the group's tier rather than root's.

### Restarting, because half the settings need one

The mode, a mesh switched on or off, a mesh just joined: each writes the config
and then says "on the next restart". Before, applying that meant `systemctl
restart shrooms` in a terminal — the terminal these controls exist to remove. A
setting you can change from a UI and cannot apply from a UI is half a feature,
and the half that is missing is the half that does anything.

So `/restart` ends the process through the same path the rendezvous watchdog
uses, and the service manager starts a fresh one. It **refuses when nothing
would restart the daemon** — run from a shell rather than under systemd or as a
container's main process — because a button that silently means "stop" leaves a
mesh down until somebody notices. That is the rule the watchdog learned the hard
way, applied to the one other place that can end this process.

### Joining another mesh, now that a restart is one click away

This was on the deferred list, with the reasoning that a new mesh is a new
WireGuard device and only runs after a restart, so "a join that appears to work
and does nothing until the next reboot is exactly the kind of thing people
remember about a tool". The objection was about the missing second half, not
about the join — and the restart button is that second half. So `/join` redeems
an invite into an additional mesh, writes it to the config, and says plainly
that it starts on the next restart, with the button to do it directly below.

It is the most consequential thing in the group's tier and worth naming as
such: joining a mesh gives that mesh's members a tunnel to this device. It is
bounded by needing a live invite token from somebody already inside, and by the
fact that a caller who can reach this socket can already leave meshes and read
every peer's endpoints.

## What is deliberately still manual

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
  a terminal — including seeing what the daemon is saying, and restarting it to
  apply what needs it.
- `socket_group` becomes a documented, deliberate grant instead of a way to
  avoid typing sudo before `status`.
- The one operation a UI most wants — an invite as a QR code — still needs a
  passphrase prompt, which is the correct place for the friction to live.
- One more surface that can misconfigure a node, so the endpoints validate the
  whole config rather than their own field, and say what they changed.
- The socket group can now end the daemon's process. Under a service manager
  that is a few seconds of downtime; without one the endpoint refuses, so the
  worst case is a restart nobody asked for rather than a mesh that stays down.
