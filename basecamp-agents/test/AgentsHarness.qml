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
        list: { sessions: [
            { name: "shrooms", dir: "/home/someone/logos-vpn", state: "waiting", pending: 1, running: true,
              last_seq: 9, last_time: "2026-10-03T14:27:31+02:00", auto_approve: false,
              context_used: 678705, context_window: 1000000, model: "claude-opus-5[1m]",
              preview: "The view loads and the existing checks pass." },
            { name: "notes", dir: "/home/someone/notes", state: "idle", pending: 0, running: false,
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
    property string lastWatch: ""
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
    property var usageBody: ({ machine: "laptop", rows: [
        { day: "2026-10-05", session: "shrooms", by: "nothing.office", model: "claude-opus-5[1m]", turns: 3, input: 10, cache_read: 1000, cache_write: 200, output: 900, cost_usd: 1.5, busy_ms: 120000 },
        { day: "2026-10-05", session: "notes", by: "", model: "ollama/qwen3", turns: 5, input: 50, cache_read: 0, cache_write: 0, output: 2000, cost_usd: 0, busy_ms: 3600000 } ] })
    property var voiceNow: ({ installed: false, busy: false, step: "", error: "", engine: "spd-say", voice: "en_US-lessac-medium" })
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
                if (method === "status") return JSON.stringify({ name: "desk",
                    meshes: [ { label: "office", overlay: "fdb0:9afc:a5ef:1111:2222:3333:4444:5555" } ], peers: [
                    { name: "laptop", mesh: "office", overlay: "fdb0:9afc:a5ef:388c:8264:7716:36fc:64eb", online: true } ] })
                // The same machine answering on a second mesh address.
                if (method === "agentsFind" && top.findNone) return JSON.stringify([])
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
                if (method === "agentWatch") { top.lastWatch = args.join(" "); return JSON.stringify({ ok: true }) }
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
                    { name: "pi", title: "pi", caps: { approve: false, takeover: false } } ] })
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
            var kinds = []
            for (var i = 0; i < chatCount(); i++) kinds.push(view.chatModelAt(i).kind)
            console.error("ROWS=" + kinds.join(","))
            console.error("WATCH=" + top.lastWatch)
            view.stopTurn()
            console.error("STOPPED=" + top.lastPostPath)
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

            // Deleting the open session: asks what the phone asks, then goes.
            console.error("DELETETEXT=" + (view.deleteSessionText("working").indexOf("cut off") > 0)
                          + "," + (view.deleteSessionText("idle").indexOf("conversation itself is kept") > 0))
            view.askDelete()
            console.error("DIALOG=" + view.deleteDialogOpen())
            view.deleteOpenSession()
            console.error("DELETED=" + top.lastDelete + " OPEN=" + (view.agentOpen === null ? "none" : view.agentOpen.session))
            console.error("CALLS=" + top.calls.filter(function(c) { return c.indexOf("agent") === 0 })
                          .filter(function(c, i, a) { return a.indexOf(c) === i }).join(","))
            top.grabToImage(function(img) {
                img.saveToFile(top.out)
                console.error("SAVED " + top.out)
                Qt.quit()
            })
        }
    }
    function chatCount() { return view.chatModelCount() }
}
