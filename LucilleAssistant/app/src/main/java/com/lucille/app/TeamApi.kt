package com.lucille.app

import android.graphics.Bitmap
import android.graphics.BitmapFactory
import android.util.LruCache
import kotlinx.coroutines.Dispatchers
import kotlinx.coroutines.withContext
import org.json.JSONArray
import org.json.JSONObject
import java.net.HttpURLConnection
import java.net.URL
import java.net.URLEncoder

// ─── Team Brief + Games: the phone's view of what Lucille Assistant on the PC keeps ──────────────
// No logic lives here. Threads, jobs, game cards and marks are all kept by the Go service (threads.go, jobs.go,
// games.go); the phone only reads them and sends what the person types, so the web page, this app and the SWARM OS
// app always show the same thing.

data class TStatement(
    val anchor: String, val text: String, val sub: String, val tag: String,
    val level: String, val status: String, val due: String
)

data class TSection(val id: String, val title: String, val note: String, val items: List<TStatement>)

data class TMsg(
    val id: String, val anchor: String, val at: String, val by: String, val text: String,
    val kind: String, val err: Boolean, val model: String
)

data class TJob(val id: String, val to: String, val status: String, val claimedBy: String, val prompt: String)

data class TeamState(
    val sections: List<TSection>,
    val threads: Map<String, List<TMsg>>,
    val pending: Set<String>, // "anchor|who": that AI is writing an answer right now
    val jobs: Map<String, List<TJob>>,
    val seq: Long
)

private fun ApiReply?.teamParse(): Outcome<JSONObject> {
    if (this == null) return Outcome.Fail("I can't reach Lucille on the PC. Is it on, and is the address right in Config?")
    val j = json
    return when {
        code == 401 -> Outcome.Fail(j?.optString("error").orEmpty().ifBlank { "This phone isn't paired yet. Put the code in Config." })
        code !in 200..299 -> Outcome.Fail(j?.optString("error").orEmpty().ifBlank { "The PC said no (HTTP $code)." })
        j == null -> Outcome.Fail("The PC sent something I couldn't read.")
        j.has("ok") && !j.optBoolean("ok") -> Outcome.Fail(j.optString("error").ifBlank { "That didn't work." })
        else -> Outcome.Ok(j)
    }
}

private fun JSONObject.str(k: String) = if (isNull(k)) "" else optString(k, "")

private fun JSONArray?.objs(): List<JSONObject> = if (this == null) emptyList() else List(length()) { getJSONObject(it) }

private fun JSONObject.toStatement() = TStatement(str("anchor"), str("text"), str("sub"), str("tag"), str("level"), str("status"), str("due"))

private fun JSONObject.toMsg() = TMsg(str("id"), str("anchor"), str("at"), str("by"), str("text"), str("kind"), optBoolean("err"), str("model"))

private fun JSONObject.toJob() = TJob(str("id"), str("to"), str("status"), str("claimed_by"), str("prompt"))

suspend fun fetchTeam(base: String, token: String): Outcome<TeamState> =
    when (val r = lucilleRequest(base, token, "GET", "/api/artifact").teamParse()) {
        is Outcome.Fail -> r
        is Outcome.Ok -> {
            val j = r.value
            val sections = j.optJSONArray("sections").objs().map { s ->
                TSection(s.str("id"), s.str("title"), s.str("note"), s.optJSONArray("items").objs().map { it.toStatement() })
            }
            val threads = HashMap<String, List<TMsg>>()
            j.optJSONObject("threads")?.let { t -> t.keys().forEach { k -> threads[k] = t.optJSONArray(k).objs().map { it.toMsg() } } }
            val jobs = HashMap<String, List<TJob>>()
            j.optJSONObject("jobs")?.let { t -> t.keys().forEach { k -> jobs[k] = t.optJSONArray(k).objs().map { it.toJob() } } }
            val pending = j.optJSONArray("pending").objs().map { it.str("anchor") + "|" + it.str("who") }.toSet()
            Outcome.Ok(TeamState(sections, threads, pending, jobs, j.optLong("seq")))
        }
    }

suspend fun postThread(base: String, token: String, anchor: String, text: String, quote: String): Outcome<String> {
    val body = JSONObject().put("anchor", anchor).put("text", text).put("quote", quote).put("title", quote)
    return when (val r = lucilleRequest(base, token, "POST", "/api/threads/post", body).teamParse()) {
        is Outcome.Ok -> Outcome.Ok(r.value.str("change"))
        is Outcome.Fail -> r
    }
}

suspend fun askThread(base: String, token: String, anchor: String, who: String): Outcome<Unit> =
    when (val r = lucilleRequest(base, token, "POST", "/api/threads/ask", JSONObject().put("anchor", anchor).put("who", who)).teamParse()) {
        is Outcome.Ok -> Outcome.Ok(Unit)
        is Outcome.Fail -> r
    }

suspend fun draftJob(base: String, token: String, anchor: String, to: String): Outcome<String> =
    when (val r = lucilleRequest(base, token, "POST", "/api/jobs/draft", JSONObject().put("anchor", anchor).put("to", to)).teamParse()) {
        is Outcome.Ok -> Outcome.Ok(r.value.str("prompt"))
        is Outcome.Fail -> r
    }

data class TDealResult(val job: TJob, val target: String, val launched: Boolean)

/** dealThread = "Deal with it": comment/answer is shared immediately and Lucille tells Claude or next available model to deal with it now. */
suspend fun dealThread(base: String, token: String, anchor: String, text: String, quote: String, to: String = "auto", launch: Boolean = true): Outcome<TDealResult> {
    val body = JSONObject().put("anchor", anchor).put("text", text).put("quote", quote).put("to", to).put("launch", launch)
    return when (val r = lucilleRequest(base, token, "POST", "/api/threads/deal", body).teamParse()) {
        is Outcome.Ok -> {
            val j = r.value
            val job = j.getJSONObject("job").toJob()
            Outcome.Ok(TDealResult(job, j.optString("target", to), j.optBoolean("launched", false)))
        }
        is Outcome.Fail -> r
    }
}

/** launch = "Approve and start": Lucille starts the coder on the PC herself (launcher.go limits apply). */
suspend fun createJob(base: String, token: String, anchor: String, to: String, prompt: String, launch: Boolean = false): Outcome<TJob> =
    when (val r = lucilleRequest(base, token, "POST", "/api/jobs", JSONObject().put("anchor", anchor).put("to", to).put("prompt", prompt).put("launch", launch)).teamParse()) {
        is Outcome.Ok -> Outcome.Ok(r.value.getJSONObject("job").toJob())
        is Outcome.Fail -> r
    }

suspend fun cancelJob(base: String, token: String, id: String): Outcome<Unit> =
    when (val r = lucilleRequest(base, token, "POST", "/api/jobs/" + URLEncoder.encode(id, "UTF-8") + "/cancel", JSONObject()).teamParse()) {
        is Outcome.Ok -> Outcome.Ok(Unit)
        is Outcome.Fail -> r
    }

// ─── Games ───────────────────────────────────────────────────────────────────────────────────

data class GameMark(val appid: Int, val name: String, val image: String, val like: String, val wish: Boolean)

data class GameHit(val appid: Int, val name: String, val image: String, val price: String, val metascore: String, val mark: GameMark?)

data class ReviewSample(val up: Boolean, val text: String, val hours: Int)

data class ShotImg(val thumb: String, val full: String)

data class StorePrice(val store: String, val price: String, val savings: Int, val url: String)

data class GameCardData(
    val appid: Int, val key: String, val name: String, val image: String, val description: String, val release: String,
    val developers: List<String>, val genres: List<String>, val metacritic: Int, val price: String,
    val reviewWord: String, val reviewPct: Int, val reviewTotal: Int, val samples: List<ReviewSample>,
    val shots: List<ShotImg>, val webTitle: String, val webExtract: String, val webUrl: String,
    val prices: List<StorePrice>, val lowestEver: String, val storeUrl: String, val searchUrl: String,
    val notes: List<String>, val mark: GameMark?
)

private fun JSONObject.toMark(): GameMark? =
    if (!optBoolean("wish") && str("like").isEmpty()) null
    else GameMark(optInt("appid"), str("name"), str("image"), str("like"), optBoolean("wish"))

private fun JSONArray?.strings(): List<String> = if (this == null) emptyList() else List(length()) { optString(it) }

suspend fun fetchGameSearch(base: String, token: String, q: String): Outcome<List<GameHit>> =
    when (val r = lucilleRequest(base, token, "GET", "/api/game/search?q=" + URLEncoder.encode(q, "UTF-8")).teamParse()) {
        is Outcome.Fail -> r
        is Outcome.Ok -> Outcome.Ok(r.value.optJSONArray("results").objs().map {
            GameHit(it.optInt("appid"), it.str("name"), it.str("image"), it.str("price"), it.str("metascore"), it.optJSONObject("mark")?.toMark())
        })
    }

suspend fun fetchGame(base: String, token: String, appid: Int, title: String): Outcome<GameCardData> {
    val q = if (appid > 0) "appid=$appid" else "title=" + URLEncoder.encode(title, "UTF-8")
    return when (val r = lucilleRequest(base, token, "GET", "/api/game?$q").teamParse()) {
        is Outcome.Fail -> r
        is Outcome.Ok -> {
            val g = r.value.getJSONObject("game")
            Outcome.Ok(
                GameCardData(
                    g.optInt("appid"), g.str("key"), g.str("name"), g.str("image"), g.str("description"), g.str("release"),
                    g.optJSONArray("developers").strings(), g.optJSONArray("genres").strings(), g.optInt("metacritic"), g.str("price"),
                    g.str("review_word"), g.optInt("review_pct"), g.optInt("review_total"),
                    g.optJSONArray("review_samples").objs().map { ReviewSample(it.optBoolean("up"), it.str("text"), it.optInt("hours")) },
                    g.optJSONArray("screenshots").objs().map { ShotImg(it.str("thumb"), it.str("full")) },
                    g.str("web_title"), g.str("web_extract"), g.str("web_url"),
                    g.optJSONArray("prices").objs().map { StorePrice(it.str("store"), it.str("sale_price"), it.optInt("savings_pct"), it.str("url")) },
                    g.str("lowest_ever"), g.str("store_url"), g.str("search_url"), g.optJSONArray("notes").strings(),
                    g.optJSONObject("mark")?.toMark()
                )
            )
        }
    }
}

suspend fun fetchGameMarks(base: String, token: String): Outcome<List<GameMark>> =
    when (val r = lucilleRequest(base, token, "GET", "/api/games/marks").teamParse()) {
        is Outcome.Fail -> r
        is Outcome.Ok -> Outcome.Ok(r.value.optJSONArray("games").objs().mapNotNull { it.toMark() })
    }

/** Leave [like] null to keep it, [wish] null to keep it. Returns the game's mark afterwards (null = nothing left to remember). */
suspend fun markGame(base: String, token: String, appid: Int, name: String, image: String, like: String?, wish: Boolean?): Outcome<GameMark?> {
    val body = JSONObject().put("appid", appid).put("name", name).put("image", image)
    if (like != null) body.put("like", like)
    if (wish != null) body.put("wish", wish)
    return when (val r = lucilleRequest(base, token, "POST", "/api/games/mark", body).teamParse()) {
        is Outcome.Ok -> Outcome.Ok(r.value.optJSONObject("mark")?.toMark())
        is Outcome.Fail -> r
    }
}

// ─── Pictures from Steam's own CDN (no image library needed) ─────────────────────────────────

private val imageCache = object : LruCache<String, Bitmap>(48 * 1024 * 1024) {
    override fun sizeOf(key: String, value: Bitmap) = value.byteCount
}

/** Only Steam's CDN is fetched; the PC already refuses any other address, this is the second lock. */
suspend fun loadRemoteBitmap(url: String): Bitmap? = withContext(Dispatchers.IO) {
    if (!(url.startsWith("https://") && url.substringAfter("https://").substringBefore("/").endsWith("steamstatic.com"))) return@withContext null
    imageCache.get(url)?.let { return@withContext it }
    try {
        val c = URL(url).openConnection() as HttpURLConnection
        try {
            c.connectTimeout = 6000
            c.readTimeout = 15000
            val bmp = c.inputStream.use { BitmapFactory.decodeStream(it) }
            if (bmp != null) imageCache.put(url, bmp)
            bmp
        } finally {
            c.disconnect()
        }
    } catch (_: Exception) {
        null
    }
}
