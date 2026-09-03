# SWD Debug Probe + Multicore Workaround

## Problem
When using a debug probe (picoprobe/CMSIS-DAP) via SWD to flash RP2040 firmware that uses multicore (`-scheduler=cores`), the standard `reset` command leaves core1 in a bad state. This prevents TinyGo's `startSecondaryCores()` from successfully launching core1.

**Symptoms:**
- Program appears stuck in `SIO_IRQ_PROC0_IRQHandler`
- Core1 remains at ROM address (0x138 or 0x184) instead of running user code
- LED/main loop doesn't run

## Root Cause
The debug probe's reset doesn't fully reset core1's state, leaving it unable to respond to the FIFO handshake sequence used to launch it.

Reference: https://github.com/raspberrypi/debugprobe/issues/62

## Workaround

After flashing, trigger an AIRCR (Application Interrupt and Reset Control Register) system reset:

```bash
# Flash and reset properly
openocd -f interface/cmsis-dap.cfg -f target/rp2040.cfg \
  -c "adapter speed 5000" \
  -c "program firmware.elf" \
  -c "init" \
  -c "mww 0xe000ed0c 0x05fa0004" \
  -c "sleep 100" \
  -c "exit"
```

The magic value `0x05fa0004` writes to AIRCR:
- `0x05fa` = VECTKEY (required for write access)
- `0x0004` = SYSRESETREQ bit (request system reset)

## Alternative Workarounds

1. **Power cycle**: Unplug and replug the target board after flashing
2. **BOOTSEL flash**: Use BOOTSEL mode to flash UF2 files instead of SWD
3. **Single-core mode**: Use `-scheduler=tasks` if multicore isn't required (it is required, don't use that)

## Flash Helper Command

```bash
# One-liner for flash + proper reset
flash_pico() {
  openocd -f interface/cmsis-dap.cfg -f target/rp2040.cfg \
    -c "adapter speed 5000" \
    -c "program $1" \
    -c "init" \
    -c "mww 0xe000ed0c 0x05fa0004" \
    -c "sleep 100" \
    -c "exit"
}

# Usage: flash_pico firmware.elf
```
