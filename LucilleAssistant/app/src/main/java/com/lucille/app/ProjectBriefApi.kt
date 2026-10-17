package com.lucille.app

import android.content.Context
import android.util.Log
import kotlinx.coroutines.Dispatchers
import kotlinx.coroutines.async
import kotlinx.coroutines.awaitAll
import kotlinx.coroutines.coroutineScope
import kotlinx.coroutines.withContext
import org.json.JSONObject
import java.net.HttpURLConnection
import java.net.URL
import java.net.URLEncoder
import java.time.OffsetDateTime

// ─── Project brief ───────────────────────────────────────────────────────────
// Lucille reads the project files on the PC (GET /api/projectbrief). If the PC does not
// answer, this app reads the same files from the public GitHub mirrors itself and says so.
// Suggestions can be added to the project markdown or sent to Dr. Boltz; both need the PC.

data class GitAddr(val repo: String, val branch: String, val rel: String)

data class BriefFileInfo(
    val path: String,
    val name: String,
    val source: String, // "pc" | "git" | "missing" | "blocked"
    val reason: String,
    val modifiedMs: Long?,
    val git: GitAddr?
)

data class TrackerRow(val id: String, val p: String, val item: String, val status: String)

data class BriefTracker(val file: String, val source: String, val open: List<TrackerRow>, val total: Int, val done: Int)

data class BriefSuggestion(
    val id: String,
    val title: String,
    val why: String,
    val body: String,
    val size: String,
    val source: String, // "model" | "briefing" | "tracker"
    val command: String,
    val inMd: Boolean,
    val sent: String,
    val sentStatus: String
)

data class BriefProject(
    val id: String,
    val code: String,
    val title: String,
    val summary: String,
    val stale: Boolean,
    val files: List<BriefFileInfo>,
    val tracker: BriefTracker,
    val gitDirty: Int?,
    val canSend: Boolean,
    val repo: String,
    val sendNote: String,
    val suggestions: List<BriefSuggestion>,
    val suggestionsSource: String,
    val command: String
)

data class ProjectBrief(
    /** "pc" = read live from the PC, "git" = the PC did not answer, read from GitHub, "cached" = neither answered. */
    val mode: String,
    val generatedAtMs: Long,
    /** Where the facts come from, in words: shown under the title. */
    val asOf: String,
    val mdPath: String,
    val mdWritable: Boolean,
    val projects: List<BriefProject>,
    val filesRead: Int,
    val filesTotal: Int
)

// ─── Tracker table (same rules as parseTracker in projectbrief.go: keep them in step) ───

private val markdownNoise = Regex("[*`]")  // every asterisk, like the PC side

/** Reads the table under "## CODE: ..." in a markdown build list. Null when there is no such section. */
fun parseTrackerRows(md: String, code: String): List<TrackerRow>? {
    var inSection = false
    var found = false
    var cols: Map<String, Int>? = null
    val rows = mutableListOf<TrackerRow>()
    for (line in md.lineSequence()) {
        val t = line.trim()
        if (t.startsWith("## ")) {
            if (inSection) break
            inSection = t.startsWith("## $code:")
            continue
        }
        if (!inSection || !t.startsWith("|")) continue
        val cells = t.trim('|').split("|").map { it.trim() }
        if (cells[0].equals("ID", ignoreCase = true)) {
            cols = cells.mapIndexed { i, c -> c.lowercase() to i }.toMap()
            found = true
            continue
        }
        val c = cols ?: continue
        if (cells[0].startsWith("---") || !cells[0].startsWith("$code-")) continue
        fun cell(name: String, def: Int): String = cells.getOrNull(c[name] ?: def).orEmpty()
        rows += TrackerRow(
            cells[0], cell("p", 1),
            markdownNoise.replace(cell("item", 2), "").take(220),
            markdownNoise.replace(cell("status", 3), "").take(60)
        )
    }
    return if (found) rows else null
}

fun isOpenStatus(status: String): Boolean {
    val u = status.trim().uppercase()
    if (u.isEmpty()) return false
    return listOf("DONE", "IDEA", "DROP", "RETIRED", "WONT", "WON'T").none { u.startsWith(it) }
}

fun summarizeTracker(file: String, source: String, rows: List<TrackerRow>): BriefTracker {
    val open = rows.filter { isOpenStatus(it.status) }.sortedBy { it.p }.take(6) // sortedBy is stable
    val done = rows.count { it.status.trim().uppercase().startsWith("DONE") }
    return BriefTracker(file, source, open, rows.size, done)
}

// ─── JSON from the PC ────────────────────────────────────────────────────────

private fun isoMs(s: String): Long? = runCatching { OffsetDateTime.parse(s).toInstant().toEpochMilli() }
    .getOrNull()?.takeIf { it > 946_684_800_000L } // the PC sends year 0001 for "unknown"

private fun JSONObject.str(k: String) = optString(k, "")

private fun JSONObject.toFile() = BriefFileInfo(
    path = str("path"), name = str("name"), source = str("source"), reason = str("reason"),
    modifiedMs = isoMs(str("modified")),
    git = optJSONObject("git")?.let { GitAddr(it.str("repo"), it.str("branch").ifBlank { "main" }, it.str("rel")) }
)

private fun JSONObject.toRow() = TrackerRow(str("id"), str("p"), str("item"), str("status"))

private fun JSONObject.toSuggestion() = BriefSuggestion(
    id = str("id"), title = str("title"), why = str("why"), body = str("body"),
    size = str("size").ifBlank { "evening" }, source = str("source"), command = str("command"),
    inMd = optBoolean("in_md"), sent = str("sent"), sentStatus = str("sent_status")
)

private fun JSONObject.toProject(): BriefProject {
    val t = optJSONObject("tracker")
    val files = optJSONArray("files")?.let { a -> List(a.length()) { a.getJSONObject(it).toFile() } }.orEmpty()
    return BriefProject(
        id = str("id"), code = str("code"), title = str("title"), summary = str("summary"),
        stale = optBoolean("stale"), files = files,
        tracker = BriefTracker(
            file = t?.str("file").orEmpty(), source = t?.str("source")?.ifBlank { "none" } ?: "none",
            open = t?.optJSONArray("open")?.let { a -> List(a.length()) { a.getJSONObject(it).toRow() } }.orEmpty(),
            total = t?.optInt("total") ?: 0, done = t?.optInt("done") ?: 0
        ),
        gitDirty = optJSONObject("git")?.let { it.optInt("dirty") + it.optInt("untracked") },
        canSend = optBoolean("can_send"), repo = str("repo"), sendNote = str("send_note"),
        suggestions = optJSONArray("suggestions")?.let { a -> List(a.length()) { a.getJSONObject(it).toSuggestion() } }.orEmpty(),
        suggestionsSource = str("suggestions_source"),
        command = str("command")
    )
}

fun parseProjectBrief(j: JSONObject, mode: String, asOf: String): ProjectBrief {
    val md = j.optJSONObject("md")
    return ProjectBrief(
        mode = mode,
        generatedAtMs = isoMs(j.optString("generated_at")) ?: System.currentTimeMillis(),
        asOf = asOf,
        mdPath = md?.str("path").orEmpty(),
        mdWritable = md?.optBoolean("writable") ?: false,
        projects = j.optJSONArray("projects")?.let { a -> List(a.length()) { a.getJSONObject(it).toProject() } }.orEmpty(),
        filesRead = j.optInt("files_read"), filesTotal = j.optInt("files_total")
    )
}

// ─── Cache of the last brief the PC gave (also tells the fallback where files live) ───

private const val CACHE_PREFS = "lucille_projectbrief"

private fun cacheBrief(context: Context, raw: String) {
    context.getSharedPreferences(CACHE_PREFS, Context.MODE_PRIVATE).edit().putString("last", raw).apply()
}

private fun cachedBrief(context: Context): JSONObject? = runCatching {
    context.getSharedPreferences(CACHE_PREFS, Context.MODE_PRIVATE).getString("last", null)?.let { JSONObject(it) }
}.getOrNull()

// ─── Loading: PC first, then git, then the last copy ─────────────────────────

private fun clockLabel(ms: Long): String =
    java.time.Instant.ofEpochMilli(ms).atZone(java.time.ZoneId.systemDefault())
        .format(java.time.format.DateTimeFormatter.ofPattern("EEE d MMM, h:mm a"))

suspend fun loadProjectBrief(context: Context, serverBase: String, token: String): Outcome<ProjectBrief> {
    val reply = lucilleRequest(serverBase, token, "GET", "/api/projectbrief")
    if (reply == null) return loadFromGit(context)
    val j = reply.json
    return when {
        reply.code in 200..299 && j != null && j.optBoolean("ok") -> {
            cacheBrief(context, j.toString())
            val pc = parseProjectBrief(j, "pc", "read from your PC at ${clockLabel(System.currentTimeMillis())}")
            Outcome.Ok(topUpFromGit(pc))
        }
        reply.code == 404 && j == null ->
            Outcome.Fail("Your PC is running an older Lucille Assistant with no project brief. Update it to 1.6 or newer.")
        reply.code == 401 -> Outcome.Fail("The PC refused this app's token. Pair this phone again in Config.")
        else -> Outcome.Fail(j?.optString("error").orEmpty().ifBlank { "The PC answered HTTP ${reply.code}." })
    }
}

/** The PC answered but a drive was missing, so a project has no build-list rows: fill those from git. */
private suspend fun topUpFromGit(pc: ProjectBrief): ProjectBrief {
    val need = pc.projects.filter { p -> p.code.isNotBlank() && p.tracker.source == "none" && p.files.any { it.source != "pc" && it.git != null } }
    if (need.isEmpty()) return pc
    val filled = readProjectsFromGit(need).first
    return pc.copy(projects = pc.projects.map { p -> filled[p.id]?.let { g -> p.copy(tracker = g.tracker) } ?: p })
}

private suspend fun loadFromGit(context: Context): Outcome<ProjectBrief> {
    val cached = cachedBrief(context)?.let { parseProjectBrief(it, "cached", "") }
    val base = cached?.projects?.takeIf { it.isNotEmpty() } ?: fallbackSkeleton()
    val (filled, stamp) = readProjectsFromGit(base)
    val reached = filled.values.any { it.files.any { f -> f.source == "git" } }
    if (!reached) {
        if (cached == null) {
            return Outcome.Fail("Can't reach your PC or GitHub, and there is no earlier brief on this phone yet.")
        }
        return Outcome.Ok(cached.copy(asOf = "Your PC and GitHub did not answer. This is the last brief this phone got, ${clockLabel(cached.generatedAtMs)}."))
    }
    // Facts come from git; the suggestions, commands and "already added" marks are the PC's last ones.
    val projects = base.map { p -> filled[p.id] ?: p }
    return Outcome.Ok(
        ProjectBrief(
            mode = "git", generatedAtMs = System.currentTimeMillis(),
            asOf = "Your PC did not answer, so I read GitHub instead ($stamp). Suggestions are from the last time the PC answered.",
            mdPath = cached?.mdPath.orEmpty(), mdWritable = false, projects = projects,
            filesRead = projects.sumOf { p -> p.files.count { it.source == "git" } },
            filesTotal = projects.sumOf { it.files.size }
        )
    )
}

/** Builds the project list from git. Returns the projects by id and a short "main @ abc1234, date" stamp. */
private suspend fun readProjectsFromGit(projects: List<BriefProject>): Pair<Map<String, BriefProject>, String> = coroutineScope {
    val addrs = projects.flatMap { p -> p.files.mapNotNull { it.git } }.distinct()
    // one commit lookup per repo/branch, so the stamp names a real commit
    val heads = addrs.map { it.repo to it.branch }.distinct().map { (repo, branch) ->
        async { (repo to branch) to headCommit(repo, branch) }
    }.awaitAll().toMap()
    val texts = addrs.map { a ->
        async {
            val ref = heads[a.repo to a.branch]?.first ?: a.branch
            a to httpGetText("https://raw.githubusercontent.com/${a.repo}/$ref/${a.rel.split('/').joinToString("/") { URLEncoder.encode(it, "UTF-8").replace("+", "%20") }}")
        }
    }.awaitAll().toMap()

    val out = projects.associate { p ->
        val files = p.files.map { f ->
            val a = f.git
            when {
                a == null -> f.copy(source = "missing", reason = "only on the PC")
                texts[a] != null -> f.copy(source = "git", reason = "")
                else -> f.copy(source = "missing", reason = "not in git (or GitHub did not answer)")
            }
        }
        var tracker = BriefTracker(p.tracker.file, "none", emptyList(), 0, 0)
        if (p.code.isNotBlank()) {
            for (f in files) {
                val text = f.git?.let { texts[it] } ?: continue
                val rows = parseTrackerRows(text, p.code) ?: continue
                tracker = summarizeTracker(f.name, "git", rows)
                break
            }
        }
        p.id to p.copy(files = files, tracker = tracker, gitDirty = null)
    }
    val known = heads.entries.mapNotNull { (k, v) -> v?.let { Triple(k.first.substringAfter('/'), it.first.take(7), it.second) } }
    val stamp = if (known.isEmpty()) "main, commit unknown" else
        known.joinToString(", ") { "${it.first}@${it.second}" } + ", oldest from ${clockLabel(known.minOf { it.third })}"
    out to stamp
}

/** (sha, commit time in ms) of a branch head via the unauthenticated GitHub API; null when it can't be read. */
private suspend fun headCommit(repo: String, branch: String): Pair<String, Long>? {
    val text = httpGetText("https://api.github.com/repos/$repo/commits/${URLEncoder.encode(branch, "UTF-8")}", json = true) ?: return null
    return runCatching {
        val j = JSONObject(text)
        val sha = j.getString("sha")
        if (!Regex("^[0-9a-f]{40}$").matches(sha)) return null
        sha to (isoMs(j.getJSONObject("commit").getJSONObject("committer").getString("date")) ?: 0L)
    }.getOrNull()
}

private const val MAX_FETCH = 2_000_000

private suspend fun httpGetText(url: String, json: Boolean = false): String? = withContext(Dispatchers.IO) {
    try {
        val conn = URL(url).openConnection() as HttpURLConnection
        try {
            conn.connectTimeout = 6000
            conn.readTimeout = 10000
            conn.setRequestProperty("User-Agent", "lucille-assistant-app")
            if (json) conn.setRequestProperty("Accept", "application/vnd.github+json")
            if (conn.responseCode !in 200..299) return@withContext null
            conn.inputStream.use { s ->
                val buf = java.io.ByteArrayOutputStream()
                val chunk = ByteArray(16 * 1024)
                while (buf.size() < MAX_FETCH) {
                    val n = s.read(chunk)
                    if (n < 0) break
                    buf.write(chunk, 0, n)
                }
                buf.toString("UTF-8")
            }
        } finally {
            conn.disconnect()
        }
    } catch (e: Exception) {
        Log.w("ProjectBrief", "git fetch failed: ${e.message}")
        null
    }
}

/** Only used when the PC has never answered and nothing is cached: the four projects and where git has their files. */
private fun fallbackSkeleton(): List<BriefProject> {
    val lucy = "cledford955-maker/i30ltz-lucille-assistant"
    val rec = "cledford955-maker/I30LTZ-Recovery-OS"
    fun f(path: String, repo: String, rel: String) =
        BriefFileInfo(path, rel.substringAfterLast('/'), "missing", "", null, GitAddr(repo, "main", rel))
    val list = f("/mnt/recoverybuild/00-CURRENT/MASTER_BUILD_LIST.md", rec, "00-CURRENT/MASTER_BUILD_LIST.md")
    fun p(id: String, code: String, title: String, vararg extra: BriefFileInfo) = BriefProject(
        id, code, title, "", false, listOf(list) + extra, BriefTracker("", "none", emptyList(), 0, 0), null,
        false, "", "Needs your PC.", emptyList(), "", ""
    )
    return listOf(
        p("swarm-os", "SW", "SWARM OS", f("/mnt/Lucy/SWARM_AI_FEATURES.md", lucy, "SWARM_AI_FEATURES.md"), f("/mnt/Lucy/CLAUDE.md", lucy, "CLAUDE.md")),
        p("recovery-os", "RC", "I30LTZ Recovery OS", f("/mnt/recoverybuild/CLAUDE.md", rec, "CLAUDE.md")),
        p("i30ltz-companion", "CP", "I30LTZ Companion"),
        p("lucille-assistant", "LA", "Lucille Assistant")
    )
}

// ─── Things a person can do with a suggestion (all need the PC) ──────────────

private fun String.enc() = URLEncoder.encode(this, "UTF-8")

private fun ApiReply?.outcome(): Outcome<JSONObject> {
    if (this == null) return Outcome.Fail("Can't reach your PC. Adding and sending need it.")
    val j = json
    if (code == 404 && j == null) return Outcome.Fail("Your PC is running an older Lucille Assistant. Update it to 1.6 or newer.")
    if (code in 200..299 && j != null && j.optBoolean("ok")) return Outcome.Ok(j)
    return Outcome.Fail(
        j?.optString("error").orEmpty().ifBlank {
            if (code == 401) "The PC refused this app's token. Pair this phone again in Config." else "The PC answered HTTP $code."
        }
    )
}

/** Asks Lucille's language model for fresh suggestions for one project (costs a little; only on a tap). */
suspend fun thinkSuggestions(serverBase: String, token: String, projectId: String): Outcome<Int> =
    when (val r = lucilleRequest(serverBase, token, "POST", "/api/projectbrief/${projectId.enc()}/think").outcome()) {
        is Outcome.Ok -> Outcome.Ok(r.value.optInt("count"))
        is Outcome.Fail -> r
    }

/** Appends a suggestion to the project markdown's inbox. Ok(true) = added, Ok(false) = it was already there. */
suspend fun addSuggestionToMd(serverBase: String, token: String, projectId: String, s: BriefSuggestion): Outcome<Boolean> =
    when (val r = lucilleRequest(
        serverBase, token, "POST", "/api/projectbrief/${projectId.enc()}/md",
        JSONObject().put("title", s.title).put("why", s.why)
    ).outcome()) {
        is Outcome.Ok -> Outcome.Ok(r.value.optBoolean("added"))
        is Outcome.Fail -> r
    }

/** Queues a coding job for Dr. Boltz with the (reviewed, possibly edited) text. Ok = the challenge id. */
suspend fun sendSuggestionToBoltz(serverBase: String, token: String, projectId: String, sid: String, title: String, body: String, size: String): Outcome<String> =
    when (val r = lucilleRequest(
        serverBase, token, "POST", "/api/projectbrief/${projectId.enc()}/boltz",
        JSONObject().put("title", title).put("body", body).put("size", size).put("sid", sid)
    ).outcome()) {
        is Outcome.Ok -> Outcome.Ok(r.value.optJSONObject("challenge")?.optString("id").orEmpty())
        is Outcome.Fail -> r
    }
