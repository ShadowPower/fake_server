//go:build linux

package main

func optimizeLimits() {
	var rLim syscall.Rlimit
	if err := syscall.Getrlimit(syscall.RLIMIT_NOFILE, &rLim); err == nil {
		rLim.Cur = rLim.Max
		syscall.Setrlimit(syscall.RLIMIT_NOFILE, &rLim)
	}
}
