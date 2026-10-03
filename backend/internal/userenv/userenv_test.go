package userenv

import (
	"reflect"
	"testing"
)

func TestParseTextAssignments(t *testing.T) {
	got, err := Parse("TOKEN=abc=123\nEMPTY=\n\nPATH=/usr/bin")
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	want := map[string]string{"TOKEN": "abc=123", "EMPTY": "", "PATH": "/usr/bin"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("Parse() = %#v, want %#v", got, want)
	}
}

func TestParseRejectsInvalidAssignment(t *testing.T) {
	for _, raw := range []string{"MISSING", "1TOKEN=value", "BAD KEY=value", "TOKEN=bad\x00value"} {
		if _, err := Parse(raw); err == nil {
			t.Errorf("Parse(%q) succeeded, want error", raw)
		}
	}
}

// The JSON object format the old admin UI stored is converted by a startup
// migration; at runtime it is just an invalid line.
func TestParseRejectsJSONObject(t *testing.T) {
	if _, err := Parse(`{"TOKEN":"abc"}`); err == nil {
		t.Fatal("Parse accepted a JSON object")
	}
}
