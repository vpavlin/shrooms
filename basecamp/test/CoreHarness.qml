import QtQuick

// Drives the view the way Basecamp does — through shrooms_core — with a
// stand-in for the core, and checks what the view asked it for.
//
// Harness.qml reads a status file, which is the one path Basecamp never takes:
// every write went untested, and a services form shipped that wrote the
// running list back over the configured one, to no mesh in particular.
Item {
    width: 1400; height: 900

    property string fixture: Qt.application.arguments[Qt.application.arguments.length - 1]
    property string statusDoc: ""
    property var calls: []
    property int progressPolls: 0
    // An older shrooms_core: no async join, no per-mesh services.
    property bool oldCore: false
    property bool firstJoinAnswer: false
    property bool keycardInstalled: true
    property int holdPolls: 0
    property int signPolls: 0

    function fakeCall(module, method, args) {
        calls.push(method + " " + JSON.stringify(args))
        console.error("CALL " + method + " " + JSON.stringify(args))
        if (oldCore && (method === "joinWithInviteStart" || method === "servicesOf")) return ""
        if (module === "keycard") {
            if (!keycardInstalled) return ""
            switch (method) {
            case "requestSign": return JSON.stringify({ signId: "sig-1", status: "pending" })
            case "checkSignStatus":
                signPolls++
                return signPolls < 2 ? JSON.stringify({ signId: "sig-1", status: "pending" })
                                     : JSON.stringify({ signId: "sig-1", status: "complete", signature: "3045ab" })
            }
            return JSON.stringify({ ok: true })
        }
        switch (method) {
        case "inviteNew": return JSON.stringify({ token: "TOKEN", grouped: "TOK-EN", uri: "shrooms://enrol?token=TOKEN",
                                                  qr: ["101", "010", "101"], ttl_s: 900 })
        case "inviteHoldStart": return JSON.stringify({ started: true })
        case "inviteHoldProgress":
            holdPolls++
            return holdPolls < 2 ? JSON.stringify({ running: true })
                : JSON.stringify({ done: true, result: { device_pub: "dd", wg_pub: "ww", seal_pub: "ss",
                                                         name: "kitchen-pi", eph_pub: "ee" } })
        case "inviteDraft": return JSON.stringify({ draft: "RFJBRlQ=", digest: "ab".repeat(32),
                                                    admin_keys: ["AKEY1", "AKEY2"], card_only: true })
        case "cardPath": return JSON.stringify({ bip32_path: "m/64265'/3'/0'", account: 3, known: true })
        case "inviteReply": return JSON.stringify({ ok: true })
        case "status": return statusDoc
        case "getPref": return ""
        case "servicesOf": return JSON.stringify({ services: ["svc-" + args[0] + ":80"] })
        case "services": return JSON.stringify({ services: ["top:81"] })
        case "hostsSuffix": return JSON.stringify({ hosts_suffix: "internal", default: "mesh", legacy: "mesh" })
        case "blindRelays": return JSON.stringify({ relays: ["203.0.113.10:31760"], token_set: true,
                                                    meshes: { test: { relays: [], none: true } } })
        case "joinWithInviteStart": return JSON.stringify({ started: true, serial: 1 })
        case "joinProgress":
            progressPolls++
            if (progressPolls < 2) return JSON.stringify({ running: true })
            return firstJoinAnswer
                ? JSON.stringify({ done: true, result: { mesh: "home", overlay: "fd00::1", credential: true } })
                : JSON.stringify({ done: true, result: { result: "joined home" } })
        case "logs": return JSON.stringify({ lines: [] })
        }
        return JSON.stringify({ result: method + " done" })
    }

    // What Basecamp hands a view as `logos`: callModule, and on newer hosts
    // request() for intents.
    QtObject {
        id: fakeBridge
        property var callModule: function (module, method, args) { return fakeCall(module, method, args) }
        property var request: function (intent, params, cb) {
            calls.push("request " + intent + " " + JSON.stringify(params))
            console.error("CALL request " + intent + " " + JSON.stringify(params))
            cb({ ok: true, data: {}, error: "" })
        }
    }

    Main {
        id: view
        anchors.fill: parent
        bridge: fakeBridge
    }

    property int fails: 0
    function check(ok, what) {
        if (ok) console.error("CHECK ok   " + what)
        else { console.error("FAIL " + what); fails++ }
    }
    function lastCall(prefix) {
        for (var i = calls.length - 1; i >= 0; i--) if (calls[i].indexOf(prefix) === 0) return calls[i]
        return ""
    }
    function called(prefix) { return lastCall(prefix) !== "" }

    Component.onCompleted: {
        var x = new XMLHttpRequest()
        x.open("GET", "file://" + fixture, false)
        x.send()
        statusDoc = x.responseText
    }

    Timer {
        interval: 1500
        running: true
        onTriggered: {
            check(view.peers.length === 3, "status read through the core (" + view.peers.length + " peers)")

            // Services: the configured list per mesh, never the running one.
            view.settingsOpen = true
            check(view.configuredServices["default"] === "svc-default:80"
                  && view.configuredServices["test"] === "svc-test:80",
                  "configured services read per mesh: " + JSON.stringify(view.configuredServices))
            check(!called("services "), "the label-less services() was not used when meshes are listed")
            view.saveServices("test", "a:1, b:2")
            check(lastCall("setServicesOf") === 'setServicesOf ["test","a:1, b:2"]', "services saved to the mesh: " + lastCall("setServicesOf"))
            check(!called("setServices "), "the label-less setServices was not used")

            // The domain suffix and blind relays, read when settings open.
            check(view.suffixInfo.hosts_suffix === "internal", "suffix read")
            check(view.relaysText(view.relayInfo) === "203.0.113.10:31760", "relays read: " + view.relaysText(view.relayInfo))
            view.saveRelays("198.51.100.2:31760", "", false)
            check(lastCall("setBlindRelays") === 'setBlindRelays ["","198.51.100.2:31760"]',
                  "relays saved without touching the token: " + lastCall("setBlindRelays"))
            view.saveRelays("198.51.100.2:31760", "tok", true)
            check(lastCall("setBlindRelaysWithToken") === 'setBlindRelaysWithToken ["","198.51.100.2:31760","tok"]',
                  "a typed token is sent: " + lastCall("setBlindRelaysWithToken"))

            // A join runs on the core's thread and is watched from here.
            view.startJoin("  tok  ", "home")
            check(view.joining, "joining after start")
            check(lastCall("joinWithInviteStart") === 'joinWithInviteStart ["tok","vps","home"]',
                  "join started with the token, this device's name and the label: " + lastCall("joinWithInviteStart"))
            check(!called("joinWithInvite "), "the blocking join was not used")
            view.pollJoin()
            check(view.joining, "still joining while the core says running")
            view.pollJoin()
            check(!view.joining && view.said.indexOf("joined home") === 0 && !view.saidBad,
                  "join finished and said so: " + view.said)

            // Renewals: the daemon's command, per credential.
            check(view.due.length === 2 && view.dueSelf.length === 1, "due read: " + view.due.length)

            // Peers: what one listens on, as addresses.
            var jc = view.peerFor("test", "jimmy-crib")
            var b = view.boundAddrs(jc)
            check(b.length === 1 && b[0].addr === "jimmy-crib.test.mesh:22", "bound as host:port: " + JSON.stringify(b))
            check(view.focusPeer("test", "jimmy-crib") && view.focused === "test/jimmy-crib", "a graph click finds the card")
            check(!view.focusPeer("test", "nobody"), "an unknown node finds nothing")

            // The agents app, through Basecamp's intent.
            check(view.canLaunch, "a host with request() can launch apps")
            view.openAgents()
            check(lastCall("request") === 'request basecamp.apps.launch {"app":"shrooms_agents"}',
                  "launch asked for the agents app: " + lastCall("request"))

            // Diagnostics: what helps, and nothing secret.
            var d = view.diagnosticsText()
            check(d.indexOf("device vps") >= 0 && d.indexOf("mesh default") >= 0
                  && d.indexOf("peer test/jimmy-crib") >= 0 && d.indexOf("due default/nothing EXPIRED") >= 0,
                  "diagnostics name the device, meshes, peers and renewals")
            check(d.toLowerCase().indexOf("token") < 0 && d.indexOf("network_key") < 0, "diagnostics carry no secret")

            // The log filter.
            view.logLines = [{ t: 1, level: "INFO", msg: "a" }, { t: 2, level: "WARN", msg: "b" },
                             { t: 3, level: "ERROR", msg: "c" }]
            view.logLevel = "warn"
            check(view.shownLog.length === 2, "warn shows warnings and errors: " + view.shownLog.length)
            view.logLevel = "error"
            check(view.shownLog.length === 1 && view.shownLog[0].msg === "c", "error shows errors")
            view.logLevel = "all"

            // An older core: the join falls back and says what it cannot know.
            oldCore = true
            view.startJoin("tok", "home")
            check(called("joinWithInvite ") && !view.joining && view.said.indexOf("timeout") >= 0,
                  "an older core joins the blocking way and says so: " + view.said)

            // Inviting with a card: mint, hold, a device comes, the card signs
            // through the Keycard module, the daemon admits.
            view.startInvite("default")
            check(view.invite.step === "open" && view.invite.grouped === "TOK-EN" && view.invite.qr.length === 3,
                  "an invite opens with its token and QR: " + JSON.stringify(view.invite))
            check(lastCall("inviteHoldStart") === 'inviteHoldStart ["TOKEN","default"]', "held on the right mesh: " + lastCall("inviteHoldStart"))
            view.pollInvite()
            check(view.invite.step === "open", "still open while the daemon holds it")
            view.pollInvite()
            check(view.invite.step === "joiner" && view.invite.joiner.name === "kitchen-pi", "the joining device is shown: " + view.invite.step)
            view.admitInvite()
            check(lastCall("inviteDraft") === 'inviteDraft ["default","dd","ww","ss","kitchen-pi"]', "drafted for that device: " + lastCall("inviteDraft"))
            check(lastCall("cardPath") === 'cardPath ["AKEY1,AKEY2"]', "the account looked up by the mesh's keys")
            var rs = lastCall("requestSign")
            check(rs.indexOf("m/64265'/3'/0'") > 0 && rs.indexOf('"scheme\\":\\"ecdsa') > 0 && rs.indexOf("abababab") > 0,
                  "the card asked to sign the digest at the mesh's path: " + rs)
            check(view.invite.step === "card", "waiting on the card")
            view.pollInvite()
            check(view.invite.step === "card", "still waiting while the card is pending")
            view.pollInvite()
            check(view.invite.step === "done", "admitted once the card signed: " + view.invite.step + " " + (view.invite.error || ""))
            check(lastCall("inviteReply") === 'inviteReply ["TOKEN","ee","kitchen-pi","default","RFJBRlQ=","3045ab"]',
                  "the reply carries the draft and the card's signature: " + lastCall("inviteReply"))

            // No Keycard module: said, not hung.
            view.invite = { step: "idle" }
            holdPolls = 0
            keycardInstalled = false
            view.startInvite("default"); view.pollInvite(); view.pollInvite(); view.admitInvite()
            check(view.invite.step === "failed" && view.invite.error.indexOf("Keycard module") >= 0,
                  "a missing Keycard module is named: " + view.invite.error)
            keycardInstalled = true

            // A daemon with no mesh yet: the view offers the first join, and
            // the daemon's answer (no "result", it restarts itself) is read.
            oldCore = false
            progressPolls = 0
            statusDoc = JSON.stringify({ waiting: true, config: "/etc/shrooms/config.toml", name: "" })
            view.reload()
            check(view.waitingForMesh, "a waiting daemon is recognised")
            firstJoinAnswer = true
            view.startJoin("tok2", "", "kitchen-pi")
            check(lastCall("joinWithInviteStart") === 'joinWithInviteStart ["tok2","kitchen-pi",""]',
                  "the first join carries the name typed: " + lastCall("joinWithInviteStart"))
            view.pollJoin(); view.pollJoin()
            check(view.said.indexOf("joined home as fd00::1") === 0 && view.said.indexOf("starting it now") > 0,
                  "a first join says the daemon is starting the mesh: " + view.said)

            console.error(fails ? "FAIL " + fails + " check(s)" : "CHECKS DONE")
            Qt.quit()
        }
    }
}
