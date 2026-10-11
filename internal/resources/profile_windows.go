//go:build windows

package resources

import (
	"encoding/binary"
	"golang.org/x/sys/windows"
	"unsafe"
)

func detectOS(p *Profile) {
	kernel := windows.NewLazySystemDLL("kernel32.dll")
	mem := struct {
		Length, Load                                                                         uint32
		Total, Available, PageTotal, PageAvailable, VirtualTotal, VirtualAvailable, Extended uint64
	}{}
	mem.Length = uint32(unsafe.Sizeof(mem))
	if ok, _, _ := kernel.NewProc("GlobalMemoryStatusEx").Call(uintptr(unsafe.Pointer(&mem))); ok != 0 {
		p.TotalMemory = mem.Total
		p.AvailableMemory = mem.Available
	}
	topology := kernel.NewProc("GetLogicalProcessorInformationEx")
	var size uint32
	topology.Call(0xffff, 0, uintptr(unsafe.Pointer(&size)))
	if size == 0 || size > 16<<20 {
		return
	}
	buf := make([]byte, size)
	if ok, _, _ := topology.Call(0xffff, uintptr(unsafe.Pointer(&buf[0])), uintptr(unsafe.Pointer(&size))); ok == 0 {
		return
	}
	for offset := uint32(0); offset+8 <= size; {
		kind := binary.LittleEndian.Uint32(buf[offset:])
		length := binary.LittleEndian.Uint32(buf[offset+4:])
		if length < 8 || offset+length > size {
			break
		}
		if kind == 0 {
			p.PhysicalCores++
		}
		if kind == 3 {
			p.CPUPackages++
		}
		offset += length
	}
}
