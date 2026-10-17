package com.lucille.mobile

import android.os.Bundle
import android.os.Handler
import android.os.Looper
import android.text.InputType
import android.view.Gravity
import android.view.WindowManager
import android.widget.Button
import android.widget.EditText
import android.widget.LinearLayout
import android.widget.ProgressBar
import android.widget.TextView
import androidx.appcompat.app.AlertDialog
import androidx.appcompat.app.AppCompatActivity
import androidx.biometric.BiometricManager
import androidx.biometric.BiometricPrompt
import androidx.core.content.ContextCompat

/**
 * "Sudo code": the rotating 6-digit code to type at the PC's sudo prompt while this phone is plugged into it.
 * Behind the phone's fingerprint or PIN every time the screen opens, hidden from screenshots, nothing sent anywhere.
 */
class SudoCodeActivity : AppCompatActivity() {
    private val ui = Handler(Looper.getMainLooper())
    private var secret: ByteArray? = null
    private lateinit var codeView: TextView
    private lateinit var leftView: TextView
    private lateinit var bar: ProgressBar
    private lateinit var root: LinearLayout

    private val tick = object : Runnable {
        override fun run() {
            val s = secret ?: return
            val now = System.currentTimeMillis() / 1000
            val c = Totp.code(s, now)
            codeView.text = c.substring(0, 3) + " " + c.substring(3)
            val left = Totp.secondsLeft(now)
            leftView.text = "changes in ${left}s"
            bar.progress = left
            ui.postDelayed(this, 500)
        }
    }

    override fun onCreate(savedInstanceState: Bundle?) {
        super.onCreate(savedInstanceState)
        window.setFlags(WindowManager.LayoutParams.FLAG_SECURE, WindowManager.LayoutParams.FLAG_SECURE)
        val pad = (20 * resources.displayMetrics.density).toInt()
        root = LinearLayout(this).apply {
            orientation = LinearLayout.VERTICAL; gravity = Gravity.CENTER_HORIZONTAL
            setPadding(pad, pad, pad, pad); setBackgroundColor(getColor(R.color.bg)); fitSystemBars()
        }
        setContentView(root)
    }

    override fun onResume() {
        super.onResume()
        secret = null; ui.removeCallbacks(tick)
        if (!SudoCodeStore.exists(this)) showSetup() else unlock()
    }

    override fun onPause() { ui.removeCallbacks(tick); secret = null; super.onPause() }

    private fun label(s: String, size: Float = 14f) = TextView(this).apply {
        text = s; textSize = size; setTextColor(getColor(R.color.text_primary)); setPadding(0, 16, 0, 0); gravity = Gravity.CENTER
    }

    private fun showSetup() {
        root.removeAllViews()
        root.addView(label("Sudo code", 22f))
        root.addView(label("Type the secret the PC printed when you ran setup-usb-sudo.sh. It is stored encrypted on this phone and never sent anywhere."))
        val field = EditText(this).apply { hint = "WMK6 CWWW 5HDQ …"; inputType = InputType.TYPE_CLASS_TEXT or InputType.TYPE_TEXT_FLAG_NO_SUGGESTIONS or InputType.TYPE_TEXT_FLAG_CAP_CHARACTERS }
        val msg = label("", 13f)
        root.addView(field)
        root.addView(Button(this).apply {
            text = "Save"
            setOnClickListener {
                val err = SudoCodeStore.save(this@SudoCodeActivity, field.text.toString())
                if (err != null) msg.text = err else { field.setText(""); unlock() }
            }
        })
        root.addView(msg)
    }

    private fun unlock() {
        val methods = BiometricManager.Authenticators.BIOMETRIC_STRONG or BiometricManager.Authenticators.DEVICE_CREDENTIAL
        root.removeAllViews()
        root.addView(label("Sudo code", 22f))
        if (BiometricManager.from(this).canAuthenticate(methods) != BiometricManager.BIOMETRIC_SUCCESS) {
            root.addView(label("Set a screen lock (PIN, pattern, password or fingerprint) on this phone first. The code only shows behind it."))
            return
        }
        val cb = object : BiometricPrompt.AuthenticationCallback() {
            override fun onAuthenticationSucceeded(result: BiometricPrompt.AuthenticationResult) = showCode()
            override fun onAuthenticationError(errorCode: Int, errString: CharSequence) {
                root.removeAllViews(); root.addView(label("Sudo code", 22f)); root.addView(label("Locked. $errString"))
                root.addView(Button(this@SudoCodeActivity).apply { text = "Unlock"; setOnClickListener { unlock() } })
            }
        }
        BiometricPrompt(this, ContextCompat.getMainExecutor(this), cb).authenticate(
            BiometricPrompt.PromptInfo.Builder().setTitle("Show sudo code").setSubtitle("Fingerprint or phone PIN").setAllowedAuthenticators(methods).build()
        )
    }

    private fun showCode() {
        secret = SudoCodeStore.load(this) ?: run {
            root.removeAllViews(); root.addView(label("Couldn't read the stored secret. Reset it and enter it again.")); resetButton(); return
        }
        root.removeAllViews()
        root.addView(label("Sudo code", 22f))
        root.addView(label("Type this at the PC's sudo prompt while this phone is plugged in."))
        codeView = TextView(this).apply { textSize = 52f; setTextColor(getColor(R.color.accent)); typeface = android.graphics.Typeface.MONOSPACE; gravity = Gravity.CENTER; setPadding(0, 48, 0, 16) }
        leftView = label("", 13f)
        bar = ProgressBar(this, null, android.R.attr.progressBarStyleHorizontal).apply { max = Totp.STEP }
        root.addView(codeView); root.addView(bar); root.addView(leftView)
        resetButton()
        tick.run()
    }

    private fun resetButton() = root.addView(Button(this).apply {
        text = "Reset secret"
        setOnClickListener {
            AlertDialog.Builder(this@SudoCodeActivity).setMessage("Forget the secret on this phone? You'll need to type it in again.")
                .setPositiveButton("Reset") { _, _ -> SudoCodeStore.clear(this@SudoCodeActivity); onResume() }.setNegativeButton("Keep", null).show()
        }
    })
}
