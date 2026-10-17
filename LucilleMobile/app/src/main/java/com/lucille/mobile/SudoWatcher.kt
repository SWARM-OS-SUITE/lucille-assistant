package com.lucille.mobile

import android.app.Notification
import android.app.NotificationChannel
import android.app.NotificationManager
import android.app.PendingIntent
import android.content.Context
import android.content.Intent
import android.os.Build

/**
 * Raises a notification the moment the PC asks for a sudo approval, so the
 * request can be answered from the lock screen route instead of the PC
 * password. Does nothing unless this phone is paired with the PC AND has
 * enrolled its approval key. Tapping opens SudoActivity, where the number
 * is picked and the biometric confirmed; nothing is approved from the
 * notification itself.
 */
object SudoWatcher {
    private const val CHANNEL_ID = "lucille_sudo"
    private val seen = HashSet<String>()

    fun checkOnce(ctx: Context) {
        if (!LucilleTarget.paired(ctx) || !PhoneApprovalKey.hasKey()) return
        val out = SudoApprovalApi.pending(ctx) as? SudoApprovalApi.Outcome.Ok ?: return
        for (r in out.value) {
            if (seen.add(r.id)) notify(ctx, r)
        }
        if (seen.size > 200) seen.clear() // ids are random; stale ones can't repeat
    }

    private fun notify(ctx: Context, r: SudoUnlockRequest) {
        val mgr = ctx.getSystemService(NotificationManager::class.java)
        if (Build.VERSION.SDK_INT >= Build.VERSION_CODES.O && mgr.getNotificationChannel(CHANNEL_ID) == null) {
            mgr.createNotificationChannel(
                NotificationChannel(CHANNEL_ID, "Sudo approvals", NotificationManager.IMPORTANCE_HIGH).apply {
                    description = "When your PC asks you to approve sudo"
                }
            )
        }
        val open = PendingIntent.getActivity(ctx, 1, Intent(ctx, SudoActivity::class.java), PendingIntent.FLAG_IMMUTABLE)
        val secondsLeft = (r.expires - System.currentTimeMillis() / 1000).coerceAtLeast(1)
        val n = Notification.Builder(ctx, CHANNEL_ID)
            .setContentTitle("Approve sudo on ${r.host}?")
            .setContentText("${r.user} · tap to pick the number the PC shows")
            .setSmallIcon(R.drawable.ic_stat_lucille)
            .setCategory(Notification.CATEGORY_ALARM)
            .setContentIntent(open)
            .setAutoCancel(true)
            .setTimeoutAfter(secondsLeft * 1000)
            .build()
        mgr.notify(("sudo_" + r.id).hashCode(), n)
    }
}
