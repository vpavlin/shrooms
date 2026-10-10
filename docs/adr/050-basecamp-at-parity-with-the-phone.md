# 050. Basecamp at parity with the phone

**Status:** accepted 2026-10-10; built the same day, except inviting with a
Keycard, which is designed below and built separately

## Context

The Basecamp view had not been compared with the Android app for weeks
(docs/before-1.0.md said so). A review on 2026-10-10 found:

- **Three bugs.**
  - The services field was filled from the services *running* on the first
    mesh. Saving wrote that list back over the configured one, deleting any
    service that was configured but not running at the time. It also sent no
    mesh, which a node on several meshes refuses.
  - A join reported a timeout every time. The core gives every socket call two
    seconds, and a join waits up to two minutes for the inviting device.
  - The view pointed at things that no longer exist: a loopback HTTP port, and
    `/etc/logos-vpn/config.toml`.
- **Missing compared with the phone:**
  - a way to the agents app;
  - a peer's IPv4 alias and the ports it listens on;
  - the command that renews a credential;
  - blind relays;
  - joining a first mesh;
  - a diagnostics report;
  - admitting a device with a Keycard.

## Decision

**Basecamp does what the phone does, except what only a phone can do**
(NFC, VPN consent, the home-screen widget). On top of that it has what a
desktop is better at: copying, wide lists, a log you can filter.

The decisions that change who may do what:

1. **A daemon with no mesh lets the socket group join with an invite.**
   - A running daemon already let the group join *another* mesh with one. An
     invite is the admin's decision, made on another device, so redeeming it
     grants nothing the admin did not.
   - Joining with a bare network key still needs root
     (`firstJoinNeedsRoot`), because that is membership with nobody's say-so.
   - `/reload` on a waiting daemon stays root's.
2. **Blind relays are a socket-group setting** (`/config/blind-relays`), as
   they are on the phone. The operator's token can be written but is never
   read back, only whether there is one: anyone in the group can read
   settings, and the token is the relay operator's, not theirs to copy.
3. **A join runs on a thread of the core's own** (`joinWithInviteStart`,
   `joinProgress`). The view's thread still never blocks; the join gets the
   time it actually takes.
4. **The agents app is opened through Basecamp's intent**
   (`basecamp.apps.launch`, declared in `uses`). The view never names a
   provider's internals, and the link is not drawn on a host without
   `request()`.

The status payload gained the per-mesh settings a form has to show:
`quiet_revocations`, `blind_relays_configured` and `blind_relays_refused`
(after inheriting the device's settings).

## Keycard invites from Basecamp (designed; built separately)

The daemon side exists. `/invite/hold` and `/invite/reply` require an
identified caller, and a reply from the group must carry a credential signed
by an admin key (ADR-033). What is missing is the signature: `shrooms_core`
has to ask keycard-basecamp to sign (`requestSign`/`checkSignStatus`, the card
holding the key at `m/64265'/<acct>'/0'`), and the view needs the QR, the
"wants to join" step and the PIN. Until that is built and tested with a real
card, the membership section keeps printing `shrooms invite`.

## Consequences

- `basecamp/test/core_check.sh` drives every new core call against a
  stand-in daemon. `CoreHarness.qml` drives the view through a stand-in core.
  Write paths used to have no test at all.
- A view newer than its daemon hides what the daemon cannot do (the relays
  block is absent against a daemon without the endpoint). It does not show
  controls that fail.
- Creating a mesh from Basecamp is not offered. It makes an admin key, and on
  a desktop that belongs on a card or in the CLI's passphrase file, not in a
  form.
