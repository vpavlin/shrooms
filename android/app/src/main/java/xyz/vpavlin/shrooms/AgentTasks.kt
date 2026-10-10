package xyz.vpavlin.shrooms

import org.json.JSONArray
import org.json.JSONObject

/**
 * The A2A tasks between agents, as the phone lists them (ADR-047): read live
 * from each machine's agent, grouped by what a person has to do about them.
 * The same rules as the Basecamp board's panel, so the two never disagree on
 * what a task is called or where it belongs.
 */
data class AgentTask(
    /** SESSION:MESSAGE-ID, the worker's own id. */
    val id: String,
    /** The worker session, on the machine that answered. */
    val session: String,
    /** Who asked, as the worker's agent wrote it: "laptop.default (laptop/shrooms)". */
    val from: String,
    /** The A2A state as a word: working, input-required, completed, … */
    val state: String,
    /** The asker's own title (shrooms/title); empty when it gave none. */
    val asked: String = "",
    /** The request, from the task's history. */
    val request: String = "",
    /** What the task last said. */
    val latest: String = "",
    /** When it last changed, epoch millis; 0 when unknown. */
    val at: Long = 0,
    val acked: Boolean = false,
    val stalled: Boolean = false,
    /** Waiting for its session to be free: not yet arrived anywhere to jump to. */
    val queued: Boolean = false,
) {
    /** The message id part, which the worker's session received the task as. */
    val messageId: String get() = id.substringAfter(':', "")
}

/** A task with the machine that holds it, which is where it is acknowledged. */
data class TaskRow(val host: String, val address: String, val mesh: String, val task: AgentTask, val group: String)

object AgentTasks {
    const val NEEDS_YOU = "needs-you"
    const val BLOCKED = "blocked"
    const val WORKING = "working"
    const val STALLED = "stalled"
    const val UNACKED = "unacked"
    const val DONE = "done"
    val ORDER = listOf(NEEDS_YOU, BLOCKED, WORKING, STALLED, UNACKED)

    private val FINISHED = setOf("completed", "failed", "canceled", "rejected", "expired")

    fun label(group: String): String = when (group) {
        NEEDS_YOU -> "Needs you"
        BLOCKED -> "Blocked"
        WORKING -> "Working"
        STALLED -> "Stalled"
        else -> "Done, unacked"
    }

    /** "TASK_STATE_INPUT_REQUIRED" → "input-required". */
    fun stateWord(s: String): String = s.removePrefix("TASK_STATE_").lowercase().replace('_', '-')

    /**
     * Where a task belongs. A task waiting on its asker comes first even when
     * it also stalled: somebody has to answer it. Waiting on a person — asked
     * from an app, so no session named — it needs you; waiting on the agent
     * session that asked, it is blocked on that agent (which is told, and may
     * be answered for, nudged, or called off). Acknowledged ones are finished
     * and not listed at all.
     */
    fun group(t: AgentTask): String = when {
        t.state == "input-required" -> if (CLAIM.containsMatchIn(t.from)) BLOCKED else NEEDS_YOU
        t.state in FINISHED -> if (t.acked) DONE else UNACKED
        t.stalled -> STALLED
        else -> WORKING
    }

    /**
     * What a task is called: the asker's title, else the request's first
     * line, else what it last said, else its id. One line, never cut here:
     * the row ellipsizes to whatever width it has.
     */
    fun title(t: AgentTask): String =
        listOf(t.asked, firstLine(t.request), firstLine(t.latest), t.id).map(::oneLine).first { it.isNotEmpty() }

    private fun oneLine(s: String) = s.replace(Regex("\\s+"), " ").trim()
    private fun firstLine(s: String) = s.lineSequence().map { it.trim() }.firstOrNull { it.isNotEmpty() } ?: ""

    private val CLAIM = Regex("""\(([^)/]+)/([^),]+)(, in a cage)?\)\s*$""")

    /**
     * Who asked: the session the asking agent named, or the device itself
     * when no session was named (the phone, Basecamp). Caged askers keep the
     * cage as a flag instead of in the name.
     */
    fun asker(from: String): Pair<String, Boolean> {
        CLAIM.find(from)?.let { return it.groupValues[2] to it.groupValues[3].isNotEmpty() }
        val device = from.substringBefore(" (").trim()
        return (device.ifEmpty { "?" }) to false
    }

    /**
     * How long since it moved, said for what it is: an open task has been
     * quiet that long, a finished one done that long ago. `at` is the last
     * change, not when it was asked.
     */
    fun age(group: String, at: Long, now: Long, queued: Boolean = false): String {
        if (queued) return "queued"
        if (at <= 0) return ""
        val s = maxOf(0, (now - at) / 1000)
        val n = when {
            s < 60 -> "${s}s"
            s < 3600 -> "${s / 60}m"
            s < 86400 -> "${s / 3600}h"
            else -> "${s / 86400}d"
        }
        return (if (group == UNACKED || group == DONE) "done " else "quiet ") + n
    }

    /**
     * Every machine's tasks as one list: what waits on a person first, oldest
     * first within a group. A machine reached two ways (by name and by an
     * address added by hand) answers with the same tasks; each is listed once.
     */
    fun rows(perHost: List<Pair<AgentHost, List<AgentTask>>>): List<TaskRow> =
        perHost.flatMap { (h, ts) -> ts.map { TaskRow(h.name, h.address, h.mesh, it, group(it)) } }
            .filter { it.group != DONE }
            .distinctBy { it.task.id + "|" + it.task.from }
            .sortedWith(compareBy<TaskRow>({ ORDER.indexOf(it.group) }, { it.task.at }))

    /** The tasks with those acknowledged here marked so, until their agents say the same. */
    fun withAcked(perHost: List<Pair<AgentHost, List<AgentTask>>>, ids: Set<String>): List<Pair<AgentHost, List<AgentTask>>> =
        withLocal(perHost, ids, emptyMap())

    /**
     * What was done here, over what the agents last said: acknowledged, and
     * answered (working again) — so the row moves at once, not a round later.
     */
    fun withLocal(perHost: List<Pair<AgentHost, List<AgentTask>>>, acked: Set<String>,
                  states: Map<String, String>): List<Pair<AgentHost, List<AgentTask>>> =
        if (acked.isEmpty() && states.isEmpty()) perHost
        else perHost.map { (h, ts) ->
            h to ts.map { t ->
                val s = states[t.id]?.takeIf { t.state == "input-required" } ?: t.state
                if (t.id in acked || s != t.state) t.copy(acked = t.acked || t.id in acked, state = s) else t
            }
        }

    /**
     * Of a search for the task, the hit that IS its arrival: the agent's own
     * header starts "[shrooms task <id> from"; follow-ups say "— more from".
     */
    fun arrival(found: List<Found>, id: String): Found? {
        val want = "[shrooms task $id from"
        return found.firstOrNull { it.snippet.startsWith(want) || it.text.startsWith(want) } ?: found.firstOrNull()
    }

    fun parse(o: JSONObject): AgentTask {
        val md = o.optJSONObject("metadata") ?: JSONObject()
        val status = o.optJSONObject("status") ?: JSONObject()
        fun textOf(m: JSONObject?): String = m?.optJSONArray("parts")?.optJSONObject(0)?.optString("text") ?: ""
        val history = o.optJSONArray("history")
        var request = ""
        if (history != null) for (i in 0 until history.length()) {
            val m = history.optJSONObject(i) ?: continue
            val role = m.optString("role").lowercase()
            if (role.isEmpty() || role == "role_user" || role == "user") { request = textOf(m); break }
        }
        return AgentTask(
            id = o.optString("id"),
            session = md.optString("shrooms/session"),
            from = md.optString("shrooms/from"),
            state = stateWord(status.optString("state")),
            asked = md.optString("shrooms/title"),
            request = request,
            latest = textOf(status.optJSONObject("message")),
            at = AgentClient.parseTime(status.optString("timestamp")),
            acked = md.optBoolean("shrooms/acknowledged"),
            stalled = md.optBoolean("shrooms/stalled"),
            queued = md.optBoolean("shrooms/queued"),
        )
    }
}

/**
 * The tasks list as last seen, kept on the phone so the screen opens with it
 * at once and the agents' answers replace it as they come (as HostCache does
 * for the session list).
 */
object TaskCache {
    fun encode(rows: List<TaskRow>): String = JSONArray().apply {
        for (r in rows) put(JSONObject()
            .put("host", r.host).put("address", r.address).put("mesh", r.mesh)
            .put("id", r.task.id).put("session", r.task.session).put("from", r.task.from)
            .put("state", r.task.state).put("asked", r.task.asked).put("request", r.task.request.take(500))
            .put("latest", r.task.latest.take(500)).put("at", r.task.at)
            .put("acked", r.task.acked).put("stalled", r.task.stalled).put("queued", r.task.queued))
    }.toString()

    fun decode(s: String): List<TaskRow> = runCatching {
        val a = JSONArray(s)
        (0 until a.length()).map { i ->
            val o = a.getJSONObject(i)
            val t = AgentTask(o.optString("id"), o.optString("session"), o.optString("from"), o.optString("state"),
                o.optString("asked"), o.optString("request"), o.optString("latest"), o.optLong("at"),
                o.optBoolean("acked"), o.optBoolean("stalled"), o.optBoolean("queued"))
            TaskRow(o.optString("host"), o.optString("address"), o.optString("mesh"), t, AgentTasks.group(t))
        }
    }.getOrDefault(emptyList())
}

