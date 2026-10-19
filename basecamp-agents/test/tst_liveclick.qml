import QtQuick
import QtTest

// Live-click test for the Tasks panel's answer/nudge/cancel/ack controls
// (reviewer task cli-20261019T011233). Drives the REAL, unmodified Main.qml
// with real, hit-tested synthetic clicks (TestCase.mouseClick) - this goes
// through Qt's actual input/event pipeline, the same path a real finger or
// mouse takes, so a MouseArea that happens to sit above a button would
// actually swallow the click here too. It does not call handler functions
// directly, and it does not read the source to decide pass/fail.
//
// A fresh Main instance per scenario (Loader, active toggled off/on): reusing
// one instance across several agentHosts assignments, plus going through the
// Repeater.itemAt() API rather than a name-search of the whole tree: rapid
// re-binding of taskPanelList during one setup (agentHosts, then boardMode,
// then showBoard()'s own several steps) left OLDER, already-superseded
// delegates reachable by a plain objectName walk for a short time, each with
// a real but STALE match - a harness artifact (Repeater + a model rebuilt by
// a function on every dependency change), not a finding about the real app,
// which nothing drives this way in normal use.
TestCase {
    id: top
    name: "LiveClick"
    visible: true
    width: 1400
    height: 900
    when: windowShown

    property var calls: []
    property string lastPostPath: ""
    property string lastPost: ""
    property int postCount: 0
    property var currentHosts: []

    QtObject {
        id: stubBridge
        signal moduleEventReceived(string moduleName, string eventName, var data)
        function onModuleEvent(module, name) { return true }
        function callModule(module, method, args) {
            top.calls.push(method)
            if (method === "status") return JSON.stringify({ name: "here", meshes: [], peers: [] })
            if (method === "getPref") return ""
            // The LIVE shape refreshAgents() reads: tasks nested as
            // { tasks: [...] }, not a bare array (AgentsDemo.qml's own
            // fixture is the same shape, for the same reason).
            if (method === "agentsFind") return JSON.stringify(top.currentHosts.map(function(h) {
                return Object.assign({}, h, { list: { sessions: h.sessions || [], limits: null }, tasks: { tasks: h.tasks || [] } })
            }))
            if (method === "agentPost") {
                top.lastPostPath = args[1]
                top.lastPost = args[2]
                top.postCount++
                return JSON.stringify({ ok: true })
            }
            if (method === "agentGet" && String(args[1]) === "/v1/harnesses")
                return JSON.stringify({ harnesses: [{ name: "claude", title: "Claude Code", caps: { approve: true, takeover: true } }] })
            if (method === "agentGet") return JSON.stringify({ history: [] })
            if (method === "agentJobs") return JSON.stringify({ recording: false, jobs: [] })
            if (method === "agentOutbox") return JSON.stringify([])
            if (method === "agentWatch") return JSON.stringify({ ok: true })
            if (method === "agentEvents") return JSON.stringify({ next: 0, more: false, connected: true, error: "", events: [] })
            return JSON.stringify({ ok: true })
        }
    }

    Loader {
        id: viewLoader
        anchors.fill: parent
        active: false
        sourceComponent: Main { bridge: stubBridge }
    }

    // QtTest's own findChild() does not reach into ScrollView's internal
    // content tree here (checked: it returns null past it) - manual walk
    // instead, the same workaround basecamp-agents/test/AgentsHarness.qml
    // already uses for the same reason. Scoped to a SINGLE delegate's subtree
    // by every caller below, so the staleness artifact above cannot apply:
    // an old, superseded delegate is not inside the CURRENT one's children.
    function byName(item, targetName) {
        if (!item) return null
        if (item.objectName === targetName) return item
        var kids = item.children || []
        for (var i = 0; i < kids.length; i++) {
            var f = byName(kids[i], targetName)
            if (f) return f
        }
        if (item.contentItem) {
            var f2 = byName(item.contentItem, targetName)
            if (f2) return f2
        }
        if (item.contentChildren) {
            for (var j = 0; j < item.contentChildren.length; j++) {
                var f3 = byName(item.contentChildren[j], targetName)
                if (f3) return f3
            }
        }
        return null
    }

    function hostsWith(id, session, from, state, extra) {
        // `mesh` is not decoration: a host on the wire always carries it
        // (AgentsHarness.qml builds its hosts the same way), and the session
        // list reads it for the mesh tint and the mesh label. Leaving it out
        // made Main.qml assign `undefined` to a QString at line 4471 on every
        // scenario, and meant the mesh-label path was never exercised at all.
        return [{ name: "laptop", mesh: "office", address: "fd00::1", sessions: [{ name: session }],
                  tasks: [{ id: id, status: { state: state, timestamp: "2026-10-18T11:00:00Z",
                                              message: { parts: [{ text: "which way?" }] } },
                            metadata: Object.assign({ "shrooms/session": session, "shrooms/from": from }, extra || {}) }] }]
    }
    readonly property var blockedHosts: hostsWith("review:b1", "review", "pi5 (pi5/jimmy)", "TASK_STATE_INPUT_REQUIRED")
    readonly property var needsYouHosts: hostsWith("review:n1", "review", "nothing.default", "TASK_STATE_INPUT_REQUIRED")
    readonly property var workingHosts: hostsWith("review:w1", "review", "pi5 (pi5/jimmy)", "TASK_STATE_WORKING")
    readonly property var stalledHosts: hostsWith("review:s1", "review", "pi5 (pi5/jimmy)", "TASK_STATE_WORKING", { "shrooms/stalled": true })
    readonly property var unackedHosts: hostsWith("review:u1", "review", "pi5 (pi5/jimmy)", "TASK_STATE_COMPLETED")

    // Full teardown and a fresh Main for each scenario - returns the new view.
    function freshBoard(hosts) {
        top.calls = []; top.lastPostPath = ""; top.lastPost = ""; top.postCount = 0
        top.currentHosts = hosts
        viewLoader.active = false
        wait(30)
        viewLoader.active = true
        wait(30)
        var view = viewLoader.item
        view.agentHosts = hosts
        view.boardMode = true
        // Done/unacked tasks fold behind root.showDone by default (the
        // real UI's own "show"/"hide" toggle) - found by this failing on
        // the unacked fixture: rep.count was 1 (header only), not 2. Not a
        // bug, the app's own intended behaviour; unfolded here so the ack
        // tests can reach the row the same way a person clicking "show" would.
        view.showDone = true
        view.showBoard()
        wait(150)
        return view
    }
    function panelOf(view) { return byName(view, "taskPanel") }
    function repeaterIn(panel) {
        var col = panel.children[0].contentChildren[0]
        for (var i = 0; i < col.children.length; i++)
            if (col.children[i].count !== undefined) return col.children[i]
        return null
    }
    // The ONE task-kind delegate's root item - this fixture always has
    // exactly one task row, at index 1 (index 0 is its group header).
    function theTaskDelegate(view) {
        var rep = repeaterIn(panelOf(view))
        verify(rep !== null, "the task panel's Repeater must exist")
        compare(rep.count, 2, "one header + one task row expected in this fixture")
        return rep.itemAt(1)
    }

    function test_01_boardShowsExactlyOneTaskRow() {
        var view = freshBoard(blockedHosts)
        var panel = panelOf(view)
        verify(panel !== null, "taskPanel must exist")
        compare(panel.visible, true, "taskPanel must be visible once a task exists")
        theTaskDelegate(view) // asserts header+task count==2 inside
    }

    // Q3: nudge appears ONLY on a Blocked task - checked across every other
    // group too, each with a fresh Main so nothing from a previous group
    // could still be on screen.
    function test_02_nudgeOnlyOnBlocked() {
        var cases = [["blocked", blockedHosts, true], ["needs-you", needsYouHosts, false],
                     ["working", workingHosts, false], ["stalled", stalledHosts, false],
                     ["unacked", unackedHosts, false]]
        for (var i = 0; i < cases.length; i++) {
            var view = freshBoard(cases[i][1])
            var row = theTaskDelegate(view)
            var lnk = byName(row, "nudgeLnk")
            var want = cases[i][2]
            var got = lnk !== null && lnk.visible
            compare(got, want, "nudgeLnk visible on " + cases[i][0] + ": got " + got + " want " + want)
        }
    }

    // Q1: clicking "answer" opens the field (no network call yet); typing and
    // clicking SEND posts the real request with the typed text.
    function test_03_answerClickIssuesTheRealRequest() {
        var view = freshBoard(blockedHosts)
        var row = theTaskDelegate(view)
        var answerLnk = byName(row, "answerLnk")
        verify(answerLnk !== null && answerLnk.visible, "answerLnk must be visible on a blocked task")
        mouseClick(answerLnk)
        wait(60)
        compare(top.postCount, 0, "opening the answer field must not itself post anything")
        var field = byName(row, "answerField")
        verify(field !== null && field.visible, "the answer field must be open after clicking answer")
        field.text = "go ahead"
        var sendLnk = byName(row, "sendLnk")
        verify(sendLnk !== null && sendLnk.visible, "SEND must be visible once answering")
        mouseClick(sendLnk)
        wait(60)
        compare(top.postCount, 1, "SEND must issue exactly one request")
        compare(top.lastPostPath, "/v1/tasks/review:b1/answer", "answerPath is a plain concatenation, not urlencoded")
        compare(top.lastPost, JSON.stringify({ text: "go ahead" }), "the body must carry the typed text")
    }

    // Q2: cancel is two-step. One tap only arms it; nothing is sent.
    function test_04_cancelIsTwoStep() {
        var view = freshBoard(blockedHosts)
        var row = theTaskDelegate(view)
        var cancelLnk = byName(row, "cancelLnk")
        verify(cancelLnk !== null && cancelLnk.visible, "cancelLnk must be visible on a blocked task")
        compare(cancelLnk.text, "cancel", "first state must read 'cancel', not 'sure?'")
        mouseClick(cancelLnk)
        wait(60)
        compare(top.postCount, 0, "the first tap must ARM only - nothing sent")
        compare(cancelLnk.text, "sure?", "after one tap the label must change to confirm")
        mouseClick(cancelLnk)
        wait(60)
        compare(top.postCount, 1, "the second tap must send exactly one request")
        verify(top.lastPostPath.indexOf("/cancel") > 0, "the second tap's request must be the cancel path: " + top.lastPostPath)
        verify(top.lastPostPath.indexOf("/v1/") === 0, "must be /v1/..., not /a2a/<session>: " + top.lastPostPath)
    }

    // Q4/Q5: nudge really fires, and the path is always /v1/...
    function test_05_nudgeClickIssuesTheRealRequest() {
        var view = freshBoard(blockedHosts)
        var nudgeLnk = byName(theTaskDelegate(view), "nudgeLnk")
        verify(nudgeLnk !== null && nudgeLnk.visible)
        mouseClick(nudgeLnk)
        wait(60)
        compare(top.postCount, 1)
        verify(top.lastPostPath.indexOf("/v1/") === 0, top.lastPostPath)
        verify(top.lastPostPath.indexOf("/nudge") > 0, top.lastPostPath)
    }

    function test_06_ackClickIssuesTheRealRequest() {
        var view = freshBoard(unackedHosts)
        var ackLnk = byName(theTaskDelegate(view), "ackLnk")
        verify(ackLnk !== null && ackLnk.visible, "ackLnk must be visible on an unacked task")
        mouseClick(ackLnk)
        wait(60)
        compare(top.postCount, 1)
        verify(top.lastPostPath.indexOf("/v1/") === 0, top.lastPostPath)
        verify(top.lastPostPath.indexOf("/ack") > 0, top.lastPostPath)
    }

    // Q4, the specific class: click ACK, and prove the click did NOT also
    // fall through to the row's own "open the session" MouseArea underneath
    // it - if it had, agentWatch (opening a session) would appear in
    // top.calls alongside agentPost, and the view would stop being the board.
    function test_07_ackClickDoesNotAlsoOpenTheSession() {
        var view = freshBoard(unackedHosts)
        var ackLnk = byName(theTaskDelegate(view), "ackLnk")
        verify(ackLnk !== null && ackLnk.visible)
        mouseClick(ackLnk)
        wait(80)
        verify(top.calls.indexOf("agentWatch") < 0, "ACK must not also open the session: calls=" + top.calls.join(","))
        compare(view.boardMode, true, "the board must still be in board mode, not a session view, after ACK")
    }

    // Same class, the control this bit jimmy before: click answer/nudge/
    // cancel and confirm the row's own MouseArea did not also fire.
    function test_08_controlClicksDoNotAlsoOpenTheSession() {
        var names = ["answerLnk", "nudgeLnk", "cancelLnk"]
        for (var i = 0; i < names.length; i++) {
            var view = freshBoard(blockedHosts)
            var lnk = byName(theTaskDelegate(view), names[i])
            verify(lnk !== null && lnk.visible, names[i] + " must be visible")
            mouseClick(lnk)
            wait(60)
            verify(top.calls.indexOf("agentWatch") < 0, names[i] + " must not also open the session: calls=" + top.calls.join(","))
        }
    }
}
