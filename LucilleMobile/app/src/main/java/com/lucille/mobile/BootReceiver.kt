package com.lucille.mobile

import android.content.BroadcastReceiver
import android.content.Context
import android.content.Intent

/** Starts Lucille Mobile automatically at boot, so she's available without the user opening the app. */
class BootReceiver : BroadcastReceiver() {
    override fun onReceive(context: Context, intent: Intent) {
        if (intent.action != Intent.ACTION_BOOT_COMPLETED) return
        val serviceIntent = Intent(context, LucilleHostService::class.java)
        context.startForegroundService(serviceIntent)
    }
}
