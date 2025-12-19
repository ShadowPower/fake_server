package main

import (
	"log"
	"os"
	"os/signal"
	"syscall"
)

func main() {
	// 1. Optimize Syscall limits (from limit_linux.go or limit_other.go)
	optimizeLimits()

	// 2. Initialize Base Filesystem
	initFS()

	// 3. Start Services
	go runSSHServer()
	go runTelnetServer()
	go runRLoginServer()

	// 4. Wait for interrupt
	log.Println("Fake Server Suite Running...")
	log.Println("SSH: 2200, Telnet: 2300, RLogin: 5130")

	sig := make(chan os.Signal, 1)
	signal.Notify(sig, syscall.SIGINT, syscall.SIGTERM)
	<-sig
	log.Println("Shutting down...")
}
