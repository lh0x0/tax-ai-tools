package logger

import (
	"context"
	"io"
	"os"
	"strings"
	"time"

	"github.com/rs/zerolog"
	"github.com/rs/zerolog/log"
)

// LogConfig holds logging configuration
type LogConfig struct {
	Level      string // trace, debug, info, warn, error, fatal, panic
	Format     string // json, console
	TimeFormat string // RFC3339, Unix, or custom format
	Output     string // stdout, stderr, or file path
}

// DefaultConfig returns a sensible default logging configuration
func DefaultConfig() LogConfig {
	return LogConfig{
		Level:      "info",
		Format:     "console",
		TimeFormat: time.RFC3339,
		Output:     "stdout",
	}
}

// Setup initializes the global logger with the provided configuration
func Setup(config LogConfig) error {
	// Set log level
	level, err := zerolog.ParseLevel(strings.ToLower(config.Level))
	if err != nil {
		return err
	}
	zerolog.SetGlobalLevel(level)

	// Configure output
	var output io.Writer
	switch config.Output {
	case "stdout":
		output = os.Stdout
	case "stderr":
		output = os.Stderr
	default:
		// Assume it's a file path
		file, err := os.OpenFile(config.Output, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0666)
		if err != nil {
			return err
		}
		output = file
	}

	// Configure format
	switch strings.ToLower(config.Format) {
	case "console":
		output = zerolog.ConsoleWriter{
			Out:        output,
			TimeFormat: config.TimeFormat,
			NoColor:    false,
		}
	case "json":
		// JSON format is the default for zerolog
	default:
		// Default to console for unknown formats
		output = zerolog.ConsoleWriter{
			Out:        output,
			TimeFormat: config.TimeFormat,
			NoColor:    false,
		}
	}

	// Set global logger
	log.Logger = zerolog.New(output).With().
		Timestamp().
		Caller().
		Logger()

	// Configure time format
	if config.TimeFormat != "" {
		zerolog.TimeFieldFormat = config.TimeFormat
	}

	return nil
}

// GetLogger returns a logger instance
func GetLogger() zerolog.Logger {
	return log.Logger
}

// WithContext returns a logger with context
func WithContext(ctx context.Context) *zerolog.Logger {
	return log.Ctx(ctx)
}

// WithComponent returns a logger with a component field
func WithComponent(component string) zerolog.Logger {
	return log.Logger.With().Str("component", component).Logger()
}

// WithRequestID returns a logger with a request ID field
func WithRequestID(requestID string) zerolog.Logger {
	return log.Logger.With().Str("request_id", requestID).Logger()
}

// WithUserID returns a logger with a user ID field
func WithUserID(userID string) zerolog.Logger {
	return log.Logger.With().Str("user_id", userID).Logger()
}

// WithFields returns a logger with custom fields
func WithFields(fields map[string]interface{}) zerolog.Logger {
	logger := log.Logger
	for key, value := range fields {
		logger = logger.With().Interface(key, value).Logger()
	}
	return logger
}

// Info logs an info message
func Info(msg string) {
	log.Info().Msg(msg)
}

// Debug logs a debug message
func Debug(msg string) {
	log.Debug().Msg(msg)
}

// Warn logs a warning message
func Warn(msg string) {
	log.Warn().Msg(msg)
}

// Error logs an error message
func Error(err error, msg string) {
	log.Error().Err(err).Msg(msg)
}

// Fatal logs a fatal message and exits
func Fatal(err error, msg string) {
	log.Fatal().Err(err).Msg(msg)
}

// Panic logs a panic message and panics
func Panic(err error, msg string) {
	log.Panic().Err(err).Msg(msg)
}