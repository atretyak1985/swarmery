// swift-tools-version: 6.0
// Package manifest for the swarmery menu-bar notch companion. Zero external
// dependencies by design (SC acceptance criterion): the daemon client, WS
// stream, and attention model are built entirely on Foundation + URLSession.
import PackageDescription

let package = Package(
    name: "SwarmeryNotch",
    platforms: [.macOS(.v14)],
    products: [
        .executable(name: "SwarmeryNotch", targets: ["SwarmeryNotch"])
    ],
    dependencies: [],
    targets: [
        .executableTarget(
            name: "SwarmeryNotch",
            dependencies: [],
            path: "Sources/SwarmeryNotch"
        ),
        .testTarget(
            name: "SwarmeryNotchTests",
            dependencies: ["SwarmeryNotch"],
            path: "Tests/SwarmeryNotchTests"
        ),
    ]
)
