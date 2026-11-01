package main

import (
	"os"

	"dualflow-core/internal/app"
	"dualflow-core/internal/diagnostics"
)

func main() {
	diagnostics.Info("DualFlow Network Core Daemon process starting (PID: %d)", os.Getpid())
	application := app.NewApp()
	if err := application.Run(); err != nil {
		diagnostics.Error("Fatal core runtime error: %v", err)
		os.Exit(1)
	}
	diagnostics.Info("DualFlow Network Core Daemon exiting.")
}

