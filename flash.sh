#!/bin/bash
# Flash a .uf2 file to the Pico and monitor serial output
# Usage: ./flash.sh /path/to/firmware.uf2

if [ -z "$1" ]; then
    echo "Usage: $0 /path/to/firmware.uf2"
    exit 1
fi

UF2_FILE="$1"

if [ ! -f "$UF2_FILE" ]; then
    echo "Error: File not found: $UF2_FILE"
    exit 1
fi

# Send reboot command to enter bootloader
echo 'r' > /dev/ttyACM0 2>/dev/null
sleep 2

# Flash the firmware
picotool load "$UF2_FILE"
picotool reboot

# Wait for device to boot and monitor serial
sleep 3
#timeout 5 mcu mon -m /dev/ttyACM0
mcu mon -m /dev/ttyACM0
