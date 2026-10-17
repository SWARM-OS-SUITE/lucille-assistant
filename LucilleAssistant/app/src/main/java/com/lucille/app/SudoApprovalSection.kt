package com.lucille.app

import androidx.biometric.BiometricManager
import androidx.biometric.BiometricPrompt
import androidx.compose.foundation.BorderStroke
import androidx.compose.foundation.layout.Arrangement
import androidx.compose.foundation.layout.Column
import androidx.compose.foundation.layout.Row
import androidx.compose.foundation.layout.fillMaxWidth
import androidx.compose.foundation.layout.padding
import androidx.compose.foundation.shape.RoundedCornerShape
import androidx.compose.foundation.text.KeyboardOptions
import androidx.compose.material3.Button
import androidx.compose.material3.ButtonDefaults
import androidx.compose.material3.Card
import androidx.compose.material3.CardDefaults
import androidx.compose.material3.OutlinedTextField
import androidx.compose.material3.OutlinedTextFieldDefaults
import androidx.compose.material3.Text
import androidx.compose.material3.TextButton
import androidx.compose.runtime.Composable
import androidx.compose.runtime.LaunchedEffect
import androidx.compose.runtime.getValue
import androidx.compose.runtime.mutableStateListOf
import androidx.compose.runtime.mutableStateOf
import androidx.compose.runtime.remember
import androidx.compose.runtime.rememberCoroutineScope
import androidx.compose.runtime.saveable.rememberSaveable
import androidx.compose.runtime.setValue
import androidx.compose.ui.Modifier
import androidx.compose.ui.platform.LocalContext
import androidx.compose.ui.text.font.FontWeight
import androidx.compose.ui.text.input.KeyboardType
import androidx.compose.ui.unit.dp
import androidx.compose.ui.unit.sp
import androidx.core.content.ContextCompat
import androidx.fragment.app.FragmentActivity
import kotlinx.coroutines.delay
import kotlinx.coroutines.launch
import java.util.Date

@Composable
fun SudoApprovalSection(palette: DashboardPalette, serverHost: String, serverToken: String) {
    val context = LocalContext.current
    val activity = context as? FragmentActivity
    val scope = rememberCoroutineScope()
    var enrollmentCode by rememberSaveable { mutableStateOf("") }
    var status by remember { mutableStateOf<String?>(null) }
    var statusError by remember { mutableStateOf(false) }
    var busy by remember { mutableStateOf(false) }
    val requests = remember { mutableStateListOf<SudoUnlockRequest>() }
    val accent = palette.pink

    fun refresh() {
        if (busy) return
        scope.launch {
            busy = true
            when (val result = SudoApprovalApi.pending(serverHost, serverToken)) {
                is Outcome.Ok -> {
                    requests.clear()
                    requests.addAll(result.value)
                    status = null
                }
                is Outcome.Fail -> {
                    statusError = true
                    status = result.message
                }
            }
            busy = false
        }
    }

    LaunchedEffect(serverHost, serverToken) {
        if (serverToken.isNotBlank()) {
            while (true) {
                when (val result = SudoApprovalApi.pending(serverHost, serverToken)) {
                    is Outcome.Ok -> {
                        requests.clear()
                        requests.addAll(result.value)
                    }
                    is Outcome.Fail -> Unit // keep the last good view; show errors on explicit refresh
                }
                delay(1500)
            }
        }
    }

    Card(
        colors = CardDefaults.cardColors(containerColor = palette.panel),
        shape = RoundedCornerShape(16.dp),
        border = BorderStroke(1.dp, accent.copy(alpha = 0.4f)),
        modifier = Modifier.fillMaxWidth()
    ) {
        Column(
            modifier = Modifier.padding(14.dp),
            verticalArrangement = Arrangement.spacedBy(9.dp)
        ) {
            Text("📱 Phone approval for sudo", color = accent, fontSize = 14.sp, fontWeight = FontWeight.Bold)
            Text(
                "This is only for sudo. Each approval signs one short-lived request after a strong fingerprint or face biometric. Your PC password remains the fallback.",
                color = palette.smoke, fontSize = 10.sp
            )

            Text("Enroll this phone", color = palette.text, fontSize = 12.sp, fontWeight = FontWeight.SemiBold)
            Text(
                "On the PC, create a one-time code with: curl -s http://127.0.0.1:8403/api/unlock/enrol/new. Enter its six digits here. After enrollment, install the staged public key on the PC as root; the private key stays in Android Keystore.",
                color = palette.smoke, fontSize = 10.sp
            )
            Row(horizontalArrangement = Arrangement.spacedBy(8.dp)) {
                OutlinedTextField(
                    value = enrollmentCode,
                    onValueChange = { enrollmentCode = it.filter(Char::isDigit).take(6) },
                    placeholder = { Text("6-digit PC code", color = palette.smoke) },
                    singleLine = true,
                    keyboardOptions = KeyboardOptions(keyboardType = KeyboardType.NumberPassword),
                    modifier = Modifier.weight(1f),
                    colors = OutlinedTextFieldDefaults.colors(
                        focusedBorderColor = accent,
                        unfocusedBorderColor = palette.smoke,
                        focusedTextColor = palette.text,
                        unfocusedTextColor = palette.text
                    ),
                    shape = RoundedCornerShape(10.dp)
                )
                Button(
                    enabled = !busy && enrollmentCode.length == 6 && serverToken.isNotBlank(),
                    colors = ButtonDefaults.buttonColors(containerColor = accent),
                    onClick = {
                        scope.launch {
                            busy = true
                            try {
                                PhoneApprovalKey.ensureCreated()
                                when (val result = SudoApprovalApi.enroll(serverHost, serverToken, enrollmentCode, PhoneApprovalKey.publicKeyDer())) {
                                    is Outcome.Ok -> {
                                        enrollmentCode = ""
                                        statusError = false
                                        status = "Phone key enrolled. On the PC run the install command returned by the service: ${result.value}"
                                    }
                                    is Outcome.Fail -> {
                                        statusError = true
                                        status = result.message
                                    }
                                }
                            } catch (e: Exception) {
                                statusError = true
                                status = "Could not prepare the secure phone key: ${e.message ?: "Keystore error"}"
                            }
                            busy = false
                        }
                    }
                ) { Text(if (busy) "…" else "Enroll", color = palette.text) }
            }
            TextButton(
                enabled = !busy,
                onClick = {
                    try {
                        PhoneApprovalKey.reset()
                        statusError = false
                        status = "A new secure key was created. Generate a new PC enrollment code and enroll again; the old PC key will no longer match."
                    } catch (e: Exception) {
                        statusError = true
                        status = "Could not reset the secure key: ${e.message ?: "Keystore error"}"
                    }
                }
            ) { Text("Reset phone key (requires PC re-enrollment)", color = palette.smoke, fontSize = 10.sp) }

            Row(horizontalArrangement = Arrangement.spacedBy(8.dp)) {
                Text("Pending sudo requests", color = palette.text, fontSize = 12.sp, fontWeight = FontWeight.SemiBold, modifier = Modifier.weight(1f))
                Button(
                    enabled = !busy && serverToken.isNotBlank(),
                    colors = ButtonDefaults.buttonColors(containerColor = accent),
                    onClick = { refresh() }
                ) { Text(if (busy) "…" else "Refresh", color = palette.text) }
            }
            if (serverToken.isBlank()) {
                Text("Pair this phone with the PC above before using sudo approval.", color = palette.glow, fontSize = 10.sp)
            } else if (requests.isEmpty()) {
                Text("No sudo approvals waiting.", color = palette.smoke, fontSize = 10.sp)
            }
            requests.toList().forEach { request ->
                SudoRequestCard(
                    request = request,
                    palette = palette,
                    busy = busy,
                    activity = activity,
                    onStatus = { error, text -> statusError = error; status = text },
                    onApprove = { busy = true },
                    onFinished = { busy = false; refresh() },
                    serverHost = serverHost,
                    serverToken = serverToken
                )
            }
            status?.let { Text(it, color = if (statusError) palette.glow else palette.text, fontSize = 10.sp) }
        }
    }
}

@Composable
private fun SudoRequestCard(
    request: SudoUnlockRequest,
    palette: DashboardPalette,
    busy: Boolean,
    activity: FragmentActivity?,
    onStatus: (Boolean, String) -> Unit,
    onApprove: () -> Unit,
    onFinished: () -> Unit,
    serverHost: String,
    serverToken: String
) {
    val scope = rememberCoroutineScope()
    val manager = remember(activity) { activity?.let { BiometricManager.from(it) } }
    Card(
        colors = CardDefaults.cardColors(containerColor = palette.panel.copy(alpha = 0.8f)),
        shape = RoundedCornerShape(12.dp),
        border = BorderStroke(1.dp, palette.glow.copy(alpha = 0.3f)),
        modifier = Modifier.fillMaxWidth()
    ) {
        Column(modifier = Modifier.padding(10.dp), verticalArrangement = Arrangement.spacedBy(6.dp)) {
            Text("${request.user} on ${request.host}", color = palette.text, fontSize = 12.sp, fontWeight = FontWeight.Bold)
            Text("sudo · expires ${Date(request.expires * 1000)}", color = palette.smoke, fontSize = 9.sp)
            Text("Compare these choices with the number shown on the PC. Tap only that number.", color = palette.glow, fontSize = 10.sp)
            request.choices.forEach { choice ->
                Button(
                    enabled = !busy,
                    modifier = Modifier.fillMaxWidth(),
                    colors = ButtonDefaults.buttonColors(containerColor = palette.pink.copy(alpha = 0.86f)),
                    onClick = {
                        if (System.currentTimeMillis() / 1000 > request.expires) {
                            onStatus(true, "That sudo request expired. Refresh the list.")
                        } else if (activity == null) {
                            onStatus(true, "The biometric prompt is unavailable in this screen.")
                        } else if (manager?.canAuthenticate(BiometricManager.Authenticators.BIOMETRIC_STRONG) != BiometricManager.BIOMETRIC_SUCCESS) {
                            onStatus(true, "Set up a strong fingerprint or face biometric on this phone before approving sudo.")
                        } else {
                            try {
                                val message = SudoUnlockProtocol.message(request, choice)
                                val signature = PhoneApprovalKey.prepareSignature(message)
                                onApprove()
                                launchSudoBiometricPrompt(
                                    activity = activity,
                                    signature = signature,
                                    onSignature = { signed ->
                                        scope.launch {
                                            when (val result = SudoApprovalApi.respond(serverHost, serverToken, request.id, choice, signed)) {
                                                is Outcome.Ok -> onStatus(false, "Approval sent. The PC checks the signature and displayed number.")
                                                is Outcome.Fail -> onStatus(true, result.message)
                                            }
                                            onFinished()
                                        }
                                    },
                                    onError = { messageText ->
                                        onStatus(true, messageText)
                                        onFinished()
                                    }
                                )
                            } catch (e: Exception) {
                                onStatus(true, "Could not prepare the approval signature: ${e.message ?: "Keystore error"}")
                                onFinished()
                            }
                        }
                    }
                ) { Text(choice, color = palette.text, fontSize = 18.sp, fontWeight = FontWeight.Bold) }
            }
        }
    }
}

private fun launchSudoBiometricPrompt(
    activity: FragmentActivity,
    signature: java.security.Signature,
    onSignature: (ByteArray) -> Unit,
    onError: (String) -> Unit
) {
    val callback = object : BiometricPrompt.AuthenticationCallback() {
        override fun onAuthenticationSucceeded(result: BiometricPrompt.AuthenticationResult) {
            super.onAuthenticationSucceeded(result)
            try {
                val signed = result.cryptoObject?.signature?.sign()
                    ?: throw IllegalStateException("The secure signing operation was unavailable")
                onSignature(signed)
            } catch (e: Exception) {
                onError("The phone could not complete the secure signature. Enroll the phone key again if its biometrics changed.")
            }
        }

        override fun onAuthenticationError(errorCode: Int, errString: CharSequence) {
            super.onAuthenticationError(errorCode, errString)
            onError("Approval was not signed: $errString")
        }
    }
    try {
        BiometricPrompt(activity, ContextCompat.getMainExecutor(activity), callback).authenticate(
            BiometricPrompt.PromptInfo.Builder()
                .setTitle("Approve sudo request")
                .setSubtitle("Confirm the exact request with your strong biometric")
                .setAllowedAuthenticators(BiometricManager.Authenticators.BIOMETRIC_STRONG)
                .setNegativeButtonText("Deny")
                .build(),
            BiometricPrompt.CryptoObject(signature)
        )
    } catch (e: Exception) {
        onError("Biometric approval could not start: ${e.message ?: "unavailable"}")
    }
}
