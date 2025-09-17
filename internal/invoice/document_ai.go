package invoice

import (
	"context"
	"fmt"
	"io"
	"os"
	"regexp"
	"strconv"
	"strings"
	"time"

	documentai "cloud.google.com/go/documentai/apiv1"
	"cloud.google.com/go/documentai/apiv1/documentaipb"
	"google.golang.org/api/option"
	"github.com/rs/zerolog"

	"tools/internal/logger"
	"tools/pkg/models"
)

const (
	// MaxDocumentSizeBytes is the maximum document size for processing (20MB)
	MaxDocumentSizeBytes = 20 * 1024 * 1024

	// DefaultProcessorType is the default Document AI processor type for invoices
	DefaultProcessorType = "INVOICE_PROCESSOR"
)

// DocumentAIInvoiceProcessor implements InvoiceProcessor using Google Document AI.
type DocumentAIInvoiceProcessor struct {
	client *documentai.DocumentProcessorClient
	config DocumentAIConfig
	log    zerolog.Logger
}

// NewDocumentAIInvoiceProcessor creates processor with credentials from environment.
// Expects: GOOGLE_APPLICATION_CREDENTIALS or GOOGLE_CREDENTIALS
// Requires: GOOGLE_PROJECT_ID, GOOGLE_LOCATION (e.g., "us" or "eu")
// Optional: GOOGLE_PROCESSOR_ID (or use default invoice processor)
func NewDocumentAIInvoiceProcessor(ctx context.Context) (InvoiceProcessor, error) {
	const op = "NewDocumentAIInvoiceProcessor"

	// Load configuration from environment
	config := DocumentAIConfig{
		ProjectID:   getEnvVar("GOOGLE_PROJECT_ID", "GOOGLE_CLOUD_PROJECT"),
		Location:    getEnvVar("GOOGLE_LOCATION", "GOOGLE_CLOUD_LOCATION"),
		ProcessorID: getEnvVar("GOOGLE_PROCESSOR_ID", "DOCUMENT_AI_PROCESSOR_ID"),
		Timeout:     60 * time.Second,
	}

	// Validate required configuration
	if config.ProjectID == "" {
		return nil, WrapInvoiceProcessingError(op, ErrInvalidConfiguration, "GOOGLE_PROJECT_ID or GOOGLE_CLOUD_PROJECT is required")
	}
	if config.Location == "" {
		config.Location = "us" // Default location
	}

	// Create Document AI client with regional endpoint
	var clientOptions []option.ClientOption

	// Set regional endpoint if not us-central1
	if config.Location != "" && config.Location != "us" {
		endpoint := fmt.Sprintf("%s-documentai.googleapis.com:443", config.Location)
		clientOptions = append(clientOptions, option.WithEndpoint(endpoint))
	}

	// Add credentials
	if credJSON := os.Getenv("GOOGLE_CREDENTIALS"); credJSON != "" {
		clientOptions = append(clientOptions, option.WithCredentialsJSON([]byte(credJSON)))
	} else if credFile := os.Getenv("GOOGLE_APPLICATION_CREDENTIALS"); credFile != "" {
		clientOptions = append(clientOptions, option.WithCredentialsFile(credFile))
	}

	// Create client with options
	client, err := documentai.NewDocumentProcessorClient(ctx, clientOptions...)
	if err != nil {
		if len(clientOptions) == 0 {
			return nil, WrapInvoiceProcessingError(op, ErrMissingCredentials, "no credentials found in environment")
		}
		return nil, WrapInvoiceProcessingError(op, err, fmt.Sprintf("failed to create Document AI client for location: %s", config.Location))
	}

	return &DocumentAIInvoiceProcessor{
		client: client,
		config: config,
		log:    logger.WithComponent("document-ai"),
	}, nil
}

// NewDocumentAIInvoiceProcessorWithConfig creates processor with explicit config and client (for testing).
func NewDocumentAIInvoiceProcessorWithConfig(config DocumentAIConfig, client *documentai.DocumentProcessorClient) InvoiceProcessor {
	return &DocumentAIInvoiceProcessor{
		client: client,
		config: config,
		log:    logger.WithComponent("document-ai"),
	}
}

// ProcessInvoice extracts structured data from an invoice PDF.
func (p *DocumentAIInvoiceProcessor) ProcessInvoice(ctx context.Context, pdfData io.Reader) (*models.Invoice, error) {
	invoice, _, err := p.ProcessInvoiceWithConfidence(ctx, pdfData)
	return invoice, err
}

// ProcessInvoiceWithConfidence extracts structured data with confidence scores.
func (p *DocumentAIInvoiceProcessor) ProcessInvoiceWithConfidence(ctx context.Context, pdfData io.Reader) (*models.Invoice, map[string]float32, error) {
	const op = "ProcessInvoiceWithConfidence"

	// Read PDF data
	pdfBytes, err := io.ReadAll(pdfData)
	if err != nil {
		return nil, nil, WrapInvoiceProcessingError(op, err, "failed to read PDF data")
	}

	// Validate file size
	if len(pdfBytes) > MaxDocumentSizeBytes {
		return nil, nil, WrapInvoiceProcessingError(op, ErrDocumentTooLarge, fmt.Sprintf("file size: %d bytes", len(pdfBytes)))
	}

	// Validate PDF header
	if len(pdfBytes) < 4 || string(pdfBytes[:4]) != "%PDF" {
		return nil, nil, WrapInvoiceProcessingError(op, ErrInvalidPDF, "missing PDF header")
	}

	// Create context with timeout
	processCtx, cancel := context.WithTimeout(ctx, p.config.Timeout)
	defer cancel()

	// Get processor name
	processorName := p.getProcessorName()

	// Prepare the request
	req := &documentaipb.ProcessRequest{
		Name: processorName,
		Source: &documentaipb.ProcessRequest_RawDocument{
			RawDocument: &documentaipb.RawDocument{
				Content:  pdfBytes,
				MimeType: "application/pdf",
			},
		},
	}

	// Process document
	resp, err := p.client.ProcessDocument(processCtx, req)
	if err != nil {
		return nil, nil, p.handleProcessingError(op, err)
	}

	// Check for processing errors
	if resp.Document == nil {
		return nil, nil, WrapInvoiceProcessingError(op, ErrProcessingFailed, "no document in response")
	}

	// Extract invoice data
	invoice, confidence, err := p.extractInvoiceData(resp.Document)
	if err != nil {
		return nil, nil, WrapInvoiceProcessingError(op, err, "failed to extract invoice data")
	}

	// Set processing metadata
	invoice.CreatedAt = time.Now()
	invoice.UpdatedAt = invoice.CreatedAt

	return invoice, confidence, nil
}

// getProcessorName constructs the full processor name for Document AI API.
func (p *DocumentAIInvoiceProcessor) getProcessorName() string {
	if p.config.ProcessorID != "" {
		if p.config.ProcessorVersion != "" {
			return fmt.Sprintf("projects/%s/locations/%s/processors/%s/processorVersions/%s",
				p.config.ProjectID, p.config.Location, p.config.ProcessorID, p.config.ProcessorVersion)
		}
		return fmt.Sprintf("projects/%s/locations/%s/processors/%s",
			p.config.ProjectID, p.config.Location, p.config.ProcessorID)
	}
	// If no processor ID specified, use the default format
	// This will need to be updated based on actual processor discovery
	return fmt.Sprintf("projects/%s/locations/%s/processors/%s",
		p.config.ProjectID, p.config.Location, "default-invoice-processor")
}

// handleProcessingError converts Document AI errors to appropriate invoice processing errors.
func (p *DocumentAIInvoiceProcessor) handleProcessingError(op string, err error) error {
	errStr := err.Error()

	switch {
	case strings.Contains(errStr, "PERMISSION_DENIED"):
		return WrapInvoiceProcessingError(op, ErrInvalidCredentials, "insufficient permissions for Document AI")
	case strings.Contains(errStr, "QUOTA_EXCEEDED"):
		return WrapInvoiceProcessingError(op, ErrQuotaExceeded, "Document AI API quota exceeded")
	case strings.Contains(errStr, "NOT_FOUND"):
		return WrapInvoiceProcessingError(op, ErrProcessorNotFound, fmt.Sprintf("processor not found: %s", p.config.ProcessorID))
	case strings.Contains(errStr, "INVALID_ARGUMENT"):
		return WrapInvoiceProcessingError(op, ErrInvalidPDF, "document format not supported or corrupted")
	case strings.Contains(errStr, "DeadlineExceeded") || strings.Contains(errStr, "context deadline exceeded"):
		return WrapInvoiceProcessingError(op, context.DeadlineExceeded, "processing timeout")
	case strings.Contains(errStr, "Canceled") || strings.Contains(errStr, "context canceled"):
		return WrapInvoiceProcessingError(op, ErrContextCanceled, "processing was canceled")
	default:
		return WrapInvoiceProcessingError(op, ErrProcessingFailed, fmt.Sprintf("Document AI error: %v", err))
	}
}

// extractInvoiceData converts Document AI entities to Invoice model.
func (p *DocumentAIInvoiceProcessor) extractInvoiceData(doc *documentaipb.Document) (*models.Invoice, map[string]float32, error) {
	invoice := &models.Invoice{
		Type:      "",    // Default to payable (incoming invoice)
		Currency:  "EUR", // Default currency
		CreatedAt: time.Now(),
		UpdatedAt: time.Now(),
	}

	confidence := make(map[string]float32)

	// Extract entities
	for _, entity := range doc.Entities {
		entityType := entity.Type
		value := strings.TrimSpace(entity.MentionText)
		conf := entity.Confidence

		confidence[entityType] = conf

		p.log.Debug().
			Str("entity_type", entityType).
			Str("value", value).
			Float32("confidence", conf).
			Msg("Processing Document AI entity")

		switch entityType {
		case "invoice_id", "invoice_number":
			invoice.InvoiceNumber = value
		case "supplier_name", "vendor_name":
			invoice.Vendor = value
		case "buyer_name", "customer_name":
			invoice.Customer = value
		case "invoice_date":
			if date, err := p.extractDate(entity); err == nil {
				invoice.IssueDate = date
			}
		case "due_date":
			if date, err := p.extractDate(entity); err == nil {
				invoice.DueDate = date
			}
		case "net_amount", "subtotal_amount":
			if amount, err := p.extractMoneyValue(entity); err == nil {
				p.log.Debug().
					Int64("amount", amount).
					Str("raw_value", value).
					Msg("Extracted net amount from Document AI")
				invoice.NetAmount = amount
			} else {
				p.log.Warn().
					Err(err).
					Str("raw_value", value).
					Msg("Failed to extract net amount from Document AI")
			}
		case "total_tax_amount", "vat_amount":
			if amount, err := p.extractMoneyValue(entity); err == nil {
				p.log.Debug().
					Int64("amount", amount).
					Str("raw_value", value).
					Msg("Extracted VAT amount from Document AI")
				invoice.VATAmount = amount
			} else {
				p.log.Warn().
					Err(err).
					Str("raw_value", value).
					Msg("Failed to extract VAT amount from Document AI")
			}
		case "total_amount", "gross_amount":
			if amount, err := p.extractMoneyValue(entity); err == nil {
				p.log.Debug().
					Int64("amount", amount).
					Str("raw_value", value).
					Msg("Extracted gross amount from Document AI")
				invoice.GrossAmount = amount
			} else {
				p.log.Warn().
					Err(err).
					Str("raw_value", value).
					Msg("Failed to extract gross amount from Document AI")
			}
		case "currency":
			if value != "" {
				invoice.Currency = p.normalizeCurrency(value)
			}
		case "purchase_order", "reference_number":
			invoice.Reference = value
		}
	}

	// Apply invoice number fallback strategies if no number was extracted
	if invoice.InvoiceNumber == "" {
		if fallbackNumber := p.extractInvoiceNumberFallback(doc); fallbackNumber != "" {
			invoice.InvoiceNumber = fallbackNumber
			confidence["invoice_number_fallback"] = 0.6 // Lower confidence for fallback
			p.log.Info().
				Str("fallback_number", fallbackNumber).
				Msg("Invoice number extracted using fallback strategy")
		}
	}

	// Generate ID if not present
	if invoice.ID == "" {
		invoice.ID = p.generateInvoiceID(invoice)
	}

	// Calculate missing amounts if possible
	p.calculateMissingAmounts(invoice)

	// Log final extracted amounts
	p.log.Info().
		Str("invoice_number", invoice.InvoiceNumber).
		Int64("net_amount", invoice.NetAmount).
		Int64("vat_amount", invoice.VATAmount).
		Int64("gross_amount", invoice.GrossAmount).
		Str("currency", invoice.Currency).
		Msg("Document AI extraction completed")

	// Validate critical fields
	if err := p.validateInvoice(invoice); err != nil {
		return nil, nil, err
	}

	return invoice, confidence, nil
}

// extractDate safely extracts date value from Document AI entity.
func (p *DocumentAIInvoiceProcessor) extractDate(entity *documentaipb.Document_Entity) (time.Time, error) {
	if entity.NormalizedValue != nil {
		if dateValue := entity.NormalizedValue.GetDateValue(); dateValue != nil {
			return time.Date(
				int(dateValue.Year),
				time.Month(dateValue.Month),
				int(dateValue.Day),
				0, 0, 0, 0,
				time.UTC,
			), nil
		}
	}

	// Fallback to parsing mention text
	dateStr := strings.TrimSpace(entity.MentionText)
	if dateStr == "" {
		return time.Time{}, fmt.Errorf("empty date value")
	}

	// Try common date formats
	formats := []string{
		"2006-01-02",
		"02.01.2006",
		"01/02/2006",
		"2006/01/02",
		"02-01-2006",
		"January 2, 2006",
		"Jan 2, 2006",
		"2 January 2006",
		"2 Jan 2006",
	}

	for _, format := range formats {
		if date, err := time.Parse(format, dateStr); err == nil {
			return date, nil
		}
	}

	return time.Time{}, fmt.Errorf("unable to parse date: %s", dateStr)
}

// extractMoneyValue safely extracts and converts monetary value from Document AI entity to cents.
func (p *DocumentAIInvoiceProcessor) extractMoneyValue(entity *documentaipb.Document_Entity) (int64, error) {
	if entity.NormalizedValue != nil {
		if moneyValue := entity.NormalizedValue.GetMoneyValue(); moneyValue != nil {
			// Convert to cents
			units := moneyValue.Units
			nanos := moneyValue.Nanos
			return units*100 + int64(nanos)/10000000, nil
		}
	}

	// Fallback to parsing mention text
	amountStr := strings.TrimSpace(entity.MentionText)
	if amountStr == "" {
		return 0, fmt.Errorf("empty amount value")
	}

	// Use the same robust German number parsing as Invoice Completion
	amount, err := p.parseAmount(amountStr)
	if err != nil {
		return 0, fmt.Errorf("unable to parse amount: %s", entity.MentionText)
	}

	return amount, nil
}

// parseAmount parses amount string handling both German and English formats
func (p *DocumentAIInvoiceProcessor) parseAmount(amountStr string) (int64, error) {
	// Clean the amount string
	cleaned := strings.TrimSpace(amountStr)
	cleaned = strings.ReplaceAll(cleaned, " ", "")
	cleaned = strings.ReplaceAll(cleaned, "€", "")
	cleaned = strings.ReplaceAll(cleaned, "$", "")
	cleaned = strings.ReplaceAll(cleaned, "EUR", "")
	cleaned = strings.ReplaceAll(cleaned, "USD", "")
	
	// Handle German number format (7.303,08 -> 7303.08)
	if strings.Contains(cleaned, ",") {
		// If there's both . and , assume German format (. = thousands, , = decimal)
		if strings.Contains(cleaned, ".") && strings.Contains(cleaned, ",") {
			// Remove thousands separators (dots)
			cleaned = strings.ReplaceAll(cleaned, ".", "")
			// Replace decimal separator (comma) with dot
			cleaned = strings.ReplaceAll(cleaned, ",", ".")
		} else if strings.Contains(cleaned, ",") {
			// Only comma, could be decimal separator
			// Count digits after comma to determine if it's decimal
			parts := strings.Split(cleaned, ",")
			if len(parts) == 2 && len(parts[1]) <= 2 {
				// Likely decimal separator (e.g., "1234,50")
				cleaned = strings.ReplaceAll(cleaned, ",", ".")
			}
		}
	}

	amount, err := strconv.ParseFloat(cleaned, 64)
	if err != nil {
		return 0, fmt.Errorf("unable to parse amount: %s (cleaned: %s)", amountStr, cleaned)
	}

	return int64(amount * 100), nil
}

// generateInvoiceID generates a unique invoice ID if not present.
func (p *DocumentAIInvoiceProcessor) generateInvoiceID(invoice *models.Invoice) string {
	if invoice.InvoiceNumber != "" {
		return invoice.InvoiceNumber
	}
	// Generate based on vendor and timestamp
	timestamp := time.Now().Format("20060102-150405")
	if invoice.Vendor != "" {
		vendorPrefix := strings.ToUpper(strings.ReplaceAll(invoice.Vendor, " ", ""))
		if len(vendorPrefix) > 8 {
			vendorPrefix = vendorPrefix[:8]
		}
		return fmt.Sprintf("%s-%s", vendorPrefix, timestamp)
	}
	return fmt.Sprintf("INV-%s", timestamp)
}

// calculateMissingAmounts calculates missing amount fields if possible.
func (p *DocumentAIInvoiceProcessor) calculateMissingAmounts(invoice *models.Invoice) {
	// If we have net and VAT, calculate gross
	if invoice.NetAmount > 0 && invoice.VATAmount > 0 && invoice.GrossAmount == 0 {
		invoice.GrossAmount = invoice.NetAmount + invoice.VATAmount
	}
	// If we have gross and VAT, calculate net
	if invoice.GrossAmount > 0 && invoice.VATAmount > 0 && invoice.NetAmount == 0 {
		invoice.NetAmount = invoice.GrossAmount - invoice.VATAmount
	}
	// If we have gross and net, calculate VAT
	if invoice.GrossAmount > 0 && invoice.NetAmount > 0 && invoice.VATAmount == 0 {
		invoice.VATAmount = invoice.GrossAmount - invoice.NetAmount
	}
}

// validateInvoice performs basic validation on extracted invoice data.
func (p *DocumentAIInvoiceProcessor) validateInvoice(invoice *models.Invoice) error {
	if invoice.InvoiceNumber == "" && invoice.ID == "" {
		return NewValidationError("invoice_number", "", "invoice number is required")
	}
	// Allow zero amounts for credit notes, refunds, or corrective invoices
	if invoice.GrossAmount < 0 && invoice.NetAmount < 0 {
		return NewValidationError("amount", invoice.GrossAmount, "invoice amount cannot be negative")
	}
	return nil
}

// getEnvVar tries multiple environment variable names and returns the first non-empty value
func getEnvVar(names ...string) string {
	for _, name := range names {
		if value := os.Getenv(name); value != "" {
			return value
		}
	}
	return ""
}

// Close closes the underlying Document AI client.
func (p *DocumentAIInvoiceProcessor) Close() error {
	if p.client != nil {
		return p.client.Close()
	}
	return nil
}

// normalizeCurrency standardizes currency codes to consistent format
func (p *DocumentAIInvoiceProcessor) normalizeCurrency(currency string) string {
	if currency == "" {
		return "EUR" // Default to EUR for German invoices
	}
	
	// Convert to uppercase and trim
	normalized := strings.ToUpper(strings.TrimSpace(currency))
	
	// Common currency mappings to standard ISO codes
	switch normalized {
	case "€", "EURO", "EUROS", "EUR":
		return "EUR"
	case "$", "DOLLAR", "DOLLARS", "USD", "US$":
		return "USD" 
	case "£", "POUND", "POUNDS", "GBP":
		return "GBP"
	case "¥", "YEN", "JPY":
		return "JPY"
	case "CHF", "FRANKEN", "SWISS FRANC":
		return "CHF"
	default:
		// If it's already a 3-letter code, return as-is
		if len(normalized) == 3 {
			return normalized
		}
		// Otherwise default to EUR
		return "EUR"
	}
}

// extractInvoiceNumberFallback implements fallback strategies for invoice number extraction
func (p *DocumentAIInvoiceProcessor) extractInvoiceNumberFallback(doc *documentaipb.Document) string {
	// Strategy 1: Search in line item descriptions for HORNBACH patterns
	for _, entity := range doc.Entities {
		if entity.Type == "line_item" || entity.Type == "line_item/description" {
			text := strings.TrimSpace(entity.MentionText)
			if invoiceNum := p.extractInvoiceNumberFromText(text); invoiceNum != "" {
				p.log.Debug().
					Str("source", "line_item").
					Str("text", text).
					Str("number", invoiceNum).
					Msg("Found invoice number in line item")
				return invoiceNum
			}
		}
	}
	
	// Strategy 2: Search in all OCR text for known patterns
	if doc.Text != "" {
		if invoiceNum := p.extractInvoiceNumberFromText(doc.Text); invoiceNum != "" {
			p.log.Debug().
				Str("source", "full_text").
				Str("number", invoiceNum).
				Msg("Found invoice number in full OCR text")
			return invoiceNum
		}
	}
	
	// Strategy 3: Search in entity properties and sub-entities
	for _, entity := range doc.Entities {
		// Check if entity has properties that might contain invoice numbers
		if entity.Properties != nil {
			for _, prop := range entity.Properties {
				text := strings.TrimSpace(prop.MentionText)
				if invoiceNum := p.extractInvoiceNumberFromText(text); invoiceNum != "" {
					p.log.Debug().
						Str("source", "entity_property").
						Str("property_type", prop.Type).
						Str("text", text).
						Str("number", invoiceNum).
						Msg("Found invoice number in entity property")
					return invoiceNum
				}
			}
		}
	}
	
	return ""
}

// extractInvoiceNumberFromText searches for invoice number patterns in text
func (p *DocumentAIInvoiceProcessor) extractInvoiceNumberFromText(text string) string {
	
	// Common German invoice number patterns
	patterns := []string{
		// HORNBACH specific patterns
		`(?i)(?:rechnung|belegnr|beleg)[\s\-:\.]*(\d{8,}|\d{4,}\-\d+|\d+\.\d+)`,
		`(?i)(?:rechnungsnr|rg\.?nr|rg\.?)[\s\-:\.]*(\d{8,}|\d{4,}\-\d+|\d+\.\d+)`,
		`(?i)(?:invoice|inv)[\s\-:\.]*(?:no|nr|number)[\s\-:\.]*(\d{8,}|\d{4,}\-\d+|\d+\.\d+)`,
		
		// Generic patterns
		`(?i)(?:^|\s)(?:nr|no|number)[\s\-:\.]*(\d{6,})`,
		`(?i)(?:dokument|document)[\s\-:\.]*(?:nr|no)[\s\-:\.]*(\d{6,})`,
		`(?i)(?:^|\s)(\d{8,})(?:\s|$)`, // Standalone 8+ digit numbers
		
		// Date-based invoice numbers (common in Germany)
		`(?i)(\d{4,}\-\d{4,}\-\d+)`, // Format: YYYY-MMMM-XXX
		`(?i)(\d{6,}\.\d+)`,          // Format: YYYYMM.XXX
	}
	
	for _, pattern := range patterns {
		re := regexp.MustCompile(pattern)
		if matches := re.FindStringSubmatch(text); len(matches) > 1 {
			candidate := strings.TrimSpace(matches[1])
			// Validate candidate (basic sanity checks)
			if len(candidate) >= 6 && len(candidate) <= 20 {
				p.log.Debug().
					Str("pattern", pattern).
					Str("candidate", candidate).
					Str("source_text", text[:min(50, len(text))]).
					Msg("Invoice number pattern matched")
				return candidate
			}
		}
	}
	
	return ""
}

// min returns the minimum of two integers
func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}
