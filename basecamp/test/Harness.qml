import QtQuick

// Loads the view offscreen against a fixture and asserts what it understood.
//
// Exists because "the QML looks right" is not a test: the first version loaded
// cleanly and silently failed to read anything, and a null dereference in the
// trouble banner only appeared once something actually rendered it.
Item {
    width: 900; height: 700

    property string fixture: Qt.application.arguments[Qt.application.arguments.length - 1]

    Main {
        id: view
        anchors.fill: parent
        statusPath: fixture
        // Deliberately not "status.json": that name is also the sibling file
        // the view tries first, so serving it here would let source 0 satisfy
        // the endpoint test and the escalation would never be exercised.
        statusUrl: "http://127.0.0.1:8787/endpoint.json"
    }

    Timer {
        interval: 7000     // long enough to walk all three sources in order
        running: true
        onTriggered: {
            console.error("PEERS=" + view.peers.length
                + " SELF=" + (view.st.name || "?")
                + " FILEBLOCKED=" + view.fileBlocked
                + " PROBLEM=[" + view.problem + "]")
            for (var i = 0; i < view.peers.length; i++) {
                var p = view.peers[i]
                console.error("  " + p.name
                    + " live=" + p.live
                    + " relayed=" + p.relayed
                    + " colour=" + view.colourOf(p)
                    + " rate=" + view.rate(p.rx_bps))
            }

            // The sections that are not the roster, exercised through the same
            // derivations the delegates bind to. A Repeater over an empty model
            // renders without complaint, so "it loaded" says nothing at all
            // about whether these read the payload correctly.
            console.error("VERSION=" + (view.st.version || "?")
                + " DNS=" + (view.st.dns ? (view.st.dns.registered ? "resolving" : "partial")
                                         : "absent"))
            console.error("SERVICES=" + view.allServices.length
                + " ROWS=" + view.serviceRows.length)
            for (var j = 0; j < view.allServices.length; j++) {
                var s = view.allServices[j]
                console.error("  " + s.device + " " + s.label
                    + " " + s.addr + (s.bound ? " bound" : ""))
            }
            for (var k = 0; k < view.peers.length; k++) {
                console.error("  membership " + view.peers[k].name + " "
                    + view.membershipText(view.peers[k].expires)
                    + " " + view.membershipColour(view.peers[k].expires))
            }
            // The settings section, which showed every option in a fixed
            // colour and so never said which one was in force — "on" was lit
            // beside a mesh that was switched off, and read as its state.
            console.error("SWITCHABLE=" + view.switchableMeshes.length
                + " RUNNING=" + view.runningMeshes.length)
            for (var m = 0; m < view.switchableMeshes.length; m++) {
                var mm = view.switchableMeshes[m]
                console.error("  mesh " + mm.label
                    + " on=" + (mm.disabled !== true)
                    + " lit=" + view.pick(mm.disabled !== true)
                    + " primary=" + view.isPrimary(mm)
                    + " state=[" + view.meshState(mm) + "]")
            }
            console.error("MODE=" + view.st.mode + " RUNNING=" + view.st.mode_running
                + " ANNOUNCE=" + (view.primaryMesh.announce_services === true))

            Qt.quit()
        }
    }
}
