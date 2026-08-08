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
    property string statusPath: "/run/logos-vpn/status.json"
    property string statusUrl: "http://127.0.0.1:8787/status"
    property bool fileBlocked: false
    property bool everLoaded: false
    property int attempts: 0

    function reload() {
        attempts++
        // Escalate on elapsed attempts rather than on an error.
        //
        // When QML refuses a file:// read it does not deliver a failed request
        // — the handler simply never runs — so waiting for an error waits
        // forever. After two fruitless tries, assume the file is unreachable
        // for whatever reason and use the endpoint instead.
        if (!everLoaded && !fileBlocked && attempts > 2) fileBlocked = true
        fetch(fileBlocked ? statusUrl : "file://" + statusPath, !fileBlocked)
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
            Rectangle {
                Layout.fillWidth: true
                Layout.preferredHeight: Math.max(220, root.height * 0.38)
                color: cPanel
                radius: 10

                Canvas {
                    id: graph
                    anchors.fill: parent
                    anchors.margins: 12

                    // Repaint only when the shape could have changed; a canvas
                    // redrawing on a timer for a static mesh is wasted work.
                    property string shape: JSON.stringify(root.peers.map(function(p) {
                        return [p.name, p.live, p.online, p.relayed, p.relay]
                    }))
                    onShapeChanged: requestPaint()
                    onWidthChanged: requestPaint()
                    onHeightChanged: requestPaint()

                    onPaint: {
                        var ctx = getContext("2d")
                        ctx.reset()

                        var cx = width / 2, cy = height / 2
                        var radius = Math.min(width, height) / 2 * 0.66
                        var n = root.peers.length
                        if (n === 0) {
                            ctx.fillStyle = root.cAsh
                            ctx.font = "11px monospace"
                            ctx.textAlign = "center"
                            ctx.fillText("no peers yet", cx, cy)
                            return
                        }

                        function nodeAt(i) {
                            var a = -Math.PI / 2 + 2 * Math.PI * i / n
                            return { x: cx + radius * Math.cos(a), y: cy + radius * Math.sin(a) }
                        }
                        // A relay is drawn where traffic through it passes, so
                        // relayed links visibly bend around it instead of
                        // crossing the middle as if they were direct.
                        var relayIdx = -1
                        for (var k = 0; k < n; k++) if (root.peers[k].relay) { relayIdx = k; break }

                        for (var i = 0; i < n; i++) {
                            var p = root.peers[i], at = nodeAt(i)
                            ctx.lineWidth = 2
                            ctx.strokeStyle = root.colourOf(p)
                            ctx.globalAlpha = p.live ? 0.8 : 0.35
                            ctx.setLineDash(p.live && !p.relayed ? [] : [6, 6])
                            ctx.beginPath()
                            if (p.live && p.relayed && relayIdx >= 0 && relayIdx !== i) {
                                var via = nodeAt(relayIdx)
                                ctx.moveTo(cx, cy); ctx.lineTo(via.x, via.y); ctx.lineTo(at.x, at.y)
                            } else {
                                ctx.moveTo(cx, cy); ctx.lineTo(at.x, at.y)
                            }
                            ctx.stroke()
                        }
                        ctx.globalAlpha = 1
                        ctx.setLineDash([])

                        for (i = 0; i < n; i++) {
                            p = root.peers[i]; at = nodeAt(i)
                            var col = root.colourOf(p)
                            // Punch the background out so links do not run
                            // under the node or its label.
                            ctx.fillStyle = root.cPanel
                            ctx.beginPath(); ctx.arc(at.x, at.y, 20, 0, 2 * Math.PI); ctx.fill()
                            ctx.fillStyle = col
                            ctx.beginPath(); ctx.arc(at.x, at.y, 6, 0, 2 * Math.PI); ctx.fill()
                            if (p.relay) {
                                ctx.strokeStyle = root.cViolet; ctx.lineWidth = 1.5
                                ctx.beginPath(); ctx.arc(at.x, at.y, 15, 0, 2 * Math.PI); ctx.stroke()
                            }
                            ctx.fillStyle = col
                            ctx.font = "10px monospace"
                            ctx.textAlign = "center"
                            ctx.fillText(p.name || "?", at.x, at.y + 32)
                        }

                        // This device last: everything else is drawn relative
                        // to it. One node's view, not a map of the mesh — no
                        // node knows how any two others reach each other.
                        ctx.fillStyle = root.cVoid
                        ctx.beginPath(); ctx.arc(cx, cy, 22, 0, 2 * Math.PI); ctx.fill()
                        ctx.fillStyle = root.cBone
                        ctx.beginPath(); ctx.arc(cx, cy, 11, 0, 2 * Math.PI); ctx.fill()
                    }
                }
            }

            // --- list -------------------------------------------------------
            ListView {
                Layout.fillWidth: true
                Layout.fillHeight: true
                clip: true
                spacing: 5
                model: root.peers

                delegate: Rectangle {
                    width: ListView.view.width
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
                                color: root.colourOf(modelData)
                            }
                            Text {
                                text: modelData.name || "?"
                                color: cBone
                                font.family: "monospace"; font.pixelSize: 13
                            }
                            Text {
                                visible: modelData.relay === true
                                text: "RELAY"
                                color: cViolet
                                font.family: "monospace"; font.pixelSize: 9
                            }
                            Item { Layout.fillWidth: true }
                            Text {
                                text: modelData.live
                                      ? (modelData.relayed ? "relayed" : "direct")
                                        + (modelData.rtt_ms ? " · " + modelData.rtt_ms + "ms" : "")
                                      : (modelData.online ? "no tunnel" : "offline")
                                color: modelData.relayed ? cViolet : cAsh
                                font.family: "monospace"; font.pixelSize: 11
                            }
                        }

                        Text {
                            text: (modelData.dns_name ? modelData.dns_name + "   " : "")
                                  + (modelData.overlay || "")
                            color: cAsh
                            font.family: "monospace"; font.pixelSize: 10
                            elide: Text.ElideRight
                            Layout.fillWidth: true
                        }

                        RowLayout {
                            spacing: 16
                            visible: modelData.live === true
                            Text {
                                text: "↓ " + root.rate(modelData.rx_bps)
                                      + "   ↑ " + root.rate(modelData.tx_bps)
                                color: cAsh
                                font.family: "monospace"; font.pixelSize: 10
                            }
                            Text {
                                visible: modelData.tunnel_after_s > 0
                                text: "connected in " + modelData.tunnel_after_s.toFixed(1) + "s"
                                color: cAsh
                                font.family: "monospace"; font.pixelSize: 10
                            }
                            Text {
                                visible: modelData.handshake_age_s > 0
                                // Shown always, never a bare "up": a peer that
                                // restarted leaves the other side holding a
                                // session that stays valid for a while.
                                text: "handshake " + root.since(modelData.handshake_age_s) + " ago"
                                color: cAsh
                                font.family: "monospace"; font.pixelSize: 10
                            }
                        }
                    }
                }
            }
        }
    }
}
