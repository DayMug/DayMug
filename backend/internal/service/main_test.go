package service

import (
	"os"
	"testing"

	"github.com/DayMug/DayMug/backend/internal/service/modeltest"
)

// shippedProviderModels is ProviderModels as compiled, captured before
// TestMain swaps in the fixture.
var shippedProviderModels map[string][]string

func TestMain(m *testing.M) {
	shippedProviderModels = ProviderModels
	ProviderModels = modeltest.Models()
	os.Exit(m.Run())
}
