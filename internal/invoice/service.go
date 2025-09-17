// Package invoice provides invoice processing capabilities using Google Document AI.
//
// This package supports processing PDF invoices and extracting structured data
// using Google Cloud Document AI's specialized invoice parser processor.
//
// Required Environment Variables:
//   - GOOGLE_APPLICATION_CREDENTIALS: Path to service account JSON file, OR
//   - GOOGLE_CREDENTIALS: Inline JSON credentials string
//   - GOOGLE_PROJECT_ID: Google Cloud project ID
//   - GOOGLE_LOCATION: Processing location (e.g., "us", "eu")
//   - GOOGLE_PROCESSOR_ID: Document AI processor ID (optional, uses default invoice processor)
//
// Document AI API Limitations:
//   - Maximum file size: 20MB for synchronous processing
//   - Supported formats: PDF, TIFF, GIF, JPEG, PNG, BMP, WEBP
//   - Processing time: Typically 5-15 seconds per invoice
//   - Quota limits apply (check Google Cloud Console)
//
// Invoice Processing Features:
//   - Extracts key invoice fields (amounts, dates, parties, etc.)
//   - Handles multiple currencies
//   - Provides confidence scores for extracted data
//   - Converts monetary values to cents for precision
//   - Supports both payable and receivable invoices
package invoice

import (
	"context"
	"io"
	"time"

	"tools/pkg/models"
)

// InvoiceProcessor defines the interface for invoice processing services.
type InvoiceProcessor interface {
	// ProcessInvoice extracts structured data from an invoice PDF.
	// Returns a populated Invoice model with extracted information.
	ProcessInvoice(ctx context.Context, pdfData io.Reader) (*models.Invoice, error)

	// ProcessInvoiceWithConfidence extracts structured data with confidence scores.
	// Returns the Invoice model and a map of field names to confidence values (0.0-1.0).
	// Field names correspond to Document AI entity types (e.g., "invoice_id", "supplier_name").
	ProcessInvoiceWithConfidence(ctx context.Context, pdfData io.Reader) (*models.Invoice, map[string]float32, error)
}

// DocumentAIConfig holds configuration for Google Document AI processing.
type DocumentAIConfig struct {
	// ProjectID is the Google Cloud project ID where Document AI is enabled.
	ProjectID string

	// Location is the processing location (e.g., "us", "eu").
	// Should match where your Document AI processor is created.
	Location string

	// ProcessorID is the Document AI processor ID.
	// If empty, will attempt to find a default invoice processor.
	ProcessorID string

	// Timeout is the maximum time to wait for processing.
	// Default: 60 seconds.
	Timeout time.Duration

	// ProcessorVersion specifies a particular processor version.
	// If empty, uses the default version.
	ProcessorVersion string
}

// DefaultConfig returns a DocumentAIConfig with sensible defaults.
func DefaultConfig() DocumentAIConfig {
	return DocumentAIConfig{
		Location: "us",
		Timeout:  60 * time.Second,
	}
}

// InvoiceProcessingResult contains detailed processing results.
type InvoiceProcessingResult struct {
	// Invoice is the extracted invoice data.
	Invoice *models.Invoice

	// Confidence contains confidence scores for each extracted field.
	// Keys are Document AI entity types, values are confidence scores (0.0-1.0).
	Confidence map[string]float32

	// ProcessingTime is how long the Document AI processing took.
	ProcessingTime time.Duration

	// ProcessedAt is when the processing completed.
	ProcessedAt time.Time

	// DocumentAIResponse contains the raw Document AI response for advanced use cases.
	// This field may be nil if not requested or if there was an error.
	DocumentAIResponse interface{} `json:"-"`
}