package service

import (
	"errors"
	"strings"
	"testing"
)

const suspendedPayload = `{"request_id":"r1","questions":[{"id":"q1","header":"数据通道","question":"数据从哪来？","options":[{"label":"先查额度"},{"label":"只做 YouTube"}]},{"id":"q2","question":"范围？"}]}`

func TestSuspendedAnswerPromptListsEachAnswer(t *testing.T) {
	got, err := SuspendedAnswerPrompt(suspendedPayload, "r1", map[string][]string{"q1": {"只做 YouTube"}})
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"数据从哪来？\n  → 只做 YouTube", "范围？\n  → (no answer)"} {
		if !strings.Contains(got, want) {
			t.Errorf("prompt missing %q:\n%s", want, got)
		}
	}
}

func TestSuspendedAnswerPromptRejectsOtherRequest(t *testing.T) {
	_, err := SuspendedAnswerPrompt(suspendedPayload, "r2", map[string][]string{"q1": {"x"}})
	if !errors.Is(err, ErrSuspendedQuestionMismatch) {
		t.Fatalf("err = %v, want mismatch", err)
	}
}
