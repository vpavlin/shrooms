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
        statusUrl: "http://127.0.0.1:8787/status.json"
    }

    Timer {
        interval: 4000     // long enough for the file->endpoint escalation
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
            Qt.quit()
        }
    }
}
