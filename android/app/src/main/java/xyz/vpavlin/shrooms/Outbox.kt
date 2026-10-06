package xyz.vpavlin.shrooms

import android.content.Context
import kotlinx.coroutines.Dispatchers
import kotlinx.coroutines.flow.MutableStateFlow
import kotlinx.coroutines.sync.Mutex
import kotlinx.coroutines.sync.withLock
import kotlinx.coroutines.withContext
import org.json.JSONArray
import org.json.JSONObject
import java.io.File

/**
 * Something written to a session, kept on the phone until its agent has it:
 * a message, or a voice note waiting to be sent as one. [id] is made here and
 * goes with it, so sending again — the network went before the answer came —
 * is harmless: the agent takes an id once.
 */
data class Outgoing(
    val id: String,
    val address: String,
    val host: String,
    val session: String,
    /** "text" or "voice". */
    val kind: String,
    val text: String = "",
    /** The recording, for a voice note: a file under the app's own storage. */
    val file: String = "",
    val created: Long = 0,
    /** Why the last try failed, for showing; empty if never tried or fine. */
    val lastError: String = "",
    /** Files sent with a message, uploaded just before it, in this order. */
    val attachments: List<Attachment> = emptyList(),
)

/**
 * A file going with a message: a copy under the app's own storage until the
 * agent's machine has it, then [sent], the path it was kept at there — which
 * is what the message names. Recorded as soon as it is known, so a send that
 * fails after it does not upload the file again.
 */
data class Attachment(val file: String, val name: String, val sent: String = "")

/**
 * The outbox: typed or recorded with the machine unreachable — or the phone
 * offline — and sent when it can be, in order per session, by whichever is
 * running: the conversation on screen, or the watcher in the background.
 */
object Outbox {
    /** Bumped on every change, for what shows the outbox to redraw. */
    val changed = MutableStateFlow(0L)
    private val lock = Mutex()

    private fun file(ctx: Context) = File(ctx.filesDir, "outbox.json")

    /** Where a voice note waits: inside the app, not in a cache that may be cleared. */
    fun voiceDir(ctx: Context) = File(ctx.filesDir, "outbox").apply { mkdirs() }

    fun encode(items: List<Outgoing>): String = JSONArray(items.map {
        JSONObject().put("id", it.id).put("address", it.address).put("host", it.host).put("session", it.session)
            .put("kind", it.kind).put("text", it.text).put("file", it.file).put("created", it.created)
            .put("error", it.lastError)
            .put("attachments", JSONArray(it.attachments.map { a ->
                JSONObject().put("file", a.file).put("name", a.name).put("sent", a.sent)
            }))
    }).toString()

    fun decode(json: String): List<Outgoing> = runCatching {
        val a = JSONArray(json)
        (0 until a.length()).map { i ->
            val o = a.getJSONObject(i)
            Outgoing(o.optString("id"), o.optString("address"), o.optString("host"), o.optString("session"),
                o.optString("kind"), o.optString("text"), o.optString("file"), o.optLong("created"),
                o.optString("error"),
                o.optJSONArray("attachments")?.let { at ->
                    (0 until at.length()).map { j ->
                        val x = at.getJSONObject(j)
                        Attachment(x.optString("file"), x.optString("name"), x.optString("sent"))
                    }
                } ?: emptyList())
        }.filter { it.id.isNotEmpty() && (it.kind == "text" || it.kind == "voice") }
    }.getOrDefault(emptyList())

    @Synchronized fun list(ctx: Context): List<Outgoing> =
        file(ctx).takeIf { it.exists() }?.let { decode(it.readText()) } ?: emptyList()

    @Synchronized private fun write(ctx: Context, items: List<Outgoing>) {
        val f = file(ctx)
        val tmp = File(f.path + ".tmp")
        tmp.writeText(encode(items))
        tmp.renameTo(f)
        changed.value = changed.value + 1
    }

    @Synchronized fun add(ctx: Context, o: Outgoing) = write(ctx, list(ctx) + o)

    /** Takes one out — sent, or given up on — with its recording and files. */
    @Synchronized fun remove(ctx: Context, id: String) {
        val items = list(ctx)
        items.firstOrNull { it.id == id }?.let { o ->
            o.file.takeIf { it.isNotEmpty() }?.let { File(it).delete() }
            o.attachments.forEach { File(it.file).delete() }
        }
        write(ctx, items.filterNot { it.id == id })
    }

    @Synchronized private fun update(ctx: Context, o: Outgoing) =
        write(ctx, list(ctx).map { if (it.id == o.id) o else it })

    /**
     * Keeps a copy of a file picked to go with the next message, so that
     * picking it needs nothing from the agent's machine. A copy no message
     * took — picked, then the screen left — is cleared by [sweep].
     */
    fun keep(ctx: Context, name: String, from: java.io.InputStream): Attachment {
        val f = File(voiceDir(ctx), newId() + "-" + name.replace(Regex("[/\\\\]"), "_"))
        f.outputStream().use { from.copyTo(it) }
        return Attachment(f.path, name)
    }

    /** Deletes files in the outbox's folder that no queued item names, a day after they were made. */
    @Synchronized fun sweep(ctx: Context, now: Long = System.currentTimeMillis()) {
        val named = list(ctx).flatMap { o -> listOf(o.file) + o.attachments.map { it.file } }.toSet()
        voiceDir(ctx).listFiles()?.forEach { f ->
            if (f.path !in named && now - f.lastModified() > 24 * 3600_000L) f.delete()
        }
    }

    /**
     * Sends one item: a voice note, or a message after its files — each file
     * uploaded once, its path recorded through [progress] as soon as it is
     * known, and the message naming them all. Throws on the first failure,
     * with what was done kept.
     */
    fun sendOne(
        o: Outgoing,
        upload: (Attachment) -> String,
        send: (Outgoing, String) -> Unit,
        voice: (Outgoing) -> Unit,
        progress: (Outgoing) -> Unit,
    ) {
        if (o.kind == "voice") return voice(o)
        var cur = o
        for ((i, a) in o.attachments.withIndex()) {
            if (a.sent.isNotEmpty()) continue
            val path = upload(a)
            cur = cur.copy(attachments = cur.attachments.toMutableList().also { it[i] = a.copy(sent = path) })
            progress(cur)
        }
        send(cur, withAttachments(cur.text, cur.attachments.map { it.sent }))
    }

    @Synchronized private fun failed(ctx: Context, id: String, why: String) =
        write(ctx, list(ctx).map { if (it.id == id) it.copy(lastError = why) else it })

    fun newId(): String = "o-" + System.currentTimeMillis().toString(36) + "-" + (0..999_999).random().toString(36)

    /**
     * What to send now, in order: per session, its oldest first, and nothing
     * after one that has not gone — the order things were said in is kept.
     */
    fun nextPerSession(items: List<Outgoing>): List<Outgoing> =
        items.sortedBy { it.created }.groupBy { it.address + "/" + it.session }.values.map { it.first() }

    /**
     * Sends what can be sent: per session, oldest first, until one fails.
     * [only] limits it — the conversation on screen flushes its own.
     */
    suspend fun flush(ctx: Context, only: (Outgoing) -> Boolean = { true }) = lock.withLock {
        withContext(Dispatchers.IO) {
            runCatching { sweep(ctx) }
            while (true) {
                val next = nextPerSession(list(ctx)).filter(only)
                if (next.isEmpty()) break
                var sent = false
                for (o in next) {
                    val c = AgentClient(o.address)
                    val r = runCatching {
                        sendOne(o,
                            upload = { a -> c.upload(o.session, a.name, File(a.file).readBytes()) },
                            send = { q, text -> c.send(q.session, text, q.id) },
                            voice = { q -> c.voice(q.session, File(q.file).name, File(q.file).readBytes(), q.id) },
                            progress = { q -> update(ctx, q) })
                    }
                    r.onSuccess { remove(ctx, o.id); sent = true }
                        .onFailure { failed(ctx, o.id, it.message ?: "not sent") }
                }
                // Another round only while something went: the rest of a
                // session's queue goes once its head has.
                if (!sent) break
            }
        }
    }
}
