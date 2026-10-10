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
    // Code in a reply copies itself when clicked: a link to "copy:" and the
    // text, which activateLink hands to the clipboard instead of opening.
    function copyHref(text) { return "copy:" + encodeURIComponent(String(text)) }
    function activateLink(link) {
        link = String(link || "")
        if (link.indexOf("copy:") === 0) {
            var t = decodeURIComponent(link.substring(5))
            copyText(t)
            root.said = "copied " + (t.indexOf("\n") >= 0 ? t.split("\n").length + " lines" : (t.length > 60 ? t.substring(0, 57) + "…" : t))
            return true
        }
        openUrl(link)
        return false
    }
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
    // Markdown as the phone draws it (Markdown.kt, MarkdownText): Qt's own
    // MarkdownText has no styles, so inline code — paths, mostly — was the
    // colour of the text around it. Ported, not reinvented: the same blocks
    // and inline rules, drawn as rich text in the phone's colours (2026-10-08).
    function mdEsc(s) { return String(s).replace(/&/g, "&amp;").replace(/</g, "&lt;").replace(/>/g, "&gt;").replace(/"/g, "&quot;") }
    function mdLetter(ch) { return ch !== null && /[0-9A-Za-zÀ-ɏ]/.test(ch) }
    function mdItalicMark(s, i, open) {
        var before = i > 0 ? s.charAt(i - 1) : null, after = i + 1 < s.length ? s.charAt(i + 1) : null
        if (!open) return after !== null && !/\s/.test(after) && (before === null || !mdLetter(before))
        return before !== null && !/\s/.test(before) && (after === null || !mdLetter(after))
    }
    function mdLinks(s, bold, italic) {
        var out = [], at = 0, m
        // A literal, not the bareUrl property: QML turns a regex property
        // into a Qt regular expression, with no .source to build one from.
        var re = /https?:\/\/[^\s<>()\[\]`"']+[^\s<>()\[\]`"'.,;:!?*_~]/g
        while ((m = re.exec(s)) !== null) {
            if (m.index > at) out.push({ text: s.substring(at, m.index), bold: bold, italic: italic })
            out.push({ text: m[0], link: m[0], bold: bold, italic: italic })
            at = m.index + m[0].length
        }
        if (at < s.length) out.push({ text: s.substring(at), bold: bold, italic: italic })
        return out
    }
    function mdInline(s) {
        var styled = [], buf = "", bold = false, italic = false, i = 0
        function emit() { if (buf !== "") { styled.push({ text: buf, bold: bold, italic: italic }); buf = "" } }
        while (i < s.length) {
            var c = s.charAt(i)
            if (c === "\\" && i + 1 < s.length) { buf += s.charAt(i + 1); i += 2 }
            else if (c === "`") {
                var end = s.indexOf("`", i + 1)
                if (end < 0) { buf += c; i++ } else { emit(); styled.push({ text: s.substring(i + 1, end), code: true }); i = end + 1 }
            } else if ((c === "*" || c === "_") && i + 1 < s.length && s.charAt(i + 1) === c) { emit(); bold = !bold; i += 2 }
            else if ((c === "*" || c === "_") && mdItalicMark(s, i, italic)) { emit(); italic = !italic; i++ }
            else if (c === "[") {
                var close = s.indexOf("](", i), e = close > 0 ? s.indexOf(")", close) : -1
                if (close < 0 || e < 0) { buf += c; i++ }
                else { emit(); styled.push({ text: s.substring(i + 1, close), bold: bold, italic: italic, link: s.substring(close + 2, e) }); i = e + 1 }
            } else { buf += c; i++ }
        }
        emit()
        var out = []
        for (var k = 0; k < styled.length; k++) {
            var sp = styled[k]
            if (sp.code || sp.link) out.push(sp)
            else out = out.concat(mdLinks(sp.text, sp.bold, sp.italic))
        }
        return out
    }
    function mdSpans(spans, base) {
        var h = ""
        for (var i = 0; i < spans.length; i++) {
            var sp = spans[i], t = mdEsc(sp.text)
            // Code is a link that copies it (activateLink), as Telegram does.
            if (sp.code) { h += "<a href=\"" + copyHref(sp.text) + "\" style=\"text-decoration:none;\"><span style=\"color:" + cChartreuse + "; background-color:" + cVoid + ";\">" + t + "</span></a>"; continue }
            if (sp.italic) t = "<i>" + t + "</i>"
            if (sp.bold) t = "<b style=\"color:#ffffff;\">" + t + "</b>"
            if (sp.link) t = "<a href=\"" + mdEsc(sp.link) + "\" style=\"color:" + cSky + ";\">" + t + "</a>"
            else if (base) t = "<span style=\"color:" + base + ";\">" + t + "</span>"
            h += t
        }
        return h
    }
    function mdCells(row) { return row.trim().replace(/^\|/, "").replace(/\|$/, "").split("|").map(function(c) { return mdInline(c.trim()) }) }
    function mdHtml(src) {
        var lines = String(src || "").replace(/\r\n/g, "\n").split("\n"), out = [], para = []
        var fence = /^\s*(```|~~~)\s*([\w+-]*)\s*$/, heading = /^(#{1,6})\s+(.*?)\s*#*\s*$/
        var bullet = /^(\s*)[-*+]\s+(.*)$/, numbered = /^(\s*)(\d{1,3})[.)]\s+(.*)$/
        var rule = /^\s*([-*_])(\s*\1){2,}\s*$/, tableSep = /^\s*\|?\s*:?-{2,}:?\s*(\|\s*:?-{2,}:?\s*)*\|?\s*$/
        var P = "<p style=\"margin-top:0px; margin-bottom:6px;\">"
        var lastItem = -1
        function flush() { if (para.length) { out.push(P + mdSpans(mdInline(para.join(" ").trim())) + "</p>"); para = []; lastItem = -1 } }
        function item(indent, marker, text) {
            out.push("<p style=\"margin-top:0px; margin-bottom:2px; margin-left:" + (indent * 16) + "px;\"><span style=\"color:" + cPhosphor + ";\">" +
                     mdEsc(marker) + "</span>&nbsp;" + mdSpans(mdInline(text)) + "</p>")
            lastItem = out.length - 1
        }
        var i = 0
        while (i < lines.length) {
            var line = lines[i], f = line.match(fence)
            if (f) {
                flush()
                var close = f[1], body = []
                i++
                while (i < lines.length && lines[i].trim() !== close) { body.push(lines[i]); i++ }
                while (body.length && body[body.length - 1].trim() === "") body.pop() // a reply still streaming
                out.push("<table width=\"100%\" cellspacing=\"0\" cellpadding=\"8\" style=\"background-color:" + cVoid + "; border:1px solid " + cLine +
                         "; margin-bottom:6px;\"><tr><td><pre style=\"color:" + cChartreuse + "; margin:0px;\"><a href=\"" + copyHref(body.join("\n")) +
                         "\" style=\"text-decoration:none;\"><span style=\"color:" + cChartreuse + ";\">" + mdEsc(body.join("\n")) + "</span></a></pre></td></tr></table>")
                i++
                continue
            }
            if (line.trim() === "") { flush(); i++; continue }
            if (line.indexOf("|") >= 0 && i + 1 < lines.length && tableSep.test(lines[i + 1])) {
                flush()
                var rows = [mdCells(line)]
                i += 2
                while (i < lines.length && lines[i].indexOf("|") >= 0 && lines[i].trim() !== "") { rows.push(mdCells(lines[i])); i++ }
                var t = "<table cellspacing=\"0\" cellpadding=\"4\" border=\"1\" style=\"border-color:" + cLine + "; border-style:solid; margin-bottom:6px;\">"
                for (var r = 0; r < rows.length; r++) {
                    t += "<tr>"
                    for (var c = 0; c < rows[r].length; c++) t += "<td>" + mdSpans(rows[r][c], r === 0 ? cPhosphor : "") + "</td>"
                    t += "</tr>"
                }
                out.push(t + "</table>")
                continue
            }
            var h = line.match(heading), b = line.match(bullet), n = line.match(numbered)
            if (h) { flush(); out.push("<p style=\"margin-top:6px; margin-bottom:4px; font-weight:bold;\">" + mdSpans(mdInline(h[2]), cPhosphor) + "</p>") }
            else if (rule.test(line)) { flush(); out.push("<hr/>") }
            else if (b) { flush(); item(Math.floor(b[1].length / 2), "•", b[2]) }
            else if (n) { flush(); item(Math.floor(n[1].length / 2), n[2] + ".", n[3]) }
            else if (/^\s*>/.test(line)) {
                flush()
                out.push(P + "<span style=\"color:" + cViolet + ";\">▍</span>&nbsp;" + mdSpans(mdInline(line.replace(/^\s*>/, "").trim()), cAsh) + "</p>")
            } else if (/^  /.test(line) && lastItem === out.length - 1 && lastItem >= 0 && para.length === 0) {
                // A continuation of the list item above it.
                out[lastItem] = out[lastItem].replace(/<\/p>$/, " " + mdSpans(mdInline(line.trim())) + "</p>")
            } else para.push(line.trim())
            i++
        }
        flush()
        return out.join("")
    }
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
        root.boardMode = String(callCore("getPref", ["agent_layout"]) || "") === "board"
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
                         sessions: (h.list && h.list.sessions) ? h.list.sessions : [],
                         tasks: (h.tasks && h.tasks.tasks) ? h.tasks.tasks : [] })
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
            // Not the tasks: a week of them, and the next round brings them.
            savePref("agent_hosts", JSON.stringify(agentHosts.map(function(h) { return Object.assign({}, h, { tasks: [] }) })))
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
            if (r.connected) {
                root.agentCaughtUp = true
                // A jump that is still armed was waiting for exactly this: rebuildChat only
                // fires on new events, and an idle session sends none, so the search fallback
                // never ran (2026-10-10).
                if (root.jumpRef !== "") Qt.callLater(root.rebuildChat)
            }
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
            else if (e.kind === "task") add(e, { kind: "note", text: taskNote(d.id || "", d.state || "", d.summary || "") })
            else if (e.kind === "renamed") add(e, { kind: "note", text: "renamed (was " + (d.from || "") + ")" + (e.by ? " from " + e.by : "") })
            else if (e.kind === "caged") add(e, { kind: "note", text: cagedNote(d, e.by) })
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
                // Read BEFORE clearing: asking isQuietJump after `jumpTo = 0` is asking
                // about 0, and the quiet branch could never be taken at all.
                var wasQuiet = isQuietJump(root.jumpTo)
                root.jumpTo = 0
                if (wasQuiet) {
                    root.jumpQuietFor = 0
                    Qt.callLater(function() { chatList.positionViewAtIndex(at, ListView.End) })
                    return
                }
                root.agentLit = chatModel.get(i).seq
                Qt.callLater(function() { chatList.positionViewAtIndex(at, ListView.Center) })
                litTimer.restart()
                return
            }
        }
        if (root.jumpToId !== "") {
            for (i = 0; i < chatModel.count; i++) {
                if (!isJumpRow(chatModel.get(i), root.jumpToId)) continue
                var jat = i
                root.chatStick = false
                root.jumpToId = ""
                root.jumpRef = ""
                root.agentLit = chatModel.get(i).seq
                Qt.callLater(function() { chatList.positionViewAtIndex(jat, ListView.Center) })
                litTimer.restart()
                return
            }
            // Not in the tail, and the session is not still arriving: reach back with
            // the search the view already has, rather than a second jump path.
            if (root.jumpRef !== "" && root.agentCaughtUp) jumpToTaskMessage()
        }
        if (root.chatStick) Qt.callLater(function() { chatList.positionViewAtEnd() })
    }

    // Search: the whole conversation, on the agent's machine; the core does it
    // in the background and pumpSearch reads the answer.
    property bool searchOpen: false
    property bool searchBusy: false
    property var searchFound: null
    property real jumpTo: 0
    // A jump to a TASK's message, which is a different thing from jumpTo: the task
    // ref's second half is the message id, and the event that carried it has that id,
    // so the match is on the id and not on a sequence.
    property string jumpToId: ""
    property string jumpRef: ""
    property bool jumpViaSearch: false
    // The message id out of a task ref ("machine/session:messageId"). A ref with no
    // colon is not one, and the jump is then simply not armed.
    function taskMessageId(ref) {
        var s = String(ref || "")
        var i = s.lastIndexOf(":")
        return i > 0 ? s.slice(i + 1) : ""
    }
    // The query that reaches a message outside the loaded tail. The agent stores the
    // task id in the message it sends, so this is the string it can be found by.
    function taskSearchQuery(ref) { return ref ? "[shrooms task " + ref : "" }
    // Is this model row the task's message? Pure, so the harness pins it: the async
    // load around the jump is not something the harness can wait for.
    function isJumpRow(r, id) { return !!r && !r.earlier && id !== "" && r.pid === id }
    // The search fallback fires only once the session is caught up, so it cannot race
    // a message that is still arriving - the view already tracks that as agentCaughtUp.
    // The seq a QUIET jump was armed for: "load them" keeps the reader where they
    // were, not lit. A plain flag was not enough - "load them" set it, its own jump
    // never matched, and the flag stayed set, so the NEXT jump (tapping a task) took
    // the quiet branch and lit nothing and moved nothing. The reviewer's live pass:
    // "nothing is lit, nothing moves" (2026-10-10). Keyed to its seq, a stale one
    // cannot swallow a different jump. Pure, so the harness pins it.
    property real jumpQuietFor: 0
    function isQuietJump(seq) { return root.jumpQuietFor !== 0 && root.jumpQuietFor === seq }
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
        if (root.jumpViaSearch) {
            root.jumpViaSearch = false
            var hit = bestHit(root.searchFound, root.jumpRef)
            if (hit) root.openFound(hit)
            else { root.said = "no message found for " + root.jumpRef; root.saidBad = true }
        }
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
        root.jumpQuietFor = first
        root.jumpTo = first
    }
    function tailReaching(current, lastSeq, seq) {
        return current === 0 ? 0 : Math.max(current, lastSeq - seq + 1 + 20)
    }
    // The task's message was not in the loaded tail, so ask the agent for it. The
    // first hit is opened with the search's own path (openFound), which widens the
    // tail to reach it - one jump path, not two.
    function jumpToTaskMessage() {
        if (root.jumpRef === "") return
        root.jumpToId = ""
        root.jumpViaSearch = true
        root.searchOpen = true
        runSearch(taskSearchQuery(root.jumpRef))
    }
    // The hit that IS the task arriving, not a later mention of it: the agent's own
    // message starts "[shrooms task <id> from". The live pass landed on the newest
    // follow-up instead (2026-10-10). Falls back to the first hit, as before. Pure.
    function bestHit(hits, ref) {
        var want = "[shrooms task " + String(ref || "") + " from"
        for (var i = 0; i < (hits || []).length; i++) {
            if (String((hits[i] && hits[i].snippet) || "").indexOf(want) === 0) return hits[i]
        }
        return (hits && hits.length > 0) ? hits[0] : null
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
        root.cageStatus = null
        root.nsCage = false
        root.cageOpts = { image: "", nix: false, github: false }
        if (!h) return
        var r = unwrap(callCore("agentGet", [h.address, "/v1/harnesses"]))
        if (r && r.harnesses && r.harnesses.length > 0) root.harnesses = r.harnesses
        if (r && r.cage && r.cage.available) root.cageStatus = r.cage
    }
    // Cages (docs/agents-in-cages.md): a session in a rootless podman
    // container of its own, offered where the machine has podman.
    property var cageStatus: null
    property bool nsCage: false
    function cageNote(st) {
        if (!st) return ""
        var what = "root inside, and of this machine only the project, the files sent to it and the harness's settings; what it installs stays until the session is deleted"
        if (st.error) return what + ". The last build of the image failed: " + st.error
        if (st.building) return what + ". The image is being built (a few minutes)."
        if (!st.ready) return what + ". The image is built with the first one (a few minutes)."
        return what + "."
    }
    // A session that cannot work for want of quota (the agent's `limited`):
    // when it comes back, or why.
    function quotaLabel(lim) {
        if (!lim) return ""
        var until = lim.until ? epoch(lim.until) : 0
        return "QUOTA" + (until ? " · back " + clock(until) : "")
    }
    function cageLabel(sess) { return sess && sess.cage ? "caged" : "" }
    // What a cage is given, chosen in "+ session" and in the cage dialog:
    // its image ("" for the machine's), nix, the GitHub login.
    property var cageOpts: ({ image: "", nix: false, github: false, sealed: false })
    function cageBody(o) {
        var b = {}
        if (o && o.image) b.image = o.image
        // Sealed (ADR-045): none of the options that widen a cage.
        if (o && o.sealed) { b.sealed = true; return b }
        if (o && o.nix) b.nix = true
        if (o && o.github) b.github = true
        return b
    }
    function imageLabel(img) {
        if (img === "localhost/shrooms-workbench:latest") return "workbench"
        if (img === "localhost/shrooms-workbench:desktop") return "desktop — Xvfb, xdotool, ffmpeg"
        return String(img || "")
    }
    function shortImage(img) { return img === "localhost/shrooms-workbench:latest" ? "workbench" : img === "localhost/shrooms-workbench:desktop" ? "desktop" : String(img || "") }
    function cageWords(c) {
        return [c.sealed ? "sealed" : "", shortImage(c.image), c.nix ? "nix" : "", c.github ? "GitHub login" : ""].filter(function(x) { return x !== "" }).join(", ")
    }
    function cagedNote(d, by) {
        return (d && d.caged ? "moved into a cage (" + cageWords(d) + ")" : "taken out of its cage") + (by ? " from " + by : "")
    }
    // What "+ session" and taking a conversation over send.
    function sessionBody(fields) {
        var b = Object.assign({}, fields)
        if (nsCage && cageStatus) b.cage = cageBody(cageOpts)
        return JSON.stringify(b)
    }
    // Moving the open session into a cage, changing it, or out of it.
    function askCage() {
        var h = null
        for (var i = 0; i < agentHosts.length; i++) if (agentOpen && agentHosts[i].address === agentOpen.address) h = agentHosts[i]
        root.cageStatus = null
        if (h) {
            var r = unwrap(callCore("agentGet", [h.address, "/v1/harnesses"]))
            if (r && r.cage && r.cage.available) root.cageStatus = r.cage
        }
        var c = agentInfo && agentInfo.cage
        root.cageOpts = c ? { image: c.image, nix: !!c.nix, github: !!c.github, sealed: !!c.sealed } : { image: "", nix: false, github: false, sealed: false }
        cageDialog.open()
    }
    // Whether the open session takes tasks from caged agents.
    function setAcceptCaged(on) {
        if (!agentOpen) return false
        if (agentCall("agentPost", [agentOpen.address, "/v1/sessions/" + agentOpen.session + "/settings", JSON.stringify({ accept_caged: on })]) === null) return false
        root.said = on ? "session " + agentOpen.session + " takes tasks from caged agents" : "session " + agentOpen.session + " takes no tasks from caged agents"
        root.saidBad = false
        Qt.callLater(refreshAgents)
        return true
    }
    function cageOpenSession(caged) {
        if (!agentOpen) return false
        var body = JSON.stringify({ cage: caged ? cageBody(cageOpts) : null })
        if (agentCall("agentPost", [agentOpen.address, "/v1/sessions/" + agentOpen.session + "/cage", body]) === null) return false
        root.said = caged ? "session " + agentOpen.session + " is in a cage from its next message" : "session " + agentOpen.session + " is out of its cage"
        root.saidBad = false
        Qt.callLater(refreshAgents)
        return true
    }
    function harnessApproves(name) {
        for (var i = 0; i < harnesses.length; i++) if (harnesses[i].name === name) return !!(harnesses[i].caps && harnesses[i].caps.approve)
        return false
    }
    function harnessLabel(h) { return (!h || h === "claude") ? "" : h }
    function createSession(host, name, dir, auto) {
        var r = agentCall("agentPost", [host.address, "/v1/sessions",
            sessionBody({ name: name, dir: dir, harness: nsHarness, auto_approve: auto && harnessApproves(nsHarness) })])
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
                          sessionBody({ name: name, resume: conv.id, auto_approve: nsAuto.checked })])
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
    // A2A tasks from other agents (the phone's AgentChat.taskNote and the
    // session list's lines).
    function taskNote(id, state, summary) { return "task " + id + " " + String(state).replace(/_/g, " ") + (summary ? " — " + summary : "") }
    function tasksLabel(n) { return n > 0 ? (n === 1 ? "1 task" : n + " tasks") : "" }
    function stalledLabel(n) { return "⚠ " + (n === 1 ? "a task stalled" : n + " tasks stalled") + " — no progress after the reminders" }

    // The board: every session a card with its last lines, and a dashed link
    // from an agent to another that is working on a task it asked for. A
    // second layout beside the list (agent_layout), an experiment
    // (2026-10-08); the phone keeps its list.
    property bool boardMode: false
    function setBoard(on) {
        root.boardMode = on
        savePref("agent_layout", on ? "board" : "list")
    }
    // Esc goes back to the board from a session opened from it — but not
    // while Esc has something nearer to close: a dialog, or the search.
    function escToBoard() {
        if (!boardMode || agentOpen === null || searchOpen) return false
        if (dialogs().some(function(d) { return d.visible })) return false
        showBoard()
        return true
    }
    Shortcut {
        sequence: "Esc"
        context: Qt.WindowShortcut
        enabled: root.boardMode && root.agentOpen !== null && !root.searchOpen
        onActivated: Qt.callLater(root.escToBoard)
    }
    // The board, with no session open in front of it.
    // A tap on a task row: open the session that is WORKING ON it. The task
    // arrived in that session's conversation, which is where a person wants to
    // be - not on the machine that asked.
    // ACK a finished task: the asker has seen the result. It goes to the WORKER's
    // agent (A2A AckTask at /a2a/<session>), because the task lives on the machine
    // that ran it - the board is only the surface that shows it.
        // What an ACK reply MEANS: a JSON-RPC error comes back as a BODY, not as a failure,
        // so this used to say "acked ..." while the agent had refused (no such task, the
        // session renamed away). Pure, so the harness pins it - the call itself starts an
        // async path a test cannot wait on, which is why a direct ackTask() case hangs.
        // Returns the reason to show, or null when the ack really was accepted.
        function ackRefusal(reply) {
            if (reply === null || reply === undefined) return "no reply"
            try {
                var o = (typeof reply === "string") ? JSON.parse(reply) : reply
                if (o && o.error) return (o.error.message || "the agent refused")
            } catch (e) { return "unreadable reply" }
            return null
        }

    // The rows an "ACK all" would send: the finished-but-unacked ones, and nothing else.
    // Pure, so the harness pins it - a bulk action must not sweep up a task that is still
    // running or one that is already acked.
    // Where an ack is sent. Pure, so the harness pins it: the path is what was wrong -
    // /a2a/<session> is not forwarded by the core, so an ack from Basecamp could never
    // work at all (2026-10-10). The agent serves POST /v1/tasks/{id}/ack.
    function ackPath(id) { return "/v1/tasks/" + String(id || "") + "/ack" }
    function unackedRows(rows) {
        return (rows || []).filter(function(r) {
            return r && r.kind === "task" && r.group === "unacked"
        })
    }
    function ackAllUnacked() {
        var rows = unackedRows(root.taskPanelList)
        for (var i = 0; i < rows.length; i++) ackTask(rows[i], true)
        if (rows.length > 0) { root.said = "acked " + rows.length + " tasks"; root.saidBad = false }
        refreshAgents()
        return rows.length
    }
    function ackTask(row, quiet) {
        if (!row || row.kind !== "task") return false
        // /v1/, not the A2A route: the core forwards only /v1/ paths, so an agentPost to
        // /a2a/<session> came back an error and the panel said "could not ack" - the ack
        // could never work from Basecamp at all (2026-10-10). The agent grew
        // POST /v1/tasks/{id}/ack for this.
        var refusal = ackRefusal(agentCall("agentPost", [row.address, ackPath(row.id), ""]))
        if (refusal !== null) {
            root.said = "could not ack " + row.id + (refusal === "no reply" ? "" : ": " + refusal)
            root.saidBad = true
            return false
        }
        root.said = "acked " + (row.title || row.id)
        root.saidBad = false
        if (!quiet) refreshAgents()
        return true
    }
    function openTaskRow(row) {
        if (!row || row.kind !== "task") return
        for (var i = 0; i < agentHosts.length; i++) {
            if (agentHosts[i].name !== row.machine) continue
            // Arm the jump BEFORE the session loads: rebuildChat matches on the id as
            // soon as the events arrive, and falls back to the search if they never do.
            root.jumpRef = String(row.id || "")
            root.jumpToId = taskMessageId(row.id)
            // NO tail/fresh arguments: passing `true` for tail made the watch "-1" (fresh
            // negates it), which is ONE event - the session opened with a single line and
            // "N earlier events not loaded" (2026-10-10). The default tail is what opens a
            // session the way the list does.
            openSession(agentHosts[i], row.session)
            return
        }
    }
    function showBoard() {
        root.agentCreating = false
        noteRead()
        root.agentOpen = null
        chatModel.clear()
        setBoard(true)
    }
    function boardKey(h, name) { return h.name + "/" + name }
    // The cards: the starred first, as in the list, then each machine's.
    function boardCards(hosts) {
        var out = [], starred = []
        for (var i = 0; i < hosts.length; i++) {
            var ss = hosts[i].sessions || []
            for (var j = 0; j < ss.length; j++) {
                var c = { key: boardKey(hosts[i], ss[j].name), host: hosts[i], sess: ss[j] }
                if (ss[j].starred) starred.push(c); else out.push(c)
            }
        }
        starred.sort(function(a, b) { return a.sess.name === b.sess.name ? (a.host.name < b.host.name ? -1 : 1) : (a.sess.name < b.sess.name ? -1 : 1) })
        return starred.concat(out)
    }
    // Who asked: a task's shrooms/from is "DEVICE (MACHINE/SESSION)" — the
    // device the mesh names and the session it claims to be. The card is the
    // claimed session's on that machine; a machine named a little otherwise
    // ("laptop" for "laptop.home") still finds it; an asker that is no
    // session (the CLI, an app) has no card.
    function askerKey(from, keys) {
        // The same two shapes askerName knows about. A CAGED sender is
        // "laptop (laptop/shrooms, in a cage)": the old regex captured "shrooms, in a cage"
        // as the session, found no such card, and the link silently vanished.
        var m = /\(([^)\/]+)\/([^),]+)(,\s*[^)]*)?\)\s*$/.exec(String(from || ""))
        if (!m) return ""
        var want = m[1] + "/" + m[2]
        if (keys[want]) return want
        for (var k in keys) {
            var slash = k.indexOf("/")
            var mach = k.substring(0, slash), sess = k.substring(slash + 1)
            if (sess === m[2] && (mach.indexOf(m[1] + ".") === 0 || m[1].indexOf(mach + ".") === 0)) return k
        }
        return ""
    }
    // The links: one per open task whose asker and worker both have a card,
    // from the asker to the worker.
    function boardEdges(hosts) {
        var keys = {}, out = []
        for (var i = 0; i < hosts.length; i++) {
            var ss = hosts[i].sessions || []
            for (var j = 0; j < ss.length; j++) keys[boardKey(hosts[i], ss[j].name)] = true
        }
        for (i = 0; i < hosts.length; i++) {
            var ts = hosts[i].tasks || []
            for (j = 0; j < ts.length; j++) {
                var t = ts[j], md = t.metadata || {}, st = t.status ? t.status.state : ""
                if (/COMPLETED|FAILED|CANCELED|REJECTED/.test(st)) continue
                var to = boardKey(hosts[i], md["shrooms/session"] || "")
                var from = askerKey(md["shrooms/from"], keys)
                if (!keys[to] || from === "" || from === to) continue
                out.push({ id: t.id, from: from, to: to, state: md["shrooms/stalled"] ? "stalled" : stateWordOf(st) })
            }
        }
        return out
    }
    function stateWordOf(st) { return String(st || "").replace(/^TASK_STATE_/, "").toLowerCase().replace(/_/g, "-") }
    // The links, ONE ENTRY PER PAIR of sessions, carrying the tasks on it: how
    // many are open and the tone of the most urgent. A task that needs a person
    // is what someone must see from across the board, so it wins the link's
    // colour; a stalled one is next; otherwise the link is working.
    function boardLinkList(hosts) {
        var keys = {}, out = [], byPair = {}
        for (var i = 0; i < hosts.length; i++) {
            var ss = hosts[i].sessions || []
            for (var j = 0; j < ss.length; j++) keys[boardKey(hosts[i], ss[j].name)] = true
        }
        for (i = 0; i < hosts.length; i++) {
            var ts = hosts[i].tasks || []
            for (j = 0; j < ts.length; j++) {
                var t = ts[j], md = t.metadata || {}, w = stateWordOf(t.status ? t.status.state : "")
                if (/completed|failed|canceled|rejected|expired/.test(w)) continue
                var to = boardKey(hosts[i], md["shrooms/session"] || "")
                var from = askerKey(md["shrooms/from"], keys)
                if (!keys[to] || from === "" || from === to) continue
                // Both directions are ONE link, so their tasks share a badge.
                var pair = pairKeyOf(from, to)
                if (pair === "") continue
                if (!byPair[pair]) {
                    byPair[pair] = { from: from, to: to, count: 0, needsYou: 0, stalled: 0, working: 0,
                                     tasks: [], tone: "working" }
                    out.push(byPair[pair])
                }
                var e = byPair[pair]
                e.count++
                e.tasks.push({ id: t.id, title: taskTitleOf(t), state: taskStalledOf(t) ? "stalled" : w })
                if (taskStalledOf(t)) e.stalled++
                else if (w === "input-required") e.needsYou++
                else e.working++
                e.tone = e.needsYou > 0 ? "input-required" : e.stalled > 0 ? "stalled" : "working"
            }
        }
        return out
    }
    readonly property var boardLinkData: boardLinkList(agentHosts)
    // What a session OWES and what it is WAITING FOR: the tasks it is working on,
    // and the tasks it asked others for. A card doing four things should say so.
    function cardLoad(hosts) {
        var keys = {}, out = {}
        for (var i = 0; i < hosts.length; i++) {
            var ss = hosts[i].sessions || []
            for (var j = 0; j < ss.length; j++) keys[boardKey(hosts[i], ss[j].name)] = true
        }
        for (i = 0; i < hosts.length; i++) {
            var ts = hosts[i].tasks || []
            for (j = 0; j < ts.length; j++) {
                var t = ts[j], md = t.metadata || {}, w = stateWordOf(t.status ? t.status.state : "")
                if (/completed|failed|canceled|rejected|expired/.test(w)) continue
                var mine = boardKey(hosts[i], md["shrooms/session"] || "")
                if (keys[mine]) { out[mine] = out[mine] || { waiting: 0, asked: 0 }; out[mine].waiting++ }
                var from = askerKey(md["shrooms/from"], keys)
                if (from !== "" && keys[from]) { out[from] = out[from] || { waiting: 0, asked: 0 }; out[from].asked++ }
            }
        }
        return out
    }
    readonly property var cardLoadData: cardLoad(agentHosts)
    // A short label for a load count: "2 owed · 1 asked", and nothing when idle.
    function loadLabel(l) {
        if (!l) return ""
        var bits = []
        if (l.waiting > 0) bits.push(l.waiting + " owed")
        if (l.asked > 0) bits.push(l.asked + " asked")
        return bits.join(" \u00b7 ")
    }
    // A link's badge: the count, and the word when it is not simply working.
    function linkLabel(e) {
        if (!e || e.count < 1) return ""
        if (e.needsYou > 0) return e.count + (e.needsYou === 1 ? " \u00b7 needs you" : " \u00b7 " + e.needsYou + " need you")
        if (e.stalled > 0) return e.count + " \u00b7 stalled"
        return String(e.count)
    }

    // ---- the tasks themselves, read live from the agents -------------------
    // A link on the board IS a task, and so is a row in the tasks panel: the
    // same objects, read from the machine that ran them. Nothing is copied to a
    // hub, so there is no bridge lag and no second copy to disagree with.
    //
    // A task's NAME: the asker's title (shrooms/title), else the first line of
    // the request (the A2A history's ROLE_USER message), else the worker's
    // summary. Same precedence the hub board uses, so the two never disagree.
    function taskTitleOf(t) {
        var md = (t && t.metadata) || {}
        var named = oneLine(md["shrooms/title"] || (t && t.title))
        if (named !== "") return named
        var h = (t && t.history) || []
        for (var i = 0; i < h.length; i++) {
            var m = h[i]
            if (!m) continue
            var role = String(m.role || "").toLowerCase()
            if (role !== "" && role !== "role_user" && role !== "user") continue
            var p = (m.parts && m.parts[0] && m.parts[0].text) || m.text || m.content || ""
            var line = oneLine(firstLine(p))
            if (line !== "") return line
        }
        return oneLine(t && t.summary)
    }
    // One line, whitespace collapsed. NOT cut: fitting a narrow card is the view's job
    // (elide: Text.ElideRight), and a second number in the data would only drift from the
    // store's 120. A wide panel can then show more of the same title.
    function oneLine(s) {
        return String(s === null || s === undefined ? "" : s).replace(/\s+/g, " ").trim()
    }
    // The first line only: a request is usually "From X..." and then the ask.
    function firstLine(s) {
        if (s === null || s === undefined) return ""
        var parts = String(s).split("\n")
        for (var i = 0; i < parts.length; i++) { var l = parts[i].trim(); if (l !== "") return l }
        return ""
    }
    // What the task last said, for the line under the title.
    function taskLatestOf(t) {
        try { return oneLine(t.status.message.parts[0].text) } catch (e) { return "" }
    }
    function taskAtOf(t) { return (t && t.status && t.status.timestamp) || "" }
    function taskAckedOf(t) { return !!((t && t.metadata) || {})["shrooms/acknowledged"] }
    function taskStalledOf(t) { return !!((t && t.metadata) || {})["shrooms/stalled"] }
    // Which group a row belongs to. "Needs you" first: a task waiting on a
    // person is the only one that is urgent. Acked tasks are finished and are
    // not listed at all - the list is what is still owed.
    function taskGroup(t) {
        var w = stateWordOf(t && t.status ? t.status.state : "")
        // Needs you FIRST, even when it is also stalled: a task waiting on a person is the
        // one that is urgent, and a stalled task that is also blocked on you is still blocked
        // on you. (The reviewer: it should stay in Needs you.)
        if (w === "input-required") return "needs-you"
        if (taskStalledOf(t)) return "stalled"
        if (/completed|failed|canceled|rejected|expired/.test(w)) return taskAckedOf(t) ? "done" : "unacked"
        return "working"
    }
    readonly property var taskGroupOrder: ["needs-you", "working", "stalled", "unacked"]
    // Where a link badge is drawn vertically, clamped into the canvas. The top row's links
    // arc ABOVE the cards, so an unclamped badge lands under the header and is cut in half -
    // and a badge half off the top reads as a rendering fault, which is worse than not
    // drawing it. Pure, so the harness pins it and a mutation fails.
    function badgeY(y, height, pad) {
        var p = (pad === undefined) ? 9 : pad
        return Math.max(p, Math.min(y, height - p))
    }
    function taskGroupLabel(g) {
        return g === "needs-you" ? "Needs you" : g === "working" ? "Working"
             : g === "stalled" ? "Stalled" : "Done, unacked"
    }
    // The age, in the units a person reads.
    function ageOf(at, now) {
        var ms = Date.parse(at)
        if (isNaN(ms)) return ""
        var s = Math.max(0, Math.floor(((now === undefined ? Date.now() : now) - ms) / 1000))
        if (s < 60) return s + "s"
        if (s < 3600) return Math.floor(s / 60) + "m"
        if (s < 86400) return Math.floor(s / 3600) + "h"
        return Math.floor(s / 86400) + "d"
    }
    // The age, labelled for what it is: "quiet 2h" while a task is open (how long since it
    // last moved), "done 3h" once it is finished (how long since it finished).
    // The clock is an ARGUMENT, so a row's age is a binding on nowMs and the row itself
    // does not change with it. Pure, so the harness pins both halves: the row has no
    // clock-derived field, and the label moves when the clock does.
    function ageLabel(row, now) {
        if (!row || !row.at) return ""
        var a = ageOf(row.at, now)
        return a === "" ? "" : (row.quiet ? "quiet " : "done ") + a
    }

    // Does this card have a task waiting on a person? That is what amber is for.
    // The rows are a parameter so it can be pinned against a fixture: reading
    // root.taskPanelList directly is untestable, because that reads the live hosts.
    function cardNeedsYou(key, rows) {
        rows = rows || root.taskPanelList
        for (var i = 0; i < rows.length; i++) {
            if (rows[i].kind === "task" && rows[i].group === "needs-you"
                && (rows[i].machine + "/" + rows[i].session) === key) return true
        }
        return false
    }

    // The asker as a SESSION, not the device claim. Two shapes to know about:
    //   "laptop.default (laptop/SPEL)"                 -> SPEL
    //   "laptop (laptop/shrooms, in a cage)"           -> shrooms, and it IS caged
    //   "pi5.office"                                   -> no session at all: a phone or
    //                                                     Basecamp user. Someone asked, so
    //                                                     show the DEVICE rather than nobody.
    // The cage used to come through as part of the session ("shrooms, in a cage"), which then
    // matched no card and silently dropped the link.
    function askerName(from) {
        var s = String(from || "")
        var m = /\(([^)\/]+)\/([^),]+)(,\s*[^)]*)?\)\s*$/.exec(s)
        if (m) return m[2].trim()
        return s.trim()
    }
    function askerCaged(from) {
        return /,\s*in a cage/.test(String(from || ""))
    }
    // Every task on every machine, as the panel's rows. The machines' own
    // answers, straight through, grouped in the order a person needs them.
    // NO clock argument: a row that depends on nowMs is rebuilt on every tick, the
    // Repeater recreates every delegate, and the panel shifts under the cursor - a
    // click then acked the row that had slid into place, not the one aimed at
    // (2026-10-10). The row carries `at`; the AGE is a binding on nowMs in the view.
    function taskRows(hosts) {
        var out = []
        // Every session key, so a row can name the asker the same way a link does
        // (askerKey) - a link's pair is "asker>worker" and the panel filters on it.
        var keys = {}
        for (var k = 0; k < (hosts || []).length; k++) {
            var ks = hosts[k].sessions || []
            for (var q = 0; q < ks.length; q++) keys[boardKey(hosts[k], ks[q].name)] = true
        }
        for (var i = 0; i < (hosts || []).length; i++) {
            var h = hosts[i], ts = h.tasks || []
            for (var j = 0; j < ts.length; j++) {
                var t = ts[j], g = taskGroup(t)
                if (g === "done") continue
                var md = t.metadata || {}, who = md["shrooms/session"] || ""
                var from = md["shrooms/from"] || ""
                // A row is never a gap: a task from an older agent may have no title, no
                // request and no summary, and the id is still something a person can use.
                var title = taskTitleOf(t)
                if (title === "") title = String(t.id || "(no id)")
                out.push({ id: t.id, group: g, title: title, latest: taskLatestOf(t),
                           from: from, asker: askerName(from), caged: askerCaged(from),
                           worker: who, machine: h.name, address: h.address, session: who,
                           askerKey: askerKey(from, keys),
                           at: taskAtOf(t),
                           // `status.timestamp` is the last UPDATE, so this is how long the
                           // task has been QUIET, not how long since it was asked. Say which:
                           // a task asked three days ago that moved a minute ago is quiet 1m.
                           quiet: g !== "unacked",
                           acked: taskAckedOf(t), stalled: taskStalledOf(t) })
            }
        }
        out.sort(function(a, b) {
            var d = root.taskGroupOrder.indexOf(a.group) - root.taskGroupOrder.indexOf(b.group)
            return d !== 0 ? d : (a.at < b.at ? -1 : a.at > b.at ? 1 : 0)
        })
        return out
    }
    readonly property var taskRowList: taskRows(agentHosts)
    // The panel's rows INCLUDING a header before each group that has something in
    // it. One flat list, because a QML Repeater cannot insert a header when a
    // value changes - and a group with nothing in it gets no header, so the
    // panel never shows an empty heading.
    // Which link's badge is at this point, as its pair, or "" for none. Pure, so the
    // harness pins it: a harness cannot click through layers, and that is exactly the
    // behaviour that broke - a press off every badge must be handed to the card below.
    function badgeAt(hits, x, y) {
        for (var i = 0; i < (hits || []).length; i++) {
            var b = hits[i]
            if (x >= b.x && x <= b.x + b.w && y >= b.y && y <= b.y + b.h) return b.pair
        }
        return ""
    }
    // A pair written the way a row writes it - the session names, not the raw keys:
    // "laptop/a>pi5/b" reads "a -> b". Pure.
    function pairLabel(pair) {
        var s = String(pair || "")
        var i = s.indexOf(">")
        if (i < 0) return s
        var a = s.slice(0, i), b = s.slice(i + 1)
        var j = a.lastIndexOf("/"), k = b.lastIndexOf("/")
        // Both ways round, because the filter shows the link in both directions.
        return (j >= 0 ? a.slice(j + 1) : a) + " \u2194 " + (k >= 0 ? b.slice(k + 1) : b)
    }
    // A link is between TWO cards, and a task may run either way along it. One arc, one
    // badge, one filter: the key SORTS the two, so a->b and b->a are the same link. Two
    // directions used to make two badges at the same midpoint, and a tap returned the
    // first - the hidden one - so the live pass filtered to the opposite direction and
    // the panel came out empty (2026-10-10). Pure, so the harness pins it.
    function pairKeyOf(a, b) {
        a = String(a || "")
        b = String(b || "")
        if (a === "" || b === "") return ""
        return a < b ? a + ">" + b : b + ">" + a
    }
    // The link a row belongs to, written the way a link is: "asker>worker". Pure.
    // A group HEADER is not a row: it has no machine/session, and "undefined/undefined"
    // would be a pair no task has. A plain task row is accepted with or without the
    // `kind` that taskPanelRows adds - requiring it made this answer "" for a row from
    // taskRows, which is the same empty panel by another route (2026-10-10).
    function panelPair(row) {
        if (!row || row.kind === "header") return ""
        if (!row.machine || !row.session) return ""
        return pairKeyOf(row.askerKey, row.machine + "/" + row.session)
    }
    // Tapping a link filters the panel to that pair rather than opening a second list.
    // An empty pair is every task, which is what "x all tasks" clears back to.
    function linkFiltered(rows, pair) {
        if (!pair) return rows
        return rows.filter(function(r) { return panelPair(r) === pair })
    }
    function taskPanelRows(hosts, pair) {
        var rows = linkFiltered(taskRows(hosts), pair), out = [], last = null
        for (var i = 0; i < rows.length; i++) {
            if (rows[i].group !== last) {
                last = rows[i].group
                out.push({ kind: "header", group: last, label: taskGroupLabel(last),
                           count: rows.filter(function(r) { return r.group === last }).length })
            }
            out.push(Object.assign({ kind: "task" }, rows[i]))
        }
        return out
    }
    // The pair a link tap filtered to, or  for all of them.
    property string linkFilter: ""
    // Where the link badges were drawn, so a tap on one can filter the panel to its
    // pair. Written by the canvas, read by the tap handler.
    property var badgeHit: []
    readonly property var taskPanelList: taskPanelRows(agentHosts, linkFilter)

    function edgeTint(state) {
        return state === "working" ? cPhosphor : state === "input-required" ? cAmber : state === "stalled" ? cRust : cAsh
    }
    // A link's curve between two cards (x, y, w, h), as a cubic Bézier's four
    // points: cards in a row, from top to top, arcing over the row by lift
    // (and further for cards further apart, so it clears those between);
    // otherwise from the facing edges, bottom to top. spread parts several
    // links between the same two cards.
    function linkCurve(a, b, spread, lift) {
        var ax = a.x + a.w / 2, bx = b.x + b.w / 2
        if (Math.abs(a.y - b.y) < a.h / 2) {
            var y0 = a.y, y1 = b.y
            // Not off the top of the board: a curve's peak is 3/4 of its lift.
            var up = Math.min(lift + Math.abs(bx - ax) * 0.12 + spread, 2.5 * lift, (Math.min(y0, y1) - 2) / 0.75)
            var sx = ax + (bx > ax ? 1 : -1) * a.w * 0.2, ex = bx - (bx > ax ? 1 : -1) * b.w * 0.2
            return [{ x: sx, y: y0 }, { x: sx, y: y0 - up }, { x: ex, y: y1 - up }, { x: ex, y: y1 }]
        }
        var down = b.y > a.y
        // Leaving and arriving off-centre, towards each other: a link that
        // goes on sideways does not start where one from above arrives.
        var side = Math.abs(bx - ax) < a.w / 4 ? 0 : (bx > ax ? 1 : -1)
        var s = { x: ax + side * a.w * 0.2 + spread, y: down ? a.y + a.h : a.y }, e = { x: bx - side * b.w * 0.2 + spread, y: down ? b.y : b.y + b.h }
        var dy = (e.y - s.y) / 2
        return [s, { x: s.x, y: s.y + dy }, { x: e.x, y: e.y - dy }, e]
    }
    function bezierAt(p, t) {
        var u = 1 - t
        return { x: u*u*u*p[0].x + 3*u*u*t*p[1].x + 3*u*t*t*p[2].x + t*t*t*p[3].x,
                 y: u*u*u*p[0].y + 3*u*u*t*p[1].y + 3*u*t*t*p[2].y + t*t*t*p[3].y }
    }
    // Dashes drawn by hand along the curve (no reliance on setLineDash), an
    // arrowhead at the worker's end and a dot at the asker's.
    function drawDashed(ctx, p, color, offset, width) {
        var dash = 6, gap = 5, period = dash + gap
        ctx.strokeStyle = color; ctx.fillStyle = color; ctx.lineWidth = width; ctx.lineCap = "round"
        // Steps of about 2 px, so every dash is as long as the next.
        var hull = 0
        for (var k = 1; k < 4; k++) hull += Math.sqrt((p[k].x - p[k-1].x) * (p[k].x - p[k-1].x) + (p[k].y - p[k-1].y) * (p[k].y - p[k-1].y))
        var steps = Math.max(40, Math.ceil(hull / 2)), prev = p[0], along = (period - offset % period) % period
        ctx.beginPath()
        for (var i = 1; i <= steps; i++) {
            var q = bezierAt(p, i / steps)
            var len = Math.sqrt((q.x - prev.x) * (q.x - prev.x) + (q.y - prev.y) * (q.y - prev.y))
            var mid = along + len / 2
            if (mid % period < dash) { ctx.moveTo(prev.x, prev.y); ctx.lineTo(q.x, q.y) }
            along += len
            prev = q
        }
        ctx.stroke()
        var tip = p[3], back = bezierAt(p, 0.95)
        var ang = Math.atan2(tip.y - back.y, tip.x - back.x), h = width * 5
        ctx.beginPath()
        ctx.moveTo(tip.x, tip.y)
        ctx.lineTo(tip.x - h * Math.cos(ang - 0.45), tip.y - h * Math.sin(ang - 0.45))
        ctx.lineTo(tip.x - h * Math.cos(ang + 0.45), tip.y - h * Math.sin(ang + 0.45))
        ctx.closePath(); ctx.fill()
        ctx.beginPath(); ctx.arc(p[0].x, p[0].y, width * 1.6, 0, 2 * Math.PI); ctx.fill()
    }
    readonly property var boardCardList: boardCards(agentHosts)
    readonly property var boardEdgeList: boardEdges(agentHosts)
    function askDelete() { deleteDialog.open() }
    function dialogs() { return [usageDialog, voiceDialog, deleteDialog, renameDialog, restartDialog, readingDialog, cageDialog] }
    function closeDialogs() { dialogs().forEach(function(d) { d.close() }) }
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
    // Pay-as-you-go keys (Venice), once each: what is left and when it refills.
    property var usageCredits: []
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
    // The phone's UsageView.parseCredits / keys / creditLine.
    function usageCreditsOf(machine, cs) {
        if (!Array.isArray(cs)) return []
        return cs.map(function(c) {
            return { machines: [machine], provider: c.provider || "", key: c.key || "", balances: c.balances || {},
                     resetsAt: Date.parse(c.resets_at) || 0, at: Date.parse(c.at) || 0, error: c.error || "",
                     runsOutAt: Date.parse(c.runs_out_at) || 0, leftAtRefill: c.left_at_refill === undefined ? -1 : Number(c.left_at_refill) }
        })
    }
    function creditKeys(all) {
        var groups = {}, keys = []
        for (var i = 0; i < all.length; i++) {
            var k = all[i].provider + "/" + all[i].key
            if (!(k in groups)) { groups[k] = []; keys.push(k) }
            groups[k].push(all[i])
        }
        keys.sort()
        return keys.map(function(k) {
            var cs = groups[k], newest = cs[0], ms = []
            for (var j = 0; j < cs.length; j++) {
                if (cs[j].at > newest.at) newest = cs[j]
                for (var m = 0; m < cs[j].machines.length; m++) if (ms.indexOf(cs[j].machines[m]) < 0) ms.push(cs[j].machines[m])
            }
            ms.sort()
            var out = {}
            for (var f in newest) out[f] = newest[f]
            out.machines = ms
            return out
        })
    }
    // Where a limit is heading at its pace — the agent's forecast; the phone's
    // UsageView.windowForecast / creditForecast.
    function windowForecast(w, now) {
        if (w.renewed || !(w.projected > 0)) return ""
        if (w.runsOutAt > 0) return "at this pace: runs out " + planResets(w.runsOutAt, now) + " — before it resets"
        return "at this pace: about " + Math.floor(w.projected * 100) + "% at the reset — it lasts"
    }
    function creditForecast(c, now) {
        if (c.error) return ""
        if (c.runsOutAt > 0) return "at this pace: runs out " + planResets(c.runsOutAt, now) + " — before the refill"
        if (c.leftAtRefill >= 0) return "at this pace: about " + c.leftAtRefill.toFixed(1) + " DIEM left at the refill"
        return ""
    }
    function creditLine(c) {
        if (c.error) return c.error
        var parts = []
        if (c.balances.DIEM !== undefined) parts.push(Number(c.balances.DIEM).toFixed(2) + " DIEM left today")
        Object.keys(c.balances).sort().forEach(function(k) {
            if (k !== "DIEM" && Number(c.balances[k]) !== 0) parts.push(k + " " + Number(c.balances[k]).toFixed(2))
        })
        return parts.length ? parts.join(" · ") : "nothing left"
    }
    function planLimits(machine, l) {
        if (!l || typeof l !== "object") return null
        var ws = [], order = { five_hour: 0, seven_day: 1 }
        for (var k in (l.windows || {})) ws.push({ name: k, utilization: Number(l.windows[k].utilization) || 0, resetsAt: Date.parse(l.windows[k].resets_at) || 0,
                                                   projected: Number(l.windows[k].projected) || 0, runsOutAt: Date.parse(l.windows[k].runs_out_at) || 0 })
        ws.sort(function(a, b) { return (a.name in order ? order[a.name] : 2) - (b.name in order ? order[b.name] : 2) })
        if (ws.length === 0 && !l.status) return null
        return { machines: [machine], at: Date.parse(l.at) || 0, status: l.status || "", window: l.window || "", overage: !!l.overage, windows: ws }
    }
    // Once per account: machines whose windows reset at the same moments share
    // a subscription; the newest reading among them is shown.
    // By the 7-day window's reset where there is one (the phone's
    // UsageView.accounts): an old reading of the same account still has it,
    // while its 5-hour reset is long past.
    function planAccounts(all, now) {
        var groups = {}, keys = []
        for (var i = 0; i < all.length; i++) {
            var l = all[i]
            var week = l.windows.filter(function(w) { return w.name === "seven_day" })[0]
            var key = week ? "seven_day@" + Math.floor(week.resetsAt / 60000)
                : l.windows.map(function(w) { return w.name + "@" + Math.floor(w.resetsAt / 60000) }).sort().join(",")
            if (!(key in groups)) { groups[key] = []; keys.push(key) }
            groups[key].push(l)
        }
        var out = keys.map(function(k) {
            var ls = groups[k], newest = ls[0], machines = []
            for (var j = 0; j < ls.length; j++) {
                if (ls[j].at > newest.at) newest = ls[j]
                for (var m = 0; m < ls[j].machines.length; m++) if (machines.indexOf(ls[j].machines[m]) < 0) machines.push(ls[j].machines[m])
            }
            return planCurrent(Object.assign({}, newest, { machines: machines.sort() }), now)
        })
        return out.sort(function(a, b) { return b.at - a.at })
    }
    // A window whose reset has passed is renewed: its share was of a window
    // that is over, and a status about it no longer holds.
    function planCurrent(l, now) {
        var t = now === undefined ? Date.now() : now
        var ws = l.windows.map(function(w) {
            return w.resetsAt > 0 && w.resetsAt <= t ? Object.assign({}, w, { utilization: 0, renewed: true, projected: 0, runsOutAt: 0 }) : w
        })
        var over = ws.filter(function(w) { return w.name === l.window && w.renewed }).length > 0
        return Object.assign({}, l, { windows: ws, status: over ? "" : l.status })
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
        var rows = [], missing = [], waiting = [], limits = [], credits = []
        for (var i = 0; i < usageHosts.length; i++) {
            var h = usageHosts[i]
            var r = g.results.filter(function(x) { return x.address === h.address })[0]
            if (!r || !r.done) { waiting.push(h.name); continue }
            if (!r.body || !Array.isArray(r.body.rows)) { missing.push(h.name); continue }
            for (var j = 0; j < r.body.rows.length; j++) { var row = r.body.rows[j]; row.machine = h.name; rows.push(row) }
            var pl = planLimits(h.name, r.body.limits)
            if (pl) limits.push(pl)
            credits = credits.concat(usageCreditsOf(h.name, r.body.credits))
        }
        root.usageWaiting = waiting
        root.usageMissing = missing
        root.usagePlans = planAccounts(limits)
        root.usageCredits = creditKeys(credits)
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
                                    Text { text: pw.modelData.renewed ? "started again " + root.planResets(pw.modelData.resetsAt) + " · no reading since"
                                                 : Math.floor(pw.modelData.utilization * 100) + "% · resets " + root.planResets(pw.modelData.resetsAt)
                                           color: cBone; font.family: "monospace"; font.pixelSize: root.fs(11) }
                                }
                                Rectangle {
                                    width: parent.width; height: root.sz(5); radius: height / 2; color: cLine
                                    Rectangle { height: parent.height; radius: parent.radius
                                                color: root.planLevel(pw.modelData.utilization) === 2 ? cRust : root.planLevel(pw.modelData.utilization) === 1 ? cAmber : cPhosphor
                                                width: parent.width * Math.max(0, Math.min(1, pw.modelData.utilization)) }
                                }
                                Text { visible: text !== ""; text: root.windowForecast(pw.modelData)
                                       color: pw.modelData.runsOutAt > 0 ? cRust : cAsh; font.family: "monospace"; font.pixelSize: root.fs(9) }
                            }
                        }
                    }
                }
            }
            Column {
                Layout.fillWidth: true
                visible: root.usageCredits.length > 0
                spacing: root.sz(4)
                Text { text: "CREDITS"; color: cAmber; font.family: "monospace"; font.pixelSize: root.fs(10); font.letterSpacing: 1; topPadding: root.sz(6) }
                Repeater {
                    model: root.usageCredits
                    Column {
                        id: credit
                        required property var modelData
                        width: parent.width
                        spacing: 2
                        Text { text: credit.modelData.provider.charAt(0).toUpperCase() + credit.modelData.provider.slice(1) + " key " + credit.modelData.key
                                     + " · " + credit.modelData.machines.join(", ") + " · as of " + root.planAge(credit.modelData.at)
                               color: cAsh; font.family: "monospace"; font.pixelSize: root.fs(9) }
                        RowLayout {
                            width: parent.width
                            Text { text: root.creditLine(credit.modelData); color: credit.modelData.error ? cAmber : cBone
                                   font.family: "monospace"; font.pixelSize: root.fs(11); Layout.fillWidth: true }
                            Text { visible: credit.modelData.resetsAt > 0 && !credit.modelData.error
                                   text: "refills " + root.planResets(credit.modelData.resetsAt); color: cBone; font.family: "monospace"; font.pixelSize: root.fs(11) }
                        }
                        Text { visible: text !== ""; text: root.creditForecast(credit.modelData)
                               color: credit.modelData.runsOutAt > 0 ? cRust : cAsh; font.family: "monospace"; font.pixelSize: root.fs(9) }
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
    Dialog {
        id: cageDialog
        modal: true
        anchors.centerIn: parent
        width: Math.min(root.sz(560), root.width - root.sz(40))
        padding: root.sz(20)
        closePolicy: Popup.CloseOnEscape | Popup.CloseOnPressOutside
        Overlay.modal: Rectangle { color: Qt.rgba(0, 0, 0, 0.6) }
        background: Rectangle { color: cPanel; radius: root.sz(12); border.color: cPhosphor }
        header: Item {}
        footer: Item {}
        contentItem: ColumnLayout {
            spacing: root.sz(14)
            readonly property bool caged: !!(root.agentInfo && root.agentInfo.cage)
            Text {
                Layout.fillWidth: true; wrapMode: Text.Wrap
                text: root.agentOpen ? (parent.caged ? "Session \"" + root.agentOpen.session + "\" is in a cage (" + root.cageWords(root.agentInfo.cage) + ")"
                                                     : "Move session \"" + root.agentOpen.session + "\" on " + root.agentOpen.name + " into a cage?") : ""
                color: cBone; font.family: "monospace"; font.pixelSize: root.fs(14)
            }
            Text {
                Layout.fillWidth: true; wrapMode: Text.Wrap
                text: root.cageStatus === null ? "This machine's agent has no podman, or is too old to cage sessions."
                    : root.cageNote(root.cageStatus) + " Its process is stopped and the conversation carries on in the cage with the next message"
                      + (parent.caged ? "; a cage changed or left is deleted, with what was installed in it." : ".")
                color: cAsh; font.family: "monospace"; font.pixelSize: root.fs(11)
            }
            CageOptions { visible: root.cageStatus !== null; Layout.fillWidth: true }
            Text {
                visible: root.agentWorking
                Layout.fillWidth: true; wrapMode: Text.Wrap
                text: "It is working: move it once it is idle."
                color: cAmber; font.family: "monospace"; font.pixelSize: root.fs(11)
            }
            RowLayout {
                Layout.alignment: Qt.AlignRight
                spacing: root.sz(20)
                Lnk { text: "CANCEL"; base: cBone; font.pixelSize: root.fs(12); onClicked: cageDialog.close() }
                Lnk { visible: parent.parent.caged; text: "TAKE OUT"; base: cRust; font.pixelSize: root.fs(12)
                      onClicked: if (root.cageOpenSession(false)) cageDialog.close() }
                Lnk { visible: root.cageStatus !== null; text: parent.parent.caged ? "CHANGE" : "MOVE INTO THE CAGE"; base: cPhosphor; font.pixelSize: root.fs(12)
                      onClicked: if (root.cageOpenSession(true)) cageDialog.close() }
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
                    textFormat: TextEdit.RichText
                    text: !root.reading ? "" : root.reading.role !== "user" ? root.mdHtml(root.reading.text) : root.linkPlain(root.reading.text)
                    onLinkActivated: function(link) { root.activateLink(link) }
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
        // Dimmed too while it cannot work for want of quota (QuotaTag).
        opacity: !up ? 0.5 : (sess.limited ? 0.6 : 1)

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
                CageTag { sess: srow.sess }
                QuotaTag { sess: srow.sess }
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
            // Tasks from other agents: a stalled one is the owner's to look at.
            Text {
                visible: (srow.sess.tasks_stalled || 0) > 0
                width: parent.width
                text: root.stalledLabel(srow.sess.tasks_stalled || 0)
                color: cAmber; font.family: "monospace"; font.pixelSize: root.fs(9); elide: Text.ElideRight
            }
            Text {
                width: parent.width
                text: [root.clock(root.epoch(srow.sess.last_time)),
                       root.contextLabel(srow.sess.context_used, srow.sess.context_window),
                       root.harnessLabel(srow.sess.harness), srow.sess.cage ? root.cageWords(srow.sess.cage) : "", root.shortModel(srow.sess.model),
                       srow.sess.auto_approve && !(srow.sess.caps && !srow.sess.caps.approve) ? "auto-approve" : "",
                       root.tasksLabel(srow.sess.tasks_open || 0)].filter(function(x) { return x !== "" }).join("  ·  ")
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

    // What a cage is given: its image, nix, the GitHub login (root.cageOpts),
    // as the machine offers them (root.cageStatus).
    component CageOptions: ColumnLayout {
        spacing: root.sz(6)
        Flow {
            Layout.fillWidth: true
            spacing: root.sz(8)
            Repeater {
                model: root.cageStatus && root.cageStatus.images ? root.cageStatus.images : []
                delegate: Text {
                    id: ichip
                    required property var modelData
                    required property int index
                    readonly property bool on: root.cageOpts.image === ichip.modelData || (root.cageOpts.image === "" && ichip.index === 0)
                    text: root.imageLabel(ichip.modelData)
                    color: on ? cVoid : cBone; font.family: "monospace"; font.pixelSize: root.fs(10)
                    leftPadding: root.sz(10); rightPadding: root.sz(10); topPadding: root.sz(5); bottomPadding: root.sz(5)
                    Rectangle { anchors.fill: parent; z: -1; radius: root.sz(10); color: ichip.on ? cPhosphor : "transparent"; border.color: ichip.on ? cPhosphor : cLine }
                    MouseArea { anchors.fill: parent; cursorShape: Qt.PointingHandCursor
                                onClicked: root.cageOpts = Object.assign({}, root.cageOpts, { image: ichip.index === 0 ? "" : ichip.modelData }) }
                }
            }
        }
        // Sealed (ADR-045): for code nobody vouches for. Offered once the
        // machine has a token of its own for it; how to give it one, until then.
        CheckBox {
            id: sealBox
            objectName: "sealBox"
            enabled: !!(root.cageStatus && root.cageStatus.sealed)
            checked: root.cageOpts.sealed
            onToggled: root.cageOpts = Object.assign({}, root.cageOpts, { sealed: checked })
            text: root.cageStatus && root.cageStatus.sealed
                ? "sealed — for code you don't trust: the internet and nothing local (no LAN, no mesh, no agents), its own login, results in ~/shrooms-outbox"
                : "sealed — needs a token of its own on that machine: run `claude setup-token` there and save it to ~/.local/share/shrooms-agent/sealed-claude-token"
            contentItem: Text { leftPadding: sealBox.indicator.width + 6; text: sealBox.text; color: sealBox.enabled ? cAmber : cAsh; wrapMode: Text.Wrap; font.family: "monospace"; font.pixelSize: root.fs(10); verticalAlignment: Text.AlignVCenter }
            Layout.fillWidth: true
        }
        CheckBox {
            id: nixBox
            visible: !!(root.cageStatus && root.cageStatus.nix) && !root.cageOpts.sealed
            checked: root.cageOpts.nix
            onToggled: root.cageOpts = Object.assign({}, root.cageOpts, { nix: checked })
            text: "nix — the machine's store and profile (a single-user nix: written by the cage, as by you)"
            contentItem: Text { leftPadding: nixBox.indicator.width + 6; text: nixBox.text; color: cAsh; wrapMode: Text.Wrap; font.family: "monospace"; font.pixelSize: root.fs(10); verticalAlignment: Text.AlignVCenter }
            Layout.fillWidth: true
        }
        CheckBox {
            id: ghBox
            visible: !root.cageOpts.sealed
            checked: root.cageOpts.github
            onToggled: root.cageOpts = Object.assign({}, root.cageOpts, { github: checked })
            text: "GitHub login — your gh login, read-only"
            contentItem: Text { leftPadding: ghBox.indicator.width + 6; text: ghBox.text; color: cAsh; wrapMode: Text.Wrap; font.family: "monospace"; font.pixelSize: root.fs(10); verticalAlignment: Text.AlignVCenter }
            Layout.fillWidth: true
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

    // One session on the board: its name, machine and state, its last lines
    // (the agent's ?tail), and its figures. Clicked, it fills the panel.
    component BoardCard: Rectangle {
        id: bcard
        required property var card
        readonly property string cardKey: card.key
        readonly property var host: card.host
        readonly property var sess: card.sess
        readonly property bool up: root.hostReachable(bcard.host)
        readonly property int tailLines: 6
        // Dimmed too while it cannot work for want of quota (QuotaTag).
        opacity: !up ? 0.5 : (sess.limited ? 0.6 : 1)
        height: bCol.implicitHeight + root.sz(16)
        radius: root.sz(8)
        color: cPanel
        border.width: 1
        border.color: !bcard.up ? cLine : bcard.sess.state === "waiting" ? cAmber : bcard.sess.state === "working" ? Qt.rgba(0.21, 0.94, 0.63, 0.5) : cLine
        Column {
            id: bCol
            anchors.left: parent.left; anchors.right: parent.right; anchors.top: parent.top
            anchors.margins: root.sz(8)
            spacing: 3
            // What this session OWES and what it is WAITING FOR. A card doing four
            // things should say so rather than looking idle.
            Text {
                width: parent.width; elide: Text.ElideRight
                visible: text !== ""
                text: root.loadLabel(root.cardLoadData[bcard.cardKey])
                color: root.cardNeedsYou(bcard.cardKey) ? cAmber : cAsh
                font.family: "monospace"; font.pixelSize: root.fs(9)
            }
            RowLayout {
                width: parent.width
                spacing: 6
                Text { text: (bcard.sess.starred ? "🍄 " : "") + bcard.sess.name; color: cBone; font.family: "monospace"; font.pixelSize: root.fs(13); elide: Text.ElideRight; Layout.maximumWidth: bcard.width * 0.5 }
                CageTag { sess: bcard.sess }
                QuotaTag { sess: bcard.sess }
                Rectangle {
                    readonly property int n: root.unreadOf(bcard.host, bcard.sess)
                    visible: n > 0
                    implicitWidth: bUnread.implicitWidth + root.sz(10); implicitHeight: bUnread.implicitHeight + root.sz(2)
                    radius: height / 2; color: cBone
                    Text { id: bUnread; anchors.centerIn: parent; text: parent.n > 99 ? "99+" : String(parent.n)
                           color: cVoid; font.family: "monospace"; font.pixelSize: root.fs(9); font.bold: true }
                }
                Text { text: bcard.host.name; color: root.meshTint(bcard.host.mesh); font.family: "monospace"; font.pixelSize: root.fs(10); elide: Text.ElideRight; Layout.fillWidth: true }
                Pulse { visible: bcard.up && bcard.sess.state !== "idle"; tint: bcard.sess.state === "waiting" ? cAmber : cPhosphor }
                Text {
                    text: !bcard.up ? "unreachable" : bcard.sess.state === "waiting" ? "NEEDS YOU" : (bcard.sess.state === "working" ? "WORKING" : (bcard.sess.running ? "idle" : "asleep"))
                    color: !bcard.up ? cAsh : bcard.sess.state === "waiting" ? cAmber : (bcard.sess.state === "working" ? cPhosphor : cAsh)
                    font.family: "monospace"; font.pixelSize: root.fs(9); font.letterSpacing: 1
                }
            }
            // The last lines, a fixed number of rows so the cards line up;
            // an agent from before ?tail shows the preview instead.
            Column {
                width: parent.width
                height: bcard.tailLines * (root.fs(10) + root.sz(5))
                clip: true
                Repeater {
                    model: (bcard.sess.tail && bcard.sess.tail.length > 0) ? bcard.sess.tail.slice(-bcard.tailLines)
                                                                          : [bcard.sess.preview || ""]
                    delegate: Text {
                        required property var modelData
                        width: parent.width
                        text: String(modelData)
                        color: /^›/.test(text) ? cBone : /^▸/.test(text) ? cAsh : /^◆/.test(text) ? cViolet : Qt.rgba(0.84, 0.87, 0.89, 0.8)
                        font.family: "monospace"; font.pixelSize: root.fs(10)
                        elide: Text.ElideRight; maximumLineCount: 1
                        height: root.fs(10) + root.sz(5)
                    }
                }
            }
            Text {
                visible: (bcard.sess.tasks_stalled || 0) > 0
                width: parent.width
                text: root.stalledLabel(bcard.sess.tasks_stalled || 0)
                color: cAmber; font.family: "monospace"; font.pixelSize: root.fs(9); elide: Text.ElideRight
            }
            Text {
                width: parent.width
                text: [root.clock(root.epoch(bcard.sess.last_time)),
                       root.contextLabel(bcard.sess.context_used, bcard.sess.context_window),
                       root.harnessLabel(bcard.sess.harness), bcard.sess.cage ? root.cageWords(bcard.sess.cage) : "", root.shortModel(bcard.sess.model),
                       root.tasksLabel(bcard.sess.tasks_open || 0)].filter(function(x) { return x !== "" }).join("  ·  ")
                color: cAsh; font.family: "monospace"; font.pixelSize: root.fs(9); elide: Text.ElideRight
            }
        }
        // As the list's cards: the handler only schedules (Qt.callLater).
        MouseArea { objectName: "boardCardArea"; anchors.fill: parent; cursorShape: Qt.PointingHandCursor; onClicked: Qt.callLater(root.openSession, bcard.host, bcard.sess.name) }
    }

    // The board: the cards in a grid that fills the panel, and over them the
    // tasks between agents, a dashed curve from the asker to the worker,
    // coloured by how the task stands; one being worked on moves.
    component BoardView: ColumnLayout {
        id: board
        spacing: root.sz(10)
        RowLayout {
            spacing: 8
            Pulse {}
            Text { text: "AGENTS · BOARD"; color: cPhosphor; font.family: "monospace"; font.pixelSize: root.fs(12); font.letterSpacing: 1.5 }
            Item { Layout.fillWidth: true }
            Row {
                spacing: root.sz(10)
                visible: root.boardEdgeList.length > 0
                Repeater {
                    model: [["working", "working"], ["input-required", "blocked"], ["stalled", "stalled"], ["submitted", "queued"]]
                    delegate: Text {
                        required property var modelData
                        text: "╌ " + modelData[1]; color: root.edgeTint(modelData[0])
                        font.family: "monospace"; font.pixelSize: root.fs(9)
                    }
                }
            }
            Lnk { visible: root.haveCore; text: root.usageGlance ? "usage " + root.usageGlance.percent + "%" : "usage"
                  base: !root.usageGlance ? cAsh : root.usageGlance.level === 2 ? cRust : root.usageGlance.level === 1 ? cAmber : cAsh
                  font.pixelSize: root.fs(10); onClicked: Qt.callLater(root.openUsage) }
            Lnk { visible: root.haveCore; text: "voice"; base: cAsh; font.pixelSize: root.fs(10); onClicked: Qt.callLater(root.openVoice) }
            Lnk { objectName: "toList"; text: "list"; base: cAsh; font.pixelSize: root.fs(10); onClicked: Qt.callLater(root.setBoard, false) }
        }
        Text {
            Layout.fillWidth: true
            visible: root.agentHosts.length === 0
            wrapMode: Text.Wrap
            text: root.haveCore ? "Looking for agents among the reachable peers…" : "Agents need shrooms_core, which runs inside Basecamp."
            color: cAsh; font.family: "monospace"; font.pixelSize: root.fs(11)
        }
        RowLayout {
            Layout.fillWidth: true
            Layout.fillHeight: true
            spacing: root.sz(10)
        ScrollView {
            id: boardScroll
            Layout.fillWidth: true
            Layout.fillHeight: true
            clip: true
            contentWidth: availableWidth
            Item {
                width: boardScroll.availableWidth
                // Room above the first row for a link that arcs over it.
                implicitHeight: boardFlow.y + boardFlow.height + root.sz(8)
                height: implicitHeight
                Flow {
                    id: boardFlow
                    objectName: "boardFlow"
                    y: root.sz(40)
                    width: parent.width
                    readonly property real gap: root.sz(28)
                    readonly property int cols: Math.max(1, Math.floor((width + gap) / (root.sz(380) + gap)))
                    readonly property real cardW: Math.floor((width - gap * (cols - 1)) / cols)
                    spacing: gap
                    onPositioningComplete: links.requestPaint()
                    Repeater {
                        model: root.boardCardList
                        delegate: BoardCard {
                            required property var modelData
                            card: modelData
                            width: boardFlow.cardW
                        }
                    }
                }
                Canvas {
                    id: links
                    objectName: "boardLinks"
                    anchors.fill: parent
                    // The dashes' offset: moving along a link being worked on.
                    property real march: 0
                    property int drawn: 0
                    // A link badge is a hit target: tapping it filters the panel to that
                    // pair. This canvas sits ON TOP of the cards, so the area must decide on
                    // PRESS: a press that is not on a badge is not ours and is handed back,
                    // and the press then falls through to the card underneath. Accepting
                    // every press here killed every card tap on the board (2026-10-10).
                    MouseArea {
                        anchors.fill: parent
                        onPressed: (mouse) => { mouse.accepted = root.badgeAt(root.badgeHit, mouse.x, mouse.y) !== "" }
                        onClicked: (mouse) => {
                            var pair = root.badgeAt(root.badgeHit, mouse.x, mouse.y)
                            if (pair !== "") root.linkFilter = pair
                        }
                    }
                    Connections { target: root; function onBoardEdgeListChanged() { links.requestPaint() } }
                    Timer {
                        interval: 90; repeat: true
                        running: board.visible && root.boardEdgeList.some(function(e) { return e.state === "working" })
                        onTriggered: { links.march = (links.march + 1.5) % 11; links.requestPaint() }
                    }
                    function rectOf(key) {
                        for (var i = 0; i < boardFlow.children.length; i++) {
                            var c = boardFlow.children[i]
                            if (c.cardKey === key) return { x: c.x, y: c.y + boardFlow.y, w: c.width, h: c.height }
                        }
                        return null
                    }
                    onPaint: {
                        var ctx = getContext("2d")
                        ctx.reset()
                        var es = root.boardEdgeList, n = 0
                        // Several tasks between the same two cards: each its own curve.
                        var seen = {}
                        for (var i = 0; i < es.length; i++) {
                            var a = rectOf(es[i].from), b = rectOf(es[i].to)
                            if (!a || !b) continue
                            var pair = es[i].from + ">" + es[i].to, k = seen[pair] || 0
                            seen[pair] = k + 1
                            var pts = root.linkCurve(a, b, k * root.sz(10), root.sz(30))
                            root.drawDashed(ctx, pts, root.edgeTint(es[i].state), es[i].state === "working" ? march : 0, root.sz(2))
                            n++
                        }
                        drawn = n
                        // The links carry their tasks: one badge per PAIR, at the middle of the
                        // curve between them, saying how many and - when it is not simply working -
                        // why it is coloured that way. A link that needs a person should say so
                        // where the person is looking.
                        var badges = root.boardLinkData
                        var hits = []
                        for (var m = 0; m < badges.length; m++) {
                            var lb = badges[m]
                            var ra = rectOf(lb.from), rb = rectOf(lb.to)
                            if (!ra || !rb) continue
                            var label = root.linkLabel(lb)
                            if (label === "") continue
                            var lp = root.linkCurve(ra, rb, 0, root.sz(30))
                            // The cubic at t=0.5: (p0 + 3p1 + 3p2 + p3) / 8.
                            var mx = (lp[0].x + 3 * lp[1].x + 3 * lp[2].x + lp[3].x) / 8
                            // The CANVAS's height, not ctx.height: a QML Context2D has no
                            // height, so badgeY got undefined, returned NaN and every badge
                            // was drawn at NaN - invisible, untappable, and the filter with
                            // it. A pure check on badgeY could not see this (2026-10-10).
                            var my = root.badgeY((lp[0].y + 3 * lp[1].y + 3 * lp[2].y + lp[3].y) / 8,
                                                 links.height, root.sz(34))
                            ctx.font = root.fs(9) + "px monospace"
                            var bw = ctx.measureText(label).width + root.sz(8)
                            ctx.fillStyle = root.cVoid
                            ctx.fillRect(mx - bw / 2, my - root.sz(7), bw, root.sz(14))
                            ctx.strokeStyle = root.edgeTint(lb.tone)
                            ctx.lineWidth = 1
                            ctx.strokeRect(mx - bw / 2, my - root.sz(7), bw, root.sz(14))
                            ctx.fillStyle = root.edgeTint(lb.tone)
                            ctx.textAlign = "center"
                            ctx.textBaseline = "middle"
                            ctx.fillText(label, mx, my)
                            // Where this badge is, so a tap on it can filter the panel
                            // to that pair. Recorded here because this is the only place
                            // that knows where the badge ended up.
                            hits.push({ x: mx - bw / 2, y: my - root.sz(7), w: bw, h: root.sz(14),
                                        // The SAME key the panel filters on (both
                                        // directions are one link), or the tap filters to a
                                        // pair no row has and the panel comes out empty - which
                                        // is exactly what the live pass saw (2026-10-10).
                                        pair: pairKeyOf(lb.from, lb.to) })
                        }
                        root.badgeHit = hits
                    }
                }
            }
        }
        // The tasks panel: every task on every machine, grouped as a person
        // needs them, BESIDE the board whose links are those same tasks. A link
        // is one task or several; this is the list of them, in the order that
        // matters - what is waiting on a person first.
        Rectangle {
            id: taskPanel
            objectName: "taskPanel"
            // Also shown when a filter is on and matches nothing: otherwise filtering to a
            // pair with no tasks left hides the panel AND the only way back to all tasks.
            visible: root.boardMode && (root.taskPanelList.length > 0 || root.linkFilter !== "")
            Layout.preferredWidth: Math.min(root.sz(380), Math.max(root.sz(240), root.width * 0.32))
            Layout.fillHeight: true
            color: "transparent"
            ScrollView {
                anchors.fill: parent
                clip: true
                contentWidth: availableWidth
                Column {
                    width: taskPanel.width
                    spacing: root.sz(2)
                    // What the panel is filtered to, and the way back to everything.
                    Lnk {
                        objectName: "allTasks"
                        visible: root.linkFilter !== ""
                        text: "x all tasks  (" + root.pairLabel(root.linkFilter) + ")"
                        font.pixelSize: root.fs(10)
                        base: cAmber
                        onClicked: root.linkFilter = ""
                    }
                    Repeater {
                        model: root.taskPanelList
                        delegate: Item {
                            id: trow
                            required property var modelData
                            width: taskPanel.width
                            height: trow.modelData.kind === "header" ? root.sz(26)
                  : (trow.modelData.latest ? root.sz(58) : root.sz(42))
                            // 75 rows in Done, unacked is a wall nobody clears one tap at a
                            // time. The header offers it once; each ack still goes to the
                            // worker's agent, and only to tasks whose state says so.
                            Lnk {
                                objectName: "ackAll"
                                anchors.right: parent.right
                                anchors.rightMargin: root.sz(14)
                                anchors.top: parent.top
                                anchors.topMargin: root.sz(6)
                                visible: trow.modelData.kind === "header" && trow.modelData.group === "unacked"
                                text: "ACK all"; base: cAmber; font.pixelSize: root.fs(9)
                                onClicked: Qt.callLater(root.ackAllUnacked)
                            }
                            Text {
                                anchors.left: parent.left; anchors.right: parent.right; anchors.top: parent.top
                                anchors.topMargin: root.sz(6)
                                visible: trow.modelData.kind === "header"
                                text: trow.modelData.kind === "header" ? trow.modelData.label + "  " + trow.modelData.count : ""
                                color: trow.modelData.kind === "header" && trow.modelData.group === "needs-you" ? cAmber : cAsh
                                font.family: "monospace"; font.pixelSize: root.fs(10); font.bold: true
                            }
                            Column {
                                visible: trow.modelData.kind === "task"
                                anchors.left: parent.left; anchors.top: parent.top
                                // The ACK takes its width out of the text, rather than sitting
                                // on top of the title (2026-10-10).
                                anchors.right: ackLnk.visible ? ackLnk.left : parent.right
                                anchors.rightMargin: ackLnk.visible ? root.sz(4) : 0
                                Text {
                                    width: parent.width; elide: Text.ElideRight
                                    text: trow.modelData.kind === "task" ? trow.modelData.title : ""
                                    color: cBone; font.family: "monospace"; font.pixelSize: root.fs(11)
                                }
                                Text {
                                    width: parent.width; elide: Text.ElideRight
                                    text: trow.modelData.kind === "task"
                                          ? (trow.modelData.asker || "?") + " \u2192 " + (trow.modelData.worker || "?")
                                            + "  " + root.ageLabel(trow.modelData, root.nowMs)
                                          : ""
                                    color: root.edgeTint(trow.modelData.kind === "task" ? trow.modelData.group : "")
                                    font.family: "monospace"; font.pixelSize: root.fs(9)
                                }
                                Text {
                                    width: parent.width; elide: Text.ElideRight
                                    text: trow.modelData.kind === "task" ? trow.modelData.latest : ""
                                    color: cAsh; font.family: "monospace"; font.pixelSize: root.fs(9)
                                }
                            }
                            MouseArea {
                                anchors.fill: parent
                                enabled: trow.modelData.kind === "task"
                                cursorShape: Qt.PointingHandCursor
                                onClicked: Qt.callLater(function() { root.openTaskRow(trow.modelData) })
                            }
                            // A finished task still owes an ack: that is the one thing a person
                            // must be able to say back, and only where it does something. On top
                            // of the row's MouseArea (so a click here does not also open the
                            // session), and clear of the scrollbar, which sits over the right
                            // edge - the first live pass scrolled the list instead of acking.
                            Lnk {
                                id: ackLnk
                                objectName: "ackLnk"
                                anchors.right: parent.right
                                anchors.rightMargin: root.sz(14)
                                anchors.top: parent.top
                                anchors.topMargin: root.sz(6)
                                visible: trow.modelData.kind === "task" && trow.modelData.group === "unacked"
                                text: "ACK"; base: cAmber; font.pixelSize: root.fs(9)
                                onClicked: Qt.callLater(function() { root.ackTask(trow.modelData) })
                            }
                        }
                    }
                }
            }
        }
        }
    }

    // A session in a cage, by its name: a tag in the cage's violet, its
    // details (image, nix, GitHub login) on hover and in the line below.
    component CageTag: Text {
        id: ctag
        required property var sess
        objectName: "cageTag"
        visible: !!(sess && sess.cage)
        // SEALED in amber for code nobody vouches for (ADR-045).
        text: sess && sess.cage && sess.cage.sealed ? "SEALED" : "CAGED"
        color: sess && sess.cage && sess.cage.sealed ? cAmber : cViolet
        font.family: "monospace"; font.pixelSize: root.fs(9); font.letterSpacing: 1; font.bold: true
        leftPadding: root.sz(5); rightPadding: root.sz(5); topPadding: root.sz(1); bottomPadding: root.sz(1)
        Rectangle { anchors.fill: parent; z: -1; radius: root.sz(4); color: "transparent"; border.color: ctag.color; border.width: 1 }
        ToolTip.visible: ctagMouse.containsMouse && visible
        ToolTip.text: sess && sess.cage ? "in a cage: " + root.cageWords(sess.cage) + (sess.cage.outbox ? "; results in " + sess.cage.outbox : "") : ""
        MouseArea { id: ctagMouse; anchors.fill: parent; hoverEnabled: true; acceptedButtons: Qt.NoButton }
    }

    // Out of quota, by the name: when it comes back; why, on hover.
    component QuotaTag: Text {
        id: qtag
        required property var sess
        objectName: "quotaTag"
        visible: !!(sess && sess.limited)
        text: root.quotaLabel(sess ? sess.limited : null)
        color: cAmber
        font.family: "monospace"; font.pixelSize: root.fs(9); font.letterSpacing: 1; font.bold: true
        leftPadding: root.sz(5); rightPadding: root.sz(5); topPadding: root.sz(1); bottomPadding: root.sz(1)
        Rectangle { anchors.fill: parent; z: -1; radius: root.sz(4); color: "transparent"; border.color: cAmber; border.width: 1 }
        ToolTip.visible: qtagMouse.containsMouse && visible
        ToolTip.text: sess && sess.limited ? sess.limited.reason : ""
        MouseArea { id: qtagMouse; anchors.fill: parent; hoverEnabled: true; acceptedButtons: Qt.NoButton }
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
            // On the board it gives way to the cards, and comes back beside a
            // session opened from one: what needs the owner stays in sight.
            ColumnLayout {
                objectName: "agentList"
                visible: !root.boardMode || root.agentOpen !== null || root.agentCreating
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
                    Lnk { visible: root.haveCore; objectName: "toBoard"; text: "board"; base: cAsh; font.pixelSize: root.fs(10); onClicked: Qt.callLater(root.showBoard) }
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
                visible: !root.boardMode || root.agentOpen !== null || root.agentCreating
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
            BoardView {
                visible: root.boardMode && root.agentOpen === null && !root.agentCreating
                Layout.fillWidth: true
                Layout.fillHeight: true
            }

            ColumnLayout {
                visible: !root.boardMode || root.agentOpen !== null || root.agentCreating
                Layout.fillWidth: true
                Layout.preferredWidth: 0
                Layout.minimumWidth: 0
                Layout.fillHeight: true
                spacing: root.sz(8)

                // On the board, a session is the whole panel; back to the board.
                RowLayout {
                    visible: root.boardMode
                    spacing: 8
                    Lnk { objectName: "backToBoard"; text: "← board"; font.pixelSize: root.fs(11)
                          onClicked: Qt.callLater(root.showBoard) }
                    Text { visible: root.agentOpen !== null; text: root.agentOpen ? root.agentOpen.name : ""; color: cAsh; font.family: "monospace"; font.pixelSize: root.fs(10) }
                }

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
                    CheckBox {
                        id: nsCageBox
                        objectName: "nsCage"
                        visible: root.cageStatus !== null
                        checked: root.nsCage
                        onToggled: root.nsCage = checked
                        text: "in a cage — " + root.cageNote(root.cageStatus)
                        contentItem: Text { leftPadding: nsCageBox.indicator.width + 6; text: nsCageBox.text; color: cAsh; wrapMode: Text.Wrap; font.family: "monospace"; font.pixelSize: root.fs(10); verticalAlignment: Text.AlignVCenter }
                        Layout.fillWidth: true
                    }
                    CageOptions { visible: root.nsCage && root.cageStatus !== null; Layout.fillWidth: true; Layout.leftMargin: root.sz(24) }
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
                        Lnk { objectName: "cageLink"; text: root.agentInfo && root.agentInfo.cage ? "caged" : "cage"
                              base: root.agentInfo && root.agentInfo.cage ? cPhosphor : cAsh; onClicked: Qt.callLater(root.askCage) }
                        // Whether it takes tasks from caged agents (ADR-044):
                        // lit when it does.
                        Lnk { readonly property bool on: !!(root.agentInfo && root.agentInfo.accept_caged)
                              objectName: "acceptCagedLink"; text: on ? "CAGED TASKS" : "caged tasks"; base: on ? cViolet : cAsh
                              onClicked: Qt.callLater(root.setAcceptCaged, !on) }
                        Lnk { text: "restart"; base: cAsh; onClicked: root.askRestart() }
                        Lnk { text: "delete"; base: cAsh; onClicked: root.askDelete() }
                        Lnk { visible: root.agentWorking; text: "■ stop"; base: cRust; onClicked: root.stopTurn() }
                    }
                    Text {
                        text: root.agentOpen ? [root.agentOpen.name, root.agentOpen.mesh,
                              root.agentInfo ? root.harnessLabel(root.agentInfo.harness) : "",
                              root.agentInfo && root.agentInfo.cage ? "in a cage (" + root.cageWords(root.agentInfo.cage) + ")" + (root.agentInfo.cage.outbox ? ", results in " + root.agentInfo.cage.outbox : "") : "",
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
                                    textFormat: TextEdit.RichText
                                    text: root.mdHtml(root.agentStreaming) + "<span style=\"color:" + cPhosphor + ";\">▍</span>"
                                    onLinkActivated: function(link) { root.activateLink(link) }
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
                                        textFormat: TextEdit.RichText
                                        text: readingThis ? root.aloudHtml() : (crow.kind === "said" ? root.mdHtml(crow.text) : root.linkPlain(crow.text))
                                        color: cBone; selectionColor: Qt.rgba(0.21, 0.94, 0.63, 0.35)
                                        font.family: "monospace"; font.pixelSize: root.fs(12)
                                        onLinkActivated: function(link) { root.activateLink(link) }
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
