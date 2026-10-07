// SPDX-License-Identifier: Apache-2.0

import Testing
@testable import ContainerizationShim

@Test func driverNameMatchesProtocol() {
    #expect(Shim.driverName == "apple")
}
