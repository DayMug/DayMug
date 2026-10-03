// Package archtest holds no production code. It exists so the layering the
// rest of the backend already follows is enforced by a failing test instead
// of by reviewer memory.
//
// The rules below describe today's import graph exactly — none of them is
// aspirational, and none required a code change to introduce. What they buy
// is the direction of travel: an adapter reaching for the store, or the store
// reaching back up into service, is the kind of edge that looks locally
// reasonable in a diff and is expensive to unpick a year later.
package archtest

import (
	"fmt"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"testing"
)

const modulePath = "github.com/DayMug/DayMug/backend"

// rule states which internal packages a layer may import. Prefixes match on
// package-path boundaries, so "internal/agent" covers internal/agent/codexapp
// but never a hypothetical internal/agentfoo.
type rule struct {
	// layer is the package prefix the rule applies to.
	layer string
	// allowed lists the internal prefixes it may import, transitively. Any
	// internal import outside this set fails the test.
	allowed []string
	// why explains the rule in the failure message — a bare "forbidden
	// import" tells whoever hits this nothing about which way to fix it.
	why string
}

var rules = []rule{
	{
		layer:   "internal/agent",
		allowed: []string{"internal/agent", "internal/config", "internal/prompts"},
		why: "adapters translate a provider's protocol into StreamEvents and nothing else. " +
			"They must not persist, read conversation state, or know about HTTP: the turn's " +
			"events go up to service, which owns both the database and the broadcast.",
	},
	{
		layer:   "internal/store",
		allowed: []string{"internal/store"},
		why: "the store is the bottom of the stack. An import of service or handler from here " +
			"is a cycle waiting to happen and puts business rules below the layer that owns them.",
	},
	{
		layer:   "internal/service",
		allowed: []string{"internal/agent", "internal/config", "internal/imbot", "internal/prompts", "internal/service", "internal/store", "internal/userenv"},
		why: "service is the business layer: it may use everything below it, but importing handler " +
			"(or middleware, which is handler-side) would make HTTP concerns a dependency of the logic.",
	},
	{
		// The leaves have no internal dependencies at all, which is what makes
		// the agent rule above transitive: agent may import config and
		// prompts, and neither can smuggle in a store.
		layer:   "internal/config",
		allowed: nil,
		why:     "config is a leaf: it is parsed before anything else exists and must not depend on a layer.",
	},
	{
		layer:   "internal/prompts",
		allowed: nil,
		why:     "prompts is a leaf holding embedded text; a dependency here would be pulled in by every layer.",
	},
	{
		layer:   "internal/userenv",
		allowed: nil,
		why:     "userenv is a leaf resolving per-user paths; it is imported by service and must stay dependency-free.",
	},
}

func TestLayering(t *testing.T) {
	imports := internalImports(t)

	for _, r := range rules {
		t.Run(r.layer, func(t *testing.T) {
			for _, v := range violations(imports, r) {
				t.Errorf("%s\n\n%s", v, r.why)
			}
		})
	}
}

// TestViolationsAreActuallyDetected is the guard on the guard. TestLayering
// passes on a clean tree, which is indistinguishable from passing because the
// matcher never matches anything — so feed it a graph that does break the
// rules and check it says so.
func TestViolationsAreActuallyDetected(t *testing.T) {
	dirty := map[string][]string{
		"internal/agent/codexapp": {"internal/store", "internal/config"},
		"internal/agent":          {"internal/config"},
	}
	agentRule := rules[0]
	if agentRule.layer != "internal/agent" {
		t.Fatalf("rules[0] is %s; this test pins the agent rule", agentRule.layer)
	}

	got := violations(dirty, agentRule)
	if len(got) != 1 {
		t.Fatalf("violations = %v, want exactly the store import", got)
	}
	if !strings.Contains(got[0], "internal/agent/codexapp") || !strings.Contains(got[0], "internal/store") {
		t.Fatalf("violation %q does not name the offending package and import", got[0])
	}
}

// violations lists every import under r.layer that r does not allow.
func violations(imports map[string][]string, r rule) []string {
	var out []string
	for _, pkg := range sortedKeys(imports) {
		if !underLayer(pkg, r.layer) {
			continue
		}
		for _, imp := range imports[pkg] {
			if allowedBy(imp, r.allowed) {
				continue
			}
			out = append(out, fmt.Sprintf("%s imports %s", pkg, imp))
		}
	}
	return out
}

// TestRulesCoverEveryLayer keeps the rule table from quietly going stale: a
// new top-level package under internal/ gets no layering guarantee at all
// unless someone decides where it sits, and the cheapest moment to decide is
// when it is created.
func TestRulesCoverEveryLayer(t *testing.T) {
	// Layers deliberately left unruled, with the reason they need none.
	exempt := map[string]string{
		"internal/archtest":   "this package",
		"internal/handler":    "the top layer: it wires everything and has nothing above it to protect",
		"internal/imbot":      "protocol clients for Slack/Feishu/Telegram/WeChat; imported by service, no rule needed yet",
		"internal/logfile":    "a leaf log helper",
		"internal/middleware": "handler-side; covered by the service rule that forbids importing it",
	}

	entries, err := os.ReadDir(filepath.Join(repoRoot(t), "internal"))
	if err != nil {
		t.Fatalf("read internal/: %v", err)
	}
	ruled := map[string]bool{}
	for _, r := range rules {
		ruled[r.layer] = true
	}
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		layer := "internal/" + entry.Name()
		if ruled[layer] || exempt[layer] != "" {
			continue
		}
		t.Errorf("%s has no layering rule and is not listed as exempt; add it to rules or to the exempt map with a reason", layer)
	}
}

// internalImports maps each internal package path to the internal packages it
// imports. Test files are excluded: a store test may legitimately reach for
// service's fixtures, and forbidding that would buy nothing — test code is not
// what ends up coupled in production.
func internalImports(t *testing.T) map[string][]string {
	t.Helper()
	root := repoRoot(t)
	out := map[string][]string{}
	fset := token.NewFileSet()

	err := filepath.WalkDir(filepath.Join(root, "internal"), func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() || !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return nil
		}
		file, err := parser.ParseFile(fset, path, nil, parser.ImportsOnly)
		if err != nil {
			return fmt.Errorf("parse %s: %w", path, err)
		}
		rel, err := filepath.Rel(root, filepath.Dir(path))
		if err != nil {
			return err
		}
		pkg := filepath.ToSlash(rel)
		for _, spec := range file.Imports {
			imported := strings.Trim(spec.Path.Value, `"`)
			if !strings.HasPrefix(imported, modulePath+"/internal/") {
				continue
			}
			trimmed := strings.TrimPrefix(imported, modulePath+"/")
			if trimmed == pkg || contains(out[pkg], trimmed) {
				continue
			}
			out[pkg] = append(out[pkg], trimmed)
		}
		// Record the package even when it imports nothing internal, so
		// TestRulesCoverEveryLayer and the rules see the full set.
		if _, ok := out[pkg]; !ok {
			out[pkg] = nil
		}
		return nil
	})
	if err != nil {
		t.Fatalf("walk internal/: %v", err)
	}
	if len(out) == 0 {
		t.Fatal("found no packages under internal/; the repo-root lookup is wrong")
	}
	return out
}

// repoRoot resolves backend/ from this test file's own location, so the test
// reads the tree it was compiled from rather than depending on the working
// directory or on anything outside the repo.
func repoRoot(t *testing.T) string {
	t.Helper()
	_, file, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("cannot locate this test file")
	}
	return filepath.Dir(filepath.Dir(filepath.Dir(file)))
}

// underLayer reports whether pkg is layer or a package beneath it.
func underLayer(pkg, layer string) bool {
	return pkg == layer || strings.HasPrefix(pkg, layer+"/")
}

func allowedBy(imported string, allowed []string) bool {
	for _, prefix := range allowed {
		if underLayer(imported, prefix) {
			return true
		}
	}
	return false
}

func contains(list []string, want string) bool {
	for _, item := range list {
		if item == want {
			return true
		}
	}
	return false
}

func sortedKeys(m map[string][]string) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}
