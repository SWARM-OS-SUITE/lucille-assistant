package com.lucille.mobile

import android.os.Bundle
import android.os.Handler
import android.os.Looper
import android.text.InputType
import android.view.Gravity
import android.view.ViewGroup
import android.widget.Button
import android.widget.EditText
import android.widget.LinearLayout
import android.widget.ScrollView
import android.widget.TextView
import androidx.appcompat.app.AppCompatActivity
import androidx.biometric.BiometricManager
import androidx.biometric.BiometricPrompt
import androidx.core.content.ContextCompat
import java.util.Date

/**
 * "Approve sudo from your phone". Pick the number the PC is showing, then
 * confirm with a strong fingerprint/face: the phone signs that exact
 * request with its hardware-backed key (PhoneApprovalKey) and the PC's
 * root-owned helper verifies it. Your PC password stays the fallback.
 * Enrolling is a one-time step: a 6-digit code made on the PC, then the
 * public key it prints is installed on the PC as root by the user.
 */
class SudoActivity : AppCompatActivity() {
    private companion object {
        // Fingerprint/face where the phone has them, otherwise the screen-lock PIN. Must match PhoneApprovalKey.
        const val UNLOCK_METHODS = BiometricManager.Authenticators.BIOMETRIC_STRONG or BiometricManager.Authenticators.DEVICE_CREDENTIAL
    }

    private val ui = Handler(Looper.getMainLooper())
    private lateinit var list: LinearLayout
    private lateinit var statusText: TextView
    private lateinit var codeField: EditText
    private var busy = false
    private var open = false

    private val poll = object : Runnable {
        override fun run() {
            if (!open) return
            refresh()
            ui.postDelayed(this, 1500)
        }
    }

    override fun onCreate(savedInstanceState: Bundle?) {
        super.onCreate(savedInstanceState)
        val pad = (16 * resources.displayMetrics.density).toInt()
        fun text(s: String, size: Float = 13f, bold: Boolean = false) = TextView(this).apply {
            text = s; textSize = size; setTextColor(getColor(R.color.text_primary))
            if (bold) setTypeface(typeface, android.graphics.Typeface.BOLD)
            setPadding(0, pad / 2, 0, 0)
        }
        fun button(s: String, onClick: () -> Unit) = Button(this).apply {
            text = s; setOnClickListener { onClick() }
        }

        statusText = text("", 12f)
        codeField = EditText(this).apply {
            hint = "6-digit code from the PC"
            inputType = InputType.TYPE_CLASS_NUMBER or InputType.TYPE_NUMBER_VARIATION_PASSWORD
            filters = arrayOf(android.text.InputFilter.LengthFilter(6))
        }
        list = LinearLayout(this).apply { orientation = LinearLayout.VERTICAL }

        val root = LinearLayout(this).apply {
            orientation = LinearLayout.VERTICAL
            setPadding(pad, pad * 2, pad, pad)
            setBackgroundColor(getColor(R.color.bg))
            addView(text("Phone approval for sudo", 18f, true))
            addView(text("Only for sudo. Each approval signs one short-lived request after a strong fingerprint or face check. Your PC password stays the fallback."))
            addView(text("Pending requests", 14f, true))
            addView(list)
            addView(text("Enroll this phone (once)", 14f, true))
            addView(text("On the PC run: curl -s http://127.0.0.1:8403/api/unlock/enrol/new   then type the six digits here. Afterwards install the key the PC tells you about, as root. The private key never leaves this phone's secure hardware."))
            addView(codeField)
            addView(button("Enroll") { enroll() })
            addView(button("Reset phone key (needs re-enrolling)") { resetKey() })
            addView(statusText)
        }
        setContentView(ScrollView(this).apply {
            setBackgroundColor(getColor(R.color.bg))
            addView(root, ViewGroup.LayoutParams.MATCH_PARENT, ViewGroup.LayoutParams.WRAP_CONTENT)
            fitSystemBars()
        })
    }

    override fun onResume() {
        super.onResume()
        open = true
        if (!LucilleTarget.paired(this)) {
            statusText.text = "Pair this phone with your PC first (⋮ menu, Which Lucille)."
        } else {
            ui.post(poll)
        }
    }

    override fun onPause() {
        open = false
        ui.removeCallbacks(poll)
        super.onPause()
    }

    private fun say(msg: String) = runOnUiThread { statusText.text = msg }

    private fun refresh() {
        if (busy) return
        Thread {
            val result = SudoApprovalApi.pending(this)
            runOnUiThread {
                if (result is SudoApprovalApi.Outcome.Ok) render(result.value)
            }
        }.start()
    }

    private fun render(requests: List<SudoUnlockRequest>) {
        list.removeAllViews()
        if (requests.isEmpty()) {
            list.addView(TextView(this).apply {
                text = "No sudo approvals waiting."; textSize = 12f; setTextColor(getColor(R.color.text_primary))
            })
            return
        }
        for (r in requests) {
            list.addView(TextView(this).apply {
                text = "${r.user} on ${r.host}\nexpires ${Date(r.expires * 1000)}\nTap only the number the PC is showing:"
                textSize = 13f; setTextColor(getColor(R.color.text_primary)); setPadding(0, 24, 0, 8)
            })
            for (choice in r.choices) {
                list.addView(Button(this).apply {
                    text = choice; textSize = 20f; gravity = Gravity.CENTER
                    setOnClickListener { approve(r, choice) }
                })
            }
        }
    }

    private fun approve(request: SudoUnlockRequest, choice: String) {
        if (busy) return
        if (System.currentTimeMillis() / 1000 > request.expires) { say("That request expired."); return }
        if (BiometricManager.from(this).canAuthenticate(UNLOCK_METHODS) != BiometricManager.BIOMETRIC_SUCCESS) {
            say("Set a screen lock (PIN, pattern or password, or a fingerprint) on this phone before approving sudo.")
            return
        }
        val signature = try {
            PhoneApprovalKey.prepareSignature(SudoUnlockProtocol.message(request, choice))
        } catch (e: Exception) {
            say("Could not prepare the approval signature: ${e.message ?: "Keystore error"}")
            return
        }
        busy = true
        val callback = object : BiometricPrompt.AuthenticationCallback() {
            override fun onAuthenticationSucceeded(result: BiometricPrompt.AuthenticationResult) {
                val signed = try {
                    result.cryptoObject?.signature?.sign()
                } catch (_: Exception) { null }
                if (signed == null) {
                    busy = false
                    say("The phone could not complete the secure signature. Enroll the phone key again if its biometrics changed.")
                    return
                }
                Thread {
                    val out = SudoApprovalApi.respond(this@SudoActivity, request.id, choice, signed)
                    busy = false
                    say(if (out is SudoApprovalApi.Outcome.Ok) "Approval sent. The PC checks the signature and the number."
                        else (out as SudoApprovalApi.Outcome.Fail).message)
                    runOnUiThread { refresh() }
                }.start()
            }

            override fun onAuthenticationError(errorCode: Int, errString: CharSequence) {
                busy = false
                say("Approval was not signed: $errString")
            }
        }
        try {
            BiometricPrompt(this, ContextCompat.getMainExecutor(this), callback).authenticate(
                BiometricPrompt.PromptInfo.Builder()
                    .setTitle("Approve sudo request")
                    .setSubtitle("Confirm with your fingerprint, or your phone PIN")
                    .setAllowedAuthenticators(UNLOCK_METHODS)
                    .build(),
                BiometricPrompt.CryptoObject(signature),
            )
        } catch (e: Exception) {
            busy = false
            say("Biometric approval could not start: ${e.message ?: "unavailable"}")
        }
    }

    private fun enroll() {
        val code = codeField.text.toString()
        if (!LucilleTarget.paired(this)) { say("Pair this phone with your PC first (⋮ menu, Which Lucille)."); return }
        busy = true
        Thread {
            val msg = try {
                PhoneApprovalKey.ensureCreated()
                when (val out = SudoApprovalApi.enroll(this, code, PhoneApprovalKey.publicKeyDer())) {
                    is SudoApprovalApi.Outcome.Ok -> {
                        runOnUiThread { codeField.setText("") }
                        "Phone key enrolled. Now install it on the PC, as root: ${out.value}"
                    }
                    is SudoApprovalApi.Outcome.Fail -> out.message
                }
            } catch (e: Exception) {
                "Could not prepare the secure phone key: ${e.message ?: "Keystore error"}"
            }
            busy = false
            say(msg)
        }.start()
    }

    private fun resetKey() {
        try {
            PhoneApprovalKey.reset()
            say("A new secure key was created. Make a new code on the PC and enroll again; the old PC key will no longer match.")
        } catch (e: Exception) {
            say("Could not reset the secure key: ${e.message ?: "Keystore error"}")
        }
    }
}
