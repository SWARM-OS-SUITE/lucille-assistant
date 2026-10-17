package com.lucille.mobile

import android.content.Context
import android.os.Build
import android.provider.Settings
import android.util.Log
import java.io.BufferedReader
import java.io.File
import java.io.InputStreamReader

/**
 * Shared-memory wiring for Lucille Mobile: the bundled swarm-memory CLI
 * (libswarm_memory.so) gets a phone-local log under app storage, and
 * `swarm-memory sync` pushes/pulls it against the user's PC (LucyServer's
 * /api/memory/sync) so what's said on the phone reaches Lucille everywhere.
 *
 * No swarm token: the phone syncs with the PC it was paired with (the one-time code
 * from "Which Lucille?"), presenting that pairing's device token. The PC's Lucille
 * Assistant relays /api/memory/sync to LucyServer for paired devices only.
 */
object MemorySync {
    private const val TAG = "MemorySync"

    fun binary(ctx: Context): File = File(ctx.applicationInfo.nativeLibraryDir, "libswarm_memory.so")

    fun dir(ctx: Context): File = File(ctx.filesDir, "swarm-memory").apply { mkdirs() }

    /** phone-live-<model>-<android id prefix>; swarm-memory device ids allow [A-Za-z0-9._@-], max 64. */
    fun deviceId(ctx: Context): String {
        val androidId = Settings.Secure.getString(ctx.contentResolver, Settings.Secure.ANDROID_ID) ?: "unknown"
        val model = Build.MODEL.replace(Regex("[^A-Za-z0-9._-]"), "-")
        return "phone-live-$model-${androidId.take(6)}".take(64)
    }

    /** The paired PC this phone syncs with, or "" when it hasn't been paired. */
    fun peer(ctx: Context): String = if (LucilleTarget.syncToken(ctx).isBlank()) "" else LucilleTarget.syncUrl(ctx)

    /** Env that lets the server process's own swarm-memory calls find the bundled CLI and the phone-local log. */
    fun serverEnv(ctx: Context): Map<String, String> = mapOf(
        "SWARM_MEMORY_BIN" to binary(ctx).absolutePath,
        "SWARM_MEMORY_DIR" to dir(ctx).absolutePath,
        "SWARM_DEVICE_ID" to deviceId(ctx),
    )

    /** One `swarm-memory sync` against the paired PC. No-op (returns null) until this phone is paired. */
    fun syncOnce(ctx: Context): String? {
        val peers = peer(ctx)
        val bin = binary(ctx)
        if (peers.isBlank() || !bin.exists()) return null
        return try {
            val pb = ProcessBuilder(bin.absolutePath, "sync").redirectErrorStream(true)
            pb.environment().putAll(serverEnv(ctx))
            pb.environment()["SWARM_MEMORY_PEERS"] = peers
            pb.environment()["SWARM_AUTH_TOKEN"] = LucilleTarget.syncToken(ctx)
            val p = pb.start()
            val out = BufferedReader(InputStreamReader(p.inputStream)).readText().trim()
            val code = p.waitFor()
            Log.d(TAG, "sync exit=$code: $out")
            out
        } catch (e: Exception) {
            Log.e(TAG, "sync failed", e)
            "sync failed: ${e.message}"
        }
    }
}
