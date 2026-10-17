package com.lucille.app

import android.content.ClipboardManager
import android.content.Context
import android.os.SystemClock
import android.widget.Toast
import androidx.activity.ComponentActivity
import androidx.compose.runtime.Composable
import androidx.compose.runtime.DisposableEffect
import androidx.compose.runtime.getValue
import androidx.compose.runtime.rememberCoroutineScope
import androidx.compose.runtime.rememberUpdatedState
import androidx.compose.ui.platform.LocalContext
import androidx.lifecycle.Lifecycle
import kotlinx.coroutines.launch
import org.json.JSONObject

// ─── Copy here, paste on the PC ───────────────────────────────────────────────────────────────
// User 2026-10-15: "if I copy on her app it should be on my PC keyboard". Anything copied while
// this app is in front (a Copy button, or text selected and copied) is also sent to the paired
// PC's clipboard through POST /api/clipboard (clipboard.go), so Ctrl+V on the PC pastes it.
// One direction only: the phone never reads the PC clipboard. Copies made by other apps are never
// sent: the listener only acts while this app is resumed, which on Android 10+ is also the only
// time it may read the clipboard at all. Clips Android marks sensitive are skipped.

private const val SENSITIVE_EXTRA = "android.content.extra.IS_SENSITIVE"
private const val REPEAT_WINDOW_MS = 3_000L

@Composable
fun PcClipboardBridge(serverHost: String, token: String) {
    val context = LocalContext.current
    val scope = rememberCoroutineScope()
    val host by rememberUpdatedState(serverHost)
    val tok by rememberUpdatedState(token)

    DisposableEffect(context) {
        val cm = context.getSystemService(Context.CLIPBOARD_SERVICE) as ClipboardManager
        var lastText = ""
        var lastAt = 0L
        val listener = ClipboardManager.OnPrimaryClipChangedListener {
            val resumed = (context as? ComponentActivity)?.lifecycle?.currentState?.isAtLeast(Lifecycle.State.RESUMED) == true
            if (!resumed || host.isBlank()) return@OnPrimaryClipChangedListener
            val clip = try { cm.primaryClip } catch (_: SecurityException) { null } ?: return@OnPrimaryClipChangedListener
            if (clip.description?.extras?.getBoolean(SENSITIVE_EXTRA) == true || clip.itemCount == 0) return@OnPrimaryClipChangedListener
            val text = clip.getItemAt(0).coerceToText(context)?.toString().orEmpty()
            if (text.isBlank()) return@OnPrimaryClipChangedListener
            // Some copy paths fire the listener twice for one copy.
            val now = SystemClock.elapsedRealtime()
            if (text == lastText && now - lastAt < REPEAT_WINDOW_MS) return@OnPrimaryClipChangedListener
            lastText = text
            lastAt = now
            scope.launch {
                val msg = when (val r = sendToPcClipboard(host, tok, text)) {
                    is Outcome.Ok -> if (r.value > 0) "📋 On the PC clipboard too (${r.value} key-looking part${if (r.value == 1) "" else "s"} blanked out)" else "📋 On the PC clipboard too"
                    is Outcome.Fail -> "Copied on the phone only: ${r.message}"
                }
                Toast.makeText(context, msg, Toast.LENGTH_SHORT).show()
            }
        }
        cm.addPrimaryClipChangedListener(listener)
        onDispose { cm.removePrimaryClipChangedListener(listener) }
    }
}

/** Ok(number of key-shaped parts the PC blanked out) or Fail(why it isn't on the PC). */
suspend fun sendToPcClipboard(base: String, token: String, text: String): Outcome<Int> {
    val r = lucilleRequest(base, token, "POST", "/api/clipboard", JSONObject().put("text", text))
        ?: return Outcome.Fail("the PC isn't reachable")
    val j = r.json
    return when {
        r.code in 200..299 && j?.optBoolean("ok") == true -> Outcome.Ok(j.optInt("redacted"))
        r.code == 401 -> Outcome.Fail("this phone isn't paired")
        r.code == 404 -> Outcome.Fail("the PC's Lucille is too old for this")
        else -> Outcome.Fail(j?.optString("error").orEmpty().ifBlank { "the PC said no (HTTP ${r.code})" })
    }
}
