package xyz.vpavlin.shrooms

import android.content.Context
import org.json.JSONArray
import org.json.JSONObject
import java.io.File

/**
 * The end of each conversation opened here, kept on the phone: what a
 * session shows while its machine cannot be reached — the phone offline, the
 * laptop asleep — instead of an empty screen. Shown marked as kept, and
 * replaced by the machine's own events as soon as they arrive.
 */
data class KeptHistory(val saved: Long, val events: List<AgentEvent>, val earlier: List<Earlier>)

object History {
    /** At most this many events are kept per session… */
    const val EVENTS = SESSION_TAIL
    /** …and at most this much of them: a tool's output can be megabytes. */
    const val BYTES = 1 shl 20
    /**
     * Strings longer than this — a tool's output, nearly always, folded to its
     * first line when shown — are kept cut: whole, a few of them filled the
     * megabyte and a busy session's copy held 66 events.
     */
    const val STRING = 4096

    /** [v] with every string in it cut to [STRING]; a copy, [v] untouched. */
    fun trimmed(v: Any?): Any? = when (v) {
        is String -> if (v.length > STRING) v.take(STRING) + "…" else v
        is JSONObject -> JSONObject().also { o -> v.keys().forEach { k -> o.put(k, trimmed(v.opt(k))) } }
        is JSONArray -> JSONArray().also { a -> for (i in 0 until v.length()) a.put(trimmed(v.opt(i))) }
        else -> v
    }

    /**
     * The newest of [events] that fit in [EVENTS] and [BYTES], oldest first.
     * Streamed text is never kept: the whole message that follows it is.
     */
    fun encode(saved: Long, events: List<AgentEvent>, earlier: List<Earlier>): String {
        val kept = ArrayList<JSONObject>()
        var size = 0
        for (e in events.asReversed()) {
            if (e.kind == "partial") continue
            val o = JSONObject().put("seq", e.seq).put("kind", e.kind).put("by", e.by).put("data", trimmed(e.data))
                .put("time", if (e.time > 0) java.time.Instant.ofEpochMilli(e.time).toString() else "")
            val n = o.toString().length
            if (kept.size >= EVENTS || size + n > BYTES) break
            kept += o
            size += n
        }
        return JSONObject().put("saved", saved)
            .put("events", JSONArray(kept.asReversed()))
            .put("earlier", JSONArray(earlier.map { JSONObject().put("time", it.time).put("role", it.role).put("text", it.text) }))
            .toString()
    }

    fun decode(json: String): KeptHistory? = runCatching {
        val o = JSONObject(json)
        val ev = o.optJSONArray("events") ?: JSONArray()
        val ea = o.optJSONArray("earlier") ?: JSONArray()
        KeptHistory(
            o.optLong("saved"),
            (0 until ev.length()).map { AgentClient.parseEvent(ev.getJSONObject(it)) },
            (0 until ea.length()).map { i ->
                ea.getJSONObject(i).let { Earlier(it.optLong("time"), it.optString("role"), it.optString("text")) }
            },
        )
    }.getOrNull()

    private fun file(ctx: Context, host: String, session: String): File {
        val key = java.security.MessageDigest.getInstance("SHA-256")
            .digest("$host/$session".toByteArray()).joinToString("") { "%02x".format(it) }.take(32)
        return File(File(ctx.filesDir, "history").apply { mkdirs() }, "$key.json")
    }

    fun load(ctx: Context, host: String, session: String): KeptHistory? =
        runCatching { file(ctx, host, session).readText() }.getOrNull()?.let(::decode)

    fun save(ctx: Context, host: String, session: String, events: List<AgentEvent>, earlier: List<Earlier>) {
        runCatching {
            val f = file(ctx, host, session)
            // Its own temporary file: the watcher and the screen may save the
            // same conversation at once.
            val tmp = File.createTempFile(f.name, ".tmp", f.parentFile)
            tmp.writeText(encode(System.currentTimeMillis(), events, earlier))
            if (!tmp.renameTo(f)) tmp.delete()
        }
    }

    /**
     * What to fetch to bring a kept copy up to [lastSeq], the session's newest
     * event: null when it is up to date; otherwise the event to read after,
     * 0 for the whole tail — nothing kept yet, or the session is not the one
     * kept (its numbers went backwards: deleted and made again).
     */
    fun after(kept: KeptHistory?, lastSeq: Long): Long? {
        val have = kept?.events?.lastOrNull()?.seq ?: 0L
        return when {
            lastSeq <= 0L || have == lastSeq -> null
            have > lastSeq -> 0L
            else -> have
        }
    }

    /** A kept copy with [more] after it; [after] 0 means [more] is all of it. */
    fun extend(kept: KeptHistory?, after: Long, more: List<AgentEvent>): List<AgentEvent> =
        if (after == 0L || kept == null) more else kept.events + more

    /**
     * Brings the copy of one session up to date from its machine, reading only
     * what it lacks — so a conversation is there offline without having been
     * opened first. Called by the watcher in the background. Returns the
     * events it read, for auto-play (Speech.heard).
     */
    fun refresh(ctx: Context, client: AgentClient, host: String, session: String, lastSeq: Long): List<AgentEvent> {
        val kept = load(ctx, host, session)
        val after = after(kept, lastSeq) ?: return emptyList()
        val more = ArrayList<AgentEvent>()
        var last = after
        client.follow(session, after, stop = { last >= lastSeq }, tail = EVENTS) { e ->
            if (e.kind != "partial") { more += e; last = e.seq }
        }
        if (more.isEmpty()) return more
        val earlier = if (after == 0L || kept == null) {
            runCatching { client.history(session, 30) }.getOrDefault(emptyList())
        } else kept.earlier
        save(ctx, host, session, extend(kept, after, more), earlier)
        return more
    }

    fun forget(ctx: Context, host: String, session: String) {
        file(ctx, host, session).delete()
    }

    /** The copy follows a session that was renamed. */
    fun rename(ctx: Context, host: String, from: String, to: String) {
        val f = file(ctx, host, from)
        if (f.exists()) f.renameTo(file(ctx, host, to))
    }
}
