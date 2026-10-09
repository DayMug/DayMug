package storetest

import (
	"go/ast"
	"go/parser"
	"go/token"
	"path/filepath"
	"reflect"
	"sort"
	"testing"

	"github.com/DayMug/DayMug/backend/internal/store"
)

// conformanceUncovered lists store.Store methods no conformance test calls
// yet, so the Fake's version of them is unverified against SQLite. It may only
// shrink: TestConformanceCoversStoreInterface fails on a new uncovered method
// (add a conformance case, or list it here with a reason if its result is
// engine-specific) and on an entry that is now covered (delete the line).
var conformanceUncovered = map[string]string{
	// Engine-specific by contract (engine name, file size, reclaimed bytes,
	// lifecycle): the two implementations are not meant to agree.
	"Backend":          "maintenance: engine-specific",
	"Close":            "maintenance: engine-specific",
	"DatabaseSize":     "maintenance: engine-specific",
	"Init":             "maintenance: engine-specific",
	"OptimizeDatabase": "maintenance: engine-specific",

	// Not yet covered: debt to pay down, not an exemption.
	"ClearMessages":                    "no conformance case yet",
	"CountUserMessages":                "no conformance case yet",
	"CreateConversationRecord":         "no conformance case yet",
	"DeleteBot":                        "no conformance case yet",
	"DeleteConversationsUpdatedBefore": "no conformance case yet",
	"DeleteExpiredSessions":            "no conformance case yet",
	"DeleteUser":                       "no conformance case yet",
	"DisableConversationShare":         "no conformance case yet",
	"EnableConversationShare":          "no conformance case yet",
	"GetBotThreadByConversation":       "no conformance case yet",
	"GetOwner":                         "no conformance case yet",
	"GetSessionID":                     "no conformance case yet",
	"GetSharedConversation":            "no conformance case yet",
	"GetUserByEmail":                   "no conformance case yet",
	"LatestMessageTime":                "no conformance case yet",
	"ListBots":                         "no conformance case yet",
	"ListConversationAttention":        "no conformance case yet",
	"ListConversations":                "no conformance case yet",
	"ListConversationsPage":            "no conformance case yet",
	"ListUsers":                        "no conformance case yet",
	"RecordClaudeTokenUsage":           "no conformance case yet",
	"ReorderAgents":                    "no conformance case yet",
	"ReorderPinnedConversations":       "no conformance case yet",
	"ResetConversationSession":         "no conformance case yet",
	"SetConversationAttention":         "no conformance case yet",
	"SetSessionID":                     "no conformance case yet",
	"SetUserAdmin":                     "no conformance case yet",
	"SetUserBarkURL":                   "no conformance case yet",
	"SetUserDefaultModel":              "no conformance case yet",
	"SetUserDisabled":                  "no conformance case yet",
	"SetUserEmail":                     "no conformance case yet",
	"SetUserEnv":                       "no conformance case yet",
	"SetUserNotificationChannel":       "no conformance case yet",
	"SetUserPassword":                  "no conformance case yet",
	"SetUserPushDeerKey":               "no conformance case yet",
	"SetUserSandboxMode":               "no conformance case yet",
	"SetUserWorkDir":                   "no conformance case yet",
	"UpdateConversationAccount":        "no conformance case yet",
	"UpdateConversationContextUsage":   "no conformance case yet",
	"UpdateConversationModel":          "no conformance case yet",
	"UpdateConversationNotifications":  "no conformance case yet",
	"UpdateConversationThinkLevel":     "no conformance case yet",
	"UpdateConversationTitle":          "no conformance case yet",
	"UpdateConversationWorkDir":        "no conformance case yet",
	"UpdateUsageEventContext":          "no conformance case yet",
}

// conformanceCalls returns the names of every method invoked on the
// conformance store variable (`s`) across the conformance test files.
func conformanceCalls(t *testing.T) map[string]bool {
	t.Helper()
	files, err := filepath.Glob("conformance*_test.go")
	if err != nil || len(files) == 0 {
		t.Fatalf("glob conformance tests: %v (found %d)", err, len(files))
	}
	called := make(map[string]bool)
	fset := token.NewFileSet()
	for _, name := range files {
		f, err := parser.ParseFile(fset, name, nil, 0)
		if err != nil {
			t.Fatalf("parse %s: %v", name, err)
		}
		ast.Inspect(f, func(n ast.Node) bool {
			// Skip implCase.build closures: the SQLite builder calls Init and
			// Close on its own store, which exercises nothing on the Fake.
			if lit, ok := n.(*ast.FuncLit); ok && returnsConformanceStore(lit) {
				return false
			}
			call, ok := n.(*ast.CallExpr)
			if !ok {
				return true
			}
			sel, ok := call.Fun.(*ast.SelectorExpr)
			if !ok {
				return true
			}
			if recv, ok := sel.X.(*ast.Ident); ok && recv.Name == "s" {
				called[sel.Sel.Name] = true
			}
			return true
		})
	}
	return called
}

func returnsConformanceStore(lit *ast.FuncLit) bool {
	if lit.Type.Results == nil {
		return false
	}
	for _, r := range lit.Type.Results.List {
		if id, ok := r.Type.(*ast.Ident); ok && id.Name == "conformanceStore" {
			return true
		}
	}
	return false
}

// Every method of store.Store must be exercised against both implementations
// by the conformance suite, or be explicitly allowlisted above. The Fake
// hand-writes each one, and an unverified hand-written method is exactly
// where the double drifts from production and lets wrong code pass.
func TestConformanceCoversStoreInterface(t *testing.T) {
	called := conformanceCalls(t)
	iface := reflect.TypeOf((*store.Store)(nil)).Elem()
	methods := make(map[string]bool, iface.NumMethod())
	var missing []string
	for i := 0; i < iface.NumMethod(); i++ {
		name := iface.Method(i).Name
		methods[name] = true
		if !called[name] {
			if _, allowed := conformanceUncovered[name]; !allowed {
				missing = append(missing, name)
			}
		}
	}
	var stale []string
	for name := range conformanceUncovered {
		if !methods[name] || called[name] {
			stale = append(stale, name)
		}
	}
	sort.Strings(missing)
	sort.Strings(stale)
	if len(missing) > 0 {
		t.Errorf("store.Store methods not exercised by any conformance test and not allowlisted: %v", missing)
	}
	if len(stale) > 0 {
		t.Errorf("conformanceUncovered entries that are now covered or no longer exist — delete them: %v", stale)
	}
	covered := 0
	for name := range methods {
		if called[name] {
			covered++
		}
	}
	t.Logf("conformance covers %d of %d store.Store methods", covered, len(methods))
}
