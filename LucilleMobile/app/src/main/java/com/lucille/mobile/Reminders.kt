package com.lucille.mobile

import android.app.NotificationChannel
import android.app.NotificationManager
import android.app.PendingIntent
import android.content.Context
import android.content.Intent
import android.os.Build
import android.util.Log
import org.json.JSONObject
import java.net.HttpURLConnection
import java.net.URL

/**
 * Push reminders. The bundled server already works out what Lucille should
 * nag about right now (meds due, water behind, no food, a long session
 * without a break, sleep: care.go's /api/nudges). This turns each one into
 * a real Android notification, once per cooldown rather than every poll,
 * so the phone buzzes with the PC off, no Firebase needed.
 */
object Reminders {
    private const val TAG = "Reminders"
    private const val CHANNEL_ID = "lucille_reminders"
    private const val PREFS = "reminders"

    // How long before the same kind of reminder may fire again, by severity.
    private fun cooldownMs(severity: String) = when (severity) {
        "critical" -> 30 * 60_000L
        "warn" -> 60 * 60_000L
        else -> 6 * 60 * 60_000L
    }

    private fun title(kind: String) = when (kind) {
        "meds" -> "Medication"
        "water" -> "Water"
        "food" -> "Eat something"
        "sleep" -> "Sleep"
        "break" -> "Take a break"
        else -> "Lucille"
    }

    /** One poll of the local server; posts a notification for each nudge that is out of cooldown. */
    fun checkOnce(ctx: Context) {
        val nudges = fetch() ?: return
        val prefs = ctx.getSharedPreferences(PREFS, Context.MODE_PRIVATE)
        val now = System.currentTimeMillis()
        for (i in 0 until nudges.length()) {
            val n = nudges.getJSONObject(i)
            val kind = n.optString("kind")
            val severity = n.optString("severity", "info")
            val text = n.optString("text")
            if (kind.isEmpty() || text.isEmpty()) continue
            val key = "last_$kind"
            if (now - prefs.getLong(key, 0L) < cooldownMs(severity)) continue
            prefs.edit().putLong(key, now).apply()
            notify(ctx, kind, severity, text)
        }
    }

    private fun fetch() = try {
        val c = URL("http://127.0.0.1:${LucilleHostService.LISTEN_PORT}/api/nudges").openConnection() as HttpURLConnection
        c.connectTimeout = 3000
        c.readTimeout = 3000
        try {
            if (c.responseCode == 200) JSONObject(c.inputStream.bufferedReader().readText()).optJSONArray("nudges") else null
        } finally {
            c.disconnect()
        }
    } catch (e: Exception) {
        Log.d(TAG, "nudges not available yet: ${e.message}")
        null
    }

    private fun notify(ctx: Context, kind: String, severity: String, text: String) {
        val mgr = ctx.getSystemService(NotificationManager::class.java)
        if (Build.VERSION.SDK_INT >= Build.VERSION_CODES.O && mgr.getNotificationChannel(CHANNEL_ID) == null) {
            mgr.createNotificationChannel(
                NotificationChannel(CHANNEL_ID, "Reminders", NotificationManager.IMPORTANCE_HIGH).apply {
                    description = "Medication, water, food, breaks and sleep"
                }
            )
        }
        val open = PendingIntent.getActivity(ctx, 0, Intent(ctx, MainActivity::class.java), PendingIntent.FLAG_IMMUTABLE)
        val id = ("reminder_$kind").hashCode()
        val b = android.app.Notification.Builder(ctx, CHANNEL_ID)
            .setContentTitle(title(kind))
            .setContentText(text)
            .setStyle(android.app.Notification.BigTextStyle().bigText(text))
            .setSmallIcon(R.drawable.ic_stat_lucille)
            .setCategory(android.app.Notification.CATEGORY_REMINDER)
            .setContentIntent(open)
            .setAutoCancel(true)
        // One-tap answers so the nudge can be dealt with without opening the app.
        when (kind) {
            "water" -> b.addAction(action(ctx, id, "+250 ml", "/api/care/water", """{"ml":250}"""))
            "food" -> b.addAction(action(ctx, id, "Ate something", "/api/care/meal", """{"label":"Meal"}"""))
            "meds" -> medName(text)?.let {
                b.addAction(action(ctx, id, "Took it", "/api/care/med", JSONObject().put("name", it).toString()))
            }
        }
        mgr.notify(id, b.build())
        Log.d(TAG, "notified $kind/$severity")
    }

    // The meds nudge reads "<name> were due at HH:MM. Take them, then tap it off."
    private fun medName(text: String): String? =
        Regex("^(.+?) were due at").find(text)?.groupValues?.get(1)

    private fun action(ctx: Context, notifId: Int, label: String, path: String, body: String): android.app.Notification.Action {
        val i = Intent(ctx, ReminderActionReceiver::class.java)
            .putExtra(ReminderActionReceiver.EXTRA_PATH, path)
            .putExtra(ReminderActionReceiver.EXTRA_BODY, body)
            .putExtra(ReminderActionReceiver.EXTRA_NOTIF_ID, notifId)
        val pi = PendingIntent.getBroadcast(ctx, (path + body).hashCode(), i, PendingIntent.FLAG_IMMUTABLE or PendingIntent.FLAG_UPDATE_CURRENT)
        return android.app.Notification.Action.Builder(null as android.graphics.drawable.Icon?, label, pi).build()
    }
}
