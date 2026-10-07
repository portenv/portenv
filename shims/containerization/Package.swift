// swift-tools-version: 6.2
// SPDX-License-Identifier: Apache-2.0

import PackageDescription

// The apple box driver: a small Swift process that implements
// portenv.driver.v1.DriverService on Apple Containerization and serves it to
// portenvd over a local Unix socket. Milestone 0.1 scaffold only; the driver
// itself arrives in milestone 1.3.
let package = Package(
    name: "ContainerizationShim",
    platforms: [.macOS(.v26)],
    products: [
        .executable(name: "portenv-containerization-shim", targets: ["portenv-containerization-shim"]),
    ],
    targets: [
        .target(name: "ContainerizationShim"),
        .executableTarget(
            name: "portenv-containerization-shim",
            dependencies: ["ContainerizationShim"]
        ),
        .testTarget(
            name: "ContainerizationShimTests",
            dependencies: ["ContainerizationShim"]
        ),
    ]
)
