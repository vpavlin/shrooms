package xyz.vpavlin.shrooms

import android.content.Context
import android.media.AudioAttributes
import android.media.AudioFocusRequest
import android.media.AudioManager
import android.os.Bundle
import android.speech.tts.TextToSpeech
import android.speech.tts.UtteranceProgressListener
import kotlinx.coroutines.flow.MutableStateFlow
import org.json.JSONObject
import java.util.Locale

/**
 * The model's replies read aloud, with the phone's own speech engine: offline,
 * nothing to install on the agent's machine, and it starts at once — so
 * research results can be listened to on the go. A ▶ on each reply; and per
 * session, auto-play of new replies (the model's text only: not tool calls,
 * their output or its thinking), from the conversation on screen or, with the
 * phone in a pocket, from the background watcher.
 */
object Speech {
    /**
     * The reply being read: its id ("host/session/seq"), its sentences, the
     * one being read now, and whether it is paused. Null when nothing is.
     * Read sentence by sentence — by the app, not the engine — so any engine
     * (Google's, SherpaTTS with Piper voices, …) can be paused, skipped and
     * followed: the screen highlights the sentence being read.
     */
    data class Reading(val id: String, val sentences: List<String>, val index: Int, val paused: Boolean)

    val reading = MutableStateFlow<Reading?>(null)

    // --- what is said: pure, for the tests ------------------------------------

    /**
     * Markdown as it should sound: code blocks are named rather than read out
     * symbol by symbol, links become their text, and the marks of emphasis,
     * headings, lists and tables go.
     */
    fun speakable(markdown: String): String {
        var s = markdown.replace("\r", "")
        // Fenced code: named, with its language when it has one.
        s = Regex("```([A-Za-z0-9_+-]*)[^\\n]*\\n[\\s\\S]*?(```|$)").replace(s) { m ->
            val lang = m.groupValues[1]
            if (lang.isNotEmpty()) "\n($lang code)\n" else "\n(code)\n"
        }
        s = Regex("!\\[([^\\]]*)\\]\\([^)]*\\)").replace(s) { it.groupValues[1] } // images
        s = Regex("\\[([^\\]]+)\\]\\([^)]*\\)").replace(s) { it.groupValues[1] }  // links
        s = Regex("<(https?://[^>]+)>").replace(s) { "link" }
        s = Regex("https?://\\S+").replace(s, "link")
        s = Regex("`([^`]*)`").replace(s) { it.groupValues[1] }
        s = spokenPaths(s)
        s = s.lines().joinToString("\n") { line ->
            var l = line
            l = Regex("^\\s{0,3}#{1,6}\\s+").replace(l, "")           // headings
            l = Regex("^\\s*>\\s?").replace(l, "")                    // quotes
            l = Regex("^\\s*([-*+]|\\d+[.)])\\s+").replace(l, "")     // list markers
            if (Regex("^\\s*\\|?[\\s:|-]+\\|[\\s:|-]*$").matches(l)) l = "" // table rule
            l = l.replace("|", ", ")
            if (Regex("^\\s*([-*_]\\s*){3,}$").matches(l)) l = ""     // horizontal rule
            l
        }
        s = Regex("(\\*\\*|\\*|~~)(?=\\S)(.+?)(?<=\\S)\\1").replace(s) { it.groupValues[2] }
        // Underscores only as emphasis between words: inside one —
        // basecamp_voice_core.lgx, x86_64 — they are the word.
        s = Regex("(?<![\\w])(__|_)(?=\\S)(.+?)(?<=\\S)\\1(?![\\w])").replace(s) { it.groupValues[2] }
        s = Regex("\\n{3,}").replace(s, "\n\n")
        return s.trim()
    }

    private val czech = Regex("[ěščřžýůťďňĚŠČŘŽÝŮŤĎŇ]")

    /**
     * Czech or English, from the text: Czech has letters English never uses,
     * and a reply of any length in Czech uses them. Only these two — the
     * languages these conversations are in.
     */
    fun isCzech(text: String): Boolean {
        val letters = text.count { it.isLetter() }
        if (letters == 0) return false
        return czech.findAll(text).count() * 100 >= letters // 1% and up
    }

    /**
     * Paths and places said the way a person would: "internal/agent/session.go:654"
     * is "session.go, line 654" — the directories, read out, were what made a
     * reply impossible to follow by ear.
     */
    fun spokenPaths(text: String): String =
        Regex("(?<![\\w/.-])(?:[\\w.~-]+/)*([\\w-]+(?:\\.[\\w-]+)*\\.[A-Za-z][\\w]{0,9})(?::(\\d+))?(?::\\d+)?(?![\\w/])")
            .replace(text) { m ->
                val (name, line) = m.destructured
                val dirs = m.value.contains('/')
                when {
                    line.isNotEmpty() -> "$name, line $line"
                    dirs -> name
                    else -> m.value
                }
            }

    /**
     * The text as sentences, the units read and highlighted: cut after . ! ?
     * where a new sentence starts, and at line breaks (a list's items, a
     * table's rows). Never inside "e.g. this" or "v0.3.0". A sentence that
     * starts a new line (after the first) starts with "\n", so the screen can
     * keep a list a list while it is read.
     */
    fun sentences(text: String): List<String> {
        val out = ArrayList<String>()
        for (line in text.split("\n")) {
            val l = line.trim()
            if (l.isEmpty()) continue
            var from = 0
            var first = out.isNotEmpty()
            fun add(x: String) { if (x.isNotEmpty()) { out += if (first) "\n" + x else x; first = false } }
            for (m in Regex("[.!?…]+[\"')\\]]*\\s+(?=[\"'(\\[]?[A-Z0-9ÁČĎÉĚÍŇÓŘŠŤÚŮÝŽ])").findAll(l)) {
                add(l.substring(from, m.range.last + 1).trim())
                from = m.range.last + 1
            }
            if (from < l.length) add(l.substring(from).trim())
        }
        // A fragment of a few letters ("A." "1.") read alone is noise: joined
        // to the one before.
        val merged = ArrayList<String>()
        for (x in out.filter { it.isNotBlank() }) {
            if (merged.isNotEmpty() && x.count { it.isLetterOrDigit() } <= 3) merged[merged.size - 1] = merged.last() + " " + x.trim()
            else merged += x
        }
        return merged
    }

    /** The model's text in an event, or null for anything else. */
    fun replyText(e: AgentEvent): String? {
        if (e.kind != "claude" || e.data.optString("type") != "assistant") return null
        val content = e.data.optJSONObject("message")?.optJSONArray("content") ?: return null
        val parts = (0 until content.length()).mapNotNull { content.optJSONObject(it) }
            .filter { it.optString("type") == "text" }.map { it.optString("text").trim() }.filter { it.isNotEmpty() }
        return parts.joinToString("\n\n").ifEmpty { null }
    }

    /**
     * Of [events], the replies to read for a session with auto-play: those
     * after [spoken], the last one already read. Null [spoken] reads nothing —
     * auto-play reads what is new from when it was switched on, never the past.
     */
    fun toRead(events: List<AgentEvent>, spoken: Long?): List<Pair<Long, String>> =
        if (spoken == null) emptyList()
        else events.filter { it.seq > spoken }.mapNotNull { e -> replyText(e)?.let { e.seq to it } }

    // --- auto-play, per session -------------------------------------------------

    private fun prefs(ctx: Context) = ctx.getSharedPreferences("speech", Context.MODE_PRIVATE)
    private fun key(host: String, session: String) = "$host/$session"
    val autoChanged = MutableStateFlow(0L)

    /** Auto-play, and what it has read, follow a session that was renamed. */
    fun rename(ctx: Context, host: String, from: String, to: String) {
        val p = prefs(ctx)
        val e = p.edit()
        for (k in listOf("auto:", "spoken:")) {
            when (val v = p.all[k + key(host, from)]) {
                is Boolean -> e.putBoolean(k + key(host, to), v)
                is Long -> e.putLong(k + key(host, to), v)
            }
            e.remove(k + key(host, from))
        }
        e.apply()
    }

    fun autoPlay(ctx: Context, host: String, session: String): Boolean =
        prefs(ctx).getBoolean("auto:" + key(host, session), false)

    /** Switched on, it reads what comes after [lastSeq], the newest event now. */
    fun setAutoPlay(ctx: Context, host: String, session: String, on: Boolean, lastSeq: Long) {
        val e = prefs(ctx).edit().putBoolean("auto:" + key(host, session), on)
        if (on) e.putLong("spoken:" + key(host, session), lastSeq)
        e.apply()
        if (!on) stop()
        autoChanged.value = autoChanged.value + 1
    }

    @Synchronized
    private fun spoken(ctx: Context, host: String, session: String): Long? =
        prefs(ctx).takeIf { it.contains("spoken:" + key(host, session)) }?.getLong("spoken:" + key(host, session), 0)

    /**
     * New events of a session, from whichever saw them — the screen or the
     * watcher: its new replies are queued once, in order, when auto-play is
     * on. The mark of what was read moves past every event given, so neither
     * reads them again.
     */
    @Synchronized
    fun heard(ctx: Context, host: String, session: String, events: List<AgentEvent>) {
        if (events.isEmpty() || !autoPlay(ctx, host, session)) return
        val read = toRead(events, spoken(ctx, host, session))
        val last = events.maxOf { it.seq }
        if (last > (spoken(ctx, host, session) ?: 0)) prefs(ctx).edit().putLong("spoken:" + key(host, session), last).apply()
        for ((seq, text) in read) say(ctx, "$host/$session/$seq", text, queue = true)
    }

    // --- the engine ---------------------------------------------------------------

    private var tts: TextToSpeech? = null
    private var ready = false
    private val waiting = ArrayList<Triple<String, String, Boolean>>()
    private val queue = ArrayDeque<Pair<String, String>>() // auto-play: (id, markdown) waiting their turn
    private var focus: AudioFocusRequest? = null
    private var appAudio: AudioManager? = null

    /** Reads a reply: now, stopping what was being read, or after it ([queue]). */
    @Synchronized
    fun say(ctx: Context, id: String, markdown: String, queue: Boolean = false) {
        val app = ctx.applicationContext
        appAudio = app.getSystemService(AudioManager::class.java)
        if (tts == null) {
            tts = TextToSpeech(app) { status ->
                synchronized(this) {
                    ready = status == TextToSpeech.SUCCESS
                    if (ready) {
                        tts?.setAudioAttributes(AudioAttributes.Builder()
                            .setUsage(AudioAttributes.USAGE_ASSISTANT)
                            .setContentType(AudioAttributes.CONTENT_TYPE_SPEECH).build())
                        tts?.setOnUtteranceProgressListener(listener)
                        val w = waiting.toList(); waiting.clear()
                        for ((i, t, q) in w) take(i, t, q)
                    } else waiting.clear()
                }
            }
        }
        if (!ready) { waiting += Triple(id, markdown, queue); return }
        take(id, markdown, queue)
    }

    private fun take(id: String, markdown: String, queued: Boolean) {
        if (!queued) { queue.clear(); start(id, markdown); return }
        if (reading.value == null) start(id, markdown) else queue.addLast(id to markdown)
    }

    private fun start(id: String, markdown: String) {
        val text = speakable(markdown)
        val s = sentences(text)
        if (s.isEmpty()) { finished(); return }
        val engine = tts ?: return
        val locale = if (isCzech(text)) Locale("cs", "CZ") else Locale.ENGLISH
        if (engine.isLanguageAvailable(locale) >= TextToSpeech.LANG_AVAILABLE) engine.language = locale
        reading.value = Reading(id, s, 0, false)
        speakFrom(0)
    }

    /** The reply being read, from sentence [i]: each its own utterance. */
    private fun speakFrom(i: Int) {
        val r = reading.value ?: return
        val engine = tts ?: return
        engine.stop()
        requestFocus()
        reading.value = r.copy(index = i, paused = false)
        for (j in i until r.sentences.size) {
            engine.speak(r.sentences[j].trim(), if (j == i) TextToSpeech.QUEUE_FLUSH else TextToSpeech.QUEUE_ADD, Bundle(), "${r.id}#$j")
        }
    }

    @Synchronized fun pause() {
        val r = reading.value ?: return
        if (r.paused) return
        reading.value = r.copy(paused = true)
        tts?.stop()
    }

    /** Resumes at the start of the sentence it was paused in. */
    @Synchronized fun resume() {
        val r = reading.value ?: return
        if (r.paused) speakFrom(r.index)
    }

    /** The next sentence, or the one before ([by] -1); past the end, done. */
    @Synchronized fun skip(by: Int) {
        val r = reading.value ?: return
        val to = r.index + by
        when {
            to >= r.sentences.size -> { tts?.stop(); finished() }
            else -> speakFrom(to.coerceAtLeast(0))
        }
    }

    /** SherpaTTS: offline Piper (and Coqui) voices as a system engine. */
    const val SHERPA = "org.woheller69.ttsengine"

    /**
     * The system's speech engines, and the one it prefers — for the voice
     * section, which says what reads now.
     */
    fun engines(ctx: Context, done: (List<TextToSpeech.EngineInfo>, String?) -> Unit) {
        var probe: TextToSpeech? = null
        probe = TextToSpeech(ctx.applicationContext) { _ ->
            val p = probe ?: return@TextToSpeech
            val list = runCatching { p.engines }.getOrDefault(emptyList())
            val default = runCatching { p.defaultEngine }.getOrNull()
            p.shutdown()
            done(list, default)
        }
    }

    /**
     * Lets go of the engine, so the next reading binds to whichever the system
     * prefers now: chosen in Android's settings, a new one is not picked up by
     * an engine already bound.
     */
    @Synchronized
    fun rebind() {
        stop()
        tts?.shutdown()
        tts = null
        ready = false
    }

    @Synchronized
    fun stop() {
        queue.clear()
        reading.value = null
        tts?.stop()
        focus?.let { f -> appAudio?.abandonAudioFocusRequest(f) }
    }

    /** The reply is read: the next one waiting, or quiet. */
    private fun finished() {
        reading.value = null
        val next = queue.removeFirstOrNull()
        if (next != null) start(next.first, next.second)
        else focus?.let { f -> appAudio?.abandonAudioFocusRequest(f) }
    }

    private fun requestFocus() {
        val am = appAudio ?: return
        val f = focus ?: AudioFocusRequest.Builder(AudioManager.AUDIOFOCUS_GAIN_TRANSIENT_MAY_DUCK)
            .setAudioAttributes(AudioAttributes.Builder().setUsage(AudioAttributes.USAGE_ASSISTANT)
                .setContentType(AudioAttributes.CONTENT_TYPE_SPEECH).build()).build().also { focus = it }
        am.requestAudioFocus(f)
    }

    private val listener = object : UtteranceProgressListener() {
        private fun parse(u: String): Pair<String, Int>? =
            u.lastIndexOf('#').takeIf { it > 0 }?.let { u.substring(0, it) to (u.substring(it + 1).toIntOrNull() ?: return null) }

        override fun onStart(utteranceId: String) {
            val (id, i) = parse(utteranceId) ?: return
            synchronized(Speech) {
                val r = reading.value ?: return
                if (r.id == id && !r.paused) reading.value = r.copy(index = i)
            }
        }

        override fun onDone(utteranceId: String) {
            val (id, i) = parse(utteranceId) ?: return
            synchronized(Speech) {
                val r = reading.value ?: return
                if (r.id == id && !r.paused && i == r.sentences.size - 1) finished()
            }
        }

        @Deprecated("Deprecated in Java") override fun onError(utteranceId: String) = onDone(utteranceId)
        // Stopped by pause, skip or stop, which have already said what happens next.
        override fun onStop(utteranceId: String, interrupted: Boolean) {}
    }
}
