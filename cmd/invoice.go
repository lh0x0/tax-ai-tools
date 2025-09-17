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
	"tools/internal/invoice"
	"tools/internal/logger"
	"tools/pkg/models"
)

var invoiceCmd = &cobra.Command{
	Use:   "invoice [pdf-file]",
	Short: "Extract structured invoice data from PDF using Google Document AI",
	Long: `Process a PDF invoice using Google Document AI's specialized invoice parser
to extract structured data such as amounts, dates, vendor information, and more.

This command uses Google Document AI's invoice processor which is specifically 
trained to understand invoice formats and extract key business information with
high accuracy. The output is always in JSON format containing the structured
invoice data.

Required environment variables:
  GOOGLE_APPLICATION_CREDENTIALS - Path to service account JSON file, OR
  GOOGLE_CREDENTIALS - Inline JSON credentials string
  GOOGLE_CLOUD_PROJECT - Your Google Cloud project ID
  GOOGLE_CLOUD_LOCATION - Processing location (us, eu, etc.)
  DOCUMENT_AI_PROCESSOR_ID - Your Document AI invoice processor ID`,
	Example: `  # Extract invoice data to stdout (JSON format)
  tools invoice invoice.pdf

  # Save extracted data to JSON file
  tools invoice invoice.pdf -o invoice-data.json

  # Include confidence scores for each extracted field
  tools invoice invoice.pdf --confidence

  # Process with custom timeout
  tools invoice large-invoice.pdf --timeout 120`,
	Args: cobra.ExactArgs(1),
	RunE: runInvoice,
}

// InvoiceOutput represents the JSON output structure for invoice processing
type InvoiceOutput struct {
	// Invoice contains the extracted and structured invoice data
	Invoice InvoiceData `json:"invoice"`

	// Confidence contains confidence scores for each extracted field (optional)
	Confidence map[string]float32 `json:"confidence,omitempty"`

	// Metadata contains processing information
	Metadata ProcessingMetadata `json:"metadata"`
}

// InvoiceData represents the structured invoice information
type InvoiceData struct {
	ID            string     `json:"id"`
	InvoiceNumber string     `json:"invoice_number"`
	Type          string     `json:"type"`
	Vendor        string     `json:"vendor"`
	Customer      string     `json:"customer"`
	IssueDate     *time.Time `json:"issue_date,omitempty"`
	DueDate       *time.Time `json:"due_date,omitempty"`
	PaymentDate   *time.Time `json:"payment_date,omitempty"`
	NetAmount     int64      `json:"net_amount_cents"`
	VATAmount     int64      `json:"vat_amount_cents"`
	GrossAmount   int64      `json:"gross_amount_cents"`
	Currency      string     `json:"currency"`
	IsPaid        bool       `json:"is_paid"`
	Reference     string     `json:"reference,omitempty"`
	Description   string     `json:"description,omitempty"`
	CreatedAt     time.Time  `json:"created_at"`
	UpdatedAt     time.Time  `json:"updated_at"`
}

// ProcessingMetadata contains information about the processing operation
type ProcessingMetadata struct {
	FileName           string        `json:"file_name"`
	FileSize           int64         `json:"file_size_bytes"`
	ProcessedAt        time.Time     `json:"processed_at"`
	ProcessingDuration time.Duration `json:"processing_duration"`
	ProcessorUsed      string        `json:"processor_used"`
}

func init() {
	rootCmd.AddCommand(invoiceCmd)

	invoiceCmd.Flags().StringP("output", "o", "", "Output file path (default: stdout)")
	invoiceCmd.Flags().Bool("confidence", false, "Include confidence scores in output")
	invoiceCmd.Flags().Int("timeout", 120, "Processing timeout in seconds")
}

func runInvoice(cmd *cobra.Command, args []string) error {
	log := logger.WithComponent("invoice")

	// Get flags
	outputPath, _ := cmd.Flags().GetString("output")
	includeConfidence, _ := cmd.Flags().GetBool("confidence")
	timeoutSecs, _ := cmd.Flags().GetInt("timeout")

	pdfPath := args[0]

	log.Info().
		Str("file", pdfPath).
		Str("output", outputPath).
		Bool("confidence", includeConfidence).
		Int("timeout", timeoutSecs).
		Msg("Starting invoice processing")

	// Validate and get file info
	fileInfo, err := validateInvoicePDF(pdfPath, log)
	if err != nil {
		return err
	}

	// Create context with timeout and signal handling
	ctx, cancel := createInvoiceContext(timeoutSecs, log)
	defer cancel()

	// Create invoice processor
	processor, err := createInvoiceProcessor(ctx, log)
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
		Msg("Processing invoice PDF with Document AI")

	// Process invoice
	startTime := time.Now()
	var invoiceData *InvoiceData
	var confidence map[string]float32

	if includeConfidence {
		modelInvoice, conf, err := processor.ProcessInvoiceWithConfidence(ctx, pdfFile)
		if err != nil {
			return handleInvoiceError(err, log)
		}
		invoiceData = convertToInvoiceData(modelInvoice)
		confidence = conf
	} else {
		modelInvoice, err := processor.ProcessInvoice(ctx, pdfFile)
		if err != nil {
			return handleInvoiceError(err, log)
		}
		invoiceData = convertToInvoiceData(modelInvoice)
	}

	processingDuration := time.Since(startTime)

	log.Info().
		Str("invoice_number", invoiceData.InvoiceNumber).
		Str("vendor", invoiceData.Vendor).
		Float64("gross_amount", float64(invoiceData.GrossAmount)/100).
		Str("currency", invoiceData.Currency).
		Dur("duration", processingDuration).
		Msg("Invoice processing completed successfully")

	// Create output structure
	output := InvoiceOutput{
		Invoice: *invoiceData,
		Metadata: ProcessingMetadata{
			FileName:           filepath.Base(fileInfo.Name()),
			FileSize:           fileInfo.Size(),
			ProcessedAt:        time.Now(),
			ProcessingDuration: processingDuration,
			ProcessorUsed:      "Google Document AI Invoice Parser",
		},
	}

	if includeConfidence {
		output.Confidence = confidence
	}

	// Output results as JSON
	return outputInvoiceResults(output, outputPath, log)
}

// validateInvoicePDF validates the PDF file for invoice processing
func validateInvoicePDF(pdfPath string, log zerolog.Logger) (os.FileInfo, error) {
	// Check if file exists and get info
	fileInfo, err := os.Stat(pdfPath)
	if err != nil {
		if os.IsNotExist(err) {
			log.Error().
				Str("file", pdfPath).
				Msg("Invoice PDF file not found")
			return nil, fmt.Errorf("invoice PDF file not found: %s", pdfPath)
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

	// Check file extension
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

	if fileInfo.Size() > invoice.MaxDocumentSizeBytes {
		log.Error().
			Str("file", pdfPath).
			Int64("size", fileInfo.Size()).
			Int64("max_size", invoice.MaxDocumentSizeBytes).
			Msg("PDF file exceeds maximum size limit")
		return nil, fmt.Errorf("PDF file too large (%d bytes). Maximum size is %d bytes (20MB)",
			fileInfo.Size(), invoice.MaxDocumentSizeBytes)
	}

	return fileInfo, nil
}

// createInvoiceContext creates a context with timeout and signal handling
func createInvoiceContext(timeoutSecs int, log zerolog.Logger) (context.Context, context.CancelFunc) {
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
				Msg("Received interrupt signal, canceling invoice processing")
			cancel()
		case <-ctx.Done():
			// Context completed normally
		}
	}()

	return ctx, cancel
}

// createInvoiceProcessor creates and configures the invoice processor
func createInvoiceProcessor(ctx context.Context, log zerolog.Logger) (invoice.InvoiceProcessor, error) {
	processor, err := invoice.NewDocumentAIInvoiceProcessor(ctx)
	if err != nil {
		if errors.Is(err, invoice.ErrMissingCredentials) {
			log.Error().
				Err(err).
				Msg("Google Cloud credentials not configured")
			return nil, fmt.Errorf("missing Google Cloud credentials. Please set one of:\n" +
				"  GOOGLE_APPLICATION_CREDENTIALS=/path/to/service-account-key.json\n" +
				"  GOOGLE_CREDENTIALS='<json-credentials>'\n" +
				"Also ensure these are set:\n" +
				"  GOOGLE_PROJECT_ID=your-project-id\n" +
				"  GOOGLE_LOCATION=us (or eu)\n" +
				"  GOOGLE_PROCESSOR_ID=your-processor-id\n" +
				"Original error: %w", err)
		}
		if errors.Is(err, invoice.ErrInvalidConfiguration) {
			log.Error().
				Err(err).
				Msg("Document AI configuration invalid")
			return nil, fmt.Errorf("invalid Document AI configuration. Please check your .env file:\n" +
				"  GOOGLE_CLOUD_PROJECT - your Google Cloud project ID\n" +
				"  GOOGLE_CLOUD_LOCATION - processing location (us, eu, etc.)\n" +
				"  DOCUMENT_AI_PROCESSOR_ID - your Document AI processor ID\n" +
				"Original error: %w", err)
		}
		log.Error().
			Err(err).
			Msg("Failed to create invoice processor")
		return nil, fmt.Errorf("failed to create invoice processor: %w", err)
	}

	log.Debug().Msg("Invoice processor created successfully")
	return processor, nil
}

// handleInvoiceError provides user-friendly error messages for invoice processing failures
func handleInvoiceError(err error, log zerolog.Logger) error {
	log.Error().Err(err).Msg("Invoice processing failed")

	errStr := err.Error()

	switch {
	case errors.Is(err, context.DeadlineExceeded):
		return fmt.Errorf("invoice processing timed out. Try increasing --timeout or processing a smaller file")
	case errors.Is(err, context.Canceled):
		return fmt.Errorf("invoice processing was canceled")
	case errors.Is(err, invoice.ErrInvalidPDF):
		return fmt.Errorf("invalid or corrupted PDF file. Please check the file integrity")
	case errors.Is(err, invoice.ErrDocumentTooLarge):
		return fmt.Errorf("PDF file is too large (maximum 20MB). Try compressing or splitting the file")
	case errors.Is(err, invoice.ErrProcessorNotFound):
		return fmt.Errorf("Document AI processor not found. Please check your GOOGLE_PROCESSOR_ID environment variable")
	case errors.Is(err, invoice.ErrMissingRequiredField):
		return fmt.Errorf("could not extract required invoice fields. The PDF may not be a valid invoice format")
	case strings.Contains(errStr, "Unauthenticated") ||
		strings.Contains(errStr, "invalid_grant") ||
		strings.Contains(errStr, "auth:") ||
		strings.Contains(errStr, "credentials"):
		return fmt.Errorf("Google Cloud authentication failed. Please check your credentials:\n\n" +
			"1. Set GOOGLE_APPLICATION_CREDENTIALS to your service account JSON file path\n" +
			"2. Or set GOOGLE_CREDENTIALS with inline JSON credentials\n" +
			"3. Ensure the service account has 'Document AI API User' role\n\n" +
			"Original error: %v", err)
	case strings.Contains(errStr, "PERMISSION_DENIED"):
		return fmt.Errorf("permission denied. Please ensure your service account has 'Document AI API User' role")
	case strings.Contains(errStr, "QUOTA_EXCEEDED"):
		return fmt.Errorf("Document AI API quota exceeded. Check your project quotas in Google Cloud Console")
	case errors.Is(err, invoice.ErrProcessingFailed):
		return fmt.Errorf("Document AI processing failed. This may be due to network issues or service unavailability: %w", err)
	default:
		return fmt.Errorf("invoice processing failed: %w", err)
	}
}

// convertToInvoiceData converts models.Invoice to InvoiceData for JSON output
func convertToInvoiceData(modelInvoice *models.Invoice) *InvoiceData {
	data := &InvoiceData{
		ID:            modelInvoice.ID,
		InvoiceNumber: modelInvoice.InvoiceNumber,
		Type:          modelInvoice.Type,
		Vendor:        modelInvoice.Vendor,
		Customer:      modelInvoice.Customer,
		NetAmount:     modelInvoice.NetAmount,
		VATAmount:     modelInvoice.VATAmount,
		GrossAmount:   modelInvoice.GrossAmount,
		Currency:      modelInvoice.Currency,
		IsPaid:        modelInvoice.IsPaid,
		Reference:     modelInvoice.Reference,
		Description:   modelInvoice.Description,
		CreatedAt:     modelInvoice.CreatedAt,
		UpdatedAt:     modelInvoice.UpdatedAt,
	}

	// Handle potentially zero time values
	if !modelInvoice.IssueDate.IsZero() {
		data.IssueDate = &modelInvoice.IssueDate
	}
	if !modelInvoice.DueDate.IsZero() {
		data.DueDate = &modelInvoice.DueDate
	}
	if modelInvoice.PaymentDate != nil && !modelInvoice.PaymentDate.IsZero() {
		data.PaymentDate = modelInvoice.PaymentDate
	}

	return data
}

// outputInvoiceResults formats and outputs the invoice processing results as JSON
func outputInvoiceResults(output InvoiceOutput, outputPath string, log zerolog.Logger) error {
	// Marshal to JSON with pretty printing
	jsonData, err := json.MarshalIndent(output, "", "  ")
	if err != nil {
		log.Error().Err(err).Msg("Failed to marshal invoice data to JSON")
		return fmt.Errorf("failed to create JSON output: %w", err)
	}

	// Write output
	if outputPath != "" {
		// Write to file
		err = os.WriteFile(outputPath, jsonData, 0644)
		if err != nil {
			log.Error().
				Err(err).
				Str("output_file", outputPath).
				Msg("Failed to write output file")
			return fmt.Errorf("failed to write output file: %w", err)
		}

		log.Info().
			Str("output_file", outputPath).
			Int("bytes", len(jsonData)).
			Msg("Invoice data written to file")
	} else {
		// Write to stdout
		_, err = os.Stdout.Write(jsonData)
		if err != nil {
			log.Error().Err(err).Msg("Failed to write to stdout")
			return fmt.Errorf("failed to write output: %w", err)
		}
		// Add newline for better terminal output
		fmt.Println()
	}

	return nil
}