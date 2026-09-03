# Makefile for color_run VGA example
#
# Build and flash workflow for Raspberry Pi Pico with TinyGo

.PHONY: build flash bootsel-go bootsel-c mon clean

# Build the TinyGo firmware
build:
	tinygo build -target=pico -o color_run.uf2 main.go

# Flash the firmware (device must be in BOOTSEL mode)
flash:
	picotool load -f color_run.uf2 && picotool reboot

# Build and flash in one step
all: build flash

# Reboot TinyGo firmware to BOOTSEL mode
# TinyGo firmware listens for 'r' character on serial to trigger reboot
# This works because the firmware calls machine.EnterBootloader() when it receives 'r'
bootsel-go:
	echo 'r' > /dev/ttyACM0

# Reboot C firmware to BOOTSEL mode
# C firmware typically has picotool reset interface enabled
# Use -f to force reboot even if device appears busy
bootsel-c:
	picotool reboot -f -u

# Serial monitor for debug output
# Uses mcu mon tool at 115200 baud (TinyGo default)
mon:
	mcu mon -m /dev/ttyACM0 -b 115200

# Build, reboot to bootsel, wait, flash, and monitor
# Full workflow for TinyGo development
dev: build
	@echo "Rebooting to BOOTSEL mode..."
	-echo 'r' > /dev/ttyACM0 2>/dev/null || true
	@sleep 3
	picotool load -f color_run.uf2 && picotool reboot
	@sleep 3
	mcu mon -m /dev/ttyACM0 -b 115200

clean:
	rm -f color_run.uf2 color_run.elf
