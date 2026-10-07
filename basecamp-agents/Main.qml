import QtQuick
import QtQuick.Controls
import QtQuick.Layouts
import QtQuick.Dialogs

// Shrooms Agents (docs/agents.md): the Claude Code sessions on the owner's
// machines, talked to over the shrooms mesh. What grows out of the mycelium —
// a module of its own beside the shrooms one, sharing its core (shrooms_core),
// which does all the networking: Basecamp's sandbox blocks it here.
Item {
    id: root
    width: 1280; height: 800

    // The shrooms palette: the same app family, the same meanings.
    readonly property color cVoid:     "#07090B"
    readonly property color cPanel:    "#0E1216"
    readonly property color cLine:     "#1C2229"
    readonly property color cAsh:      "#6B7680"
    readonly property color cBone:     "#D6DDE3"
    readonly property color cPhosphor: "#35F0A0"
    readonly property color cAmber:    "#F0B429"
    readonly property color cRust:     "#E05252"
    readonly property color cViolet:   "#9A7BFF"
    readonly property color cSky:        "#5AA9FF"
    readonly property color cBlossom:    "#FF6FB5"
    readonly property color cChartreuse: "#C8E64A"
    readonly property var meshTints: [cSky, cBlossom, cChartreuse, cAsh]

    readonly property real autoScale: Math.max(1.0, Math.min(1.45, root.width / 2000))
    property real uiNudge: 0
    readonly property real uiScale: Math.max(0.8, Math.min(2.2, autoScale + uiNudge))
    function fs(n) { return Math.round(n * root.uiScale) }
    function sz(n) { return Math.round(n * root.uiScale) }

    // Basecamp's bridge to the core module. A property, so a test harness can
    // hand the view a stand-in (test/AgentsHarness.qml).
    property var bridge: typeof logos !== "undefined" ? logos : null
    readonly property bool haveCore: !!bridge && !!bridge.callModule
    function callCore(method, args) {
        if (!haveCore) return ""
        try {
            return String(bridge.callModule("shrooms_core", method, args || []))
        } catch (e) {
            return ""
        }
    }

    // The daemon's status, for this device's addresses and its peers: where
    // agents may be.
    property var st: ({})
    property var peers: []
    property string problem: ""
    function reload() {
        var d = unwrap(callCore("status", []))
        if (d && typeof d === "object" && !d.error) {
            root.st = d
            root.peers = d.peers || []
            root.problem = ""
        } else if (d && d.error) {
            root.problem = d.error + (d.detail ? " — " + d.detail : "")
        }
    }
    Timer { interval: 5000; running: root.haveCore; repeat: true; triggeredOnStart: true; onTriggered: root.reload() }

    // What the last action said, shown at the bottom until the next one.
    property string said: ""
    property bool saidBad: false

    TextEdit { id: clipboard; visible: false; width: 0; height: 0 }
    function copyText(s) {
        if (!s) return
        clipboard.text = String(s)
        clipboard.selectAll()
        clipboard.copy()
        root.said = "copied"
        root.saidBad = false
    }
    // Bare URLs as links, by the phone's rule (Markdown.kt bareUrl): no
    // trailing punctuation or emphasis marks (**https://…** kept its closing
    // ** in the link), nothing inside brackets or quotes.
    readonly property var bareUrl: /https?:\/\/[^\s<>()\[\]`"']+[^\s<>()\[\]`"'.,;:!?*_~]/g
    // Markdown with its bare URLs made autolinks (<url>), leaving code — fenced
    // or inline — and URLs already in a link alone. Qt's markdown does not
    // link a bare URL by itself.
    function linkMarkdown(md) {
        var lines = String(md || "").split("\n"), fenced = false
        for (var i = 0; i < lines.length; i++) {
            if (/^\s*(```|~~~)/.test(lines[i])) { fenced = !fenced; continue }
            if (fenced) continue
            var parts = lines[i].split("`")
            for (var j = 0; j < parts.length; j += 2) {
                parts[j] = parts[j].replace(bareUrl, function(u, at, whole) {
                    var before = at > 0 ? whole.charAt(at - 1) : ""
                    return (before === "(" || before === "<" || before === "[") ? u : "<" + u + ">"
                })
            }
            lines[i] = parts.join("`")
        }
        return lines.join("\n")
    }
    // Plain text — what somebody typed, where a * is just a * — as rich text
    // with only its URLs made links.
    function linkPlain(t) {
        var esc = String(t || "").replace(/&/g, "&amp;").replace(/</g, "&lt;").replace(/>/g, "&gt;")
        esc = esc.replace(bareUrl, function(u) { return '<a href="' + u.replace(/"/g, "%22") + '" style="color:#5AA9FF">' + u + "</a>" })
        return '<span style="white-space:pre-wrap">' + esc + "</span>"
    }
    // Basecamp's sandbox blocks every http and https URL in a view, so
    // Qt.openUrlExternally does nothing here: the core opens it (xdg-open).
    // Copied, and said so, when even that cannot.
    function openUrl(u) {
        if (!u) return
        var r = unwrap(callCore("agentOpenUrl", [String(u)]))
        if (r && r.ok) return
        if (Qt.openUrlExternally(u)) return
        copyText(u)
        root.said = "could not open " + u + (r && (r.detail || r.error) ? " (" + explain(r.detail || r.error) + ")" : "") + " — copied it instead"
        root.saidBad = true
    }

    // Mesh colours as the shrooms view assigns them: by the mesh's place in
    // this device's sorted list.
    function meshTint(label) {
        var ls = (root.st && root.st.meshes ? root.st.meshes : []).map(function(m) { return m.label }).sort()
        var i = Math.max(0, ls.indexOf(label))
        return meshTints[i % meshTints.length]
    }

    property bool prefsLoaded: false
    // The machines-and-sessions list's width, set by dragging the divider and
    // kept (agent_list_width), in unscaled pixels like everything sz() takes.
    // Bounded so neither pane can be dragged away.
    property real listWidth: 320
    function listWidthPx() { return Math.max(sz(200), Math.min(sz(listWidth), agentsPanel.width * 0.6)) }
    function loadPrefs() {
        if (prefsLoaded || !haveCore) return
        prefsLoaded = true
        // The machines as last seen, at once (mergeHosts).
        try {
            var kept = JSON.parse(String(callCore("getPref", ["agent_hosts"]) || "[]"))
            if (Array.isArray(kept) && root.agentHosts.length === 0)
                root.agentHosts = kept.filter(function(h) { return h && h.name && h.address && Array.isArray(h.sessions) })
        } catch (e) {}
        try {
            var rd = JSON.parse(String(callCore("getPref", ["agent_read"]) || "{}"))
            if (rd && typeof rd === "object" && !Array.isArray(rd)) root.readTurns = rd
        } catch (e) {}
        try {
            var ap = JSON.parse(String(callCore("getPref", ["agent_autoplay"]) || "{}"))
            if (ap && typeof ap === "object" && !Array.isArray(ap)) root.autoPlay = ap
        } catch (e) {}
        var lw = parseFloat(String(callCore("getPref", ["agent_list_width"]) || ""))
        if (!isNaN(lw)) root.listWidth = lw
        var n = parseFloat(String(callCore("getPref", ["ui_nudge"]) || ""))
        if (!isNaN(n)) root.uiNudge = Math.max(-0.4, Math.min(1.0, n))
    }
    function savePref(key, value) {
        if (!haveCore) return
        callCore("setPref", [key, String(value)])
    }
    // After the first paint: a call during construction freezes the view.
    Component.onCompleted: Qt.callLater(function() { root.loadPrefs(); root.reload() })

    Rectangle { anchors.fill: parent; color: cVoid }

    Text {
        anchors.left: parent.left; anchors.right: parent.right; anchors.bottom: parent.bottom
        anchors.margins: root.sz(6)
        z: 60
        visible: text !== ""
        text: root.said !== "" ? root.said : root.problem
        color: (root.saidBad || root.said === "") ? cRust : cAsh
        elide: Text.ElideRight
        font.family: "monospace"; font.pixelSize: root.fs(10)
    }

    // ========================================================================
    // Agents (docs/agents.md): the Claude Code sessions on the owner's
    // machines, as the Android app shows them. Everything that touches the
    // network runs in shrooms_core on threads of its own; this only reads what
    // it collected, so no call here can stall the view.
    // ========================================================================

    // Always open: this module is the panel.
    readonly property bool agentsOpen: true
    property var agentHosts: []
    property var agentOpen: null          // {address, name, mesh, session}
    property var agentEventsList: []      // the agent's events, partials left out
    property var agentEarlier: []         // from the transcript, before the events
    property string agentStreaming: ""
    property int agentNext: 0
    property bool agentConnected: false
    // When the events shown are the copy kept here, for a machine that cannot
    // be reached: when they were kept (ms), 0 once they are its own.
    property real agentKept: 0
    property int agentEpoch: -1
    property string agentProblem: ""
    property bool agentCreating: false
    // Follow new messages while at the bottom; stop once scrolled up.
    property bool chatStick: true
    // Files sent to the session's machine, named in the next message.
    property var agentAttached: []
    property var agentJobsSeen: ({})
    property string agentSending: ""
    property bool agentRecording: false
    property bool agentTranscribing: false
    // Conversations on the host a new session is being made on.
    property var conversations: []
    property string conversationsProblem: ""
    // Asked and answered: an empty list now means there are none, not that
    // they are still being looked for (it said "looking…" for ever).
    property bool conversationsLoaded: false

    function unwrap(raw) {
        var r = raw
        for (var k = 0; k < 2 && typeof r === "string"; k++) {
            try { r = JSON.parse(r) } catch (e) { return null }
        }
        return r
    }

    // What Basecamp says when the core running has no such method: the core
    // runs in its own process and keeps running across a reload of this view,
    // so a view newer than its core meets this until Basecamp restarts.
    function explain(msg) {
        msg = String(msg || "")
        return msg === "Invalid response"
            ? "the shrooms core running is older than this view — quit and restart Basecamp"
            : msg
    }
    function agentCall(method, args) {
        var r = unwrap(callCore(method, args))
        if (r && r.error) {
            root.said = explain(r.detail || r.error)
            root.saidBad = true
            return null
        }
        return r
    }

    // "name|mesh|address;..." of where agents may be: this device first — an
    // agent on the machine Basecamp runs on is not a peer of it, and was
    // missed — then the peers that can be reached now.
    function agentPeers() {
        var out = []
        var ms = (root.st && root.st.meshes) ? root.st.meshes : []
        for (var m = 0; m < ms.length; m++) {
            if (ms[m] && ms[m].overlay) out.push((root.st.name || "this device") + "|" + (ms[m].label || "") + "|" + ms[m].overlay)
        }
        for (var i = 0; i < root.peers.length; i++) {
            var p = root.peers[i]
            if (p.online && p.overlay) out.push(p.name + "|" + (p.mesh || "") + "|" + p.overlay)
        }
        return out.join(";")
    }

    function refreshAgents() {
        var r = unwrap(callCore("agentsFind", [agentPeers()]))
        if (!Array.isArray(r)) return
        var hosts = [], seen = {}
        for (var i = 0; i < r.length; i++) {
            var h = r[i]
            // One machine on several meshes answers on each of its addresses;
            // it is one machine with one set of sessions.
            if (seen[h.name]) continue
            seen[h.name] = true
            hosts.push({ name: h.name, mesh: h.mesh, address: h.address,
                         sessions: (h.list && h.list.sessions) ? h.list.sessions : [] })
            // Where its subscription stands comes with the list: kept for the
            // usage link at a glance (one that misses a round keeps its last).
            var pl = h.list ? planLimits(h.name, h.list.limits) : null
            if (pl) { var lv = Object.assign({}, liveLimits); lv[h.name] = pl; root.liveLimits = lv }
        }
        var now = Date.now()
        root.nowMs = now
        root.agentHosts = mergeHosts(agentHosts, hosts, now)
        noteRead()
        // Kept for the next start, now and then rather than every round.
        if (now - lastHostsSave > 30000) {
            lastHostsSave = now
            savePref("agent_hosts", JSON.stringify(agentHosts))
        }
    }

    // The list kept between rounds of finding — the phone's HostCache: a
    // machine that misses a round stays where it is, with its sessions as last
    // seen, rather than vanishing, coming back, and moving everything under
    // the reader. Greyed once quiet for staleMs; forgotten after forgetMs.
    readonly property double staleMs: 25000
    readonly property double forgetMs: 7 * 24 * 3600 * 1000
    property double nowMs: Date.now()
    property double lastHostsSave: 0
    function mergeHosts(prev, found, now) {
        var out = [], names = {}
        for (var i = 0; i < found.length; i++) {
            var h = Object.assign({}, found[i], { lastSeen: now })
            names[h.name] = true
            out.push(h)
        }
        for (i = 0; i < (prev || []).length; i++) {
            var p = prev[i]
            if (names[p.name] || !(now - (p.lastSeen || 0) < forgetMs)) continue
            out.push(p)
        }
        out.sort(function(a, b) { return a.name < b.name ? -1 : 1 })
        return out
    }
    function hostReachable(h, now) { return (now === undefined ? nowMs : now) - (h.lastSeen || 0) < staleMs }

    // Replies not yet seen in this Basecamp, per session (the phone's Unread):
    // the session's turns — one per reply, counted by the agent — less those
    // there were when it was last open here with the window in front.
    // host/session -> turns read; kept in the core's prefs.
    property var readTurns: ({})
    function readKey(h, s) { return h.name + "/" + s }
    function unreadOf(h, sess) {
        var t = sess.turns, r = readTurns[readKey(h, sess.name)]
        if (t === undefined || t < 0 || r === undefined || t < r) return 0
        return t - r
    }
    // What is read now: a session seen for the first time (its past is not
    // news), one whose count went down (made again), and the open one while
    // the window is in front. Pure, for the test.
    function nextRead(read, hosts, open, active) {
        var out = {}, changed = false
        for (var k in read) out[k] = read[k]
        for (var i = 0; i < hosts.length; i++) {
            var ss = hosts[i].sessions || []
            for (var j = 0; j < ss.length; j++) {
                var t = ss[j].turns
                if (t === undefined || t < 0) continue
                var key = readKey(hosts[i], ss[j].name)
                var isOpen = active && open && open.address === hosts[i].address && open.session === ss[j].name
                if (out[key] === undefined || t < out[key] || (isOpen && out[key] !== t)) { out[key] = t; changed = true }
            }
        }
        return { read: out, changed: changed }
    }
    function noteRead() {
        var r = nextRead(readTurns, agentHosts, agentOpen, Qt.application.state === Qt.ApplicationActive)
        if (!r.changed) return
        root.readTurns = r.read
        savePref("agent_read", JSON.stringify(r.read))
    }

    // The open session's figures, from the last round of finding.
    readonly property var agentInfo: {
        if (!agentOpen) return null
        for (var i = 0; i < agentHosts.length; i++) {
            if (agentHosts[i].address !== agentOpen.address) continue
            var ss = agentHosts[i].sessions
            for (var j = 0; j < ss.length; j++) if (ss[j].name === agentOpen.session) return ss[j]
        }
        return null
    }

    // Sessions open at their last agentTail events; "load them" asks for all
    // (0), and a search result further back for as many as reach it.
    readonly property int agentTail: 300
    property int agentTailNow: agentTail
    function openSession(h, s, tail, fresh) {
        var t = (tail === undefined || tail === null) ? agentTail : tail
        root.agentTailNow = t
        root.searchOpen = false
        Qt.callLater(refreshOutbox)
        // Picking a session in the list leaves the "+ session" form, which
        // otherwise stayed in front of it (2026-10-04).
        root.agentCreating = false
        // The one left was read up to what it showed.
        noteRead()
        root.agentOpen = { address: h.address, name: h.name, mesh: h.mesh, session: s }
        noteRead()
        root.agentEventsList = []
        root.agentEarlier = []
        root.agentStreaming = ""
        root.agentNext = 0
        root.agentKept = 0
        root.agentEpoch = -1
        root.chatStick = true
        root.agentAttached = []
        chatModel.clear()
        // fresh: without the copy kept by the core (it is of a session since
        // made again), which a negative tail says.
        agentCall("agentWatch", [h.address, s, String(fresh && t > 0 ? -t : t)])
        root.agentEarlierAsked = false
        root.agentCaughtUp = false
        // The last session's connection says nothing about this one's.
        root.agentConnected = false
        root.agentProblem = ""
        // After the first paint: a call during construction of what it fills
        // freezes the view. What the core has — the copy kept here — shows
        // at once; the transcript is asked for later (fetchEarlier).
        Qt.callLater(pumpAgent)
    }

    // The transcript's turns from before the agent had the conversation: asked
    // of the agent once it is connected, and only when the events start at
    // the session's first, the only place they are shown. Asked first, as it
    // was, it held the pane blank for a round trip over the mesh — up to the
    // 5 s timeout, the view frozen, for a machine that is away.
    property bool agentEarlierAsked: false
    // The machine answered and everything it had was read: an empty
    // conversation now is empty, not still loading.
    property bool agentCaughtUp: false
    readonly property string chatPlaceholder: !agentOpen || chatModel.count > 0 || agentStreaming !== "" ? ""
        : agentCaughtUp ? "no messages yet"
        : agentConnected ? "loading the conversation…"
        : agentProblem !== "" ? "reaching " + agentOpen.name + "… — " + agentProblem
        : "reaching " + agentOpen.name + "…"
    function fetchEarlier() {
        if (!agentOpen || agentEarlierAsked || !agentConnected) return
        var evs = agentEventsList
        if (evs.length > 0 && evs[0].seq > 1) return
        agentEarlierAsked = true
        var open = agentOpen
        Qt.callLater(function() {
            if (root.agentOpen !== open) return
            var r = agentCall("agentGet", [open.address, "/v1/sessions/" + open.session + "/history?limit=30"])
            if (r && r.history && root.agentOpen === open) { root.agentEarlier = r.history; rebuildChat() }
        })
    }

    function pumpAgent() {
        if (!agentOpen) return
        var r = unwrap(callCore("agentEvents", [String(agentNext)]))
        if (!r || r.next === undefined) return
        root.agentConnected = !!r.connected
        root.agentProblem = r.error || ""
        root.agentKept = r.kept || 0
        // The machine's own events have replaced the kept copy: start again
        // from what the core now has.
        var replaced = r.epoch !== undefined && root.agentEpoch >= 0 && r.epoch !== root.agentEpoch
        if (r.epoch !== undefined) root.agentEpoch = r.epoch
        if (replaced) {
            root.agentEventsList = []
            root.agentStreaming = ""
            root.agentNext = r.next
            if (!r.events || r.events.length === 0) { rebuildChat(); return }
        }
        // Caught up with what the core has: the transcript, if it belongs.
        if (!r.events || r.events.length === 0) {
            if (r.connected) root.agentCaughtUp = true
            fetchEarlier()
            return
        }
        var evs = root.agentEventsList.slice()
        var streaming = root.agentStreaming
        for (var i = 0; i < r.events.length; i++) {
            var e = r.events[i]
            if (e.kind === "partial") { streaming += (e.data && e.data.text) || ""; continue }
            var t = e.data ? e.data.type : ""
            if (e.kind === "claude" && (t === "assistant" || t === "result")) streaming = ""
            evs.push(e)
        }
        // The machine's own new events (not the copy kept here): auto-play.
        if (!(r.kept > 0)) heardEvents(r.events)
        root.agentNext = r.next
        root.agentEventsList = evs
        root.agentStreaming = streaming
        rebuildChat()
        var movedTo = renamedIn(r.events, root.agentOpen.session)
        if (movedTo !== "") { Qt.callLater(renamedTo, movedTo); return }
        // While the copy is what is shown: the session's numbers are below
        // the copy's oldest — deleted and made again since it was kept, its
        // numbering restarted. Opened again without it. Against the oldest:
        // the list's last_seq trails a session that is talking, so measured
        // against the copy's newest it fired on every busy session, dropped
        // its copy and left the pane to a replay (2026-10-04).
        var li = root.agentInfo
        if (r.kept > 0 && li && li.last_seq > 0 && li.last_seq < evs[0].seq) {
            openSession(root.agentOpen, root.agentOpen.session, root.agentTailNow, true)
            return
        }
        // The core answers in pieces of about half a megabyte: keep reading
        // until caught up, without waiting for the next tick.
        if (r.more) Qt.callLater(pumpAgent)
    }

    // ========================================================================
    // Reading the model's replies aloud (the phone's Speech): a ▶ on each, and
    // per session auto-play of new ones — the model's text only, not tool
    // calls, their output or its thinking. The core speaks (agentSpeak): Piper
    // when it is set up, else spd-say.
    // ========================================================================
    // The reply being read — {key, sentences, index, paused, lang} — sentence
    // by sentence, by the view: so it can be paused, skipped and followed (the
    // bubble lights the sentence being read), whatever the engine.
    property var aloud: null
    readonly property string speakingKey: aloud ? aloud.key : ""
    property var speakQueue: []          // [{key, text}] waiting to be read
    property var autoPlay: ({})          // "address/session" -> last seq read
    function speakKey(seq) { return agentOpen ? agentOpen.address + "/" + agentOpen.session + "/" + seq : "" }

    // Markdown as it should sound: code named, links as their text, the marks
    // of emphasis, headings, lists and tables gone. Kept in step with the
    // phone's Speech.speakable.
    function speakable(md) {
        var s = String(md || "").replace(/\r/g, "")
        s = s.replace(/```([A-Za-z0-9_+-]*)[^\n]*\n[\s\S]*?(```|$)/g, function(m, lang) { return lang ? "\n(" + lang + " code)\n" : "\n(code)\n" })
        s = s.replace(/!\[([^\]]*)\]\([^)]*\)/g, "$1")
        s = s.replace(/\[([^\]]+)\]\([^)]*\)/g, "$1")
        s = s.replace(/<(https?:\/\/[^>]+)>/g, "link")
        s = s.replace(/https?:\/\/\S+/g, "link")
        s = s.replace(/`([^`]*)`/g, "$1")
        s = spokenPaths(s)
        s = s.split("\n").map(function(l) {
            l = l.replace(/^\s{0,3}#{1,6}\s+/, "").replace(/^\s*>\s?/, "").replace(/^\s*([-*+]|\d+[.)])\s+/, "")
            if (/^\s*\|?[\s:|-]+\|[\s:|-]*$/.test(l)) l = ""
            l = l.replace(/\|/g, ", ")
            if (/^\s*([-*_]\s*){3,}$/.test(l)) l = ""
            return l
        }).join("\n")
        s = s.replace(/(\*\*|\*|~~)(\S(?:.*?\S)?)\1/g, "$2")
        // Underscores only as emphasis between words: inside one — x86_64 —
        // they are the word.
        s = s.replace(/(^|[^\w])(__|_)(\S(?:.*?\S)?)\2(?![\w])/g, "$1$3")
        s = s.replace(/\n{3,}/g, "\n\n")
        return s.trim()
    }
    // Czech or English: Czech has letters English never uses.
    function isCzech(text) {
        // Letters counted as what has a case: Qt's JavaScript has no \p{L}.
        var t = String(text || ""), letters = 0
        for (var i = 0; i < t.length; i++) if (t[i].toLowerCase() !== t[i].toUpperCase()) letters++
        if (letters === 0) return false
        return (t.match(/[ěščřžýůťďňĚŠČŘŽÝŮŤĎŇ]/g) || []).length * 100 >= letters
    }
    function replyText(e) {
        if (!e || e.kind !== "claude" || !e.data || e.data.type !== "assistant") return ""
        var c = (e.data.message && e.data.message.content) || [], parts = []
        for (var i = 0; i < c.length; i++) if (c[i].type === "text" && String(c[i].text).trim() !== "") parts.push(String(c[i].text).trim())
        return parts.join("\n\n")
    }
    // Paths said as a person would: "internal/agent/session.go:654" is
    // "session.go, line 654". The phone's Speech.spokenPaths.
    function spokenPaths(text) {
        return String(text).replace(/(^|[^\w\/.-])((?:[\w.~-]+\/)*)([\w-]+(?:\.[\w-]+)*\.[A-Za-z]\w{0,9})(?::(\d+))?(?::\d+)?(?![\w\/])/g,
            function(m, pre, dirs, name, line) {
                if (line) return pre + name + ", line " + line
                if (dirs) return pre + name
                return m
            })
    }
    // Sentences, the units read and lit: cut after . ! ? where a new one
    // starts, and at line breaks. The phone's Speech.sentences.
    function sentences(text) {
        var out = [], lines = String(text).split("\n")
        for (var i = 0; i < lines.length; i++) {
            var l = lines[i].trim()
            if (l === "") continue
            // A sentence that starts a line keeps it ("\n"), for the display.
            var re = /[.!?…]+["')\]]*\s+(?=["'(\[]?[A-Z0-9ÁČĎÉĚÍŇÓŘŠŤÚŮÝŽ])/g, m, from = 0, lineStart = out.length > 0
            var add = function(x) { if (x !== "") { out.push(lineStart ? "\n" + x : x); lineStart = false } }
            while ((m = re.exec(l)) !== null) {
                add(l.substring(from, m.index + m[0].length).trim())
                from = m.index + m[0].length
            }
            if (from < l.length) add(l.substring(from).trim())
        }
        var merged = []
        for (var j = 0; j < out.length; j++) {
            var x = out[j]
            if (x.trim() === "") continue
            if (merged.length > 0 && (x.match(/[A-Za-z0-9\u00C0-\u017F]/g) || []).length <= 3) merged[merged.length - 1] += " " + x.trim()
            else merged.push(x)
        }
        return merged
    }
    function startReading(key, text) {
        var plain = speakable(text), s = sentences(plain)
        if (s.length === 0) { root.aloud = null; return }
        root.aloud = { key: key, sentences: s, index: 0, paused: false, lang: isCzech(plain) ? "cs" : "en" }
        sayCurrent()
    }
    function sayCurrent() {
        var r = aloud
        if (!r) return
        if (!agentCall("agentSpeak", ["say", String(r.sentences[r.index]).trim(), r.lang])) root.aloud = null
    }
    function setReading(changes) {
        if (!aloud) return
        var r = {}
        for (var k in aloud) r[k] = aloud[k]
        for (var c in changes) r[c] = changes[c]
        root.aloud = r
    }
    // ▶ on a reply: read it now, stopping anything else.
    function readAloud(seq, text) {
        var key = speakKey(seq)
        if (key === "") return
        root.speakQueue = []
        if (aloud) agentCall("agentSpeak", ["stop", "", ""])
        startReading(key, text)
    }
    function pauseReading() {
        if (!aloud || aloud.paused) return
        agentCall("agentSpeak", ["stop", "", ""])
        setReading({ paused: true })
    }
    // Resumes at the start of the sentence it was paused in.
    function resumeReading() {
        if (!aloud || !aloud.paused) return
        setReading({ paused: false })
        sayCurrent()
    }
    // The next sentence, or the one before (by -1); past the end, done.
    function skipReading(by) {
        if (!aloud) return
        agentCall("agentSpeak", ["stop", "", ""])
        var to = Math.max(0, aloud.index + by)
        if (to >= aloud.sentences.length) { root.aloud = null; return }
        setReading({ index: to, paused: false })
        sayCurrent()
    }
    // Scrolls to the reply being read.
    function showReading() {
        if (!aloud) return
        var seq = Number(aloud.key.substring(aloud.key.lastIndexOf("/") + 1))
        for (var i = 0; i < chatModel.count; i++) {
            if (chatModel.get(i).seq === seq) { root.chatStick = false; chatList.positionViewAtIndex(i, ListView.Beginning); return }
        }
    }
    function stopReading() {
        root.speakQueue = []
        if (aloud) agentCall("agentSpeak", ["stop", "", ""])
        root.aloud = null
    }
    // The reply as read: its sentences, the current one lit.
    function aloudHtml() {
        var r = aloud
        if (!r) return ""
        var out = []
        for (var i = 0; i < r.sentences.length; i++) {
            var raw = String(r.sentences[i]), br = i > 0 && raw.charAt(0) === "\n"
            var t = raw.trim().replace(/&/g, "&amp;").replace(/</g, "&lt;").replace(/>/g, "&gt;")
            if (br) out.push("<br>")
            if (i === r.index) out.push('<span style="background-color:#5AA9FF; color:#07090B">' + t + '</span>')
            else if (i < r.index) out.push('<span style="color:#6B7680">' + t + '</span>')
            else out.push(t)
        }
        return out.join(" ").replace(/ <br> /g, "<br>")
    }
    function autoKey(o) { return o ? o.address + "/" + o.session : "" }
    function autoPlayOn(o) { return o !== null && autoPlay[autoKey(o)] !== undefined }
    // Switched on, it reads what comes after the newest event shown now.
    function setAutoPlay(o, on) {
        if (!o) return
        var a = {}
        for (var k in autoPlay) a[k] = autoPlay[k]
        if (on) {
            var evs = agentEventsList
            a[autoKey(o)] = evs.length > 0 ? evs[evs.length - 1].seq : 0
        } else {
            delete a[autoKey(o)]
            stopReading()
        }
        root.autoPlay = a
        savePref("agent_autoplay", JSON.stringify(a))
    }
    // New events of the open session: its new replies queued, once, in order.
    function heardEvents(events) {
        var k = autoKey(agentOpen)
        if (!events || events.length === 0 || autoPlay[k] === undefined) return
        var last = autoPlay[k], q = speakQueue.slice(), newest = last
        for (var i = 0; i < events.length; i++) {
            var e = events[i]
            if (e.kind === "partial" || !(e.seq > last)) continue
            newest = Math.max(newest, e.seq)
            var t = replyText(e)
            if (t !== "") q.push({ key: k + "/" + e.seq, text: t })
        }
        if (newest === last) return
        var a = {}
        for (var kk in autoPlay) a[kk] = autoPlay[kk]
        a[k] = newest
        root.autoPlay = a
        root.speakQueue = q
        savePref("agent_autoplay", JSON.stringify(a))
    }
    // On the view's tick: notices a aloud ending, and starts the next.
    function pumpSpeech() {
        if (!aloud && speakQueue.length === 0) return
        if (aloud) {
            if (aloud.paused) return
            var st = unwrap(callCore("agentSpeak", ["state", "", ""]))
            if (st && st.speaking) return
            // That sentence is said: the next, or the reply is done.
            if (aloud.index + 1 < aloud.sentences.length) {
                setReading({ index: aloud.index + 1 })
                sayCurrent()
                return
            }
            root.aloud = null
        }
        if (speakQueue.length > 0) {
            var next = speakQueue[0]
            root.speakQueue = speakQueue.slice(1)
            startReading(next.key, next.text)
        }
    }

    function keptWhen(ms) {
        var d = new Date(ms)
        return (new Date().toDateString() === d.toDateString() ? "" : Qt.formatDate(d, "d MMM") + " ") + Qt.formatTime(d, "HH:mm")
    }
    function epoch(s) { var t = Date.parse(s || ""); return isNaN(t) ? 0 : t }
    function clock(ms) {
        if (!ms) return ""
        var d = new Date(ms), now = new Date()
        var hm = ("0" + d.getHours()).slice(-2) + ":" + ("0" + d.getMinutes()).slice(-2)
        return d.toDateString() === now.toDateString() ? hm : (d.getDate() + "." + (d.getMonth() + 1) + ". " + hm)
    }
    function summarise(input) {
        if (!input) return ""
        // AskUserQuestion: what it asks, not its JSON.
        if (Array.isArray(input.questions)) return input.questions.map(function(q) { return q.question }).join("  ·  ")
        var ks = ["command", "file_path", "pattern", "path", "url", "query", "description", "prompt"]
        for (var i = 0; i < ks.length; i++) if (input[ks[i]]) return String(input[ks[i]])
        return JSON.stringify(input).slice(0, 200)
    }
    function contextLabel(used, win) {
        if (!used || !win) return ""
        return Math.min(100, Math.floor(used * 100 / win)) + "% of " + (win >= 1000000 ? (win / 1000000) + "M" : (win / 1000) + "k")
    }
    function shortModel(m) { return String(m || "").replace(/^claude-/, "").replace("[", " ").replace("]", "").replace(/-\d{8}$/, "") }

    // The conversation as rows, with the same rules as the Android app
    // (AgentChat.kt): which prompts are open, answered, or orphaned by a
    // process that stopped; history only before the first kept event.
    function chatItems() {
        var evs = agentEventsList, out = []
        var answers = {}, lastStop = -1
        for (var i = 0; i < evs.length; i++) {
            var e = evs[i]
            if (e.kind === "answer" && e.data) answers[e.data.prompt] = (e.data.answers
                ? "answered: " + Object.keys(e.data.answers).map(function(k) { return e.data.answers[k] }).join("; ")
                : (e.data.allow ? "allowed" : "denied")) + (e.by ? " from " + e.by : "")
            if (e.kind === "stopped") lastStop = e.seq
        }
        var first = 0
        for (i = 0; i < evs.length; i++) if (epoch(evs[i].time)) { first = epoch(evs[i].time); break }
        // Only above the session's own first event (the phone's rule): above
        // a later one, the transcript's turns from after the agent took it
        // over would show again as "earlier".
        var fromStart = evs.length === 0 || evs[0].seq <= 1
        for (i = 0; fromStart && i < agentEarlier.length; i++) {
            var h = agentEarlier[i], ht = epoch(h.time)
            if (first && ht >= first) continue
            out.push({ key: "h" + i, seq: 0, kind: h.role === "user" ? "you" : "said", earlier: true, text: h.text, time: ht, by: "" })
        }
        var per = {}
        function add(e, item) {
            per[e.seq] = (per[e.seq] || 0) + 1
            item.key = "e" + e.seq + "-" + per[e.seq]
            item.seq = e.seq
            item.time = epoch(e.time)
            item.earlier = false
            out.push(item)
        }
        // A voice note shows until its turn arrives, as its latest state
        // (the phone's AgentChat).
        var sentIds = {}, lastVoice = {}
        for (i = 0; i < evs.length; i++) {
            if (evs[i].kind === "message" && evs[i].data && evs[i].data.id) sentIds[evs[i].data.id] = true
            if (evs[i].kind === "voice" && evs[i].data) lastVoice[evs[i].data.id] = evs[i].seq
        }
        for (i = 0; i < evs.length; i++) {
            e = evs[i]
            var d = e.data || {}
            // A turn the agent's harness started itself (a heartbeat, a chat
            // bridge) is labelled by where it came from, not as yours.
            if (e.kind === "message") add(e, { kind: "you", text: d.text || "", by: e.by || "", voice: !!d.voice, outside: !!d.outside })
            else if (e.kind === "voice" && !sentIds[d.id] && lastVoice[d.id] === e.seq)
                add(e, { kind: "voicenote", id: d.id, error: d.status === "failed", text: d.error || "" })
            else if (e.kind === "stopped") add(e, { kind: "note", text: "asleep; the next message wakes it" })
            else if (e.kind === "restarted") add(e, { kind: "note", text: "restarted" + (e.by ? " from " + e.by : "") })
            else if (e.kind === "renamed") add(e, { kind: "note", text: "renamed (was " + (d.from || "") + ")" + (e.by ? " from " + e.by : "") })
            else if (e.kind === "setting" && d.auto_approve !== undefined)
                add(e, { kind: "note", text: (d.auto_approve ? "auto-approve on" : "auto-approve off") + (e.by ? " from " + e.by : "") })
            else if (e.kind === "claude") {
                if (d.type === "assistant" && d.message && d.message.content) {
                    var c = d.message.content
                    for (var j = 0; j < c.length; j++) {
                        if (c[j].type === "text" && String(c[j].text).trim() !== "") add(e, { kind: "said", text: String(c[j].text).trim() })
                        else if (c[j].type === "tool_use") add(e, { kind: "tool", text: c[j].name + "  " + summarise(c[j].input) })
                    }
                } else if (d.type === "user" && d.message && Array.isArray(d.message.content)) {
                    c = d.message.content
                    for (j = 0; j < c.length; j++) {
                        if (c[j].type !== "tool_result") continue
                        var t = typeof c[j].content === "string" ? c[j].content
                              : (Array.isArray(c[j].content) ? c[j].content.map(function(x) { return x.text || "" }).join("\n") : "")
                        add(e, { kind: "output", text: t, error: !!c[j].is_error })
                    }
                } else if (d.type === "control_request" && d.request && d.request.subtype === "can_use_tool") {
                    var ans = answers[d.request_id] || (lastStop > e.seq ? "the session stopped before it was answered" : "")
                    if (d.request.tool_name === "AskUserQuestion") {
                        add(e, { kind: "question", id: d.request_id, tool: d.request.tool_name, text: summarise(d.request.input),
                                 qjson: JSON.stringify((d.request.input && d.request.input.questions) || []),
                                 open: ans === "", answer: ans })
                    } else
                    add(e, { kind: "prompt", id: d.request_id, tool: d.request.tool_name,
                             text: summarise(d.request.input), description: d.request.description || "",
                             open: ans === "", answer: ans })
                } else if (d.type === "result") {
                    var note = d.subtype === "success" ? "done" : String(d.subtype).replace(/_/g, " ")
                    if (d.total_cost_usd !== undefined) note += "  ·  $" + Number(d.total_cost_usd).toFixed(3)
                    add(e, { kind: "note", text: note })
                }
            }
        }
        return out
    }

    // Updates the model in place where it can: rows are only ever appended or
    // changed (a prompt being answered), so the view keeps its place. History
    // arriving after the events is the one case that rebuilds it.
    function rebuildChat() {
        var items = chatItems()
        var i = 0
        for (; i < chatModel.count && i < items.length; i++) {
            if (chatModel.get(i).key !== items[i].key) break
            var same = chatModel.get(i).blob === JSON.stringify(items[i])
            if (!same) chatModel.set(i, row(items[i]))
        }
        if (i < chatModel.count) {
            chatModel.clear()
            i = 0
        }
        for (; i < items.length; i++) chatModel.append(row(items[i]))
        if (root.jumpTo > 0) {
            for (i = 0; i < chatModel.count; i++) {
                if (chatModel.get(i).earlier || chatModel.get(i).seq !== root.jumpTo) continue
                // The one scroll done in code that is not following the end:
                // somebody asked for this message.
                var at = i
                root.chatStick = false
                root.jumpTo = 0
                if (root.jumpQuiet) {
                    root.jumpQuiet = false
                    Qt.callLater(function() { chatList.positionViewAtIndex(at, ListView.End) })
                    return
                }
                root.agentLit = chatModel.get(i).seq
                Qt.callLater(function() { chatList.positionViewAtIndex(at, ListView.Center) })
                litTimer.restart()
                return
            }
        }
        if (root.chatStick) Qt.callLater(function() { chatList.positionViewAtEnd() })
    }

    // Search: the whole conversation, on the agent's machine; the core does it
    // in the background and pumpSearch reads the answer.
    property bool searchOpen: false
    property bool searchBusy: false
    property var searchFound: null
    property real jumpTo: 0
    // A jump back to where the reader was, after loading more: not lit, and
    // the event at the bottom of the view with what was loaded above it.
    property bool jumpQuiet: false
    property real agentLit: 0
    property var reading: null
    function runSearch(q) {
        q = String(q || "").trim()
        if (q === "" || !agentOpen) return
        var r = agentCall("agentSearch", [agentOpen.address, agentOpen.session, q])
        if (r === null) return
        root.searchBusy = true
        root.searchFound = null
    }
    function pumpSearch() {
        if (!searchBusy) return
        var r = unwrap(callCore("agentSearched", []))
        if (!r || !r.done) return
        root.searchBusy = false
        if (r.error) { root.said = "search: " + r.error; root.saidBad = true; return }
        root.searchFound = r.found || []
    }
    // What tailReaching does on the phone (AgentChat.kt).
    // Earlier events loaded at a time, scrolled up to and asked for: the whole
    // of a long session took minutes over the mesh (the phone's AgentChat).
    readonly property int moreEvents: 150
    function moreTail(firstSeq, lastSeq) { return lastSeq - firstSeq + 1 + moreEvents }
    function moreLabel(notLoaded) {
        return "— " + notLoaded + " earlier events not loaded · " + (notLoaded <= moreEvents ? "load them" : "load " + moreEvents + " more") + " —"
    }
    function loadMore() {
        var evs = agentEventsList
        if (!agentOpen || evs.length === 0) return
        var first = evs[0].seq, last = evs[evs.length - 1].seq
        var h = { address: agentOpen.address, name: agentOpen.name, mesh: agentOpen.mesh }
        openSession(h, agentOpen.session, first - 1 <= moreEvents ? 0 : moreTail(first, last))
        root.jumpQuiet = true
        root.jumpTo = first
    }
    function tailReaching(current, lastSeq, seq) {
        return current === 0 ? 0 : Math.max(current, lastSeq - seq + 1 + 20)
    }
    function openFound(f) {
        if (!f.seq) { root.reading = f; readingDialog.open(); return }
        root.searchOpen = false
        var evs = agentEventsList
        var first = evs.length > 0 ? evs[0].seq : Infinity
        if (f.seq < first) {
            var lastSeq = Math.max(agentInfo ? (agentInfo.last_seq || 0) : 0, evs.length > 0 ? evs[evs.length - 1].seq : 0)
            var h = { address: agentOpen.address, name: agentOpen.name, mesh: agentOpen.mesh }
            openSession(h, agentOpen.session, tailReaching(agentTailNow, lastSeq, f.seq))
        }
        root.jumpTo = f.seq
        rebuildChat()
    }
    Timer { id: litTimer; interval: 4000; onTriggered: root.agentLit = 0 }
    function row(it) {
        return { key: it.key, seq: it.seq || 0, kind: it.kind, text: it.text || "", by: it.by || "", time: it.time || 0,
                 earlier: !!it.earlier, error: !!it.error, pid: it.id || "", tool: it.tool || "",
                 description: it.description || "", open: !!it.open, answer: it.answer || "", qjson: it.qjson || "",
                 voice: !!it.voice, outside: !!it.outside, blob: JSON.stringify(it) }
    }

    readonly property bool agentWorking: {
        // A kept copy says nothing about now.
        if (agentKept > 0) return false
        if (agentInfo && agentInfo.state === "working") return true
        if (agentStreaming !== "") return true
        var n = agentEventsList.length
        if (n === 0) return false
        var last = agentEventsList[n - 1]
        if (last.kind === "message") return true
        return last.kind === "claude" && last.data && last.data.type !== "result" &&
               !(last.data.type === "control_request")
    }

    // A message with the files sent alongside it named at the end, by their
    // path on the agent's machine — the same words the phone uses.
    function withAttachments(text, paths) {
        if (paths.length === 0) return text
        return (text === "" ? "" : text + "\n\n") + "Attached from Basecamp (on this machine):\n"
               + paths.map(function(p) { return "- " + p }).join("\n")
    }
    // Through the core's outbox: sent now if the machine answers, later if
    // not, so a message can be written with it unreachable (the phone's Outbox).
    function sendToAgent(text) {
        if (!agentOpen || (text.trim() === "" && agentAttached.length === 0)) return false
        var r = agentAttached.length === 0
            ? agentCall("agentQueue", [agentOpen.address, agentOpen.session, text.trim()])
            : agentCall("agentQueueFiles", [agentOpen.address, agentOpen.session, text.trim(), agentAttached.join("\n")])
        if (r !== null) { root.agentAttached = []; root.chatStick = true; refreshOutbox() }
        return r !== null
    }
    // What is written and not yet sent, for the open session.
    property var agentQueued: []
    property int outboxTick: 0
    function refreshOutbox() {
        var all = unwrap(callCore("agentOutbox", []))
        if (!Array.isArray(all) || !agentOpen) { root.agentQueued = []; return }
        root.agentQueued = all.filter(function(q) { return q.address === agentOpen.address && q.session === agentOpen.session })
    }
    function cancelQueued(id) { agentCall("agentUnqueue", [id]); refreshOutbox() }
    function queuedLabel(q) {
        var host = agentOpen ? agentOpen.name : "the machine"
        return q.error ? "QUEUED · waiting for " + host + " — " + q.error : "QUEUED · sending to " + host + "…"
    }
    function retryVoice(id) {
        if (!agentOpen) return
        agentCall("agentPost", [agentOpen.address, "/v1/sessions/" + agentOpen.session + "/voice/" + id + "/retry", ""])
    }
    function localPath(url) {
        var u = String(url)
        return u.indexOf("file://") === 0 ? decodeURIComponent(u.slice(7)) : u
    }
    // Kept by the core at once, and sent with the message through the
    // outbox: a machine that is not there holds up nothing, and loses nothing.
    function attachFile(url) {
        if (!agentOpen) return
        var r = agentCall("agentKeep", [localPath(url)])
        if (r && r.file) root.agentAttached = agentAttached.concat([r.file])
    }
    // A kept file's name as it was: the core keeps it as b-<id>-<name>.
    function attachedName(path) {
        return String(path).split("/").pop().replace(/^b-[0-9a-f]+-[0-9a-f]+-[0-9a-f]+-/, "").replace(/^\d{8}-\d{6}-(\d+-)?/, "")
    }
    function toggleRecording() {
        if (!agentOpen || agentTranscribing) return
        if (!agentRecording) {
            if (agentCall("agentRecord", ["start", "", "", ""]) !== null) root.agentRecording = true
        } else {
            root.agentRecording = false
            // A voice note goes as a voice note: queued, transcribed on the
            // agent's machine and sent as the turn — nothing to wait for here.
            if (agentCall("agentRecord", ["send", agentOpen.address, agentOpen.session, ""]) !== null) {
                root.chatStick = true
                refreshOutbox()
            }
        }
    }
    function shortDir(d) { return String(d || "").replace(/^\/home\/[^\/]+/, "~") }
    // The coding agents a machine runs (GET /v1/harnesses), Claude Code first;
    // an agent too old to list them runs Claude Code alone.
    readonly property var claudeOnly: [ { name: "claude", title: "Claude Code", caps: { approve: true } } ]
    property var harnesses: claudeOnly
    property string nsHarness: "claude"
    function loadHarnesses(h) {
        root.harnesses = claudeOnly
        root.nsHarness = "claude"
        if (!h) return
        var r = unwrap(callCore("agentGet", [h.address, "/v1/harnesses"]))
        if (r && r.harnesses && r.harnesses.length > 0) root.harnesses = r.harnesses
    }
    function harnessApproves(name) {
        for (var i = 0; i < harnesses.length; i++) if (harnesses[i].name === name) return !!(harnesses[i].caps && harnesses[i].caps.approve)
        return false
    }
    function harnessLabel(h) { return (!h || h === "claude") ? "" : h }
    function createSession(host, name, dir, auto) {
        var r = agentCall("agentPost", [host.address, "/v1/sessions",
            JSON.stringify({ name: name, dir: dir, harness: nsHarness, auto_approve: auto && harnessApproves(nsHarness) })])
        return r !== null
    }
    function loadConversations(h) {
        root.conversations = []
        root.conversationsProblem = ""
        root.conversationsLoaded = false
        if (!h) return
        var r = unwrap(callCore("agentGet", [h.address, "/v1/conversations?limit=15"]))
        if (r && r.conversations) { root.conversations = r.conversations; root.conversationsLoaded = true }
        else root.conversationsProblem = (r && (r.detail || r.error)) || "the agent does not list conversations — update shrooms-agent"
    }
    // A session name from the directory the conversation ran in, unique on
    // that machine.
    function nameFor(h, conv) {
        var base = String(conv.dir || "conversation").split("/").pop().replace(/[^a-zA-Z0-9._-]/g, "-").replace(/^[^a-zA-Z0-9]+/, "") || "conversation"
        base = base.slice(0, 40)
        var taken = {}
        for (var i = 0; i < h.sessions.length; i++) taken[h.sessions[i].name] = true
        var name = base, n = 2
        while (taken[name]) name = base + "-" + (n++)
        return name
    }
    function takeOver(h, conv) {
        var name = nameFor(h, conv)
        var r = agentCall("agentPost", [h.address, "/v1/sessions",
                          JSON.stringify({ name: name, resume: conv.id, auto_approve: nsAuto.checked })])
        if (r === null) return
        root.agentCreating = false
        refreshAgents()
        openSession(h, name)
    }
    function stopTerminal(h, pid) {
        if (agentCall("agentPost", [h.address, "/v1/terminals/" + pid + "/stop", ""]) !== null) {
            root.said = "stopped the terminal's claude"
            root.saidBad = false
            Qt.callLater(function() { root.loadConversations(h) })
        }
    }
    // Uploads and voice notes finish in the core's own time: picked up here.
    function pumpJobs() {
        // The outbox about once a second: it changes as the core sends.
        root.outboxTick = (outboxTick + 1) % 3
        if (outboxTick === 0) refreshOutbox()
        var r = unwrap(callCore("agentJobs", []))
        if (!r || !r.jobs) return
        root.agentRecording = !!r.recording
        var sending = [], transcribing = false
        for (var i = 0; i < r.jobs.length; i++) {
            var j = r.jobs[i]
            if (j.state === "pending") {
                if (j.kind === "upload") sending.push(j.name)
                else transcribing = true
                continue
            }
            if (agentJobsSeen[j.id]) continue
            agentJobsSeen[j.id] = true
            if (j.state === "failed") { root.said = j.name + ": " + j.error; root.saidBad = true; continue }
            if (j.kind === "upload" && j.path) root.agentAttached = agentAttached.concat([j.path])
            if (j.kind === "voice" && j.text) composer.text = composer.text.trim() === "" ? j.text : composer.text.trim() + " " + j.text
        }
        root.agentSending = sending.join(", ")
        root.agentTranscribing = transcribing
    }
    function answerPrompt(id, allow) {
        if (!agentOpen) return
        agentCall("agentPost", [agentOpen.address, "/v1/sessions/" + agentOpen.session + "/prompts/" + id,
                                JSON.stringify({ allow: allow })])
    }
    // A question from the model (AskUserQuestion): what has been picked or
    // typed so far, per prompt and question, kept here so a row rebuilt by
    // arriving events does not lose it. The rules are the phone's
    // (AgentChat.answersFor): picks in the order offered, joined by ", ";
    // typed words in their place; nothing sent until every question has one.
    property var qPicked: ({})
    property var qTyped: ({})
    function pickOption(pid, question, label, multi) {
        var all = Object.assign({}, qPicked)
        var mine = Object.assign({}, all[pid] || {})
        var cur = (mine[question] || []).slice()
        var at = cur.indexOf(label)
        if (multi) { if (at >= 0) cur.splice(at, 1); else cur.push(label) }
        else cur = [label]
        mine[question] = cur
        all[pid] = mine
        root.qPicked = all
        typeAnswer(pid, question, "")
    }
    function typeAnswer(pid, question, text) {
        var all = Object.assign({}, qTyped)
        var mine = Object.assign({}, all[pid] || {})
        mine[question] = text
        all[pid] = mine
        root.qTyped = all
    }
    function isPicked(pid, question, label) {
        var p = (qPicked[pid] || {})[question] || []
        return p.indexOf(label) >= 0
    }
    function questionAnswers(pid, questions) {
        var out = {}
        for (var i = 0; i < questions.length; i++) {
            var q = questions[i]
            var t = String((qTyped[pid] || {})[q.question] || "").trim()
            var picked = (qPicked[pid] || {})[q.question] || []
            var p = (q.options || []).map(function(o) { return o.label }).filter(function(l) { return picked.indexOf(l) >= 0 })
            if (t !== "") out[q.question] = t
            else if (p.length > 0) out[q.question] = p.join(", ")
            else return null
        }
        return out
    }
    function answerQuestion(pid, questions) {
        var a = questionAnswers(pid, questions)
        if (!a || !agentOpen) return false
        return agentCall("agentPost", [agentOpen.address, "/v1/sessions/" + agentOpen.session + "/prompts/" + pid,
                                       JSON.stringify({ allow: true, answers: a })]) !== null
    }

    // Starred sessions: first, from every machine, by name (the phone's
    // starredFirst). The star is kept on the agent, so the phone shows it too.
    readonly property var starredSessions: {
        var out = []
        for (var i = 0; i < agentHosts.length; i++) {
            var ss = agentHosts[i].sessions || []
            for (var j = 0; j < ss.length; j++) if (ss[j].starred) out.push({ host: agentHosts[i], sess: ss[j] })
        }
        out.sort(function(a, b) { return a.sess.name === b.sess.name ? (a.host.name < b.host.name ? -1 : 1) : (a.sess.name < b.sess.name ? -1 : 1) })
        return out
    }
    function unstarred(h) { return (h.sessions || []).filter(function(x) { return !x.starred }) }
    function setStarred(h, name, on) {
        // Shown at once; the agent's answer is the next refresh's.
        var hs = agentHosts.map(function(x) {
            if (x.address !== h.address) return x
            var c = Object.assign({}, x)
            c.sessions = (x.sessions || []).map(function(y) { return y.name === name ? Object.assign({}, y, { starred: on }) : y })
            return c
        })
        root.agentHosts = hs
        agentCall("agentPost", [h.address, "/v1/sessions/" + name + "/settings", JSON.stringify({ starred: on })])
    }

    function setAutoApprove(on) {
        if (!agentOpen) return
        if (agentCall("agentPost", [agentOpen.address, "/v1/sessions/" + agentOpen.session + "/settings",
                                    JSON.stringify({ auto_approve: on })]) !== null) refreshAgents()
    }

    ListModel { id: chatModel }

    // What deleting a session in this state does, in words: the same as the
    // phone's (Agents.kt, deleteSessionText).
    function deleteSessionText(state) {
        var t = "This stops the session and removes it from the list. The Claude Code conversation itself is kept on that machine, and can be continued again from \"+ session\"."
        if (state === "working") t += "\n\nIt is working right now: that turn will be cut off."
        if (state === "waiting") t += "\n\nIt is waiting for an answer to a permission prompt, which will be dropped."
        return t
    }
    // Ends the turn running now, as Esc does in Claude Code's terminal; the
    // session stays and takes the next message.
    function stopTurn() {
        if (!agentOpen) return
        agentCall("agentPost", [agentOpen.address, "/v1/sessions/" + agentOpen.session + "/interrupt", ""])
    }
    // For a process that stop does not reach — a request hanging on a dropped
    // connection. Only this session's; the others on the machine go on.
    function askRestart() { restartDialog.open() }
    function restartOpenSession() {
        if (!agentOpen) return false
        if (agentCall("agentPost", [agentOpen.address, "/v1/sessions/" + agentOpen.session + "/restart", ""]) === null) return false
        root.said = "restarted session " + agentOpen.session
        root.saidBad = false
        return true
    }
    // Renamed — here, from the phone, or from another Basecamp: the copy kept
    // here, the queued messages, what was read and auto-play move with it,
    // and the session opens again under its new name.
    function askRename() { renameField.text = agentOpen ? agentOpen.session : ""; renameDialog.open() }
    function renameOpenSession(to) {
        to = String(to || "").trim()
        if (!agentOpen || to === "" || to === agentOpen.session) return false
        var path = "/v1/sessions/" + agentOpen.session + "/rename"
        if (agentCall("agentPost", [agentOpen.address, path, JSON.stringify({ name: to })]) === null) return false
        root.said = "renamed session " + agentOpen.session + " to " + to
        root.saidBad = false
        renamedTo(to)
        return true
    }
    function renamedTo(to) {
        if (!agentOpen || !to || to === agentOpen.session) return
        var o = agentOpen, from = o.session
        agentCall("agentMoveKept", [o.address, from, to])
        var rd = {}
        for (var k in readTurns) rd[k === readKey(o, from) ? readKey(o, to) : k] = readTurns[k]
        root.readTurns = rd
        savePref("agent_read", JSON.stringify(rd))
        var ap = {}
        for (var a in autoPlay) ap[a === o.address + "/" + from ? o.address + "/" + to : a] = autoPlay[a]
        root.autoPlay = ap
        savePref("agent_autoplay", JSON.stringify(ap))
        openSession(o, to, agentTailNow)
    }
    // The name a session open as session was given, if events say it was
    // renamed; "" if not. An older rename, replayed, names an older name.
    function renamedIn(events, session) {
        var to = ""
        for (var i = 0; i < events.length; i++)
            if (events[i].kind === "renamed" && events[i].data && events[i].data.from === session) to = events[i].data.to || ""
        return to
    }
    // Where the session list is scrolled to; given y, scrolls it there.
    function hostScroll(y) {
        if (y !== undefined) hostList.contentItem.contentY = y
        return hostList.contentItem.contentY
    }
    function askDelete() { deleteDialog.open() }
    function deleteDialogOpen() { return deleteDialog.visible }
    function deleteOpenSession() {
        if (!agentOpen) return false
        var r = agentCall("agentDelete", [agentOpen.address, "/v1/sessions/" + agentOpen.session])
        if (r === null) return false
        root.said = "deleted session " + agentOpen.session
        root.saidBad = false
        root.agentOpen = null
        chatModel.clear()
        refreshAgents()
        return true
    }

    // Usage (the agents' GET /v1/usage, the phone's UsageView): per turn, the
    // device that asked — its mesh address, which WireGuard makes unforgeable —
    // the machine whose model answered, the model, tokens, cost and time; every
    // machine asked, summed by who, where and which model.
    property var usageRows: null         // null while asking
    property var usageMissing: []
    property int usageDays: 7
    property string usageMeasure: "output"   // output, turns, cost, busy
    function usageSince(days, today) {
        if (days <= 0) return ""
        var d = today ? new Date(today) : new Date()
        d.setDate(d.getDate() - (days - 1))
        return Qt.formatDate(d, "yyyy-MM-dd")
    }
    function openUsage() {
        usageDialog.open()
        loadUsage()
    }
    // Every machine asked at once, in the background (agentGather), and the
    // answers shown as they come: asked one after another from here, each
    // call held the window, and a machine away held it the longest.
    property var usageHosts: []
    // Where each Claude subscription stands (the agent's "limits", Claude
    // Code's own reports): the phone's UsageView.parseLimits and accounts.
    property var usagePlans: []
    // Each machine's newest reading, from its session list (refreshAgents).
    property var liveLimits: ({})
    // The session quota at a glance — the phone's UsageView.glance: the
    // 5-hour window's share on the busiest account, and a level: 0 fine, 1
    // from half of it (slow down), 2 from 80% or once a request was refused.
    function planGlance(plans) {
        var top = null
        for (var i = 0; i < plans.length; i++) {
            var p = plans[i], w = p.windows.filter(function(x) { return x.name === "five_hour" })[0]
            if (!w) w = p.windows.slice().sort(function(a, b) { return b.utilization - a.utilization })[0]
            var share = w ? w.utilization : 0, refused = p.status === "rejected"
            var rank = refused ? 2 : share
            if (!top || rank > top.rank) top = { rank: rank, share: share, refused: refused }
        }
        if (!top) return null
        return { percent: Math.floor(top.share * 100), level: top.refused || top.share >= 0.8 ? 2 : top.share >= 0.5 ? 1 : 0 }
    }
    function planLevel(u) { return u >= 0.8 ? 2 : u >= 0.5 ? 1 : 0 }
    readonly property var usageGlance: planGlance(planAccounts(Object.keys(liveLimits).map(function(k) { return liveLimits[k] })))
    function planLimits(machine, l) {
        if (!l || typeof l !== "object") return null
        var ws = [], order = { five_hour: 0, seven_day: 1 }
        for (var k in (l.windows || {})) ws.push({ name: k, utilization: Number(l.windows[k].utilization) || 0, resetsAt: Date.parse(l.windows[k].resets_at) || 0 })
        ws.sort(function(a, b) { return (a.name in order ? order[a.name] : 2) - (b.name in order ? order[b.name] : 2) })
        if (ws.length === 0 && !l.status) return null
        return { machines: [machine], at: Date.parse(l.at) || 0, status: l.status || "", window: l.window || "", overage: !!l.overage, windows: ws }
    }
    // Once per account: machines whose windows reset at the same moments share
    // a subscription; the newest reading among them is shown.
    function planAccounts(all) {
        var groups = {}, keys = []
        for (var i = 0; i < all.length; i++) {
            var l = all[i]
            var key = l.windows.map(function(w) { return w.name + "@" + Math.floor(w.resetsAt / 60000) }).sort().join(",")
            if (!(key in groups)) { groups[key] = []; keys.push(key) }
            groups[key].push(l)
        }
        var out = keys.map(function(k) {
            var ls = groups[k], newest = ls[0], machines = []
            for (var j = 0; j < ls.length; j++) {
                if (ls[j].at > newest.at) newest = ls[j]
                for (var m = 0; m < ls[j].machines.length; m++) if (machines.indexOf(ls[j].machines[m]) < 0) machines.push(ls[j].machines[m])
            }
            return Object.assign({}, newest, { machines: machines.sort() })
        })
        return out.sort(function(a, b) { return b.at - a.at })
    }
    function planWindowLabel(n) {
        return n === "five_hour" ? "5 hours" : n === "seven_day" ? "7 days" : n === "seven_day_opus" ? "7 days, Opus"
             : n === "seven_day_sonnet" ? "7 days, Sonnet" : String(n).replace(/_/g, " ")
    }
    function planResets(ms, now) {
        if (!ms) return "?"
        now = now || Date.now()
        var d = new Date(ms)
        var hm = ("0" + d.getHours()).slice(-2) + ":" + ("0" + d.getMinutes()).slice(-2)
        return ms - now < 20 * 3600000 ? hm : ["Sun", "Mon", "Tue", "Wed", "Thu", "Fri", "Sat"][d.getDay()] + " " + hm
    }
    function planStatus(l, now) {
        var w = l.windows.filter(function(x) { return x.name === l.window })[0]
        var which = l.window ? " (" + planWindowLabel(l.window) + ")" : ""
        if (l.status === "rejected") return "limit reached" + which + (w ? " — back at " + planResets(w.resetsAt, now) : "")
        // Claude Code warns at thresholds of its own (50% of the 7-day window,
        // 90% of the 5-hour one): the share, not "close".
        if (l.status === "allowed_warning") return w ? "past " + Math.floor(w.utilization * 100) + "% of " + planWindowLabel(w.name) : "warned" + which
        return ""
    }
    function planAge(at, now) {
        if (!at) return "?"
        var m = Math.floor(((now || Date.now()) - at) / 60000)
        return m < 1 ? "just now" : m < 60 ? m + " min ago" : m < 48 * 60 ? Math.floor(m / 60) + " h ago" : Math.floor(m / 1440) + " d ago"
    }
    property var usageWaiting: []
    function loadUsage() {
        root.usageRows = null
        root.usageMissing = []
        root.usagePlans = []
        var since = usageSince(usageDays)
        root.usageHosts = agentHosts.map(function(h) { return { name: h.name, address: h.address } })
        root.usageWaiting = usageHosts.map(function(h) { return h.name })
        if (usageHosts.length === 0) { root.usageRows = []; return }
        agentCall("agentGather", [usageHosts.map(function(h) { return h.address }).join("\n"),
                                  "/v1/usage" + (since ? "?since=" + since : "")])
    }
    function pumpUsage() {
        if (usageWaiting.length === 0) return
        var g = unwrap(callCore("agentGathered", []))
        if (!g || !Array.isArray(g.results)) return
        var rows = [], missing = [], waiting = [], limits = []
        for (var i = 0; i < usageHosts.length; i++) {
            var h = usageHosts[i]
            var r = g.results.filter(function(x) { return x.address === h.address })[0]
            if (!r || !r.done) { waiting.push(h.name); continue }
            if (!r.body || !Array.isArray(r.body.rows)) { missing.push(h.name); continue }
            for (var j = 0; j < r.body.rows.length; j++) { var row = r.body.rows[j]; row.machine = h.name; rows.push(row) }
            var pl = planLimits(h.name, r.body.limits)
            if (pl) limits.push(pl)
        }
        root.usageWaiting = waiting
        root.usageMissing = missing
        root.usagePlans = planAccounts(limits)
        // Shown once something came, or everything did.
        if (rows.length > 0 || waiting.length === 0) root.usageRows = rows
    }
    Timer { interval: 300; repeat: true; running: usageDialog.visible && root.usageWaiting.length > 0; onTriggered: root.pumpUsage() }
    // Who asked, as a person reads it: "nothing.office" is "nothing"; "" is
    // the agent's own machine, counted under its name — the same device as
    // when it asks another machine.
    function usageDevice(r) { return r.by ? String(r.by).split(".")[0] : r.machine }
    function usageValue(l, m) { return m === "turns" ? l.turns : m === "cost" ? l.cost : m === "busy" ? l.busy : l.output }
    function usageCount(n) { return n >= 1e6 ? (n / 1e6).toFixed(1) + "M" : n >= 1e3 ? (n / 1e3).toFixed(1) + "k" : String(n) }
    function usageHours(ms) { var m = Math.floor(ms / 60000); return m >= 60 ? Math.floor(m / 60) + "h " + (m % 60) + "m" : m + "m" }
    function usageFormat(l, m) {
        return m === "turns" ? String(l.turns) : m === "cost" ? "$" + l.cost.toFixed(2) : m === "busy" ? usageHours(l.busy) : usageCount(l.output)
    }
    // Rows summed by key, largest first by the measure.
    function usageGroup(rows, key, m) {
        var by = {}, out = []
        for (var i = 0; i < rows.length; i++) {
            var r = rows[i], k = key(r), l = by[k]
            if (!l) { l = by[k] = { name: k, turns: 0, output: 0, input: 0, cost: 0, busy: 0 }; out.push(l) }
            l.turns += r.turns || 0
            l.output += r.output || 0
            l.input += (r.input || 0) + (r.cache_read || 0) + (r.cache_write || 0)
            l.cost += r.cost_usd || 0
            l.busy += r.busy_ms || 0
        }
        out.sort(function(a, b) { return usageValue(b, m) - usageValue(a, m) })
        return out
    }
    function usageSections() {
        var r = usageRows || [], m = usageMeasure
        return [
            { title: "WHO ASKED", tint: cPhosphor, lines: usageGroup(r, usageDevice, m) },
            { title: "WHERE IT RAN", tint: cSky, lines: usageGroup(r, function(x) { return x.machine }, m) },
            { title: "MODEL", tint: cViolet, lines: usageGroup(r, function(x) { return x.model || "?" }, m) }
        ]
    }
    Dialog {
        id: usageDialog
        modal: true
        anchors.centerIn: parent
        width: Math.min(root.sz(620), root.width - root.sz(40))
        height: Math.min(root.sz(640), root.height - root.sz(40))
        padding: root.sz(20)
        closePolicy: Popup.CloseOnEscape | Popup.CloseOnPressOutside
        Overlay.modal: Rectangle { color: Qt.rgba(0, 0, 0, 0.6) }
        background: Rectangle { color: cPanel; radius: root.sz(12); border.color: cPhosphor }
        header: Item {}
        footer: Item {}
        contentItem: ColumnLayout {
            spacing: root.sz(10)
            RowLayout {
                Text { text: "USAGE"; color: cPhosphor; font.family: "monospace"; font.pixelSize: root.fs(12); font.letterSpacing: 1.5; Layout.fillWidth: true }
                Lnk { text: "CLOSE"; base: cBone; font.pixelSize: root.fs(11); onClicked: usageDialog.close() }
            }
            RowLayout {
                spacing: root.sz(14)
                Repeater {
                    model: [[1, "today"], [7, "7 days"], [30, "30 days"], [0, "all"]]
                    Lnk { required property var modelData
                          text: modelData[1]; base: root.usageDays === modelData[0] ? cPhosphor : cAsh; font.pixelSize: root.fs(10)
                          onClicked: { root.usageDays = modelData[0]; Qt.callLater(root.loadUsage) } }
                }
            }
            RowLayout {
                spacing: root.sz(14)
                Repeater {
                    model: [["output", "tokens out"], ["turns", "turns"], ["cost", "cost"], ["busy", "busy"]]
                    Lnk { required property var modelData
                          text: modelData[1]; base: root.usageMeasure === modelData[0] ? cPhosphor : cAsh; font.pixelSize: root.fs(10)
                          onClicked: root.usageMeasure = modelData[0] }
                }
            }
            // Where the subscription stands, whatever the period.
            Column {
                Layout.fillWidth: true
                visible: root.usagePlans.length > 0
                spacing: root.sz(4)
                Text { text: "PLAN LIMITS"; color: cAmber; font.family: "monospace"; font.pixelSize: root.fs(10); font.letterSpacing: 1; topPadding: root.sz(6) }
                Repeater {
                    model: root.usagePlans
                    Column {
                        id: plan
                        required property var modelData
                        width: parent.width
                        spacing: 2
                        Text { text: plan.modelData.machines.join(", ") + " · as of " + root.planAge(plan.modelData.at); color: cAsh; font.family: "monospace"; font.pixelSize: root.fs(9) }
                        Text { visible: text !== ""; text: root.planStatus(plan.modelData)
                               color: plan.modelData.status === "rejected" ? cRust : cAmber; font.family: "monospace"; font.pixelSize: root.fs(10) }
                        Repeater {
                            model: plan.modelData.windows
                            Column {
                                id: pw
                                required property var modelData
                                width: plan.width
                                spacing: 2
                                RowLayout {
                                    width: parent.width
                                    Text { text: root.planWindowLabel(pw.modelData.name); color: cBone; font.family: "monospace"; font.pixelSize: root.fs(11); Layout.fillWidth: true }
                                    Text { text: Math.floor(pw.modelData.utilization * 100) + "% · resets " + root.planResets(pw.modelData.resetsAt); color: cBone; font.family: "monospace"; font.pixelSize: root.fs(11) }
                                }
                                Rectangle {
                                    width: parent.width; height: root.sz(5); radius: height / 2; color: cLine
                                    Rectangle { height: parent.height; radius: parent.radius
                                                color: root.planLevel(pw.modelData.utilization) === 2 ? cRust : root.planLevel(pw.modelData.utilization) === 1 ? cAmber : cPhosphor
                                                width: parent.width * Math.max(0, Math.min(1, pw.modelData.utilization)) }
                                }
                            }
                        }
                    }
                }
            }
            Text { visible: root.usageRows === null; text: "asking the machines…"; color: cAsh; font.family: "monospace"; font.pixelSize: root.fs(11) }
            Text { visible: root.usageRows !== null && root.usageWaiting.length > 0; text: "still asking: " + root.usageWaiting.join(", ");
                   color: cAsh; font.family: "monospace"; font.pixelSize: root.fs(10) }
            Text { visible: root.usageRows !== null && root.usageRows.length === 0 && root.usageWaiting.length === 0; text: "nothing used in this period"; color: cAsh; font.family: "monospace"; font.pixelSize: root.fs(11) }
            ScrollView {
                Layout.fillWidth: true; Layout.fillHeight: true
                visible: root.usageRows !== null && root.usageRows.length > 0
                clip: true
                Column {
                    width: usageDialog.availableWidth - root.sz(12)
                    spacing: root.sz(6)
                    Repeater {
                        model: root.usageRows ? root.usageSections() : []
                        Column {
                            id: usec
                            required property var modelData
                            width: parent.width
                            spacing: root.sz(6)
                            Text { text: modelData.title; color: modelData.tint; font.family: "monospace"; font.pixelSize: root.fs(10); font.letterSpacing: 1; topPadding: root.sz(8) }
                            Repeater {
                                model: modelData.lines
                                Column {
                                    id: uline
                                    required property var modelData
                                    property real peak: {
                                        var ls = usec.modelData.lines, t = 0
                                        for (var i = 0; i < ls.length; i++) t = Math.max(t, root.usageValue(ls[i], root.usageMeasure))
                                        return t > 0 ? t : 1
                                    }
                                    width: parent.width
                                    spacing: 2
                                    RowLayout {
                                        width: parent.width
                                        Text { text: uline.modelData.name; color: cBone; font.family: "monospace"; font.pixelSize: root.fs(11); elide: Text.ElideRight; Layout.fillWidth: true }
                                        Text { text: root.usageFormat(uline.modelData, root.usageMeasure); color: cBone; font.family: "monospace"; font.pixelSize: root.fs(11) }
                                    }
                                    Rectangle {
                                        width: parent.width; height: root.sz(5); radius: height / 2; color: cLine
                                        Rectangle { height: parent.height; radius: parent.radius
                                                    color: usec.modelData.tint
                                                    width: parent.width * Math.max(0, Math.min(1, root.usageValue(uline.modelData, root.usageMeasure) / uline.peak)) }
                                    }
                                    Text {
                                        text: uline.modelData.turns + " turns · " + root.usageCount(uline.modelData.output) + " out · " + root.usageCount(uline.modelData.input) + " in"
                                              + (uline.modelData.cost > 0 ? " · $" + uline.modelData.cost.toFixed(2) : "") + " · " + root.usageHours(uline.modelData.busy)
                                        color: cAsh; font.family: "monospace"; font.pixelSize: root.fs(9)
                                    }
                                }
                            }
                        }
                    }
                }
            }
            Text { visible: root.usageMissing.length > 0; text: "not reached: " + root.usageMissing.join(", "); color: cAmber; font.family: "monospace"; font.pixelSize: root.fs(10) }
        }
    }

    // The read-aloud voice: what reads now, and the natural one set up in one
    // click (agentVoice) — Piper and an English voice, downloaded by the core.
    property var voice: null             // agentVoice's state
    function openVoice() {
        root.voice = unwrap(callCore("agentVoice", ["state"]))
        voiceDialog.open()
    }
    function voiceAction(action) {
        var r = agentCall("agentVoice", [action])
        if (r) root.voice = r
    }
    function voiceText(v) {
        if (!v) return ""
        if (v.busy) return (v.step || "working") + "…"
        if (v.error) return "Could not set it up: " + v.error
        if (v.installed) return "Natural voice: Piper, " + v.voice + ". Replies are read with it."
        return "Replies are read with " + (v.engine === "spd-say" ? "speech-dispatcher (espeak), which sounds robotic"
                                                              : "nothing: no speech engine was found") + "."
    }
    Timer {
        interval: 700; repeat: true
        running: voiceDialog.visible && root.voice !== null && root.voice.busy
        onTriggered: root.voice = root.unwrap(root.callCore("agentVoice", ["state"]))
    }
    Dialog {
        id: voiceDialog
        modal: true
        anchors.centerIn: parent
        width: Math.min(root.sz(520), root.width - root.sz(40))
        padding: root.sz(20)
        closePolicy: Popup.CloseOnEscape | Popup.CloseOnPressOutside
        Overlay.modal: Rectangle { color: Qt.rgba(0, 0, 0, 0.6) }
        background: Rectangle { color: cPanel; radius: root.sz(12); border.color: cSky }
        header: Item {}
        footer: Item {}
        contentItem: ColumnLayout {
            spacing: root.sz(14)
            Text { text: "READ-ALOUD VOICE"; color: cSky; font.family: "monospace"; font.pixelSize: root.fs(12); font.letterSpacing: 1.5 }
            Text {
                Layout.fillWidth: true; wrapMode: Text.Wrap
                text: root.voiceText(root.voice)
                color: root.voice && root.voice.error ? cRust : cBone; font.family: "monospace"; font.pixelSize: root.fs(12)
            }
            Text {
                Layout.fillWidth: true; wrapMode: Text.Wrap
                visible: root.voice !== null && !root.voice.installed && !root.voice.busy
                text: "Set up a natural voice: Piper, an offline speech engine, with an English voice — about 90 MB, downloaded once into ~/.local/share/shrooms/piper. Nothing is installed system-wide."
                color: cAsh; font.family: "monospace"; font.pixelSize: root.fs(11)
            }
            RowLayout {
                Layout.alignment: Qt.AlignRight
                spacing: root.sz(20)
                Lnk { visible: root.voice !== null && root.voice.installed && !root.voice.busy; text: "remove"; base: cAsh; font.pixelSize: root.fs(12)
                      onClicked: root.voiceAction("remove") }
                Lnk { visible: root.voice !== null && root.voice.installed && !root.voice.busy; text: "▶ try"; base: cSky; font.pixelSize: root.fs(12)
                      onClicked: root.agentCall("agentSpeak", ["say", "This is how replies will sound.", "en"]) }
                Lnk { visible: root.voice !== null && !root.voice.installed && !root.voice.busy; text: "SET UP"; base: cPhosphor; font.pixelSize: root.fs(12)
                      onClicked: root.voiceAction("setup") }
                Lnk { text: "CLOSE"; base: cBone; font.pixelSize: root.fs(12); onClicked: voiceDialog.close() }
            }
        }
    }

    // Asks before deleting, and says what is lost and what is not.
    Dialog {
        id: deleteDialog
        modal: true
        anchors.centerIn: parent
        width: Math.min(root.sz(460), root.width - root.sz(40))
        padding: root.sz(20)
        closePolicy: Popup.CloseOnEscape | Popup.CloseOnPressOutside
        Overlay.modal: Rectangle { color: Qt.rgba(0, 0, 0, 0.6) }
        background: Rectangle { color: cPanel; radius: root.sz(12); border.color: cRust }
        header: Item {}
        footer: Item {}
        contentItem: ColumnLayout {
            spacing: root.sz(14)
            Text {
                Layout.fillWidth: true; wrapMode: Text.Wrap
                text: root.agentOpen ? "Delete session \"" + root.agentOpen.session + "\" on " + root.agentOpen.name + "?" : ""
                color: cBone; font.family: "monospace"; font.pixelSize: root.fs(14)
            }
            Text {
                Layout.fillWidth: true; wrapMode: Text.Wrap
                text: root.deleteSessionText(root.agentInfo ? root.agentInfo.state : "idle")
                color: cAsh; font.family: "monospace"; font.pixelSize: root.fs(11)
            }
            RowLayout {
                Layout.alignment: Qt.AlignRight
                spacing: root.sz(20)
                Lnk { text: "CANCEL"; base: cBone; font.pixelSize: root.fs(12); onClicked: deleteDialog.close() }
                Lnk { text: "DELETE"; base: cRust; font.pixelSize: root.fs(12)
                      onClicked: if (root.deleteOpenSession()) deleteDialog.close() }
            }
        }
    }
    Dialog {
        id: renameDialog
        modal: true
        anchors.centerIn: parent
        width: Math.min(root.sz(460), root.width - root.sz(40))
        padding: root.sz(20)
        closePolicy: Popup.CloseOnEscape | Popup.CloseOnPressOutside
        Overlay.modal: Rectangle { color: Qt.rgba(0, 0, 0, 0.6) }
        background: Rectangle { color: cPanel; radius: root.sz(12); border.color: cPhosphor }
        header: Item {}
        footer: Item {}
        onOpened: renameField.forceActiveFocus()
        contentItem: ColumnLayout {
            spacing: root.sz(14)
            Text {
                Layout.fillWidth: true; wrapMode: Text.Wrap
                text: root.agentOpen ? "Rename session \"" + root.agentOpen.session + "\" on " + root.agentOpen.name : ""
                color: cBone; font.family: "monospace"; font.pixelSize: root.fs(14)
            }
            TextField {
                id: renameField; Layout.fillWidth: true; color: cBone; font.family: "monospace"
                background: Rectangle { color: cPanel; border.color: renameField.activeFocus ? cPhosphor : cLine; radius: 6 }
                onAccepted: if (root.renameOpenSession(text)) renameDialog.close()
            }
            Text {
                Layout.fillWidth: true; wrapMode: Text.Wrap
                text: "Letters, digits, dot, dash and underscore. Its history and what it is doing go with it."
                color: cAsh; font.family: "monospace"; font.pixelSize: root.fs(11)
            }
            RowLayout {
                Layout.alignment: Qt.AlignRight
                spacing: root.sz(20)
                Lnk { text: "CANCEL"; base: cBone; font.pixelSize: root.fs(12); onClicked: renameDialog.close() }
                Lnk { text: "RENAME"; base: cPhosphor; font.pixelSize: root.fs(12)
                      onClicked: if (root.renameOpenSession(renameField.text)) renameDialog.close() }
            }
        }
    }
    Dialog {
        id: restartDialog
        modal: true
        anchors.centerIn: parent
        width: Math.min(root.sz(460), root.width - root.sz(40))
        padding: root.sz(20)
        closePolicy: Popup.CloseOnEscape | Popup.CloseOnPressOutside
        Overlay.modal: Rectangle { color: Qt.rgba(0, 0, 0, 0.6) }
        background: Rectangle { color: cPanel; radius: root.sz(12); border.color: cRust }
        header: Item {}
        footer: Item {}
        contentItem: ColumnLayout {
            spacing: root.sz(14)
            Text {
                Layout.fillWidth: true; wrapMode: Text.Wrap
                text: root.agentOpen ? "Restart session \"" + root.agentOpen.session + "\" on " + root.agentOpen.name + "?" : ""
                color: cBone; font.family: "monospace"; font.pixelSize: root.fs(14)
            }
            Text {
                Layout.fillWidth: true; wrapMode: Text.Wrap
                text: "Ends its process — what it is doing now is lost — and starts it again on the same conversation."
                color: cAsh; font.family: "monospace"; font.pixelSize: root.fs(11)
            }
            RowLayout {
                Layout.alignment: Qt.AlignRight
                spacing: root.sz(20)
                Lnk { text: "CANCEL"; base: cBone; font.pixelSize: root.fs(12); onClicked: restartDialog.close() }
                Lnk { text: "RESTART"; base: cRust; font.pixelSize: root.fs(12)
                      onClicked: if (root.restartOpenSession()) restartDialog.close() }
            }
        }
    }
    // A search result from before this agent had the conversation: no event to
    // jump to, so it is shown whole.
    Dialog {
        id: readingDialog
        modal: true
        anchors.centerIn: parent
        width: Math.min(root.sz(640), root.width - root.sz(40))
        height: Math.min(root.sz(520), root.height - root.sz(40))
        padding: root.sz(20)
        closePolicy: Popup.CloseOnEscape | Popup.CloseOnPressOutside
        Overlay.modal: Rectangle { color: Qt.rgba(0, 0, 0, 0.6) }
        background: Rectangle { color: cPanel; radius: root.sz(12); border.color: cSky }
        header: Item {}
        footer: Item {}
        contentItem: ColumnLayout {
            spacing: root.sz(10)
            RowLayout {
                Text {
                    Layout.fillWidth: true
                    text: root.reading ? [root.reading.role === "user" ? "YOU" : "CLAUDE", root.clock(root.epoch(root.reading.time)), "before this agent"].join("  ·  ") : ""
                    color: cAsh; font.family: "monospace"; font.pixelSize: root.fs(10); font.letterSpacing: 1
                }
                Lnk { text: "copy"; base: cAsh; font.pixelSize: root.fs(10); onClicked: root.copyText(root.reading ? root.reading.text : "") }
                Lnk { text: "close"; base: cBone; font.pixelSize: root.fs(10); onClicked: readingDialog.close() }
            }
            ScrollView {
                Layout.fillWidth: true; Layout.fillHeight: true
                clip: true
                TextEdit {
                    width: readingDialog.availableWidth
                    readOnly: true; selectByMouse: true; wrapMode: TextEdit.Wrap
                    textFormat: root.reading && root.reading.role !== "user" ? TextEdit.MarkdownText : TextEdit.RichText
                    text: !root.reading ? "" : root.reading.role !== "user" ? root.linkMarkdown(root.reading.text) : root.linkPlain(root.reading.text)
                    onLinkActivated: function(link) { root.openUrl(link) }
                    LinkCursor {}
                    color: cBone; font.family: "monospace"; font.pixelSize: root.fs(12)
                }
            }
        }
    }
    function readingOpen() { return readingDialog.visible }

    // For the harness, which cannot reach an id inside this component.
    function chatModelCount() { return chatModel.count }
    function chatModelAt(i) { return chatModel.get(i) }
    function composerText() { return composer.text }
    function nsAutoChecked() { return nsAuto.checked }

    Timer {
        // Finding agents: cheap, since the core probes in the background and
        // this returns at once what it has.
        interval: 3000
        running: root.agentsOpen && root.haveCore
        repeat: true; triggeredOnStart: true
        onTriggered: root.refreshAgents()
    }
    Timer {
        // The open session: collected by the core, read here several times a
        // second so a streamed reply grows smoothly.
        interval: 300
        running: root.agentsOpen && root.agentOpen !== null && root.haveCore
        repeat: true
        onTriggered: { root.pumpAgent(); root.pumpJobs(); root.pumpSearch(); root.pumpSpeech() }
    }

    // One session in the list: in its machine's group, or among the starred
    // ones, where the machine is named.
    component SessionCard: Rectangle {
        id: srow
        required property var host
        required property var sess
        property bool showHost: false
        readonly property bool up: root.hostReachable(srow.host)
        opacity: up ? 1 : 0.5

        readonly property bool isOpen: root.agentOpen !== null && root.agentOpen.address === srow.host.address && root.agentOpen.session === srow.sess.name
        height: sCol.implicitHeight + root.sz(16)
        radius: root.sz(8)
        color: isOpen ? Qt.rgba(0.21, 0.94, 0.63, 0.08) : cPanel
        border.width: 1
        border.color: srow.up && srow.sess.state === "waiting" ? cAmber : (isOpen ? cPhosphor : cLine)
        Column {
            id: sCol
            anchors.left: parent.left; anchors.right: parent.right; anchors.top: parent.top
            anchors.margins: root.sz(8)
            spacing: 3
            RowLayout {
                width: parent.width
                Text { text: srow.sess.name; color: cBone; font.family: "monospace"; font.pixelSize: root.fs(12); elide: Text.ElideRight; Layout.fillWidth: !srow.showHost }
                Rectangle {
                    readonly property int n: root.unreadOf(srow.host, srow.sess)
                    visible: n > 0
                    implicitWidth: unreadText.implicitWidth + root.sz(10); implicitHeight: unreadText.implicitHeight + root.sz(2)
                    radius: height / 2; color: cBone
                    Text { id: unreadText; anchors.centerIn: parent; text: parent.n > 99 ? "99+" : String(parent.n)
                           color: cVoid; font.family: "monospace"; font.pixelSize: root.fs(9); font.bold: true }
                }
                Text { visible: srow.showHost; text: srow.host.name; color: cAsh; font.family: "monospace"; font.pixelSize: root.fs(10); Layout.fillWidth: true }
                Pulse { visible: srow.up && srow.sess.state !== "idle"; tint: srow.sess.state === "waiting" ? cAmber : cPhosphor }
                Text {
                    Layout.rightMargin: root.sz(22)
                    text: !srow.up ? "unreachable" : srow.sess.state === "waiting" ? "NEEDS YOU" : (srow.sess.state === "working" ? "WORKING" : (srow.sess.running ? "idle" : "asleep"))
                    color: !srow.up ? cAsh : srow.sess.state === "waiting" ? cAmber : (srow.sess.state === "working" ? cPhosphor : cAsh)
                    font.family: "monospace"; font.pixelSize: root.fs(9); font.letterSpacing: 1
                }
            }
            Text {
                visible: (srow.sess.preview || "") !== ""
                width: parent.width
                text: srow.sess.preview || ""
                color: cAsh; font.family: "monospace"; font.pixelSize: root.fs(10)
                wrapMode: Text.Wrap; maximumLineCount: 2; elide: Text.ElideRight
            }
            Text {
                width: parent.width
                text: [root.clock(root.epoch(srow.sess.last_time)),
                       root.contextLabel(srow.sess.context_used, srow.sess.context_window),
                       root.harnessLabel(srow.sess.harness), root.shortModel(srow.sess.model),
                       srow.sess.auto_approve && !(srow.sess.caps && !srow.sess.caps.approve) ? "auto-approve" : ""].filter(function(x) { return x !== "" }).join("  ·  ")
                color: cAsh; font.family: "monospace"; font.pixelSize: root.fs(9); elide: Text.ElideRight
            }
        }
        // Handlers in a list's items only schedule their work (Qt.callLater):
        // openSession calls the core, each call spins a nested event loop, and
        // a refresh of the machines in it rebuilt this very card while its
        // handler ran — Qt aborts the process on that (2026-10-04).
        MouseArea { objectName: "sessionCardArea"; anchors.fill: parent; cursorShape: Qt.PointingHandCursor; onClicked: Qt.callLater(root.openSession, srow.host, srow.sess.name) }
        // Starred: listed first. Faint until it is. Above the row's own area.
        Text {
            anchors.right: parent.right; anchors.top: parent.top; anchors.margins: root.sz(6)
            z: 2
            text: "🍄"; font.pixelSize: root.fs(13)
            opacity: srow.sess.starred ? 1 : (starMouse.containsMouse ? 0.6 : 0.25)
            MouseArea { id: starMouse; anchors.fill: parent; anchors.margins: -4; hoverEnabled: true; cursorShape: Qt.PointingHandCursor
                        onClicked: Qt.callLater(root.setStarred, srow.host, srow.sess.name, !srow.sess.starred) }
        }
    }

    // A hand over a link in text: TextEdit shows none of its own. Hover only
    // (no buttons), so clicks still reach the text — links and selection.
    component LinkCursor: MouseArea {
        anchors.fill: parent
        acceptedButtons: Qt.NoButton
        hoverEnabled: true
        cursorShape: parent.linkAt(mouseX, mouseY) !== "" ? Qt.PointingHandCursor : Qt.IBeamCursor
    }

    component Lnk: Text {
        id: lnk
        signal clicked()
        property color base: cPhosphor
        color: lnkMouse.containsMouse ? cBone : base
        font.family: "monospace"; font.pixelSize: root.fs(11)
        font.underline: lnkMouse.containsMouse
        MouseArea { id: lnkMouse; anchors.fill: parent; hoverEnabled: true; cursorShape: Qt.PointingHandCursor; onClicked: lnk.clicked() }
    }

    component Pulse: Rectangle {
        property color tint: cPhosphor
        width: root.sz(8); height: width; radius: width / 2
        color: tint
        SequentialAnimation on opacity {
            loops: Animation.Infinite
            NumberAnimation { from: 1; to: 0.25; duration: 900 }
            NumberAnimation { from: 0.25; to: 1; duration: 900 }
        }
    }

    Rectangle {
        id: agentsPanel
        anchors.fill: parent
        visible: root.agentsOpen
        z: 50
        color: cVoid


        RowLayout {
            anchors.fill: parent
            anchors.margins: root.sz(20)
            spacing: root.sz(18)

            // --- machines and their sessions ---------------------------------
            // Exactly as wide as set: a preferred width alone let the
            // conversation's longest line take room from it.
            ColumnLayout {
                objectName: "agentList"
                Layout.preferredWidth: root.listWidthPx()
                Layout.minimumWidth: root.listWidthPx()
                Layout.maximumWidth: root.listWidthPx()
                Layout.fillHeight: true
                spacing: root.sz(10)

                RowLayout {
                    spacing: 8
                    Pulse {}
                    Text { text: "AGENTS"; color: cPhosphor; font.family: "monospace"; font.pixelSize: root.fs(12); font.letterSpacing: 1.5 }
                    Item { Layout.fillWidth: true }
                    // The session quota at a glance: amber from half, red from 80%.
                    Lnk { visible: root.haveCore; text: root.usageGlance ? "usage " + root.usageGlance.percent + "%" : "usage"
                          base: !root.usageGlance ? cAsh : root.usageGlance.level === 2 ? cRust : root.usageGlance.level === 1 ? cAmber : cAsh
                          font.pixelSize: root.fs(10); onClicked: Qt.callLater(root.openUsage) }
                    Lnk { visible: root.haveCore; text: "voice"; base: cAsh; font.pixelSize: root.fs(10); onClicked: Qt.callLater(root.openVoice) }
                }
                Text {
                    Layout.fillWidth: true
                    visible: !root.haveCore
                    wrapMode: Text.Wrap
                    text: "Agents need shrooms_core, which runs inside Basecamp."
                    color: cAsh; font.family: "monospace"; font.pixelSize: root.fs(11)
                }
                Text {
                    Layout.fillWidth: true
                    visible: root.haveCore && root.agentHosts.length === 0
                    wrapMode: Text.Wrap
                    text: "Looking for agents among the reachable peers… An agent is shrooms-agent on one of your machines, on its mesh address, port 7387."
                    color: cAsh; font.family: "monospace"; font.pixelSize: root.fs(11)
                }

                // A ScrollView over a Column, not a ListView: the hosts are a
                // new array every round of finding, and a ListView given a
                // new model rebuilds and goes back to the top — the list
                // jumped up under the reader on every refresh (2026-10-07).
                ScrollView {
                    id: hostList
                    Layout.fillWidth: true
                    Layout.fillHeight: true
                    clip: true
                    contentWidth: availableWidth
                    Column {
                        width: hostList.availableWidth
                        spacing: root.sz(6)
                    Column {
                        width: hostList.availableWidth
                        spacing: root.sz(6)
                        bottomPadding: root.starredSessions.length > 0 ? root.sz(10) : 0
                        Text {
                            visible: root.starredSessions.length > 0
                            text: "🍄  STARRED"; color: cPhosphor; font.family: "monospace"; font.pixelSize: root.fs(11); font.letterSpacing: 1.5
                        }
                        Repeater {
                            model: root.starredSessions
                            delegate: SessionCard {
                                required property var modelData
                                width: hostList.availableWidth
                                host: modelData.host
                                sess: modelData.sess
                                showHost: true
                            }
                        }
                    }
                    Repeater {
                    model: root.agentHosts
                    delegate: Column {
                        id: hostCol
                        required property var modelData
                        width: hostList.availableWidth
                        spacing: root.sz(6)
                        readonly property bool up: root.hostReachable(hostCol.modelData)
                        RowLayout {
                            width: parent.width
                            spacing: 8
                            opacity: hostCol.up ? 1 : 0.45
                            Rectangle { width: root.sz(9); height: width; radius: width / 2; color: "transparent"; border.width: 2; border.color: root.meshTint(hostCol.modelData.mesh) }
                            Text { text: hostCol.modelData.name; color: cBone; font.family: "monospace"; font.pixelSize: root.fs(14) }
                            Text { text: hostCol.modelData.mesh; color: root.meshTint(hostCol.modelData.mesh); font.family: "monospace"; font.pixelSize: root.fs(10) }
                            Text { visible: !hostCol.up; text: "unreachable · seen " + root.clock(hostCol.modelData.lastSeen); color: cAsh; font.family: "monospace"; font.pixelSize: root.fs(10) }
                            Item { Layout.fillWidth: true }
                            Lnk { visible: hostCol.up; text: "+ session"; onClicked: {
                                newSession.host = hostCol.modelData
                                root.agentCreating = true
                                Qt.callLater(function() { root.loadHarnesses(hostCol.modelData); root.loadConversations(hostCol.modelData) })
                            } }
                        }
                        Text {
                            visible: hostCol.modelData.sessions.length === 0
                            text: "no sessions yet"; color: cAsh; font.family: "monospace"; font.pixelSize: root.fs(10)
                        }
                        Text {
                            visible: hostCol.modelData.sessions.length > 0 && root.unstarred(hostCol.modelData).length === 0
                            text: "all starred"; color: cAsh; font.family: "monospace"; font.pixelSize: root.fs(10)
                        }
                        Repeater {
                            model: root.unstarred(hostCol.modelData)
                            delegate: SessionCard {
                                required property var modelData
                                width: hostCol.width
                                host: hostCol.modelData
                                sess: modelData
                            }
                        }
                    }
                    }
                    }
                }
            }

            // The divider: drag to resize the list; double-click for the
            // default width.
            Item {
                Layout.fillHeight: true
                Layout.preferredWidth: root.sz(9)
                Rectangle {
                    anchors.horizontalCenter: parent.horizontalCenter
                    width: dividerMouse.containsMouse || dividerMouse.pressed ? 3 : 1
                    height: parent.height
                    color: dividerMouse.containsMouse || dividerMouse.pressed ? cPhosphor : cLine
                }
                MouseArea {
                    id: dividerMouse
                    anchors.fill: parent
                    hoverEnabled: true
                    cursorShape: Qt.SplitHCursor
                    property real startX: 0
                    property real startWidth: 0
                    onPressed: function(ev) { startX = mapToItem(agentsPanel, ev.x, 0).x; startWidth = root.listWidth }
                    onPositionChanged: function(ev) {
                        if (!pressed) return
                        var dx = mapToItem(agentsPanel, ev.x, 0).x - startX
                        var px = Math.max(root.sz(200), Math.min(root.sz(startWidth) + dx, agentsPanel.width * 0.6))
                        root.listWidth = px / root.uiScale
                    }
                    onReleased: root.savePref("agent_list_width", Math.round(root.listWidth))
                    onDoubleClicked: { root.listWidth = 320; root.savePref("agent_list_width", 320) }
                }
            }

            // --- the conversation -------------------------------------------
            // Its width is what is left, never what its text would like: an
            // implicit width from a long line made it grow and the list shrink.
            ColumnLayout {
                Layout.fillWidth: true
                Layout.preferredWidth: 0
                Layout.minimumWidth: 0
                Layout.fillHeight: true
                spacing: root.sz(8)

                // New session form, in place of the conversation.
                ColumnLayout {
                    id: newSession
                    property var host: null
                    visible: root.agentCreating
                    Layout.fillWidth: true
                    spacing: root.sz(8)
                    Text { text: "NEW SESSION ON " + (newSession.host ? newSession.host.name.toUpperCase() : ""); color: cPhosphor; font.family: "monospace"; font.pixelSize: root.fs(12); font.letterSpacing: 1.5 }
                    Text { text: "A name and a directory on that machine, like a cl session. ~ is that machine's home."; color: cAsh; font.family: "monospace"; font.pixelSize: root.fs(10) }
                    TextField { id: nsName; Layout.fillWidth: true; placeholderTextColor: cAsh; placeholderText: "name"; color: cBone; font.family: "monospace"; background: Rectangle { color: cPanel; border.color: nsName.activeFocus ? cPhosphor : cLine; radius: 6 } }
                    TextField { id: nsDir; Layout.fillWidth: true; placeholderTextColor: cAsh; text: "~/"; placeholderText: "directory"; color: cBone; font.family: "monospace"; background: Rectangle { color: cPanel; border.color: nsDir.activeFocus ? cPhosphor : cLine; radius: 6 } }
                    // Which coding agent, when the machine has more than Claude Code.
                    Row {
                        visible: root.harnesses.length > 1
                        spacing: root.sz(8)
                        Repeater {
                            model: root.harnesses
                            delegate: Rectangle {
                                id: hchip
                                required property var modelData
                                readonly property bool on: root.nsHarness === hchip.modelData.name
                                width: hText.implicitWidth + root.sz(20); height: hText.implicitHeight + root.sz(10)
                                radius: root.sz(8)
                                color: on ? cPhosphor : "transparent"; border.color: on ? cPhosphor : cLine
                                Text { id: hText; anchors.centerIn: parent; text: hchip.modelData.title
                                       color: hchip.on ? cVoid : cBone; font.family: "monospace"; font.pixelSize: root.fs(11) }
                                MouseArea { anchors.fill: parent; cursorShape: Qt.PointingHandCursor; onClicked: root.nsHarness = hchip.modelData.name }
                            }
                        }
                    }
                    CheckBox { id: nsAuto; visible: root.harnessApproves(root.nsHarness); text: "auto-approve — never ask, like --dangerously-skip-permissions"; contentItem: Text { leftPadding: nsAuto.indicator.width + 6; text: nsAuto.text; color: cAsh; font.family: "monospace"; font.pixelSize: root.fs(10); verticalAlignment: Text.AlignVCenter } }
                    RowLayout {
                        Lnk {
                            text: "create"
                            onClicked: {
                                if (root.createSession(newSession.host, nsName.text.trim(), nsDir.text.trim(), nsAuto.checked)) {
                                    root.agentCreating = false
                                    root.refreshAgents()
                                    root.openSession(newSession.host, nsName.text.trim())
                                    nsName.text = ""
                                }
                            }
                        }
                        Lnk { text: "cancel"; base: cAsh; onClicked: root.agentCreating = false }
                    }

                    // Or carry on one that started somewhere else — in a
                    // terminal, under cl. Resuming keeps writing to the same
                    // conversation, so this continues it rather than copying it.
                    Text {
                        visible: root.nsHarness === "claude"
                        Layout.topMargin: root.sz(14)
                        text: "OR CONTINUE A CONVERSATION FROM " + (newSession.host ? newSession.host.name.toUpperCase() : "")
                        color: cPhosphor; font.family: "monospace"; font.pixelSize: root.fs(12); font.letterSpacing: 1.5
                    }
                    Text {
                        visible: root.nsHarness === "claude"
                        Layout.fillWidth: true; wrapMode: Text.Wrap
                        text: "Newest first. A terminal still open in the same directory may hold it: stop it before carrying on here, or the two will write over each other's turns."
                        color: cAsh; font.family: "monospace"; font.pixelSize: root.fs(10)
                    }
                    Text {
                        visible: root.nsHarness === "claude" && root.conversations.length === 0
                        text: root.conversationsProblem !== "" ? root.conversationsProblem
                            : root.conversationsLoaded ? "none here yet — only conversations that ran in a directory on this machine are listed" : "looking…"
                        color: cAsh; font.family: "monospace"; font.pixelSize: root.fs(10)
                    }
                    ListView {
                        id: convList
                        visible: root.nsHarness === "claude"
                        Layout.fillWidth: true
                        Layout.preferredHeight: Math.min(contentHeight, root.sz(420))
                        clip: true
                        spacing: root.sz(6)
                        model: root.conversations
                        delegate: Rectangle {
                            id: conv
                            required property var modelData
                            readonly property bool taken: (conv.modelData.adopted_by || "") !== ""
                            width: convList.width
                            height: convCol.implicitHeight + root.sz(16)
                            radius: root.sz(8)
                            color: cPanel
                            border.color: convMouse.containsMouse && !taken ? cPhosphor : cLine
                            opacity: taken ? 0.6 : 1
                            MouseArea {
                                id: convMouse
                                anchors.fill: parent; hoverEnabled: true
                                cursorShape: conv.taken ? Qt.ArrowCursor : Qt.PointingHandCursor
                                onClicked: if (!conv.taken) Qt.callLater(root.takeOver, newSession.host, conv.modelData)
                            }
                            Column {
                                id: convCol
                                x: root.sz(10); y: root.sz(8); width: parent.width - root.sz(20)
                                spacing: 3
                                RowLayout {
                                    width: parent.width
                                    Text { text: root.shortDir(conv.modelData.dir || "?"); color: cBone; font.family: "monospace"; font.pixelSize: root.fs(12); elide: Text.ElideMiddle; Layout.fillWidth: true }
                                    Text { text: root.clock(root.epoch(conv.modelData.modified)); color: cAsh; font.family: "monospace"; font.pixelSize: root.fs(9) }
                                }
                                Text { visible: (conv.modelData.last_user || "") !== ""; width: parent.width; elide: Text.ElideRight
                                       text: "you: " + (conv.modelData.last_user || ""); color: cAsh; font.family: "monospace"; font.pixelSize: root.fs(10) }
                                Text { visible: (conv.modelData.last_assistant || "") !== ""; width: parent.width; elide: Text.ElideRight
                                       text: "claude: " + (conv.modelData.last_assistant || ""); color: cAsh; font.family: "monospace"; font.pixelSize: root.fs(10) }
                                Text { visible: conv.taken; text: "continued here as \"" + (conv.modelData.adopted_by || "") + "\""; color: cPhosphor; font.family: "monospace"; font.pixelSize: root.fs(10) }
                                Repeater {
                                    model: conv.modelData.terminals || []
                                    delegate: RowLayout {
                                        id: term
                                        required property var modelData
                                        spacing: 8
                                        Pulse { tint: cAmber }
                                        Text { text: "open in a terminal: " + (term.modelData.tmux ? "tmux " + term.modelData.tmux : "pid " + term.modelData.pid)
                                               color: cAmber; font.family: "monospace"; font.pixelSize: root.fs(10) }
                                        Lnk { text: "stop it"; base: cRust; font.pixelSize: root.fs(10)
                                              onClicked: Qt.callLater(root.stopTerminal, newSession.host, term.modelData.pid) }
                                    }
                                }
                            }
                        }
                    }
                }

                Text {
                    visible: !root.agentCreating && root.agentOpen === null
                    text: "Pick a session on the left."
                    color: cAsh; font.family: "monospace"; font.pixelSize: root.fs(11)
                }

                // Header: the session, its facts, its switches.
                ColumnLayout {
                    visible: !root.agentCreating && root.agentOpen !== null
                    Layout.fillWidth: true
                    spacing: 4
                    RowLayout {
                        spacing: 8
                        // Clicked, it is renamed.
                        Lnk { text: root.agentOpen ? root.agentOpen.session : ""; base: cBone; font.pixelSize: root.fs(16); onClicked: root.askRename() }
                        Pulse { visible: root.agentWorking; }
                        Item { Layout.fillWidth: true }
                        Lnk {
                            // A harness that never asks has nothing to approve.
                            visible: !(root.agentInfo && root.agentInfo.caps && !root.agentInfo.caps.approve)
                            readonly property bool on: root.agentInfo !== null && !!root.agentInfo.auto_approve
                            text: on ? "AUTO-APPROVE ON" : "asks first"
                            base: on ? cPhosphor : cAsh
                            onClicked: root.setAutoApprove(!on)
                        }
                        Lnk { text: root.searchOpen ? "close search" : "search"; base: cSky
                              onClicked: { root.searchOpen = !root.searchOpen; if (root.searchOpen) searchField.forceActiveFocus() } }
                        Lnk { readonly property bool on: root.autoPlayOn(root.agentOpen)
                              text: on ? "AUTO-PLAY" : "auto-play"; base: on ? cPhosphor : cAsh
                              onClicked: root.setAutoPlay(root.agentOpen, !on) }
                        Lnk { text: "restart"; base: cAsh; onClicked: root.askRestart() }
                        Lnk { text: "delete"; base: cAsh; onClicked: root.askDelete() }
                        Lnk { visible: root.agentWorking; text: "■ stop"; base: cRust; onClicked: root.stopTurn() }
                    }
                    Text {
                        text: root.agentOpen ? [root.agentOpen.name, root.agentOpen.mesh,
                              root.agentInfo ? root.harnessLabel(root.agentInfo.harness) : "",
                              root.agentInfo ? root.shortModel(root.agentInfo.model) : "",
                              root.agentInfo ? root.contextLabel(root.agentInfo.context_used, root.agentInfo.context_window) : "",
                              root.agentKept > 0 ? "offline — as it was " + root.keptWhen(root.agentKept) : "",
                              root.agentConnected ? "" : ("reconnecting" + (root.agentProblem ? " — " + root.agentProblem : ""))
                             ].filter(function(x) { return x !== "" }).join("  ·  ") : ""
                        color: root.agentConnected ? cAsh : cAmber
                        font.family: "monospace"; font.pixelSize: root.fs(10)
                    }
                    Rectangle {
                        visible: root.agentInfo !== null && root.agentInfo.context_window > 0
                        Layout.fillWidth: true; height: 2; color: cLine
                        Rectangle {
                            readonly property real f: root.agentInfo && root.agentInfo.context_window ? Math.min(1, root.agentInfo.context_used / root.agentInfo.context_window) : 0
                            width: parent.width * f; height: 2
                            color: f >= 0.9 ? cRust : (f >= 0.7 ? cAmber : cPhosphor)
                        }
                    }
                }

                ColumnLayout {
                    visible: !root.agentCreating && root.agentOpen !== null && root.searchOpen
                    Layout.fillWidth: true
                    Layout.fillHeight: true
                    spacing: root.sz(8)
                    TextField {
                        id: searchField
                        Layout.fillWidth: true
                        placeholderText: "search the whole conversation — Enter"
                        placeholderTextColor: cAsh; color: cBone; font.family: "monospace"
                        background: Rectangle { color: cPanel; border.color: searchField.activeFocus ? cSky : cLine; radius: 6 }
                        onAccepted: root.runSearch(text)
                    }
                    RowLayout {
                        spacing: 8
                        Pulse { visible: root.searchBusy; tint: cSky }
                        Text {
                            text: root.searchBusy ? "searching…"
                                : root.searchFound === null ? "Words anywhere in what was typed or answered — also before this agent had it. Case and accents do not matter."
                                : root.searchFound.length === 0 ? "Nothing found."
                                : root.searchFound.length >= 100 ? "the newest 100" : root.searchFound.length + " found"
                            color: cAsh; font.family: "monospace"; font.pixelSize: root.fs(10)
                            Layout.fillWidth: true; wrapMode: Text.Wrap
                        }
                    }
                    ListView {
                        id: foundList
                        Layout.fillWidth: true
                        Layout.fillHeight: true
                        clip: true
                        spacing: root.sz(8)
                        model: root.searchFound || []
                        ScrollBar.vertical: ScrollBar {}
                        delegate: Rectangle {
                            id: frow
                            required property var modelData
                            width: foundList.width
                            height: frowCol.implicitHeight + root.sz(16)
                            radius: root.sz(8)
                            color: frowMouse.containsMouse ? cPanel : "transparent"
                            border.color: cLine
                            Column {
                                id: frowCol
                                x: root.sz(8); y: root.sz(8); width: parent.width - root.sz(16)
                                spacing: 4
                                Text {
                                    text: [frow.modelData.role === "user" ? "YOU" : "CLAUDE", root.clock(root.epoch(frow.modelData.time)),
                                           frow.modelData.seq ? "" : "before this agent"].filter(function(x) { return x !== "" }).join("  ·  ")
                                    color: frow.modelData.role === "user" ? cPhosphor : cAsh
                                    font.family: "monospace"; font.pixelSize: root.fs(9); font.letterSpacing: 1
                                }
                                Text {
                                    width: parent.width
                                    text: frow.modelData.snippet
                                    color: cBone; font.family: "monospace"; font.pixelSize: root.fs(11)
                                    wrapMode: Text.Wrap; maximumLineCount: 4; elide: Text.ElideRight
                                }
                            }
                            MouseArea { id: frowMouse; anchors.fill: parent; hoverEnabled: true; cursorShape: Qt.PointingHandCursor
                                        onClicked: Qt.callLater(root.openFound, frow.modelData) }
                        }
                    }
                }

                ListView {
                    id: chatList
                    visible: !root.agentCreating && root.agentOpen !== null && !root.searchOpen
                    Layout.fillWidth: true
                    Layout.fillHeight: true
                    clip: true
                    spacing: root.sz(10)
                    model: chatModel
                    // Nothing to show yet: say what is happening rather than
                    // a blank pane — a session with no copy kept here waits
                    // for its machine.
                    Row {
                        anchors.centerIn: parent
                        visible: root.chatPlaceholder !== ""
                        spacing: root.sz(8)
                        Pulse { visible: !root.agentCaughtUp; anchors.verticalCenter: parent.verticalCenter }
                        Text { text: root.chatPlaceholder; color: cAsh; font.family: "monospace"; font.pixelSize: root.fs(11) }
                    }
                    ScrollBar.vertical: ScrollBar {
                        onPressedChanged: if (!pressed) root.chatStick = chatList.atYEnd
                    }
                    // Messages measure themselves after they are added, so
                    // the height keeps growing after a scroll to the end: it
                    // is followed for as long as the reader is down there.
                    onContentHeightChanged: if (root.chatStick) Qt.callLater(chatList.positionViewAtEnd)
                    onMovementEnded: root.chatStick = chatList.atYEnd
                    onAtYEndChanged: if (atYEnd) root.chatStick = true
                    // Scrolling up by any means — the wheel included, which
                    // reports no movement — stops the following. Content
                    // growing never moves the view up, so this is the reader.
                    property real lastY: 0
                    onContentYChanged: {
                        if (contentY < lastY - 2 && !atYEnd) root.chatStick = false
                        lastY = contentY
                    }

                    // Files dropped on the conversation are sent like 📎 ones.
                    DropArea {
                        anchors.fill: parent
                        onDropped: function(drop) {
                            if (!drop.hasUrls) return
                            for (var i = 0; i < drop.urls.length; i++) root.attachFile(drop.urls[i])
                            drop.accept()
                        }
                    }

                    Rectangle {
                        visible: !chatList.atYEnd && chatModel.count > 0
                        anchors.right: parent.right; anchors.bottom: parent.bottom; anchors.margins: root.sz(12)
                        width: root.sz(34); height: width; radius: width / 2
                        color: cPanel; border.color: cPhosphor
                        z: 5
                        Text { anchors.centerIn: parent; text: "↓"; color: cPhosphor; font.pixelSize: root.fs(16) }
                        MouseArea { anchors.fill: parent; cursorShape: Qt.PointingHandCursor
                                    onClicked: { root.chatStick = true; chatList.positionViewAtEnd() } }
                    }
                    header: Item {
                        readonly property int firstSeq: root.agentEventsList.length > 0 ? root.agentEventsList[0].seq : 0
                        width: chatList.width
                        height: visible ? root.sz(30) : 0
                        visible: root.agentTailNow !== 0 && firstSeq > 1
                        Lnk {
                            anchors.centerIn: parent
                            text: root.moreLabel(parent.firstSeq - 1)
                            font.pixelSize: root.fs(10)
                            onClicked: Qt.callLater(root.loadMore)
                        }
                    }
                    footer: Item {
                        width: chatList.width
                        height: liveCol.implicitHeight + root.sz(8)
                        Column {
                            id: liveCol
                            width: parent.width
                            topPadding: root.sz(8)
                            spacing: root.sz(8)
                            // What is written and not sent yet: the machine is
                            // unreachable, or it is being sent now.
                            Repeater {
                                model: root.agentQueued
                                delegate: Rectangle {
                                    id: qrow
                                    required property var modelData
                                    width: liveCol.width
                                    height: qcol.implicitHeight + root.sz(16)
                                    radius: root.sz(10)
                                    color: Qt.rgba(0.21, 0.94, 0.63, 0.03)
                                    border.color: Qt.rgba(0.21, 0.94, 0.63, 0.25)
                                    Column {
                                        id: qcol
                                        x: root.sz(10); y: root.sz(8); width: parent.width - root.sz(20)
                                        spacing: 4
                                        RowLayout {
                                            width: parent.width
                                            Pulse { tint: cAsh }
                                            Text { Layout.fillWidth: true; elide: Text.ElideRight; text: root.queuedLabel(qrow.modelData)
                                                   color: cAsh; font.family: "monospace"; font.pixelSize: root.fs(9); font.letterSpacing: 1 }
                                            Lnk { text: "cancel"; base: cRust; font.pixelSize: root.fs(9); onClicked: Qt.callLater(root.cancelQueued, qrow.modelData.id) }
                                        }
                                        Text { width: parent.width; wrapMode: Text.Wrap
                                               visible: text !== ""
                                               text: qrow.modelData.kind === "voice" ? "🎤 voice note" : qrow.modelData.text
                                               color: Qt.rgba(0.84, 0.87, 0.89, 0.7); font.family: "monospace"; font.pixelSize: root.fs(12) }
                                        Repeater {
                                            model: qrow.modelData.files || []
                                            delegate: Text {
                                                required property var modelData
                                                text: (modelData.sent ? "📎 ✓ " : "📎 ") + modelData.name
                                                color: cSky; font.family: "monospace"; font.pixelSize: root.fs(10)
                                            }
                                        }
                                    }
                                }
                            }
                            Rectangle {
                                visible: root.agentStreaming !== ""
                                width: parent.width
                                height: streamText.implicitHeight + root.sz(20)
                                color: cPanel; radius: root.sz(10); border.color: cLine
                                TextEdit {
                                    id: streamText
                                    x: root.sz(10); y: root.sz(10); width: parent.width - root.sz(20)
                                    readOnly: true; selectByMouse: true; wrapMode: TextEdit.Wrap
                                    textFormat: TextEdit.MarkdownText
                                    text: root.linkMarkdown(root.agentStreaming) + " ▍"
                                    onLinkActivated: function(link) { root.openUrl(link) }
                                    LinkCursor {}
                                    color: cBone; font.family: "monospace"; font.pixelSize: root.fs(12)
                                }
                            }
                            // Where the eye is while it works: stopping the reply
                            // is offered here too, not only in the header, where
                            // a bare "stop" did not say what it stopped.
                            RowLayout {
                                visible: root.agentWorking
                                spacing: 8
                                Pulse {}
                                Text { text: root.agentStreaming === "" ? "thinking…" : "writing…"; color: cAsh; font.family: "monospace"; font.pixelSize: root.fs(10) }
                                Lnk { text: "■ stop"; base: cRust; font.pixelSize: root.fs(10); onClicked: root.stopTurn() }
                                Lnk { text: "↻ restart"; base: cAsh; font.pixelSize: root.fs(10); onClicked: root.askRestart() }
                            }
                        }
                    }
                    delegate: Item {
                        id: crow
                        required property int index
                        required property real seq
                        required property string kind
                        required property string text
                        required property string by
                        required property real time
                        required property bool earlier
                        required property bool error
                        required property string pid
                        required property string tool
                        required property string description
                        required property bool open
                        required property string answer
                        required property bool voice
                        required property bool outside
                        required property string qjson
                        width: chatList.width
                        height: crowCol.implicitHeight

                        Column {
                            id: crowCol
                            width: parent.width
                            spacing: 4

                            Text {
                                visible: crow.earlier && crow.index === 0
                                text: "— earlier, from the transcript —"
                                color: cAsh; font.family: "monospace"; font.pixelSize: root.fs(10)
                            }
                            Text {
                                visible: !crow.earlier && crow.index > 0 && chatModel.get(crow.index - 1).earlier
                                text: "— with the agent —"
                                color: cAsh; font.family: "monospace"; font.pixelSize: root.fs(10)
                            }

                            // You, or the model: a bubble with its time and a copy link.
                            Rectangle {
                                visible: crow.kind === "you" || crow.kind === "said"
                                width: parent.width
                                height: bubbleCol.implicitHeight + root.sz(20)
                                radius: root.sz(10)
                                color: crow.kind === "you" ? Qt.rgba(0.21, 0.94, 0.63, crow.earlier ? 0.04 : 0.07) : cPanel
                                border.color: crow.seq !== 0 && crow.seq === root.agentLit ? cSky
                                            : crow.kind === "you" ? Qt.rgba(0.21, 0.94, 0.63, 0.35) : cLine
                                border.width: crow.seq !== 0 && crow.seq === root.agentLit ? 2 : 1
                                opacity: crow.earlier ? 0.8 : 1
                                Column {
                                    id: bubbleCol
                                    x: root.sz(10); y: root.sz(10); width: parent.width - root.sz(20)
                                    spacing: 4
                                    RowLayout {
                                        width: parent.width
                                        Text {
                                            text: [crow.kind === "you" ? (crow.outside ? crow.by.toUpperCase() : crow.voice ? "YOU 🎤" : "YOU") : "", crow.outside ? "" : crow.by, root.clock(crow.time)].filter(function(x) { return x !== "" }).join("  ·  ")
                                            color: crow.kind === "you" ? cPhosphor : cAsh
                                            font.family: "monospace"; font.pixelSize: root.fs(9); font.letterSpacing: 1
                                            Layout.fillWidth: true
                                        }
                                        Lnk { visible: crow.kind === "said" && root.speakingKey !== root.speakKey(crow.seq)
                                              text: "▶ listen"; base: cSky; font.pixelSize: root.fs(9)
                                              onClicked: Qt.callLater(root.readAloud, crow.seq, crow.text) }
                                        Lnk { text: "copy"; base: cAsh; font.pixelSize: root.fs(9); onClicked: Qt.callLater(root.copyText, crow.text) }
                                    }
                                    TextEdit {
                                        readonly property bool readingThis: crow.kind === "said" && root.speakingKey !== "" && root.speakingKey === root.speakKey(crow.seq)
                                        width: parent.width
                                        readOnly: true; selectByMouse: true; wrapMode: TextEdit.Wrap
                                        textFormat: readingThis ? TextEdit.RichText : (crow.kind === "said" ? TextEdit.MarkdownText : TextEdit.RichText)
                                        text: readingThis ? root.aloudHtml() : (crow.kind === "said" ? root.linkMarkdown(crow.text) : root.linkPlain(crow.text))
                                        color: cBone; selectionColor: Qt.rgba(0.21, 0.94, 0.63, 0.35)
                                        font.family: "monospace"; font.pixelSize: root.fs(12)
                                        onLinkActivated: function(link) { root.openUrl(link) }
                                        LinkCursor {}
                                    }
                                }
                            }

                            // A tool it used: the first line of its command, the rest on a click —
                            // a long script filled the screen (2026-10-03).
                            Text {
                                id: toolText
                                property bool expanded: false
                                readonly property bool more: crow.text.indexOf("\n") >= 0 || crow.text.length > 160
                                visible: crow.kind === "tool"
                                width: parent.width
                                text: "▸ " + (expanded ? crow.text : crow.text.split("\n")[0].slice(0, 160) + (more ? "  … (click)" : ""))
                                color: cViolet; font.family: "monospace"; font.pixelSize: root.fs(11)
                                wrapMode: expanded ? Text.Wrap : Text.NoWrap
                                elide: expanded ? Text.ElideNone : Text.ElideRight
                                MouseArea { anchors.fill: parent; enabled: toolText.more; cursorShape: toolText.more ? Qt.PointingHandCursor : Qt.ArrowCursor
                                            onClicked: toolText.expanded = !toolText.expanded }
                            }

                            TextEdit {
                                id: outText
                                property bool expanded: false
                                visible: crow.kind === "output"
                                width: parent.width
                                leftPadding: root.sz(14)
                                readOnly: true; selectByMouse: true; wrapMode: TextEdit.Wrap
                                text: expanded ? crow.text : (crow.text.split("\n")[0].slice(0, 160) + ((crow.text.indexOf("\n") >= 0 || crow.text.length > 160) ? "  … (click)" : ""))
                                color: crow.error ? cRust : cAsh
                                font.family: "monospace"; font.pixelSize: root.fs(10)
                                MouseArea { anchors.fill: parent; acceptedButtons: Qt.LeftButton; propagateComposedEvents: true
                                            onClicked: function(m) { outText.expanded = !outText.expanded; m.accepted = false } }
                            }

                            // A permission prompt: what it would run, in full, above the buttons.
                            Rectangle {
                                visible: crow.kind === "prompt"
                                width: parent.width
                                height: promptCol.implicitHeight + root.sz(24)
                                radius: root.sz(10)
                                color: cPanel
                                border.color: crow.open ? cAmber : cLine
                                Column {
                                    id: promptCol
                                    x: root.sz(12); y: root.sz(12); width: parent.width - root.sz(24)
                                    spacing: 8
                                    RowLayout {
                                        spacing: 8
                                        Pulse { visible: crow.open; tint: cAmber }
                                        Text { text: crow.open ? (crow.tool.toUpperCase() + " WANTS TO RUN") : crow.tool
                                               color: crow.open ? cAmber : cAsh; font.family: "monospace"; font.pixelSize: root.fs(10); font.letterSpacing: 1 }
                                    }
                                    Rectangle {
                                        width: parent.width
                                        height: cmdText.implicitHeight + root.sz(16)
                                        color: cVoid; radius: 6; border.color: cLine
                                        TextEdit {
                                            id: cmdText
                                            x: root.sz(8); y: root.sz(8); width: parent.width - root.sz(16)
                                            readOnly: true; selectByMouse: true; wrapMode: TextEdit.WrapAnywhere
                                            text: crow.text; color: cChartreuse
                                            font.family: "monospace"; font.pixelSize: root.fs(11)
                                        }
                                    }
                                    Text { visible: crow.description !== "" && crow.description !== crow.text; width: parent.width; wrapMode: Text.Wrap
                                           text: crow.description; color: cAsh; font.family: "monospace"; font.pixelSize: root.fs(10) }
                                    RowLayout {
                                        visible: crow.open
                                        spacing: root.sz(16)
                                        Lnk { text: "ALLOW"; font.pixelSize: root.fs(12); onClicked: Qt.callLater(root.answerPrompt, crow.pid, true) }
                                        Lnk { text: "DENY"; base: cRust; font.pixelSize: root.fs(12); onClicked: Qt.callLater(root.answerPrompt, crow.pid, false) }
                                    }
                                    Text { visible: !crow.open; text: crow.answer; color: cAsh; font.family: "monospace"; font.pixelSize: root.fs(10) }
                                }
                            }

                            // The model asking something: its questions, the options
                            // to pick, or your own words, then sent together.
                            Rectangle {
                                id: qcard
                                visible: crow.kind === "question"
                                readonly property var questions: crow.kind === "question" && crow.qjson ? JSON.parse(crow.qjson) : []
                                width: parent.width
                                height: visible ? qCol.implicitHeight + root.sz(24) : 0
                                radius: root.sz(10)
                                color: cPanel
                                border.color: crow.open ? cSky : cLine
                                Column {
                                    id: qCol
                                    x: root.sz(12); y: root.sz(12); width: parent.width - root.sz(24)
                                    spacing: 10
                                    RowLayout {
                                        spacing: 8
                                        Pulse { visible: crow.open; tint: cSky }
                                        Text { text: crow.open ? "CLAUDE ASKS" : "CLAUDE ASKED"
                                               color: crow.open ? cSky : cAsh; font.family: "monospace"; font.pixelSize: root.fs(10); font.letterSpacing: 1 }
                                    }
                                    Repeater {
                                        model: qcard.questions
                                        delegate: Column {
                                            id: qq
                                            required property var modelData
                                            width: qCol.width
                                            spacing: 6
                                            Text { visible: !!qq.modelData.header; text: String(qq.modelData.header || "").toUpperCase() + (qq.modelData.multiSelect ? "  ·  pick any" : "")
                                                   color: cAsh; font.family: "monospace"; font.pixelSize: root.fs(9); font.letterSpacing: 1 }
                                            Text { width: parent.width; wrapMode: Text.Wrap; text: qq.modelData.question
                                                   color: cBone; font.family: "monospace"; font.pixelSize: root.fs(12) }
                                            Repeater {
                                                model: crow.open ? (qq.modelData.options || []) : []
                                                delegate: Rectangle {
                                                    id: opt
                                                    required property var modelData
                                                    readonly property bool on: root.isPicked(crow.pid, qq.modelData.question, opt.modelData.label)
                                                    width: qq.width
                                                    height: optCol.implicitHeight + root.sz(14)
                                                    radius: root.sz(8)
                                                    color: on ? Qt.rgba(0.35, 0.66, 1.0, 0.12) : (optMouse.containsMouse ? cVoid : "transparent")
                                                    border.color: on ? cSky : cLine
                                                    Column {
                                                        id: optCol
                                                        x: root.sz(10); y: root.sz(7); width: parent.width - root.sz(20)
                                                        Text { text: opt.modelData.label; color: opt.on ? cSky : cBone; font.family: "monospace"; font.pixelSize: root.fs(12) }
                                                        Text { visible: !!opt.modelData.description; width: parent.width; wrapMode: Text.Wrap
                                                               text: opt.modelData.description || ""; color: cAsh; font.family: "monospace"; font.pixelSize: root.fs(10) }
                                                    }
                                                    MouseArea { id: optMouse; anchors.fill: parent; hoverEnabled: true; cursorShape: Qt.PointingHandCursor
                                                                onClicked: root.pickOption(crow.pid, qq.modelData.question, opt.modelData.label, !!qq.modelData.multiSelect) }
                                                }
                                            }
                                            TextField {
                                                id: own
                                                visible: crow.open
                                                width: qq.width
                                                placeholderText: "or in your own words"
                                                placeholderTextColor: cAsh; color: cBone; font.family: "monospace"; font.pixelSize: root.fs(11)
                                                background: Rectangle { color: cVoid; border.color: own.activeFocus ? cSky : cLine; radius: 6 }
                                                text: (root.qTyped[crow.pid] || {})[qq.modelData.question] || ""
                                                onTextEdited: {
                                                    if (text.trim() !== "") {
                                                        var all = Object.assign({}, root.qPicked), mine = Object.assign({}, all[crow.pid] || {})
                                                        delete mine[qq.modelData.question]; all[crow.pid] = mine; root.qPicked = all
                                                    }
                                                    root.typeAnswer(crow.pid, qq.modelData.question, text)
                                                }
                                            }
                                        }
                                    }
                                    RowLayout {
                                        visible: crow.open
                                        spacing: root.sz(16)
                                        Lnk { readonly property bool ready: root.questionAnswers(crow.pid, qcard.questions) !== null
                                              text: "ANSWER"; base: ready ? cSky : cAsh; font.pixelSize: root.fs(12)
                                              onClicked: if (ready) root.answerQuestion(crow.pid, qcard.questions) }
                                        Lnk { text: "DECLINE"; base: cRust; font.pixelSize: root.fs(12); onClicked: Qt.callLater(root.answerPrompt, crow.pid, false) }
                                    }
                                    Text { visible: !crow.open; width: parent.width; wrapMode: Text.Wrap; text: crow.answer; color: cAsh; font.family: "monospace"; font.pixelSize: root.fs(10) }
                                }
                            }

                            // A voice note on its way to being a turn: transcribing on
                            // the agent's machine, or failed there and kept to try again.
                            Column {
                                visible: crow.kind === "voicenote"
                                width: parent.width
                                spacing: 4
                                RowLayout {
                                    visible: !crow.error
                                    spacing: 8
                                    Pulse { tint: cSky }
                                    Text { text: "🎤 voice note — transcribing on the agent's machine…"; color: cAsh; font.family: "monospace"; font.pixelSize: root.fs(10) }
                                }
                                Text {
                                    visible: crow.error
                                    width: parent.width; wrapMode: Text.Wrap
                                    text: "🎤 the voice note could not be transcribed: " + crow.text
                                    color: cRust; font.family: "monospace"; font.pixelSize: root.fs(11)
                                }
                                Lnk { visible: crow.error; text: "transcribe again"; base: cSky; font.pixelSize: root.fs(10)
                                      onClicked: Qt.callLater(root.retryVoice, crow.pid) }
                            }

                            Text {
                                visible: crow.kind === "note"
                                text: "— " + crow.text + (crow.time ? "  ·  " + root.clock(crow.time) : "")
                                color: cAsh; font.family: "monospace"; font.pixelSize: root.fs(10)
                            }
                        }
                    }
                }

                FileDialog {
                    id: attachDialog
                    title: "Send a file to the agent"
                    onAccepted: root.attachFile(selectedFile)
                }

                // What goes with the next message, and what is still on its way.
                Flow {
                    visible: !root.agentCreating && root.agentOpen !== null && (root.agentAttached.length > 0 || root.agentSending !== "" || root.agentTranscribing)
                    Layout.fillWidth: true
                    spacing: root.sz(8)
                    Repeater {
                        model: root.agentAttached
                        delegate: Rectangle {
                            id: chip
                            required property string modelData
                            height: chipText.implicitHeight + root.sz(10)
                            width: chipText.implicitWidth + root.sz(20)
                            radius: height / 2; color: "transparent"; border.color: cSky
                            Text {
                                id: chipText
                                anchors.centerIn: parent
                                text: "📎 " + root.attachedName(chip.modelData) + "  ×"
                                color: cSky; font.family: "monospace"; font.pixelSize: root.fs(10)
                            }
                            MouseArea { anchors.fill: parent; cursorShape: Qt.PointingHandCursor
                                        onClicked: root.agentAttached = root.agentAttached.filter(function(x) { return x !== chip.modelData }) }
                        }
                    }
                    RowLayout {
                        visible: root.agentSending !== ""
                        Pulse { tint: cSky }
                        Text { text: "sending " + root.agentSending + "…"; color: cAsh; font.family: "monospace"; font.pixelSize: root.fs(10) }
                    }
                    RowLayout {
                        visible: root.agentTranscribing
                        Pulse { tint: cSky }
                        Text { text: "transcribing on " + (root.agentOpen ? root.agentOpen.name : "") + "…"; color: cAsh; font.family: "monospace"; font.pixelSize: root.fs(10) }
                    }
                }

                // Reading this session aloud: the controls stay here, in reach
                // however far the reply being read is scrolled (the phone's
                // ReadingBar); "show" brings it back.
                Rectangle {
                    readonly property bool on: root.aloud !== null && root.agentOpen !== null
                                                && root.speakingKey.indexOf(root.agentOpen.address + "/" + root.agentOpen.session + "/") === 0
                    visible: on
                    Layout.fillWidth: true
                    implicitHeight: barRow.implicitHeight + root.sz(8)
                    color: cPanel; radius: root.sz(8); border.color: Qt.rgba(0.35, 0.66, 1.0, 0.4)
                    RowLayout {
                        id: barRow
                        anchors.left: parent.left; anchors.right: parent.right; anchors.verticalCenter: parent.verticalCenter
                        anchors.leftMargin: root.sz(8); anchors.rightMargin: root.sz(8)
                        Pulse { tint: root.aloud && root.aloud.paused ? cAsh : cSky }
                        Text { text: root.aloud ? (root.aloud.index + 1) + " / " + root.aloud.sentences.length : ""
                               color: cAsh; font.family: "monospace"; font.pixelSize: root.fs(10) }
                        Lnk { text: "show"; base: cAsh; font.pixelSize: root.fs(10); onClicked: Qt.callLater(root.showReading) }
                        Item { Layout.fillWidth: true }
                        Lnk { text: "⏮"; base: cBone; font.pixelSize: root.fs(11); onClicked: Qt.callLater(root.skipReading, -1) }
                        Lnk { text: root.aloud && root.aloud.paused ? "▶ resume" : "⏸ pause"; base: cSky; font.pixelSize: root.fs(11)
                              onClicked: Qt.callLater(root.aloud && root.aloud.paused ? root.resumeReading : root.pauseReading) }
                        Lnk { text: "⏭"; base: cBone; font.pixelSize: root.fs(11); onClicked: Qt.callLater(root.skipReading, 1) }
                        Lnk { text: "■"; base: cRust; font.pixelSize: root.fs(11); onClicked: Qt.callLater(root.stopReading) }
                    }
                }

                // Composer: Enter sends, Shift+Enter is a new line.
                RowLayout {
                    visible: !root.agentCreating && root.agentOpen !== null
                    Layout.fillWidth: true
                    spacing: root.sz(10)
                    Lnk { text: "📎"; font.pixelSize: root.fs(16); onClicked: attachDialog.open() }
                    Item {
                        width: root.sz(26); height: root.sz(26)
                        Pulse { anchors.centerIn: parent; visible: root.agentRecording; tint: cRust; width: root.sz(18) }
                        Text {
                            anchors.centerIn: parent
                            text: root.agentRecording ? "■" : "🎤"
                            color: root.agentRecording ? cBone : (root.agentTranscribing ? cAsh : cPhosphor)
                            font.pixelSize: root.fs(root.agentRecording ? 11 : 16)
                        }
                        MouseArea { anchors.fill: parent; cursorShape: Qt.PointingHandCursor; onClicked: root.toggleRecording() }
                    }
                    ScrollView {
                        Layout.fillWidth: true
                        Layout.preferredHeight: Math.min(root.sz(140), Math.max(root.sz(40), composer.implicitHeight))
                        TextArea {
                            id: composer
                            placeholderText: "message — Enter sends, Shift+Enter for a new line"
                            placeholderTextColor: cAsh
                            wrapMode: TextArea.Wrap
                            color: cBone
                            font.family: "monospace"; font.pixelSize: root.fs(12)
                            background: Rectangle { color: cPanel; radius: root.sz(10); border.color: composer.activeFocus ? cPhosphor : cLine }
                            // An image on the clipboard goes to the agent like a
                            // file; anything else pastes as usual.
                            Keys.onPressed: function(ev) {
                                if (!ev.matches(StandardKey.Paste) || !root.agentOpen) return
                                var r = root.unwrap(root.callCore("agentPaste", [root.agentOpen.address, root.agentOpen.session]))
                                if (r && r.file) { ev.accepted = true; root.agentAttached = root.agentAttached.concat([r.file]) }
                                else if (r && r.error) { root.said = r.detail || r.error; root.saidBad = true }
                            }
                            Keys.onReturnPressed: function(ev) {
                                if (ev.modifiers & Qt.ShiftModifier) { ev.accepted = false; return }
                                if (root.sendToAgent(composer.text)) composer.text = ""
                            }
                        }
                    }
                    Lnk {
                        text: "SEND"; font.pixelSize: root.fs(12)
                        base: (composer.text.trim() !== "" || root.agentAttached.length > 0) ? cPhosphor : cAsh
                        onClicked: if (root.sendToAgent(composer.text)) composer.text = ""
                    }
                }
            }
        }
    }
}
