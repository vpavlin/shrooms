# Android, wanted next

Raised 2026-08-17, in the order I'd do them.

## 1. The app dies when the phone goes fully offline, and does not come back

Observed while testing BLE mesh with everything else switched off: the app
crashed and never auto-started. This is the one that matters — a VPN that needs
launching by hand after a bad network moment is a VPN you stop trusting.

Two halves, and they need separating before either is fixed: *why* it died (a
path that assumes some network exists), and *why nothing restarted it* (service
restart policy, battery optimisation, or the crash taking the VpnService down
with it). Reproduce with aeroplane mode rather than guessing.

## 2. The disconnect button sits where the gesture-navigation swipe lands

Pulling up from the bottom of the phone sometimes disconnects the mesh. A
destructive control directly under the system gesture area. Move it, inset it
above the navigation bar, or make it need a deliberate second action — the
first is probably enough.

## 3. Settings wording

- "Light node" should read "Edge node", matching the config value and the rest
  of the documentation.
- Drop "Tell peers what this device offers". A phone rarely publishes services,
  so the setting is a disclosure control for something that does not exist —
  and every setting nobody needs is one more thing to explain.

## 4. The widget should list meshes, with a toggle each

Today it is a single connect/disconnect. Wanted: one row per mesh with its own
switch, so a mesh can be dropped or brought back without opening the app.

Needs checking first: whether toggling a mesh from the widget is the same
operation the app already has (config write plus a restart of that instance),
and what that costs on a phone — a widget that silently restarts the tunnel is
worse than no widget.
