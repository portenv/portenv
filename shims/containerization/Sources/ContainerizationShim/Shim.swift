// SPDX-License-Identifier: Apache-2.0

/// Identity of the apple box driver, as reported by
/// `DriverService.Capabilities`.
public enum Shim {
    /// Driver name, matching `CapabilitiesResponse.driver`.
    public static let driverName = "apple"
    /// Build version; "dev" for local builds.
    public static let version = "dev"
}
