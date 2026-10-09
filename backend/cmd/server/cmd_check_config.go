package main

import (
	"flag"
	"fmt"
	"os"

	"github.com/DayMug/DayMug/backend/internal/config"
)

// cmdCheckConfig validates a config file with this binary's rules. The
// upgrade preflight invokes it on the staged release, so its contract is the
// exit code: 0 means this binary would boot with the file.
func cmdCheckConfig() {
	fs := flag.NewFlagSet("check-config", flag.ExitOnError)
	configPath := fs.String("config", "", "Path to YAML config (overrides DAYMUG_CONFIG env)")
	_ = fs.Parse(os.Args[2:])
	if err := checkConfig(*configPath); err != nil {
		fatalf("%v", err)
	}
}

func checkConfig(path string) error {
	cfg, err := config.Load(path)
	if err != nil {
		return err
	}
	fmt.Printf("config OK: %s\n", cfg.Path())
	return nil
}
