// Package ocr provides OCR (Optical Character Recognition) capabilities using Google Cloud Vision API.
//
// This package supports processing PDF documents and extracting text with metadata.
// It uses Google Cloud Vision API's document text detection features to handle multi-page PDFs.
//
// Required Environment Variables:
//   - GOOGLE_APPLICATION_CREDENTIALS: Path to service account JSON file, OR
//   - GOOGLE_CREDENTIALS: Inline JSON credentials string
//   - GOOGLE_CLOUD_PROJECT: Google Cloud project ID
//
// Cloud Vision API Limitations:
//   - Maximum file size: 20MB for synchronous processing
//   - Maximum pages: 5 pages for synchronous processing
//   - For larger documents, consider using asynchronous processing with Cloud Storage
//   - Supported formats: PDF, TIFF
//
// Implementation Details:
//   - Uses synchronous document text detection for PDFs up to 5 pages
//   - Processes PDFs as base64-encoded inline data (no GCS upload required)
//   - Aggregates text from all pages in reading order
//   - Calculates average confidence scores across all detected text
package ocr

import (
	"context"
	"io"
	"time"
)

// OCRService defines the interface for OCR text extraction services.
type OCRService interface {
	// ProcessPDF extracts text from a PDF document.
	// Returns the concatenated text from all pages.
	ProcessPDF(ctx context.Context, pdfData io.Reader) (string, error)

	// ProcessPDFWithMetadata extracts text from a PDF document with additional metadata.
	// Returns detailed results including confidence scores and processing information.
	ProcessPDFWithMetadata(ctx context.Context, pdfData io.Reader) (*OCRResult, error)
}

// OCRResult contains the results of OCR processing with metadata.
type OCRResult struct {
	// Text is the extracted text content from all pages, concatenated in reading order.
	Text string `json:"text"`

	// PageCount is the number of pages that were processed.
	PageCount int `json:"page_count"`

	// Confidence is the average confidence score across all detected text (0.0 to 1.0).
	// Higher values indicate more reliable text detection.
	Confidence float32 `json:"confidence"`

	// ProcessedAt is the timestamp when the OCR processing completed.
	ProcessedAt time.Time `json:"processed_at"`

	// LanguageCodes contains the detected languages in the document.
	LanguageCodes []string `json:"language_codes,omitempty"`

	// ProcessingDuration is how long the OCR processing took.
	ProcessingDuration time.Duration `json:"processing_duration"`
}