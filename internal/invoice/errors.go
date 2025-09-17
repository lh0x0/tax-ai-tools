package invoice

import (
	"errors"
	"fmt"
)

// Common invoice processing errors
var (
	// ErrInvalidPDF is returned when the provided data is not a valid PDF document
	// or cannot be processed by Document AI.
	ErrInvalidPDF = errors.New("invalid or corrupted PDF document")

	// ErrProcessingFailed is returned when Document AI processing fails.
	ErrProcessingFailed = errors.New("document AI processing failed")

	// ErrMissingRequiredField is returned when critical invoice fields are missing
	// or cannot be extracted from the document.
	ErrMissingRequiredField = errors.New("missing required invoice field")

	// ErrInvalidCredentials is returned when Google Cloud credentials are invalid
	// or do not have the necessary permissions.
	ErrInvalidCredentials = errors.New("invalid Google Cloud credentials")

	// ErrMissingCredentials is returned when Google Cloud credentials are not configured.
	ErrMissingCredentials = errors.New("missing Google Cloud credentials")

	// ErrInvalidConfiguration is returned when the Document AI configuration is invalid.
	ErrInvalidConfiguration = errors.New("invalid Document AI configuration")

	// ErrProcessorNotFound is returned when the specified Document AI processor
	// cannot be found or accessed.
	ErrProcessorNotFound = errors.New("Document AI processor not found")

	// ErrQuotaExceeded is returned when Document AI API quota limits are exceeded.
	ErrQuotaExceeded = errors.New("Document AI API quota exceeded")

	// ErrDocumentTooLarge is returned when the PDF exceeds size limits.
	ErrDocumentTooLarge = errors.New("document exceeds maximum size limit")

	// ErrUnsupportedFormat is returned when the document format is not supported.
	ErrUnsupportedFormat = errors.New("unsupported document format")

	// ErrContextCanceled is returned when processing is canceled via context.
	ErrContextCanceled = errors.New("invoice processing was canceled")
)

// InvoiceProcessingError wraps errors with additional context about invoice processing failures.
type InvoiceProcessingError struct {
	// Op is the operation that failed (e.g., "ProcessInvoice", "ExtractEntities").
	Op string

	// Err is the underlying error.
	Err error

	// Details provides additional context about the failure.
	Details string

	// ProcessorID is the Document AI processor ID used (if available).
	ProcessorID string

	// DocumentSize is the size of the processed document in bytes (if available).
	DocumentSize int64
}

// Error implements the error interface.
func (e *InvoiceProcessingError) Error() string {
	if e.Details != "" {
		return fmt.Sprintf("invoice: %s failed: %s: %v", e.Op, e.Details, e.Err)
	}
	if e.ProcessorID != "" {
		return fmt.Sprintf("invoice: %s failed (processor: %s): %v", e.Op, e.ProcessorID, e.Err)
	}
	return fmt.Sprintf("invoice: %s failed: %v", e.Op, e.Err)
}

// Unwrap returns the underlying error for error unwrapping.
func (e *InvoiceProcessingError) Unwrap() error {
	return e.Err
}

// Is implements error matching for Go 1.13+ error handling.
func (e *InvoiceProcessingError) Is(target error) bool {
	return errors.Is(e.Err, target)
}

// NewInvoiceProcessingError creates a new InvoiceProcessingError with the specified operation and underlying error.
func NewInvoiceProcessingError(op string, err error, details string) *InvoiceProcessingError {
	return &InvoiceProcessingError{
		Op:      op,
		Err:     err,
		Details: details,
	}
}

// WrapInvoiceProcessingError wraps an error as an InvoiceProcessingError if it isn't already one.
func WrapInvoiceProcessingError(op string, err error, details string) error {
	if err == nil {
		return nil
	}

	var invoiceErr *InvoiceProcessingError
	if errors.As(err, &invoiceErr) {
		return err // Already wrapped
	}

	return NewInvoiceProcessingError(op, err, details)
}

// ValidationError represents errors in invoice data validation.
type ValidationError struct {
	Field   string
	Value   interface{}
	Message string
}

// Error implements the error interface.
func (e *ValidationError) Error() string {
	return fmt.Sprintf("validation error for field '%s': %s (value: %v)", e.Field, e.Message, e.Value)
}

// NewValidationError creates a new ValidationError.
func NewValidationError(field string, value interface{}, message string) *ValidationError {
	return &ValidationError{
		Field:   field,
		Value:   value,
		Message: message,
	}
}

// EntityExtractionError represents errors during entity extraction from Document AI response.
type EntityExtractionError struct {
	EntityType string
	Reason     string
}

// Error implements the error interface.
func (e *EntityExtractionError) Error() string {
	return fmt.Sprintf("failed to extract entity '%s': %s", e.EntityType, e.Reason)
}

// NewEntityExtractionError creates a new EntityExtractionError.
func NewEntityExtractionError(entityType, reason string) *EntityExtractionError {
	return &EntityExtractionError{
		EntityType: entityType,
		Reason:     reason,
	}
}