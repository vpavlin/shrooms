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
        val caged = JSONObject(sessionBody(JSONObject().put("name", "box"), SessionCage("")))
        assertEquals(0, caged.getJSONObject("cage").length())
        assertFalse(JSONObject(sessionBody(JSONObject().put("name", "free"), null)).has("cage"))
        val desk = JSONObject(sessionBody(JSONObject(), SessionCage("localhost/shrooms-workbench:desktop", nix = true))).getJSONObject("cage")
        assertEquals("localhost/shrooms-workbench:desktop", desk.getString("image"))
        assertTrue(desk.getBoolean("nix") && !desk.has("github"))
    }

    @Test fun aCagedSessionIsKeptCagedInTheCache() {
        val s = AgentSession("box", "/p", "idle", 0, false, 0, cage = SessionCage("localhost/bench:1", github = true))
        val back = HostCache.decode(HostCache.encode(listOf(AgentHost("laptop", "office", "fd00::1", listOf(s, s.copy(name = "free", cage = null)), lastSeen = 1))))
        assertEquals(SessionCage("localhost/bench:1", github = true), back[0].sessions[0].cage)
        assertNull(back[0].sessions[1].cage)
        // As 0.37.0 kept it: the image alone.
        val old = """[{"name":"laptop","mesh":"office","address":"fd00::1","sessions":[{"name":"box","cage":"localhost/bench:1"}]}]"""
        assertEquals(SessionCage("localhost/bench:1"), HostCache.decode(old)[0].sessions[0].cage)
    }

    @Test fun aMoveIsAskedForAndNoted() {
        assertEquals("""{"cage":null}""", cageBody(null))
        assertEquals("""{"cage":{"image":"localhost/shrooms-workbench:desktop","github":true}}""",
            cageBody(SessionCage("localhost/shrooms-workbench:desktop", github = true)))
        assertEquals("moved into a cage (desktop, GitHub login)",
            cagedNote(JSONObject("""{"caged":true,"image":"localhost/shrooms-workbench:desktop","github":true}""")))
        assertEquals("taken out of its cage", cagedNote(JSONObject("""{"caged":false}""")))
        val o = parseOffer("""{"harnesses":[],"cage":{"available":true,"image":"a","images":["a","b"],"nix":true}}""")
        assertEquals(listOf("a", "b"), o.cage?.images)
        assertTrue(o.cage!!.nix)
    }
}
