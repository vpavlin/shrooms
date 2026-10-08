package xyz.vpavlin.shrooms

import org.json.JSONObject
import org.junit.Assert.assertEquals
import org.junit.Assert.assertFalse
import org.junit.Assert.assertNull
import org.junit.Assert.assertTrue
import org.junit.Test

/** Cages on the phone as in Basecamp (docs/agents-in-cages.md): offered where the machine has podman, and asked for. */
class CageTest {
    @Test fun aCageIsOfferedWhereTheMachineHasPodman() {
        val o = parseOffer("""{"harnesses":[{"name":"claude","title":"Claude Code","caps":{"approve":true}}],
            "cage":{"available":true,"image":"localhost/shrooms-workbench:latest","ready":false}}""")
        assertEquals("localhost/shrooms-workbench:latest", o.cage?.image)
        assertTrue(cageNote(o.cage!!).endsWith("The image is built with the first one (a few minutes)."))
        assertNull(parseOffer("""{"harnesses":[],"cage":{"available":false}}""").cage)
        // An agent from before cages, or before harnesses.
        assertNull(parseOffer("""{"harnesses":[]}""").cage)
        assertEquals(claudeOnly, parseOffer("""{}""").harnesses)
    }

    @Test fun aCagedSessionIsAskedForWithTheMachinesImage() {
        val caged = JSONObject(sessionBody(JSONObject().put("name", "box"), true))
        assertEquals(0, caged.getJSONObject("cage").length())
        assertFalse(JSONObject(sessionBody(JSONObject().put("name", "free"), false)).has("cage"))
    }

    @Test fun aCagedSessionIsKeptCagedInTheCache() {
        val s = AgentSession("box", "/p", "idle", 0, false, 0, cage = "localhost/bench:1")
        val back = HostCache.decode(HostCache.encode(listOf(AgentHost("laptop", "office", "fd00::1", listOf(s, s.copy(name = "free", cage = null)), lastSeen = 1))))
        assertEquals("localhost/bench:1", back[0].sessions[0].cage)
        assertNull(back[0].sessions[1].cage)
    }
}
