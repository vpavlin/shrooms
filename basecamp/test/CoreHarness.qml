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
    // A shrooms_core from before the clipboard: Basecamp's own answer.
    property bool clipsOld: false
    property string clipPref: ""

    function fakeCall(module, method, args) {
        calls.push(method + " " + JSON.stringify(args))
        console.error("CALL " + method + " " + JSON.stringify(args))
        if (oldCore && (method === "joinWithInviteStart" || method === "servicesOf")) return ""
        if (clipsOld && method.indexOf("clip") === 0) return JSON.stringify({ error: "Invalid response" })
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
        case "renewDraft": return JSON.stringify({ card_only: true, admin_keys: ["AKEY1"], drafts: [
            { name: "vps", device_pub: "aa", draft: "D1", digest: "11".repeat(32) },
            { name: "nothing", device_pub: "bb", draft: "D2", digest: "22".repeat(32) }] })
        case "revokeDraft": return JSON.stringify({ card_only: true, admin_keys: ["AKEY1"], draft: "RV", digest: "33".repeat(32) })
        case "grantSigned": return JSON.stringify({ ok: true })
        case "revokeSigned": return JSON.stringify({ ok: true })
        case "status": return statusDoc
        case "getPref": return args[0] === "clip_recent" ? clipPref : ""
        case "clips": return JSON.stringify({
            received: [{ id: "r1", text: "https://example.org", mesh: "home", from: "phone",
                         created: "2026-11-11T10:00:00.123456789+01:00", expires: "2099-01-01T00:00:00Z" },
                       { id: "r2", text: "hunter2", secret: true, mesh: "home", from: "laptop",
                         created: "2026-11-11T10:00:00Z", expires: "2099-01-01T00:00:00Z" }],
            sent: [{ id: "s1", text: "", secret: true, mesh: "home",
                     to: [{ name: "phone", sent: "2026-11-11T10:00:01Z" }, { name: "vps", error: "connection refused" }],
                     created: "2026-11-11T10:00:00Z", expires: "2099-01-01T00:00:00Z" }],
            devices: [{ mesh: "home", name: "laptop", online: true }, { mesh: "home", name: "phone", online: false },
                      { mesh: "office", name: "desk", online: true }],
            meshes: ["home", "office"] })
        case "clipSend":
            var rq = JSON.parse(args[0])
            if (rq.to && rq.to.indexOf("nope") >= 0) return JSON.stringify({ error: "no device called nope on home", detail: "the daemon answered 400" })
            return JSON.stringify({ id: "s2", text: rq.text, mesh: rq.mesh, to: [] })
        case "clipTake": return JSON.stringify({ id: args[0], text: "x" })
        case "clipDelete": return JSON.stringify({ ok: true })
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

            // The clipboard (ADR-083): read when opened, sent as the daemon's
            // JSON, a received clip copied and taken, a secret masked.
            clipPref = '["home/phone"]'
            view.clipsOpen = true
            view.loadClips()
            check(!view.clipsCoreOld && view.clipData.received.length === 2 && view.clipMesh === "home",
                  "clips read, the first mesh chosen: " + view.clipsProblem + " " + view.clipMesh)
            check(view.clipDevices.map(function (d) { return d.name }).join(",") === "phone,laptop",
                  "devices on the chosen mesh, the recent one first: " + view.clipDevices.map(function (d) { return d.name }))
            check(view.clipShown(view.clipData.received[1], true) === "(secret)", "a received secret is masked")
            check(view.clipShown(view.clipData.sent[0], false) === "(secret)", "a delivered secret reads as one")
            check(view.clipAge(view.clipData.received[0].created) !== "", "a nanosecond time is read")
            check(!view.sendClip("hello"), "nothing is sent with no device chosen")
            view.toggleClipTo("laptop")
            view.clipSecret = true
            check(view.sendClip("hello"), "sent")
            check(lastCall("clipSend") === 'clipSend ["{\\"text\\":\\"hello\\",\\"secret\\":true,\\"mesh\\":\\"home\\",\\"to\\":[\\"laptop\\"]}"]',
                  "the request names the device, the mesh and the secret: " + lastCall("clipSend"))
            check(view.clipRecent[0] === "home/laptop" && view.clipRecent[1] === "home/phone" && !view.clipSecret,
                  "the recipient remembered first, secret reset: " + JSON.stringify(view.clipRecent))
            check(lastCall("setPref").indexOf("clip_recent") > 0, "recents saved")
            view.toggleClipAll()
            check(view.clipAll && view.clipTo.length === 0, "all clears the chosen devices")
            view.sendClip("to all")
            check(lastCall("clipSend").indexOf('all\\":true') > 0 && lastCall("clipSend").indexOf('mesh\\":\\"home') > 0,
                  "all of one mesh, named: " + lastCall("clipSend"))
            view.chooseClipMesh("office")
            check(!view.clipAll && view.clipDevices.length === 1 && view.clipDevices[0].name === "desk", "another mesh, its devices")
            view.toggleClipTo("nope")
            check(!view.sendClip("x") && view.clipSaidBad && view.clipSaid === "no device called nope on home",
                  "the daemon's refusal is said: " + view.clipSaid)
            view.takeClip(view.clipData.received[1])
            check(lastCall("clipTake") === 'clipTake ["r2"]' && view.clipShown(view.clipData.received[1], true) === "hunter2",
                  "a secret clicked is taken and shown: " + lastCall("clipTake"))
            view.deleteClip("s1")
            check(lastCall("clipDelete") === 'clipDelete ["s1"]', "deleted")
            clipsOld = true
            view.loadClips()
            check(view.clipsCoreOld && view.clipsProblem.indexOf("update shrooms_core") >= 0,
                  "an old core is named, not an error: " + view.clipsProblem)
            clipsOld = false
            view.clipsOpen = false

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

            // Renewing with the card: one signature per member that is due,
            // each delivered as it comes back.
            view.invite = { step: "idle" }
            check(view.dueMeshes.length === 1 && view.dueMeshes[0] === "default", "renewal offered per mesh: " + view.dueMeshes)
            signPolls = 0
            view.startRenew("default")
            check(view.cardJob.step === "signing" && view.cardJob.items.length === 2, "two renewals queued: " + JSON.stringify(view.cardJob))
            check(lastCall("requestSign").indexOf("11111111") > 0, "the first digest went to the card")
            view.pollCardJob()   // pending
            view.pollCardJob()   // complete → grant, next
            check(lastCall("grantSigned") === 'grantSigned ["default","D1","3045ab"]', "the first delivered: " + lastCall("grantSigned"))
            check(lastCall("requestSign").indexOf("22222222") > 0, "then the second asked for")
            view.pollCardJob()   // complete (signPolls already past) → grant, done
            check(view.cardJob.step === "done" && view.cardJob.ok === 2, "both renewed: " + JSON.stringify(view.cardJob))
            view.closeCardJob()

            // Revoking one device.
            view.startRevoke("test", "0101", "jimmy-crib")
            check(lastCall("revokeDraft") === 'revokeDraft ["test","0101"]', "drafted for that device: " + lastCall("revokeDraft"))
            view.pollCardJob()
            check(view.cardJob.step === "done" && lastCall("revokeSigned") === 'revokeSigned ["test","RV","3045ab"]',
                  "revoked with the card's signature: " + lastCall("revokeSigned"))
            view.closeCardJob()

            // A file-key mesh: said, not attempted.
            view.startCardJob("renew", "x", [{ label: "a", draft: "d", digest: "e" }], { card_only: false })
            check(view.cardJob.step === "failed" && view.cardJob.error.indexOf("shrooms admin renew") > 0, "a file-key mesh points at the CLI")
            view.closeCardJob()

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
