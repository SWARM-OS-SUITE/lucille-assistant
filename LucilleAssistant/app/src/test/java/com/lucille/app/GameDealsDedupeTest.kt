package com.lucille.app

import org.junit.Assert.assertEquals
import org.junit.Test

class GameDealsDedupeTest {
    private fun deal(title: String, store: String, price: String, appid: Int) =
        GameDeal(title, store, price, "40.00", 50, appid, "", "", "")

    @Test
    fun oneRowPerGameCheapestWins() {
        val out = dedupeDeals(
            listOf(
                deal("Shadow Tactics", "Steam", "3.99", 418240),
                deal("Disco Elysium", "GamesPlanet", "9.99", 632470),
                deal("Disco Elysium", "Fanatical", "8.49", 632470),
                deal("Monster Hunter Rise", "GMG", "9.99", 1446780),
                deal("Monster Hunter Rise", "Fanatical", "10.49", 1446780),
                deal("Monster Hunter Rise", "IndieGala", "9.99", 1446780),
            )
        )
        assertEquals(listOf("Shadow Tactics", "Disco Elysium", "Monster Hunter Rise"), out.map { it.title })
        assertEquals("8.49", out[1].salePrice)
        // A tie keeps the first shop listed.
        assertEquals("GMG", out[2].store)
    }

    @Test
    fun gamesWithoutAnAppIdMatchByTitle() {
        val out = dedupeDeals(listOf(deal("Some Game ", "A", "5.00", 0), deal("some game", "B", "4.00", 0), deal("Other", "A", "1.00", 0)))
        assertEquals(2, out.size)
        assertEquals("B", out[0].store)
    }
}
