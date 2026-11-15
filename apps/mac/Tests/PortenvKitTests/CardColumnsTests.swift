// SPDX-License-Identifier: Apache-2.0

import Foundation
import Testing

@testable import PortenvKit

/// New workspace's choice cards wrap into rows at the window's width, never
/// wider than it (the owner's dev build: at the minimum window, the Team
/// step's first and last cards were cut off).
struct CardColumnsTests {
    /// 720 pt window less first run's 64 pt margins.
    static let minimumWidth: CGFloat = 720 - 2 * 64

    @Test func theMinimumWindowWrapsFourCardsIntoTwoRowsOfTwo() {
        #expect(CardColumns.count(cards: 4, width: Self.minimumWidth) == 2)
    }

    @Test func aWideWindowKeepsFourCardsInOneRow() {
        #expect(CardColumns.count(cards: 4, width: 1440 - 2 * 64) == 4)
    }

    @Test func rowsAreBalancedNotLeftWithOneCard() {
        // Three fit, but 3 + 1 leaves a lone card: 2 + 2 instead.
        #expect(CardColumns.count(cards: 4, width: 3 * CardColumns.minimumWidth + 2 * CardColumns.spacing) == 2)
        // Five in a row of three: 3 + 2.
        #expect(CardColumns.count(cards: 5, width: 3 * CardColumns.minimumWidth + 2 * CardColumns.spacing) == 3)
    }

    @Test func neverMoreColumnsThanCards() {
        #expect(CardColumns.count(cards: 2, width: 2000) == 2)
        #expect(CardColumns.count(cards: 1, width: 2000) == 1)
    }

    @Test func aNarrowWidthStillHasOneColumn() {
        #expect(CardColumns.count(cards: 4, width: 100) == 1)
        #expect(CardColumns.count(cards: 0, width: 600) == 1)
    }

    /// Every column is at least the card's minimum, and the row never wider
    /// than the width, at every width from the minimum window up.
    @Test func everyRowFitsItsWidth() {
        for width in stride(from: CardColumns.minimumWidth, through: 2400, by: 7) {
            for cards in 1...8 {
                let n = CardColumns.count(cards: cards, width: width)
                let column = CardColumns.columnWidth(columns: n, width: width)
                #expect(column >= CardColumns.minimumWidth - 0.5, "\(cards) cards at \(width): \(n) columns of \(column)")
                #expect(CGFloat(n) * column + CGFloat(n - 1) * CardColumns.spacing <= width + 0.5)
            }
        }
    }
}
