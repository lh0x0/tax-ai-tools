package cmd

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/rs/zerolog"
	"github.com/spf13/cobra"
	"tools/internal/logger"
	"tools/internal/ocr"
)

var ocrCmd = &cobra.Command{
	Use:   "ocr [pdf-file]",
	Short: "Extract text from PDF using Google Cloud Vision OCR",
	Long: `Process a PDF file using Google Cloud Vision API to extract all text content.

This command uses Google Cloud Vision API's document text detection to extract
text from PDF files with high accuracy. The service supports multi-page PDFs
up to 5 pages and 20MB in size for synchronous processing.

Required environment variables:
  GOOGLE_APPLICATION_CREDENTIALS - Path to service account JSON file, OR
  GOOGLE_CREDENTIALS - Inline JSON credentials string
  GOOGLE_CLOUD_PROJECT - Your Google Cloud project ID`,
	Example: `  # Extract text from invoice.pdf to stdout
  tools ocr invoice.pdf

  # Save extracted text to file
  tools ocr invoice.pdf -o extracted.txt

  # Include metadata and output as JSON
  tools ocr invoice.pdf --metadata --json -o result.json

  # Process with custom timeout
  tools ocr large-document.pdf --timeout 600`,
	Args: cobra.ExactArgs(1),
	RunE: runOCR,
}

// OCROutput represents the JSON output structure when --json flag is used
type OCROutput struct {
	Text               string    `json:"text"`
	PageCount          int       `json:"page_count,omitempty"`
	Confidence         float32   `json:"confidence,omitempty"`
	LanguageCodes      []string  `json:"language_codes,omitempty"`
	ProcessedAt        time.Time `json:"processed_at,omitempty"`
	ProcessingDuration string    `json:"processing_duration,omitempty"`
	FileName           string    `json:"file_name"`
	FileSize           int64     `json:"file_size"`
}

func init() {
	rootCmd.AddCommand(ocrCmd)

	ocrCmd.Flags().StringP("output", "o", "", "Output file path (default: stdout)")
	ocrCmd.Flags().BoolP("metadata", "m", false, "Include metadata in output")
	ocrCmd.Flags().Bool("json", false, "Output as JSON")
	ocrCmd.Flags().Int("timeout", 300, "Processing timeout in seconds")
}

func runOCR(cmd *cobra.Command, args []string) error {
	log := logger.WithComponent("ocr")
	
	// Get flags
	outputPath, _ := cmd.Flags().GetString("output")
	includeMetadata, _ := cmd.Flags().GetBool("metadata")
	jsonOutput, _ := cmd.Flags().GetBool("json")
	timeoutSecs, _ := cmd.Flags().GetInt("timeout")
	
	pdfPath := args[0]
	
	log.Info().
		Str("file", pdfPath).
		Str("output", outputPath).
		Bool("metadata", includeMetadata).
		Bool("json", jsonOutput).
		Int("timeout", timeoutSecs).
		Msg("Starting OCR processing")

	// Validate and get file info
	fileInfo, err := validatePDFFile(pdfPath, log)
	if err != nil {
		return err
	}

	// Create context with timeout and signal handling
	ctx, cancel := createContextWithTimeout(timeoutSecs, log)
	defer cancel()

	// Create OCR service
	ocrService, err := createOCRService(ctx, log)
	if err != nil {
		return err
	}

	// Open PDF file
	pdfFile, err := os.Open(pdfPath)
	if err != nil {
		log.Error().
			Err(err).
			Str("file", pdfPath).
			Msg("Failed to open PDF file")
		return fmt.Errorf("failed to open PDF file: %w", err)
	}
	defer func() {
		if closeErr := pdfFile.Close(); closeErr != nil {
			log.Warn().Err(closeErr).Msg("Failed to close PDF file")
		}
	}()

	log.Info().
		Str("file", pdfPath).
		Int64("size", fileInfo.Size()).
		Msg("Processing PDF")

	// Process PDF
	startTime := time.Now()
	var result *ocr.OCRResult
	
	if includeMetadata || jsonOutput {
		result, err = ocrService.ProcessPDFWithMetadata(ctx, pdfFile)
	} else {
		text, processErr := ocrService.ProcessPDF(ctx, pdfFile)
		if processErr != nil {
			err = processErr
		} else {
			// Create minimal result for consistency
			result = &ocr.OCRResult{
				Text:               text,
				ProcessedAt:        time.Now(),
				ProcessingDuration: time.Since(startTime),
			}
		}
	}

	if err != nil {
		return handleOCRError(err, log)
	}

	processingDuration := time.Since(startTime)
	log.Info().
		Int("page_count", result.PageCount).
		Float32("confidence", result.Confidence).
		Dur("duration", processingDuration).
		Int("text_length", len(result.Text)).
		Msg("OCR processing completed successfully")

	// Format and output results
	return outputResults(result, fileInfo, outputPath, jsonOutput, includeMetadata, log)
}

// validatePDFFile checks if the file exists, is readable, and appears to be a PDF
func validatePDFFile(pdfPath string, log zerolog.Logger) (os.FileInfo, error) {
	// Check if file exists and get info
	fileInfo, err := os.Stat(pdfPath)
	if err != nil {
		if os.IsNotExist(err) {
			log.Error().
				Str("file", pdfPath).
				Msg("PDF file not found")
			return nil, fmt.Errorf("PDF file not found: %s", pdfPath)
		}
		if os.IsPermission(err) {
			log.Error().
				Str("file", pdfPath).
				Msg("Permission denied accessing PDF file")
			return nil, fmt.Errorf("permission denied accessing PDF file: %s", pdfPath)
		}
		return nil, fmt.Errorf("error accessing PDF file: %w", err)
	}

	// Check if it's a regular file
	if !fileInfo.Mode().IsRegular() {
		log.Error().
			Str("file", pdfPath).
			Msg("Path is not a regular file")
		return nil, fmt.Errorf("path is not a regular file: %s", pdfPath)
	}

	// Check file extension (basic validation)
	if !strings.HasSuffix(strings.ToLower(pdfPath), ".pdf") {
		log.Warn().
			Str("file", pdfPath).
			Msg("File does not have .pdf extension")
	}

	// Check file size
	if fileInfo.Size() == 0 {
		log.Error().
			Str("file", pdfPath).
			Msg("PDF file is empty")
		return nil, fmt.Errorf("PDF file is empty: %s", pdfPath)
	}

	if fileInfo.Size() > ocr.MaxFileSizeBytes {
		log.Error().
			Str("file", pdfPath).
			Int64("size", fileInfo.Size()).
			Int64("max_size", ocr.MaxFileSizeBytes).
			Msg("PDF file exceeds maximum size limit")
		return nil, fmt.Errorf("PDF file too large (%d bytes). Maximum size is %d bytes (20MB)", 
			fileInfo.Size(), ocr.MaxFileSizeBytes)
	}

	return fileInfo, nil
}

// createContextWithTimeout creates a context with timeout and signal handling
func createContextWithTimeout(timeoutSecs int, log zerolog.Logger) (context.Context, context.CancelFunc) {
	// Create context with timeout
	ctx, cancel := context.WithTimeout(context.Background(), time.Duration(timeoutSecs)*time.Second)

	// Handle interrupt signals for graceful shutdown
	sigChan := make(chan os.Signal, 1)
	signal.Notify(sigChan, os.Interrupt, syscall.SIGTERM)
	
	go func() {
		select {
		case sig := <-sigChan:
			log.Info().
				Str("signal", sig.String()).
				Msg("Received interrupt signal, canceling OCR processing")
			cancel()
		case <-ctx.Done():
			// Context completed normally
		}
	}()

	return ctx, cancel
}

// createOCRService creates and configures the OCR service
func createOCRService(ctx context.Context, log zerolog.Logger) (ocr.OCRService, error) {
	// Check if credentials are configured before attempting to create service
	hasCredentials := os.Getenv("GOOGLE_APPLICATION_CREDENTIALS") != "" || os.Getenv("GOOGLE_CREDENTIALS") != ""
	
	if !hasCredentials {
		log.Error().Msg("Google Cloud credentials not configured")
		return nil, fmt.Errorf("Google Cloud credentials not configured. Please set one of:\n\n" +
			"1. Export GOOGLE_APPLICATION_CREDENTIALS with path to service account JSON:\n" +
			"   export GOOGLE_APPLICATION_CREDENTIALS=/path/to/service-account-key.json\n\n" +
			"2. Export GOOGLE_CREDENTIALS with inline JSON:\n" +
			"   export GOOGLE_CREDENTIALS='{\"type\":\"service_account\",\"project_id\":\"your-project\",...}'\n\n" +
			"3. Use Application Default Credentials (if gcloud is configured):\n" +
			"   gcloud auth application-default login\n\n" +
			"4. Check that your .env file contains the credentials variables")
	}
	
	ocrService, err := ocr.NewGoogleVisionOCRService(ctx)
	if err != nil {
		if errors.Is(err, ocr.ErrMissingCredentials) {
			log.Error().
				Err(err).
				Msg("Google Cloud credentials validation failed")
			return nil, fmt.Errorf("Google Cloud credentials validation failed. Please verify:\n\n" +
				"1. Credentials file exists and is readable\n" +
				"2. JSON format is valid\n" +
				"3. Service account has proper permissions\n\n" +
				"Original error: %w", err)
		}
		log.Error().
			Err(err).
			Msg("Failed to create OCR service")
		return nil, fmt.Errorf("failed to create OCR service: %w", err)
	}

	log.Debug().Msg("OCR service created successfully")
	return ocrService, nil
}

// handleOCRError provides user-friendly error messages for OCR failures
func handleOCRError(err error, log zerolog.Logger) error {
	log.Error().Err(err).Msg("OCR processing failed")
	
	errStr := err.Error()

	switch {
	case errors.Is(err, context.DeadlineExceeded):
		return fmt.Errorf("OCR processing timed out. Try increasing --timeout or processing a smaller file")
	case errors.Is(err, context.Canceled):
		return fmt.Errorf("OCR processing was canceled")
	case errors.Is(err, ocr.ErrPDFTooLarge):
		return fmt.Errorf("PDF file is too large (maximum 20MB). Try compressing or splitting the file")
	case errors.Is(err, ocr.ErrTooManyPages):
		return fmt.Errorf("PDF has too many pages (maximum 5 pages). Try splitting into smaller files")
	case errors.Is(err, ocr.ErrInvalidPDF):
		return fmt.Errorf("invalid or corrupted PDF file. Please check the file integrity")
	case errors.Is(err, ocr.ErrEmptyDocument):
		return fmt.Errorf("no readable text found in the document. The PDF may contain only images or be corrupted")
	case strings.Contains(errStr, "Unauthenticated") || 
		 strings.Contains(errStr, "invalid_grant") || 
		 strings.Contains(errStr, "invalid_rapt") ||
		 strings.Contains(errStr, "auth:") ||
		 strings.Contains(errStr, "transport: per-RPC creds failed"):
		return fmt.Errorf("Google Cloud authentication failed. Please check your credentials:\n\n" +
			"1. Set GOOGLE_APPLICATION_CREDENTIALS to your service account JSON file path:\n" +
			"   export GOOGLE_APPLICATION_CREDENTIALS=/path/to/service-account-key.json\n\n" +
			"2. Or set GOOGLE_CREDENTIALS with inline JSON:\n" +
			"   export GOOGLE_CREDENTIALS='{\"type\":\"service_account\",\"project_id\":\"your-project\",...}'\n\n" +
			"3. Ensure the service account has 'Cloud Vision API User' role\n\n" +
			"4. If using Application Default Credentials, run:\n" +
			"   gcloud auth application-default login\n\n" +
			"Original error: %v", err)
	case strings.Contains(errStr, "PERMISSION_DENIED") ||
		 strings.Contains(errStr, "permission") ||
		 strings.Contains(errStr, "forbidden"):
		return fmt.Errorf("permission denied. Please ensure your Google Cloud service account has the 'Cloud Vision API User' role")
	case strings.Contains(errStr, "QUOTA_EXCEEDED") ||
		 strings.Contains(errStr, "quota"):
		return fmt.Errorf("Google Cloud Vision API quota exceeded. Check your project quotas in the Google Cloud Console")
	case strings.Contains(errStr, "API_KEY") ||
		 strings.Contains(errStr, "api key"):
		return fmt.Errorf("invalid API key. Please check your Google Cloud credentials")
	case errors.Is(err, ocr.ErrOCRFailed):
		return fmt.Errorf("OCR processing failed. This may be due to network issues, API quota limits, or service unavailability: %w", err)
	default:
		return fmt.Errorf("OCR processing failed: %w", err)
	}
}

// outputResults formats and outputs the OCR results
func outputResults(result *ocr.OCRResult, fileInfo os.FileInfo, outputPath string, jsonOutput, includeMetadata bool, log zerolog.Logger) error {
	var output strings.Builder
	var outputData []byte
	var err error

	if jsonOutput {
		// JSON output
		ocrOutput := OCROutput{
			Text:               result.Text,
			FileName:           filepath.Base(fileInfo.Name()),
			FileSize:           fileInfo.Size(),
			PageCount:          result.PageCount,
			Confidence:         result.Confidence,
			LanguageCodes:      result.LanguageCodes,
			ProcessedAt:        result.ProcessedAt,
			ProcessingDuration: result.ProcessingDuration.String(),
		}
		
		outputData, err = json.MarshalIndent(ocrOutput, "", "  ")
		if err != nil {
			log.Error().Err(err).Msg("Failed to marshal JSON output")
			return fmt.Errorf("failed to create JSON output: %w", err)
		}
	} else {
		// Text output
		if includeMetadata {
			// Add metadata header
			output.WriteString(fmt.Sprintf("=== OCR Results for %s ===\n", filepath.Base(fileInfo.Name())))
			output.WriteString(fmt.Sprintf("File size: %d bytes\n", fileInfo.Size()))
			if result.PageCount > 0 {
				output.WriteString(fmt.Sprintf("Pages processed: %d\n", result.PageCount))
			}
			if result.Confidence > 0 {
				output.WriteString(fmt.Sprintf("Confidence: %.1f%%\n", result.Confidence*100))
			}
			if len(result.LanguageCodes) > 0 {
				output.WriteString(fmt.Sprintf("Languages: %s\n", strings.Join(result.LanguageCodes, ", ")))
			}
			output.WriteString(fmt.Sprintf("Processing time: %v\n", result.ProcessingDuration))
			output.WriteString(fmt.Sprintf("Processed at: %s\n", result.ProcessedAt.Format(time.RFC3339)))
			output.WriteString("\n=== Extracted Text ===\n\n")
		}
		
		output.WriteString(result.Text)
		outputData = []byte(output.String())
	}

	// Write output
	if outputPath != "" {
		// Write to file
		err = os.WriteFile(outputPath, outputData, 0644)
		if err != nil {
			log.Error().
				Err(err).
				Str("output_file", outputPath).
				Msg("Failed to write output file")
			return fmt.Errorf("failed to write output file: %w", err)
		}
		
		log.Info().
			Str("output_file", outputPath).
			Int("bytes", len(outputData)).
			Msg("OCR results written to file")
	} else {
		// Write to stdout
		_, err = os.Stdout.Write(outputData)
		if err != nil {
			log.Error().Err(err).Msg("Failed to write to stdout")
			return fmt.Errorf("failed to write output: %w", err)
		}
		
		// Add newline if not JSON (JSON already has proper formatting)
		if !jsonOutput {
			fmt.Println()
		}
	}

	return nil
}