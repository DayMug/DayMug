package imbridge

import (
	"os"
	"testing"

	"github.com/DayMug/DayMug/backend/internal/service"
	"github.com/DayMug/DayMug/backend/internal/service/modeltest"
)

// Production ships no built-in models; seed the fixture so conversation flows
// under test have valid models without a registry row per test account.
func TestMain(m *testing.M) {
	service.ProviderModels = modeltest.Models()
	os.Exit(m.Run())
}
