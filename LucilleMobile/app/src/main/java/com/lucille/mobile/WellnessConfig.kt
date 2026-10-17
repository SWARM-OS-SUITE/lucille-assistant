package com.lucille.mobile

import android.content.Context
import org.json.JSONArray
import org.json.JSONObject
import java.io.File

/**
 * The wellness settings the bundled server reads from its config.json:
 * medication times, wake and bed time, water goal. They decide what the
 * reminders nag about (care.go). Other keys already in the file are kept.
 */
object WellnessConfig {
    data class Values(val wake: String, val bed: String, val waterGoalMl: Int, val meds: String)

    fun file(ctx: Context): File = File(ctx.filesDir, "config.json")

    fun read(ctx: Context): Values {
        val j = try { JSONObject(file(ctx).readText()) } catch (_: Exception) { JSONObject() }
        val meds = j.optJSONArray("meds") ?: JSONArray()
        val lines = (0 until meds.length()).joinToString("\n") {
            val m = meds.getJSONObject(it)
            "${m.optString("name")} ${m.optString("at")}"
        }
        return Values(
            wake = j.optString("wake_at", "08:00"),
            bed = j.optString("bed_at", "00:30"),
            waterGoalMl = j.optInt("water_goal_ml", 2500),
            meds = lines,
        )
    }

    /** Returns null when saved, otherwise what to fix. One medication per line: "Morning meds 09:00". */
    fun write(ctx: Context, wake: String, bed: String, waterGoal: String, medsText: String): String? {
        val time = Regex("^([01]\\d|2[0-3]):[0-5]\\d$")
        if (!time.matches(wake.trim())) return "Wake time must look like 08:00."
        if (!time.matches(bed.trim())) return "Bed time must look like 00:30."
        val goal = waterGoal.trim().toIntOrNull()
        if (goal == null || goal !in 500..8000) return "Water goal must be between 500 and 8000 ml."
        val meds = JSONArray()
        for (line in medsText.lines().map { it.trim() }.filter { it.isNotEmpty() }) {
            val m = Regex("^(.+?)\\s+(\\d{2}:\\d{2})$").find(line)
                ?: return "Medication lines look like: Morning meds 09:00"
            if (!time.matches(m.groupValues[2])) return "\"${m.groupValues[2]}\" isn't a time."
            meds.put(JSONObject().put("name", m.groupValues[1]).put("at", m.groupValues[2]))
        }
        val j = try { JSONObject(file(ctx).readText()) } catch (_: Exception) { JSONObject() }
        j.put("wake_at", wake.trim()).put("bed_at", bed.trim()).put("water_goal_ml", goal).put("meds", meds)
        file(ctx).writeText(j.toString(2))
        return null
    }
}
