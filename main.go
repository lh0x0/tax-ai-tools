package main

import (
	"log"

	"github.com/joho/godotenv"
	"tools/cmd"
)

func main() {
	if err := godotenv.Load(); err != nil {
		log.Printf("Warning: Could not load .env file: %v", err)
	}

	cmd.Execute()
}