package com.lucille.mobile

import android.app.Notification
import android.app.NotificationChannel
import android.app.NotificationManager
import android.app.PendingIntent
import android.app.Service
import android.content.Intent
import android.os.Build
import android.os.IBinder
import android.util.Log
import java.io.BufferedReader
import java.io.File
import java.io.InputStreamReader
import java.net.NetworkInterface

/**
 * Runs Lucille Assistant's own Go server (the same brain as SWARM OS and
 * Debian, cross-compiled for Android — see app/build.gradle.kts'
 * buildGoServer task) as a foreground service, so it keeps running while
 * the PC is off or updating.
 *
 * The binary is bundled as jniLibs/arm64-v8a/liblucille_mobile.so — the only
 * location Android will execute a bundled file from (app-private copies
 * are blocked by W^X since Android 10). It listens on all interfaces so
 * the PC or other phones can reach it over the LAN/Tailscale, same API as
 * the desktop service.
 */
class LucilleHostService : Service() {
    companion object {
        private const val TAG = "LucilleHostService"
        private const val CHANNEL_ID = "lucille_mobile"
        private const val NOTIF_ID = 1
        const val LISTEN_PORT = 8403
        const val ACTION_STOP = "com.lucille.mobile.action.STOP"
        const val ACTION_RESTART = "com.lucille.mobile.action.RESTART"

        @Volatile var running = false
            private set
    }

    private var process: Process? = null
    private var logThread: Thread? = null
    private var syncThread: Thread? = null
    private var modelProcess: Process? = null
    private var reminderThread: Thread? = null
    private var sudoThread: Thread? = null

    override fun onBind(intent: Intent?): IBinder? = null

    override fun onStartCommand(intent: Intent?, flags: Int, startId: Int): Int {
        if (intent?.action == ACTION_STOP) {
            stopServer()
            stopSelf()
            return START_NOT_STICKY
        }
        startForeground(NOTIF_ID, buildNotification(starting = true))
        if (intent?.action == ACTION_RESTART) {
            // The server reads config.json (wellness settings) only at start.
            process?.let {
                it.destroy()
                it.waitFor(3, java.util.concurrent.TimeUnit.SECONDS) // free the port before the new one binds
            }
            process = null
        }
        if (process == null) startServer()
        return START_STICKY
    }

    override fun onDestroy() {
        stopServer()
        super.onDestroy()
    }

    private fun startServer() {
        val binary = File(applicationInfo.nativeLibraryDir, "liblucille_mobile.so")
        if (!binary.exists()) {
            Log.e(TAG, "server binary missing at ${binary.absolutePath}")
            updateNotification("Install problem: server binary missing")
            return
        }
        val dataDir = File(filesDir, "lucille-data").apply { mkdirs() }
        OnDeviceModel.configure(this)
        if (modelProcess?.isAlive != true) modelProcess = OnDeviceModel.start(this)
        try {
            val pb = ProcessBuilder(binary.absolutePath)
                .redirectErrorStream(true)
            pb.environment()["LUCILLE_ASSISTANT_DATA"] = dataDir.absolutePath
            pb.environment()["LUCILLE_ASSISTANT_LISTEN"] = "0.0.0.0:$LISTEN_PORT"
            pb.environment()["LUCILLE_ASSISTANT_CONFIG"] = WellnessConfig.file(this).absolutePath
            pb.environment().putAll(MemorySync.serverEnv(this))
            process = pb.start()
            running = true
            startSyncLoop()
            startReminderLoop()
            startSudoLoop()
            logThread = Thread {
                try {
                    BufferedReader(InputStreamReader(process!!.inputStream)).use { r ->
                        var line: String?
                        while (r.readLine().also { line = it } != null) {
                            Log.d(TAG, line ?: "")
                        }
                    }
                } catch (_: Exception) {
                    // process ended / stream closed
                }
            }.apply { isDaemon = true; start() }
            updateNotification(null)
        } catch (e: Exception) {
            Log.e(TAG, "failed to start server", e)
            running = false
            updateNotification("Failed to start: ${e.message}")
        }
    }

    // Push/pull the phone-local swarm-memory log against the user's PC every
    // 5 minutes (same cadence as the desktop timer). Does nothing until a
    // peer is configured in the app.
    private fun startSyncLoop() {
        if (syncThread?.isAlive == true) return
        syncThread = Thread {
            try {
                Thread.sleep(10_000)
                while (running) {
                    MemorySync.syncOnce(this)
                    Thread.sleep(5 * 60_000L)
                }
            } catch (_: InterruptedException) {
                // service stopping
            }
        }.apply { isDaemon = true; start() }
    }

    // Check the server's nudges (meds, water, food, breaks, sleep) once a
    // minute and raise a notification for each that is out of cooldown.
    private fun startReminderLoop() {
        if (reminderThread?.isAlive == true) return
        reminderThread = Thread {
            try {
                Thread.sleep(20_000)
                while (running) {
                    Reminders.checkOnce(this)
                    Thread.sleep(60_000L)
                }
            } catch (_: InterruptedException) {
                // service stopping
            }
        }.apply { isDaemon = true; start() }
    }

    // Watch the PC for sudo approval requests (only does work once this phone
    // is paired with the PC and has enrolled its approval key).
    private fun startSudoLoop() {
        if (sudoThread?.isAlive == true) return
        sudoThread = Thread {
            try {
                while (running) {
                    SudoWatcher.checkOnce(this)
                    Thread.sleep(5_000L)
                }
            } catch (_: InterruptedException) {
                // service stopping
            }
        }.apply { isDaemon = true; start() }
    }

    private fun stopServer() {
        running = false
        sudoThread?.interrupt()
        sudoThread = null
        syncThread?.interrupt()
        syncThread = null
        reminderThread?.interrupt()
        reminderThread = null
        process?.destroy()
        process = null
        modelProcess?.destroy()
        modelProcess = null
    }

    private fun buildNotification(starting: Boolean = false, errorText: String? = null): Notification {
        if (Build.VERSION.SDK_INT >= Build.VERSION_CODES.O) {
            val mgr = getSystemService(NotificationManager::class.java)
            if (mgr.getNotificationChannel(CHANNEL_ID) == null) {
                mgr.createNotificationChannel(
                    NotificationChannel(CHANNEL_ID, getString(R.string.notif_channel_name), NotificationManager.IMPORTANCE_LOW)
                )
            }
        }
        val openIntent = PendingIntent.getActivity(
            this, 0,
            Intent(this, MainActivity::class.java),
            PendingIntent.FLAG_IMMUTABLE,
        )
        val text = when {
            errorText != null -> errorText
            starting -> getString(R.string.notif_text_starting)
            else -> getString(R.string.notif_text_running, "${localIpAddress() ?: "this device"}:$LISTEN_PORT")
        }
        return Notification.Builder(this, CHANNEL_ID)
            .setContentTitle(getString(R.string.notif_title))
            .setContentText(text)
            .setSmallIcon(R.drawable.ic_stat_lucille)
            .setContentIntent(openIntent)
            .setOngoing(true)
            .build()
    }

    private fun updateNotification(errorText: String?) {
        val mgr = getSystemService(NotificationManager::class.java)
        mgr.notify(NOTIF_ID, buildNotification(errorText = errorText))
    }

    private fun localIpAddress(): String? {
        return try {
            NetworkInterface.getNetworkInterfaces().asSequence()
                .flatMap { it.inetAddresses.asSequence() }
                .firstOrNull { !it.isLoopbackAddress && it.hostAddress?.contains(':') == false }
                ?.hostAddress
        } catch (_: Exception) {
            null
        }
    }
}
