// swift-tools-version: 6.2
// SPDX-License-Identifier: Apache-2.0

import PackageDescription

// Portenv.app. Milestone 1.0 (walking skeleton): one window with a terminal
// in the box's tmux session and a title menu with Move To and Revert To.
// Run it from Xcode (open this Package.swift, run the Portenv scheme). It
// talks to portenvd through the portenv CLI until the app's own gRPC client
// lands in 1.1.
let package = Package(
    name: "Portenv",
    platforms: [.macOS(.v26)],
    products: [
        .executable(name: "Portenv", targets: ["Portenv"]),
    ],
    dependencies: [
        // MIT-licensed terminal emulator (docs/PLAN.md, 1.2).
        .package(url: "https://github.com/migueldeicaza/SwiftTerm.git", exact: "1.20.0"),
    ],
    targets: [
        .target(name: "PortenvKit"),
        .executableTarget(
            name: "Portenv",
            dependencies: ["PortenvKit", .product(name: "SwiftTerm", package: "SwiftTerm")]
        ),
        .testTarget(name: "PortenvKitTests", dependencies: ["PortenvKit"]),
    ]
)
