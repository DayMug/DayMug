package handler

import (
	"testing"

	"github.com/gin-gonic/gin"

	"github.com/DayMug/DayMug/backend/internal/agent/agenttest"
	"github.com/DayMug/DayMug/backend/internal/service"
	"github.com/DayMug/DayMug/backend/internal/store/storetest"
)

// The production assembly must leave no required collaborator unwired: a nil
// here is exactly what used to be swallowed by a nil-tolerant branch at run
// time instead of refusing startup.
func TestAssembleRuntimeWiresEveryCollaborator(t *testing.T) {
	b := &routeBuilder{store: storetest.New(), cfg: agenttest.Config()}
	drainer := service.NewDrainer()
	b.assembleRuntime(drainer)

	if err := b.rt.Validate(); err != nil {
		t.Fatal(err)
	}
	if b.rt.Drainer != drainer {
		t.Error("runtime does not use the process drainer the upgrade/shutdown path waits on")
	}
	if b.imBridge.Runtime != b.rt {
		t.Error("IM bridge runs on a different runtime than web chat")
	}
	if b.rt.PromptObserver != b.imBridge {
		t.Error("web-chat turns are not mirrored through the IM bridge")
	}
	if b.imManager.Handler != b.imBridge {
		t.Error("inbound IM messages are not routed to the bridge")
	}
	for _, provider := range []string{"claude", "codex", "claude-compatible", "openai-compatible"} {
		if _, ok := b.rt.Backends.Lookup(provider); !ok {
			t.Errorf("no backend registered for %s", provider)
		}
	}
}

func TestRegisterRoutesWithConfigValidatesRuntime(t *testing.T) {
	gin.SetMode(gin.TestMode)
	err := RegisterRoutes(gin.New(), storetest.New(), service.NewDrainer(), agenttest.Config(), RegisterRoutesOpts{})
	if err != nil {
		t.Fatalf("production route assembly: %v", err)
	}
}

// Cron, marketplace, bots and usage insights used to be mounted only when a
// type assertion on the store succeeded, so a test double missing one of them
// silently lost the routes. They are part of store.Store now; assembling the
// app with the in-memory Fake must mount every one of them.
func TestRegisterRoutesMountsEveryStoreBackedSurface(t *testing.T) {
	gin.SetMode(gin.TestMode)
	r := gin.New()
	if err := RegisterRoutes(r, storetest.New(), service.NewDrainer(), agenttest.Config(), RegisterRoutesOpts{}); err != nil {
		t.Fatalf("register routes: %v", err)
	}
	mounted := map[string]bool{}
	for _, route := range r.Routes() {
		mounted[route.Method+" "+route.Path] = true
	}
	for _, want := range []string{
		"GET /api/cron-jobs",
		"POST /api/cron-jobs",
		"GET /api/marketplace/apps",
		"POST /api/marketplace/apps",
		"GET /api/usage/insights",
		"GET /api/admin/usage/insights",
	} {
		if !mounted[want] {
			t.Errorf("route %s not mounted", want)
		}
	}
}
