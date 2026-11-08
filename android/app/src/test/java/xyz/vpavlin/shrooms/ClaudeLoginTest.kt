package xyz.vpavlin.shrooms

import org.json.JSONObject
import org.junit.Assert.assertEquals
import org.junit.Assert.assertFalse
import org.junit.Assert.assertNull
import org.junit.Assert.assertTrue
import org.junit.Test

// A machine's Claude Code login as the agent reports it (ADR-081).
class ClaudeLoginTest {
    @Test
    fun aLoggedOutMachineIsOfferedALogin() {
        val o = parseOffer("""{"harnesses":[{"name":"claude","title":"Claude Code","caps":{"approve":true}}],
            "claude_login":{"logged_in":false}}""")
        assertTrue(o.claudeLogin!!.needed)
        assertNull(o.claudeLogin!!.flow)
    }

    @Test
    fun anAgentTooOldToSayOffersNothing() {
        assertNull(parseOffer("""{"harnesses":[]}""").claudeLogin)
    }

    @Test
    fun aStatusThatCouldNotBeReadIsNotALoginNeeded() {
        val l = ClaudeLogin.parse(JSONObject("""{"logged_in":false,"error":"Claude Code is not installed on this machine"}"""))
        assertFalse(l.needed)
    }

    @Test
    fun aLoginInProgress() {
        val l = ClaudeLogin.parse(JSONObject("""{"logged_in":true,"method":"claude.ai","email":"owner@example.org",
            "flow":{"id":"f1","url":"https://claude.com/cai/oauth/authorize?x","state":"done","started":"2026-11-08T10:00:00Z"}}"""))
        assertFalse(l.needed)
        assertEquals("done", l.flow!!.state)
        assertEquals("owner@example.org", l.email)
    }
}
