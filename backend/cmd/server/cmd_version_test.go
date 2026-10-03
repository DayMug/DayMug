package main

import (
	"errors"
	"reflect"
	"strings"
	"testing"
)

func TestServiceStatusQueriesUserServiceManager(t *testing.T) {
	cases := []struct {
		goos     string
		wantCmd  []string
		wantText string
	}{
		{"linux", []string{"systemctl", "--user", "status", "daymug.service"}, "manager output"},
		{"darwin", []string{"launchctl", "print", launchdTarget()}, "manager output"},
		{"windows", nil, "No service manager on windows"},
	}
	for _, tc := range cases {
		t.Run(tc.goos, func(t *testing.T) {
			var got []string
			orig := runCommand
			t.Cleanup(func() { runCommand = orig })
			runCommand = func(name string, args ...string) ([]byte, error) {
				got = append([]string{name}, args...)
				// Non-zero exit for an inactive service must not hide the output.
				return []byte("manager output\n"), errors.New("exit status 3")
			}

			out := serviceStatus(tc.goos)
			if !reflect.DeepEqual(got, tc.wantCmd) {
				t.Errorf("command = %v, want %v", got, tc.wantCmd)
			}
			if !strings.Contains(out, tc.wantText) {
				t.Errorf("output = %q, want it to contain %q", out, tc.wantText)
			}
		})
	}
}
