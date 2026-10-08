package xyz.vpavlin.shrooms

import org.json.JSONObject
import java.io.BufferedReader
import java.io.InputStreamReader
import java.net.HttpURLConnection
import java.net.InetAddress
import java.net.URL

/**
 * The agent API of shrooms-agent on another device (docs/agents.md).
 *
 * Plain HTTP, and only ever to a mesh address: the traffic is inside the
 * WireGuard tunnel, which is what encrypts it and what decides who can reach
 * the agent at all. [AgentClient] refuses any other address, so the cleartext
 * this app is allowed (res/xml/network_security_config.xml) can only ever go
 * through the mesh.
 */
const val AGENT_PORT = 7387

data class AgentSession(
    val name: String,
    val dir: String,
    /** idle, working, or waiting — a permission prompt needs an answer. */
    val state: String,
    val pending: Int,
    val running: Boolean,
    val lastSeq: Long,
    /** When the last event happened, epoch millis; 0 if never. */
    val lastTime: Long = 0,
    val autoApprove: Boolean = false,
    /** Tokens of the model's context the conversation fills, and of how many. */
    val contextUsed: Long = 0,
    val contextWindow: Long = 0,
    /** The start of the last thing the model said. */
    val preview: String = "",
    /** The model the conversation runs on, as its harness names it. */
    val model: String = "",
    /** What runs it: "claude", "pi", … and whether it ever asks before using tools. */
    val harness: String = "claude",
    val approves: Boolean = true,
    /** Listed first, above every machine's others; kept on the agent. */
    val starred: Boolean = false,
    /** Turns that have ended, as the agent counts them; -1 from an agent too old to. */
    val turns: Long = -1,
    /** A2A tasks it has open, and of them those that stalled after the reminders. */
    val tasksOpen: Int = 0,
    val tasksStalled: Int = 0,
    /** The container it runs in (docs/agents-in-cages.md); null when it is not caged. */
    val cage: SessionCage? = null,
    /** Why it cannot work now for want of quota, and until when (epoch millis, 0 unknown); null when it can. */
    val limited: Limited? = null,
)

/** A session out of quota: the plan's limit reached, or its key's allowance spent (the agent's `limited`). */
data class Limited(val reason: String, val until: Long = 0) {
    /** The tag by its name, as in Basecamp: "QUOTA · back 14:20". */
    fun label(now: Long = System.currentTimeMillis()): String =
        "QUOTA" + if (until > 0) " · back " + quotaClock(until, now) else ""

    companion object {
        fun parse(o: JSONObject?): Limited? = o?.let { Limited(it.optString("reason"), AgentClient.parseTime(it.optString("until"))) }
    }
}

/** HH:mm today, "d.M. HH:mm" another day. */
fun quotaClock(ms: Long, now: Long): String {
    val z = java.time.ZoneId.systemDefault()
    val t = java.time.Instant.ofEpochMilli(ms).atZone(z)
    val hm = "%02d:%02d".format(t.hour, t.minute)
    return if (t.toLocalDate() == java.time.Instant.ofEpochMilli(now).atZone(z).toLocalDate()) hm else "${t.dayOfMonth}.${t.monthValue}. $hm"
}

/** A cage: its image, and whether it has the machine's nix and the owner's GitHub login. */
data class SessionCage(val image: String, val nix: Boolean = false, val github: Boolean = false) {
    fun json(): JSONObject = JSONObject().apply {
        if (image.isNotEmpty()) put("image", image)
        if (nix) put("nix", true)
        if (github) put("github", true)
    }

    companion object {
        fun parse(o: JSONObject?): SessionCage? = o?.let { SessionCage(it.optString("image"), it.optBoolean("nix"), it.optBoolean("github")) }
    }
}

/** A coding agent a machine can run sessions of (GET /v1/harnesses). */
data class Harness(val name: String, val title: String, val approves: Boolean)

/** Whether a machine can cage sessions (GET /v1/harnesses' "cage"): it has podman, and the image is ready, being built, or failed to build. */
data class CageOffer(
    val image: String, val ready: Boolean, val building: Boolean, val error: String,
    /** The images offered, the machine's first; and whether it has nix to give. */
    val images: List<String> = listOf(image), val nix: Boolean = false,
)

/** What "+ session" offers on a machine: its harnesses, and a cage if it has podman. */
data class SessionOffer(val harnesses: List<Harness>, val cage: CageOffer?)

/** Claude Code alone: what an agent too old to list its harnesses runs. */
val claudeOnly = listOf(Harness("claude", "Claude Code", true))

/**
 * A Claude Code conversation on an agent's machine — one started in a
 * terminal, say — that a session can take over. [terminals] are claude
 * processes open in the same directory: one may hold it, and two writers on
 * one conversation split it.
 */
data class Conversation(
    val id: String,
    val dir: String,
    val modified: Long,
    val lastUser: String,
    val lastAssistant: String,
    val adoptedBy: String,
    val terminals: List<Pair<Int, String>>,
)

/** A line of the conversation from before the agent had it (the transcript). */
data class Earlier(val time: Long, val role: String, val text: String)

/**
 * A turn that matched a search. [seq] is the event to jump to; 0 when it was
 * said before the agent had the conversation, and then [text] is all of it.
 */
data class Found(val seq: Long, val time: Long, val role: String, val snippet: String, val text: String)

data class AgentEvent(
    val seq: Long,
    /** claude, message, answer or stopped. */
    val kind: String,
    val by: String,
    val data: JSONObject,
    /** Epoch millis. */
    val time: Long = 0,
)

class AgentClient(private val address: String) {
    private val base: String

    init {
        require(isMeshAddress(address)) { "$address is not a mesh address" }
        base = if (address.contains(':')) "http://[$address]:$AGENT_PORT" else "http://$address:$AGENT_PORT"
    }

    fun sessions(timeoutMs: Int = 4000): List<AgentSession> {
        val raw = request("GET", "/v1/sessions", null, timeoutMs)
        val o = JSONObject(raw)
        // Where the machine's subscription stands comes with the list, kept
        // for the at-a-glance usage link (PlanLive).
        runCatching { PlanLive.note(address, UsageView.parseLimits("", raw)) }
        val a = o.optJSONArray("sessions") ?: return emptyList()
        return (0 until a.length()).map { i ->
            val s = a.getJSONObject(i)
            AgentSession(
                name = s.optString("name"),
                dir = s.optString("dir"),
                state = s.optString("state"),
                pending = s.optInt("pending"),
                running = s.optBoolean("running"),
                lastSeq = s.optLong("last_seq"),
                lastTime = parseTime(s.optString("last_time")),
                autoApprove = s.optBoolean("auto_approve"),
                contextUsed = s.optLong("context_used"),
                contextWindow = s.optLong("context_window"),
                preview = s.optString("preview"),
                model = s.optString("model"),
                harness = s.optString("harness").ifEmpty { "claude" },
                // An agent from before harnesses has no caps, and is Claude Code.
                approves = s.optJSONObject("caps")?.optBoolean("approve") ?: true,
                starred = s.optBoolean("starred"),
                turns = s.optLong("turns", -1),
                tasksOpen = s.optInt("tasks_open"),
                tasksStalled = s.optInt("tasks_stalled"),
                cage = SessionCage.parse(s.optJSONObject("cage")),
                limited = Limited.parse(s.optJSONObject("limited")),
            )
        }
    }

    fun create(name: String, dir: String, autoApprove: Boolean = false, harness: String = "claude", cage: SessionCage? = null) {
        request("POST", "/v1/sessions", sessionBody(JSONObject().put("name", name).put("dir", dir)
            .put("auto_approve", autoApprove).put("harness", harness), cage))
    }

    /** Moves a session into a cage, changes it, or (null) takes it out: from its next message. */
    fun setCage(session: String, cage: SessionCage?) {
        request("POST", "/v1/sessions/${enc(session)}/cage", cageBody(cage))
    }

    /** The harnesses this machine runs, Claude Code first. */
    fun harnesses(): List<Harness> = offer().harnesses

    /** The harnesses this machine runs, and whether it can cage a session. */
    fun offer(): SessionOffer =
        runCatching { parseOffer(request("GET", "/v1/harnesses", null)) }.getOrDefault(SessionOffer(claudeOnly, null))

    /** What the model did, per day, session, device and model (UsageView.parse); since a local date or "". */
    fun usage(since: String): String =
        // Long: a machine's first count reads every log it has.
        request("GET", "/v1/usage" + if (since.isEmpty()) "" else "?since=" + enc(since), null, 30_000)

    /** The machine's Claude Code conversations, newest first. */
    fun conversations(limit: Int = 15): List<Conversation> {
        val a = JSONObject(request("GET", "/v1/conversations?limit=$limit", null)).optJSONArray("conversations")
            ?: return emptyList()
        return (0 until a.length()).map { i ->
            val o = a.getJSONObject(i)
            val ts = o.optJSONArray("terminals")
            Conversation(
                id = o.optString("id"), dir = o.optString("dir"), modified = parseTime(o.optString("modified")),
                lastUser = o.optString("last_user"), lastAssistant = o.optString("last_assistant"),
                adoptedBy = o.optString("adopted_by"),
                terminals = (0 until (ts?.length() ?: 0)).map { j ->
                    val t = ts!!.getJSONObject(j)
                    t.optInt("pid") to t.optString("tmux").ifEmpty { "pid " + t.optInt("pid") }
                },
            )
        }
    }

    /** A session continuing a conversation, in the directory it ran in. */
    fun takeOver(name: String, conversation: String, autoApprove: Boolean, cage: SessionCage? = null) {
        request("POST", "/v1/sessions", sessionBody(JSONObject().put("name", name).put("resume", conversation)
            .put("auto_approve", autoApprove), cage))
    }

    /** Ends a terminal's claude, so its conversation can be carried on here. */
    fun stopTerminal(pid: Int) {
        request("POST", "/v1/terminals/$pid/stop", null)
    }

    /** Whether a session asks before running things — the desktop's --dangerously-skip-permissions. */
    /** Stars a session, or unstars it: kept on the agent, so every device lists it first. */
    fun setStarred(session: String, on: Boolean) {
        request("POST", "/v1/sessions/${enc(session)}/settings", JSONObject().put("starred", on).toString())
    }

    fun setAutoApprove(session: String, on: Boolean) {
        // POST rather than PATCH: HttpURLConnection refuses PATCH outright.
        request("POST", "/v1/sessions/${enc(session)}/settings", JSONObject().put("auto_approve", on).toString())
    }

    /** The last [limit] exchanges before the agent had the conversation, oldest first. */
    fun history(session: String, limit: Int = 30): List<Earlier> {
        val a = JSONObject(request("GET", "/v1/sessions/${enc(session)}/history?limit=$limit", null))
            .optJSONArray("history") ?: return emptyList()
        return (0 until a.length()).map { i ->
            val o = a.getJSONObject(i)
            Earlier(parseTime(o.optString("time")), o.optString("role"), o.optString("text"))
        }
    }

    /** Turns of the whole conversation containing [q], newest first. */
    fun search(session: String, q: String, limit: Int = 100): List<Found> {
        val a = JSONObject(request("GET", "/v1/sessions/${enc(session)}/search?q=${enc(q)}&limit=$limit", null))
            .optJSONArray("found") ?: return emptyList()
        return (0 until a.length()).map { i ->
            val o = a.getJSONObject(i)
            Found(o.optLong("seq"), parseTime(o.optString("time")), o.optString("role"),
                o.optString("snippet"), o.optString("text"))
        }
    }

    fun remove(name: String) {
        request("DELETE", "/v1/sessions/${enc(name)}", null)
    }

    /**
     * Sends a turn. [id] is the outbox's, which makes sending it again
     * harmless: the agent takes an id once (a repeat answers "duplicate").
     */
    fun send(session: String, text: String, id: String = "") {
        val body = JSONObject().put("text", text)
        if (id.isNotEmpty()) body.put("id", id)
        request("POST", "/v1/sessions/${enc(session)}/messages", body.toString())
    }

    /**
     * Sends a voice note as a turn: the agent keeps it, transcribes it on its
     * machine and sends what was said — nothing comes back to read first.
     */
    fun voice(session: String, name: String, bytes: ByteArray, id: String) {
        val c = open("POST", "/v1/sessions/${enc(session)}/voice?name=${enc(name)}&id=${enc(id)}", 120_000)
        try {
            c.doOutput = true
            c.setRequestProperty("Content-Type", "audio/mp4")
            c.setFixedLengthStreamingMode(bytes.size)
            c.outputStream.use { it.write(bytes) }
            if (c.responseCode / 100 != 2) throw AgentError(errorOf(c))
        } finally {
            c.disconnect()
        }
    }

    /** Transcribes a voice note that failed again, from the recording the agent kept. */
    fun retryVoice(session: String, id: String) {
        request("POST", "/v1/sessions/${enc(session)}/voice/${enc(id)}/retry", "")
    }

    /** Answers a prompt; for a question, [answers] maps each question to its answer. */
    fun answer(session: String, prompt: String, allow: Boolean, message: String = "", answers: Map<String, String>? = null) {
        val body = JSONObject().put("allow", allow).put("message", message)
        if (answers != null) body.put("answers", JSONObject(answers as Map<*, *>))
        request("POST", "/v1/sessions/${enc(session)}/prompts/${enc(prompt)}", body.toString())
    }

    /**
     * Sends a file to the agent's machine and returns where it was kept, for
     * the next message to name. The agent picks the path; the name only says
     * what the file was.
     */
    fun upload(session: String, name: String, bytes: ByteArray): String {
        val c = open("POST", "/v1/sessions/${enc(session)}/files?name=${enc(name)}", 60_000)
        try {
            c.doOutput = true
            c.setRequestProperty("Content-Type", "application/octet-stream")
            c.setFixedLengthStreamingMode(bytes.size)
            c.outputStream.use { it.write(bytes) }
            if (c.responseCode / 100 != 2) throw AgentError(errorOf(c))
            return JSONObject(c.inputStream.bufferedReader().use { it.readText() }).optString("path")
        } finally {
            c.disconnect()
        }
    }

    /**
     * Sends a voice note and returns its text, transcribed on the agent's
     * machine, which detects the language. Long timeout: a minute of speech
     * takes the model a while.
     */
    fun transcribe(session: String, name: String, bytes: ByteArray): String {
        val c = open("POST", "/v1/sessions/${enc(session)}/transcribe?name=${enc(name)}&lang=auto", 180_000)
        try {
            c.doOutput = true
            c.setRequestProperty("Content-Type", "audio/mp4")
            c.setFixedLengthStreamingMode(bytes.size)
            c.outputStream.use { it.write(bytes) }
            if (c.responseCode / 100 != 2) throw AgentError(errorOf(c))
            return JSONObject(c.inputStream.bufferedReader().use { it.readText() }).optString("text")
        } finally {
            c.disconnect()
        }
    }

    /** The mesh as the agent's machine sees it: (name, mesh, overlay address). */
    fun peers(): List<Triple<String, String, String>> {
        val a = JSONObject(request("GET", "/v1/peers", null, 4000)).optJSONArray("peers") ?: return emptyList()
        return (0 until a.length()).map { i ->
            val o = a.getJSONObject(i)
            Triple(o.optString("name"), o.optString("mesh"), o.optString("overlay"))
        }.filter { isMeshAddress(it.third) }
    }

    fun interrupt(session: String) {
        request("POST", "/v1/sessions/${enc(session)}/interrupt", null)
    }

    /** Gives the session another name; its history and process go with it. */
    fun rename(session: String, name: String) {
        request("POST", "/v1/sessions/${enc(session)}/rename", JSONObject().put("name", name).toString())
    }

    /** Ends the session's process, stuck or not, and starts it again on the same conversation. */
    fun restart(session: String) {
        request("POST", "/v1/sessions/${enc(session)}/restart", null)
    }

    /**
     * Follows a session's events after [after], calling [onEvent] for each, until
     * the connection ends or [stop] says so. Returns the last seq seen, so the
     * caller reconnects from there and loses nothing — the server keeps every
     * event, numbered.
     */
    fun follow(session: String, after: Long, stop: () -> Boolean, tail: Int = 0, onOpen: () -> Unit = {},
               onEvent: (AgentEvent) -> Unit): Long {
        var last = after
        // tail: on a first connection, only the last N events — a long
        // session's whole history is thousands, and replaying them is slow.
        val t = if (tail > 0 && after == 0L) "&tail=$tail" else ""
        val c = open("GET", "/v1/sessions/${enc(session)}/events?after=$after$t", 10_000)
        // The server sends a comment every 20 seconds; 60 without anything is a
        // dead connection, which on mobile data is the normal way they end.
        c.readTimeout = 60_000
        c.setRequestProperty("Accept", "text/event-stream")
        try {
            if (c.responseCode != 200) throw AgentError(errorOf(c))
            onOpen()
            BufferedReader(InputStreamReader(c.inputStream, Charsets.UTF_8)).use { r ->
                while (!stop()) {
                    val line = r.readLine() ?: break
                    if (!line.startsWith("data: ")) continue
                    val e = parseEvent(JSONObject(line.removePrefix("data: ")))
                    // Streamed text is live only and carries the last real
                    // event's number, so it is passed on without moving `last`.
                    if (e.kind == "partial") { onEvent(e); continue }
                    if (e.seq <= last) continue
                    last = e.seq
                    onEvent(e)
                    // Asked to stop by what it was just given: without
                    // waiting for the next line, which may be 20 s away.
                    if (stop()) break
                }
            }
        } finally {
            c.disconnect()
        }
        return last
    }

    private fun open(method: String, path: String, timeoutMs: Int): HttpURLConnection {
        val c = URL(base + path).openConnection() as HttpURLConnection
        c.requestMethod = method
        c.connectTimeout = timeoutMs
        c.readTimeout = timeoutMs
        return c
    }

    private fun request(method: String, path: String, body: String?, timeoutMs: Int = 15_000): String {
        val c = open(method, path, timeoutMs)
        try {
            if (body != null) {
                c.doOutput = true
                c.setRequestProperty("Content-Type", "application/json")
                c.outputStream.use { it.write(body.toByteArray()) }
            }
            if (c.responseCode / 100 != 2) throw AgentError(errorOf(c))
            return c.inputStream.bufferedReader().use { it.readText() }
        } finally {
            c.disconnect()
        }
    }

    private fun errorOf(c: HttpURLConnection): String {
        val body = runCatching { c.errorStream?.bufferedReader()?.use { it.readText() } }.getOrNull()
        val msg = body?.let { runCatching { JSONObject(it).optString("error") }.getOrNull() }
        return msg?.takeIf { it.isNotEmpty() } ?: "HTTP ${c.responseCode}"
    }

    private fun enc(s: String) = java.net.URLEncoder.encode(s, "UTF-8").replace("+", "%20")

    companion object {
        private val ipv4 = Regex("""^\d{1,3}(\.\d{1,3}){3}$""")

        /**
         * Overlay addresses are ULA (fd00::/8, ADR: cryptographic addressing);
         * the IPv4 aliases are in 198.18.0.0/15 (ADR-021). Anything else is not
         * the mesh, and the cleartext allowance must never reach it.
         */
        fun isMeshAddress(address: String): Boolean {
            // Literals only. getByName on a name would resolve it, and a name
            // can resolve to anything; on a literal it parses and nothing more.
            val literal = address.contains(':') || ipv4.matches(address)
            if (!literal) return false
            val a = runCatching { InetAddress.getByName(address) }.getOrNull() ?: return false
            val b = a.address
            return when (b.size) {
                16 -> (b[0].toInt() and 0xff) == 0xfd
                4 -> (b[0].toInt() and 0xff) == 198 && ((b[1].toInt() and 0xff) == 18 || (b[1].toInt() and 0xff) == 19)
                else -> false
            }
        }

        fun parseEvent(o: JSONObject) = AgentEvent(
            seq = o.optLong("seq"),
            kind = o.optString("kind"),
            by = o.optString("by"),
            data = o.optJSONObject("data") ?: JSONObject(),
            time = parseTime(o.optString("time")),
        )

        /** RFC 3339 with up to nanoseconds, as Go writes it; 0 if absent or unreadable. */
        fun parseTime(s: String): Long = runCatching {
            if (s.isEmpty() || s.startsWith("0001-")) 0L
            else java.time.OffsetDateTime.parse(s).toInstant().toEpochMilli()
        }.getOrDefault(0L)
    }
}

class AgentError(message: String) : Exception(message)

/** What creating a session sends: [fields], and its [cage] if it is to have one. */
fun sessionBody(fields: JSONObject, cage: SessionCage?): String =
    (if (cage != null) fields.put("cage", cage.json()) else fields).toString()

/** What moving a session sends: its new cage, or null to take it out. */
fun cageBody(cage: SessionCage?): String = JSONObject().put("cage", cage?.json() ?: JSONObject.NULL).toString()

/** GET /v1/harnesses: the harnesses (Claude Code alone from an agent too old to list them) and the cage, if offered. */
fun parseOffer(raw: String): SessionOffer {
    val o = JSONObject(raw)
    val a = o.optJSONArray("harnesses")
    val hs = (0 until (a?.length() ?: 0)).map { i ->
        val h = a!!.getJSONObject(i)
        Harness(h.optString("name"), h.optString("title"), h.optJSONObject("caps")?.optBoolean("approve") ?: false)
    }.ifEmpty { claudeOnly }
    val c = o.optJSONObject("cage")?.takeIf { it.optBoolean("available") }
    return SessionOffer(hs, c?.let {
        val ims = it.optJSONArray("images")
        CageOffer(it.optString("image"), it.optBoolean("ready"), it.optBoolean("building"), it.optString("error"),
            (0 until (ims?.length() ?: 0)).map { i -> ims!!.getString(i) }.ifEmpty { listOf(it.optString("image")) },
            it.optBoolean("nix"))
    })
}
