cmake_minimum_required(VERSION 3.13)

# Pull in SDK (must be before project)
include($ENV{PICO_SDK_PATH}/external/pico_sdk_import.cmake)
include($ENV{PICO_EXTRAS_PATH}/external/pico_extras_import.cmake)

project(scanvideo_debug C CXX ASM)
set(CMAKE_C_STANDARD 11)
set(CMAKE_CXX_STANDARD 17)

# Initialize the SDK
pico_sdk_init()

add_executable(scanvideo_debug
    scanvideo_minimal_debug.c
)

target_link_libraries(scanvideo_debug PRIVATE
    pico_multicore
    pico_stdlib
    pico_scanvideo_dpi
    hardware_clocks
)

# Enable USB output, disable UART output
pico_enable_stdio_usb(scanvideo_debug 1)
pico_enable_stdio_uart(scanvideo_debug 0)

pico_add_extra_outputs(scanvideo_debug)
