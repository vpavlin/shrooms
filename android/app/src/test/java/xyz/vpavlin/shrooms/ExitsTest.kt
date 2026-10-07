package xyz.vpavlin.shrooms

import org.junit.Assert.assertEquals
import org.junit.Assert.assertTrue
import org.junit.Test

class ExitsTest {
    // Android's account of how the app ended goes before the long part of
    // the report — memory, announces, the log — so a share that cuts the end
    // off still carries it (three reports in a row lost it, 2026-10-07).
    @Test fun androidsSectionComesBeforeTheLog() {
        val core = "shrooms diagnostics — t\n\n== how it stopped, most recent last ==\nKILLED\n\n== last panic ==\n(none)\n" +
            "\n== memory ==\nresident 1\n\n== recent log ==\n" + "line\n".repeat(1000)
        val exits = "\n== how Android says it ended, most recent first ==\nexit self\n\n== last Kotlin crash ==\n(none)\n"
        val r = Exits.insert(core, exits)
        assertTrue(r.indexOf("== how Android says it ended") in (r.indexOf("== last panic ==") + 1) until r.indexOf("== memory =="))
        assertTrue(r.indexOf("== last Kotlin crash ==") < r.indexOf("== recent log =="))
        assertEquals(core.length + exits.length, r.length)
        // A core report without a memory section: at the end, as before.
        assertEquals("a" + exits, Exits.insert("a", exits))
    }
}
