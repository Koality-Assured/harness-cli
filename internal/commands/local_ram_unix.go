//go:build !windows

package commands

import (
	"math"
	"os"
	"os/exec"
	"runtime"
	"strconv"
	"strings"
)

func probeSystemRAM() *float64 {
	if runtime.GOOS == "linux" {
		data, err := os.ReadFile("/proc/meminfo")
		if err == nil {
			for _, line := range strings.Split(string(data), "\n") {
				if strings.HasPrefix(line, "MemTotal:") {
					fields := strings.Fields(line)
					if len(fields) >= 2 {
						if kb, err := strconv.ParseFloat(fields[1], 64); err == nil {
							gb := math.Round((kb/(1024*1024))*10) / 10
							return &gb
						}
					}
				}
			}
		}
	} else if runtime.GOOS == "darwin" {
		out, err := exec.Command("sysctl", "-n", "hw.memsize").Output()
		if err == nil {
			if bytesVal, err := strconv.ParseFloat(strings.TrimSpace(string(out)), 64); err == nil {
				gb := math.Round((bytesVal/(1024*1024*1024))*10) / 10
				return &gb
			}
		}
	}
	return nil
}
