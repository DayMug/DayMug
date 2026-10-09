package handler

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"testing"
)

// This file is a static layering gate, not a behaviour test. The handler →
// service refactor moved business orchestration into internal/service; handlers
// are meant to parse parameters, enforce the authorization boundary and render
// responses. Persistence *writes* are orchestration, so a write issued straight
// from a handler is the signal that a use case leaked back up a layer.
//
// It is a go/ast walk rather than a golangci-lint rule because what we forbid
// is a *call pattern* ("the write methods of this field"), not a package
// import — depguard can only express the latter, and it cannot waive per file.
// Keeping the check in handler/layering_test.go also matches the repo
// convention that every foo.go has its checks next to it.

// layeringStoreInterfaces are the store package interfaces whose write methods
// are off-limits to handlers. A value counts as "a store" when it is *declared*
// with one of these types — that is what separates h.Store.CreateUser (a store
// write, forbidden) from h.ops().CreateHuman (a service call, fine) even though
// both are Create* calls on a field-ish expression.
var layeringStoreInterfaces = map[string]bool{
	"Store":                true,
	"MaintenanceStore":     true,
	"UserStore":            true,
	"ProviderBindingStore": true,
	"SessionStore":         true,
	"ConversationStore":    true,
	"MessageStore":         true,
	"MessageQueueStore":    true,
	"AppSettingStore":      true,
	"BotThreadStore":       true,
	"UsageStore":           true,
	"UsageInsightsStore":   true,
	"BotStore":             true,
	"CronStore":            true,
	"MarketplaceStore":     true,
}

// layeringWritePrefixes are the naming conventions the store package uses for
// mutating methods. Matching is word-boundary aware, so "Set" matches
// SetUserAdmin but would not match a hypothetical Settings().
var layeringWritePrefixes = []string{
	"Create", "Update", "Set", "Delete", "Reset", "Reorder",
	"Archive", "Unarchive", "Ensure", "Regenerate", "Save", "Clear",
}

// layeringWaivers lists the store writes that are allowed to stay in the
// handler layer. Key is the file name; value maps method name → reason, or is
// empty to waive the whole file. An entry without a reason is a TODO in
// disguise. Granularity is file+method, so a *second* call to an already-waived
// method in the same file slips through — accepted, because the alternative
// (waiving by line) churns on every unrelated edit.
//
// The bar for pushing a write down into service is: the handler function issues
// >= 2 store writes, or it pairs a write with non-trivial decision logic. A
// lone pass-through write wrapped in a service call is pure indirection.
//
// Entries come in two flavours and are grouped accordingly: deliberate (the
// write belongs here and always will) and debt (not yet migrated). The gate's
// job either way is to stop *new* writes from appearing.
var layeringWaivers = map[string]map[string]string{
	// --- deliberate ---

	// Logout: the session row is the server side of the cookie the handler
	// is clearing. One pass-through write, no decision logic, error
	// deliberately ignored — an AuthOps.CloseSession would be pure
	// indirection.
	"auth.go": {
		"DeleteSession": "deliberate: session row mirrors the cookie the handler owns; single pass-through write",
	},
	// Best-effort "first writer wins" cwd lock issued after the file has
	// already landed on disk. Failure is logged, never surfaced; the rest
	// of the function is filesystem I/O.
	"upload.go": {
		"UpdateConversationWorkDir": "deliberate: best-effort cwd lock alongside the filesystem write; failure is logged, not surfaced",
	},

	// --- debt: resource families that predate the handler→service split ---
	//
	// (None right now. The last batch — bot / cron / bootstrap / oidc CRUD —
	// moved to service.BotOps, service.CronOps, service.BootstrapOps and
	// service.OIDCUserOps. Keep this section for the next one rather than
	// deleting it, so a future entry lands under the right heading.)
}

// layeringHit is one store write found in a handler production file.
type layeringHit struct {
	file   string
	line   int
	expr   string // rendered receiver, e.g. "h.Store"
	method string
}

func (h layeringHit) String() string {
	return h.file + ":" + strconv.Itoa(h.line) + ": " + h.expr + "." + h.method
}

func TestHandlersDoNotWriteToStoreDirectly(t *testing.T) {
	var violations []string
	for _, hit := range layeringScanPackage(t) {
		if !layeringWaived(hit.file, hit.method) {
			violations = append(violations, hit.String())
		}
	}
	sort.Strings(violations)
	if len(violations) > 0 {
		t.Errorf("handlers must not write to the store directly — move the write into internal/service, "+
			"or add a justified entry to layeringWaivers:\n\t%s", strings.Join(violations, "\n\t"))
	}
}

// TestLayeringWaiversAreLive keeps the waiver list from outliving the code it
// excuses: once a write is migrated into service, its waiver must go too,
// otherwise the list slowly re-opens the hole it was carved out of.
func TestLayeringWaiversAreLive(t *testing.T) {
	seen := map[string]map[string]bool{}
	for _, hit := range layeringScanPackage(t) {
		if seen[hit.file] == nil {
			seen[hit.file] = map[string]bool{}
		}
		seen[hit.file][hit.method] = true
	}
	for file, methods := range layeringWaivers {
		if len(seen[file]) == 0 {
			t.Errorf("layeringWaivers has a stale entry for %s: no direct store write left in that file", file)
			continue
		}
		for method := range methods {
			if !seen[file][method] {
				t.Errorf("layeringWaivers has a stale entry %s/%s: that write is gone — drop the waiver", file, method)
			}
		}
	}
}

// TestLayeringGateDiscriminatesStoreFromService pins the judgement that makes
// the gate useful rather than noisy: a Create* call is a violation because of
// what it is called *on*, not because of its name. h.Store.CreateUser is a
// store write; h.ops().CreateHuman is a service call that happens to share the
// prefix. If this ever regresses to "flag every Create*", the gate becomes
// unusable and someone will delete it.
func TestLayeringGateDiscriminatesStoreFromService(t *testing.T) {
	const src = `package handler

type h struct {
	Store store.Store
	Bots  store.BotStore
	Jobs  store.CronStore
	Ops   *service.AdminUserOps
}

func (x *h) f(ctx context.Context, s store.Store) {
	x.Store.CreateUser(ctx, nil)      // violation
	x.Bots.CreateBot(ctx, nil)        // violation
	x.Jobs.CreateCronJob(ctx, nil)    // violation
	s.SetUserAdmin(ctx, "", true)     // violation (store-typed param)
	bots, _ := x.Store.(store.BotStore)
	bots.DeleteBot(ctx, "", "")       // violation (type-asserted store)

	x.ops().CreateHuman(ctx, nil)     // ok: service call
	x.Ops.CreateHuman(ctx, nil)       // ok: field is not store-typed
	x.Store.GetUser(ctx, "")          // ok: read
	x.Store.Settings()                // ok: Set* needs a word boundary
	uploader.SaveFile("x")            // ok: unknown receiver
}
`
	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, "fixture.go", src, parser.SkipObjectResolution)
	if err != nil {
		t.Fatalf("parse fixture: %v", err)
	}
	fields := map[string]bool{}
	layeringCollectStoreFields(file, fields)
	hits := layeringScan(fset, "fixture.go", file, fields, layeringStoreIdents(file))

	var got []string
	for _, h := range hits {
		got = append(got, h.expr+"."+h.method)
	}
	sort.Strings(got)
	want := []string{
		"bots.DeleteBot",
		"s.SetUserAdmin",
		"x.Bots.CreateBot",
		"x.Jobs.CreateCronJob",
		"x.Store.CreateUser",
	}
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Errorf("store-write detection drifted\n got: %v\nwant: %v", got, want)
	}
}

func layeringScanPackage(t *testing.T) []layeringHit {
	t.Helper()
	fset := token.NewFileSet()
	files, err := layeringParseProductionFiles(fset, ".")
	if err != nil {
		t.Fatalf("parse handler package: %v", err)
	}

	// Struct fields typed as a store interface give us the field names to
	// watch for on selector chains (h.Store, h.Bots, h.Jobs). Collected
	// package-wide because handlers are declared across many files.
	fields := map[string]bool{}
	for _, f := range files {
		layeringCollectStoreFields(f, fields)
	}

	var hits []layeringHit
	for path, f := range files {
		// Locals, parameters and type-asserted values holding a store are
		// file-scoped. An over-broad name set here can only add findings,
		// never hide one.
		locals := layeringStoreIdents(f)
		hits = append(hits, layeringScan(fset, filepath.Base(path), f, fields, locals)...)
	}
	return hits
}

func layeringParseProductionFiles(fset *token.FileSet, dir string) (map[string]*ast.File, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, err
	}
	files := map[string]*ast.File{}
	for _, e := range entries {
		name := e.Name()
		if e.IsDir() || !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
			continue
		}
		path := filepath.Join(dir, name)
		f, err := parser.ParseFile(fset, path, nil, parser.SkipObjectResolution)
		if err != nil {
			return nil, err
		}
		files[path] = f
	}
	return files, nil
}

// layeringCollectStoreFields records struct field names declared with a store
// interface type.
func layeringCollectStoreFields(file *ast.File, out map[string]bool) {
	ast.Inspect(file, func(n ast.Node) bool {
		st, ok := n.(*ast.StructType)
		if !ok || st.Fields == nil {
			return true
		}
		for _, f := range st.Fields.List {
			if !layeringIsStoreType(f.Type) {
				continue
			}
			for _, id := range f.Names {
				out[id.Name] = true
			}
		}
		return true
	})
}

// layeringStoreIdents collects identifiers in one file bound to a store
// interface: function parameters/results, var declarations, and the LHS of a
// type assertion to a store interface.
func layeringStoreIdents(file *ast.File) map[string]bool {
	names := map[string]bool{}
	addFields := func(fl *ast.FieldList) {
		if fl == nil {
			return
		}
		for _, f := range fl.List {
			if !layeringIsStoreType(f.Type) {
				continue
			}
			for _, id := range f.Names {
				names[id.Name] = true
			}
		}
	}
	ast.Inspect(file, func(n ast.Node) bool {
		switch node := n.(type) {
		case *ast.FuncDecl:
			addFields(node.Recv)
			if node.Type != nil {
				addFields(node.Type.Params)
				addFields(node.Type.Results)
			}
		case *ast.FuncLit:
			if node.Type != nil {
				addFields(node.Type.Params)
				addFields(node.Type.Results)
			}
		case *ast.ValueSpec:
			if layeringIsStoreType(node.Type) {
				for _, id := range node.Names {
					names[id.Name] = true
				}
			}
		case *ast.AssignStmt:
			// botStore, _ := h.Store.(store.BotStore)
			if len(node.Rhs) != 1 || len(node.Lhs) == 0 {
				return true
			}
			ta, ok := node.Rhs[0].(*ast.TypeAssertExpr)
			if !ok || !layeringIsStoreType(ta.Type) {
				return true
			}
			if id, ok := node.Lhs[0].(*ast.Ident); ok {
				names[id.Name] = true
			}
		}
		return true
	})
	return names
}

func layeringScan(fset *token.FileSet, file string, f *ast.File, fields, locals map[string]bool) []layeringHit {
	var out []layeringHit
	ast.Inspect(f, func(n ast.Node) bool {
		call, ok := n.(*ast.CallExpr)
		if !ok {
			return true
		}
		sel, ok := call.Fun.(*ast.SelectorExpr)
		if !ok || !layeringIsWriteMethod(sel.Sel.Name) {
			return true
		}
		if !layeringIsStoreReceiver(sel.X, fields, locals) {
			return true
		}
		out = append(out, layeringHit{
			file:   file,
			line:   fset.Position(sel.Sel.Pos()).Line,
			expr:   layeringRender(sel.X),
			method: sel.Sel.Name,
		})
		return true
	})
	return out
}

// layeringIsStoreReceiver reports whether expr evaluates to a store value. It
// matches h.Store / h.Bots-style field selections, bare identifiers bound to a
// store, and inline type assertions — and deliberately does not match call
// results such as h.ops(), which is how service methods that share a store
// method's name (h.ops().CreateHuman) stay out of the report.
func layeringIsStoreReceiver(expr ast.Expr, fields, locals map[string]bool) bool {
	switch x := expr.(type) {
	case *ast.Ident:
		return locals[x.Name]
	case *ast.SelectorExpr:
		return fields[x.Sel.Name]
	case *ast.ParenExpr:
		return layeringIsStoreReceiver(x.X, fields, locals)
	case *ast.TypeAssertExpr:
		return layeringIsStoreType(x.Type)
	}
	return false
}

func layeringIsStoreType(expr ast.Expr) bool {
	sel, ok := expr.(*ast.SelectorExpr)
	if !ok {
		return false
	}
	pkg, ok := sel.X.(*ast.Ident)
	if !ok || pkg.Name != "store" {
		return false
	}
	return layeringStoreInterfaces[sel.Sel.Name]
}

func layeringIsWriteMethod(name string) bool {
	for _, p := range layeringWritePrefixes {
		if len(name) <= len(p) || !strings.HasPrefix(name, p) {
			continue
		}
		if c := name[len(p)]; c >= 'A' && c <= 'Z' {
			return true
		}
	}
	return false
}

func layeringWaived(file, method string) bool {
	methods, ok := layeringWaivers[file]
	if !ok {
		return false
	}
	if len(methods) == 0 {
		return true // whole-file waiver
	}
	_, ok = methods[method]
	return ok
}

func layeringRender(expr ast.Expr) string {
	switch x := expr.(type) {
	case *ast.Ident:
		return x.Name
	case *ast.SelectorExpr:
		return layeringRender(x.X) + "." + x.Sel.Name
	case *ast.ParenExpr:
		return "(" + layeringRender(x.X) + ")"
	case *ast.TypeAssertExpr:
		return layeringRender(x.X) + ".(" + layeringRender(x.Type) + ")"
	}
	return "?"
}
