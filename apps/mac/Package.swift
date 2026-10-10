// swift-tools-version: 6.2
// SPDX-License-Identifier: Apache-2.0

import PackageDescription

// Portenv.app. Milestone 1.0 (walking skeleton): one window with a terminal
// in the box's tmux session and a title menu with Move To and Revert To.
// Run it from Xcode (open this Package.swift, run the Portenv scheme). It
// talks to portenvd over gRPC on its socket (gRPC Swift 2, ADR 0014; macOS
// 15 or later, below the app's macOS 26).
let package = Package(
    name: "Portenv",
    platforms: [.macOS(.v26)],
    products: [
        .executable(name: "Portenv", targets: ["Portenv"]),
    ],
    dependencies: [
        // MIT-licensed terminal emulator (docs/PLAN.md, 1.2).
        .package(url: "https://github.com/migueldeicaza/SwiftTerm.git", exact: "1.20.0"),
        // portenvd's API: the generated client (make proto-swift) and gRPC
        // Swift 2's transport over the Unix socket. Exact versions, resolved
        // in Package.resolved.
        .package(path: "../../proto/gen/swift"),
        .package(url: "https://github.com/grpc/grpc-swift-2.git", exact: "2.4.3"),
        .package(url: "https://github.com/grpc/grpc-swift-nio-transport.git", exact: "2.10.0"),
        .package(url: "https://github.com/apple/swift-protobuf.git", exact: "1.38.1"),
    ],
    targets: [
        .target(
            name: "PortenvKit",
            dependencies: [
                .product(name: "PortenvProto", package: "swift"),
                .product(name: "GRPCCore", package: "grpc-swift-2"),
                .product(name: "GRPCNIOTransportHTTP2", package: "grpc-swift-nio-transport"),
                .product(name: "SwiftProtobuf", package: "swift-protobuf"),
            ]
        ),
        // The terminal's SwiftTerm glue (per-row overlays, ADR 0017), kept
        // out of the app target so it can be tested on a headless terminal.
        .target(
            name: "PortenvTerm",
            dependencies: ["PortenvKit", .product(name: "SwiftTerm", package: "SwiftTerm")]
        ),
        .executableTarget(
            name: "Portenv",
            dependencies: ["PortenvKit", "PortenvTerm", .product(name: "SwiftTerm", package: "SwiftTerm")]
        ),
        .testTarget(name: "PortenvKitTests", dependencies: ["PortenvKit"]),
        .testTarget(
            name: "PortenvTermTests",
            dependencies: ["PortenvTerm", "PortenvKit", .product(name: "SwiftTerm", package: "SwiftTerm")]
        ),
    ]
)
