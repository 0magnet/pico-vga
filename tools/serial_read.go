// Simple serial reader utility
package main

import (
	"fmt"
	"os"
	"time"

	"go.bug.st/serial"
)

func main() {
	port := "/dev/ttyACM0"
	if len(os.Args) > 1 {
		port = os.Args[1]
	}

	mode := &serial.Mode{
		BaudRate: 115200,
	}

	s, err := serial.Open(port, mode)
	if err != nil {
		fmt.Println("Error opening port:", err)
		os.Exit(1)
	}
	defer s.Close()

	// Set read timeout
	s.SetReadTimeout(1 * time.Second)

	// Read and print data
	buf := make([]byte, 4096)
	for i := 0; i < 20; i++ {
		n, err := s.Read(buf)
		if err != nil {
			break
		}
		if n > 0 {
			fmt.Print(string(buf[:n]))
		}
	}
}
