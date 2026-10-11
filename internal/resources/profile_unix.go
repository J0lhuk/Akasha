//go:build !windows

package resources

import (
	"os"
	"strconv"
	"strings"
)

func detectOS(p *Profile) {
	data, _ := os.ReadFile("/proc/meminfo")
	for _, line := range strings.Split(string(data), "\n") {
		f := strings.Fields(line)
		if len(f) < 2 {
			continue
		}
		n, _ := strconv.ParseUint(f[1], 10, 64)
		switch f[0] {
		case "MemTotal:":
			p.TotalMemory = n * 1024
		case "MemAvailable:":
			p.AvailableMemory = n * 1024
		}
	}
	limit, _ := os.ReadFile("/sys/fs/cgroup/memory.max")
	used, _ := os.ReadFile("/sys/fs/cgroup/memory.current")
	if n, e := strconv.ParseUint(strings.TrimSpace(string(limit)), 10, 64); e == nil && n > 0 {
		u, _ := strconv.ParseUint(strings.TrimSpace(string(used)), 10, 64)
		p.TotalMemory = min(p.TotalMemory, n)
		if u < n {
			p.AvailableMemory = min(p.AvailableMemory, n-u)
		}
	}
	cpu, _ := os.ReadFile("/proc/cpuinfo")
	packages := map[string]bool{}
	cores := map[string]bool{}
	for _, block := range strings.Split(string(cpu), "\n\n") {
		pack, core := "", ""
		for _, line := range strings.Split(block, "\n") {
			kv := strings.SplitN(line, ":", 2)
			if len(kv) != 2 {
				continue
			}
			switch strings.TrimSpace(kv[0]) {
			case "physical id":
				pack = strings.TrimSpace(kv[1])
			case "core id":
				core = strings.TrimSpace(kv[1])
			}
		}
		if pack != "" {
			packages[pack] = true
		}
		if core != "" {
			cores[pack+"/"+core] = true
		}
	}
	p.PhysicalCores = len(cores)
	p.CPUPackages = len(packages)
}
