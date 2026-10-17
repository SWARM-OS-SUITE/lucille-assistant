package com.lucille.app

import android.util.Log
import kotlinx.coroutines.Dispatchers
import kotlinx.coroutines.withContext
import org.json.JSONArray
import org.json.JSONObject
import java.net.HttpURLConnection
import java.net.URL
import java.net.URLEncoder

// ─── Talking to Lucille Assistant on the PC ──────────────────────────────────

/** PC addresses to try, in order: the one set in Config first, then the usual ones. */
internal fun lucilleBases(serverBase: String): List<String> = buildList {
    if (serverBase.isNotBlank()) add(serverBase.trim().trimEnd('/'))
    add("http://127.0.0.1:8403")
    add("http://100.82.1.19:8403") // this PC on Tailscale
    add("http://10.0.2.2:8403")
}.distinct()

/** What the PC answered: the HTTP status and its JSON body (null when the body was not JSON). */
data class ApiReply(val code: Int, val json: JSONObject?)

/**
 * Sends one request to the first PC address that answers at all. An HTTP error still counts as
 * an answer (the PC is there, it just said no), so only connection failures move on to the next
 * address. Returns null when none could be reached.
 */
suspend fun lucilleRequest(
    serverBase: String,
    token: String,
    method: String,
    path: String,
    body: JSONObject? = null
): ApiReply? = withContext(Dispatchers.IO) {
    for (base in lucilleBases(serverBase)) {
        try {
            val conn = URL(base + path).openConnection() as HttpURLConnection
            try {
                conn.requestMethod = method
                conn.connectTimeout = 3500
                conn.readTimeout = 15000
                if (token.isNotBlank()) conn.setRequestProperty("X-Swarm-Auth", token)
                conn.setRequestProperty("Accept", "application/json")
                if (body != null) {
                    conn.setRequestProperty("Content-Type", "application/json")
                    conn.doOutput = true
                    conn.outputStream.use { it.write(body.toString().toByteArray(Charsets.UTF_8)) }
                }
                val code = conn.responseCode
                val stream = if (code in 200..299) conn.inputStream else conn.errorStream
                val text = stream?.bufferedReader()?.use { it.readText() }.orEmpty()
                val json = try { JSONObject(text) } catch (_: Exception) { null }
                return@withContext ApiReply(code, json)
            } finally {
                conn.disconnect()
            }
        } catch (e: Exception) {
            Log.w("LucilleApi", "$method $base$path: ${e.message}")
        }
    }
    null
}

sealed class Outcome<out T> {
    data class Ok<T>(val value: T) : Outcome<T>()
    data class Fail(val message: String) : Outcome<Nothing>()
}

/** Turns a reply into the JSON of a successful call, or a message that is fit to show the user. */
private fun ApiReply?.parse(): Outcome<JSONObject> {
    if (this == null) {
        return Outcome.Fail("Can't reach your PC. Check the address in Config and that Lucille Assistant is running.")
    }
    val j = json
    if (code == 404 && j == null) {
        return Outcome.Fail("Your PC is running an older Lucille Assistant with no briefings. Update it to 1.1 or newer.")
    }
    if (code in 200..299 && j != null && j.optBoolean("ok")) return Outcome.Ok(j)
    val msg = j?.optString("error").orEmpty()
    return Outcome.Fail(
        when {
            msg.isNotBlank() -> msg
            code == 401 -> "The PC refused this app's token."
            else -> "The PC answered HTTP $code."
        }
    )
}

// ─── Briefings ───────────────────────────────────────────────────────────────

data class Briefing(
    val id: String,
    val title: String,
    val project: String,
    val summary: String,
    val body: String,
    val readFirst: List<String>
)

private fun JSONObject.toBriefing() = Briefing(
    id = optString("id"),
    title = optString("title"),
    project = optString("project"),
    summary = optString("summary"),
    body = optString("body"),
    readFirst = optJSONArray("read_first")?.let { a -> List(a.length()) { a.optString(it) } }.orEmpty()
)

suspend fun fetchBriefings(serverBase: String, token: String): Outcome<List<Briefing>> =
    when (val r = lucilleRequest(serverBase, token, "GET", "/api/briefings").parse()) {
        is Outcome.Ok -> Outcome.Ok(
            r.value.optJSONArray("briefings")?.let { a -> List(a.length()) { a.getJSONObject(it).toBriefing() } }.orEmpty()
        )
        is Outcome.Fail -> r
    }

/** Creates a briefing when [existingId] is null, otherwise replaces that briefing's text. */
suspend fun saveBriefing(serverBase: String, token: String, existingId: String?, b: Briefing): Outcome<Briefing> {
    val body = JSONObject()
        .put("title", b.title)
        .put("project", b.project)
        .put("summary", b.summary)
        .put("body", b.body)
        .put("read_first", JSONArray(b.readFirst))
    val reply = if (existingId == null) {
        lucilleRequest(serverBase, token, "POST", "/api/briefings", body)
    } else {
        lucilleRequest(serverBase, token, "PUT", "/api/briefings/" + URLEncoder.encode(existingId, "UTF-8"), body)
    }
    return when (val r = reply.parse()) {
        is Outcome.Ok -> Outcome.Ok(r.value.getJSONObject("briefing").toBriefing())
        is Outcome.Fail -> r
    }
}

suspend fun deleteBriefing(serverBase: String, token: String, id: String): Outcome<Unit> =
    when (val r = lucilleRequest(serverBase, token, "DELETE", "/api/briefings/" + URLEncoder.encode(id, "UTF-8")).parse()) {
        is Outcome.Ok -> Outcome.Ok(Unit)
        is Outcome.Fail -> r
    }

// ─── PC actions ──────────────────────────────────────────────────────────────
// A fixed allowlist the PC itself defines; the phone can only pick one by id and
// flip a confirm flag. See actions.go for why this is not an arbitrary command.

data class ActionItem(
    val id: String,
    val label: String,
    val description: String,
    val command: String,
    val sudo: Boolean,
    val destructive: Boolean
)

data class ActionRunResult(
    val ok: Boolean,
    val needsConfirm: Boolean,
    val exitCode: Int?,
    val output: String,
    val error: String
)

private fun JSONObject.toActionItem() = ActionItem(
    id = optString("id"), label = optString("label"), description = optString("description"),
    command = optString("command"), sudo = optBoolean("sudo"), destructive = optBoolean("destructive")
)

suspend fun fetchActions(serverBase: String, token: String): Outcome<List<ActionItem>> =
    when (val r = lucilleRequest(serverBase, token, "GET", "/api/actions").parse()) {
        is Outcome.Ok -> Outcome.Ok(
            r.value.optJSONArray("actions")?.let { a -> List(a.length()) { a.getJSONObject(it).toActionItem() } }.orEmpty()
        )
        is Outcome.Fail -> r
    }

/**
 * Runs one action. This does not reuse [ApiReply.parse]: a "needs confirmation" or a
 * completed-but-failed run both come back as a normal HTTP 200 with `"ok": false`, and
 * both are real answers from the PC, not a [Outcome.Fail] (which means no answer at all).
 */
suspend fun runPcAction(serverBase: String, token: String, id: String, confirm: Boolean): Outcome<ActionRunResult> {
    val path = "/api/actions/" + URLEncoder.encode(id, "UTF-8") + "/run"
    val reply = lucilleRequest(serverBase, token, "POST", path, JSONObject().put("confirm", confirm))
        ?: return Outcome.Fail("Can't reach your PC. Check the address in Config and that Lucille Assistant is running.")
    val j = reply.json
    return when {
        reply.code == 404 && j == null -> Outcome.Fail("Your PC is running an older Lucille Assistant with no command link. Update it to 1.3 or newer.")
        j == null -> Outcome.Fail(if (reply.code == 401) "The PC refused this app's token." else "The PC answered HTTP ${reply.code}.")
        else -> Outcome.Ok(
            ActionRunResult(
                ok = j.optBoolean("ok"),
                needsConfirm = j.optBoolean("needs_confirm"),
                exitCode = if (j.has("exit_code") && !j.isNull("exit_code")) j.optInt("exit_code") else null,
                output = j.optString("output"),
                error = j.optString("error")
            )
        )
    }
}

// ─── Pairing ─────────────────────────────────────────────────────────────────
// Run `lucille-assistant pair` on the PC; it prints a 6-digit code good for one phone,
// one use, 10 minutes. Enter it here once — the PC hands back this phone's own permanent
// token, so nothing ever needs to be retyped again, even if the PC's main token rotates.

/** No token is sent: a device with none yet is exactly who this call is for. */
suspend fun redeemPairCode(serverBase: String, code: String, label: String): Outcome<String> {
    val body = JSONObject().put("code", code).put("label", label)
    val reply = lucilleRequest(serverBase, "", "POST", "/api/pair", body)
        ?: return Outcome.Fail("Can't reach your PC. Check the address in Config and that Lucille Assistant is running.")
    val j = reply.json
    if (j != null && j.optBoolean("ok")) {
        val token = j.optString("token")
        if (token.isNotBlank()) return Outcome.Ok(token)
    }
    val msg = j?.optString("error").orEmpty()
    return Outcome.Fail(
        when {
            msg.isNotBlank() -> msg
            reply.code == 404 -> "Your PC is running an older Lucille Assistant with no pairing. Update it to 1.3 or newer."
            else -> "The PC answered HTTP ${reply.code}."
        }
    )
}

// ─── Wellness (meds, water, meals, sleep) ────────────────────────────────────

data class MedTaken(val name: String, val takenAtMs: Long?)
data class MealEntry(val label: String, val atMs: Long?)
data class SleepRecord(val hours: Double)
data class MedSchedule(val name: String, val at: String) // "HH:MM" local time, same as the PC's config

data class CareState(
    val waterMl: Int,
    val meds: List<MedTaken>,
    val meals: List<MealEntry>,
    val sleep: SleepRecord?,
    val schedule: List<MedSchedule>,
    val waterGoalMl: Int,
    val sleepGoalHours: Double
)

private fun isoToMs(s: String): Long? = runCatching { java.time.Instant.parse(s).toEpochMilli() }.getOrNull()

suspend fun fetchCare(serverBase: String, token: String): Outcome<CareState> =
    when (val r = lucilleRequest(serverBase, token, "GET", "/api/care").parse()) {
        is Outcome.Ok -> {
            val day = r.value.optJSONObject("day") ?: JSONObject()
            val schedule = r.value.optJSONArray("schedule")
            val sleepJson = day.optJSONObject("sleep")
            Outcome.Ok(
                CareState(
                    waterMl = day.optInt("water_ml"),
                    meds = day.optJSONArray("meds")?.let { a -> List(a.length()) { i -> a.getJSONObject(i).let { MedTaken(it.optString("name"), isoToMs(it.optString("taken_at"))) } } }.orEmpty(),
                    meals = day.optJSONArray("meals")?.let { a -> List(a.length()) { i -> a.getJSONObject(i).let { MealEntry(it.optString("label"), isoToMs(it.optString("at"))) } } }.orEmpty(),
                    sleep = sleepJson?.let { SleepRecord(it.optDouble("hours")) },
                    schedule = schedule?.let { a -> List(a.length()) { i -> a.getJSONObject(i).let { MedSchedule(it.optString("name"), it.optString("at")) } } }.orEmpty(),
                    waterGoalMl = r.value.optInt("water_goal_ml", 2500),
                    sleepGoalHours = r.value.optDouble("sleep_goal_hours", 7.5)
                )
            )
        }
        is Outcome.Fail -> r
    }

/** Every care POST only needs "ok": the UI reloads /api/care afterward to get fresh state. */
private fun Outcome<JSONObject>.toUnit(): Outcome<Unit> = when (this) {
    is Outcome.Ok -> Outcome.Ok(Unit)
    is Outcome.Fail -> this
}

suspend fun logMed(serverBase: String, token: String, name: String): Outcome<Unit> =
    lucilleRequest(serverBase, token, "POST", "/api/care/med", JSONObject().put("name", name)).parse().toUnit()

suspend fun addWater(serverBase: String, token: String, ml: Int): Outcome<Unit> =
    lucilleRequest(serverBase, token, "POST", "/api/care/water", JSONObject().put("ml", ml)).parse().toUnit()

suspend fun logMeal(serverBase: String, token: String, label: String): Outcome<Unit> =
    lucilleRequest(serverBase, token, "POST", "/api/care/meal", JSONObject().put("label", label)).parse().toUnit()

/** [bedMs]/[wakeMs] are epoch milliseconds; the PC only needs the gap between them. */
suspend fun logSleep(serverBase: String, token: String, bedMs: Long, wakeMs: Long): Outcome<Unit> {
    val body = JSONObject()
        .put("bed", java.time.Instant.ofEpochMilli(bedMs).toString())
        .put("wake", java.time.Instant.ofEpochMilli(wakeMs).toString())
    return lucilleRequest(serverBase, token, "POST", "/api/care/sleep", body).parse().toUnit()
}

/**
 * The text "Copy for an AI" puts on the clipboard: a formal prompt, in the same sections as every
 * prompt Lucille hands an AI on the PC (formalprompt.go). Keep the headings equal to that file's.
 */
fun briefingText(b: Briefing): String {
    val name = b.title + if (b.project.isNotBlank()) " (${b.project})" else ""
    return listOf(
        "WHO" to "Lucille (the user's assistant) is handing you a project briefing on the user's behalf. You are the coder.",
        "THE JOB" to "Pick up \"$name\" from the briefing below: read the files under READ FIRST before you change anything, then carry on with what the briefing says is next.",
        "BACKGROUND" to listOf(b.summary, b.body).filter { it.isNotBlank() }.joinToString("\n\n"),
        "READ FIRST" to b.readFirst.filter { it.isNotBlank() }.joinToString("\n") { "- $it" },
        "HOW TO WORK" to listOf(
            "Read /mnt/recoverybuild/00-CURRENT/AI_INSTRUCTIONS.md and find this item in /mnt/recoverybuild/00-CURRENT/MASTER_BUILD_LIST.md. Claim the area on the ai-board before you edit anything, and stay out of areas another AI has claimed.",
            "Do not touch the user's keys, other drives, or phones. Anything destructive needs the user's explicit OK first."
        ).mapIndexed { i, r -> "${i + 1}. $r" }.joinToString("\n"),
        "DONE WHEN" to "The next step in the briefing is built and checked, or you have said what blocks it.",
        "REPORT BACK" to "Tell the user what changed, what you checked, and what is left."
    ).filter { it.second.isNotBlank() }.joinToString("\n\n") { (h, body) -> "$h:\n$body" } + "\n"
}
