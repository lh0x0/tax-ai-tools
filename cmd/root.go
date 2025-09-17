package cmd

import (
	"fmt"
	"os"

	"github.com/spf13/cobra"
	"tools/internal/logger"
)

var version = "1.0.0"

var rootCmd = &cobra.Command{
	Use:   "tools",
	Short: "Tools CLI - A command-line interface for various utilities",
	Long: `Tools CLI is a flexible command-line interface that provides
various utilities and tools for development and automation tasks.

This application is built with Go and Cobra, making it easy to extend
with additional subcommands as needed.`,
	Version: version,
	Run: func(cmd *cobra.Command, args []string) {
		log := logger.WithComponent("root")
		log.Info().
			Str("version", version).
			Msg("Tools CLI executed")
		
		fmt.Println("Welcome to Tools CLI!")
		fmt.Println("Use --help to see available commands and options.")
	},
}

func Execute() {
	log := logger.WithComponent("cmd")
	
	if err := rootCmd.Execute(); err != nil {
		log.Error().
			Err(err).
			Msg("Command execution failed")
		fmt.Fprintf(os.Stderr, "Error executing command: %v\n", err)
		os.Exit(1)
	}
}

func init() {
	rootCmd.Flags().BoolP("version", "v", false, "Print version information")
}