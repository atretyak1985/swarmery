---
name: embedded-systems
description: "Use this skill when writing or reviewing Python code for Raspberry Pi 5 edge devices -- UART hardware drivers, camera streaming via picamera2, GPIO control, systemd services, or resource monitoring. Don't use it for MAVLink protocol parsing (use mavlink-integration), web portal server code, or Helm work."
version: "1.0.0"
owner: "agentry-core"
allowed-tools: Read, Edit, Bash, Grep, Glob
---

# Purpose

Produce and review Python code for Raspberry Pi 5 edge devices running the edge service. Covers UART serial communication, picamera2 camera streaming, GPIO control, resource monitoring, and systemd service configuration. All generated code passes `mypy`, handles `MOCK_MODE` for CI, and releases hardware resources on exit. For MAVLink protocol parsing (message IDs, dialects, command sequences), compose with `mavlink-integration`.

Success criteria: generated code imports without hardware attached (`MOCK_MODE=true`), passes `mypy` with zero errors, and releases every acquired resource in a `finally` block.

# When to use

- Writing or reviewing Python code that interfaces with RPi5 hardware (UART, GPIO, camera)
- Configuring systemd services for the edge service on edge devices
- Debugging hardware communication issues (serial port, camera initialization)
- Implementing resource monitoring (CPU temperature, memory, throttling) on edge devices

# When NOT to use

- Web portal server-side TypeScript code (use `api-integration` or `code-standards`)
- Infrastructure or Helm chart work (use the project's deployment workflow)
- MAVLink message parsing, dialect selection, or command sequencing (use `mavlink-integration`)
- Docker image building for the edge service (use `docker-build`)
- Python dependency auditing (use `deps-check`)
- Any Python code in the edge service that is purely MAVLink protocol without hardware driver concerns (use `mavlink-integration`)

# Required environment

- Runtime: `.claude/skills/embedded-systems/SKILL.md`
- Tools/libraries: Python 3.11+, `serial_asyncio`, `picamera2`, `psutil`, `asyncio`
- Hardware: Raspberry Pi 5 (or `MOCK_MODE=true` for CI/dev without hardware)
- Code standards: `mypy` for type checking, `black` (line length 100), `isort`, `flake8` (max complexity 10)

# Inputs

- `component: "uart" | "camera" | "gpio" | "systemd" | "monitoring"` -- which hardware subsystem to work with
- `mock_mode: boolean` -- if true, generate code that works without physical hardware (for CI testing)

# Outputs

- Format: Python code with type hints, specific exception handling, and `MOCK_MODE` support. All code passes `mypy` without errors.
- Length budget: max 120 lines per class; max 40 lines per standalone function; systemd unit files under 25 lines.

# Procedure

1. **Check mock mode** -- Determine if the code should support running without hardware. If `MOCK_MODE` is set in the environment, all hardware interfaces return simulated data.

   ```python
   import os
   MOCK_MODE = os.environ.get("MOCK_MODE", "false").lower() == "true"
   ```

   **Checkpoint:** `MOCK_MODE` check is present at module level. The check reads from the environment at runtime, never hardcoded to a literal `"false"`.

2. **Import required modules** -- Always include explicit imports. Never assume modules are available in scope. If using `serial.EIGHTBITS` or `serial.SerialException`, you must `import serial` separately -- `serial_asyncio` does not bring `serial` constants into scope.

   ```python
   import asyncio
   import logging
   import serial          # required for serial.EIGHTBITS, serial.SerialException
   import serial_asyncio
   from typing import Optional

   logger = logging.getLogger(__name__)
   ```

   **Checkpoint:** `logger` and all type imports are explicitly defined. `import serial` is present if any `serial.*` constant or exception is referenced.

3. **Implement with specific exceptions** -- Use specific exception types, not bare `except Exception`. Hardware code handles known failure modes:

   - UART: `serial.SerialException`, `OSError`, `asyncio.TimeoutError`
   - Camera: `RuntimeError` (picamera2 init failure), `OSError` (device not found)
   - GPIO: `PermissionError`, `FileNotFoundError` (sysfs access)

   **Checkpoint:** Every `except` block catches a specific exception type.

4. **Add cleanup in `finally` blocks** -- Hardware resources (serial ports, camera) are released on exit.

   **Checkpoint:** Every `connect`/`initialize` has a corresponding `close`/`cleanup` in a `finally` block.

5. **Add exit conditions to loops** -- Streaming loops accept a stop signal.

   **Checkpoint:** Every `while` loop checks a `stop_event: asyncio.Event` or equivalent.

6. **Show diff before applying Edit** -- Before applying any Edit to an existing file, show the operator a before/after diff of the code change and confirm the target hardware component.

   **Checkpoint:** Diff shown; operator has context on which hardware subsystem is affected.

7. **Test with MOCK_MODE** -- Verify the code runs in CI without hardware by setting `MOCK_MODE=true`.

   **Checkpoint:** `mypy` passes, and the module can be imported without hardware attached.

# Self-check

<self-check>
- [ ] All Python code has type hints on parameters and return types
- [ ] `logger` is explicitly created via `logging.getLogger(__name__)`, not used as an undefined name
- [ ] `serial` module is imported before referencing `serial.EIGHTBITS`, `serial.SerialException`, etc. -- `serial_asyncio` does NOT bring these into scope
- [ ] Every `except` block catches a specific exception, not bare `Exception`
- [ ] Every streaming loop has an exit condition (e.g., `stop_event.is_set()`)
- [ ] Hardware resources are released in `finally` blocks
- [ ] `MOCK_MODE` support is present for CI compatibility -- the check reads `os.environ`, never hardcoded
- [ ] No hardcoded paths like `/home/pi/` -- use environment variables or systemd specifiers
- [ ] `mypy` would pass on the generated code
- [ ] Systemd service templates use `MOCK_MODE=${MOCK_MODE:-false}`, not a hardcoded literal
- [ ] Before/after diff was shown for every Edit applied to existing files
</self-check>

# Common mistakes

- DO NOT use bare `except Exception` -- always catch specific exceptions (`serial.SerialException`, `OSError`, `RuntimeError`)
- DO NOT reference `logger` without defining it -- always add `logger = logging.getLogger(__name__)` at module level
- DO NOT reference `serial.EIGHTBITS` without `import serial` -- the `serial_asyncio` import does not bring `serial` constants into scope
- DO NOT write infinite loops without exit conditions -- always use `while not stop_event.is_set()` or a cancellation token
- DO NOT hardcode `/home/pi/` in systemd service files -- use `%h` (home directory specifier) or environment variables; the deploy user may be `ubuntu` (as on the staging environment)
- DO NOT use `raspi-config` menu instructions for camera enable -- on Raspberry Pi OS Bookworm (2024+), use `dtoverlay=camera_auto_detect` in `/boot/firmware/config.txt`
- DO NOT call `self.camera.capture_file()` without checking `self.camera is not None` first
- DO NOT open UART without a corresponding `finally` block to release the port
- DO NOT hardcode `MOCK_MODE=false` in systemd service templates -- use `MOCK_MODE=${MOCK_MODE:-false}` so environment-specific overrides work via drop-in files

# Escalation

- Stop and ask when: The target RPi model is not RPi5 (GPIO pinout and UART assignments differ)
- Stop and ask when: Multiple services need the same UART port (serial port contention)
- Stop and ask when: Camera resolution exceeds 1920x1080 at 30fps (may exceed RPi5 ISP throughput)
- Stop and ask when: The code must interact with MAVLink messages (hand off to `mavlink-integration`)

# What to surface

- Whether `MOCK_MODE` is being used (affects test validity)
- Hardware-specific side effects: UART monopolizes the serial port, camera locks the CSI interface
- The systemd service user and working directory (must match the target device)
- Any `asyncio.to_thread()` calls wrapping blocking I/O (pymavlink, picamera2 capture)

# Examples

<example name="uart-reader">
**Scenario**: Implement a UART reader for serial communication on the edge service

```python
import asyncio
import logging
import os
import serial
import serial_asyncio
from typing import Optional

logger = logging.getLogger(__name__)

MOCK_MODE = os.environ.get("MOCK_MODE", "false").lower() == "true"


class UARTReader:
    """Async UART reader for serial communication with flight controller."""

    def __init__(self, port: str = "/dev/ttyAMA0", baudrate: int = 57600) -> None:
        self.port = port
        self.baudrate = baudrate
        self.reader: Optional[asyncio.StreamReader] = None
        self.writer: Optional[asyncio.StreamWriter] = None

    async def connect(self) -> bool:
        """Establish UART connection. Returns True on success."""
        if MOCK_MODE:
            logger.info("MOCK_MODE: Simulating UART connection on %s", self.port)
            return True

        try:
            self.reader, self.writer = await serial_asyncio.open_serial_connection(
                url=self.port,
                baudrate=self.baudrate,
                bytesize=serial.EIGHTBITS,
                parity=serial.PARITY_NONE,
                stopbits=serial.STOPBITS_ONE,
            )
            logger.info("Connected to UART: %s", self.port)
            return True
        except serial.SerialException as err:
            logger.error("UART serial error on %s: %s", self.port, err)
            return False
        except OSError as err:
            logger.error("UART OS error on %s: %s", self.port, err)
            return False

    async def read_bytes(self, size: int, timeout: float = 1.0) -> Optional[bytes]:
        """Read bytes from UART with timeout."""
        if MOCK_MODE:
            return b"\x00" * size

        if self.reader is None:
            return None

        try:
            data = await asyncio.wait_for(self.reader.read(size), timeout=timeout)
            return data
        except asyncio.TimeoutError:
            logger.debug("UART read timeout after %.1fs", timeout)
            return None
        except serial.SerialException as err:
            logger.error("UART read serial error: %s", err)
            return None
        except OSError as err:
            logger.error("UART read OS error: %s", err)
            return None

    async def close(self) -> None:
        """Release UART resources."""
        if self.writer is not None:
            try:
                self.writer.close()
                await self.writer.wait_closed()
            except OSError as err:
                logger.warning("Error closing UART writer: %s", err)
            finally:
                self.writer = None
                self.reader = None
        logger.info("UART connection closed on %s", self.port)
```
</example>

<example name="camera-streamer">
**Scenario**: Camera streaming with exit condition and cleanup

```python
import asyncio
import logging
import os
from io import BytesIO
from typing import Awaitable, Callable, Optional

logger = logging.getLogger(__name__)

MOCK_MODE = os.environ.get("MOCK_MODE", "false").lower() == "true"


class CameraStreamer:
    """Async camera streamer using picamera2."""

    def __init__(self, width: int = 640, height: int = 480, fps: int = 30) -> None:
        self.width = width
        self.height = height
        self.fps = fps
        self._camera: Optional[object] = None  # Picamera2 instance

    async def initialize(self) -> bool:
        """Initialize camera. Returns True on success."""
        if MOCK_MODE:
            logger.info("MOCK_MODE: Simulating camera initialization")
            return True

        try:
            from picamera2 import Picamera2

            self._camera = Picamera2()
            config = self._camera.create_still_configuration(
                main={"size": (self.width, self.height)}
            )
            self._camera.configure(config)
            self._camera.start()
            logger.info("Camera initialized: %dx%d @ %dfps", self.width, self.height, self.fps)
            return True
        except RuntimeError as err:
            logger.error("Camera init runtime error: %s", err)
            return False
        except OSError as err:
            logger.error("Camera device not found: %s", err)
            return False

    async def capture_jpeg(self) -> Optional[bytes]:
        """Capture a single JPEG frame."""
        if MOCK_MODE:
            return b"\xff\xd8\xff\xe0" + b"\x00" * 100 + b"\xff\xd9"

        if self._camera is None:
            logger.warning("Camera not initialized, cannot capture")
            return None

        try:
            buffer = BytesIO()
            await asyncio.to_thread(self._camera.capture_file, buffer, format="jpeg")
            return buffer.getvalue()
        except RuntimeError as err:
            logger.error("Camera capture error: %s", err)
            return None

    async def stream_loop(
        self,
        callback: Callable[[bytes], Awaitable[None]],
        stop_event: asyncio.Event,
    ) -> None:
        """Stream JPEG frames until stop_event is set."""
        logger.info("Starting camera stream loop")
        try:
            while not stop_event.is_set():
                jpeg_data = await self.capture_jpeg()
                if jpeg_data:
                    await callback(jpeg_data)
                await asyncio.sleep(1.0 / self.fps)
        finally:
            logger.info("Camera stream loop stopped")

    async def cleanup(self) -> None:
        """Release camera resources."""
        if self._camera is not None:
            try:
                self._camera.stop()
                self._camera.close()
            except RuntimeError as err:
                logger.warning("Error closing camera: %s", err)
            finally:
                self._camera = None
        logger.info("Camera resources released")
```
</example>

<example name="systemd-service">
**Scenario**: Systemd service template (parameterized, 12-factor compliant)

```ini
# /etc/systemd/system/<device>@.service
[Unit]
Description=Edge Control Box Service
After=network.target

[Service]
Type=simple
User=%i
WorkingDirectory=%h/<device>
ExecStart=%h/<device>/venv/bin/python src/send_data.py
Restart=always
RestartSec=10
Environment="PYTHONUNBUFFERED=1"
Environment="LOG_LEVEL=INFO"
Environment="MOCK_MODE=${MOCK_MODE:-false}"

[Install]
WantedBy=multi-user.target
```

Usage: `sudo systemctl enable <device>@ubuntu` (where `ubuntu` is the deploy user).

To override `MOCK_MODE` for a specific device, create a drop-in:
```bash
sudo systemctl edit <device>@ubuntu
# Add: [Service]
# Add: Environment="MOCK_MODE=true"
```
</example>

# Failure modes

| Mode | Symptom | Detection | Fix |
|------|---------|-----------|-----|
| UART permission denied | `PermissionError` on `/dev/ttyAMA0` | Exception caught in `connect()` | Add user to `dialout` group (`sudo usermod -a -G dialout $USER`), then log out and back in |
| Camera not detected | `RuntimeError` during `Picamera2()` init | Exception caught in `initialize()` | Verify `dtoverlay=camera_auto_detect` in `/boot/firmware/config.txt` and reboot; check CSI cable connection |
| CPU thermal throttling | Performance degradation, CPU temp > 80C | `get_cpu_temperature()` returns > 80.0 | Add heatsink, reduce camera resolution, or reduce telemetry polling rate |
| Serial port contention | `serial.SerialException` with "device busy" | Another process holds `/dev/ttyAMA0` | Check for other UART consumers (`lsof /dev/ttyAMA0`), stop conflicting service |

# Related skills

- `mavlink-integration` -- compose for MAVLink protocol handling; embedded-systems provides the UART transport layer, mavlink-integration provides message parsing and command sequences
- `docker-build` -- defer for creating the edge service Docker image (ARM64)
- `code-standards` -- code-standards defines Python style rules (black, isort, flake8, mypy) that apply to all edge service code
- `api-integration` -- the edge service WebSocket server (port 8081) that the web portal connects to is documented in api-integration
