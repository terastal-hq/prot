package registry

import "syscall"

func processExists(pid int) bool { return syscall.Kill(pid, 0) == nil }
