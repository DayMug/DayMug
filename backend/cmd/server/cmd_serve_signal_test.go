package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/signal"
	"path/filepath"
	"runtime"
	"strings"
	"syscall"
	"testing"
	"time"
)

type fakeStopEnv struct {
	vars     map[string]string
	goos     string
	unit     string
	userMgr  bool
	inUnit   bool
	show     string
	showErr  error
	showUser *bool
}

func (f *fakeStopEnv) policy(pid int) stopPolicy {
	goos := f.goos
	if goos == "" {
		goos = "linux"
	}
	return stopPolicy{
		getenv: func(k string) string { return f.vars[k] },
		goos:   goos,
		pid:    pid,
		systemdUnit: func() (string, bool, bool) {
			return f.unit, f.userMgr, f.inUnit
		},
		unitShow: func(_ context.Context, _ string, user bool) (string, error) {
			f.showUser = &user
			return f.show, f.showErr
		},
	}
}

func TestStopPolicyDecide(t *testing.T) {
	const pid = 4242
	systemd := map[string]string{"INVOCATION_ID": "abc"}
	cases := []struct {
		name string
		sig  os.Signal
		env  fakeStopEnv
		want bool
	}{
		{"SIGINT always stops", syscall.SIGINT, fakeStopEnv{}, true},
		{"foreground SIGTERM is ignored", syscall.SIGTERM, fakeStopEnv{}, false},
		{"opt-out env honours SIGTERM", syscall.SIGTERM,
			fakeStopEnv{vars: map[string]string{honorSIGTERMEnv: "1"}}, true},
		{"launchd stop is honoured", syscall.SIGTERM,
			fakeStopEnv{goos: "darwin", vars: map[string]string{"XPC_SERVICE_NAME": launchdAgentLabel}}, true},
		{"Terminal.app XPC name is not launchd", syscall.SIGTERM,
			fakeStopEnv{goos: "darwin", vars: map[string]string{"XPC_SERVICE_NAME": "0"}}, false},
		{"systemctl stop is honoured", syscall.SIGTERM,
			fakeStopEnv{vars: systemd, unit: "daymug.service", inUnit: true,
				show: fmt.Sprintf("MainPID=%d\nActiveState=deactivating\n", pid)}, true},
		{"stray kill while unit is active is ignored", syscall.SIGTERM,
			fakeStopEnv{vars: systemd, unit: "daymug.service", inUnit: true,
				show: fmt.Sprintf("MainPID=%d\nActiveState=active\n", pid)}, false},
		{"agent-launched instance in the prod cgroup ignores the unit state", syscall.SIGTERM,
			fakeStopEnv{vars: systemd, unit: "daymug.service", inUnit: true,
				show: "MainPID=1\nActiveState=deactivating\n"}, false},
		{"INVOCATION_ID outside a service cgroup is ignored", syscall.SIGTERM,
			fakeStopEnv{vars: systemd}, false},
		{"unreachable systemd fails open", syscall.SIGTERM,
			fakeStopEnv{vars: systemd, unit: "daymug.service", inUnit: true, showErr: errors.New("no bus")}, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, why := tc.env.policy(pid).decide(tc.sig)
			if got != tc.want {
				t.Fatalf("decide(%s) = %v (%s), want %v", tc.sig, got, why, tc.want)
			}
		})
	}
}

func TestStopPolicyQueriesTheRightManager(t *testing.T) {
	env := fakeStopEnv{vars: map[string]string{"INVOCATION_ID": "x"}, unit: "daymug.service", inUnit: true, userMgr: true}
	env.policy(1).decide(syscall.SIGTERM)
	if env.showUser == nil || !*env.showUser {
		t.Fatal("a user-manager unit must be queried with systemctl --user")
	}
}

func TestParseCgroupUnit(t *testing.T) {
	cases := []struct {
		cgroup string
		unit   string
		user   bool
		ok     bool
	}{
		{"0::/user.slice/user-1000.slice/user@1000.service/app.slice/daymug.service\n", "daymug.service", true, true},
		{"0::/system.slice/daymug.service\n", "daymug.service", false, true},
		{"0::/user.slice/user-1000.slice/session-3.scope\n", "", false, false},
		{"12:pids:/foo\n1:name=systemd:/bar\n", "", false, false},
	}
	for _, tc := range cases {
		unit, user, ok := parseCgroupUnit(tc.cgroup)
		if unit != tc.unit || user != tc.user || ok != tc.ok {
			t.Errorf("parseCgroupUnit(%q) = %q,%v,%v; want %q,%v,%v", tc.cgroup, unit, user, ok, tc.unit, tc.user, tc.ok)
		}
	}
}

// The regression this whole policy exists for: a stray SIGTERM must leave the
// server running, and only the later SIGINT ends the wait.
func TestAwaitStopIgnoresStraySIGTERM(t *testing.T) {
	sigCh := make(chan os.Signal, 2)
	sigCh <- syscall.SIGTERM
	sigCh <- syscall.SIGINT
	var logs []string
	logf := func(format string, args ...any) { logs = append(logs, fmt.Sprintf(format, args...)) }

	sig, err := awaitStop(make(chan error), sigCh, (&fakeStopEnv{}).policy(7), logf)
	if err != nil || sig != syscall.SIGINT {
		t.Fatalf("awaitStop = %v, %v; want SIGINT", sig, err)
	}
	if len(logs) != 2 || !strings.HasPrefix(logs[0], "ignoring terminated") {
		t.Fatalf("expected an 'ignoring' line before the drain line, got %q", logs)
	}
}

func TestAwaitStopReturnsServerError(t *testing.T) {
	serverErr := make(chan error, 1)
	serverErr <- errors.New("bind: address in use")
	sig, err := awaitStop(serverErr, make(chan os.Signal), (&fakeStopEnv{}).policy(7), t.Logf)
	if sig != nil || err == nil {
		t.Fatalf("awaitStop = %v, %v; want the server error", sig, err)
	}
}

func TestPIDFileCleanupOnlyRemovesOwnPID(t *testing.T) {
	path := filepath.Join(t.TempDir(), "data", "daymug.pid")
	cleanup, err := writePIDFile(path, 100)
	if err != nil {
		t.Fatal(err)
	}
	if got, err := readPIDFile(path); err != nil || got != 100 {
		t.Fatalf("readPIDFile = %d, %v", got, err)
	}
	// A successor instance took over the file before we exited.
	if err := os.WriteFile(path, []byte("200\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	cleanup()
	if got, _ := readPIDFile(path); got != 200 {
		t.Fatalf("cleanup removed a pid file it no longer owned (got %d)", got)
	}
}

func TestPIDRunsBinaryRejectsOtherExecutables(t *testing.T) {
	self, err := resolveBinPath()
	if err != nil {
		t.Fatal(err)
	}
	if !pidRunsBinary(os.Getpid(), self) {
		t.Fatal("own pid should match own executable")
	}
	if runtime.GOOS != "linux" {
		return // the executable check reads /proc
	}
	if pidRunsBinary(os.Getpid(), "/nonexistent/daymug") {
		t.Fatal("a pid running another executable must not match (pid reuse guard)")
	}
}

func TestReadPIDFileRejectsGarbage(t *testing.T) {
	path := filepath.Join(t.TempDir(), "daymug.pid")
	if err := os.WriteFile(path, []byte("not-a-pid"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := readPIDFile(path); err == nil {
		t.Fatal("expected an error for a malformed pid file")
	}
}

// Same regression through real signal delivery: the test process signals
// itself, exactly as `kill <pid>` from an agent's shell would.
func TestAwaitStopWithRealSignals(t *testing.T) {
	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)
	defer signal.Stop(sigCh)

	ignored := make(chan struct{}, 1)
	logf := func(format string, _ ...any) {
		if strings.HasPrefix(format, "ignoring") {
			ignored <- struct{}{}
		}
	}
	done := make(chan os.Signal, 1)
	go func() {
		sig, _ := awaitStop(make(chan error), sigCh, (&fakeStopEnv{}).policy(os.Getpid()), logf)
		done <- sig
	}()

	if err := syscall.Kill(os.Getpid(), syscall.SIGTERM); err != nil {
		t.Fatal(err)
	}
	select {
	case <-ignored:
	case sig := <-done:
		t.Fatalf("SIGTERM stopped the wait (%v)", sig)
	case <-time.After(5 * time.Second):
		t.Fatal("SIGTERM was never observed")
	}
	if err := syscall.Kill(os.Getpid(), syscall.SIGINT); err != nil {
		t.Fatal(err)
	}
	select {
	case sig := <-done:
		if sig != syscall.SIGINT {
			t.Fatalf("stopped on %v, want SIGINT", sig)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("SIGINT did not stop the wait")
	}
}
