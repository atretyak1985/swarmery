# Sample Commit Message Examples (legacy stack; some scopes are deprecated)

## Feature Commits

```
feat(cb): add WebSocket telemetry streaming

- Implement async WebSocket server on port 8081
- Stream BLE telemetry at 5Hz
- Add /ws/telemetry and /ws/image endpoints
- Support MOCK_MODE for testing without hardware
```

```
feat(next): add SSE telemetry endpoint for browser streaming

- Create /api/telemetry/stream route with ReadableStream
- Subscribe to device-firmware WebSocket via server-side ws client
- Auto-reconnect on connection drop with 3s backoff
```

```
feat(db): add V1.0.3 migration for device identifiers

- Rename bee identifiers 10001-10012 to d1-d12
- Set all bee_mission.port to 8081
- Add index on bee.identifier
```

## Bug Fix Commits

```
fix(be): resolve WebSocket reconnection loop

FrontendDataAggregatorHandler was creating new connections
without closing previous ones on device-firmware restart.
Added connection state tracking and cleanup.
```

```
fix(helm): correct init container shell syntax

Use `. file` instead of `source file` in /bin/sh.
Alpine containers don't have bash.
```

## Refactoring Commits

```
refactor(be): adapt telemetry to BLE UPPER_SNAKE_CASE

- Add @JsonProperty("UPPER_SNAKE_CASE") to Telemetry.java
- Add @JsonAlias for backward compatibility
- Backend adapts to BLE standard per RFC-001
```

```
refactor(be): remove legacy device-firmware emulator

- Delete ImitationTelemetryWebSocketHandler
- Delete ImageWebSocketHandler
- Delete TelemetryService (dead code)
- Simplify WebSocketConfig
```

## Infrastructure Commits

```
build(helm): bump device-gateway chart to v0.2.0

- Add EXTERNAL_BEE_DNS env var
- Fix init container BEE_DNS computation
- Update NodePort range to 30100-30108

BREAKING CHANGE: NodePort base changed from 30080 to 30100
```

```
ci: add deployment lint validation to PR checks
```
