//go:build windows

package commands

import (
	"math"
	"syscall"
	"unsafe"
)

func probeSystemRAM() *float64 {
	kernel32 := syscall.NewLazyDLL("kernel32.dll")
	proc := kernel32.NewProc("GlobalMemoryStatusEx")
	if proc != nil && proc.Find() == nil {
		type memorystatusex struct {
			length               uint32
			memoryLoad           uint32
			totalPhys            uint64
			availPhys            uint64
			totalPageFile        uint64
			availPageFile        uint64
			totalVirtual         uint64
			availVirtual         uint64
			availExtendedVirtual uint64
		}
		var mem memorystatusex
		mem.length = uint32(unsafe.Sizeof(mem))
		ret, _, _ := proc.Call(uintptr(unsafe.Pointer(&mem)))
		if ret != 0 && mem.totalPhys > 0 {
			gb := math.Round((float64(mem.totalPhys)/(1024*1024*1024))*10) / 10
			return &gb
		}
	}
	return nil
}
