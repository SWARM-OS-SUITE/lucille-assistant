package com.lucille.mobile

import android.os.Build
import android.security.keystore.KeyGenParameterSpec
import android.security.keystore.KeyInfo
import android.security.keystore.KeyProperties
import java.security.KeyPairGenerator
import java.security.KeyFactory
import java.security.KeyStore
import java.security.Signature
import java.security.spec.ECGenParameterSpec

/**
 * Device-bound P-256 key. Each signature operation requires a fresh unlock: a strong biometric
 * (fingerprint/face) where the phone has one, or the phone's screen-lock PIN/pattern/password where it
 * doesn't (user decision 2026-10-17: the Revoview has no fingerprint reader). Android decides the allowed
 * methods when the key is created, hence the "v2" alias: v1 keys were biometric-only.
 */
object PhoneApprovalKey {
    private const val ANDROID_KEYSTORE = "AndroidKeyStore"
    private const val ALIAS = "lucille.sudo.approver.p256.v2"

    @Synchronized
    fun ensureCreated() {
        val store = keyStore()
        if (store.containsAlias(ALIAS)) {
            requireHardwareBackedKey(store)
            return
        }

        val spec = KeyGenParameterSpec.Builder(ALIAS, KeyProperties.PURPOSE_SIGN)
            .setAlgorithmParameterSpec(ECGenParameterSpec("secp256r1"))
            .setDigests(KeyProperties.DIGEST_SHA256)
            .setUserAuthenticationRequired(true)
            .setInvalidatedByBiometricEnrollment(true)
            .apply {
                if (Build.VERSION.SDK_INT >= Build.VERSION_CODES.R) {
                    setUserAuthenticationParameters(
                        0,
                        KeyProperties.AUTH_BIOMETRIC_STRONG or KeyProperties.AUTH_DEVICE_CREDENTIAL
                    )
                } else {
                    // -1 means authentication is required for every operation, not a timed window.
                    setUserAuthenticationValidityDurationSeconds(-1)
                }
            }
            .build()
        KeyPairGenerator.getInstance(KeyProperties.KEY_ALGORITHM_EC, ANDROID_KEYSTORE).apply {
            initialize(spec)
        }.generateKeyPair()
        requireHardwareBackedKey(keyStore())
    }

    fun hasKey(): Boolean = keyStore().containsAlias(ALIAS)

    fun publicKeyDer(): ByteArray {
        val cert = keyStore().getCertificate(ALIAS)
            ?: throw IllegalStateException("No phone approval key exists yet")
        return cert.publicKey.encoded
    }

    /** Returns a prepared per-use signing operation to pass to BiometricPrompt.CryptoObject. */
    fun prepareSignature(message: ByteArray): Signature {
        val store = keyStore()
        val privateKey = requireHardwareBackedKey(store)
        return Signature.getInstance("SHA256withECDSA").apply {
            initSign(privateKey as java.security.PrivateKey)
            update(message)
        }
    }

    /** Explicit recovery for a key invalidated by adding/removing device biometrics. */
    @Synchronized
    fun reset() {
        keyStore().deleteEntry(ALIAS)
        ensureCreated()
    }

    private fun keyStore(): KeyStore = KeyStore.getInstance(ANDROID_KEYSTORE).apply { load(null) }

    private fun requireHardwareBackedKey(store: KeyStore): java.security.Key {
        val privateKey = store.getKey(ALIAS, null)
            ?: throw IllegalStateException("No phone approval key exists yet")
        val keyInfo: KeyInfo = KeyFactory.getInstance(privateKey.algorithm, ANDROID_KEYSTORE)
            .getKeySpec(privateKey, KeyInfo::class.java)
        if (!keyInfo.isInsideSecureHardware) {
            throw IllegalStateException("This phone cannot protect the sudo key in secure hardware")
        }
        return privateKey
    }
}
