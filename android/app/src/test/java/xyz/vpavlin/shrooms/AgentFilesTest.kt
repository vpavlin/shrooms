package xyz.vpavlin.shrooms

import org.json.JSONObject
import org.junit.Assert.assertEquals
import org.junit.Assert.assertFalse
import org.junit.Assert.assertTrue
import org.junit.Test

/** Who a session takes files from, as the app edits it (ADR-048). */
class AgentFilesTest {
    @Test fun anEntryIsASessionOrAWholeMachine() {
        assertTrue(AgentFiles.valid("jimmy-crib/vpavlin"))
        assertTrue(AgentFiles.valid(" pi5/* "))
        assertFalse(AgentFiles.valid("jimmy-crib"))
        assertFalse(AgentFiles.valid("a/b/c"))
        assertFalse(AgentFiles.valid("a b/c"))
    }

    @Test fun allowingAddsOnceAndDenyingRemoves() {
        val l = AgentFiles.allow(emptyList(), "pi5/jimmy")
        assertEquals(listOf("pi5/jimmy"), AgentFiles.allow(l, "pi5/jimmy"))
        assertEquals(listOf("pi5/jimmy"), AgentFiles.allow(l, "nonsense"))
        assertEquals(listOf("pi5/jimmy", "atlas/*"), AgentFiles.allow(l, "atlas/*"))
        assertEquals(emptyList<String>(), AgentFiles.deny(l, "pi5/jimmy"))
    }

    // As the agent lists a session: who it takes files from, and who asked.
    @Test fun theSessionCarriesItsFileSettings() {
        val c = JSONObject("""{"accept_files_from":["pi5/*"],"file_requests":[{"from":"atlas/marta","name":"page.html","size":2048}]}""")
        val allowed = c.optJSONArray("accept_files_from")!!.let { a -> (0 until a.length()).map { a.getString(it) } }
        assertEquals(listOf("pi5/*"), allowed)
        assertEquals("2.0 kB", AgentFiles.size(2048))
    }
}
