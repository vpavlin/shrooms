package xyz.vpavlin.shrooms

import org.json.JSONObject

/**
 * One line of a conversation as the phone shows it, built from the session's
 * events. Pure, so the rules — which prompts are still open, what is shown and
 * what is noise — are tested without a device (AgentChatTest).
 */
sealed class ChatItem {
    abstract val seq: Long
    /** Epoch millis; 0 when unknown. */
    open val time: Long get() = 0

    /** Said before the agent had this conversation, from the transcript. */
    data class Earlier(override val time: Long, val user: Boolean, val text: String) : ChatItem() {
        override val seq: Long get() = 0
    }

    /** A setting changed: "auto-approve on · from nothing.home". */
    data class Note(override val seq: Long, override val time: Long, val text: String) : ChatItem()

    /** A turn somebody sent, and from which device; [voice] if it was said, not typed. */
    data class You(override val seq: Long, override val time: Long, val text: String, val by: String,
                   val voice: Boolean = false) : ChatItem()

    /**
     * A voice note on its way to being a turn: transcribing on the agent's
     * machine, or [failed] there — kept, so it can be transcribed again.
     */
    data class Voice(override val seq: Long, override val time: Long, val id: String, val by: String,
                     val failed: Boolean, val error: String) : ChatItem()

    /** What the model said. */
    data class Said(override val seq: Long, override val time: Long, val text: String) : ChatItem()

    /** A tool it used, summarised: "Bash  make test". */
    data class Tool(override val seq: Long, override val time: Long, val name: String, val summary: String) : ChatItem()

    /** What the tool returned, folded by default. */
    data class Output(override val seq: Long, override val time: Long, val text: String, val error: Boolean) : ChatItem()

    /**
     * A permission prompt. [open] while it can still be answered: not yet
     * answered, and its process has not ended since.
     */
    data class Prompt(
        override val seq: Long,
        override val time: Long,
        val id: String,
        val tool: String,
        val summary: String,
        val description: String,
        val open: Boolean,
        val answer: String,
        /** Not empty when the model is asking something (AskUserQuestion), not asking leave. */
        val questions: List<Question> = emptyList(),
    ) : ChatItem()

    /** One question of an AskUserQuestion, with the options offered. */
    data class Question(val question: String, val header: String, val multi: Boolean, val options: List<Option>)
    data class Option(val label: String, val description: String)

    /** A turn ended: how, and what it cost. */
    data class Done(override val seq: Long, override val time: Long, val ok: Boolean, val note: String) : ChatItem()

    /** The process stopped; the conversation continues on the next message. */
    data class Stopped(override val seq: Long, override val time: Long, val reason: String) : ChatItem()
}

object AgentChat {
    /**
     * Where the first item of event [seq] is in the conversation's list, which
     * is laid out from the bottom with the live row as item 0; null while that
     * event is not loaded.
     */
    fun listIndexOf(items: List<ChatItem>, seq: Long): Int? {
        val i = items.indexOfFirst { it !is ChatItem.Earlier && it.seq == seq }
        return if (i < 0) null else items.size - i
    }

    /**
     * How many events to open at so that [seq] is among them, with a few
     * before it for context: never fewer than [current], and 0 (everything)
     * stays everything.
     */
    /**
     * Whether a conversation's opening replay — for one with no copy kept on
     * the phone — has caught up, and is shown: once it reaches [target], the
     * session's newest event as last listed. A pause is not taken for the
     * end: over the mesh the replay comes in bursts, and shown at the first
     * pause it was rebuilt under the reader. Only a long silence (about ten
     * seconds of rounds) shows what there is — the target may be newer than
     * anything this replay will hold, or unknown.
     */
    fun replayCaughtUp(lastSeq: Long?, target: Long, quietRounds: Int): Boolean =
        lastSeq != null && ((target > 0 && lastSeq >= target) || quietRounds >= 80)

    fun tailReaching(current: Int, lastSeq: Long, seq: Long): Int =
        if (current == 0) 0 else maxOf(current.toLong(), lastSeq - seq + 1 + 20).toInt()

    /**
     * The conversation to show: [earlier] lines from the transcript that come
     * before the first event this agent kept — the rest of the transcript is
     * the same conversation the events already show — then the events.
     */
    fun items(events: List<AgentEvent>, earlier: List<Earlier>): List<ChatItem> {
        // Only above the session's own first event: above a later one — the
        // last few hundred loaded, a short copy kept on the phone — the
        // transcript's turns from after the agent took it over would be
        // shown again, out of place, as "earlier".
        if ((events.firstOrNull()?.seq ?: 0) > 1) return items(events)
        val first = events.firstOrNull { it.time > 0 }?.time ?: Long.MAX_VALUE
        return earlier.filter { it.time in 1 until first }
            .map { ChatItem.Earlier(it.time, it.role == "user", it.text) } + items(events)
    }

    fun items(events: List<AgentEvent>): List<ChatItem> {
        // Which prompts are settled, and how: answered, or orphaned by the
        // process ending before anybody answered.
        val answers = mutableMapOf<String, String>()
        var lastStop = -1L
        for (e in events) {
            when (e.kind) {
                "answer" -> answers[e.data.optString("prompt")] = e.data.optJSONObject("answers")
                    ?.let { a -> "answered: " + a.keys().asSequence().map { a.optString(it) }.joinToString("; ") + by(e) }
                    ?: if (e.data.optBoolean("allow")) "allowed${by(e)}" else "denied${by(e)}"
                "stopped" -> lastStop = e.seq
            }
        }

        // A voice note shows until its turn arrives, as its latest state.
        val sent = events.filter { it.kind == "message" }.map { it.data.optString("id") }.filter { it.isNotEmpty() }.toSet()
        val lastVoice = mutableMapOf<String, Long>()
        for (e in events) if (e.kind == "voice") lastVoice[e.data.optString("id")] = e.seq

        val out = mutableListOf<ChatItem>()
        for (e in events) {
            when (e.kind) {
                "message" -> out += ChatItem.You(e.seq, e.time, e.data.optString("text"), e.by,
                    voice = e.data.optString("voice").isNotEmpty())
                "voice" -> {
                    val id = e.data.optString("id")
                    if (id !in sent && lastVoice[id] == e.seq) {
                        out += ChatItem.Voice(e.seq, e.time, id, e.by, e.data.optString("status") == "failed",
                            e.data.optString("error"))
                    }
                }
                "stopped" -> out += ChatItem.Stopped(e.seq, e.time, e.data.optString("reason"))
                "restarted" -> out += ChatItem.Note(e.seq, e.time, "restarted${by(e)}")
                "setting" -> if (e.data.has("auto_approve")) {
                    out += ChatItem.Note(e.seq, e.time,
                        (if (e.data.optBoolean("auto_approve")) "auto-approve on" else "auto-approve off") + by(e))
                }
                "claude" -> claude(e, answers, lastStop, out)
            }
        }
        return out
    }

    private fun by(e: AgentEvent) = if (e.by.isNotEmpty()) " from ${e.by}" else ""

    private fun claude(e: AgentEvent, answers: Map<String, String>, lastStop: Long, out: MutableList<ChatItem>) {
        val d = e.data
        when (d.optString("type")) {
            "assistant" -> {
                val content = d.optJSONObject("message")?.optJSONArray("content") ?: return
                for (i in 0 until content.length()) {
                    val c = content.getJSONObject(i)
                    when (c.optString("type")) {
                        "text" -> c.optString("text").takeIf { it.isNotBlank() }
                            ?.let { out += ChatItem.Said(e.seq, e.time, it.trim()) }
                        "tool_use" -> out += ChatItem.Tool(
                            e.seq, e.time, c.optString("name"), summarise(c.optJSONObject("input")),
                        )
                    }
                }
            }
            "user" -> {
                // Tool results come back as a "user" message from Claude Code.
                val content = d.optJSONObject("message")?.optJSONArray("content") ?: return
                for (i in 0 until content.length()) {
                    val c = content.optJSONObject(i) ?: continue
                    if (c.optString("type") != "tool_result") continue
                    out += ChatItem.Output(e.seq, e.time, resultText(c.opt("content")), c.optBoolean("is_error"))
                }
            }
            "control_request" -> {
                val r = d.optJSONObject("request") ?: return
                if (r.optString("subtype") != "can_use_tool") return
                val id = d.optString("request_id")
                val answer = answers[id]
                    ?: if (lastStop > e.seq) "the session stopped before it was answered" else ""
                out += ChatItem.Prompt(
                    seq = e.seq,
                    time = e.time,
                    id = id,
                    tool = r.optString("tool_name"),
                    summary = summarise(r.optJSONObject("input")),
                    description = r.optString("description"),
                    open = answer.isEmpty(),
                    answer = answer,
                    questions = if (r.optString("tool_name") == QUESTION_TOOL) questions(r.optJSONObject("input")) else emptyList(),
                )
            }
            "result" -> {
                val sub = d.optString("subtype")
                val cost = d.optDouble("total_cost_usd", Double.NaN)
                val note = buildString {
                    append(if (sub == "success") "done" else sub.replace('_', ' '))
                    if (!cost.isNaN()) append("  ·  $" + String.format("%.3f", cost))
                }
                out += ChatItem.Done(e.seq, e.time, sub == "success", note)
            }
        }
    }

    /**
     * A tool's input in one line: the command for Bash, the path for file
     * tools, the pattern for searches — what you would want to see before
     * saying yes.
     */
    fun summarise(input: JSONObject?): String {
        if (input == null) return ""
        // AskUserQuestion: what it asks, not its JSON.
        input.optJSONArray("questions")?.let { qs ->
            return (0 until qs.length()).mapNotNull { qs.optJSONObject(it)?.optString("question") }.joinToString("  ·  ")
        }
        for (k in listOf("command", "file_path", "pattern", "path", "url", "query", "description", "prompt")) {
            val v = input.optString(k)
            if (v.isNotEmpty()) return v
        }
        return input.toString().take(200)
    }

    const val QUESTION_TOOL = "AskUserQuestion"

    fun questions(input: JSONObject?): List<ChatItem.Question> {
        val qs = input?.optJSONArray("questions") ?: return emptyList()
        return (0 until qs.length()).mapNotNull { qs.optJSONObject(it) }.map { q ->
            val os = q.optJSONArray("options")
            ChatItem.Question(
                q.optString("question"), q.optString("header"), q.optBoolean("multiSelect"),
                (0 until (os?.length() ?: 0)).mapNotNull { os!!.optJSONObject(it) }
                    .map { ChatItem.Option(it.optString("label"), it.optString("description")) },
            )
        }
    }

    /**
     * The answers to send for a question card: per question, the options
     * picked (in the order offered, joined by ", " as Claude Code does) or, in
     * their place, what was typed. Null until every question has one.
     */
    fun answersFor(questions: List<ChatItem.Question>, picked: Map<String, Set<String>>, typed: Map<String, String>): Map<String, String>? {
        val out = mutableMapOf<String, String>()
        for (q in questions) {
            val t = typed[q.question]?.trim().orEmpty()
            val p = q.options.map { it.label }.filter { it in picked[q.question].orEmpty() }
            out[q.question] = when {
                t.isNotEmpty() -> t
                p.isNotEmpty() -> p.joinToString(", ")
                else -> return null
            }
        }
        return out
    }

    private fun resultText(content: Any?): String = when (content) {
        is String -> content
        is org.json.JSONArray -> (0 until content.length()).joinToString("\n") {
            content.optJSONObject(it)?.optString("text") ?: ""
        }
        else -> ""
    }
}
