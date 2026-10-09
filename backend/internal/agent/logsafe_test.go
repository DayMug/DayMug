package agent

import (
	"reflect"
	"strings"
	"testing"
)

func TestRedactedArgv(t *testing.T) {
	t.Parallel()
	argv := []string{"claude", "-p", "--append-system-prompt", "secret persona", "-c", `developer_instructions="token=abc"`, "--model", "opus"}
	got := RedactedArgv(argv, []string{"--append-system-prompt"}, []string{"developer_instructions="})
	want := []string{"claude", "-p", "--append-system-prompt", "<redacted 14 bytes>", "-c", "developer_instructions=<redacted 11 bytes>", "--model", "opus"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("RedactedArgv = %q, want %q", got, want)
	}
	if argv[3] != "secret persona" {
		t.Fatal("RedactedArgv must not mutate its input")
	}
}

func TestRedactedArgvTrailingFlag(t *testing.T) {
	t.Parallel()
	got := RedactedArgv([]string{"claude", "--append-system-prompt"}, []string{"--append-system-prompt"}, nil)
	if len(got) != 2 || got[1] != "--append-system-prompt" {
		t.Fatalf("got %q", got)
	}
}

func TestPromptLogValueHidesContent(t *testing.T) {
	t.Parallel()
	if v := PromptLogValue("my api key is sk-123"); strings.Contains(v, "sk-123") {
		t.Fatalf("prompt content leaked: %q", v)
	}
}
