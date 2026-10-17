package com.lucille.mobile

import android.app.NotificationManager
import android.content.BroadcastReceiver
import android.content.Context
import android.content.Intent
import android.util.Log
import org.json.JSONObject
import java.net.HttpURLConnection
import java.net.URL

/**
 * One-tap answers on a reminder notification: "+250 ml", "Took it",
 * "Ate something". Each logs straight to the on-phone server's care
 * endpoints (care.go), then dismisses the notification, so the nudge stops
 * without opening the app.
 */
class ReminderActionReceiver : BroadcastReceiver() {
    companion object {
        const val EXTRA_PATH = "path"          // e.g. /api/care/water
        const val EXTRA_BODY = "body"          // JSON body
        const val EXTRA_NOTIF_ID = "notif_id"
        private const val TAG = "ReminderAction"
    }

    override fun onReceive(context: Context, intent: Intent) {
        val path = intent.getStringExtra(EXTRA_PATH) ?: return
        val body = intent.getStringExtra(EXTRA_BODY) ?: "{}"
        val notifId = intent.getIntExtra(EXTRA_NOTIF_ID, -1)
        val pending = goAsync()
        Thread {
            try {
                val c = URL("http://127.0.0.1:${LucilleHostService.LISTEN_PORT}$path").openConnection() as HttpURLConnection
                c.requestMethod = "POST"
                c.connectTimeout = 3000
                c.readTimeout = 3000
                c.doOutput = true
                c.setRequestProperty("Content-Type", "application/json")
                c.outputStream.use { it.write(JSONObject(body).toString().toByteArray()) }
                val ok = c.responseCode in 200..299
                c.disconnect()
                Log.d(TAG, "$path -> ${if (ok) "logged" else "failed"}")
                if (ok && notifId != -1) {
                    context.getSystemService(NotificationManager::class.java).cancel(notifId)
                }
            } catch (e: Exception) {
                Log.e(TAG, "action failed", e)
            } finally {
                pending.finish()
            }
        }.start()
    }
}
