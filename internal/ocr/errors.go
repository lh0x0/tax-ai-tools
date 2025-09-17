package ocr

import (
	"errors"
	"fmt"
)

// Common OCR processing errors
var (
	// ErrPDFTooLarge is returned when the PDF exceeds the maximum file size limit.
	// Google Cloud Vision API has a 20MB limit for synchronous processing.
	ErrPDFTooLarge = errors.New("PDF file size exceeds the maximum limit (20MB)")

	// ErrInvalidPDF is returned when the provided data is not a valid PDF document.
	ErrInvalidPDF = errors.New("invalid or corrupted PDF document")

	// ErrOCRFailed is returned when the Google Cloud Vision API fails to process the document.
	ErrOCRFailed = errors.New("OCR processing failed")

	// ErrMissingCredentials is returned when neither GOOGLE_APPLICATION_CREDENTIALS 
	// nor GOOGLE_CREDENTIALS environment variables are configured.
	ErrMissingCredentials = errors.New("missing Google Cloud credentials: set GOOGLE_APPLICATION_CREDENTIALS or GOOGLE_CREDENTIALS environment variable")

	// ErrTooManyPages is returned when the PDF has too many pages for synchronous processing.
	// Google Cloud Vision API supports up to 5 pages for synchronous processing.
	ErrTooManyPages = errors.New("PDF has too many pages (maximum 5 pages for synchronous processing)")

	// ErrEmptyDocument is returned when the PDF contains no readable text.
	ErrEmptyDocument = errors.New("document contains no readable text")

	// ErrContextCanceled is returned when the context is canceled during processing.
	ErrContextCanceled = errors.New("OCR processing was canceled")
)

// OCRError wraps errors with additional context about the OCR processing failure.
type OCRError struct {
	// Op is the operation that failed (e.g., "ProcessPDF", "LoadCredentials").
	Op string

	// Err is the underlying error.
	Err error

	// Details provides additional context about the failure.
	Details string
}

// Error implements the error interface.
func (e *OCRError) Error() string {
	if e.Details != "" {
		return fmt.Sprintf("ocr: %s failed: %s: %v", e.Op, e.Details, e.Err)
	}
	return fmt.Sprintf("ocr: %s failed: %v", e.Op, e.Err)
}

// Unwrap returns the underlying error for error unwrapping.
func (e *OCRError) Unwrap() error {
	return e.Err
}

// Is implements error matching for Go 1.13+ error handling.
func (e *OCRError) Is(target error) bool {
	return errors.Is(e.Err, target)
}

// NewOCRError creates a new OCRError with the specified operation and underlying error.
func NewOCRError(op string, err error, details string) *OCRError {
	return &OCRError{
		Op:      op,
		Err:     err,
		Details: details,
	}
}

// WrapOCRError wraps an error as an OCRError if it isn't already one.
func WrapOCRError(op string, err error, details string) error {
	if err == nil {
		return nil
	}

	var ocrErr *OCRError
	if errors.As(err, &ocrErr) {
		return err // Already wrapped
	}

	return NewOCRError(op, err, details)
}