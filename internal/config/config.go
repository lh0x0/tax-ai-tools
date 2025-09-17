package config

import (
	"fmt"
	"os"

	"tools/internal/logger"
)

type Config struct {
	// OpenAI Configuration
	OpenAIAPIKey string

	// Google Cloud Configuration
	GoogleCloudProject    string
	GCSSourceBucket      string
	GCSOutputBucket      string
	DocumentAIProcessorID string
	GoogleCloudLocation  string
	DocumentAIProcessorVersion string
	GoogleServiceAccountKey string

	// Google Sheets Configuration
	GoogleSheetURL       string
	GoogleSheetWorksheet string

	// Optional: Google Cloud Storage Folder Configuration
	GCSSourceFolder string
	GCSOutputFolder string

	// Chart of Accounts Configuration
	ChartOfAccounts string

	// Logging Configuration
	LogLevel      string
	LogFormat     string
	LogTimeFormat string
	LogOutput     string
}

func Load() (*Config, error) {
	config := &Config{
		OpenAIAPIKey:               getEnv("OPENAI_API_KEY", ""),
		GoogleCloudProject:         getEnv("GOOGLE_CLOUD_PROJECT", ""),
		GCSSourceBucket:           getEnv("GCS_SOURCE_BUCKET", ""),
		GCSOutputBucket:           getEnv("GCS_OUTPUT_BUCKET", ""),
		DocumentAIProcessorID:      getEnv("DOCUMENT_AI_PROCESSOR_ID", ""),
		GoogleCloudLocation:        getEnv("GOOGLE_CLOUD_LOCATION", "us"),
		DocumentAIProcessorVersion: getEnv("DOCUMENT_AI_PROCESSOR_VERSION", ""),
		GoogleServiceAccountKey:    getEnv("GOOGLE_SERVICE_ACCOUNT_KEY", ""),
		GoogleSheetURL:            getEnv("GOOGLE_SHEET_URL", ""),
		GoogleSheetWorksheet:      getEnv("GOOGLE_SHEET_WORKSHEET", "DATEV_Bookings"),
		GCSSourceFolder:           getEnv("GCS_SOURCE_FOLDER", ""),
		GCSOutputFolder:           getEnv("GCS_OUTPUT_FOLDER", ""),
		ChartOfAccounts:           getEnv("CHART_OF_ACCOUNTS", "SKR04"),
		LogLevel:                  getEnv("LOG_LEVEL", "info"),
		LogFormat:                 getEnv("LOG_FORMAT", "console"),
		LogTimeFormat:             getEnv("LOG_TIME_FORMAT", "2006-01-02T15:04:05Z07:00"),
		LogOutput:                 getEnv("LOG_OUTPUT", "stdout"),
	}

	if err := config.validate(); err != nil {
		return nil, fmt.Errorf("config validation failed: %w", err)
	}

	return config, nil
}

func (c *Config) validate() error {
	if c.OpenAIAPIKey == "" {
		return fmt.Errorf("OPENAI_API_KEY is required")
	}
	if c.GoogleCloudProject == "" {
		return fmt.Errorf("GOOGLE_CLOUD_PROJECT is required")
	}
	if c.GCSSourceBucket == "" {
		return fmt.Errorf("GCS_SOURCE_BUCKET is required")
	}
	if c.GCSOutputBucket == "" {
		return fmt.Errorf("GCS_OUTPUT_BUCKET is required")
	}
	if c.DocumentAIProcessorID == "" {
		return fmt.Errorf("DOCUMENT_AI_PROCESSOR_ID is required")
	}
	if c.GoogleSheetURL == "" {
		return fmt.Errorf("GOOGLE_SHEET_URL is required")
	}
	return nil
}

// GetLoggerConfig returns a logger configuration from the main config
func (c *Config) GetLoggerConfig() logger.LogConfig {
	return logger.LogConfig{
		Level:      c.LogLevel,
		Format:     c.LogFormat,
		TimeFormat: c.LogTimeFormat,
		Output:     c.LogOutput,
	}
}

func getEnv(key, defaultValue string) string {
	if value := os.Getenv(key); value != "" {
		return value
	}
	return defaultValue
}