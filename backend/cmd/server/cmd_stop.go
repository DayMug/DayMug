package main

import (
	"errors"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/DayMug/DayMug/backend/internal/config"
)

// writePIDFile records pid so `daymug stop` can find this instance. The
// returned cleanup removes the file only while it still names pid, so an
// instance that lost a race for the file never deletes its successor's.
func writePIDFile(path string, pid int) (func(), error) {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return nil, err
	}
	if err := os.WriteFile(path, []byte(strconv.Itoa(pid)+"\n"), 0o644); err != nil {
		return nil, err
	}
	return func() {
		if got, err := readPIDFile(path); err == nil && got == pid {
			_ = os.Remove(path)
		}
	}, nil
}

func readPIDFile(path string) (int, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return 0, err
	}
	pid, err := strconv.Atoi(strings.TrimSpace(string(data)))
	if err != nil || pid <= 0 {
		return 0, fmt.Errorf("pid file %s is malformed", path)
	}
	return pid, nil
}

// pidRunsBinary guards against pid reuse after a crash left a stale pid file:
// on Linux the pid must still be executing exe. A self-upgrade replaces the
// binary under a running server, which the kernel reports as "<path> (deleted)".
func pidRunsBinary(pid int, exe string) bool {
	if syscall.Kill(pid, 0) != nil {
		return false
	}
	if runtime.GOOS != "linux" {
		return true
	}
	target, err := os.Readlink(fmt.Sprintf("/proc/%d/exe", pid))
	if err != nil {
		return false
	}
	return strings.TrimSuffix(target, " (deleted)") == exe
}

func cmdStop() {
	fs := flag.NewFlagSet("stop", flag.ExitOnError)
	timeout := fs.Duration("timeout", 90*time.Second, "How long to wait for the graceful drain to finish")
	if err := fs.Parse(os.Args[2:]); err != nil {
		fatalf("parse args: %v", err)
	}
	exe, err := resolveBinPath()
	if err != nil {
		fatalf("resolve binary path: %v", err)
	}
	pidPath := config.DefaultPIDPath()
	pid, err := readPIDFile(pidPath)
	if errors.Is(err, os.ErrNotExist) {
		fmt.Printf("daymug is not running (no %s)\n", pidPath)
		return
	}
	if err != nil {
		fatalf("%v", err)
	}
	if !pidRunsBinary(pid, exe) {
		_ = os.Remove(pidPath)
		fmt.Printf("daymug is not running (removed stale %s)\n", pidPath)
		return
	}
	// SIGINT is the signal serve always honours; see stopPolicy.
	if err := syscall.Kill(pid, syscall.SIGINT); err != nil {
		fatalf("signal pid %d: %v", pid, err)
	}
	fmt.Printf("stopping daymug (pid %d), waiting for in-flight work to drain...\n", pid)
	deadline := time.Now().Add(*timeout)
	for time.Now().Before(deadline) {
		if syscall.Kill(pid, 0) != nil {
			fmt.Println("stopped")
			return
		}
		time.Sleep(200 * time.Millisecond)
	}
	fatalf("pid %d still running after %s", pid, *timeout)
}
