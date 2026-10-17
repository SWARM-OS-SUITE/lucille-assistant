package com.lucille.mobile

import org.junit.Assert.assertArrayEquals
import org.junit.Assert.assertEquals
import org.junit.Assert.assertNull
import org.junit.Test

class TotpTest {
    private val rfc = "12345678901234567890".toByteArray()

    @Test fun matchesRfc6238Vectors() {
        // Same vectors the PC helper's Go tests use, so the phone and the PC agree.
        assertEquals("287082", Totp.code(rfc, 59))
        assertEquals("081804", Totp.code(rfc, 1111111109))
        assertEquals("050471", Totp.code(rfc, 1111111111))
        assertEquals("005924", Totp.code(rfc, 1234567890))
        assertEquals("279037", Totp.code(rfc, 2000000000))
    }

    @Test fun base32AsThePcPrintsIt() {
        assertArrayEquals(rfc, Totp.decodeBase32("gezd gnbv-gy3t qojq gezd gnbv gy3t qojq"))
        assertNull(Totp.decodeBase32("not base32 !!"))
        assertNull(Totp.decodeBase32("MFRGG"))
        assertNull(Totp.decodeBase32(""))
    }

    @Test fun countdown() {
        assertEquals(30, Totp.secondsLeft(60)); assertEquals(1, Totp.secondsLeft(89)); assertEquals(30, Totp.secondsLeft(90))
    }
}
