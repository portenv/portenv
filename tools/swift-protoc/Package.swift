// swift-tools-version: 6.2
// SPDX-License-Identifier: Apache-2.0

import PackageDescription

// The Swift protoc plugins, pinned (Package.resolved is committed). make
// proto-swift builds them into bin/ and generates proto/gen/swift. Keep the
// versions in step with SWIFT_PROTOBUF and GRPC_SWIFT_PROTOBUF in the
// Makefile (make proto-swift checks).
let package = Package(
    name: "swift-protoc",
    platforms: [.macOS(.v15)],
    dependencies: [
        .package(url: "https://github.com/apple/swift-protobuf.git", exact: "1.38.1"),
        .package(url: "https://github.com/grpc/grpc-swift-protobuf.git", exact: "2.4.1"),
    ],
    targets: [
        // A target is needed for SwiftPM to resolve; the plugins come from
        // the dependencies.
        .target(name: "Pins", path: "Pins"),
    ]
)
