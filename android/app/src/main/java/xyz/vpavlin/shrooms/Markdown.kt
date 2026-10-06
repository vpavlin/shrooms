package xyz.vpavlin.shrooms

/**
 * Just enough Markdown for what Claude writes: headings, paragraphs, bullet and
 * numbered lists, block quotes, fenced code, tables, rules, and inline bold,
 * italic, code and links. Parsed into plain data here — no Compose — so the
 * rules are tested off-device (MarkdownTest); [MarkdownText] draws it.
 *
 * Deliberately not a CommonMark implementation: an unusual construct falls
 * back to being shown as the text it is, which on a phone is better than a
 * dependency.
 */
object Markdown {
    sealed class Block {
        data class Heading(val level: Int, val text: List<Span>) : Block()
        data class Para(val text: List<Span>) : Block()
        /** One list item; [indent] counts nesting levels, [marker] is "•" or "3.". */
        data class Item(val indent: Int, val marker: String, val text: List<Span>) : Block()
        data class Quote(val text: List<Span>) : Block()
        data class Code(val lang: String, val text: String) : Block()
        /** Rows of cells; the separator row is dropped, the first row is the header. */
        data class Table(val rows: List<List<List<Span>>>) : Block()
        data object Rule : Block()
    }

    data class Span(
        val text: String,
        val bold: Boolean = false,
        val italic: Boolean = false,
        val code: Boolean = false,
        val link: String? = null,
    )

    private val fence = Regex("""^\s*(```|~~~)\s*([\w+-]*)\s*$""")
    private val heading = Regex("""^(#{1,6})\s+(.*?)\s*#*\s*$""")
    private val bullet = Regex("""^(\s*)[-*+]\s+(.*)$""")
    private val numbered = Regex("""^(\s*)(\d{1,3})[.)]\s+(.*)$""")
    private val rule = Regex("""^\s*([-*_])(\s*\1){2,}\s*$""")
    private val tableSep = Regex("""^\s*\|?\s*:?-{2,}:?\s*(\|\s*:?-{2,}:?\s*)*\|?\s*$""")

    fun parse(src: String): List<Block> {
        val lines = src.replace("\r\n", "\n").split('\n')
        val out = mutableListOf<Block>()
        val para = StringBuilder()
        fun flush() {
            if (para.isNotBlank()) out += Block.Para(inline(para.toString().trim()))
            para.clear()
        }
        var i = 0
        while (i < lines.size) {
            val line = lines[i]
            val f = fence.matchEntire(line)
            if (f != null) {
                flush()
                val close = f.groupValues[1]
                val body = mutableListOf<String>()
                i++
                while (i < lines.size && lines[i].trim() != close) { body += lines[i]; i++ }
                // Trailing blank lines are a reply still streaming, not code.
                out += Block.Code(f.groupValues[2], body.dropLastWhile { it.isBlank() }.joinToString("\n"))
                i++ // the closing fence, or past the end if it never came
                continue
            }
            if (line.isBlank()) { flush(); i++; continue }
            // A table is a row of pipes followed by a separator row.
            if (line.contains('|') && i + 1 < lines.size && tableSep.matches(lines[i + 1])) {
                flush()
                val rows = mutableListOf(cells(line))
                i += 2
                while (i < lines.size && lines[i].contains('|') && lines[i].isNotBlank()) { rows += cells(lines[i]); i++ }
                out += Block.Table(rows)
                continue
            }
            val h = heading.matchEntire(line)
            when {
                h != null -> { flush(); out += Block.Heading(h.groupValues[1].length, inline(h.groupValues[2])) }
                rule.matches(line) -> { flush(); out += Block.Rule }
                bullet.matches(line) -> {
                    flush()
                    val m = bullet.matchEntire(line)!!
                    out += Block.Item(m.groupValues[1].length / 2, "•", inline(m.groupValues[2]))
                }
                numbered.matches(line) -> {
                    flush()
                    val m = numbered.matchEntire(line)!!
                    out += Block.Item(m.groupValues[1].length / 2, m.groupValues[2] + ".", inline(m.groupValues[3]))
                }
                line.trimStart().startsWith(">") -> {
                    flush()
                    out += Block.Quote(inline(line.trimStart().removePrefix(">").trim()))
                }
                // A continuation of the list item above it.
                line.startsWith("  ") && out.lastOrNull() is Block.Item && para.isEmpty() -> {
                    val last = out.removeAt(out.size - 1) as Block.Item
                    out += last.copy(text = last.text + Span(" ") + inline(line.trim()))
                }
                else -> { if (para.isNotEmpty()) para.append(' '); para.append(line.trim()) }
            }
            i++
        }
        flush()
        return out
    }

    private fun cells(row: String): List<List<Span>> =
        row.trim().removePrefix("|").removeSuffix("|").split('|').map { inline(it.trim()) }

    // Not ending in punctuation, nor in markdown's emphasis marks: an agent
    // writes **https://…** and the closing ** was taken into the link.
    private val bareUrl = Regex("""https?://[^\s<>()\[\]`"']+[^\s<>()\[\]`"'.,;:!?*_~]""")

    /**
     * Plain text with its bare URLs made links, and nothing else touched — for
     * what the user typed, where a * is just a *.
     */
    fun links(s: String): List<Span> {
        val out = mutableListOf<Span>()
        var at = 0
        for (m in bareUrl.findAll(s)) {
            if (m.range.first > at) out += Span(s.substring(at, m.range.first))
            out += Span(m.value, link = m.value)
            at = m.range.last + 1
        }
        if (at < s.length) out += Span(s.substring(at))
        return out
    }

    /** Bold, italic, code and links within one line of text. */
    fun inline(s: String): List<Span> = inlineStyled(s).flatMap { sp ->
        // Bare URLs in plain runs become links too: Claude writes them that way.
        if (sp.code || sp.link != null) listOf(sp)
        else links(sp.text).map { it.copy(bold = sp.bold, italic = sp.italic) }
    }

    private fun inlineStyled(s: String): List<Span> {
        val out = mutableListOf<Span>()
        val buf = StringBuilder()
        var bold = false
        var italic = false
        fun emit() {
            if (buf.isNotEmpty()) { out += Span(buf.toString(), bold = bold, italic = italic); buf.clear() }
        }
        var i = 0
        while (i < s.length) {
            val c = s[i]
            when {
                c == '\\' && i + 1 < s.length -> { buf.append(s[i + 1]); i += 2 }
                c == '`' -> {
                    val end = s.indexOf('`', i + 1)
                    if (end < 0) { buf.append(c); i++ } else {
                        emit(); out += Span(s.substring(i + 1, end), code = true); i = end + 1
                    }
                }
                (c == '*' || c == '_') && i + 1 < s.length && s[i + 1] == c -> {
                    emit(); bold = !bold; i += 2
                }
                // A single * or _ toggles italic only where it can open or close
                // a run: snake_case and 2*3 stay as written.
                (c == '*' || c == '_') && italicMark(s, i, italic) -> {
                    emit(); italic = !italic; i++
                }
                c == '[' -> {
                    val close = s.indexOf("](", i)
                    val end = if (close > 0) s.indexOf(')', close) else -1
                    if (close < 0 || end < 0) { buf.append(c); i++ } else {
                        emit()
                        out += Span(s.substring(i + 1, close), bold = bold, italic = italic, link = s.substring(close + 2, end))
                        i = end + 1
                    }
                }
                else -> { buf.append(c); i++ }
            }
        }
        emit()
        return out
    }

    private fun italicMark(s: String, i: Int, open: Boolean): Boolean {
        val before = s.getOrNull(i - 1)
        val after = s.getOrNull(i + 1)
        return if (!open) after != null && !after.isWhitespace() && (before == null || !before.isLetterOrDigit())
        else before != null && !before.isWhitespace() && (after == null || !after.isLetterOrDigit())
    }
}
