package adaptivepool

import (
	"os"
	"strconv"
	"strings"
)

// cgroupMemoryLimitFiles are read in order: cgroup v2, then v1
var cgroupMemoryLimitFiles = []string{
	"/sys/fs/cgroup/memory.max",
	"/sys/fs/cgroup/memory/memory.limit_in_bytes",
}

// cgroupMemoryLimit returns the container's memory limit in bytes, or 0 when
// there is none or it can't be read (for example outside Linux)
func cgroupMemoryLimit() uint64 {
	for _, f := range cgroupMemoryLimitFiles {
		if b, err := os.ReadFile(f); err == nil {
			return parseCgroupMemoryLimit(string(b))
		}
	}
	return 0
}

// parseCgroupMemoryLimit parses a cgroup memory limit file. "max" (v2) and
// the near-MaxInt64 value v1 reports for no limit both mean unlimited.
func parseCgroupMemoryLimit(s string) uint64 {
	s = strings.TrimSpace(s)
	if s == "max" {
		return 0
	}
	n, err := strconv.ParseUint(s, 10, 64)
	if err != nil || n >= 1<<62 {
		return 0
	}
	return n
}
