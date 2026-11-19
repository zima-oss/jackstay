// swift-tools-version: 6.4

import PackageDescription

let package = Package(
    name: "SwiftStructuredPilot",
    platforms: [.macOS(.v15)],
    targets: [
        .testTarget(name: "LegacyTests"),
        .testTarget(name: "ModernTests"),
    ]
)
