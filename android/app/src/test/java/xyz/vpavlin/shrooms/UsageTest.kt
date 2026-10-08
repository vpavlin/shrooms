package xyz.vpavlin.shrooms

import org.junit.Assert.assertEquals
import org.junit.Assert.assertTrue
import org.junit.Test

class UsageTest {
    private val laptop = """{"machine":"laptop","rows":[
        {"day":"2026-10-05","session":"shrooms","by":"nothing.office","model":"claude-opus-5[1m]","harness":"claude","turns":3,"input":10,"cache_read":1000,"cache_write":200,"output":900,"cost_usd":1.5,"busy_ms":120000},
        {"day":"2026-10-05","session":"shrooms","by":"","model":"claude-opus-5[1m]","harness":"claude","turns":1,"input":5,"cache_read":0,"cache_write":0,"output":100,"cost_usd":0.5,"busy_ms":60000}]}"""
    private val jimmy = """{"machine":"jimmy-crib","rows":[
        {"day":"2026-10-04","session":"vpavlin","by":"nothing.office","model":"ollama/qwen3","harness":"pi","turns":10,"input":5000,"cache_read":0,"cache_write":0,"output":4000,"cost_usd":0,"busy_ms":3600000}]}"""

    // The machines' rows together: who asked, where it ran, which model.
    @Test fun summedAcrossMachines() {
        val rows = UsageView.parse("laptop", laptop) + UsageView.parse("jimmy-crib", jimmy)
        val who = UsageView.group(rows, UsageView.Measure.OUTPUT, UsageView::device)
        assertEquals(listOf("nothing", "laptop"), who.map { it.name })
        assertEquals(13, who[0].turns)
        assertEquals(4900L, who[0].output)
        assertEquals(1.5, who[0].costUsd, 1e-9)
        assertEquals(5000L + 10 + 1000 + 200, who[0].input)
        // The laptop asking its own agent and asking another are one device.
        val mixed = rows + UsageView.parse("atlas", """{"rows":[{"day":"2026-10-05","by":"laptop.default","turns":2,"output":50}]}""")
        val laptop = UsageView.group(mixed, UsageView.Measure.TURNS, UsageView::device).single { it.name == "laptop" }
        assertEquals(3, laptop.turns)

        val where = UsageView.group(rows, UsageView.Measure.TURNS) { it.machine }
        assertEquals(listOf("jimmy-crib" to 10, "laptop" to 4), where.map { it.name to it.turns })
        // By cost, the local model's free turns come last.
        assertEquals("laptop", UsageView.group(rows, UsageView.Measure.COST) { it.machine }.first().name)
        assertEquals("1h 0m", UsageView.format(where[0], UsageView.Measure.BUSY))
        assertEquals("4.9k", UsageView.count(4900))
    }

    @Test fun periodsStartOnTheRightDay() {
        val today = java.time.LocalDate.of(2026, 10, 5)
        assertEquals("2026-10-05", UsageView.since(1, today))
        assertEquals("2026-09-29", UsageView.since(7, today))
        assertEquals("", UsageView.since(0, today))
    }

    // A subscription's limits, as an agent reports them, once per account:
    // machines whose windows reset at the same moments share one, and the
    // newest reading among them is the one shown.
    @Test fun planLimitsPerAccount() {
        val json = { at: String, five: Double, status: String -> """{"machine":"m","rows":[],"limits":{"at":"$at","status":"$status","window":"five_hour",
            "windows":{"seven_day":{"utilization":0.49,"resets_at":"2026-10-11T11:00:00+02:00"},
                       "five_hour":{"utilization":$five,"resets_at":"2026-10-06T21:20:00+02:00"}}}}""" }
        val laptop = UsageView.parseLimits("laptop", json("2026-10-06T18:21:00+02:00", 0.98, "allowed_warning"))!!
        val atlas = UsageView.parseLimits("atlas", json("2026-10-06T21:04:00+02:00", 1.0, "rejected"))!!
        val other = UsageView.parseLimits("vps", """{"limits":{"at":"2026-10-06T10:00:00Z","status":"allowed",
            "windows":{"five_hour":{"utilization":0.1,"resets_at":"2026-10-06T12:00:00Z"}}}}""")!!
        assertEquals(listOf("five_hour", "seven_day"), laptop.windows.map { it.name })
        val at = java.time.OffsetDateTime.parse("2026-10-06T21:05:00+02:00").toInstant().toEpochMilli()
        val accounts = UsageView.accounts(listOf(laptop, other, atlas), at)
        assertEquals(2, accounts.size)
        val shared = accounts.first()
        assertEquals(listOf("atlas", "laptop"), shared.machines)
        assertEquals("rejected", shared.status)
        assertEquals(1.0, shared.windows.first { it.name == "five_hour" }.utilization, 1e-9)
        assertEquals(listOf("vps"), accounts[1].machines)
        val now = java.time.OffsetDateTime.parse("2026-10-06T21:05:00+02:00").toInstant().toEpochMilli()
        assertTrue(UsageView.status(shared, now).startsWith("limit reached (5 hours) — back at "))
        assertEquals("past 98% of 5 hours", UsageView.status(laptop, now))
        assertEquals("", UsageView.status(other, now))
        assertEquals("7 days", UsageView.windowLabel("seven_day"))
        assertEquals("1 min ago", UsageView.age(now - 60_000, now))
        // An agent with nothing to say, or older than limits: none.
        assertEquals(null, UsageView.parseLimits("x", """{"rows":[]}"""))
        assertEquals(null, UsageView.parseLimits("x", """{"rows":[],"limits":null}"""))
    }

    // The usage link at a glance: the session quota (the 5-hour window) on
    // the busiest account — amber from half, red from 80% or once refused.
    @Test fun glanceAtTheSessionQuota() {
        fun plan(five: Double, status: String = "allowed", seven: Double = 0.3) = PlanLimits(listOf("m"), 0, status, "five_hour", false,
            listOf(PlanWindow("five_hour", five, 1), PlanWindow("seven_day", seven, 2)))
        assertEquals(21 to 0, UsageView.glance(listOf(plan(0.21))))
        assertEquals(50 to 1, UsageView.glance(listOf(plan(0.5))))
        assertEquals(80 to 2, UsageView.glance(listOf(plan(0.8))))
        // The 7-day window past half does not colour it: the session quota does.
        assertEquals(10 to 0, UsageView.glance(listOf(plan(0.1, seven = 0.6))))
        assertEquals(30 to 2, UsageView.glance(listOf(plan(0.3, "rejected"))))
        assertEquals(62 to 1, UsageView.glance(listOf(plan(0.2), plan(0.62))))
        assertEquals(null, UsageView.glance(emptyList()))
        assertEquals(1, UsageView.level(0.5))
        assertEquals(2, UsageView.level(0.8))
        assertEquals(0, UsageView.level(0.49))
    }

    @Test fun aKeySharedByMachinesIsOneEntry() {
        val pi5 = UsageView.parseCredits("pi5", """{"credits":[{"provider":"venice","key":"1a2b3c4d",
            "balances":{"DIEM":5.62,"USD":-0.03,"BUNDLED_CREDITS":0},"resets_at":"2026-10-08T00:00:00Z","at":"2026-10-07T12:00:00Z"}]}""")
        val proteus = UsageView.parseCredits("proteus", """{"credits":[{"provider":"venice","key":"1a2b3c4d",
            "balances":{"DIEM":5.40,"USD":-0.03},"resets_at":"2026-10-08T00:00:00Z","at":"2026-10-07T12:05:00Z"}]}""")
        val keys = UsageView.keys(pi5 + proteus)
        assertEquals(1, keys.size)
        assertEquals(listOf("pi5", "proteus"), keys[0].machines)
        assertEquals("5.40 DIEM left today · USD -0.03", UsageView.creditLine(keys[0]))
        assertEquals(emptyList<Credit>(), UsageView.parseCredits("old", """{"rows":[]}"""))
    }

    @Test fun aReadingWhoseWindowHasResetIsNotItsShare() {
        val now = java.time.OffsetDateTime.parse("2026-10-07T17:00:00+02:00").toInstant().toEpochMilli()
        val atlas = UsageView.parseLimits("atlas", """{"limits":{"at":"2026-10-07T13:00:00+02:00","status":"allowed_warning","window":"five_hour",
            "windows":{"five_hour":{"utilization":0.8,"resets_at":"2026-10-07T15:00:00+02:00"},"seven_day":{"utilization":0.69,"resets_at":"2026-10-11T11:00:00+02:00"}}}}""")!!
        val laptop = UsageView.parseLimits("laptop", """{"limits":{"at":"2026-10-07T16:59:00+02:00","status":"allowed",
            "windows":{"five_hour":{"utilization":0.28,"resets_at":"2026-10-07T20:30:00+02:00"},"seven_day":{"utilization":0.72,"resets_at":"2026-10-11T11:00:00+02:00"}}}}""")!!
        val accounts = UsageView.accounts(listOf(atlas, laptop), now)
        assertEquals(1, accounts.size)
        assertEquals(listOf("atlas", "laptop"), accounts[0].machines)
        assertEquals(0.28, accounts[0].windows[0].utilization, 1e-9)
        val alone = UsageView.accounts(listOf(atlas), now)[0]
        assertTrue(alone.windows[0].renewed)
        assertEquals(0.0, alone.windows[0].utilization, 1e-9)
        assertEquals("", alone.status)
        assertEquals(0, UsageView.glance(listOf(alone))!!.first)
    }

    @Test fun aForecastSaysWhetherItLasts() {
        val now = java.time.OffsetDateTime.parse("2026-10-08T12:00:00+02:00").toInstant().toEpochMilli()
        val l = UsageView.parseLimits("laptop", """{"limits":{"at":"2026-10-08T12:00:00+02:00","status":"allowed",
            "windows":{"five_hour":{"utilization":0.5,"resets_at":"2026-10-08T14:00:00+02:00","projected":1.1,"runs_out_at":"2026-10-08T13:40:00+02:00","pace":"recent"},
                       "seven_day":{"utilization":0.6,"resets_at":"2026-10-11T11:00:00+02:00","projected":0.8,"pace":"recent"}}}}""")!!
        // In this machine's zone, as the app shows it (the build runs in UTC).
        assertEquals("at this pace: runs out " + UsageView.resets(l.windows[0].runsOutAt, now) + " — before it resets",
            UsageView.windowForecast(l.windows[0], now))
        assertTrue(l.windows[0].runsOutAt == java.time.OffsetDateTime.parse("2026-10-08T13:40:00+02:00").toInstant().toEpochMilli())
        assertEquals("at this pace: about 80% at the reset — it lasts", UsageView.windowForecast(l.windows[1], now))
        val c = UsageView.parseCredits("pi5", """{"credits":[{"provider":"venice","key":"e31a16d5","balances":{"DIEM":4},
            "resets_at":"2026-10-09T00:00:00Z","at":"2026-10-08T10:00:00Z","left_at_refill":3.04}]}""")[0]
        assertEquals("at this pace: about 3.0 DIEM left at the refill", UsageView.creditForecast(c, now))
        assertEquals("", UsageView.creditForecast(c.copy(leftAtRefill = -1.0), now))
    }
}
