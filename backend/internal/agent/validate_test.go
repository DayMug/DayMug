package agent

import (
	"go/ast"
	"go/parser"
	"go/token"
	"strings"
	"testing"
)

func TestValidate(t *testing.T) {
	tests := []struct {
		name    string
		evt     StreamEvent
		wantErr string
	}{
		{
			name: "text kinds carry prose",
			evt:  StreamEvent{Kind: KindDelta, Content: "not json {"},
		},
		{
			name: "session info is a sentence, not a payload",
			evt:  StreamEvent{Kind: KindSessionInfo, Content: "CAS resumed thread abc"},
		},
		{
			name: "tool input delta is a JSON fragment, valid only once reassembled",
			evt:  StreamEvent{Kind: KindToolInputDelta, Content: `{"command":"ec`},
		},
		{
			name: "well-formed tool result",
			evt:  StreamEvent{Kind: KindToolResult, Content: `{"id":"t1","name":"Bash"}`},
		},
		{
			// The shape a payload takes after something rewrote it as text and
			// ate an escape: still a string, no longer JSON.
			name:    "mangled tool result payload",
			evt:     StreamEvent{Kind: KindToolResult, Content: `{"id":"t1","input":{"path":"C:\work"}}`},
			wantErr: "not valid JSON",
		},
		{
			name:    "structured kind with an empty payload",
			evt:     StreamEvent{Kind: KindUsage},
			wantErr: "carries no payload",
		},
		{
			name:    "question retraction without a request id",
			evt:     StreamEvent{Kind: KindUserQuestionResolved},
			wantErr: "carries no request id",
		},
		{
			name:    "typo'd kind",
			evt:     StreamEvent{Kind: "tool_use_started", Content: "{}"},
			wantErr: "unknown Kind",
		},
		{
			name:    "no kind at all",
			evt:     StreamEvent{Content: "orphan"},
			wantErr: "no Kind",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			err := tc.evt.Validate()
			switch {
			case tc.wantErr == "" && err != nil:
				t.Fatalf("Validate() = %v, want nil", err)
			case tc.wantErr != "" && err == nil:
				t.Fatalf("Validate() = nil, want an error mentioning %q", tc.wantErr)
			case tc.wantErr != "" && !strings.Contains(err.Error(), tc.wantErr):
				t.Fatalf("Validate() = %v, want an error mentioning %q", err, tc.wantErr)
			}
		})
	}
}

// knownKinds is hand-maintained, and the cost of forgetting an entry is that
// Validate starts rejecting a Kind the adapters legitimately emit. Read the
// constants out of the source so adding one without updating the map fails
// here rather than in production.
func TestKnownKindsCoversEveryConstant(t *testing.T) {
	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, "event.go", nil, 0)
	if err != nil {
		t.Fatalf("parse event.go: %v", err)
	}

	declared := 0
	ast.Inspect(file, func(n ast.Node) bool {
		spec, ok := n.(*ast.ValueSpec)
		if !ok {
			return true
		}
		for _, name := range spec.Names {
			if !strings.HasPrefix(name.Name, "Kind") {
				continue
			}
			declared++
			lit, ok := spec.Values[0].(*ast.BasicLit)
			if !ok {
				t.Errorf("%s is not a literal constant; this check cannot read it", name.Name)
				continue
			}
			value := strings.Trim(lit.Value, `"`)
			if !knownKinds[value] {
				t.Errorf("%s (%q) is missing from knownKinds in validate.go", name.Name, value)
			}
		}
		return true
	})
	if declared == 0 {
		t.Fatal("found no Kind constants in event.go; this check is not reading the file it thinks it is")
	}
}
