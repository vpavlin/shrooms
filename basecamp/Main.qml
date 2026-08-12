import QtQuick
import QtQuick.Controls
import QtQuick.Layouts

// A monitoring view for logos-vpn.
//
// Read-only by design: the daemon owns the mesh, this only watches it. Nothing
// here can change the network, so nothing here can break it.
//
// It reads the status file the daemon writes (`status_file` in config.toml)
// rather than talking to the control socket, because QML can read a file and
// cannot open a unix socket. The file is written atomically, so a partial
// document is never observed.
//
// Deliberately plain QtQuick rather than Logos.Controls: the bundled design
// system varies between Basecamp versions — qaku's own view documents controls
// that are "not a type" on older hosts — and a monitoring view that fails to
// load tells you nothing at all. Colours mirror the Android app so the two read
// as one product.
Item {
    id: root
    anchors.fill: parent

    // --- palette (matches the Android app) -----------------------------------
    readonly property color cVoid:     "#07090B"
    readonly property color cPanel:    "#0E1216"
    readonly property color cLine:     "#1C2229"
    readonly property color cAsh:      "#6B7680"
    readonly property color cBone:     "#D6DDE3"
    readonly property color cPhosphor: "#35F0A0"   // connected, carrying traffic
    readonly property color cAmber:    "#F0B429"   // reaching, not there yet
    readonly property color cRust:     "#E05252"   // failed
    readonly property color cViolet:   "#9A7BFF"   // relayed

    // Which mesh, as a colour — deliberately none of the four above.
    //
    // Those all already mean something about whether a peer is reachable, and a
    // mesh ring in one of them around a node in another is unreadable: the
    // Android app tinted its first mesh phosphor and the rings vanished against
    // the very nodes they were meant to group. Same three tints here, in the
    // same order, so a device on two meshes looks the same on both screens.
    readonly property color cSky:        "#5AA9FF"
    readonly property color cBlossom:    "#FF6FB5"
    readonly property color cChartreuse: "#C8E64A"
    readonly property var meshTints: [cSky, cBlossom, cChartreuse, cAsh]

    property var st: ({})
    property var peers: []
    property string problem: ""

    // Two ways in, tried in order.
    //
    // The file is preferred: no port is opened and access is decided by file
    // permissions. But QML refuses file:// reads through XMLHttpRequest unless
    // the host sets QML_XHR_ALLOW_FILE_READ, which is Basecamp's environment
    // and not ours to choose — so when that is off, the daemon's loopback
    // endpoint is the only thing left. Found by running this offscreen rather
    // than by reading the documentation.
    // Where the status comes from, in order of what actually works.
    //
    // Inside Basecamp the only route is the core module. A ui_qml app is
    // sandboxed: a deny-all network manager blocks every HTTP request, and
    // XMLHttpRequest refuses local files outright unless the host sets
    // QML_XHR_ALLOW_FILE_READ, which Basecamp does not. So neither a file nor a
    // port is reachable from here however either is permissioned — the log says
    // so plainly, in two different ways, and it took three wrong guesses before
    // anyone read it.
    //
    // shrooms_core runs in its own process, unsandboxed, and reads the daemon's
    // control socket. That is what Basecamp's own spec prescribes: UI apps
    // reach the outside indirectly, through Logos Modules.
    //
    // The file and endpoint remain for running this outside Basecamp — the
    // offscreen check, or a plain qml runtime — where the sandbox does not
    // apply and the core module does not exist.
    property string statusPath: "/run/shrooms/status.json"
    property string statusUrl: "http://127.0.0.1:8787/status"
    property var sources: [
        { url: "status.json", file: true },              // beside Main.qml
        { url: "file://" + statusPath, file: true },     // an absolute path
        { url: statusUrl, file: false },                 // the daemon's endpoint
    ]
    property int source: 0
    property bool everLoaded: false
    property int attempts: 0

    readonly property bool fileBlocked: !sources[source].file

    /** True when the core module is present, which is the Basecamp case. */
    readonly property bool haveCore: typeof logos !== "undefined" && !!logos.callModule

    function callCore(method, args) {
        if (!haveCore) return ""
        try {
            return String(logos.callModule("shrooms_core", method, args || []))
        } catch (e) {
            return ""
        }
    }

    // What a write said, shown until the next one. Kept as one line rather
    // than a dialog: every one of these operations is small, and a modal for
    // "name set" would be worse than the operation.
    property string said: ""
    property bool saidBad: false
    property bool settingsOpen: false

    // A write, and then a refresh, because every one of these changes
    // something the status page shows. Reporting the daemon's own sentence is
    // deliberate: it says what changed *and when it takes effect*, which is
    // the part a UI cannot know and keeps getting wrong.
    function callWrite(method, args) {
        if (!haveCore) {
            root.said = "no shrooms_core module; this view can only read"
            root.saidBad = true
            return
        }
        var t = String(callCore(method, args) || "").trim()
        for (var i = 0; i < 2 && t.charAt(0) === '"'; i++) {
            try { t = String(JSON.parse(t)).trim() } catch (e) { break }
        }
        var d = null
        try { d = JSON.parse(t) } catch (e) { d = null }
        if (!d) {
            root.said = "the daemon said nothing readable"
            root.saidBad = true
        } else if (d.error) {
            root.said = d.error + (d.detail ? " — " + d.detail : "")
            root.saidBad = true
        } else {
            root.said = d.result ? d.result : "done"
            root.saidBad = false
        }
        root.reload()
    }

    function reload() {
        attempts++

        if (haveCore) {
            var raw = callCore("status", [])
            // The bridge may hand back a JSON string that is itself quoted;
            // qaku unwraps the same way. Unwrap, then parse.
            var t = String(raw || "").trim()
            for (var i = 0; i < 2 && t.charAt(0) === '"'; i++) {
                try { t = String(JSON.parse(t)).trim() } catch (e) { break }
            }
            if (t.charAt(0) === "{") {
                try {
                    var d = JSON.parse(t)
                    if (d.error) {
                        root.problem = d.error + (d.detail ? "\n" + d.detail : "")
                        root.peers = []
                    } else {
                        root.st = d
                        root.peers = d.peers || []
                        root.problem = ""
                        root.everLoaded = true
                    }
                    return
                } catch (e) { /* fall through to the file transports */ }
            }
            root.problem = "shrooms_core returned nothing readable"
            return
        }

        // Advance on elapsed attempts rather than on an error: a refused
        // file:// read does not deliver a failed request, so waiting for one
        // waits forever.
        if (!everLoaded && attempts > 2 * (source + 1) && source < sources.length - 1) {
            source++
        }
        var s = sources[source]
        fetch(s.url, s.file)
    }

    function fetch(url, isFile) {
        var xhr = new XMLHttpRequest()
        xhr.onreadystatechange = function() {
            if (xhr.readyState !== XMLHttpRequest.DONE) return

            if (!xhr.responseText) {
                if (isFile) return          // let the escalation above handle it
                root.problem = "no status from " + root.statusPath
                            + "\nnor from " + root.statusUrl
                root.peers = []
                return
            }
            try {
                var d = JSON.parse(xhr.responseText)
                root.st = d
                root.peers = d.peers || []
                root.problem = ""
                root.everLoaded = true
            } catch (e) {
                // The daemon writes atomically, so a partial document should be
                // impossible; if one appears, say so rather than showing a
                // stale mesh as if it were current.
                root.problem = "status is not readable JSON"
            }
        }
        try {
            xhr.open("GET", url)
            xhr.send()
        } catch (e) {
            // A refused file read can also throw outright, depending on host.
            if (isFile) root.fileBlocked = true
        }
    }

    Component.onCompleted: reload()
    Timer {
        interval: root.everLoaded ? 2000 : 700
        running: true; repeat: true
        onTriggered: root.reload()
    }

    function reachOf(p) {
        if (p.live) return "connected"
        if (p.online) return "reaching"
        return "offline"
    }
    function colourOf(p) {
        var r = reachOf(p)
        if (r === "connected") return p.relayed ? cViolet : cPhosphor
        if (r === "reaching") return cAmber
        return cAsh
    }
    function rate(bps) {
        if (!bps || bps < 1) return "—"
        if (bps < 1024) return Math.round(bps) + " B/s"
        if (bps < 1024 * 1024) return (bps / 1024).toFixed(1) + " KB/s"
        return (bps / 1048576).toFixed(1) + " MB/s"
    }
    function since(s) {
        if (!s) return ""
        if (s < 60) return Math.round(s) + "s"
        if (s < 3600) return Math.round(s / 60) + "m"
        return Math.round(s / 3600) + "h"
    }

    // --- meshes -------------------------------------------------------------
    //
    // Every mesh this device knows about, in one order that the graph, the
    // roster headings and the legend all read. They have to agree: a tint that
    // means one mesh in the ring and another in the list is worse than no tint,
    // and they would disagree the moment one of them derived its own order.
    //
    // Built from `st.meshes` when the daemon reports it and topped up from the
    // peers' own labels, because a mesh whose peers are visible is a mesh worth
    // colouring even if an older daemon never listed it. A single-mesh node
    // labels nothing at all, so this comes out empty and every multi-mesh
    // decoration below switches itself off.
    readonly property var meshOrder: {
        var out = []
        var ms = (root.st && root.st.meshes) ? root.st.meshes : []
        for (var i = 0; i < ms.length; i++) {
            var l = ms[i] ? ms[i].label : undefined
            if (l && out.indexOf(l) < 0) out.push(l)
        }
        for (var j = 0; j < root.peers.length; j++) {
            var m = root.peers[j].mesh || ""
            if (m !== "" && out.indexOf(m) < 0) out.push(m)
        }
        return out
    }
    readonly property bool multiMesh: meshOrder.length > 1

    function meshTint(label) {
        var i = meshOrder.indexOf(label)
        if (i < 0) i = 0
        return meshTints[i % meshTints.length]
    }

    // Sorted by (mesh, name) so each mesh occupies an arc of the graph's ring
    // rather than being scattered around it, and so the roster can group by
    // simply walking the list. Sorting a copy: `peers` is what the harness and
    // the header count, and reordering it under them would be a surprise.
    readonly property var sortedPeers: {
        var ps = root.peers ? root.peers.slice() : []
        ps.sort(function(a, b) {
            var am = a.mesh || "", bm = b.mesh || ""
            if (am !== bm) return am < bm ? -1 : 1
            var an = a.name || "", bn = b.name || ""
            return an < bn ? -1 : (an > bn ? 1 : 0)
        })
        return ps
    }

    // The roster as rows, with a heading before each mesh. One flat model
    // rather than a ListView section, because the model here is a plain JS
    // array and sections want roles; a header entry is the same thing and works
    // against whatever the daemon sent.
    readonly property var rows: {
        var out = [], ps = root.sortedPeers, last = null
        for (var i = 0; i < ps.length; i++) {
            var m = ps[i].mesh || ""
            if (root.multiMesh && m !== last) {
                out.push({ header: true, mesh: m })
                last = m
            }
            out.push({ header: false, peer: ps[i] })
        }
        return out
    }

    /** How many peers a mesh has, counted here when the daemon did not count. */
    function meshPeers(m) {
        if (typeof m.peers === "number") return m.peers
        var c = 0
        for (var i = 0; i < root.peers.length; i++)
            if ((root.peers[i].mesh || "") === m.label) c++
        return c
    }

    /**
     * Days until this device's credential on a mesh runs out, or 999 when the
     * daemon did not say. Old daemons omit `expires` entirely, and a missing
     * field must read as "nothing to worry about" rather than as an expiry in
     * 1970.
     */
    function endsIn(m) {
        if (!m || !m.expires) return 999
        return Math.floor((m.expires - Date.now() / 1000) / 86400)
    }

    /**
     * What a peer says it offers, as the addresses you would actually type.
     *
     * A claim, not a health report: the peer repeats this list every few
     * minutes and only it knows whether the port answers. Printed anyway
     * because a service name is worthless until you can see it spelled out.
     */
    function serviceUrls(p) {
        var s = p ? p.services : undefined
        if (!s || !s.length) return ""
        var host = p.dns_name || p.name || ""
        var out = []
        for (var i = 0; i < s.length; i++) out.push("http://" + s[i] + "." + host)
        return out.join("   ")
    }

    /**
     * Draw the links between other peers as well as this device's own.
     *
     * Off by default because those links are inferred, not measured: no node
     * knows how any two others reach each other, and a graph that draws a guess
     * the same way it draws a tunnel is lying.
     */
    property bool wholeMesh: false

    // A 32-bit string hash, the same one Java's String.hashCode computes, so a
    // node wanders the same way here as it does on the phone.
    function hashOf(s) {
        var h = 0
        for (var i = 0; i < s.length; i++) h = (h * 31 + s.charCodeAt(i)) | 0
        return h
    }

    Rectangle {
        anchors.fill: parent
        color: cVoid

        ColumnLayout {
            anchors.fill: parent
            anchors.margins: 20
            spacing: 14

            // --- header ---------------------------------------------------
            ColumnLayout {
                spacing: 3
                Layout.fillWidth: true

                RowLayout {
                    spacing: 8
                    Rectangle {
                        width: 9; height: 9; radius: 5
                        color: root.problem !== "" ? cRust
                             : (root.peers.length > 0 ? cPhosphor : cAsh)
                    }
                    Text {
                        text: root.st.name ? root.st.name : "logos-vpn"
                        color: cBone
                        font.family: "monospace"; font.pixelSize: 17
                    }
                    Item { Layout.fillWidth: true }
                    Text {
                        // Counted rather than assumed: a peer announcing is not
                        // a peer you can reach, and the difference is the whole
                        // point of the colours below.
                        text: {
                            var up = 0
                            for (var i = 0; i < root.peers.length; i++)
                                if (root.peers[i].live) up++
                            return up + " of " + root.peers.length + " reachable"
                        }
                        color: cAsh
                        font.family: "monospace"; font.pixelSize: 11
                    }
                }
                Text {
                    text: root.st.overlay ? root.st.overlay : ""
                    color: cAsh
                    font.family: "monospace"; font.pixelSize: 11
                }
            }

            // --- trouble ---------------------------------------------------
            Rectangle {
                Layout.fillWidth: true
                readonly property var rz: root.st ? root.st.rendezvous : undefined
                visible: root.problem !== "" || (rz !== undefined && rz.ok === false)
                color: "transparent"
                border.color: root.problem !== "" ? cRust : cAmber
                border.width: 1
                radius: 8
                implicitHeight: trouble.implicitHeight + 20

                Text {
                    id: trouble
                    anchors.fill: parent
                    anchors.margins: 10
                    wrapMode: Text.WordWrap
                    color: root.problem !== "" ? cRust : cAmber
                    font.family: "monospace"; font.pixelSize: 11
                    text: {
                        if (root.problem !== "")
                            return root.problem
                                 + "\n\nSet `status_file = \"" + root.statusPath
                                 + "\"` in /etc/logos-vpn/config.toml and restart the daemon."
                        // Discovery being down is not peers being offline. They
                        // look identical and have different causes.
                        var r = parent.rz
                        if (r === undefined) return ""
                        return "discovery: " + (r.problem || "unavailable")
                             + "\nestablished tunnels are unaffected"
                    }
                }
            }

            // --- graph ------------------------------------------------------
            //
            // Worth drawing because the topology is the interesting part and a
            // list cannot show it: which peers you reach directly, which go
            // through a relay, and which are talking but unreachable are three
            // different problems that look identical in a list.
            Rectangle {
                Layout.fillWidth: true
                Layout.preferredHeight: Math.max(260, root.height * 0.38)
                color: cPanel
                radius: 10

                Canvas {
                    id: graph
                    anchors.fill: parent
                    anchors.margins: 12

                    // Where the wander is in its cycle, in radians.
                    property real drift: 0

                    // Repaint the moment the mesh changes shape, rather than
                    // waiting up to a frame for the timer below: a peer coming
                    // up should land on the graph when it lands in the list.
                    property string shape: JSON.stringify(root.sortedPeers.map(function(p) {
                        return [p.mesh, p.name, p.live, p.online, p.relayed, p.relay]
                    }))
                    onShapeChanged: requestPaint()
                    onWidthChanged: requestPaint()
                    onHeightChanged: requestPaint()

                    Timer {
                        // A whole wander cycle every 19 seconds, the same period
                        // as the phone's, stepped from this timer's own interval
                        // so the two numbers cannot drift apart when one is
                        // edited. Sixteen frames a second is plenty for motion
                        // this slow and a Canvas repaint is not free.
                        interval: 60
                        running: true; repeat: true
                        onTriggered: {
                            var step = 2 * Math.PI * (interval / 1000) / 19
                            graph.drift = (graph.drift + step) % (2 * Math.PI)
                            graph.requestPaint()
                        }
                    }

                    onPaint: {
                        var ctx = getContext("2d")
                        ctx.reset()

                        var cx = width / 2, cy = height / 2
                        var radius = Math.min(width, height) / 2 * 0.66
                        var ps = root.sortedPeers
                        var n = ps.length
                        if (n === 0) {
                            ctx.fillStyle = root.cAsh
                            ctx.font = "11px monospace"
                            ctx.textAlign = "center"
                            ctx.fillText("no peers yet", cx, cy)
                            return
                        }
                        var t = drift
                        var i, j, p

                        // How much a curve bows, swaying slowly. One cycle per
                        // drift period, for the same reason as the wander
                        // below: a filament that snaps back to a different
                        // curvature is the same glitch, on the links.
                        function bendOf(seed) {
                            var ph = ((seed >>> 5) & 0xffff) / 65535 * 2 * Math.PI
                            return 0.06 + 0.05 * Math.sin(t + ph)
                        }

                        // Links drawn as curves rather than straight lines.
                        // Pure decoration — the curve carries nothing the line
                        // did not — but the graph is the thing people look at,
                        // and mycelium does not grow in straight lines.
                        function filament(ax, ay, bx, by, bend) {
                            var mx = (ax + bx) / 2 - (by - ay) * bend
                            var my = (ay + by) / 2 + (bx - ax) * bend
                            ctx.beginPath()
                            ctx.moveTo(ax, ay)
                            ctx.quadraticCurveTo(mx, my, bx, by)
                            ctx.stroke()
                        }

                        // Every position for this frame, computed once: the
                        // links and the nodes must agree, and computing the
                        // wander twice invites them not to.
                        var at = []
                        for (i = 0; i < n; i++) {
                            var a = -Math.PI / 2 + 2 * Math.PI * i / n
                            // Seeded from the peer's own identity rather than
                            // from a random number, so a node keeps its motion
                            // across refreshes instead of jumping every time
                            // the roster is polled.
                            var seed = root.hashOf((ps[i].name || "?") + "/" + (ps[i].mesh || ""))
                            var phase = ((seed & 0xffff) / 65535) * 2 * Math.PI
                            var amp = radius * 0.085
                                    * (0.55 + (((seed >>> 8) & 0xff) / 255) * 0.45)
                            // Whole numbers of cycles, always.
                            //
                            // t runs 0..2π and restarts, so a harmonic that is
                            // not an integer lands somewhere else when it wraps
                            // and every node teleports — once every nineteen
                            // seconds, which is exactly long enough to look
                            // like a redraw bug rather than the animation's own
                            // doing. Two different integers give a Lissajous
                            // path instead of a circle, which is the part that
                            // made it look alive, and it closes perfectly.
                            var kx = 1 + ((seed >>> 16) & 1)
                            var ky = 2 + ((seed >>> 20) & 1)
                            at.push({ x: cx + radius * Math.cos(a) + amp * Math.cos(kx * t + phase),
                                      y: cy + radius * Math.sin(a) + amp * Math.sin(ky * t + phase) })
                        }

                        // The relay each mesh has, not the first relay on the
                        // screen. Traffic to a peer on one mesh cannot pass
                        // through a relay on another — different prefix,
                        // different WireGuard device — so bending the line
                        // through one drew a path that cannot exist, and the
                        // whole point of the graph is which path traffic takes.
                        var relayOf = ({})
                        for (i = 0; i < n; i++) {
                            var mk = ps[i].mesh || ""
                            if (ps[i].relay && relayOf[mk] === undefined) relayOf[mk] = i
                        }

                        for (i = 0; i < n; i++) {
                            p = ps[i]
                            var bend = bendOf(root.hashOf(p.name || "?"))
                            if (p.live && p.relayed) {
                                ctx.lineWidth = 2.5
                                ctx.strokeStyle = root.cViolet
                                ctx.globalAlpha = 0.75
                                ctx.setLineDash([9, 11])
                                var r = relayOf[p.mesh || ""]
                                if (r !== undefined && r !== i) {
                                    filament(cx, cy, at[r].x, at[r].y, bend)
                                    filament(at[r].x, at[r].y, at[i].x, at[i].y, bend)
                                } else {
                                    // Relayed by someone this device cannot
                                    // see, which happens: the relay may be on a
                                    // mesh whose roster we do not carry.
                                    filament(cx, cy, at[i].x, at[i].y, bend)
                                }
                            } else if (p.live) {
                                ctx.lineWidth = 2.5
                                ctx.strokeStyle = root.cPhosphor
                                ctx.globalAlpha = 0.85
                                ctx.setLineDash([])
                                filament(cx, cy, at[i].x, at[i].y, bend)
                            } else if (p.online) {
                                // Announcing but no tunnel: trying, not
                                // connected. Drawn faintly so it is visibly not
                                // the same thing as the line above.
                                ctx.lineWidth = 1.5
                                ctx.strokeStyle = root.cAmber
                                ctx.globalAlpha = 0.5
                                ctx.setLineDash([9, 11])
                                filament(cx, cy, at[i].x, at[i].y, bend)
                            } else {
                                ctx.lineWidth = 1
                                ctx.strokeStyle = root.cLine
                                ctx.globalAlpha = 1
                                ctx.setLineDash([9, 11])
                                filament(cx, cy, at[i].x, at[i].y, bend)
                            }
                        }

                        // Assumed links, underneath the nodes: same mesh, both
                        // reachable from here, so probably reachable from each
                        // other. Only within a mesh, because peers on different
                        // meshes genuinely cannot reach each other at all.
                        if (root.wholeMesh) {
                            ctx.lineWidth = 1.4
                            // Faint enough to read as a guess, visible enough
                            // to read at all. A tenth of an alpha was honest
                            // and invisible, which is not a useful pair.
                            ctx.globalAlpha = 0.30
                            ctx.setLineDash([5, 9])
                            for (i = 0; i < n; i++) {
                                if (!ps[i].live) continue
                                for (j = i + 1; j < n; j++) {
                                    if (!ps[j].live) continue
                                    if ((ps[i].mesh || "") !== (ps[j].mesh || "")) continue
                                    ctx.strokeStyle = root.meshTint(ps[i].mesh || "")
                                    filament(at[i].x, at[i].y, at[j].x, at[j].y,
                                             bendOf(i * 31 + j))
                                }
                            }
                        }

                        ctx.setLineDash([])
                        for (i = 0; i < n; i++) {
                            p = ps[i]
                            var col = root.colourOf(p)
                            // Punch the background out so links do not run
                            // under the node or its label.
                            ctx.globalAlpha = 1
                            ctx.fillStyle = root.cVoid
                            ctx.beginPath(); ctx.arc(at[i].x, at[i].y, 22, 0, 2 * Math.PI); ctx.fill()
                            ctx.globalAlpha = 0.14
                            ctx.fillStyle = col
                            ctx.beginPath(); ctx.arc(at[i].x, at[i].y, 18, 0, 2 * Math.PI); ctx.fill()
                            ctx.globalAlpha = 1
                            ctx.beginPath(); ctx.arc(at[i].x, at[i].y, 7, 0, 2 * Math.PI); ctx.fill()
                            if (root.multiMesh) {
                                // The mesh is the ring, the reachability is the
                                // node: the colour you look at first says
                                // whether the peer answers, and the grouping is
                                // drawn around it.
                                ctx.globalAlpha = 0.85
                                ctx.strokeStyle = root.meshTint(p.mesh || "")
                                ctx.lineWidth = 2
                                ctx.beginPath()
                                ctx.arc(at[i].x, at[i].y, 18, 0, 2 * Math.PI)
                                ctx.stroke()
                                ctx.globalAlpha = 1
                            }
                            if (p.relay) {
                                // Inside the mesh ring rather than on top of
                                // it: two rings at one radius are one ring, in
                                // whichever colour was drawn second.
                                ctx.strokeStyle = root.cViolet
                                ctx.lineWidth = 1.5
                                ctx.beginPath()
                                ctx.arc(at[i].x, at[i].y, 13, 0, 2 * Math.PI)
                                ctx.stroke()
                            }
                            ctx.fillStyle = col
                            ctx.font = "10px monospace"
                            ctx.textAlign = "center"
                            ctx.fillText(p.name || "?", at[i].x, at[i].y + 32)
                        }

                        // This device last: everything else is drawn relative
                        // to it. One node's view, not a map of the mesh — no
                        // node knows how any two others reach each other. It
                        // breathes rather than wanders, since the rest moving
                        // around it is exactly what the graph means; twice the
                        // drift, so it too closes on the wrap.
                        var breath = 1 + 0.06 * Math.sin(2 * t)
                        ctx.globalAlpha = 1
                        ctx.fillStyle = root.cVoid
                        ctx.beginPath(); ctx.arc(cx, cy, 26, 0, 2 * Math.PI); ctx.fill()
                        ctx.fillStyle = root.cBone
                        ctx.beginPath(); ctx.arc(cx, cy, 13 * breath, 0, 2 * Math.PI); ctx.fill()
                        ctx.globalAlpha = 0.18
                        ctx.strokeStyle = root.cBone
                        ctx.lineWidth = 1.5
                        ctx.beginPath(); ctx.arc(cx, cy, 26 * breath, 0, 2 * Math.PI); ctx.stroke()
                    }
                }

                // Only the whole-mesh picture is labelled, and only to disown
                // the extra links: they are inferred from what peers report, so
                // an unmarked drawing of them would pass off a guess as a
                // measurement. The default picture is all measured and needs
                // nothing said about it.
                Text {
                    visible: root.wholeMesh && root.peers.length > 0
                    anchors.horizontalCenter: parent.horizontalCenter
                    anchors.top: parent.top
                    anchors.topMargin: 8
                    text: "whole mesh · faint links are assumed"
                    color: cAmber
                    font.family: "monospace"; font.pixelSize: 10
                }
            }

            // The toggle, and what the tints mean. Both belong under the
            // drawing rather than in settings: they change what this picture
            // shows, not what the daemon does.
            RowLayout {
                Layout.fillWidth: true
                spacing: 14
                visible: root.peers.length > 0

                Text {
                    text: root.wholeMesh ? "whole mesh ▾" : "whole mesh ▸"
                    color: root.wholeMesh ? cPhosphor : cAsh
                    font.family: "monospace"; font.pixelSize: 11
                    MouseArea {
                        anchors.fill: parent
                        cursorShape: Qt.PointingHandCursor
                        onClicked: {
                            root.wholeMesh = !root.wholeMesh
                            graph.requestPaint()
                        }
                    }
                }

                Repeater {
                    model: root.multiMesh ? root.meshOrder : []
                    delegate: RowLayout {
                        spacing: 5
                        Rectangle {
                            width: 7; height: 7; radius: 4
                            color: root.meshTint(modelData)
                        }
                        Text {
                            text: modelData
                            color: root.meshTint(modelData)
                            font.family: "monospace"; font.pixelSize: 10
                        }
                    }
                }
                Item { Layout.fillWidth: true }
            }

            // One row per mesh, above the roster it groups. Only when there is
            // more than one: on a single-mesh device every line here repeats
            // the header.
            Repeater {
                model: (root.st.meshes && root.st.meshes.length > 1) ? root.st.meshes : []
                delegate: RowLayout {
                    Layout.fillWidth: true
                    spacing: 10

                    Text {
                        text: modelData.label || "?"
                        color: root.meshTint(modelData.label)
                        font.family: "monospace"; font.pixelSize: 11
                        Layout.preferredWidth: 90
                    }
                    Text {
                        text: (modelData.overlay || "")
                              + (modelData.prefix ? "   " + modelData.prefix : "")
                        color: cAsh
                        font.family: "monospace"; font.pixelSize: 10
                        elide: Text.ElideRight
                        Layout.fillWidth: true
                    }
                    Text {
                        text: root.meshPeers(modelData) + " peers"
                        color: cAsh
                        font.family: "monospace"; font.pixelSize: 10
                    }
                    Text {
                        // Said out loud because zero relays is a configuration
                        // rather than a fault, and it stays invisible until
                        // somebody is on mobile data and reaches nobody.
                        visible: modelData.relays === 0
                        text: "no relay"
                        color: cAmber
                        font.family: "monospace"; font.pixelSize: 10
                    }
                    Text {
                        // Ten days is enough warning to find whoever holds the
                        // admin key, and short enough that it is not on screen
                        // permanently. Renewal is not a thing this view can do.
                        visible: root.endsIn(modelData) <= 10
                        text: root.endsIn(modelData) < 0
                              ? "membership has ended"
                              : "membership ends in " + root.endsIn(modelData) + " days"
                        color: cAmber
                        font.family: "monospace"; font.pixelSize: 10
                    }
                }
            }

            // --- list -------------------------------------------------------
            ListView {
                Layout.fillWidth: true
                Layout.fillHeight: true
                clip: true
                spacing: 5
                model: root.rows

                // One delegate for both kinds of row, switched by `header`.
                // Two delegates would want a chooser, which lives in a module
                // this view deliberately does not import.
                delegate: Item {
                    width: ListView.view.width
                    implicitHeight: modelData.header === true
                                    ? heading.implicitHeight + 12
                                    : card.implicitHeight

                    // A peer row's facts, or nothing at all on a heading row.
                    // Never undefined: a binding that dereferences it on the
                    // wrong kind of row throws, and one thrown TypeError per
                    // delegate is a roster that renders half a list.
                    readonly property var p: modelData.peer || ({})

                    Text {
                        id: heading
                        visible: modelData.header === true
                        anchors.left: parent.left
                        anchors.bottom: parent.bottom
                        text: modelData.mesh ? modelData.mesh : "no mesh label"
                        color: root.meshTint(modelData.mesh)
                        font.family: "monospace"; font.pixelSize: 11
                    }

                    Rectangle {
                        id: card
                        visible: modelData.header !== true
                        width: parent.width
                        implicitHeight: row.implicitHeight + 20
                        color: cPanel
                        radius: 8

                        ColumnLayout {
                            id: row
                            anchors.fill: parent
                            anchors.margins: 10
                            spacing: 4

                            RowLayout {
                                spacing: 8
                                Layout.fillWidth: true
                                Rectangle {
                                    width: 8; height: 8; radius: 4
                                    color: root.colourOf(p)
                                }
                                Text {
                                    text: p.name || "?"
                                    color: cBone
                                    font.family: "monospace"; font.pixelSize: 13
                                }
                                Text {
                                    visible: p.relay === true
                                    text: "RELAY"
                                    color: cViolet
                                    font.family: "monospace"; font.pixelSize: 9
                                }
                                Item { Layout.fillWidth: true }
                                Text {
                                    text: p.live
                                          ? (p.relayed ? "relayed" : "direct")
                                            + (p.rtt_ms ? " · " + p.rtt_ms + "ms" : "")
                                          : (p.online ? "no tunnel" : "offline")
                                    color: p.relayed ? cViolet : cAsh
                                    font.family: "monospace"; font.pixelSize: 11
                                }
                            }

                            Text {
                                text: (p.dns_name ? p.dns_name + "   " : "")
                                      + (p.overlay || "")
                                color: cAsh
                                font.family: "monospace"; font.pixelSize: 10
                                elide: Text.ElideRight
                                Layout.fillWidth: true
                            }

                            // What this peer says it offers, as addresses. Read
                            // only, selectable, and on one line: these are a
                            // claim the peer repeats rather than a health
                            // report, and a roster that grows a table per peer
                            // stops being a roster.
                            TextEdit {
                                visible: text !== ""
                                text: root.serviceUrls(p)
                                readOnly: true
                                selectByMouse: true
                                // Dimmed while the peer cannot be reached. The
                                // list outlives reachability on purpose — a
                                // sleeping device still offers what it offers
                                // — but drawn identically it invites somebody
                                // to try an address that cannot answer.
                                color: p.live === true ? cPhosphor : cAsh
                                font.family: "monospace"; font.pixelSize: 10
                                wrapMode: TextEdit.Wrap
                                Layout.fillWidth: true
                            }

                            RowLayout {
                                spacing: 16
                                visible: p.live === true
                                Text {
                                    text: "↓ " + root.rate(p.rx_bps)
                                          + "   ↑ " + root.rate(p.tx_bps)
                                    color: cAsh
                                    font.family: "monospace"; font.pixelSize: 10
                                }
                                Text {
                                    visible: p.tunnel_after_s > 0
                                    // Coerced rather than guarded by `visible`:
                                    // a binding is evaluated whether or not the
                                    // item is shown, so calling toFixed on the
                                    // field an older daemon omits throws even
                                    // on a row nobody can see.
                                    text: "connected in "
                                          + (p.tunnel_after_s || 0).toFixed(1) + "s"
                                    color: cAsh
                                    font.family: "monospace"; font.pixelSize: 10
                                }
                                Text {
                                    visible: p.handshake_age_s > 0
                                    // Shown always, never a bare "up": a peer
                                    // that restarted leaves the other side
                                    // holding a session that stays valid for a
                                    // while.
                                    text: "handshake " + root.since(p.handshake_age_s) + " ago"
                                    color: cAsh
                                    font.family: "monospace"; font.pixelSize: 10
                                }
                            }
                        }
                    }
                }
            }

            // --- settings -------------------------------------------------
            //
            // Collapsed by default. This view's job is to show a mesh, and a
            // form is not that; but the settings it can change are exactly the
            // ones that otherwise mean finding a terminal, so they belong here
            // rather than nowhere (ADR-025).
            //
            // What is missing is deliberate and worth knowing while reading
            // this: nothing here admits or removes a device. That needs the
            // admin key, which the daemon has never held.
            ColumnLayout {
                Layout.fillWidth: true
                spacing: 8
                visible: root.haveCore

                Text {
                    text: root.settingsOpen ? "settings ▾" : "settings ▸"
                    color: cPhosphor
                    font.family: "monospace"; font.pixelSize: 11
                    MouseArea {
                        anchors.fill: parent
                        cursorShape: Qt.PointingHandCursor
                        onClicked: root.settingsOpen = !root.settingsOpen
                    }
                }

                Text {
                    visible: root.said !== ""
                    text: root.said
                    color: root.saidBad ? cRust : cPhosphor
                    font.family: "monospace"; font.pixelSize: 10
                    wrapMode: Text.WordWrap
                    Layout.fillWidth: true
                }

                ColumnLayout {
                    visible: root.settingsOpen
                    Layout.fillWidth: true
                    spacing: 10

                    // This device's name, as its peers see it. Prefilled from
                    // status so the field starts as the truth rather than
                    // empty, which reads as "unset".
                    RowLayout {
                        spacing: 8
                        Layout.fillWidth: true
                        Text {
                            text: "name"
                            color: cAsh
                            font.family: "monospace"; font.pixelSize: 11
                            Layout.preferredWidth: 70
                        }
                        Rectangle {
                            Layout.fillWidth: true
                            height: 26
                            color: cPanel
                            border.color: cLine
                            TextInput {
                                id: nameField
                                anchors.fill: parent
                                anchors.leftMargin: 6
                                verticalAlignment: TextInput.AlignVCenter
                                color: cBone
                                font.family: "monospace"; font.pixelSize: 11
                                selectByMouse: true
                                text: root.st.name ? root.st.name : ""
                                onAccepted: root.callWrite("setName", [text])
                            }
                        }
                        Text {
                            text: "set"
                            color: cPhosphor
                            font.family: "monospace"; font.pixelSize: 11
                            MouseArea {
                                anchors.fill: parent
                                cursorShape: Qt.PointingHandCursor
                                onClicked: root.callWrite("setName", [nameField.text])
                            }
                        }
                    }

                    // Services, as the config writes them: "name:port", comma
                    // separated. Shown in the form that is stored so that what
                    // is typed here and what ends up in the file are the same
                    // string.
                    RowLayout {
                        spacing: 8
                        Layout.fillWidth: true
                        Text {
                            text: "services"
                            color: cAsh
                            font.family: "monospace"; font.pixelSize: 11
                            Layout.preferredWidth: 70
                        }
                        Rectangle {
                            Layout.fillWidth: true
                            height: 26
                            color: cPanel
                            border.color: cLine
                            TextInput {
                                id: servicesField
                                anchors.fill: parent
                                anchors.leftMargin: 6
                                verticalAlignment: TextInput.AlignVCenter
                                color: cBone
                                font.family: "monospace"; font.pixelSize: 11
                                selectByMouse: true
                                text: {
                                    var out = []
                                    var svcs = root.st.services || []
                                    for (var i = 0; i < svcs.length; i++)
                                        out.push(svcs[i].name + ":" + svcs[i].port)
                                    return out.join(", ")
                                }
                                onAccepted: root.callWrite("setServices", [text])
                            }
                        }
                        Text {
                            text: "set"
                            color: cPhosphor
                            font.family: "monospace"; font.pixelSize: 11
                            MouseArea {
                                anchors.fill: parent
                                cursorShape: Qt.PointingHandCursor
                                onClicked: root.callWrite("setServices", [servicesField.text])
                            }
                        }
                    }

                    // Whether peers are told what this device publishes.
                    //
                    // The one setting here that changes what other people can
                    // see rather than what this device does, so the words say
                    // what it actually changes: a member can already reach
                    // these services by connecting to the address, and this is
                    // about whether the names are listed for them (ADR-023).
                    RowLayout {
                        spacing: 8
                        Layout.fillWidth: true
                        Text {
                            text: "announce"
                            color: cAsh
                            font.family: "monospace"; font.pixelSize: 11
                            Layout.preferredWidth: 70
                        }
                        Text {
                            text: "list the names for peers"
                            color: cPhosphor
                            font.family: "monospace"; font.pixelSize: 11
                            MouseArea {
                                anchors.fill: parent
                                cursorShape: Qt.PointingHandCursor
                                onClicked: root.callWrite("setAnnounceServices", [true])
                            }
                        }
                        Text {
                            text: "keep them to myself"
                            color: cAsh
                            font.family: "monospace"; font.pixelSize: 11
                            MouseArea {
                                anchors.fill: parent
                                cursorShape: Qt.PointingHandCursor
                                onClicked: root.callWrite("setAnnounceServices", [false])
                            }
                        }
                        Item { Layout.fillWidth: true }
                    }

                    // Light or relay node. Two words rather than a switch,
                    // because what it costs is the part worth reading.
                    RowLayout {
                        spacing: 8
                        Layout.fillWidth: true
                        Text {
                            text: "node"
                            color: cAsh
                            font.family: "monospace"; font.pixelSize: 11
                            Layout.preferredWidth: 70
                        }
                        Text {
                            text: "light  ~3 MB/h"
                            color: cPhosphor
                            font.family: "monospace"; font.pixelSize: 11
                            MouseArea {
                                anchors.fill: parent
                                cursorShape: Qt.PointingHandCursor
                                onClicked: root.callWrite("setMode", ["Edge"])
                            }
                        }
                        Text {
                            text: "relay  ~20 MB/h"
                            color: cAmber
                            font.family: "monospace"; font.pixelSize: 11
                            MouseArea {
                                anchors.fill: parent
                                cursorShape: Qt.PointingHandCursor
                                onClicked: root.callWrite("setMode", ["Core"])
                            }
                        }
                        Item { Layout.fillWidth: true }
                    }

                    // One row per mesh, when there is more than one: switching
                    // a mesh off is not leaving it, and both are here because
                    // both otherwise mean a terminal.
                    Repeater {
                        model: (root.st.meshes && root.st.meshes.length > 1) ? root.st.meshes : []
                        delegate: RowLayout {
                            spacing: 8
                            Layout.fillWidth: true
                            Text {
                                text: "mesh"
                                color: cAsh
                                font.family: "monospace"; font.pixelSize: 11
                                Layout.preferredWidth: 70
                            }
                            Text {
                                // Defensive because this view runs against
                                // daemons it was not built with: a mesh entry
                                // without a label is malformed, and rendering
                                // "?" says so where an undefined binding just
                                // logs at the console nobody is reading.
                                text: modelData.label || "?"
                                color: cBone
                                font.family: "monospace"; font.pixelSize: 11
                                Layout.preferredWidth: 90
                            }
                            Text {
                                text: "on"
                                color: cPhosphor
                                font.family: "monospace"; font.pixelSize: 11
                                MouseArea {
                                    anchors.fill: parent
                                    cursorShape: Qt.PointingHandCursor
                                    onClicked: root.callWrite("setMeshEnabled",
                                                              [modelData.label, true])
                                }
                            }
                            Text {
                                text: "off"
                                color: cAsh
                                font.family: "monospace"; font.pixelSize: 11
                                MouseArea {
                                    anchors.fill: parent
                                    cursorShape: Qt.PointingHandCursor
                                    onClicked: root.callWrite("setMeshEnabled",
                                                              [modelData.label, false])
                                }
                            }
                            Text {
                                // Two clicks, because this one cannot be
                                // undone from here: coming back needs a new
                                // invite from somebody who is already in.
                                property bool armed: false
                                text: armed ? "sure?" : "leave"
                                color: armed ? cAmber : cRust
                                font.family: "monospace"; font.pixelSize: 11
                                MouseArea {
                                    anchors.fill: parent
                                    cursorShape: Qt.PointingHandCursor
                                    onClicked: {
                                        if (!parent.armed) { parent.armed = true; return }
                                        parent.armed = false
                                        root.callWrite("leaveMesh", [modelData.label])
                                    }
                                }
                            }
                            Item { Layout.fillWidth: true }
                        }
                    }

                    RowLayout {
                        spacing: 8
                        Layout.fillWidth: true
                        Text {
                            text: "apply"
                            color: cAsh
                            font.family: "monospace"; font.pixelSize: 11
                            Layout.preferredWidth: 70
                        }
                        Text {
                            // Says what it does and what it cannot: services
                            // change under a running daemon, a mesh coming or
                            // going does not.
                            text: "reload  ·  applies services; a mesh needs a restart"
                            color: cPhosphor
                            font.family: "monospace"; font.pixelSize: 11
                            MouseArea {
                                anchors.fill: parent
                                cursorShape: Qt.PointingHandCursor
                                onClicked: root.callWrite("reload", [])
                            }
                        }
                        Item { Layout.fillWidth: true }
                    }

                    Text {
                        text: "adding or removing a device needs the admin key, so it stays in `shrooms invite`"
                        color: cAsh
                        font.family: "monospace"; font.pixelSize: 10
                        wrapMode: Text.WordWrap
                        Layout.fillWidth: true
                    }
                }
            }
        }
    }
}
