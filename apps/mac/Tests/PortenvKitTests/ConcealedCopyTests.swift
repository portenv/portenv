// SPDX-License-Identifier: Apache-2.0

import AppKit
import Testing

@testable import PortenvKit

/// Copying the recovery key (R-0018): the pasteboard says it's concealed and
/// transient, so clipboard managers that honour it don't keep it, and the
/// key is cleared after a while if it's still there.
@MainActor
struct ConcealedCopyTests {
    private func board() -> NSPasteboard { NSPasteboard(name: NSPasteboard.Name("portenv-test-\(UUID().uuidString)")) }

    @Test func copySetsTheStringAndTheConcealedAndTransientTypes() {
        let b = board()
        defer { b.releaseGlobally() }
        let key = ["AB12", "CD34"].joined(separator: "-")
        _ = ConcealedCopy.copy(key, to: b)
        #expect(b.string(forType: .string) == key)
        let types = Set(b.types ?? [])
        #expect(types.contains(ConcealedCopy.concealed))
        #expect(types.contains(ConcealedCopy.transient))
    }

    /// Cleared only if nothing else was copied since: someone who copied
    /// something else meanwhile keeps it.
    @Test func clearsOnlyWhenTheKeyIsStillThere() {
        let b = board()
        defer { b.releaseGlobally() }
        let count = ConcealedCopy.copy("first", to: b)
        ConcealedCopy.clear(b, ifStill: count)
        #expect(b.string(forType: .string) == nil)

        let again = ConcealedCopy.copy("second", to: b)
        b.clearContents()
        b.setString("something else", forType: .string)
        ConcealedCopy.clear(b, ifStill: again)
        #expect(b.string(forType: .string) == "something else")
    }

    /// The scheduled clear happens after the delay (60 s in the app).
    @Test func theScheduledClearRuns() async {
        let b = board()
        defer { b.releaseGlobally() }
        let task = ConcealedCopy.copyThenClear("key", to: b, after: .milliseconds(50))
        #expect(b.string(forType: .string) == "key")
        await task.value
        #expect(b.string(forType: .string) == nil)
        #expect(ConcealedCopy.clearAfter == .seconds(60))
    }
}
