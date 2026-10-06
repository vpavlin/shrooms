package xyz.vpavlin.shrooms

import org.junit.Assert.assertEquals
import org.junit.Assert.assertTrue
import org.junit.Test
import xyz.vpavlin.shrooms.Markdown.Block

/** The markdown Claude actually writes, as the phone has to show it. */
class MarkdownTest {
    @Test fun blocksOfAReply() {
        val b = Markdown.parse("""
            ## Done

            The fix is **pushed** as `02befc2`.

            - first
            - second
              continued
            1. one
            > a quote

            ```
            sudo make install
            ```
            ---
        """.trimIndent())
        assertEquals(listOf("Heading", "Para", "Item", "Item", "Item", "Quote", "Code", "Rule"),
            b.map { it::class.simpleName })
        assertEquals("sudo make install", (b[6] as Block.Code).text)
        assertEquals("1.", (b[4] as Block.Item).marker)
        assertEquals("second continued", (b[3] as Block.Item).text.joinToString("") { it.text })
    }

    @Test fun inlineStyles() {
        val s = Markdown.inline("a **bold** and *it* `code` [link](http://x)")
        assertTrue(s.any { it.bold && it.text == "bold" })
        assertTrue(s.any { it.italic && it.text == "it" })
        assertTrue(s.any { it.code && it.text == "code" })
        assertTrue(s.any { it.link == "http://x" && it.text == "link" })
    }

    // snake_case, 2*3 and file paths are not emphasis.
    @Test fun underscoresAndStarsInWordsAreText() {
        for (t in listOf("moveEphemeralPorts in local_build.conf", "2*3*4", "~/.config/systemd/user")) {
            val s = Markdown.inline(t)
            assertEquals(t, s.joinToString("") { it.text })
            assertTrue("$t got styled", s.none { it.italic || it.bold })
        }
    }

    @Test fun tables() {
        val b = Markdown.parse("| a | b |\n|---|---|\n| 1 | 2 |\n| 3 | 4 |")
        val t = b.single() as Block.Table
        assertEquals(3, t.rows.size)
        assertEquals("2", t.rows[1][1].single().text)
    }

    // A reply is drawn while it streams: an unclosed fence mid-way must still show.
    @Test fun anUnfinishedCodeFenceIsStillShown() {
        val b = Markdown.parse("Look:\n```\nmake test\n")
        assertEquals("make test", (b.last() as Block.Code).text)
    }
}

class AgentWatchTest {
    private fun s(state: String, seq: Long, preview: String = "") =
        AgentSession("shrooms", "/x", state, if (state == "waiting") 1 else 0, true, seq, preview = preview)

    @Test fun whenToNotify() {
        val seen = { st: String, seq: Long -> AgentWatch.Seen(st, seq) }
        // First sight: remembered, not announced.
        assertEquals(null, AgentWatch.change(null, s("waiting", 5)))
        // Starts waiting.
        assertTrue(AgentWatch.change(seen("working", 4), s("waiting", 5))!!.startsWith("needs you"))
        // Still waiting: said once only.
        assertEquals(null, AgentWatch.change(seen("waiting", 5), s("waiting", 5)))
        // A turn ends with a reply.
        assertEquals("All done.", AgentWatch.change(seen("working", 7), s("idle", 9, "All done.")))
        // Nothing happened.
        assertEquals(null, AgentWatch.change(seen("idle", 9), s("idle", 9, "old")))
        // Still working.
        assertEquals(null, AgentWatch.change(seen("working", 9), s("working", 12)))
        // An old agent: events while idle are not a reply.
        assertEquals(null, AgentWatch.change(seen("idle", 9), s("idle", 14, "same text")))
    }

    // An agent that counts turns: one notification per poll in which turns
    // ended — heartbeats and progress while it waits on background work are
    // not replies (2026-10-04).
    @Test fun turnsNotEventsAreReplies() {
        fun s(state: String, seq: Long, turns: Long, preview: String = "") =
            AgentSession("bv", "/x", state, 0, true, seq, preview = preview, turns = turns)
        val seen = { st: String, seq: Long, turns: Long -> AgentWatch.Seen(st, seq, turns) }
        // Idle between turns, events arriving: nothing.
        assertEquals(null, AgentWatch.change(seen("idle", 100, 7), s("idle", 140, 7, "Running it in the background")))
        // Resumed by itself and still at it: nothing.
        assertEquals(null, AgentWatch.change(seen("idle", 140, 7), s("working", 160, 7, "Running it in the background")))
        // That turn ended: one.
        assertEquals("The build passed.", AgentWatch.change(seen("working", 160, 7), s("idle", 170, 8, "The build passed.")))
        // Two turns between polls, and already working on the next: still one.
        assertEquals("Next.", AgentWatch.change(seen("working", 170, 8), s("working", 200, 10, "Next.")))
        // Needing an answer is still said.
        assertTrue(AgentWatch.change(seen("working", 200, 10), s("waiting", 201, 10))!!.startsWith("needs you"))
    }
}

class AgentListTest {
    private fun ev(seq: Long, kind: String, data: String, time: Long = 0) =
        AgentEvent(seq, kind, "", org.json.JSONObject(data), time)

    // A prompt being answered, or history arriving, must not change the keys of
    // what is already on screen — that is what made the list jump.
    @Test fun keysSurviveAnAnswerAndHistory() {
        val ask = listOf(
            ev(1, "message", """{"text":"go"}""", 1000),
            ev(2, "claude", """{"type":"assistant","message":{"content":[{"type":"text","text":"ok"},{"type":"tool_use","name":"Bash","input":{"command":"ls"}}]}}""", 2000),
            ev(3, "claude", """{"type":"control_request","request_id":"p","request":{"subtype":"can_use_tool","tool_name":"Bash","input":{"command":"ls"}}}""", 3000),
        )
        val before = keysOf(AgentChat.items(ask))
        val after = keysOf(AgentChat.items(ask + ev(4, "answer", """{"prompt":"p","allow":true}"""),
            listOf(Earlier(500, "user", "earlier"))))
        assertEquals(before, after.drop(1).take(before.size))
        assertEquals(before.size, before.toSet().size)
    }

    // History stops where the agent's own events start: the transcript holds the
    // phone's turns too, and showing them twice would be wrong.
    @Test fun historyOnlyBeforeTheFirstEvent() {
        val items = AgentChat.items(
            listOf(ev(1, "message", """{"text":"from the phone"}""", 5000)),
            listOf(Earlier(1000, "user", "in tmux"), Earlier(6000, "user", "from the phone")),
        )
        assertEquals(listOf("Earlier", "You"), items.map { it::class.simpleName })
    }

    @Test fun labels() {
        assertEquals("45% of 1M", contextLabel(453_453, 1_000_000))
        assertEquals("11% of 200k", contextLabel(22_703, 200_000))
        assertEquals("", contextLabel(0, 1_000_000))
        assertEquals("opus-5 1m", shortModel("claude-opus-5[1m]"))
        assertEquals("haiku-4-5", shortModel("claude-haiku-4-5-20251001"))
    }
}

class LinksTest {
    // Bare URLs as Claude writes them, ending a sentence or in brackets.
    // Agents write **https://…**: the closing marks are not part of the link,
    // in markdown or in what was typed.
    @Test fun emphasisAroundAUrlIsNotPartOfIt() {
        val u = "https://github.com/vpavlin/shrooms/blob/master/docs/adr/037-agents-in-cages.md"
        val bold = Markdown.inline("See **$u** now").single { it.link != null }
        assertEquals(u, bold.link)
        assertTrue(bold.bold)
        assertEquals(u, Markdown.links("**$u**").single { it.link != null }.link)
        assertEquals(u, Markdown.links("_${u}_").single { it.link != null }.link)
        assertEquals("https://x.io/a*b", Markdown.links("https://x.io/a*b").single { it.link != null }.link)
    }

    @Test fun bareUrlsBecomeLinksWithoutTheirPunctuation() {
        val s = Markdown.inline("Same link: http://vps.office.mesh:8099/shrooms-preview.apk. Done")
        val link = s.single { it.link != null }
        assertEquals("http://vps.office.mesh:8099/shrooms-preview.apk", link.link)
        assertEquals("Same link: http://vps.office.mesh:8099/shrooms-preview.apk. Done", s.joinToString("") { it.text })
        assertEquals("https://x.org/a_b", Markdown.inline("(see https://x.org/a_b)").single { it.link != null }.link)
    }

    // A URL inside code is code, not a link.
    @Test fun codeIsNotLinked() {
        assertTrue(Markdown.inline("`curl http://x`").none { it.link != null })
    }

    // What the user typed: links, and a * is just a *.
    @Test fun typedTextKeepsItsStars() {
        val s = Markdown.links("2*3 is **not** bold, see http://a.b")
        assertEquals("2*3 is **not** bold, see http://a.b", s.joinToString("") { it.text })
        assertTrue(s.none { it.bold })
        assertEquals("http://a.b", s.last().link)
    }

    @Test fun attachmentsAreNamedByPath() {
        assertEquals("look\n\nAttached from my phone (on this machine):\n- /a/1.png\n- /a/2.pdf",
            withAttachments("look", listOf("/a/1.png", "/a/2.pdf")))
        assertEquals("Attached from my phone (on this machine):\n- /a/1.png", withAttachments("", listOf("/a/1.png")))
        assertEquals("plain", withAttachments("plain", emptyList()))
    }
}

/**
 * Shrooms Agents is no mesh client, so the shrooms app hands it the peers it
 * can reach when it opens it. What goes over and what comes back.
 */
class PeerHandoverTest {
    private fun peer(name: String, mesh: String, overlay: String, online: Boolean) =
        Peer(name, "$name.$mesh.mesh", overlay, "", online, online, false, false, 0, 0, 0, 0, 0.0, mesh = mesh)

    @Test fun onlineMeshPeersAreHandedOverAndReadBack() {
        val s = peersForAgents(listOf(
            peer("laptop", "office", "fdb0:9afc:a5ef:388c:8264:7716:36fc:64eb", true),
            peer("k11", "home", "fd7b:15fb:5ec1:fc97:20a3:9b5c:9fc4:7348", false),
            peer("pi5", "office", "", true),
        ))
        assertEquals("laptop|office|fdb0:9afc:a5ef:388c:8264:7716:36fc:64eb", s)
        assertEquals(listOf(AgentHosts.Host("laptop", "office", "fdb0:9afc:a5ef:388c:8264:7716:36fc:64eb")), parsePeers(s))
    }

    // What arrives in an intent is not trusted to point inside the tunnel.
    @Test fun anAddressOutsideTheMeshIsDropped() {
        assertEquals(listOf("ok"), parsePeers("evil|x|128.140.55.128;ok|office|fd00::1;bad|x;|||").map { it.name })
        assertTrue(parsePeers("").isEmpty())
    }
}

class TakeOverTest {
    @Test fun aSessionIsNamedAfterItsDirectoryAndNeverCollides() {
        assertEquals("logos-vpn", sessionNameFor("/home/x/devel/logos-vpn", emptyList()))
        assertEquals("logos-vpn-2", sessionNameFor("/home/x/devel/logos-vpn/", listOf("logos-vpn")))
        assertEquals("logos-vpn-3", sessionNameFor("/home/x/logos-vpn", listOf("logos-vpn", "logos-vpn-2")))
        assertEquals("my-project", sessionNameFor("/home/x/my project", emptyList()))
        assertEquals("conversation", sessionNameFor("", emptyList()))
    }
}

class DeleteSessionTest {
    // Says the conversation survives, and warns when something is cut off.
    @Test fun theDialogSaysWhatIsLostAndWhatIsNot() {
        val idle = deleteSessionText("idle")
        assertTrue(idle.contains("conversation itself is kept"))
        assertTrue(!idle.contains("cut off") && !idle.contains("permission prompt"))
        assertTrue(deleteSessionText("working").contains("cut off"))
        assertTrue(deleteSessionText("waiting").contains("permission prompt"))
    }
}
