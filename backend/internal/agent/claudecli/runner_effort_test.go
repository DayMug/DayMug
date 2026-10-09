package claudecli

import (
	"reflect"
	"testing"
)

func TestThinkLevelArgs(t *testing.T) {
	tests := []struct {
		name  string
		level string
		want  []string
	}{
		{name: "provider default", want: nil},
		{name: "explicit level", level: "high", want: []string{"--effort", "high"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := thinkLevelArgs(tt.level); !reflect.DeepEqual(got, tt.want) {
				t.Fatalf("thinkLevelArgs(%q) = %#v, want %#v", tt.level, got, tt.want)
			}
		})
	}
}
