package xyz.vpavlin.shrooms

import org.json.JSONObject
import org.junit.Assert.assertEquals
import org.junit.Assert.assertNull
import org.junit.Test

/** A session out of quota, as the agent says it (`limited`) and the list tags it. */
class QuotaTest {
    @Test fun aLimitedSessionSaysWhenItComesBack() {
        val l = Limited.parse(JSONObject("""{"reason":"limit reached (5 hours)","until":"2026-10-08T14:20:00+02:00"}"""))!!
        assertEquals("limit reached (5 hours)", l.reason)
        val sameDay = l.until - 3_600_000
        assertEquals("QUOTA · back " + quotaClock(l.until, sameDay), l.label(sameDay))
        assertEquals("QUOTA", Limited("no DIEM left today (venice)").label())
        assertNull(Limited.parse(null))
    }
}
