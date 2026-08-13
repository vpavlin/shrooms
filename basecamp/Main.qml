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

    // Which sections are unrolled. All shut by default: this view's job is to
    // show a mesh, and four open forms above the roster is a control panel
    // that happens to draw a graph.
    property bool settingsOpen: false
    property bool servicesOpen: false
    property bool membersOpen: false
    property bool logsOpen: false
    property bool dnsOpen: false

    // --- the log tail -------------------------------------------------------
    //
    // The same pane the Android app has had from the beginning, for the reason
    // it has had it: when a mesh does not come up, the log is the only thing
    // that says why, and on the desktop the alternative is journalctl in a
    // terminal — which is the terminal these controls exist to avoid.
    //
    // Polled incrementally. `since` is the stamp of the newest line already
    // held, so each poll carries what happened in the last two seconds rather
    // than re-sending two hundred lines and re-rendering the pane.
    property var logLines: []
    property string logSince: "0"
    readonly property int logKeep: 200

    // Why there is no log, when there is no log.
    //
    // This pane silently showed nothing against a daemon too old to have the
    // endpoint, which is the exact failure this whole feature exists to stop:
    // an empty box is indistinguishable from a quiet daemon, and the one thing
    // a diagnostic must never do is fail quietly. So every reason the lines did
    // not arrive is kept and rendered in their place.
    property string logProblem: ""

    function pumpLogs() {
        if (!logsOpen || !haveCore) return
        var t = String(callCore("logs", [root.logSince]) || "").trim()
        for (var i = 0; i < 2 && t.charAt(0) === '"'; i++) {
            try { t = String(JSON.parse(t)).trim() } catch (e) { break }
        }
        var d = null
        try { d = JSON.parse(t) } catch (e) { d = null }
        if (!d) {
            root.logProblem = "the module returned nothing readable"
            return
        }
        if (d.error) {
            // A 404 here has one cause and one fix, and the daemon's own
            // wording for it ("404 page not found") says neither. Named,
            // because "cannot read the daemon" sends somebody to check whether
            // it is running — and it plainly is, since everything above this
            // pane is being drawn from it.
            var detail = String(d.detail || "")
            root.logProblem = detail.indexOf("404") >= 0
                ? "this daemon has no log endpoint — it predates the pane. "
                  + "Update the daemon and restart it; everything else here "
                  + "keeps working meanwhile."
                : d.error + (detail ? " — " + detail : "")
            return
        }
        root.logProblem = ""
        if (!d.lines || !d.lines.length) return

        var out = root.logLines.concat(d.lines)
        // Bounded here as well as in the daemon: the daemon caps what it
        // holds, this caps what has been streamed across since the pane was
        // opened, which is a different and unbounded quantity.
        if (out.length > root.logKeep) out = out.slice(out.length - root.logKeep)
        root.logLines = out
        root.logSince = String(d.lines[d.lines.length - 1].t)
    }

    function levelColour(l) {
        if (l === "ERROR") return cRust
        if (l === "WARN") return cAmber
        if (l === "DEBUG") return cAsh
        return cBone
    }

    /** A log line's stamp as "12s ago", which is what the pane is read for. */
    function ago(ms) {
        var s = Math.max(0, (Date.now() - ms) / 1000)
        if (s < 60) return Math.round(s) + "s"
        if (s < 3600) return Math.round(s / 60) + "m"
        return Math.round(s / 3600) + "h"
    }

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
        // Not every endpoint answers in JSON, and assuming they all did is why
        // `reload` reported "the daemon said nothing readable" after every
        // successful reload since this form was built. /reload answers a plain
        // sentence — `reloaded: 1 mesh(es) republished services` — and the
        // module only returns a body at all when the daemon answered 2xx. So a
        // body that is not JSON is a success whose message is the body.
        //
        // The failure shape is unambiguous: the module builds its own
        // {"error":…} document, so anything starting with a brace is
        // structured and anything else is the daemon talking.
        var d = null
        if (t.charAt(0) === "{") {
            try { d = JSON.parse(t) } catch (e) { d = null }
        }
        if (!d && t !== "") {
            root.said = t
            root.saidBad = false
            root.reload()
            return
        }
        if (!d) {
            // Nothing at all came back. The module returns "" when the method
            // does not exist on it — an out-of-date shrooms_core — and the
            // view has no way to ask which methods it has, so that is named.
            root.said = "no answer from shrooms_core. If this control is new, "
                      + "update the shrooms_core module in Basecamp as well as "
                      + "this view — they ship separately and the view cannot "
                      + "tell which version is installed."
            root.saidBad = true
        } else if (d.error) {
            // A 404 means the daemon does not have the endpoint, which is a
            // version problem and not a fault. "the daemon answered 404" reads
            // as a bug in the app; it is a daemon that predates the button.
            var detail = String(d.detail || "")
            root.said = detail.indexOf("404") >= 0
                ? "this daemon is older than that control — update the daemon "
                  + "and restart it (sudo systemctl restart shrooms). "
                  + "Everything else here keeps working meanwhile."
                : d.error + (detail ? " — " + detail : "")
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
        onTriggered: {
            root.reload()
            root.pumpLogs()
        }
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
            // A mesh that is switched off has no peers, no address and no
            // tunnels, so a colour and a legend entry for it would decorate
            // nothing. It appears in the settings list below, which is where
            // it can be switched back on.
            if (ms[i] && ms[i].disabled === true) continue
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

    /** The meshes actually running, which is what the graph and rosters show. */
    readonly property var runningMeshes: {
        var out = [], ms = (root.st && root.st.meshes) ? root.st.meshes : []
        for (var i = 0; i < ms.length; i++) {
            if (ms[i] && ms[i].disabled !== true) out.push(ms[i])
        }
        return out
    }

    /**
     * Every mesh this device belongs to, running or not.
     *
     * The settings list binds to this rather than to the running ones, because
     * a mesh that is switched off is invisible in every other view by
     * definition — and a switch you cannot see is a switch you cannot flip
     * back. That is how a mesh went missing: off, and then absent from the one
     * list that could have turned it on.
     *
     * Shown from one entry rather than two. A device with a single running
     * mesh and one switched off needs both rows, and the "only when there is
     * more than one" rule that governs the summary above would hide exactly
     * the row that matters.
     */
    readonly property var switchableMeshes: {
        var ms = (root.st && root.st.meshes) ? root.st.meshes : []
        if (ms.length === 0) return []
        var anyOff = false
        for (var i = 0; i < ms.length; i++) if (ms[i] && ms[i].disabled === true) anyOff = true
        return (ms.length > 1 || anyOff) ? ms : []
    }

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
     * Everything reachable on any mesh, as one list.
     *
     * Grouped by mesh rather than by the device offering it, which is a
     * deliberate difference from the roster above: a service is a thing you
     * want to open, and the question in front of it is "which of my networks
     * is this on" far more often than "which box is it running on". The device
     * is still shown, because it is in the address either way.
     *
     * Two kinds in one list. An announced service (ADR-023) has a name of its
     * own and is reached at <service>.<device>; a bound port (ADR-026) has no
     * name and is reached at <device>:<port> — no forwarder involved, the
     * process is simply listening on the mesh address. Marked as such, because
     * one of them is a URL you can click and the other is a host and a port.
     */
    readonly property var allServices: {
        var out = []
        for (var i = 0; i < root.sortedPeers.length; i++) {
            var p = root.sortedPeers[i]
            var host = p.dns_name || p.name || ""
            var s = p.services || []
            for (var j = 0; j < s.length; j++) {
                out.push({ mesh: p.mesh || "", device: p.name || "?", live: p.live === true,
                           label: s[j], addr: "http://" + s[j] + "." + host, bound: false })
            }
            var b = p.bound || []
            for (var k = 0; k < b.length; k++) {
                // "ssh:22" — the name is advisory and the port is the fact, so
                // the port is what goes in the address.
                var parts = String(b[k]).split(":")
                var port = parts.length > 1 ? parts[parts.length - 1] : ""
                out.push({ mesh: p.mesh || "", device: p.name || "?", live: p.live === true,
                           label: parts[0], addr: host + (port ? ":" + port : ""), bound: true })
            }
        }
        return out
    }

    /** Rows for the services list: a heading per mesh, then its services. */
    readonly property var serviceRows: {
        var out = [], last = null
        var ss = root.allServices.slice()
        ss.sort(function(a, b) {
            if (a.mesh !== b.mesh) return a.mesh < b.mesh ? -1 : 1
            if (a.device !== b.device) return a.device < b.device ? -1 : 1
            return a.label < b.label ? -1 : (a.label > b.label ? 1 : 0)
        })
        for (var i = 0; i < ss.length; i++) {
            if (root.multiMesh && ss[i].mesh !== last) {
                out.push({ header: true, mesh: ss[i].mesh })
                last = ss[i].mesh
            }
            out.push({ header: false, svc: ss[i] })
        }
        return out
    }

    /** Days until a unix-seconds stamp, or 999 when there is none. */
    function daysTo(unix) {
        if (!unix) return 999
        return Math.floor((unix - Date.now() / 1000) / 86400)
    }

    /**
     * How a credential's remaining life reads.
     *
     * "unknown" is a real answer and not a bad one: expiry is learned from
     * what a peer announces, so a peer that has been quiet since this daemon
     * started simply has not said. Rendering that as "expired" would send
     * somebody chasing a renewal nobody needs.
     */
    function membershipText(unix) {
        if (!unix) return "unknown"
        var d = root.daysTo(unix)
        if (d < 0) return "ended"
        if (d === 0) return "ends today"
        return d + "d left"
    }
    function membershipColour(unix) {
        if (!unix) return cAsh
        var d = root.daysTo(unix)
        if (d < 0) return cRust
        if (d <= 10) return cAmber
        return cPhosphor
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
                    Text {
                        // The daemon's build, not this module's. They are
                        // different things and the daemon's is the one that
                        // decides whether a control here exists at all.
                        visible: text !== ""
                        text: root.st.version ? root.st.version : ""
                        color: cAsh
                        font.family: "monospace"; font.pixelSize: 10
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
                model: root.runningMeshes.length > 1 ? root.runningMeshes : []
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

                // Said once, up front, rather than discovered one button at a
                // time. The daemon and this view ship separately and update
                // separately, so a view newer than its daemon is the ordinary
                // case after an update — and every control that the daemon does
                // not have yet fails in its own way, which reads as "it's all
                // weirdly half-broken" rather than as one version skew.
                //
                // Detected by the version field, which arrived in the same
                // daemon as the log and the restart. Its absence is exactly the
                // set of daemons that lack them.
                Text {
                    visible: root.everLoaded && root.st.version === undefined
                    Layout.fillWidth: true
                    wrapMode: Text.WordWrap
                    color: cAmber
                    font.family: "monospace"; font.pixelSize: 10
                    text: "this daemon is older than this view: the log, the restart "
                          + "button and joining a mesh will not answer. Everything else "
                          + "here works. To update it:"
                }
                TextEdit {
                    visible: root.everLoaded && root.st.version === undefined
                    Layout.fillWidth: true
                    readOnly: true
                    selectByMouse: true
                    color: cBone
                    font.family: "monospace"; font.pixelSize: 10
                    text: "sudo install -m755 bin/shrooms /usr/local/bin/shrooms && "
                          + "sudo systemctl restart shrooms"
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
                        // Every mesh, switched off ones included — this list is
                        // the only way back for one that is off, and driving it
                        // from the running meshes is precisely how a mesh
                        // became unreachable from every screen at once.
                        //
                        // Shown from one rather than two: a device with a
                        // single running mesh and one switched off has two
                        // entries here and needs both.
                        model: root.switchableMeshes
                        delegate: RowLayout {
                            spacing: 8
                            Layout.fillWidth: true
                            Text {
                                text: modelData.disabled === true ? "mesh · off" : "mesh"
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

                    // Whether names resolve on this machine, and where.
                    //
                    // Folded away like the log, because the answer is almost
                    // always "yes" and the details matter only when it is not.
                    // Worth having at all because this fails on its own and
                    // silently: port 53 needs a capability and the system
                    // resolver needs telling about the suffix, and either can
                    // be missing while every tunnel is perfect. Then
                    // `ssh laptop.mesh` does not work and nothing else on this
                    // page hints at why.
                    ColumnLayout {
                        id: dnsBlock
                        Layout.fillWidth: true
                        spacing: 4
                        readonly property var d: root.st.dns || ({})

                        // Hidden entirely against a daemon that does not report
                        // this. A missing field is "it did not say", and
                        // rendering that as "not resolving" would put a red
                        // line on a machine whose names work perfectly.
                        visible: root.st.dns !== undefined

                        RowLayout {
                            spacing: 8
                            Layout.fillWidth: true
                            Text {
                                text: "names"
                                color: cAsh
                                font.family: "monospace"; font.pixelSize: 11
                                Layout.preferredWidth: 70
                            }
                            Text {
                                text: dnsBlock.d.registered ? "resolving \u25b8"
                                    : (dnsBlock.d.serving ? "serving, not registered \u25b8"
                                                          : "not resolving \u25b8")
                                color: dnsBlock.d.registered ? cPhosphor
                                     : (dnsBlock.d.serving ? cAmber : cRust)
                                font.family: "monospace"; font.pixelSize: 11
                                MouseArea {
                                    anchors.fill: parent
                                    cursorShape: Qt.PointingHandCursor
                                    onClicked: root.dnsOpen = !root.dnsOpen
                                }
                            }
                            Item { Layout.fillWidth: true }
                        }
                        Text {
                            visible: root.dnsOpen
                            Layout.fillWidth: true
                            Layout.leftMargin: 78
                            wrapMode: Text.WordWrap
                            color: cAsh
                            font.family: "monospace"; font.pixelSize: 10
                            text: {
                                var d = dnsBlock.d, out = []
                                if (d.address) out.push("served on " + d.address)
                                if (d.suffix) out.push("suffix ." + d.suffix)
                                out.push(d.registered
                                         ? "the host sends " + (d.suffix ? "." + d.suffix : "mesh names") + " here"
                                         : "the host has not been told to send names here, so only "
                                           + "a direct query to the address resolves")
                                if (d.err) out.push(d.err)
                                return out.join("\n")
                            }
                        }
                    }

                    RowLayout {
                        spacing: 12
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
                            text: "reload  ·  services only"
                            color: cPhosphor
                            font.family: "monospace"; font.pixelSize: 11
                            MouseArea {
                                anchors.fill: parent
                                cursorShape: Qt.PointingHandCursor
                                onClicked: root.callWrite("reload", [])
                            }
                        }
                        Text {
                            // The other half of every "on the next restart"
                            // above. Armed with a second click because it
                            // drops every tunnel for a few seconds, which is
                            // not much and is not nothing if somebody is
                            // copying a file over one.
                            property bool armed: false
                            text: armed ? "sure? every tunnel drops for a few seconds"
                                        : "restart  ·  applies the rest"
                            color: armed ? cAmber : cPhosphor
                            font.family: "monospace"; font.pixelSize: 11
                            MouseArea {
                                anchors.fill: parent
                                cursorShape: Qt.PointingHandCursor
                                onClicked: {
                                    if (!parent.armed) { parent.armed = true; return }
                                    parent.armed = false
                                    root.callWrite("restart", [])
                                }
                            }
                        }
                        Item { Layout.fillWidth: true }
                    }
                }
            }

            // --- services ---------------------------------------------------
            //
            // What everything on every mesh offers, which the roster shows one
            // peer at a time and this shows as a list you can read down. Both
            // are worth having: the roster answers "is that box reachable",
            // this answers "where do I find Immich".
            ColumnLayout {
                Layout.fillWidth: true
                spacing: 8

                Text {
                    text: (root.servicesOpen ? "services ▾  " : "services ▸  ")
                          + root.allServices.length
                    color: cPhosphor
                    font.family: "monospace"; font.pixelSize: 11
                    MouseArea {
                        anchors.fill: parent
                        cursorShape: Qt.PointingHandCursor
                        onClicked: root.servicesOpen = !root.servicesOpen
                    }
                }

                ColumnLayout {
                    visible: root.servicesOpen
                    Layout.fillWidth: true
                    spacing: 6

                    Text {
                        visible: root.allServices.length === 0
                        Layout.fillWidth: true
                        wrapMode: Text.WordWrap
                        color: cAsh
                        font.family: "monospace"; font.pixelSize: 10
                        // Says which of the two reasons it is, because they
                        // have opposite fixes and look identical from here.
                        text: "nothing announced. A peer publishes services and lists them "
                              + "separately — a device that has not been told to announce "
                              + "offers exactly what it always did, silently."
                    }

                    Repeater {
                        model: root.serviceRows
                        delegate: Item {
                            id: svcItem
                            Layout.fillWidth: true
                            implicitHeight: modelData.header === true
                                            ? svcHead.implicitHeight + 8
                                            : svcRow.implicitHeight + 4

                            // Named rather than reached through parent.parent:
                            // a delegate that walks its own tree breaks the
                            // day anything is wrapped in a layout, and it
                            // breaks as a binding loop rather than as an
                            // error.
                            readonly property var s: modelData.svc || ({})

                            Text {
                                id: svcHead
                                visible: modelData.header === true
                                anchors.left: parent.left
                                anchors.bottom: parent.bottom
                                text: modelData.mesh ? modelData.mesh : "no mesh label"
                                color: root.meshTint(modelData.mesh)
                                font.family: "monospace"; font.pixelSize: 11
                            }

                            RowLayout {
                                id: svcRow
                                visible: modelData.header !== true
                                width: parent.width
                                spacing: 10

                                Rectangle {
                                    width: 6; height: 6; radius: 3
                                    color: svcItem.s.live ? cPhosphor : cAsh
                                }
                                Text {
                                    text: svcItem.s.label || ""
                                    color: cBone
                                    font.family: "monospace"; font.pixelSize: 11
                                    Layout.preferredWidth: 110
                                    elide: Text.ElideRight
                                }
                                TextEdit {
                                    // Selectable, because the whole point is to
                                    // get this address into something else.
                                    text: svcItem.s.addr || ""
                                    readOnly: true
                                    selectByMouse: true
                                    color: svcItem.s.live ? cPhosphor : cAsh
                                    font.family: "monospace"; font.pixelSize: 10
                                    Layout.fillWidth: true
                                }
                                Text {
                                    // A bound port is not a forwarded service:
                                    // the process is listening on the mesh
                                    // address itself, so it is a host and a
                                    // port rather than a URL (ADR-026).
                                    visible: svcItem.s.bound === true
                                    text: "bound"
                                    color: cViolet
                                    font.family: "monospace"; font.pixelSize: 9
                                }
                                Text {
                                    text: svcItem.s.device || ""
                                    color: cAsh
                                    font.family: "monospace"; font.pixelSize: 10
                                }
                            }
                        }
                    }
                }
            }

            // --- membership -------------------------------------------------
            //
            // Who is in, until when, and how somebody else gets in.
            //
            // Worth its own section because credentials expiring is the one
            // failure in this system that is scheduled: it happens on a known
            // day, it takes a device off the mesh, and nothing else here hints
            // at it until the device is gone.
            ColumnLayout {
                Layout.fillWidth: true
                spacing: 8
                visible: root.haveCore

                Text {
                    text: root.membersOpen ? "membership ▾" : "membership ▸"
                    color: cPhosphor
                    font.family: "monospace"; font.pixelSize: 11
                    MouseArea {
                        anchors.fill: parent
                        cursorShape: Qt.PointingHandCursor
                        onClicked: root.membersOpen = !root.membersOpen
                    }
                }

                ColumnLayout {
                    visible: root.membersOpen
                    Layout.fillWidth: true
                    spacing: 8

                    // This device, per mesh, first: it is the one whose expiry
                    // nobody else will warn you about.
                    Repeater {
                        model: root.st.meshes || []
                        delegate: RowLayout {
                            Layout.fillWidth: true
                            spacing: 10
                            Text {
                                text: "this device"
                                color: cAsh
                                font.family: "monospace"; font.pixelSize: 11
                                Layout.preferredWidth: 90
                            }
                            Text {
                                text: modelData.label || "?"
                                color: root.meshTint(modelData.label)
                                font.family: "monospace"; font.pixelSize: 11
                                Layout.preferredWidth: 90
                            }
                            Text {
                                text: root.membershipText(modelData.expires)
                                color: root.membershipColour(modelData.expires)
                                font.family: "monospace"; font.pixelSize: 11
                            }
                            Item { Layout.fillWidth: true }
                        }
                    }

                    Repeater {
                        model: root.sortedPeers
                        delegate: RowLayout {
                            Layout.fillWidth: true
                            spacing: 10
                            Text {
                                text: modelData.name || "?"
                                color: cBone
                                font.family: "monospace"; font.pixelSize: 11
                                Layout.preferredWidth: 90
                                elide: Text.ElideRight
                            }
                            Text {
                                visible: root.multiMesh
                                text: modelData.mesh || ""
                                color: root.meshTint(modelData.mesh || "")
                                font.family: "monospace"; font.pixelSize: 11
                                Layout.preferredWidth: 90
                            }
                            Text {
                                text: root.membershipText(modelData.expires)
                                color: root.membershipColour(modelData.expires)
                                font.family: "monospace"; font.pixelSize: 11
                            }
                            Item { Layout.fillWidth: true }
                        }
                    }

                    // Joining another mesh. The token comes from whoever is
                    // running `shrooms invite` at the far end, right now — the
                    // exchange is live, so this waits for them and can take a
                    // couple of minutes.
                    RowLayout {
                        spacing: 8
                        Layout.fillWidth: true
                        Text {
                            text: "join"
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
                                id: tokenField
                                anchors.fill: parent
                                anchors.leftMargin: 6
                                verticalAlignment: TextInput.AlignVCenter
                                color: cBone
                                font.family: "monospace"; font.pixelSize: 11
                                selectByMouse: true
                            }
                            // A label rather than a placeholder property,
                            // which TextInput does not have. Shown only while
                            // the field is empty.
                            //
                            // Not echoed as dots: a token admits one device
                            // once and is useless afterwards, and hiding it
                            // would only stop somebody checking they pasted
                            // the whole thing.
                            Text {
                                visible: tokenField.text === ""
                                anchors.left: parent.left
                                anchors.leftMargin: 6
                                anchors.verticalCenter: parent.verticalCenter
                                text: "invite token"
                                color: cAsh
                                font.family: "monospace"; font.pixelSize: 11
                            }
                        }
                        Rectangle {
                            Layout.preferredWidth: 110
                            height: 26
                            color: cPanel
                            border.color: cLine
                            TextInput {
                                id: labelField
                                anchors.fill: parent
                                anchors.leftMargin: 6
                                verticalAlignment: TextInput.AlignVCenter
                                color: cBone
                                font.family: "monospace"; font.pixelSize: 11
                                selectByMouse: true
                            }
                            Text {
                                visible: labelField.text === ""
                                anchors.left: parent.left
                                anchors.leftMargin: 6
                                anchors.verticalCenter: parent.verticalCenter
                                text: "label"
                                color: cAsh
                                font.family: "monospace"; font.pixelSize: 11
                            }
                        }
                        Text {
                            text: "join"
                            color: cPhosphor
                            font.family: "monospace"; font.pixelSize: 11
                            MouseArea {
                                anchors.fill: parent
                                cursorShape: Qt.PointingHandCursor
                                onClicked: {
                                    root.said = "redeeming — the far side has to be "
                                              + "running `shrooms invite` right now"
                                    root.saidBad = false
                                    root.callWrite("joinWithInvite",
                                                   [tokenField.text, root.st.name || "",
                                                    labelField.text])
                                }
                            }
                        }
                    }
                    Text {
                        Layout.fillWidth: true
                        Layout.leftMargin: 78
                        wrapMode: Text.WordWrap
                        color: cAsh
                        font.family: "monospace"; font.pixelSize: 10
                        text: "the second box is the local label — what this device will call "
                              + "that mesh, as in laptop.<label>.mesh. A joined mesh starts on "
                              + "the next restart."
                    }

                    // The honest limit, stated where somebody would look for
                    // the button. Issuing an invite means signing a credential
                    // with the admin key, and the daemon has never held it —
                    // that separation is what keeps handing out this socket a
                    // bounded grant rather than a way to admit anybody.
                    Text {
                        Layout.fillWidth: true
                        wrapMode: Text.WordWrap
                        color: cAsh
                        font.family: "monospace"; font.pixelSize: 10
                        text: "inviting somebody, and revoking them, needs the admin key, which "
                              + "this daemon deliberately does not hold:"
                    }
                    TextEdit {
                        Layout.fillWidth: true
                        readOnly: true
                        selectByMouse: true
                        color: cBone
                        font.family: "monospace"; font.pixelSize: 10
                        text: "shrooms invite --name their-laptop"
                    }
                }
            }

            // --- log --------------------------------------------------------
            //
            // The same pane the phone has. Last, because it is the thing you
            // open when everything above has failed to explain itself.
            ColumnLayout {
                Layout.fillWidth: true
                spacing: 6
                visible: root.haveCore

                Text {
                    text: root.logsOpen ? "log ▾" : "log ▸"
                    color: cPhosphor
                    font.family: "monospace"; font.pixelSize: 11
                    MouseArea {
                        anchors.fill: parent
                        cursorShape: Qt.PointingHandCursor
                        onClicked: {
                            root.logsOpen = !root.logsOpen
                            // Fetched immediately on opening rather than at the
                            // next tick: two seconds of an empty box reads as
                            // "there is no log".
                            if (root.logsOpen) root.pumpLogs()
                        }
                    }
                }

                Rectangle {
                    visible: root.logsOpen
                    Layout.fillWidth: true
                    Layout.preferredHeight: 220
                    color: cPanel
                    radius: 8

                    ListView {
                        id: logView
                        anchors.fill: parent
                        anchors.margins: 8
                        clip: true
                        spacing: 2
                        model: root.logLines

                        // Follow the tail, but only while the reader is already
                        // at the bottom: yanking the view down under somebody
                        // who has scrolled up to read something is the single
                        // most annoying thing a log pane can do.
                        property bool atEnd: true
                        onContentYChanged: atEnd = (contentY + height >= contentHeight - 24)
                        onCountChanged: if (atEnd) positionViewAtEnd()

                        delegate: RowLayout {
                            width: ListView.view.width
                            spacing: 8
                            Text {
                                text: root.ago(modelData.t)
                                color: cAsh
                                font.family: "monospace"; font.pixelSize: 9
                                Layout.preferredWidth: 34
                                horizontalAlignment: Text.AlignRight
                            }
                            Text {
                                text: modelData.msg || ""
                                color: root.levelColour(modelData.level)
                                font.family: "monospace"; font.pixelSize: 10
                                Layout.preferredWidth: 200
                                elide: Text.ElideRight
                            }
                            Text {
                                text: modelData.attrs || ""
                                color: cAsh
                                font.family: "monospace"; font.pixelSize: 10
                                elide: Text.ElideRight
                                Layout.fillWidth: true
                            }
                        }
                    }

                    Text {
                        visible: root.logLines.length === 0
                        anchors.fill: parent
                        anchors.margins: 16
                        horizontalAlignment: Text.AlignHCenter
                        verticalAlignment: Text.AlignVCenter
                        wrapMode: Text.WordWrap
                        text: root.logProblem !== "" ? root.logProblem
                                                     : "nothing logged since this pane was opened"
                        color: root.logProblem !== "" ? cAmber : cAsh
                        font.family: "monospace"; font.pixelSize: 10
                    }
                }
            }
        }
    }
}
