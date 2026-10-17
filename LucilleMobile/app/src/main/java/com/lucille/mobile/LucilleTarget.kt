package com.lucille.mobile

import android.content.Context
import android.os.Build
import org.json.JSONObject
import java.net.HttpURLConnection
import java.net.URL
import java.net.URLEncoder

/**
 * Which Lucille the app shows: the one running on this phone (works with the
 * PC off) or the one on the user's PC (briefings, "Approve and start", the
 * gaming PC's telemetry, everything the PC has).
 *
 * Reaching the PC needs a paired-device token: the user makes a 6-digit code
 * on the PC (`lucille-assistant pair` or the Brief's "Pair a browser or
 * phone"), types it into the app once, and this redeems it for a token that
 * stays in app-private storage. The code itself is never stored.
 */
object LucilleTarget {
    private const val PREFS = "target"
    private const val KEY_PC = "pc_url"
    private const val KEY_TOKEN = "pc_token"
    private const val KEY_LAST_PC = "last_pc_url"   // survives switching to "On phone", so sync keeps going

    fun pcUrl(ctx: Context): String = prefs(ctx).getString(KEY_PC, "") ?: ""
    fun isPc(ctx: Context): Boolean = pcUrl(ctx).isNotBlank()
    private fun token(ctx: Context): String = prefs(ctx).getString(KEY_TOKEN, "") ?: ""

    fun usePc(ctx: Context, address: String) {
        val url = normalize(address)
        // A different PC needs its own pairing; don't send one PC's token to another.
        if (url != syncUrl(ctx)) prefs(ctx).edit().remove(KEY_TOKEN).apply()
        prefs(ctx).edit().putString(KEY_PC, url).putString(KEY_LAST_PC, url).apply()
    }

    fun usePhone(ctx: Context) {
        prefs(ctx).edit().remove(KEY_PC).apply()
    }

    /**
     * Trades a pairing code for a device token. Blocking: call off the main thread.
     * Returns null on success, otherwise a message to show the user.
     */
    fun pair(ctx: Context, code: String): String? {
        val base = pcUrl(ctx)
        if (base.isBlank()) return "Enter your PC's address first."
        return try {
            val c = URL("$base/api/pair").openConnection() as HttpURLConnection
            c.requestMethod = "POST"
            c.connectTimeout = 5000
            c.readTimeout = 8000
            c.doOutput = true
            c.setRequestProperty("Content-Type", "application/json")
            val label = "Lucille Mobile (${Build.MODEL})"
            c.outputStream.use { it.write(JSONObject().put("code", code.trim()).put("label", label).toString().toByteArray()) }
            val body = (if (c.responseCode in 200..299) c.inputStream else c.errorStream).bufferedReader().readText()
            val j = JSONObject(body)
            if (c.responseCode in 200..299 && j.optBoolean("ok")) {
                prefs(ctx).edit().putString(KEY_TOKEN, j.getString("token")).apply()
                null
            } else {
                j.optString("error", "Pairing failed (HTTP ${c.responseCode}).")
            }
        } catch (e: Exception) {
            "Can't reach $base: ${e.message}"
        }
    }

    /**
     * The PC this phone was last paired with, kept even while the app shows the phone's
     * own Lucille, so shared-memory sync doesn't stop when you switch to "On phone".
     */
    fun syncUrl(ctx: Context): String = pcUrl(ctx).ifBlank { prefs(ctx).getString(KEY_LAST_PC, "") ?: "" }

    /** Paired-device token for [syncUrl], or "" if this phone hasn't been paired with a PC. */
    fun syncToken(ctx: Context): String = if (syncUrl(ctx).isBlank()) "" else token(ctx)

    /** True once a PC address is set AND this phone has been paired with it. */
    fun paired(ctx: Context): Boolean = isPc(ctx) && token(ctx).isNotBlank()

    class Reply(val code: Int, val json: JSONObject?)

    /**
     * An authenticated call to the PC's Lucille API (paired-device token).
     * Blocking: call off the main thread. Null when the PC can't be reached.
     */
    fun request(ctx: Context, method: String, path: String, body: JSONObject? = null): Reply? {
        if (!paired(ctx)) return null
        return try {
            val c = URL(pcUrl(ctx) + path).openConnection() as HttpURLConnection
            c.requestMethod = method
            c.connectTimeout = 4000
            c.readTimeout = 8000
            c.setRequestProperty("X-Swarm-Auth", token(ctx))
            if (body != null) {
                c.doOutput = true
                c.setRequestProperty("Content-Type", "application/json")
                c.outputStream.use { it.write(body.toString().toByteArray()) }
            }
            val text = (if (c.responseCode in 200..299) c.inputStream else c.errorStream)?.bufferedReader()?.readText().orEmpty()
            val reply = Reply(c.responseCode, try { JSONObject(text) } catch (_: Exception) { null })
            c.disconnect()
            reply
        } catch (_: Exception) {
            null
        }
    }

    /** URL the WebView should load right now. */
    fun url(ctx: Context): String {
        if (!isPc(ctx)) return "http://127.0.0.1:${LucilleHostService.LISTEN_PORT}/"
        // The page saves ?token= to its own storage and strips it from the address.
        val t = token(ctx)
        return pcUrl(ctx) + "/" + if (t.isNotBlank()) "?token=" + URLEncoder.encode(t, "UTF-8") else ""
    }

    /** "100.82.1.19", "100.82.1.19:8403" or a full URL all become http://host:port (port defaults to 8403). */
    fun normalize(address: String): String {
        var a = address.trim().trimEnd('/')
        if (a.isEmpty()) return ""
        if (!a.startsWith("http://") && !a.startsWith("https://")) a = "http://$a"
        val hostPort = a.substringAfter("://")
        if (!hostPort.contains(':')) a = "$a:${LucilleHostService.LISTEN_PORT}"
        return a
    }

    private fun prefs(ctx: Context) = ctx.getSharedPreferences(PREFS, Context.MODE_PRIVATE)
}
