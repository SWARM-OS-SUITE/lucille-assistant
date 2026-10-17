package com.lucille.mobile

import android.Manifest
import android.content.Intent
import android.content.pm.PackageManager
import android.os.Build
import android.os.Bundle
import android.os.Handler
import android.os.Looper
import android.webkit.WebResourceError
import android.webkit.WebResourceRequest
import android.webkit.WebView
import android.webkit.WebViewClient
import android.text.InputType
import android.widget.Button
import android.widget.EditText
import android.widget.LinearLayout
import android.widget.TextView
import android.widget.Toast
import androidx.appcompat.app.AlertDialog
import androidx.appcompat.app.AppCompatActivity
import androidx.core.app.ActivityCompat

/**
 * Starts the Lucille Mobile service, then loads the server's own web UI
 * (the same Team Brief / threads / games page it serves on SWARM OS and
 * Debian) once it's answering. No separate native screens needed for v1 --
 * the server already has a full UI.
 */
class MainActivity : AppCompatActivity() {
    private val handler = Handler(Looper.getMainLooper())
    private var attempts = 0
    private val maxAttempts = 30 // ~15s at 500ms apart

    override fun onCreate(savedInstanceState: Bundle?) {
        super.onCreate(savedInstanceState)
        setContentView(R.layout.activity_main)

        if (Build.VERSION.SDK_INT >= Build.VERSION_CODES.TIRAMISU &&
            ActivityCompat.checkSelfPermission(this, Manifest.permission.POST_NOTIFICATIONS)
                != PackageManager.PERMISSION_GRANTED
        ) {
            ActivityCompat.requestPermissions(this, arrayOf(Manifest.permission.POST_NOTIFICATIONS), 1)
        }

        startForegroundService(Intent(this, LucilleHostService::class.java))

        findViewById<android.view.View>(R.id.root).fitSystemBars()
        val webView = findViewById<WebView>(R.id.webView)
        webView.settings.javaScriptEnabled = true
        webView.settings.domStorageEnabled = true
        // The Games tab's HUD opens with a swipe from the left edge, which Android's back gesture
        // would otherwise take. Android allows at most 200dp of exclusion per edge: keep the middle.
        webView.addOnLayoutChangeListener { v, l, t, r, b, _, _, _, _ ->
            val d = resources.displayMetrics.density
            val h = b - t
            val strip = (200 * d).toInt().coerceAtMost(h)
            val top = ((h - strip) / 2).coerceAtLeast(0)
            v.systemGestureExclusionRects = listOf(android.graphics.Rect(0, top, (48 * d).toInt(), top + strip))
        }
        webView.webViewClient = object : WebViewClient() {
            // Lucille Mobile's face is the purple electricity goddess. The
            // server's web page (shared with the desktop Assistant) shows
            // Lucille's brown-haired sigil, so swap it to the goddess art
            // the same server already ships in its assets.
            override fun onPageCommitVisible(view: WebView, url: String?) = applyTheme(view)

            override fun onPageFinished(view: WebView, url: String?) {
                applyTheme(view)
                view.evaluateJavascript(
                    """(function(){var a='assets/lucilla_ai_goddess.png';
                    var s=document.getElementById('sigil'); if(s) s.src=a;
                    document.querySelectorAll('.nav-brand-img').forEach(function(i){i.src=a;});})();""",
                    null,
                )
            }

            override fun onReceivedError(
                view: WebView,
                request: WebResourceRequest,
                error: WebResourceError,
            ) {
                if (request.isForMainFrame) scheduleRetry()
            }
        }
        findViewById<Button>(R.id.menuButton).setOnClickListener { showMenu() }
        findViewById<Button>(R.id.targetButton).setOnClickListener { showTargetDialog() }
        updateTargetButton()
        scheduleRetry(immediate = true)
    }

    private fun showMenu() {
        val items = arrayOf("Which Lucille? (phone or PC)", "Wellness reminders", "Approve sudo from this phone", "Shared memory sync", "Private memory", "Sudo code (phone plugged in)")
        AlertDialog.Builder(this)
            .setItems(items) { _, which ->
                when (which) {
                    0 -> showTargetDialog()
                    1 -> showWellnessDialog()
                    2 -> startActivity(Intent(this, SudoActivity::class.java))
                    3 -> showMemoryDialog()
                    4 -> startActivity(Intent(this, PrivateMemoryActivity::class.java))
                    5 -> startActivity(Intent(this, SudoCodeActivity::class.java))
                }
            }
            .show()
    }

    private fun updateTargetButton() {
        findViewById<Button>(R.id.targetButton).text = if (LucilleTarget.isPc(this)) "On PC" else "On phone"
    }

    // Choose which Lucille the page shows: this phone's own (works with the
    // PC off) or the PC's (briefings, Approve-and-start, the gaming PC's
    // telemetry). The PC's page does its own pairing.
    private fun showTargetDialog() {
        val pad = (16 * resources.displayMetrics.density).toInt()
        val address = EditText(this).apply {
            hint = "PC address, e.g. 100.82.1.19 (port 8403)"
            setText(LucilleTarget.pcUrl(this@MainActivity))
            inputType = InputType.TYPE_TEXT_VARIATION_URI
        }
        val code = EditText(this).apply {
            hint = "Pairing code (first time only)"
            inputType = InputType.TYPE_CLASS_NUMBER
        }
        val box = LinearLayout(this).apply {
            orientation = LinearLayout.VERTICAL
            setPadding(pad, pad, pad, 0)
            addView(address)
            addView(code)
        }
        AlertDialog.Builder(this)
            .setTitle("Which Lucille?")
            .setMessage("On this phone: works with the PC off. On your PC: briefings, Approve and start, and the gaming PC's telemetry. To pair, make a 6-digit code on the PC (run lucille-assistant pair) and type it here once.")
            .setView(box)
            .setPositiveButton("Use my PC") { _, _ ->
                if (address.text.isNotBlank()) {
                    LucilleTarget.usePc(this, address.text.toString())
                    val c = code.text.toString()
                    if (c.isBlank()) {
                        switchTarget()
                    } else {
                        Thread {
                            val err = LucilleTarget.pair(this, c)
                            runOnUiThread {
                                if (err != null) Toast.makeText(this, err, Toast.LENGTH_LONG).show()
                                switchTarget()
                            }
                        }.start()
                    }
                }
            }
            .setNeutralButton("Use this phone") { _, _ ->
                LucilleTarget.usePhone(this)
                switchTarget()
            }
            .setNegativeButton("Cancel", null)
            .show()
    }

    private fun switchTarget() {
        updateTargetButton()
        attempts = 0
        scheduleRetry(immediate = true)
    }

    // Mobile-only clean look (assets/mobile-theme.css) laid over the shared page.
    private val themeCss: String by lazy {
        try { assets.open("mobile-theme.css").bufferedReader().use { it.readText() } } catch (_: Exception) { "" }
    }

    private fun applyTheme(view: WebView) {
        if (themeCss.isEmpty()) return
        view.evaluateJavascript(
            """(function(){var id='lucille-mobile-theme';var s=document.getElementById(id);
            if(!s){s=document.createElement('style');s.id=id;(document.head||document.documentElement).appendChild(s);}
            s.textContent=${org.json.JSONObject.quote(themeCss)};})();""",
            null,
        )
    }

    // What the reminders nag about: meds, wake/bed time, water goal. Saved to
    // the server's config.json; the server re-reads it when it restarts.
    private fun showWellnessDialog() {
        val pad = (16 * resources.displayMetrics.density).toInt()
        val cur = WellnessConfig.read(this)
        fun field(hint: String, value: String, type: Int = InputType.TYPE_CLASS_TEXT) =
            EditText(this).apply { this.hint = hint; setText(value); inputType = type }
        val wake = field("Wake time (08:00)", cur.wake)
        val bed = field("Bed time (00:30)", cur.bed)
        val water = field("Water goal in ml (2500)", cur.waterGoalMl.toString(), InputType.TYPE_CLASS_NUMBER)
        val meds = field("Meds, one per line: Morning meds 09:00", cur.meds,
            InputType.TYPE_CLASS_TEXT or InputType.TYPE_TEXT_FLAG_MULTI_LINE).apply { minLines = 3 }
        val box = LinearLayout(this).apply {
            orientation = LinearLayout.VERTICAL
            setPadding(pad, pad, pad, 0)
            addView(wake); addView(bed); addView(water); addView(meds)
        }
        AlertDialog.Builder(this)
            .setTitle("Wellness reminders")
            .setMessage("Lucille reminds you about meds, water, food, breaks and sleep. These times decide when.")
            .setView(box)
            .setPositiveButton("Save") { _, _ ->
                val err = WellnessConfig.write(this, wake.text.toString(), bed.text.toString(), water.text.toString(), meds.text.toString())
                if (err != null) {
                    Toast.makeText(this, err, Toast.LENGTH_LONG).show()
                } else {
                    startForegroundService(Intent(this, LucilleHostService::class.java).setAction(LucilleHostService.ACTION_RESTART))
                    Toast.makeText(this, "Saved. Restarting Lucille…", Toast.LENGTH_SHORT).show()
                    attempts = 0
                    scheduleRetry()
                }
            }
            .setNegativeButton("Cancel", null)
            .show()
    }

    private fun showMemoryDialog() {
        val pad = (16 * resources.displayMetrics.density).toInt()
        val peer = MemorySync.peer(this)
        val info = TextView(this).apply {
            textSize = 13f
            text = "Device: ${MemorySync.deviceId(this@MainActivity)}\n\n" +
                if (peer.isBlank()) "Not connected. Pair this phone with your PC first: menu → Which Lucille? → PC address + the 6-digit code."
                else "Syncing with $peer (paired) every 5 minutes. No token needed."
        }
        val box = LinearLayout(this).apply {
            orientation = LinearLayout.VERTICAL
            setPadding(pad, pad, pad, 0)
            addView(info)
        }
        val b = AlertDialog.Builder(this)
            .setTitle("Shared memory sync")
            .setView(box)
            .setNegativeButton("Close", null)
        if (peer.isNotBlank()) b.setPositiveButton("Sync now") { _, _ ->
            Thread {
                val out = MemorySync.syncOnce(this) ?: "Not paired with a PC yet."
                runOnUiThread { Toast.makeText(this, out.take(200), Toast.LENGTH_LONG).show() }
            }.start()
        } else b.setPositiveButton("Pair now") { _, _ -> showTargetDialog() }
        b.show()
    }

    private fun scheduleRetry(immediate: Boolean = false) {
        val status = findViewById<TextView>(R.id.statusText)
        val pc = LucilleTarget.isPc(this)
        if (attempts >= maxAttempts) {
            status.text = if (pc) "Can't reach your PC's Lucille. Tap \"On PC\" to switch back to this phone."
            else "Lucille didn't come up. Check the notification for details."
            return
        }
        attempts++
        status.text = if (pc) "Connecting to your PC… ($attempts)" else "Starting Lucille… ($attempts)"
        handler.postDelayed({ tryLoad() }, if (immediate) 300 else 500)
    }

    private fun tryLoad() {
        val webView = findViewById<WebView>(R.id.webView)
        val status = findViewById<TextView>(R.id.statusText)
        if (LucilleTarget.isPc(this)) {
            // The PC's server is already up (or isn't); no local service to wait for.
            status.text = "Lucille on your PC: ${LucilleTarget.pcUrl(this)}"
            webView.loadUrl(LucilleTarget.url(this))
        } else if (LucilleHostService.running) {
            status.text = "Lucille is running on this phone (port ${LucilleHostService.LISTEN_PORT})"
            webView.loadUrl(LucilleTarget.url(this))
        } else {
            scheduleRetry()
        }
    }
}
