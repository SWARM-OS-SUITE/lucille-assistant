package com.lucille.mobile

import android.app.ActivityManager
import android.content.Context
import android.util.Log
import org.json.JSONArray
import org.json.JSONObject
import java.io.File

/**
 * Lucille's on-phone brain: a Qwen3 model served by a bundled llama.cpp `llama-server`
 * (libllama_server.so, built for arm64 and shipped like the Go server) on 127.0.0.1.
 * The bundled Lucille server already talks to any OpenAI-compatible endpoint for its
 * offline chain, so this only starts the model server and points that chain at it.
 *
 * The model files are big, so they are not in the APK: they live in the app's
 * external files folder (Android/data/com.lucille.mobile/files/models) and are
 * put there once. The biggest Qwen3 that fits this phone's RAM is chosen.
 */
object OnDeviceModel {
    private const val TAG = "OnDeviceModel"
    const val PORT = 11434

    /** Best first. [minRamBytes] is the phone's total RAM as Android reports it (a bit under the box number). */
    data class Option(val file: String, val label: String, val minRamBytes: Long)

    val OPTIONS = listOf(
        Option("Qwen3-1.7B-Q4_K_M.gguf", "Qwen3 1.7B", 3_000_000_000L),
        Option("Qwen3-0.6B-Q8_0.gguf", "Qwen3 0.6B", 0L),
    )

    fun modelsDir(ctx: Context): File = ctx.getExternalFilesDir("models") ?: File(ctx.filesDir, "models")

    fun binary(ctx: Context): File = File(ctx.applicationInfo.nativeLibraryDir, "libllama_server.so")

    fun totalRam(ctx: Context): Long =
        ActivityManager.MemoryInfo().also { (ctx.getSystemService(Context.ACTIVITY_SERVICE) as ActivityManager).getMemoryInfo(it) }.totalMem

    /** The best option this phone can hold that is actually on disk, or null. */
    fun choose(ctx: Context): Pair<Option, File>? {
        val ram = totalRam(ctx)
        return OPTIONS.filter { ram >= it.minRamBytes }
            .map { it to File(modelsDir(ctx), it.file) }
            .firstOrNull { it.second.isFile && it.second.length() > 100_000_000L }
    }

    fun command(ctx: Context, model: File, extraThreads: Int = 0): List<String> = listOf(
        binary(ctx).absolutePath,
        "-m", model.absolutePath,
        "--host", "127.0.0.1", "--port", PORT.toString(),
        "-c", "2048", "-t", (4 + extraThreads).toString(), "--parallel", "1",
        // Qwen3 "thinks" out loud by default; the replies are capped at ~120 tokens, so thinking would eat them all.
        "--jinja", "--chat-template-kwargs", "{\"enable_thinking\":false}",
    )

    /** Starts llama-server if a model and the binary are both here. Null otherwise (the server then uses its other brains). */
    fun start(ctx: Context): Process? {
        val bin = binary(ctx)
        if (!bin.exists()) { Log.w(TAG, "llama-server not bundled"); return null }
        val (opt, model) = choose(ctx) ?: run { Log.w(TAG, "no Qwen3 model in ${modelsDir(ctx)}"); return null }
        return try {
            val p = ProcessBuilder(command(ctx, model)).redirectErrorStream(true).start()
            Thread {
                try { p.inputStream.bufferedReader().forEachLine { Log.d(TAG, it) } } catch (_: Exception) {}
            }.apply { isDaemon = true; start() }
            Log.i(TAG, "started ${opt.label} (${model.name})")
            p
        } catch (e: Exception) {
            Log.e(TAG, "couldn't start llama-server", e); null
        }
    }

    /**
     * Points the bundled Lucille server's offline chain at the phone's own model (or clears
     * that when there is none). Keeps every other key in the file, like WellnessConfig does.
     */
    fun configure(ctx: Context) {
        val f = WellnessConfig.file(ctx)
        val j = try { JSONObject(f.readText()) } catch (_: Exception) { JSONObject() }
        val chosen = if (binary(ctx).exists()) choose(ctx) else null
        if (chosen == null) {
            j.remove("local_models")
        } else {
            j.put("local_models", JSONObject()
                .put("enabled", true)
                .put("endpoint", "http://127.0.0.1:$PORT/v1")
                .put("mode", "chain")
                .put("timeout_seconds", 120)   // phone CPUs read slowly; the desktop default of 75 s is too tight
                .put("models", JSONArray().put(JSONObject().put("name", "Qwen").put("model", "qwen3").put("role", "on-phone ${chosen.first.label}"))))
        }
        f.writeText(j.toString(2))
    }
}
