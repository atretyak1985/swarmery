---
name: mavlink-integration
description: "Use this skill when a task involves MAVLink protocol communication via pymavlink, UART/UDP/TCP drone connections, ArduPilot SITL simulation, or telemetry message parsing in the edge service. Don't use it for raw UART hardware driver code without MAVLink framing (use embedded-systems), WebSocket transport to the web portal (use api-integration), or Docker builds (use docker-build)."
version: "1.0.0"
owner: "agentry-core"
allowed-tools: Read, Bash, Grep, Glob
---

# Purpose

Produce MAVLink protocol code for drone communication in the edge service (project.json → device; Python 3.11+ on Raspberry Pi 5). Covers pymavlink connection types (UART, UDP, TCP), message parsing for common telemetry messages, async I/O patterns with `asyncio.to_thread`, ArduPilot SITL simulation setup, and `MOCK_MODE` for CI testing without hardware. For the WebSocket bridge from the edge service to the web portal, defer to `api-integration`. For Helm deployment of edge service pods, defer to the project's deployment workflow.

Success criteria: generated code connects (or simulates connection in MOCK_MODE), sends GCS heartbeats at 1Hz, reads telemetry via `recv_match`, handles `asyncio.CancelledError` for graceful shutdown, and releases the connection on stop.

# When to use

- Connecting to a drone via pymavlink (UART serial, UDP simulator, TCP)
- Parsing MAVLink telemetry messages (HEARTBEAT, GLOBAL_POSITION_INT, ATTITUDE, GPS_RAW_INT)
- Implementing async MAVLink reader/writer patterns in the edge service
- Setting up ArduPilot SITL for local development or CI testing
- Configuring `MOCK_MODE` for testing without physical hardware

# When NOT to use

- Raw UART hardware driver code without MAVLink framing (use `embedded-systems`)
- WebSocket streaming from the edge service to the web portal (use `api-integration`)
- Deploying the edge service via Helm/k3s (use the project's deployment workflow)
- Building Docker images for the edge service (use `docker-build`)
- Debugging Kubernetes pod health for the edge service (use `troubleshooting`)

# Required environment

- Runtime: `.claude/skills/mavlink-integration/SKILL.md`
- Language: Python 3.11+
- Libraries: `pymavlink`, `asyncio`, `serial` (for `serial.SerialException`)
- Repo: the edge service repo (project.json → device)
- MAVLink dialect: `ardupilotmega` (superset of `common`)
- Hardware: Raspberry Pi 5 with UART (`/dev/ttyAMA0` default, configurable via `MAVLINK_CONNECTION` env var)
- Simulator: ArduPilot SITL (UDP `127.0.0.1:14550`)
- CI testing: `MOCK_MODE=true` env var disables UART connection

# Inputs

- `connection_type: enum` -- one of: `uart`, `udp`, `tcp`
- `connection_string: string` -- pymavlink connection string (e.g., `serial:/dev/ttyAMA0:57600`, `udp:127.0.0.1:14550`)
- `message_types: string[]` -- MAVLink message types to read (e.g., `HEARTBEAT`, `GLOBAL_POSITION_INT`)

# Outputs

- Format: Python code using pymavlink with async patterns, or SITL setup commands
- Length budget: max 100 lines for a connection manager class; max 30 lines for a telemetry parsing snippet
- All code follows edge service standards: type hints, `asyncio.to_thread` for blocking calls, specific exception handling

# Procedure

1. **Determine connection type** -- UART for real hardware, UDP for SITL, TCP for remote.
   **Checkpoint:** Connection string format matches pymavlink expectations (e.g., `serial:/dev/ttyAMA0:57600` for UART, `udp:127.0.0.1:14550` for SITL).

2. **Check MOCK_MODE** -- If `MOCK_MODE=true`, skip real connection and return synthetic telemetry.
   **Checkpoint:** Env var checked before attempting hardware connection.

3. **Establish connection** -- Use `mavutil.mavlink_connection()` wrapped in `asyncio.to_thread`.
   **Checkpoint:** Connection object is not None and `recv_match(type='HEARTBEAT')` succeeds.

4. **Start heartbeat sender** -- Send GCS heartbeats at 1Hz minimum to maintain connection. Guard against double-start by checking if the heartbeat task already exists.
   **Checkpoint:** Heartbeat task running with `asyncio.CancelledError` handling.

5. **Read telemetry** -- Use `recv_match` with specific message types and timeouts.
   **Checkpoint:** Message data validated before use.

# Self-check

<self-check>
- [ ] I used `GLOBAL_POSITION_INT` (message #33) or `GPS_RAW_INT` (message #24), not the non-existent `GPS_POSITION`
- [ ] Async code wraps all pymavlink blocking calls in `asyncio.to_thread`
- [ ] The heartbeat loop handles `asyncio.CancelledError` for graceful shutdown
- [ ] Exception handling uses specific exceptions (`serial.SerialException`, `OSError`, `ConnectionRefusedError`), not bare `except Exception`
- [ ] `import serial` is present whenever `serial.SerialException` is referenced -- pymavlink does not bring `serial` into scope
- [ ] Connection string is read from `MAVLINK_CONNECTION` env var, not hardcoded
- [ ] I mentioned `MOCK_MODE=true` for CI/testing contexts
- [ ] I specified the MAVLink dialect (`ardupilotmega`) when referencing non-common messages
- [ ] The `stop()` method cancels the heartbeat task AND closes the MAVLink connection
</self-check>

# Common mistakes

- DO NOT use `GPS_POSITION` as a message name -- it does not exist in MAVLink; use `GLOBAL_POSITION_INT` (#33) for fused position or `GPS_RAW_INT` (#24) for raw GPS

  ```python
  # WRONG -- GPS_POSITION does not exist, recv_match returns None forever
  msg = await reader.read_message("GPS_POSITION", timeout=1.0)

  # CORRECT -- use GLOBAL_POSITION_INT (#33) for fused lat/lon/alt
  msg = await reader.read_message("GLOBAL_POSITION_INT", timeout=1.0)
  ```

- DO NOT reference `serial.SerialException` without `import serial` -- pymavlink does not bring `serial` into scope; missing this causes `NameError` at runtime

  ```python
  # WRONG -- NameError: name 'serial' is not defined
  from pymavlink import mavutil
  # ... later ...
  except (OSError, serial.SerialException) as err:

  # CORRECT -- import serial explicitly
  import serial
  from pymavlink import mavutil
  # ... later ...
  except (OSError, serial.SerialException) as err:
  ```

- DO NOT hardcode `/dev/ttyAMA0` -- read from `MAVLINK_CONNECTION` env var; UART device paths vary across RPi models
- DO NOT use bare `except Exception` -- catch `serial.SerialException` and `OSError` for connection errors, `ConnectionRefusedError` for SITL UDP
- DO NOT run an infinite `while True` loop without `asyncio.CancelledError` handling -- the task will leak on shutdown and trigger Python 3.11+ warnings
- DO NOT test against real hardware in CI -- set `MOCK_MODE=true` to disable UART and return synthetic telemetry
- DO NOT forget to close the MAVLink connection in `stop()` -- call `self.connection.close()` after cancelling the heartbeat task

# Escalation

- Stop and ask when: UART device path is not `/dev/ttyAMA0` and the user has not specified the correct path
- Stop and ask when: SITL is unreachable after setup and the ArduPilot firmware version is unknown
- Stop and ask when: The user needs to send flight commands (arm, takeoff, waypoint) to a real drone -- this is a safety-critical operation requiring explicit operator confirmation

# What to surface

- Which connection type and string are being used
- Whether MOCK_MODE is active
- The MAVLink dialect assumed (common vs ardupilotmega)
- Any baud rate or device path configuration that may need adjustment
- SITL connection status and which messages are being received

# Examples

<example name="async-mavlink-reader">
## Async MAVLink reader with proper error handling

```python
import asyncio
import logging
import os
import serial
from typing import Any, Optional

from pymavlink import mavutil

logger = logging.getLogger(__name__)

MOCK_MODE = os.environ.get("MOCK_MODE", "false").lower() == "true"


class MAVLinkReader:
    """Async MAVLink connection manager for the edge service."""

    def __init__(self, connection_string: Optional[str] = None) -> None:
        self.connection_string = connection_string or os.environ.get(
            "MAVLINK_CONNECTION", "serial:/dev/ttyAMA0:57600"
        )
        self.connection: Optional[Any] = None
        self._heartbeat_task: Optional[asyncio.Task[None]] = None

    async def connect(self) -> bool:
        """Establish MAVLink connection."""
        if MOCK_MODE:
            logger.info("MOCK_MODE active -- skipping real MAVLink connection")
            return True

        try:
            self.connection = await asyncio.to_thread(
                mavutil.mavlink_connection,
                self.connection_string,
                source_system=255,
                source_component=0,
            )
            # Wait for first heartbeat to confirm connection
            msg = await asyncio.to_thread(
                self.connection.recv_match,
                type="HEARTBEAT",
                blocking=True,
                timeout=10,
            )
            if msg is None:
                logger.error("No heartbeat received within 10s")
                return False
            logger.info("Connected to system %d", msg.get_srcSystem())
            return True
        except serial.SerialException as err:
            logger.error("Serial connection failed: %s", err)
            return False
        except OSError as err:
            logger.error("Connection failed: %s", err)
            return False

    async def read_message(
        self, msg_type: str, timeout: float = 1.0
    ) -> Optional[Any]:
        """Read a specific MAVLink message type."""
        if MOCK_MODE:
            return None
        if not self.connection:
            return None
        try:
            return await asyncio.to_thread(
                self.connection.recv_match,
                type=msg_type,
                blocking=True,
                timeout=timeout,
            )
        except OSError as err:
            logger.error("Read error for %s: %s", msg_type, err)
            return None

    async def send_heartbeat_loop(self) -> None:
        """Send GCS heartbeats at 1Hz. Handles cancellation for graceful shutdown."""
        try:
            while True:
                if self.connection:
                    await asyncio.to_thread(
                        self.connection.mav.heartbeat_send,
                        mavutil.mavlink.MAV_TYPE_GCS,
                        mavutil.mavlink.MAV_AUTOPILOT_INVALID,
                        0,
                        0,
                        mavutil.mavlink.MAV_STATE_ACTIVE,
                    )
                await asyncio.sleep(1)
        except asyncio.CancelledError:
            logger.info("Heartbeat sender cancelled -- shutting down")
            return

    async def start(self) -> bool:
        """Connect and start heartbeat sender."""
        if not await self.connect():
            return False
        if self._heartbeat_task is not None:
            logger.warning("Heartbeat task already running -- skipping duplicate start")
            return True
        self._heartbeat_task = asyncio.create_task(self.send_heartbeat_loop())
        return True

    async def stop(self) -> None:
        """Graceful shutdown -- cancel heartbeat and close connection."""
        if self._heartbeat_task:
            self._heartbeat_task.cancel()
            await self._heartbeat_task
            self._heartbeat_task = None
        if self.connection:
            await asyncio.to_thread(self.connection.close)
            self.connection = None
```
</example>

<example name="gps-telemetry">
## Reading GPS telemetry (GLOBAL_POSITION_INT #33)

```python
msg = await reader.read_message("GLOBAL_POSITION_INT", timeout=1.0)
if msg:
    telemetry = {
        "lat": msg.lat / 1e7,           # degrees
        "lon": msg.lon / 1e7,           # degrees
        "alt": msg.alt / 1000,          # meters above MSL
        "relative_alt": msg.relative_alt / 1000,  # meters above ground
        "heading": msg.hdg / 100,       # degrees
        "vx": msg.vx / 100,            # m/s north
        "vy": msg.vy / 100,            # m/s east
        "vz": msg.vz / 100,            # m/s down
    }
```

For raw GPS data use `GPS_RAW_INT` (#24) instead -- it provides `fix_type`, `satellites_visible`, `eph` (HDOP), and `epv` (VDOP).
</example>

<example name="sitl-setup">
## ArduPilot SITL setup

```bash
# Clone and build (one-time)
git clone https://github.com/ArduPilot/ardupilot.git
cd ardupilot
git submodule update --init --recursive
Tools/environment_install/install-prereqs-ubuntu.sh -y

# Run SITL (listens on UDP:127.0.0.1:14550)
cd ArduCopter
sim_vehicle.py -v ArduCopter --console --map

# Connect from Python
# MAVLINK_CONNECTION=udp:127.0.0.1:14550 python your_script.py
```
</example>

<example name="mock-mode-ci">
## Testing without hardware (MOCK_MODE)

Set `MOCK_MODE=true` in CI environments to skip UART connections:

```yaml
# Kubernetes values or CI env
env:
  - name: MOCK_MODE
    value: "true"
  - name: LOG_LEVEL
    value: "DEBUG"
```

In code, check `MOCK_MODE` before attempting hardware access (see `connect()` method above). Return synthetic telemetry data for testing.
</example>

# Failure modes

| Mode | Symptom | Detection | Fix |
|------|---------|-----------|-----|
| Wrong message name `GPS_POSITION` | `recv_match` returns None forever | Message name is not in MAVLink common/ardupilotmega dialect | Use `GLOBAL_POSITION_INT` (#33) or `GPS_RAW_INT` (#24) |
| UART baud rate mismatch | Connection succeeds but no messages received | Serial monitor shows garbled data | Verify baud rate matches ArduPilot `SERIAL1_BAUD` parameter (typically 57600 or 115200) |
| Heartbeat task leak on shutdown | `RuntimeWarning: coroutine was never awaited` or task pending at interpreter shutdown | No `CancelledError` handler in heartbeat loop | Add `try/except asyncio.CancelledError: return` and cancel the task in `stop()` |
| SITL connection refused | `ConnectionRefusedError` on UDP connect | ArduPilot SITL is not running | Start SITL with `sim_vehicle.py`, then retry connection |
| Missing `import serial` | `NameError: name 'serial' is not defined` at runtime | `serial.SerialException` referenced without import | Add `import serial` to import block |

# Related skills

- `api-integration` -- defer for the WebSocket bridge from the edge service to the web portal; compose when MAVLink data needs to reach the browser via SSE
- The project's deployment workflow -- defer for deploying edge service pods to k3s on Raspberry Pi; `MAVLINK_CONNECTION` and `MOCK_MODE` env vars are set in Helm values there
- `embedded-systems` -- defer for RPi hardware config (GPIO, UART enable, camera); compose when UART device path needs to be determined
- `code-standards` -- follow its Python section for type hints, specific exceptions, and `MOCK_MODE` patterns
