// swift-tools-version: 6.2
// SPDX-License-Identifier: Apache-2.0

import PackageDescription

// Generated Swift for portenvd's API (make proto-swift; do not edit
// Sources by hand). The app imports it as PortenvProto.
let package = Package(
    name: "PortenvProto",
    platforms: [.macOS(.v15)],
    products: [
        .library(name: "PortenvProto", targets: ["PortenvProto"]),
    ],
    dependencies: [
        .package(url: "https://github.com/apple/swift-protobuf.git", exact: "1.38.1"),
        .package(url: "https://github.com/grpc/grpc-swift-2.git", exact: "2.4.3"),
        .package(url: "https://github.com/grpc/grpc-swift-protobuf.git", exact: "2.4.1"),
    ],
    targets: [
        .target(
            name: "PortenvProto",
            dependencies: [
                .product(name: "SwiftProtobuf", package: "swift-protobuf"),
                .product(name: "GRPCCore", package: "grpc-swift-2"),
                .product(name: "GRPCProtobuf", package: "grpc-swift-protobuf"),
            ]
        ),
    ]
)
