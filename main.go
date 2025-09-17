package main

import (
	"log"
	"os"

	"github.com/joho/godotenv"
	"tools/cmd"
	"tools/internal/config"
	"tools/internal/logger"
)

func main() {
	// Load environment variables
	if err := godotenv.Load(); err != nil {
		log.Printf("Warning: Could not load .env file: %v", err)
	}

	// Load configuration
	cfg, err := config.Load()
	if err != nil {
		log.Printf("Warning: Could not load configuration: %v", err)
		// Use default logger config if main config fails
		if err := logger.Setup(logger.DefaultConfig()); err != nil {
			log.Fatalf("Failed to initialize logger: %v", err)
		}
	} else {
		// Initialize logger with configuration
		if err := logger.Setup(cfg.GetLoggerConfig()); err != nil {
			log.Fatalf("Failed to initialize logger: %v", err)
		}
	}

	// Log application startup
	log := logger.WithComponent("main")
	log.Info().Msg("Starting Tools CLI application")

	// Execute CLI commands
	cmd.Execute()

	// Log application shutdown
	log.Info().Msg("Tools CLI application shutdown")
	os.Exit(0)
}