package com.lucille.app

import org.junit.Assert.assertArrayEquals
import org.junit.Assert.assertEquals
import org.junit.Assert.assertFalse
import org.junit.Assert.assertThrows
import org.junit.Assert.assertTrue
import org.junit.Test
import java.nio.charset.StandardCharsets
import java.security.KeyPairGenerator
import java.security.Signature
import java.security.spec.ECGenParameterSpec

class SudoUnlockProtocolTest {
    private val request = SudoUnlockRequest(
        id = "00112233445566778899aabbccddeeff",
        nonce = "ffeeddccbbaa99887766554433221100",
        choices = listOf("0421", "9876", "0008"),
        service = "sudo",
        user = "i30ltz",
        host = "swarmbox",
        expires = 1_797_000_000
    )

    @Test
    fun messageMatchesGoWireFormatExactly() {
        val expected = """lucille-unlock-v1
id=00112233445566778899aabbccddeeff
nonce=ffeeddccbbaa99887766554433221100
chosen=0421
service=sudo
user=i30ltz
host=swarmbox
expires=1797000000""".toByteArray(StandardCharsets.UTF_8)
        assertArrayEquals(expected, SudoUnlockProtocol.message(request, "0421"))
    }

    @Test
    fun signatureVerifiesForExactRequestAndFailsIfAnyBoundFieldChanges() {
        val pairGenerator = KeyPairGenerator.getInstance("EC")
        pairGenerator.initialize(ECGenParameterSpec("secp256r1"))
        val pair = pairGenerator.generateKeyPair()
        val message = SudoUnlockProtocol.message(request, "0421")
        val signer = Signature.getInstance("SHA256withECDSA").apply {
            initSign(pair.private)
            update(message)
        }
        val signed = signer.sign()
        val verifier = Signature.getInstance("SHA256withECDSA").apply {
            initVerify(pair.public)
            update(message)
        }
        assertTrue(verifier.verify(signed))

        val altered = SudoUnlockProtocol.message(request.copy(host = "other-host"), "0421")
        val alteredVerifier = Signature.getInstance("SHA256withECDSA").apply {
            initVerify(pair.public)
            update(altered)
        }
        assertFalse(alteredVerifier.verify(signed))
    }

    @Test
    fun rejectsDecoyNotSelectedAndUnsafeRequestContext() {
        assertThrows(IllegalArgumentException::class.java) {
            SudoUnlockProtocol.message(request, "1111")
        }
        assertThrows(IllegalArgumentException::class.java) {
            SudoUnlockProtocol.message(request.copy(user = "sudo\nroot"), "0421")
        }
        assertThrows(IllegalArgumentException::class.java) {
            SudoUnlockProtocol.message(request.copy(choices = listOf("0421", "0421", "0008")), "0421")
        }
    }

    @Test
    fun base64SignatureUsesGoStdEncodingCompatibleAlphabet() {
        val encoded = SudoUnlockProtocol.signatureBase64(byteArrayOf(0, 1, 2, 3, 4))
        assertEquals("AAECAwQ=", encoded)
    }
}
