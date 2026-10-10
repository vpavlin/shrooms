import QtQuick

// Opens the Agents panel against a stand-in core and checks what it shows,
// then saves a picture of it: the panel runs only inside Basecamp, whose
// sandbox and IPC cannot be driven from a test, so the view is handed a
// bridge that answers like shrooms_core does — events shaped exactly as
// shrooms-agent sends them (Claude Code's stream-json, verbatim).
Item {
    id: top
    width: 1280; height: 800

    property string out: Qt.application.arguments[Qt.application.arguments.length - 1]
    property int eventCalls: 0

    readonly property var hosts: [{
        name: "laptop", mesh: "office", address: "fdb0:9afc:a5ef:388c:8264:7716:36fc:64eb",
        list: { limits: { at: "2026-10-06T21:40:00+02:00", status: "allowed", window: "five_hour",
                          windows: { five_hour: { utilization: 0.62, resets_at: "2099-10-07T02:20:00+02:00" } } },
                sessions: [
            { name: "shrooms", dir: "/home/someone/logos-vpn", state: "waiting", pending: 1, running: true,
              last_seq: 9, last_time: "2026-10-03T14:27:31+02:00", auto_approve: false,
              context_used: 678705, context_window: 1000000, model: "claude-opus-5[1m]",
              preview: "The view loads and the existing checks pass." },
            { name: "notes", dir: "/home/someone/notes", state: "idle", pending: 0, running: false,
              cage: { image: "localhost/shrooms-workbench:desktop", github: true },
              limited: { reason: "limit reached (5 hours)", until: "2099-10-07T02:20:00+02:00" },
              last_seq: 3, last_time: "2026-10-02T09:00:00+02:00", auto_approve: true,
              context_used: 22703, context_window: 200000, model: "claude-haiku-4-5-20251001", preview: "" } ] } }]

    function ev(seq, kind, data, by) {
        return { seq: seq, time: "2026-10-03T14:2" + (seq % 10) + ":00+02:00", kind: kind, by: by || "", data: data }
    }
    readonly property var events: [
        ev(1, "message", { text: "Can you check **the** tests? http://vps.office.mesh:8099/x" }, "nothing.office"),
        ev(2, "claude", { type: "system", subtype: "init", session_id: "s1", model: "claude-opus-5[1m]" }),
        ev(3, "claude", { type: "assistant", message: { content: [
            { type: "thinking", thinking: "" },
            { type: "text", text: "## Running them\n\nFirst `make test`, then:\n\n- the **agent** package\n- the view\n\n```\ngo test ./...\n```" },
            { type: "tool_use", id: "t1", name: "Bash", input: { command: "make test", description: "Run the tests" } } ] } }),
        ev(4, "claude", { type: "user", message: { content: [ { type: "tool_result", tool_use_id: "t1", content: "ok  internal/agent\nok  cmd/shrooms", is_error: false } ] } }),
        ev(5, "claude", { type: "result", subtype: "success", total_cost_usd: 0.0123 }),
        ev(6, "setting", { auto_approve: false }, "laptop.office"),
        ev(7, "message", { text: "Now push it." }, "nothing.office"),
        ev(8, "claude", { type: "assistant", message: { content: [ { type: "tool_use", id: "t2", name: "Bash", input: { command: "git push origin master" } } ] } }),
        ev(9, "claude", { type: "control_request", request_id: "p1", request: { subtype: "can_use_tool", tool_name: "Bash",
            input: { command: "git push origin master" }, description: "Push to GitHub" } }),
        { seq: 9, kind: "partial", data: { text: "Pushing " } },
        { seq: 9, kind: "partial", data: { text: "**now**…" } },
    ]

    property var calls: []
    property string lastFind: ""
    property string lastPost: ""
    property string lastDelete: ""
    property string lastMoveKept: ""
    property string lastWatch: ""
    // A windowed core, for the jump that has to reach OUTSIDE the loaded tail.
    property bool windowed: false
    property int watchTail: 0
    property var windowEvents: []
    // Every watch tail asked for, raw: a negative one is a FRESH open (no kept copy).
    property var watchTails: []
    property string lastSearch: ""
    property bool findNone: false
    property string lastOpen: ""
    property var outbox: []
    property string lastQueued: ""
    property string lastPostPath: ""
    property int searchAsked: 0
    // 1: the core shows the copy kept on disk; 2: the machine's events have
    // replaced it (a new epoch).
    property int keptPhase: 0
    property var spoken: []          // what the view asked the core to read
    property bool speakingNow: false
    property var voiceCalls: []
    property var usageAsked: []
    property bool noConversations: false
    property var gatherAddrs: []
    property int gatheredAsked: 0
    property var usageBody: ({ machine: "laptop",
        limits: { at: "2026-10-06T21:04:00+02:00", status: "rejected", window: "five_hour",
                  windows: { seven_day: { utilization: 0.49, resets_at: "2099-10-11T11:00:00+02:00" },
                             five_hour: { utilization: 1, resets_at: "2099-10-06T21:20:00+02:00" } } },
        rows: [
        { day: "2026-10-05", session: "shrooms", by: "nothing.office", model: "claude-opus-5[1m]", turns: 3, input: 10, cache_read: 1000, cache_write: 200, output: 900, cost_usd: 1.5, busy_ms: 120000 },
        { day: "2026-10-05", session: "notes", by: "", model: "ollama/qwen3", turns: 5, input: 50, cache_read: 0, cache_write: 0, output: 2000, cost_usd: 0, busy_ms: 3600000 } ] })
    property var voiceNow: ({ installed: false, busy: false, step: "", error: "", engine: "spd-say", voice: "en_US-lessac-medium" })
    function countVisible(item, name) {
        var n = (item.objectName === name && item.visible) ? 1 : 0
        for (var i = 0; i < item.children.length; i++) n += countVisible(item.children[i], name)
        return n
    }
    function findByName(item, name) {
        if (item.objectName === name) return item
        for (var i = 0; i < item.children.length; i++) {
            var f = findByName(item.children[i], name)
            if (f) return f
        }
        return null
    }
    property var jobsNow: []

    Main {
        id: view
        anchors.fill: parent
        bridge: QtObject {
            function callModule(module, method, args) {
                top.calls.push(method)
                if (method === "agentWatch") {
                    top.lastWatch = args.join(" ")
                    // The LIVE SHAPE: the real core answers with the LAST `tail` events, so a
                    // session deeper than the tail has no older events loaded, and a jump to a
                    // task that arrived before the window has to reach back.
                    // The core's Hub::watch IGNORES a larger positive tail while it keeps a
                    // copy of a session, and only a NEGATIVE one ("without the copy") makes
                    // it replay a wider window. Modelled here: the tail is recorded raw, and
                    // the window uses its size. A widening that stays positive is the bug
                    // the reviewer traced live (205 rebuilds wanting a seq never loaded).
                    if (top.windowed) {
                        var wt = Number(args[2])
                        top.watchTails = top.watchTails.concat([wt])
                        // Faithful to Hub::watch: while it keeps a copy of a session, a
                        // larger POSITIVE tail is ignored; only a NEGATIVE one ("without the
                        // copy") makes it replay a wider window.
                        if (wt < 0) top.watchTail = -wt
                        else if (top.watchTail === 0) top.watchTail = wt
                        else top.watchTail = Math.min(top.watchTail, wt)
                    }
                    return JSON.stringify({ ok: true })
                }
                // THE LIVE SHAPE. The real core answers with the LAST `tail` events, so a
                // session deeper than the tail does not have its older events loaded - and
                // a jump to a task that arrived before the window has to reach back. This
                if (method === "agentEvents" && top.windowed) {
                    var wafter = Number(args[0])
                    var wbase = Math.max(0, top.windowEvents.length - top.watchTail)
                    var wfrom = Math.max(wafter, wbase)
                    var wupto = Math.min(top.windowEvents.length, wfrom + 4)
                    return JSON.stringify({ next: wupto, more: wupto < top.windowEvents.length,
                                            connected: true, error: "", kept: 0, epoch: 9,
                                            events: wfrom >= top.windowEvents.length ? []
                                                    : top.windowEvents.slice(wfrom, wupto) })
                }
                if (method === "agentSearch" && top.windowed) { top.lastSearch = args.join(" "); return JSON.stringify({ search: 1 }) }
                if (method === "agentSearched" && top.windowed)
                    return JSON.stringify({ id: 1, done: true, error: "", found: [
                        { seq: 20, time: "2026-10-09T10:00:00Z", role: "user",
                          snippet: "[shrooms task jimmy:deep-1 from laptop.default (laptop/shrooms)] the arrival" },
                        { seq: 390, time: "2026-10-09T11:00:00Z", role: "user",
                          snippet: "[shrooms task jimmy:deep-1 \u2014 more from laptop.default (laptop/shrooms)]" } ] })


                if (method === "status") return JSON.stringify({ name: "desk",
                    meshes: [ { label: "office", overlay: "fdb0:9afc:a5ef:1111:2222:3333:4444:5555" } ], peers: [
                    { name: "laptop", mesh: "office", overlay: "fdb0:9afc:a5ef:388c:8264:7716:36fc:64eb", online: true } ] })
                // The same machine answering on a second mesh address.
                if (method === "agentsFind" && top.findNone) return JSON.stringify([])
                if (method === "agentsFind" && top.boardFind) return JSON.stringify(top.boardFind)
                if (method === "agentsFind") { top.lastFind = args[0]
                    return JSON.stringify(top.hosts.concat([Object.assign({}, top.hosts[0], { mesh: "home", address: "fd7b::1" })])) }
                // The core's outbox, as a list here.
                if (method === "agentQueue") {
                    var q = { id: "b-" + (top.outbox.length + 1), address: args[0], session: args[1], kind: "text", text: args[2], created: 1, error: "" }
                    top.outbox = top.outbox.concat([q]); top.lastQueued = args[2]
                    return JSON.stringify({ id: q.id })
                }
                if (method === "agentKeep") return JSON.stringify({ file: "/home/x/.local/share/shrooms/outbox/b-19a2b-3c4-0-" + String(args[0]).split("/").pop() })
                if (method === "agentQueueFiles") {
                    var qf = { id: "b-" + (top.outbox.length + 1), address: args[0], session: args[1], kind: "text", text: args[2], created: 1, error: "",
                               files: String(args[3]).split("\n").map(function(f) { return { name: f.split("/").pop(), sent: false } }) }
                    top.outbox = top.outbox.concat([qf]); top.lastQueued = args[2] + " files=" + args[3]
                    return JSON.stringify({ id: qf.id })
                }
                if (method === "agentOutbox") return JSON.stringify(top.outbox)
                if (method === "agentUnqueue") { top.outbox = top.outbox.filter(function(x) { return x.id !== args[0] }); return JSON.stringify({ ok: true }) }
                if (method === "agentRecord" && args[0] === "send") {
                    top.outbox = top.outbox.concat([{ id: "b-v", address: args[1], session: args[2], kind: "voice", text: "", created: 2, error: "connect: no route to host" }])
                    return JSON.stringify({ id: "b-v" })
                }
                if (method === "agentOpenUrl") { top.lastOpen = args[0]
                    return JSON.stringify(String(args[0]).indexOf("http") === 0 ? { ok: true } : { error: "cannot open it", detail: "only http and https links are opened" }) }
                if (method === "agentSearch") { top.lastSearch = args.join(" "); return JSON.stringify({ search: 1 }) }
                // Still running the first time it is asked, as a real search is.
                if (method === "agentSearched" && top.searchAsked++ === 0) return JSON.stringify({ id: 1, done: false, error: "", found: null })
                if (method === "agentSearched") return JSON.stringify({ id: 1, done: true, error: "", found: [
                    { seq: 3, time: "2026-10-03T14:22:00+02:00", role: "assistant", snippet: "All **tests** pass." },
                    { seq: 0, time: "2026-10-02T10:00:00+02:00", role: "user", snippet: "the tests, in a terminal", text: "Earlier: the tests, in a terminal." } ] })
                if (method === "agentGather") { top.usageAsked = top.usageAsked.concat([args[1]]); top.gatherAddrs = String(args[0]).split("\n"); top.gatheredAsked = 0
                    return JSON.stringify({ gather: 1 }) }
                // Nothing yet the first time it is asked, as over a real mesh.
                if (method === "agentGathered") {
                    var done = top.gatheredAsked++ > 0
                    return JSON.stringify({ id: 1, results: top.gatherAddrs.map(function(a) {
                        return { address: a, done: done, error: "", body: done ? top.usageBody : null } }) })
                }
                if (method === "agentVoice") {
                    top.voiceCalls = top.voiceCalls.concat([args[0]])
                    if (args[0] === "setup") top.voiceNow = { installed: false, busy: true, step: "downloading Piper (25 MB)", error: "", engine: "spd-say", voice: "en_US-lessac-medium" }
                    return JSON.stringify(top.voiceNow)
                }
                if (method === "agentSpeak") {
                    if (args[0] === "say") { top.spoken = top.spoken.concat([args[2] + ":" + args[1]]); top.speakingNow = true }
                    if (args[0] === "stop") top.speakingNow = false
                    return JSON.stringify(args[0] === "state" ? { speaking: top.speakingNow, engine: "spd-say" } : { ok: true })
                }
                if (method === "agentEvents" && top.keptPhase === 1) return JSON.stringify({ next: 3, more: false, connected: false,
                    error: "connect: no route to host", kept: 1759500000000, epoch: 4, events: Number(args[0]) >= 3 ? [] : top.events.slice(0, 3) })
                // A copy of a session since made again: numbers above its own.
                if (method === "agentEvents" && top.keptPhase === 4) return JSON.stringify({ next: 0, more: false, connected: true, error: "", kept: 0, epoch: 0, events: [] })
                if (method === "agentEvents" && top.keptPhase === 3) return JSON.stringify({ next: 3, more: false, connected: false,
                    error: "", kept: 1759500000000, epoch: 0, events: Number(args[0]) >= 3 ? [] : [ev(50, "message", { text: "old" }), ev(51, "message", { text: "older" }), ev(52, "message", { text: "oldest" })] })
                if (method === "agentEvents" && top.keptPhase === 2) return JSON.stringify({ next: 7, more: false, connected: true,
                    error: "", kept: 0, epoch: 5, events: Number(args[0]) >= 7 ? [] : top.events.slice(0, 4) })
                if (method === "agentEvents") {
                    top.eventCalls++
                    var after = Number(args[0])
                    // In pieces, as the core answers: the view must keep reading.
                    var upto = Math.min(top.events.length, after + 4)
                    return JSON.stringify({ next: upto, more: upto < top.events.length, connected: true, error: "",
                                            events: after >= top.events.length ? [] : top.events.slice(after, upto) })
                }
                if (method === "agentGet" && String(args[1]) === "/v1/harnesses") return JSON.stringify({ harnesses: [
                    { name: "claude", title: "Claude Code", caps: { approve: true, takeover: true } },
                    { name: "pi", title: "pi", caps: { approve: false, takeover: false } } ],
                    cage: { available: true, image: "localhost/shrooms-workbench:latest", ready: false, nix: true,
                            images: ["localhost/shrooms-workbench:latest", "localhost/shrooms-workbench:desktop"] } })
                if (method === "agentGet" && String(args[1]).indexOf("/v1/conversations") === 0 && top.noConversations) return JSON.stringify({ conversations: [] })
                if (method === "agentGet" && String(args[1]).indexOf("/v1/conversations") === 0) return JSON.stringify({ conversations: [
                    { id: "c-new", dir: "/home/someone/shrooms", modified: "2026-10-03T15:00:00+02:00", size: 1000,
                      last_user: "fix the tether", last_assistant: "Fixed.",
                      terminals: [ { pid: 23173, dir: "/home/someone/shrooms", tmux: "cl-logos-vpn", args: "claude --continue" } ] },
                    { id: "c-taken", dir: "/home/someone/notes", modified: "2026-10-02T09:00:00+02:00", size: 10, adopted_by: "notes" } ] })
                if (method === "agentGet") return JSON.stringify({ history: [
                    { time: "2026-10-02T10:00:00+02:00", role: "user", text: "Earlier, in a terminal." },
                    { time: "2026-10-02T10:01:00+02:00", role: "assistant", text: "And *my* answer then." },
                    // The transcript holds the phone's own turns too: this one
                    // is event 1 and must not be shown twice.
                    { time: "2026-10-03T14:21:00+02:00", role: "user", text: "Can you check **the** tests?" } ] })
                if (method === "agentPost") { top.lastPost = args[2]; top.lastPostPath = args[1]; return JSON.stringify({ ok: true }) }
                if (method === "agentUpload") return JSON.stringify({ job: 1 })
                if (method === "agentDelete") { top.lastDelete = args[1]; return JSON.stringify({ ok: true }) }
                if (method === "agentMoveKept") { top.lastMoveKept = args.join(","); return JSON.stringify({ ok: true }) }
                if (method === "agentRecord") return JSON.stringify(args[0] === "stop" ? { job: 2 } : { ok: true })
                if (method === "agentJobs") return JSON.stringify({ recording: false, jobs: top.jobsNow })
                return JSON.stringify({ error: "unknown " + method })
            }
        }
    }

    Timer {
        interval: 1500; running: true
        onTriggered: {
            console.error("HOSTS=" + view.agentHosts.length + " SESSIONS=" + view.agentHosts[0].sessions.length)
            console.error("PROBED=" + top.lastFind)
            view.openSession(view.agentHosts[0], "shrooms")
        }
    }
    // The list keeps the width it was given, however long a line the
    // conversation shows; dragging sets it. Measured a frame after each
    // change: layouts settle on the next frame (forceLayout here hung).
    property var widthCheck: ({})
    Timer {
        interval: 2000; running: true
        onTriggered: {
            top.widthCheck.kept = view.agentStreaming
            view.agentStreaming = new Array(60).join("averyveryverylongunbrokenword")
        }
    }
    Timer {
        interval: 2300; running: true
        onTriggered: {
            var list = top.findByName(view, "agentList")
            top.widthCheck.w1 = list.width; top.widthCheck.want1 = view.listWidthPx()
            view.listWidth = 400
        }
    }
    Timer {
        interval: 2600; running: true
        onTriggered: {
            var c = top.widthCheck, list = top.findByName(view, "agentList")
            var w2 = list.width, want2 = view.listWidthPx()
            view.agentStreaming = c.kept
            view.listWidth = 320
            console.error("LISTWIDTH kept=" + (Math.abs(c.w1 - c.want1) < 1) + " dragged=" + (Math.abs(w2 - want2) < 1) + " grew=" + (w2 > c.w1))
        }
    }
    // Just after opening, before the view's next poll: everything is read.
    Timer {
        interval: 1600; running: true
        onTriggered: console.error("LOADED=" + view.agentNext + "/" + top.events.length)
    }
    Timer {
        interval: 3500; running: true
        onTriggered: {
            console.error("HOSTSCROLL=" + top.hostScrollSeen)
            var kinds = []
            for (var i = 0; i < chatCount(); i++) kinds.push(view.chatModelAt(i).kind)
            console.error("ROWS=" + kinds.join(","))
            console.error("WATCH=" + top.lastWatch)
            view.stopTurn()
            console.error("STOPPED=" + top.lastPostPath)
            view.askRestart()
            view.restartOpenSession()
            console.error("QUOTATAG shown=" + top.countVisible(view, "quotaTag") + " label=" + view.quotaLabel({ reason: "x", until: "2099-10-07T02:20:00+02:00" }).replace(/back .*/, "back T") + "," + view.quotaLabel(null) + "," + view.quotaLabel({ reason: "x" }))
            console.error("CAGETAG shown=" + top.countVisible(view, "cageTag") + " words=" + view.cageWords({ image: "localhost/shrooms-workbench:desktop", github: true }))
            console.error("RESTARTED=" + top.lastPostPath + " SAID=" + view.said)
            var ch = view.mdHtml("Run `ssh-keygen -t ed25519` in ~/x:\n```\nmake test\ngo vet ./...\n```")
            var hrefs = ch.match(/href="copy:[^"]*"/g) || []
            var copied = view.activateLink(hrefs.length ? hrefs[1].slice(6, -1) : "")
            console.error("COPYCODE links=" + hrefs.length + " first=" + (hrefs.length ? decodeURIComponent(hrefs[0].slice(11, -1)) : "") + " copied=" + copied + " said=" + view.said
                          + " url=" + view.activateLink("copy:") )
            // Into a cage from the open session: the machine's offer asked
            // for, the dialog opened, the session moved with its options.
            view.askCage()
            var cageOpen = view.dialogs().some(function(d) { return d.visible })
            view.cageOpts = { image: "localhost/shrooms-workbench:desktop", nix: false, github: true }
            view.cageOpenSession(true)
            var moved = top.lastPostPath + " " + top.lastPost
            view.cageOpenSession(false)
            console.error("CAGED open=" + cageOpen + " offer=" + (view.cageStatus !== null) + " moved=" + moved + " out=" + top.lastPost)
            view.setAcceptCaged(true)
            var accepted = top.lastPostPath + " " + top.lastPost
            console.error("ACCEPTCAGED " + accepted + " said=" + view.said)
            view.closeDialogs()
            // A turn the harness started itself: labelled by its source.
            var kept = view.agentEventsList
            view.agentEventsList = [ev(90, "message", { text: "HEARTBEAT: pick one", outside: true }, "heartbeat")]
            var hb = view.row(view.chatItems()[0])
            console.error("OUTSIDE=" + hb.outside + "," + hb.by)
            view.agentEventsList = kept
            console.error("STREAMING=[" + view.agentStreaming + "] WORKING=" + view.agentWorking
                          + " CONTEXT=" + view.contextLabel(view.agentInfo.context_used, view.agentInfo.context_window)
                          + " MODEL=" + view.shortModel(view.agentInfo.model))
            for (i = 0; i < chatCount(); i++) {
                var r = view.chatModelAt(i)
                if (r.kind === "prompt") console.error("PROMPT open=" + r.open + " id=" + r.pid + " text=" + r.text)
            }
            view.answerPrompt("p1", true)

            // A file, kept by the core at once — nothing asked of the agent's
            // machine — and a voice note, finishing in the core's own time.
            view.attachFile("file:///home/x/Pictures/shot.png")
            top.jobsNow = [
                { id: 2, kind: "voice", state: "done", name: "voice note", path: "/x/voice.wav", text: "ahoj, tady Vašek", error: "" } ]
            view.pumpJobs()
            view.pumpJobs()
            console.error("ATTACHED=" + view.agentAttached.length + " " + view.attachedName(view.agentAttached[0]) + " STICK=" + view.chatStick)
            console.error("COMPOSER=[" + view.composerText() + "]")
            view.sendToAgent("look")

            console.error("SENT=" + JSON.stringify(top.lastQueued) + " ATTACHED_AFTER=" + view.agentAttached.length)

            // Written: into the outbox, shown queued until the core sends it.
            // A voice note goes there too, as audio — no text comes back.
            view.refreshOutbox()
            var queuedText = view.agentQueued.length
            view.toggleRecording(); view.toggleRecording()
            view.refreshOutbox()
            console.error("OUTBOX=" + queuedText + "," + view.agentQueued.length + " voice=" + view.agentQueued[1].kind
                          + " label=[" + view.queuedLabel(view.agentQueued[1]) + "]")
            view.cancelQueued("b-v")
            console.error("CANCELLED=" + view.agentQueued.length)

            // A voice note's way to a turn: transcribing, failed with a retry,
            // then the turn it became, marked as said.
            var n = top.events.length
            top.events.push(ev(n + 1, "voice", { id: "v1", path: "/u/n.wav", status: "transcribing" }))
            view.pumpAgent()
            var kinds1 = []
            for (i = 0; i < chatCount(); i++) kinds1.push(view.chatModelAt(i).kind)
            top.events.push(ev(n + 2, "voice", { id: "v1", status: "failed", error: "model not found" }))
            view.pumpAgent()
            var failedSaid = "none"
            for (i = 0; i < chatCount(); i++)
                if (view.chatModelAt(i).kind === "voicenote") failedSaid = view.chatModelAt(i).error + ":" + view.chatModelAt(i).text
            view.retryVoice("v1")
            var retried = top.lastPostPath
            top.events.push(ev(n + 3, "message", { text: "ahoj", id: "v1", voice: "/u/n.wav" }, "nothing"))
            view.pumpAgent()
            var lastRow = view.chatModelAt(chatCount() - 1)
            console.error("VOICE first=" + kinds1[kinds1.length - 1] + " failed=" + failedSaid
                          + " retry=" + retried + " then=" + lastRow.kind + ":" + lastRow.voice + ":" + lastRow.text
                          + " notes=" + (function() { var c = 0; for (var k = 0; k < chatCount(); k++) if (view.chatModelAt(k).kind === "voicenote") c++; return c })())

            // Search: asked of the core, read back, and a result opened — one
            // from before the agent is shown whole, one of its own is jumped to.
            view.searchOpen = true
            view.runSearch("  tests ")
            view.pumpSearch()
            console.error("STILLBUSY=" + view.searchBusy + " " + (view.searchFound === null))
            view.pumpSearch()
            console.error("SEARCH=" + top.lastSearch + " FOUND=" + view.searchFound.length + " BUSY=" + view.searchBusy)
            view.openFound(view.searchFound[1])
            console.error("READING=" + view.readingOpen() + " TEXT=" + view.reading.text)
            view.openFound(view.searchFound[0])
            var litRow = -1
            for (i = 0; i < chatCount(); i++) if (view.chatModelAt(i).seq === 3) { litRow = i; break }
            console.error("JUMP lit=" + view.agentLit + " row=" + litRow + " kind=" + (litRow >= 0 ? view.chatModelAt(litRow).kind : "")
                          + " searchOpen=" + view.searchOpen + " stick=" + view.chatStick
                          + " reach=" + view.tailReaching(300, 1000, 500) + "," + view.tailReaching(0, 1000, 5))
            // Tapping a task opens its session and arms a jump to the message that carried
            // it. The id is the ref after the colon; the match is on the event id, not a
            // sequence; and a message outside the tail is reached with the search.
            console.error("JUMPID " + [view.taskMessageId("laptop/review:abc-123"),
                view.taskMessageId("nocolon"), view.taskMessageId("")].join(","))
            console.error("JUMPROW " + [view.isJumpRow({ pid: "x" }, "x"),
                view.isJumpRow({ pid: "y" }, "x"), view.isJumpRow({ pid: "x", earlier: true }, "x"),
                view.isJumpRow({ pid: "x" }, ""), view.isJumpRow(null, "x")].join(","))
            console.error("JUMPQ " + view.taskSearchQuery("laptop/review:m1"))
            // Tapping a link filters the panel to that pair; "x all tasks" clears it.
            var fr = [{ kind: "task", askerKey: "pi5/jimmy", machine: "laptop", session: "review" },
                      { kind: "task", askerKey: "laptop/shrooms", machine: "laptop", session: "review" }]
            // Filtering by a row's OWN pair must find it - that is the whole bug class: the
            // badge and the row used to build the pair differently, so the filter came out
            // empty. Both directions of one link are the same pair, so either row's key
            // finds both rows on it.
            console.error("PAIR " + [view.panelPair(fr[0]), view.panelPair({ kind: "header" }),
                view.linkFiltered(fr, view.panelPair(fr[0])).length,
                view.linkFiltered(fr, view.panelPair(fr[1])).length,
                view.linkFiltered(fr, "nobody>x").length,
                view.linkFiltered(fr, "").length].join(","))
            // The badge hit test. The canvas sits on top of the cards, so a press that is not
            // on a badge must be handed back and fall through - a harness cannot click through
            // layers, so the decision itself is what gets pinned.
            var bh = [{ x: 10, y: 10, w: 20, h: 10, pair: "a>b" }]
            console.error("BADGEAT " + [view.badgeAt(bh, 15, 15), view.badgeAt(bh, 5, 5),
                view.badgeAt(bh, 10, 10), view.badgeAt(bh, 30, 20), view.badgeAt(bh, 31, 15),
                view.badgeAt([], 15, 15), view.badgeAt(null, 1, 1)].join(","))
            // May the view follow the end? Not while a jump is armed or pending: the replay
            // grows contentHeight with every row it appends, the list touches its end, and
            // the reader is carried to the bottom - the second half of the race, and it is
            // in the ListView, where a harness has no geometry. The DECISION is pure.
            var fe = [view.followingEnd(true), view.followingEnd(false)]
            view.jumpPending = true; fe.push(view.followingEnd(true)); view.jumpPending = false
            view.jumpTo = 5; fe.push(view.followingEnd(true)); view.jumpTo = 0
            view.jumpToId = "x"; fe.push(view.followingEnd(true)); view.jumpToId = ""
            console.error("FOLLOWING " + fe.join(","))
            console.error("PAIRLABEL " + [view.pairLabel("laptop/a>pi5/b"), view.pairLabel("nocolon"),
                view.pairLabel("")].join(","))

            // A question from the model: a card of its own, the options picked
            // (two of three, given in the order offered), and the answer sent.
            var qs = [ { question: "Which user?", header: "VPS user", multiSelect: false,
                         options: [ { label: "agent", description: "no sudo" }, { label: "root", description: "" } ] },
                       { question: "What else?", header: "Extras", multiSelect: true,
                         options: [ { label: "voice", description: "" }, { label: "backups", description: "" }, { label: "logs", description: "" } ] } ]
            top.events.push(ev(top.events.length + 1, "claude", { type: "control_request", request_id: "q1",
                request: { subtype: "can_use_tool", tool_name: "AskUserQuestion", input: { questions: qs } } }))
            view.pumpAgent()
            var qrow = null
            for (i = 0; i < chatCount(); i++) if (view.chatModelAt(i).kind === "question") qrow = view.chatModelAt(i)
            var before = view.questionAnswers("q1", qs)
            view.pickOption("q1", "Which user?", "root", false)
            view.pickOption("q1", "Which user?", "agent", false)
            view.pickOption("q1", "What else?", "logs", true)
            view.pickOption("q1", "What else?", "voice", true)
            view.answerQuestion("q1", qs)
            var posted = JSON.parse(top.lastPost)
            top.events.push(ev(top.events.length + 1, "answer", { prompt: "q1", allow: true, answers: posted.answers }, "desk"))
            view.pumpAgent()
            for (i = 0; i < chatCount(); i++) if (view.chatModelAt(i).kind === "question") qrow = view.chatModelAt(i)
            console.error("QUESTION open=" + (qrow !== null) + " before=" + before + " posted=" + JSON.stringify(posted)
                          + " after=" + qrow.open + " [" + qrow.answer + "]")

            // Bare URLs are links, in the model's markdown and in what was typed;
            // code and links already written are left alone.
            console.error("LINKMD=" + view.linkMarkdown("see https://pi.dev, or [docs](https://x.io/a) and `curl http://no.pe`\n```\nhttp://in.code\n```\n<https://already.io>"))
            console.error("LINKPLAIN=" + view.linkPlain("a <b> & http://vps.office.mesh:8099/x."))
            console.error("LINKBOLD=" + view.linkMarkdown("see **https://x.io/a.md** and _https://y.io/b_") + " | " + view.linkPlain("**https://x.io/a*b**"))

            // A link goes to the core, which may open it (the view's sandbox
            // may not); one it refuses is copied and said so.
            view.openUrl("https://example.org/a")
            var opened = top.lastOpen
            view.openUrl("ftp://example.org/b")
            console.error("EXPLAIN=" + (view.explain("Invalid response").indexOf("restart Basecamp") > 0) + "," + view.explain("no session"))
            console.error("OPENURL=" + opened + " refused=" + (view.said.indexOf("copied it instead") > 0))

            // A machine that misses a round of finding stays, as last seen;
            // greyed once quiet a while; forgotten only after days.
            top.findNone = true
            view.refreshAgents()
            var stayed = view.agentHosts.map(function(x) { return x.name + ":" + x.sessions.length }).join(",")
            var kept = view.agentHosts[0]
            console.error("FLAKY stayed=" + stayed + " now=" + view.hostReachable(kept, Date.now())
                          + " later=" + view.hostReachable(kept, Date.now() + 30000)
                          + " forgotten=" + view.mergeHosts([{ name: "old", lastSeen: 1, sessions: [] }], [], Date.now()).length)
            top.findNone = false
            view.refreshAgents()

            // Starring: first in the list, out of its machine's, and kept on the agent.
            var h0 = view.agentHosts[0]
            view.setStarred(h0, "shrooms", true)
            var starBody = top.lastPost
            console.error("STARRED=" + view.starredSessions.map(function(x) { return x.host.name + "/" + x.sess.name }).join(",")
                          + " rest=" + view.unstarred(view.agentHosts[0]).map(function(x) { return x.name }).join(",")
                          + " sent=" + starBody)
            view.setStarred(view.agentHosts[0], "shrooms", false)
            console.error("UNSTARRED=" + view.starredSessions.length + " sent=" + top.lastPost)

            // A session of another harness: offered when the machine has it,
            // created with it, and without auto-approve, which pi has no use for.
            view.loadHarnesses(view.agentHosts[0])
            var offered = view.harnesses.map(function(x) { return x.name }).join(",")
            view.nsHarness = "pi"
            view.createSession(view.agentHosts[0], "pi-proj", "~/proj", true)
            var made = JSON.parse(top.lastPost)
            // In a cage: offered, said what it is, and asked for.
            var cageOffered = view.cageStatus !== null && view.cageNote(view.cageStatus).indexOf("built with the first one") > 0
            view.nsCage = true
            view.createSession(view.agentHosts[0], "boxed", "~/proj", false)
            var boxed = JSON.parse(top.lastPost)
            view.cageOpts = { image: "localhost/shrooms-workbench:desktop", nix: true, github: false }
            view.createSession(view.agentHosts[0], "desk", "~/proj", false)
            var desk = JSON.parse(top.lastPost).cage
            view.cageOpts = { image: "localhost/shrooms-workbench:desktop", nix: true, github: true, sealed: true }
            view.createSession(view.agentHosts[0], "sealedrev", "~/proj", false)
            var sealedBody = JSON.parse(top.lastPost).cage
            view.loadHarnesses(view.agentHosts[0])
            view.createSession(view.agentHosts[0], "free", "~/proj", false)
            console.error("CAGE offered=" + cageOffered + " sent=" + JSON.stringify(boxed.cage) + " reset=" + (JSON.parse(top.lastPost).cage === undefined)
                          + " label=" + view.cageLabel({ cage: { image: "x" } }) + "," + view.cageLabel({})
                          + " desk=" + JSON.stringify(desk) + " sealed=" + JSON.stringify(sealedBody) + " words=" + view.cageWords({ sealed: true, image: "localhost/shrooms-workbench:desktop" })
                          + " note=[" + view.cagedNote({ caged: true, image: "localhost/shrooms-workbench:desktop", github: true }, "laptop.office") + "|" + view.cagedNote({ caged: false }, "") + "]")
            view.nsHarness = "pi"
            console.error("HARNESS offered=" + offered + " sent=" + made.harness + " auto=" + made.auto_approve
                          + " label=[" + view.harnessLabel("pi") + "][" + view.harnessLabel("claude") + "]")
            view.loadHarnesses(null)
            view.agentCreating = true
            view.openSession(view.agentHosts[0], "shrooms")
            console.error("FORMCLOSED=" + !view.agentCreating)

            // A machine that cannot be reached: what was kept of it is shown,
            // marked, and not as working; replaced, not added to, once it answers.
            top.keptPhase = 1
            view.openSession(view.agentHosts[0], "shrooms")
            view.pumpAgent()
            var keptSeqs = view.agentEventsList.map(function(e) { return e.seq }).join(",")
            var keptWorking = view.agentWorking, keptAt = view.agentKept
            top.keptPhase = 2
            view.pumpAgent()
            console.error("KEPT seqs=" + keptSeqs + " kept=" + keptAt + " working=" + keptWorking
                          + " then=" + view.agentEventsList.map(function(e) { return e.seq }).join(",")
                          + " kept=" + view.agentKept + " rows=" + chatCount())
            // Read aloud: markdown as prose, in the reply's language; auto-play
            // reads only new replies, in order, one after another.
            console.error("SPEAKABLE=" + JSON.stringify(view.speakable("## Results\nThe **agent** found [three papers](https://x.io/a) and `go test` passed.\n\n```go\nfunc main() {}\n```\nSee https://example.org/x for more.")))
            console.error("UNDERSCORE=" + view.speakable("built `basecamp_voice_core.lgx` for x86_64, _really_"))
            console.error("CZECH=" + view.isCzech("Tak jo, to bylo fakt rychlé, tohle se mi líbí a Parakeet je rozhodně lepší.")
                          + "," + view.isCzech("Václav asked for the release notes to be written up properly before Friday, with the changes grouped by component and the breaking ones first."))
            view.openSession(view.agentHosts[0], "shrooms")
            view.pumpAgent()
            top.spoken = []
            view.readAloud(3, "## Running them\n\nFirst `internal/agent/session.go:654` is fixed. Then tests.")
            var sents = view.aloud.sentences.join("|"), firstKey = view.speakingKey
            view.pauseReading()
            var paused = view.aloud.paused && !top.speakingNow
            view.resumeReading()
            view.skipReading(1)
            var lit = view.aloudHtml().indexOf('#5AA9FF; color:#07090B">First session.go, line 654 is fixed.') > 0
            top.speakingNow = false
            view.pumpSpeech()              // that sentence said: the next
            top.speakingNow = false
            view.pumpSpeech()              // the last said: done
            var readSeq = top.spoken.join("|"), done = view.aloud === null
            view.setAutoPlay(view.agentOpen, true)
            top.spoken = []
            view.heardEvents([
                top.ev(2, "claude", { type: "assistant", message: { content: [ { type: "text", text: "Old, before auto-play." } ] } }),
                top.ev(20, "claude", { type: "assistant", message: { content: [ { type: "tool_use", id: "t9", name: "Bash", input: {} } ] } }),
                top.ev(21, "claude", { type: "user", message: { content: [ { type: "tool_result", tool_use_id: "t9", content: "ok" } ] } }),
                top.ev(22, "claude", { type: "assistant", message: { content: [ { type: "text", text: "Hotovo, všechno běží." } ] } }),
                top.ev(23, "claude", { type: "assistant", message: { content: [ { type: "thinking", thinking: "" }, { type: "text", text: "Second **reply**." } ] } }) ])
            view.pumpSpeech()              // nothing reading: the first starts
            var afterOne = top.spoken.join("|")
            view.pumpSpeech()              // still reading: waits
            top.speakingNow = false
            view.pumpSpeech()              // finished: the next
            view.heardEvents([ top.ev(23, "claude", { type: "assistant", message: { content: [ { type: "text", text: "Second **reply**." } ] } }) ])
            console.error("SPEAK sentences=" + JSON.stringify(sents) + " key=" + firstKey.split("/").slice(-2).join("/") + " paused=" + paused
                          + " lit=" + lit + " read=" + JSON.stringify(readSeq) + " done=" + done
                          + " auto=[" + afterOne + "] then=[" + top.spoken.join("|") + "] queue=" + view.speakQueue.length)
            view.setAutoPlay(view.agentOpen, false)

            // Usage: every machine asked from the period's first day, summed by
            // who asked, where it ran and which model.
            view.usageDays = 7
            view.loadUsage()
            view.pumpUsage()               // nobody has answered yet
            var waitingFirst = view.usageWaiting.join(",") + ":" + (view.usageRows === null)
            view.pumpUsage()
            var secs = view.usageSections()
            view.usageMeasure = "cost"
            var byCost = view.usageSections()[0].lines.map(function(l) { return l.name }).join(",")
            console.error("USAGE asked=" + top.usageAsked[0].replace(/since=\d{4}-\d{2}-\d{2}/, "since=D") + " who="
                          + secs[0].lines.map(function(l) { return l.name + ":" + view.usageFormat(l, "output") }).join(",")
                          + " where=" + secs[1].lines.map(function(l) { return l.name + ":" + l.turns }).join(",")
                          + " model=" + secs[2].lines.map(function(l) { return l.name }).join(",")
                          + " bycost=" + byCost + " busy=" + view.usageHours(secs[0].lines[0].busy)
                          + " since7=" + view.usageSince(7, "2026-10-05T12:00:00") + " sinceAll=[" + view.usageSince(0) + "]"
                          + " first=" + waitingFirst + " then=" + view.usageWaiting.length)
            // Plan limits: the same account on two machines shows once, newest
            // reading first, worded as the phone words it.
            var p0 = view.usagePlans[0]
            var older = view.planLimits("desk", { at: "2026-10-06T18:21:00+02:00", status: "allowed_warning", window: "five_hour",
                windows: { five_hour: { utilization: 0.98, resets_at: "2099-10-06T21:20:00+02:00" }, seven_day: { utilization: 0.49, resets_at: "2099-10-11T11:00:00+02:00" } } })
            var merged = view.planAccounts([older, p0])
            var now = Date.parse("2026-10-06T21:05:00+02:00")
            console.error("PLANS n=" + view.usagePlans.length + " windows=" + p0.windows.map(function(w) { return w.name }).join(",")
                          + " merged=" + merged.length + ":" + merged[0].machines.join("+") + ":" + merged[0].status
                          + " status=[" + view.planStatus(merged[0], now).replace(/back at .*/, "back at T") + "]"
                          + " warn=[" + view.planStatus(older, now) + "] label=" + view.planWindowLabel("seven_day"))
            function gl(five, status) { var g = view.planGlance([{ status: status || "allowed", windows: [{ name: "five_hour", utilization: five }, { name: "seven_day", utilization: 0.6 }] }]); return g.percent + "/" + g.level }
            console.error("GLANCE " + [gl(0.21), gl(0.5), gl(0.8), gl(0.3, "rejected")].join(" ")
                          + " live=" + (view.usageGlance ? view.usageGlance.percent + "/" + view.usageGlance.level : "none"))
            view.usageMeasure = "output"

            // The voice section: says what reads now, sets the natural one up.
            view.openVoice()
            var before = view.voiceText(view.voice)
            view.voiceAction("setup")
            var during = view.voiceText(view.voice)
            top.voiceNow = { installed: true, busy: false, step: "", error: "", engine: "piper", voice: "en_US-lessac-medium" }
            view.openVoice()
            console.error("VOICE before=[" + before + "] during=[" + during + "] after=[" + view.voiceText(view.voice) + "] calls=" + top.voiceCalls.join(","))

            // A click on a session card only schedules the opening. Run inside
            // the card, a refresh during the core call (Basecamp spins a nested
            // event loop for each) destroyed the card mid-handler and Qt
            // aborted. The abort itself needs that nested loop, which a test
            // cannot spin; this checks the work is not done in the handler.
            view.agentOpen = null
            var card = top.findByName(view, "sessionCardArea")
            card.clicked(null)
            console.error("CARDCLICK found=" + (card !== null) + " deferred=" + (view.agentOpen === null))
            // Nothing yet, then caught up with nothing: said, not blank.
            top.keptPhase = 4
            view.openSession(view.agentHosts[0], "shrooms")
            var waitingText = view.chatPlaceholder
            view.pumpAgent()
            console.error("PLACEHOLDER first=[" + waitingText + "] then=[" + view.chatPlaceholder + "]")
            top.keptPhase = 3
            view.openSession(view.agentHosts[0], "shrooms")
            view.pumpAgent()
            console.error("REMADE watch=" + top.lastWatch + " rows=" + view.agentEventsList.length)
            top.keptPhase = 0

            // Taking over a conversation from a terminal.
            view.loadConversations(view.agentHosts[0])
            console.error("CONVERSATIONS=" + view.conversations.length
                          + " TERMINAL=" + view.conversations[0].terminals[0].tmux
                          + " NAME=" + view.nameFor(view.agentHosts[0], view.conversations[0]))
            view.takeOver(view.agentHosts[0], view.conversations[0])
            var took = JSON.parse(top.lastPost)
            console.error("TAKEOVER name=" + took.name + " resume=" + took.resume + " open=" + view.agentOpen.session)
            // A machine with none of its own: answered, and empty — not "looking…".
            top.noConversations = true
            view.loadConversations(view.agentHosts[0])
            console.error("CONVEMPTY loaded=" + view.conversationsLoaded + " n=" + view.conversations.length)
            top.noConversations = false

            // Renamed: posted, and the session opens again under the new
            // name, with the copy moved by the core; a rename elsewhere,
            // seen in the stream, is followed the same way.
            console.error("RENAMEDIN=" + view.renamedIn([ev(3, "renamed", { from: "a", to: "b" }), ev(9, "renamed", { from: "shrooms-2", to: "logos" })], "shrooms-2")
                          + "," + view.renamedIn([ev(3, "renamed", { from: "a", to: "b" })], "shrooms"))
            view.renameOpenSession("logos")
            console.error("RENAMED=" + top.lastPostPath + " " + top.lastPost + " MOVED=" + top.lastMoveKept + " OPEN=" + view.agentOpen.session)
            console.error("MORE=" + view.moreTail(701, 1000) + "|" + view.moreLabel(700) + "|" + view.moreLabel(90))
            var ck = view.creditKeys(view.usageCreditsOf("pi5", [{ provider: "venice", key: "1a2b3c4d", balances: { DIEM: 5.62, USD: -0.03, BUNDLED_CREDITS: 0 }, at: "2026-10-07T12:00:00Z", resets_at: "2026-10-08T00:00:00Z" }])
                .concat(view.usageCreditsOf("proteus", [{ provider: "venice", key: "1a2b3c4d", balances: { DIEM: 5.4, USD: -0.03 }, at: "2026-10-07T12:05:00Z" }])))
            console.error("CREDITS n=" + ck.length + " machines=" + ck[0].machines.join("+") + " line=" + view.creditLine(ck[0]))
            var now17 = Date.parse("2026-10-07T17:00:00+02:00")
            var stale = { machines: ["atlas"], at: Date.parse("2026-10-07T13:00:00+02:00"), status: "allowed_warning", window: "five_hour", overage: false,
                windows: [{ name: "five_hour", utilization: 0.8, resetsAt: Date.parse("2026-10-07T15:00:00+02:00") }, { name: "seven_day", utilization: 0.69, resetsAt: Date.parse("2026-10-11T11:00:00+02:00") }] }
            var fresh = { machines: ["laptop"], at: Date.parse("2026-10-07T16:59:00+02:00"), status: "allowed", window: "", overage: false,
                windows: [{ name: "five_hour", utilization: 0.28, resetsAt: Date.parse("2026-10-07T20:30:00+02:00") }, { name: "seven_day", utilization: 0.72, resetsAt: Date.parse("2026-10-11T11:00:00+02:00") }] }
            var both = view.planAccounts([stale, fresh], now17), alone = view.planAccounts([stale], now17)[0]
            console.error("RENEWED n=" + both.length + " machines=" + both[0].machines.join("+") + " share=" + both[0].windows[0].utilization
                          + " alone=" + alone.windows[0].renewed + "," + alone.windows[0].utilization + ",[" + alone.status + "] glance=" + view.planGlance([alone]).percent)
            console.error("TASKS note=" + view.taskNote("proj:m1", "stalled", "no progress after 5 reminders") + " | " + view.tasksLabel(2) + " | " + view.stalledLabel(1))
            var fnow = Date.parse("2026-10-08T12:00:00+02:00")
            var fp = view.planLimits("laptop", { at: "2026-10-08T12:00:00+02:00", status: "allowed", windows: {
                five_hour: { utilization: 0.5, resets_at: "2099-10-08T14:00:00+02:00", projected: 1.1, runs_out_at: "2026-10-08T13:40:00+02:00" },
                seven_day: { utilization: 0.6, resets_at: "2099-10-11T11:00:00+02:00", projected: 0.8 } } })
            var fc = view.usageCreditsOf("pi5", [{ provider: "venice", key: "e31a16d5", balances: { DIEM: 4 }, at: "2026-10-08T10:00:00Z", resets_at: "2099-10-09T00:00:00Z", left_at_refill: 3.04 }])[0]
            console.error("FORECAST " + view.windowForecast(fp.windows[0], fnow) + " | " + view.windowForecast(fp.windows[1], fnow) + " | " + view.creditForecast(fc, fnow))
            // Markdown as the phone draws it (MarkdownTest's cases).
            var reply = "## Done\n\nThe fix is **pushed** as `02befc2`.\n\n- first\n- second\n  continued\n1. one\n> a quote\n\n```\nsudo make install\n```\n---"
            var mh = view.mdHtml(reply)
            var mi = view.mdInline("a **bold** and *it* `code` [link](http://x)")
            var plain = ["moveEphemeralPorts in local_build.conf", "2*3*4", "~/.config/systemd/user"].every(function(t) {
                var sp = view.mdInline(t); return sp.map(function(x) { return x.text }).join("") === t && sp.every(function(x) { return !x.bold && !x.italic }) })
            var u = "https://github.com/vpavlin/shrooms/blob/master/docs/adr/037-agents-in-cages.md"
            var boldLink = view.mdInline("See **" + u + "** now").filter(function(x) { return x.link })[0]
            var dot = view.mdInline("Same link: http://vps.office.mesh:8099/shrooms-preview.apk. Done").filter(function(x) { return x.link })[0]
            var checks = [
                mh.indexOf("color:" + view.cPhosphor.toString().toLowerCase()) >= 0 || mh.indexOf("#35f0a0") >= 0,   // heading in green
                /02befc2<\/span>/.test(mh) && mh.toLowerCase().indexOf("#c8e64a") >= 0,                              // inline code in chartreuse
                mh.indexOf("second continued") >= 0, mh.indexOf("1.</span>") >= 0, mh.indexOf("▍") >= 0,
                /<pre[^>]*><a href="copy:[^"]*"[^>]*><span[^>]*>sudo make install<\/span><\/a><\/pre>/.test(mh), mh.indexOf("<hr/>") >= 0,
                mi.some(function(x) { return x.bold && x.text === "bold" }), mi.some(function(x) { return x.italic && x.text === "it" }),
                mi.some(function(x) { return x.code && x.text === "code" }), mi.some(function(x) { return x.link === "http://x" && x.text === "link" }),
                plain, boldLink && boldLink.link === u && boldLink.bold,
                dot && dot.link === "http://vps.office.mesh:8099/shrooms-preview.apk",
                view.mdInline("`curl http://x`").every(function(x) { return !x.link }),
                /<pre[^>]*><a href="copy:make%20test"[^>]*><span[^>]*>make test<\/span><\/a><\/pre>/.test(view.mdHtml("Look:\n```\nmake test\n")),                              // an unclosed fence
                (view.mdHtml("| a | b |\n|---|---|\n| 1 | 2 |\n| 3 | 4 |").match(/<tr>/g) || []).length === 3,
                view.mdHtml("<b>x</b> & y").indexOf("&lt;b&gt;x&lt;/b&gt; &amp; y") >= 0                             // text, not HTML
            ]
            console.error("MDHTML " + checks.map(function(c) { return c ? "1" : "0" }).join(""))
            // Deleting the open session: asks what the phone asks, then goes.
            console.error("DELETETEXT=" + (view.deleteSessionText("working").indexOf("cut off") > 0)
                          + "," + (view.deleteSessionText("idle").indexOf("conversation itself is kept") > 0))
            view.askDelete()
            console.error("DIALOG=" + view.deleteDialogOpen())
            view.deleteOpenSession()
            console.error("DELETED=" + top.lastDelete + " OPEN=" + (view.agentOpen === null ? "none" : view.agentOpen.session))
            console.error("CALLS=" + top.calls.filter(function(c) { return c.indexOf("agent") === 0 })
                          .filter(function(c, i, a) { return a.indexOf(c) === i }).join(","))
            // Then the board, as the agents answer with their last lines
            // and their tasks.
            // The tasks panel's rows, from the machines' own answers.
            var tnow = Date.parse("2026-10-09T12:00:00Z")
            var trows = view.taskRows(top.taskHosts)
            console.error("TASKROWS " + trows.map(function(r) { return r.group + ":" + r.id.split(":")[1] + ":" + view.ageLabel(r, tnow) }).join(","))
            console.error("TASKORDER " + view.taskGroupOrder.join(",") + " labels=" + view.taskGroupOrder.map(view.taskGroupLabel).join("/"))
            console.error("TASKNAME " + [
                view.taskTitleOf({ metadata: { "shrooms/title": "named by the asker" }, history: [{ role: "ROLE_USER", parts: [{ text: "From X: the request" }] }] }),
                view.taskTitleOf({ history: [{ role: "ROLE_USER", parts: [{ text: "From X: the request\n\nand more" }] }] }),
                view.taskTitleOf({ history: [{ role: "ROLE_AGENT", parts: [{ text: "not the request" }] }, { role: "ROLE_USER", parts: [{ text: "the real ask" }] }] }),
                view.taskTitleOf({ summary: "the worker's own summary" }),
                view.taskTitleOf({})
            ].join("|"))
            console.error("ASKER " + [view.askerName("laptop.default (laptop/SPEL)"), view.askerName("pi5 (pi5/jimmy)"),
                                      view.askerName("laptop (laptop/shrooms, in a cage)"), view.askerName("pi5.office"),
                                      view.askerName("")].join(","))
            console.error("CAGED " + [view.askerCaged("laptop (laptop/shrooms, in a cage)"),
                                      view.askerCaged("laptop.default (laptop/SPEL)")].join(","))
            console.error("AGE " + [view.ageOf("2026-10-09T11:59:30Z", tnow), view.ageOf("2026-10-09T11:00:00Z", tnow),
                                    view.ageOf("2026-10-09T02:00:00Z", tnow), view.ageOf("nonsense", tnow)].join(","))
            // The panel's rows, with a header per non-empty group.
            var prows = view.taskPanelRows(top.taskHosts)
            console.error("PANEL " + prows.map(function(r) { return r.kind === "header" ? "[" + r.label + " " + r.count + "]" : r.id.split(":")[1] }).join(" "))
            // Folded, Done keeps its header and count but not its rows.
            var folded = view.taskPanelRows(top.taskHosts, "", false)
            console.error("PANELFOLDED " + folded.map(function(r) { return r.kind === "header" ? "[" + r.label + " " + r.count + "]" : r.id.split(":")[1] }).join(" ") + " |")
            // Who a session takes files from (ADR-048): an entry is MACHINE/SESSION or a whole machine.
            console.error("FILESALLOW " + [view.filesValid("jimmy-crib/vpavlin"), view.filesValid(" pi5/* "), view.filesValid("jimmy-crib"),
                view.filesValid("a/b/c"), view.filesAllow(["pi5/jimmy"], "pi5/jimmy").join("+"), view.filesAllow(["pi5/jimmy"], "nonsense").join("+"),
                view.filesAllow(["pi5/jimmy"], "atlas/*").join("+"), view.filesDeny(["pi5/jimmy", "atlas/*"], "pi5/jimmy").join("+")].join(",") + " |")
            // An ACK takes the row away at once, before its agent says so; undone, it is back.
            view.markAcked(["review:m4"], true)
            var afterAck = view.taskRows(top.taskHosts).map(function(r) { return r.id.split(":")[1] }).join(",")
            view.markAcked(["review:m4"], false)
            console.error("ACKEDHERE " + afterAck + " | " + view.taskRows(top.taskHosts).map(function(r) { return r.id.split(":")[1] }).join(","))
            // The links carrying their tasks, and the load on each card.
            var links = view.boardLinkList(top.taskHosts)
            console.error("LINKS " + links.map(function(e) { return e.from + ">" + e.to + ":" + e.count + ":" + e.tone + ":" + view.linkLabel(e) }).join(","))
            var load = view.cardLoad(top.taskHosts)
            console.error("LOAD " + Object.keys(load).sort().map(function(k) { return k + "=" + view.loadLabel(load[k]) }).join(","))
            // Amber means "a person is needed", so the card load must only be amber for a
            // card whose task is actually in needs-you. Pure, so it can be pinned.
            var nyKeys = prows.filter(function(r) { return r.group === "needs-you" })
                .map(function(r) { return r.machine + "/" + r.session })
            // A card is amber only when one of ITS tasks is in needs-you. Built by hand: the
            // fixture puts every task on one host/session, so a key alone cannot tell the
            // groups apart - and a check that cannot fail is worse than none.
            function nyRow(g) { return { kind: "task", group: g, machine: "x", session: "y" } }
            console.error("NEEDSYOU " + [view.cardNeedsYou("x/y", [nyRow("needs-you")]),
                view.cardNeedsYou("x/y", [nyRow("working")]),
                view.cardNeedsYou("x/y", [nyRow("stalled")]),
                view.cardNeedsYou("x/y", [nyRow("unacked")]),
                view.cardNeedsYou("x/y", [{ kind: "header", label: "Needs you", count: 1 }]),
                view.cardNeedsYou("x/y", []),
                view.cardNeedsYou("other/z", [nyRow("needs-you")])].join(","))
            console.error("TASKROW1 " + (trows[0] ? trows[0].title + " | from=" + trows[0].asker + " to=" + trows[0].worker
                          + " | latest=" + trows[0].latest + " | " + view.ageLabel(trows[0], tnow)
                          + " | hasref=" + (trows[0].ref !== undefined) : "none"))
            // The label takes the CLOCK as an argument, so a row's age is a binding on it
            // and the row itself does not change - the panel shifting under the cursor was
            // exactly that (2026-10-10).
            console.error("AGELABEL " + [view.ageLabel({ at: "2026-10-09T11:00:00Z", quiet: true }, tnow),
                                         view.ageLabel({ at: "2026-10-09T10:00:00Z", quiet: false }, tnow),
                                         view.ageLabel({})].join(","))
            // a task with no title at all must still be a row, not a gap
            var bare = view.taskRows([{ name: "pi5", address: "fd00::9",
                sessions: [{ name: "jimmy" }],
                tasks: [{ id: "jimmy:no-title-1", status: { state: "TASK_STATE_WORKING", timestamp: "2026-10-09T11:00:00Z" },
                          metadata: { "shrooms/session": "jimmy" } }] }], tnow)
            console.error("BAREROW " + (bare[0] ? bare[0].title : "none"))
            // An ACK the agent refused must not read as success. The DECISION is pure
            // (ackRefusal), because calling ackTask() starts an async refresh a test cannot
            // wait on - the first version of this case hung the whole suite exactly that way.
            console.error("ACKERR " + view.ackRefusal(JSON.stringify(
                { jsonrpc: "2.0", id: "ack-review:m1", error: { code: -32001, message: "no such task" } }))
                + " | " + view.ackRefusal(JSON.stringify({ ok: true }))
                + " | " + view.ackRefusal(null))
            top.boardFind = top.boardHosts
            view.refreshAgents()
            view.setBoard(true)
            boardTimer.start()
        }
    }
    // The tasks the panel is built from: one per group, one acked (which is
    // finished and must not be listed), one stalled, and one whose name the
    // asker set. Timestamps are fixed so the ages and the order are exact.
    readonly property var taskHosts: [
        { name: "laptop", address: "fd00::1", sessions: [ { name: "review" }, { name: "shrooms" } ], tasks: [
            { id: "review:m1", status: { state: "TASK_STATE_INPUT_REQUIRED", timestamp: "2026-10-09T11:00:00Z",
                                         message: { parts: [{ text: "which of the two?" }] } },
              metadata: { "shrooms/session": "review", "shrooms/from": "pi5 (pi5/jimmy)" },
              history: [{ role: "ROLE_USER", parts: [{ text: "From Jimmy: review the module\n\nand the second line" }] }] },
            { id: "review:m2", status: { state: "TASK_STATE_WORKING", timestamp: "2026-10-09T11:30:00Z",
                                         message: { parts: [{ text: "reading it now" }] } },
              metadata: { "shrooms/session": "review", "shrooms/from": "laptop (laptop/shrooms)",
                          "shrooms/title": "the asker named this one" } },
            { id: "review:m3", status: { state: "TASK_STATE_WORKING", timestamp: "2026-10-09T10:00:00Z" },
              metadata: { "shrooms/session": "review", "shrooms/from": "pi5 (pi5/jimmy)", "shrooms/stalled": true } },
            // input-required AND stalled: a task waiting on a person stays in Needs you
            { id: "review:m6", status: { state: "TASK_STATE_INPUT_REQUIRED", timestamp: "2026-10-09T11:45:00Z" },
              metadata: { "shrooms/session": "review", "shrooms/from": "laptop (laptop/shrooms, in a cage)",
                          "shrooms/stalled": true } },
            // the session in the id is the OLD name after a rename; shrooms/session is the new one
            { id: "oldname:m7", status: { state: "TASK_STATE_WORKING", timestamp: "2026-10-09T11:50:00Z" },
              metadata: { "shrooms/session": "review", "shrooms/from": "pi5.office" } },
            { id: "review:m4", status: { state: "TASK_STATE_COMPLETED", timestamp: "2026-10-09T09:00:00Z",
                                         message: { parts: [{ text: "done, and here is why" }] } },
              metadata: { "shrooms/session": "review", "shrooms/from": "pi5 (pi5/jimmy)" } },
            { id: "review:m5", status: { state: "TASK_STATE_COMPLETED", timestamp: "2026-10-09T08:00:00Z" },
              metadata: { "shrooms/session": "review", "shrooms/from": "pi5 (pi5/jimmy)", "shrooms/acknowledged": true } }
        ] },
        { name: "pi5", address: "fd00::2", sessions: [ { name: "jimmy" } ], tasks: [] }
    ]
    function task(id, session, from, state, extra) {
        return { id: id, status: { state: state }, metadata: Object.assign({ "shrooms/session": session, "shrooms/from": from }, extra || {}) }
    }
    property var boardFind: null
    readonly property var boardHosts: [
        { name: "laptop", mesh: "office", address: "fd00::1", list: { sessions: [
            { name: "shrooms", state: "working", running: true, turns: 0, tail: ["› you: can you check the board?", "Looking at Main.qml.", "▸ Read basecamp-agents/Main.qml", "▸ Bash go test ./internal/agent/"] },
            { name: "notes", state: "idle", running: true, turns: 0, preview: "an agent from before tail" } ] },
          tasks: { tasks: [
            task("notes:m1", "notes", "proteus (proteus/review)", "TASK_STATE_INPUT_REQUIRED"),
            task("shrooms:m2", "shrooms", "duet (duet/claude)", "TASK_STATE_WORKING"),            // no card: dropped
            task("shrooms:m3", "shrooms", "pi5 (pi5/jimmy)", "TASK_STATE_COMPLETED") ] } },  // finished: dropped
        { name: "pi5", mesh: "office", address: "fd00::2", list: { sessions: [
            { name: "jimmy", state: "idle", running: true, starred: true, turns: 0, tail: ["◆ task jimmy:m4 stalled"] } ] },
          tasks: { tasks: [ task("jimmy:m4", "jimmy", "laptop (laptop/shrooms)", "TASK_STATE_WORKING", { "shrooms/stalled": true }) ] } },
        { name: "proteus", mesh: "office", address: "fd00::3", list: { sessions: [
            { name: "review", state: "working", running: true, turns: 0, tail: ["Reviewing the diff."] } ] },
          // A machine named a little otherwise in the claim.
          tasks: { tasks: [ task("review:m5", "review", "pi5 (pi5.home/jimmy)", "TASK_STATE_WORKING") ] } } ]
    Timer { id: escTimer; interval: 600; property var report: null; onTriggered: report() }

    // The jump that has to reach OUTSIDE the loaded tail: 400 events, the default
    // 300-event tail (so the window starts at seq 101), and a task that arrived at
    // seq 20. The reviewer's live pass found this path has never worked; the harness
    // could not see it because its core answered from the beginning every time.
    property int deepTick: 0
    // litTimer clears agentLit after 4s, and this test runs longer than that: the
    // highest value seen is the answer, not the value at the end.
    property real deepLitMax: 0
    property bool deepPending: false
    function startDeepJump() {
        var evs = []
        for (var i = 1; i <= 400; i++)
            evs.push({ seq: i, id: "m" + i, time: "2026-10-09T10:00:00Z", kind: "message",
                       data: { type: "assistant", text: "event " + i } })
        top.windowEvents = evs
        top.windowed = true
        // A stale "load them" quiet jump: armed for a seq nobody jumps to, and left set.
        // It used to swallow the NEXT jump - lit nothing, moved nothing (2026-10-10).
        view.jumpQuietFor = 999
        console.error("DEEPJUMP quietFor=" + view.jumpQuietFor + " isQuietFor20=" + view.isQuietJump(20))
        var h = view.agentHosts[0]
        console.error("DEEPJUMP hosts=" + view.agentHosts.map(function(x) { return x.name }).join(",")
                      + " sessions=" + (h ? h.sessions.map(function(x) { return x.name }).join(",") : "none"))
        view.openTaskRow({ kind: "task", id: "jimmy:deep-1", machine: h.name,
                           session: h.sessions[0].name, address: h.address })
        deepTimer.start()
    }
    Timer {
        id: deepTimer
        interval: 300; repeat: true
        onTriggered: {
            top.deepTick++
            if (view.agentLit > top.deepLitMax) top.deepLitMax = view.agentLit
            if (view.jumpPending) top.deepPending = true
            if (top.deepTick >= 30) {
                var evs = view.agentEventsList
                var has = false
                for (var i = 0; i < evs.length; i++) if (evs[i].seq === 20) has = true
                var fresh = false
                for (var k = 0; k < top.watchTails.length; k++) if (top.watchTails[k] < 0) fresh = true
                console.error("DEEPJUMP litMax=" + top.deepLitMax + " want=20 has20=" + has
                              + " evs=" + evs.length + " tail=" + top.watchTail + " jumpTo=" + view.jumpTo
                              + " quietFor=" + view.jumpQuietFor + " fresh=" + fresh
                              + " tails=" + top.watchTails.join("/")
                              + " settled=" + view.jumpSettled + " settledCaught=" + view.jumpSettledCaught
                              + " pendingSeen=" + top.deepPending)
                deepTimer.stop()
                Qt.quit()
            }
        }
    }
    Timer {
        id: boardTimer
        interval: 400
        property bool settled: false
        onTriggered: {
            // A session an earlier check left opening (a deferred call): the
            // board is what shows with none open.
            if (!settled) { settled = true; view.agentOpen = null; view.agentCreating = false; view.closeDialogs(); restart(); return }
            var links = top.findByName(view, "boardLinks")
            // The badge hit rects come from a PAINT, so they are read here rather than in
            // the section above. The live pass found every badge drawn at NaN because the
            // call passed ctx.height, which a Context2D does not have - a pure check on
            // badgeY could not see that, so this reads what the canvas actually recorded.
            var bhBad = view.badgeHit.filter(function(b) {
                return !(isFinite(b.x) && isFinite(b.y) && isFinite(b.w) && isFinite(b.h)) }).length
            console.error("BADGEHIT n=" + view.badgeHit.length + " bad=" + bhBad)
            // An ack goes to a /v1/ path: the core forwards only those, so /a2a/<session>
            // could never work from Basecamp.
            console.error("ACKPATH " + view.ackPath("jimmy:m1") + "," + view.ackPath(""))
            // Tapping a task opens its session the way the LIST does. Passing a tail made
            // the watch "-1" (fresh negates it), which is ONE event: the live pass opened a
            // session with a single line and "N earlier events not loaded" (2026-10-10).
            var th = view.agentHosts[0]
            if (th && th.sessions.length > 0) {
                view.openTaskRow({ kind: "task", id: th.name + "/" + th.sessions[0].name + ":m1",
                                   machine: th.name, session: th.sessions[0].name, address: th.address })
                console.error("TASKWATCH " + top.lastWatch)
                // Put the view back: this check opened a session, and the BOARD
                // check below is about the board.
                view.showBoard()
            }
            console.error("UNACKED " + view.unackedRows([
                { kind: "task", group: "unacked", id: "a" }, { kind: "task", group: "working", id: "b" },
                { kind: "task", group: "needs-you", id: "c" }, { kind: "task", group: "unacked", id: "d" },
                { kind: "header", group: "unacked" }]).map(function(r) { return r.id }).join(","))
            // A row must not carry a clock-derived field, and its label must move with the
            // clock: that is what keeps the panel still while the ages tick.
            var t0 = Date.parse("2026-10-09T12:00:00Z")
            var st = view.taskRows(top.taskHosts)[0]
            console.error("STABLE " + ("age" in st) + "," + (st.at !== undefined) + ","
                          + view.ageLabel(st, t0) + "|" + view.ageLabel(st, t0 + 3600000))
            // LIVE-SHAPED: the one open task on the jimmy<->shrooms arc runs shrooms ->
            // jimmy, and the agent's host name for that machine is "laptop". Tapping that
            // arc's badge must FIND the task: the live pass filtered to the opposite
            // direction (two badges at one midpoint, the hidden one returned) and the panel
            // came out empty.
            var liveHosts = [{ name: "laptop", address: "fd00::7",
                sessions: [{ name: "shrooms" }, { name: "jimmy" }],
                tasks: [{ id: "jimmy:cli-20261009T183346-04a4f81dbc3a0c9e",
                          status: { state: "TASK_STATE_INPUT_REQUIRED", timestamp: "2026-10-09T11:00:00Z" },
                          metadata: { "shrooms/session": "jimmy",
                                       "shrooms/from": "laptop.default (laptop/shrooms)" } }] }]
            var ll = view.boardLinkList(liveHosts)
            var liveRows = view.taskPanelRows(liveHosts)
            console.error("LIVELINK n=" + ll.length + " pair=" + (ll[0] ? ll[0].from + ">" + ll[0].to : "none")
                + " count=" + (ll[0] ? ll[0].count : 0) + " tone=" + (ll[0] ? ll[0].tone : ""))
            // The first TASK row, not the first row: the panel starts with a group header.
            var liveTask = view.taskRows(liveHosts)[0]
            console.error("LIVEFILTER " + view.panelPair(liveTask) + " n="
                + view.linkFiltered(liveRows, view.panelPair(liveTask)).length)
            console.error("PAIRKEY " + [view.pairKeyOf("b/x", "a/y"), view.pairKeyOf("a/y", "b/x"),
                view.pairKeyOf("a/y", "a/y"), view.pairKeyOf("", "a/y")].join("|"))
            // The jump must land on the task ARRIVING, not the newest mention of it.
            var hits = [{ seq: 9, snippet: "[shrooms task jimmy:m1 \u2014 more from laptop.default (laptop/shrooms)]" },
                        { seq: 5, snippet: "[shrooms task jimmy:m1 from laptop.default (laptop/shrooms)] the arrival" }]
            var bh1 = view.bestHit(hits, "jimmy:m1")
            var bh2 = view.bestHit([{ seq: 3, snippet: "nothing to do with it" }], "jimmy:m1")
            console.error("BESTHIT " + (bh1 ? bh1.seq : "none") + "," + (bh2 ? bh2.seq : "none") + ","
                + (view.bestHit([], "jimmy:m1") === null))
            console.error("BOARD cards=" + view.boardCardList.map(function(c) { return c.key }).join(",")
                          + " edges=" + view.boardEdgeList.map(function(e) { return e.from + ">" + e.to + ":" + e.state }).join(",")
                          + " drawn=" + (links ? links.drawn : -1)
                          + " list=" + top.findByName(view, "agentList").visible + " flow=" + top.findByName(view, "boardFlow").visible
                          + " tasks=" + view.agentHosts[0].tasks.length)
            top.grabToImage(function(img) {
                img.saveToFile(top.out)
                console.error("SAVED " + top.out)
                // A card opens its session in the whole panel; "← board" goes back.
                top.findByName(view, "boardCardArea").clicked(null)
                Qt.callLater(function() {
                    var back = top.findByName(view, "backToBoard")
                    var opened = view.agentOpen ? view.agentOpen.session : "none"
                    var shown = back.visible && !top.findByName(view, "boardFlow").visible && top.findByName(view, "agentList").visible
                    // Esc goes back to the board, but not while a dialog has it.
                    view.askDelete()
                    var escHeld = !view.escToBoard() && view.agentOpen !== null
                    view.closeDialogs()
                    // Once the dialog has finished closing.
                    escTimer.report = function() {
                        var escBack = view.escToBoard()
                        Qt.callLater(function() {
                            console.error("BOARDOPEN open=" + opened + " withlist=" + shown + " esc=" + escHeld + "," + escBack + " back=" + (view.agentOpen === null) + "," + top.findByName(view, "boardFlow").visible + "," + !top.findByName(view, "agentList").visible + "," + back.visible)
                            top.startDeepJump()
                        })
                    }
                    escTimer.start()
                })
            })
        }
    }
    // A refresh hands the session list a new array of hosts: where the
    // reader had scrolled to stays (it went back to the top, 2026-10-07).
    Timer {
        interval: 2600; running: true
        onTriggered: {
            var many = []
            for (var i = 0; i < 12; i++) many.push({ name: "m" + i, address: "fd00::" + (i + 1), mesh: "office", lastSeen: 1,
                sessions: [{ name: "a" + i, state: "idle", turns: 0 }, { name: "b" + i, state: "idle", turns: 0 }] })
            view.agentHosts = many
            Qt.callLater(function() {
                view.hostScroll(180)
                view.agentHosts = many.slice()
                // Read two passes later, once the list has been rebuilt —
                // before the view's own poll can replace the hosts.
                Qt.callLater(function() { Qt.callLater(function() { top.hostScrollSeen = Math.round(view.hostScroll()) }) })
            })
        }
    }
    property int hostScrollSeen: -1
    function chatCount() { return view.chatModelCount() }
}
