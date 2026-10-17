package com.lucille.app

import org.json.JSONArray
import org.json.JSONObject

// ─── Game Mode's live feed: today's deals + Steam hours ───────────────────────────────────────
// Reads the same /api/deals and /api/gaming the rest of the app already uses (feeds.go), so Game
// Mode shows real, rotating data instead of a fixed sample list.

private fun JSONObject.str(k: String) = if (isNull(k)) "" else optString(k, "")
private fun JSONArray?.objs(): List<JSONObject> = if (this == null) emptyList() else List(length()) { getJSONObject(it) }

private fun ApiReply?.parse(): Outcome<JSONObject> {
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

data class GameDeal(
    val title: String, val store: String, val salePrice: String, val normalPrice: String,
    val savingsPct: Int, val steamAppId: Int, val ratingPct: String, val thumb: String, val url: String
)

private fun JSONObject.toDeal(): GameDeal = GameDeal(
    str("title"), str("store"), str("sale_price"), str("normal_price"), optInt("savings_pct"),
    str("steam_app_id").toIntOrNull() ?: 0, str("steam_rating_pct"), str("thumb"), str("url")
)

/**
 * Steam deals first (official store, always verifiable), then authorized key resellers. One row
 * per game: the reseller feed lists the same game once per shop, so only the cheapest offer is kept.
 */
suspend fun fetchDeals(base: String, token: String): Outcome<List<GameDeal>> =
    when (val r = lucilleRequest(base, token, "GET", "/api/deals").parse()) {
        is Outcome.Fail -> r
        is Outcome.Ok -> Outcome.Ok(
            dedupeDeals(
                r.value.optJSONArray("steam").objs().map { it.toDeal() } +
                    r.value.optJSONArray("keys").objs().map { it.toDeal() }
            )
        )
    }

/** Keeps the first-listed group order (Steam before resellers) and the cheapest offer per game. */
internal fun dedupeDeals(deals: List<GameDeal>): List<GameDeal> {
    fun key(d: GameDeal) = if (d.steamAppId > 0) "app:${d.steamAppId}" else "title:" + d.title.trim().lowercase()
    val best = LinkedHashMap<String, GameDeal>()
    for (d in deals) {
        val k = key(d)
        val have = best[k]
        val price = d.salePrice.toDoubleOrNull() ?: Double.MAX_VALUE
        if (have == null || price < (have.salePrice.toDoubleOrNull() ?: Double.MAX_VALUE)) {
            // A cheaper reseller copy replaces a Steam one in place, so the group order holds.
            best[k] = d
        }
    }
    return best.values.toList()
}

data class GamingStats(val steamHours2Weeks: Double, val loggedWeekHours: Double, val steamError: String)

suspend fun fetchGamingStats(base: String, token: String): Outcome<GamingStats> =
    when (val r = lucilleRequest(base, token, "GET", "/api/gaming").parse()) {
        is Outcome.Fail -> r
        is Outcome.Ok -> Outcome.Ok(
            GamingStats(r.value.optDouble("steam_hours_2weeks", 0.0), r.value.optDouble("logged_week_hours", 0.0), r.value.str("steam_error"))
        )
    }
