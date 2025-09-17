package invoice

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/rs/zerolog"
	"github.com/sashabaranov/go-openai"
	"tools/internal/logger"
	"tools/internal/ocr"
	"tools/pkg/models"
)

// InvoiceCompletionService validates and completes invoice data using OCR and ChatGPT
type InvoiceCompletionService interface {
	// CompleteInvoice fills missing fields using OCR and ChatGPT
	CompleteInvoice(ctx context.Context, invoice *models.Invoice, pdfData io.Reader) (*models.Invoice, error)

	// ValidateInvoice checks if all required fields are present
	ValidateInvoice(invoice *models.Invoice) (bool, []string)

	// CompleteInvoiceWithConfidence returns completed invoice with confidence scores
	CompleteInvoiceWithConfidence(ctx context.Context, invoice *models.Invoice, pdfData io.Reader) (*models.Invoice, map[string]float32, error)
}

// CompletionConfig configures the invoice completion service
type CompletionConfig struct {
	CompanyName       string    // Our company name for context
	CompanyAliases    []string  // Alternative names/DBAs
	RequireAllFields  bool      // Fail if can't complete all fields
	MaxRetries        int       // ChatGPT retry attempts
	OpenAIModel       string    // gpt-4, gpt-3.5-turbo
	Temperature       float32   // ChatGPT temperature
	OCRConfidenceMin  float32   // Minimum OCR confidence
}

// DefaultInvoiceCompletionService implements InvoiceCompletionService
type DefaultInvoiceCompletionService struct {
	ocrService   ocr.OCRService
	openaiClient *openai.Client
	config       CompletionConfig
	log          zerolog.Logger
}

// ChatGPTResponse represents the structured response from ChatGPT
type ChatGPTResponse struct {
	Type              string `json:"type"`
	TypeConfidence    string `json:"type_confidence"` // Accept as string, convert later
	TypeReasoning     string `json:"type_reasoning"`
	AccountingSummary string `json:"accounting_summary,omitempty"` // German accounting summary
	Vendor            string `json:"vendor,omitempty"`
	Customer          string `json:"customer,omitempty"`
	InvoiceNumber     string `json:"invoice_number,omitempty"`
	IssueDate         string `json:"issue_date,omitempty"`
	DueDate           string `json:"due_date,omitempty"`
	NetAmount         string `json:"net_amount,omitempty"`
	VATAmount         string `json:"vat_amount,omitempty"`
	GrossAmount       string `json:"gross_amount,omitempty"`
	Currency          string `json:"currency,omitempty"`
	Reference         string `json:"reference,omitempty"`
	Description       string `json:"description,omitempty"`
}

// NewInvoiceCompletionService creates service with dependencies from environment
func NewInvoiceCompletionService(ctx context.Context) (InvoiceCompletionService, error) {
	const op = "NewInvoiceCompletionService"

	// Create OCR service
	ocrService, err := ocr.NewGoogleVisionOCRService(ctx)
	if err != nil {
		return nil, fmt.Errorf("%s: failed to create OCR service: %w", op, err)
	}

	// Get OpenAI API key
	apiKey := os.Getenv("OPENAI_API_KEY")
	if apiKey == "" {
		return nil, fmt.Errorf("%s: OPENAI_API_KEY environment variable is required", op)
	}

	// Create OpenAI client
	openaiClient := openai.NewClient(apiKey)

	// Load configuration from environment
	openaiModel := os.Getenv("OPENAI_MODEL")
	if openaiModel == "" {
		openaiModel = "gpt-3.5-turbo"
	}
	
	companyName := os.Getenv("COMPANY_NAME")
	if companyName == "" {
		companyName = "YOUR_COMPANY"
	}

	config := CompletionConfig{
		CompanyName:      companyName,
		RequireAllFields: os.Getenv("REQUIRE_ALL_FIELDS") == "true",
		MaxRetries:       parseIntEnv("COMPLETION_MAX_RETRIES", 3),
		OpenAIModel:      openaiModel,
		Temperature:      parseFloatEnv("OPENAI_TEMPERATURE", 0.1),
		OCRConfidenceMin: parseFloatEnv("OCR_CONFIDENCE_MIN", 0.0),
	}


	// Parse company aliases
	if aliases := os.Getenv("COMPANY_ALIASES"); aliases != "" {
		config.CompanyAliases = strings.Split(aliases, ",")
		for i := range config.CompanyAliases {
			config.CompanyAliases[i] = strings.TrimSpace(config.CompanyAliases[i])
		}
	}

	return NewInvoiceCompletionServiceWithDeps(ocrService, openaiClient, config), nil
}

// NewInvoiceCompletionServiceWithDeps creates service with explicit dependencies
func NewInvoiceCompletionServiceWithDeps(ocrService ocr.OCRService, openaiClient *openai.Client, config CompletionConfig) InvoiceCompletionService {
	return &DefaultInvoiceCompletionService{
		ocrService:   ocrService,
		openaiClient: openaiClient,
		config:       config,
		log:          logger.WithComponent("invoice-completion"),
	}
}

// ValidateInvoice checks if all required fields are present
func (s *DefaultInvoiceCompletionService) ValidateInvoice(invoice *models.Invoice) (bool, []string) {
	var missingFields []string

	// Required fields
	if invoice.InvoiceNumber == "" {
		missingFields = append(missingFields, "invoice_number")
	}
	if invoice.Vendor == "" {
		missingFields = append(missingFields, "vendor")
	}
	if invoice.Type == "" || (invoice.Type != "PAYABLE" && invoice.Type != "RECEIVABLE") {
		missingFields = append(missingFields, "type")
	}
	if invoice.IssueDate.IsZero() {
		missingFields = append(missingFields, "issue_date")
	}
	if invoice.GrossAmount <= 0 {
		missingFields = append(missingFields, "gross_amount")
	}
	if invoice.Currency == "" {
		missingFields = append(missingFields, "currency")
	}

	// Important but optional fields
	if invoice.Customer == "" && !s.config.RequireAllFields {
		// Customer is optional for PAYABLE invoices
	} else if invoice.Customer == "" {
		missingFields = append(missingFields, "customer")
	}

	if invoice.DueDate.IsZero() && !s.config.RequireAllFields {
		// Due date is optional
	} else if invoice.DueDate.IsZero() {
		missingFields = append(missingFields, "due_date")
	}

	if invoice.NetAmount <= 0 && !s.config.RequireAllFields {
		// Net amount can be calculated
	} else if invoice.NetAmount <= 0 {
		missingFields = append(missingFields, "net_amount")
	}

	return len(missingFields) == 0, missingFields
}

// CompleteInvoice fills missing fields using OCR and ChatGPT
func (s *DefaultInvoiceCompletionService) CompleteInvoice(ctx context.Context, invoice *models.Invoice, pdfData io.Reader) (*models.Invoice, error) {
	completed, _, err := s.CompleteInvoiceWithConfidence(ctx, invoice, pdfData)
	return completed, err
}

// CompleteInvoiceWithConfidence returns completed invoice with confidence scores
func (s *DefaultInvoiceCompletionService) CompleteInvoiceWithConfidence(ctx context.Context, invoice *models.Invoice, pdfData io.Reader) (*models.Invoice, map[string]float32, error) {
	const op = "CompleteInvoiceWithConfidence"

	s.log.Info().
		Str("invoice_id", invoice.ID).
		Str("vendor", invoice.Vendor).
		Msg("Starting invoice completion")

	// 1. Check which fields are missing
	isValid, missingFields := s.ValidateInvoice(invoice)
	if isValid {
		s.log.Info().Msg("Invoice is already complete")
		return invoice, make(map[string]float32), nil
	}

	s.log.Info().
		Strs("missing_fields", missingFields).
		Msg("Found missing fields, proceeding with completion")

	// 2. Buffer the PDF data (since we may need to read it multiple times)
	pdfBytes, err := io.ReadAll(pdfData)
	if err != nil {
		return nil, nil, fmt.Errorf("%s: failed to read PDF data: %w", op, err)
	}

	// 3. OCR the PDF to get text
	s.log.Info().Msg("Extracting text from PDF using OCR")
	ocrResult, err := s.ocrService.ProcessPDFWithMetadata(ctx, bytes.NewReader(pdfBytes))
	if err != nil {
		return nil, nil, fmt.Errorf("%s: OCR failed: %w", op, err)
	}

	if ocrResult.Text == "" {
		return nil, nil, fmt.Errorf("%s: no text extracted from PDF", op)
	}

	s.log.Info().
		Int("text_length", len(ocrResult.Text)).
		Float32("avg_confidence", ocrResult.Confidence).
		Msg("OCR extraction completed")

	// Check OCR confidence
	if ocrResult.Confidence < s.config.OCRConfidenceMin {
		s.log.Warn().
			Float32("confidence", ocrResult.Confidence).
			Float32("minimum", s.config.OCRConfidenceMin).
			Msg("OCR confidence below minimum threshold")
	}

	// 4. Use ChatGPT to extract missing information
	chatGPTResponse, err := s.extractInvoiceFromText(ctx, ocrResult.Text, missingFields, invoice)
	if err != nil {
		return nil, nil, fmt.Errorf("%s: ChatGPT extraction failed: %w", op, err)
	}

	// 5. Create completed invoice by merging data
	completedInvoice := *invoice // Copy original
	confidence := make(map[string]float32)

	// Apply ChatGPT results to missing fields
	err = s.mergeCompletionResults(&completedInvoice, chatGPTResponse, missingFields, confidence)
	if err != nil {
		return nil, nil, fmt.Errorf("%s: failed to merge completion results: %w", op, err)
	}

	// 6. Final validation
	if err := s.validateCompletedInvoice(&completedInvoice); err != nil {
		return nil, nil, fmt.Errorf("%s: completed invoice validation failed: %w", op, err)
	}

	s.log.Info().
		Str("type", completedInvoice.Type).
		Str("vendor", completedInvoice.Vendor).
		Str("customer", completedInvoice.Customer).
		Float64("gross_amount", float64(completedInvoice.GrossAmount)/100).
		Str("currency", completedInvoice.Currency).
		Msg("Invoice completion successful")

	return &completedInvoice, confidence, nil
}

// extractInvoiceFromText uses ChatGPT to extract missing invoice information
func (s *DefaultInvoiceCompletionService) extractInvoiceFromText(ctx context.Context, ocrText string, missingFields []string, partialInvoice *models.Invoice) (*ChatGPTResponse, error) {
	const op = "extractInvoiceFromText"

	prompt := s.buildCompletionPrompt(ocrText, missingFields, partialInvoice)

	s.log.Debug().
		Int("prompt_length", len(prompt)).
		Strs("missing_fields", missingFields).
		Str("model", s.config.OpenAIModel).
		Float32("temperature", s.config.Temperature).
		Msg("Sending completion request to ChatGPT")

	var lastErr error
	for attempt := 1; attempt <= s.config.MaxRetries; attempt++ {
		resp, err := s.openaiClient.CreateChatCompletion(ctx, openai.ChatCompletionRequest{
			Model:       s.config.OpenAIModel,
			Temperature: s.config.Temperature,
			Messages: []openai.ChatCompletionMessage{
				{
					Role:    openai.ChatMessageRoleSystem,
					Content: s.getSystemPrompt(),
				},
				{
					Role:    openai.ChatMessageRoleUser,
					Content: prompt,
				},
			},
			MaxTokens: 1000,
		})

		if err != nil {
			lastErr = err
			s.log.Warn().
				Err(err).
				Int("attempt", attempt).
				Int("max_retries", s.config.MaxRetries).
				Msg("ChatGPT request failed, retrying")
			continue
		}

		if len(resp.Choices) == 0 {
			lastErr = fmt.Errorf("no response choices from ChatGPT")
			continue
		}

		content := resp.Choices[0].Message.Content
		s.log.Debug().
			Str("response", content).
			Msg("Received ChatGPT response")

		// Parse JSON response with robust confidence handling
		var rawResponse map[string]interface{}
		if err := json.Unmarshal([]byte(content), &rawResponse); err != nil {
			lastErr = fmt.Errorf("failed to parse ChatGPT JSON response: %w", err)
			s.log.Warn().
				Err(err).
				Str("response", content).
				Int("attempt", attempt).
				Msg("Failed to parse ChatGPT response, retrying")
			continue
		}

		// Convert to our struct with confidence normalization
		chatGPTResponse := ChatGPTResponse{
			Type:              getString(rawResponse, "type"),
			TypeReasoning:     getString(rawResponse, "type_reasoning"),
			AccountingSummary: getString(rawResponse, "accounting_summary"),
			Vendor:            getString(rawResponse, "vendor"),
			Customer:          getString(rawResponse, "customer"),
			InvoiceNumber:     getString(rawResponse, "invoice_number"),
			IssueDate:         getString(rawResponse, "issue_date"),
			DueDate:           getString(rawResponse, "due_date"),
			NetAmount:         getString(rawResponse, "net_amount"),
			VATAmount:         getString(rawResponse, "vat_amount"),
			GrossAmount:       getString(rawResponse, "gross_amount"),
			Currency:          getString(rawResponse, "currency"),
			Reference:         getString(rawResponse, "reference"),
			Description:       getString(rawResponse, "description"),
		}

		// Handle confidence as either string or number
		if conf := rawResponse["type_confidence"]; conf != nil {
			switch v := conf.(type) {
			case string:
				chatGPTResponse.TypeConfidence = v
			case float64:
				chatGPTResponse.TypeConfidence = fmt.Sprintf("%.1f", v)
			case int:
				chatGPTResponse.TypeConfidence = fmt.Sprintf("%d", v)
			default:
				chatGPTResponse.TypeConfidence = "0.5" // fallback
			}
		}

		// Validate type field is present and valid
		if chatGPTResponse.Type == "" || (chatGPTResponse.Type != "PAYABLE" && chatGPTResponse.Type != "RECEIVABLE") {
			lastErr = fmt.Errorf("invalid or missing type in ChatGPT response: %s", chatGPTResponse.Type)
			s.log.Warn().
				Str("type", chatGPTResponse.Type).
				Int("attempt", attempt).
				Msg("Invalid invoice type from ChatGPT, retrying")
			continue
		}

		// Parse confidence for logging
		typeConfidence := float32(0.5)
		if conf, err := strconv.ParseFloat(chatGPTResponse.TypeConfidence, 32); err == nil {
			typeConfidence = float32(conf)
		}
		
		s.log.Info().
			Str("determined_type", chatGPTResponse.Type).
			Float32("type_confidence", typeConfidence).
			Str("reasoning", chatGPTResponse.TypeReasoning).
			Str("accounting_summary", chatGPTResponse.AccountingSummary).
			Int("attempt", attempt).
			Msg("Successfully extracted invoice data from ChatGPT")

		return &chatGPTResponse, nil
	}

	return nil, fmt.Errorf("%s: all %d attempts failed, last error: %w", op, s.config.MaxRetries, lastErr)
}

// getSystemPrompt returns the system prompt for ChatGPT that emphasizes invoice type determination
func (s *DefaultInvoiceCompletionService) getSystemPrompt() string {
	return fmt.Sprintf(`You are analyzing invoices for %s. Your primary task is to determine the invoice type, extract missing information, and create a German accounting summary.

CRITICAL TASK: Determine if this invoice is PAYABLE or RECEIVABLE
- PAYABLE: This is a bill TO our company (we owe money to the vendor)
- RECEIVABLE: This is an invoice FROM our company (customer owes us money)

Look for these indicators:
- If "Bill To" or "Invoice To" matches our company → PAYABLE
- If "From" or "Vendor" matches our company → RECEIVABLE  
- If payment instructions are TO another company → PAYABLE
- If payment should be made TO us → RECEIVABLE
- Check bank account details ownership
- Look for "Remit To" vs "Bill To" sections

ACCOUNTING SUMMARY: Create a German prose summary describing ONLY what goods/services are being billed:
- Focus on WHAT was purchased or what service was provided
- Do NOT mention amounts, dates, or invoice details
- Include a suggested German accounting category (Kontierungsvorschlag)
- Examples:
  * "IT-Equipment bestehend aus 5 Laptops und 10 Monitoren für Arbeitsplatzausstattung, Kontierung: IT-Hardware/Anlagegüter"
  * "Büromaterialbestellung mit Druckerpapier, Toner und Schreibwaren, Kontierung: Bürobedarf"
  * "Monatliche Cloud-Hosting Gebühren für Produktionsserver, Kontierung: IT-Infrastruktur/laufende Kosten"
  * "Beratungsleistungen für SAP-Migration, Kontierung: Externe Dienstleistungen/Projekte"

Company Context:
- Our company: %s
- Aliases: %s

Return ONLY valid JSON with the requested fields. Use null for missing values.
Amounts should be in the original currency format (e.g., "580.00" for 580 euros).
Dates should be in YYYY-MM-DD format.`,
		s.config.CompanyName,
		s.config.CompanyName,
		strings.Join(s.config.CompanyAliases, ", "))
}

// buildCompletionPrompt creates the user prompt for ChatGPT
func (s *DefaultInvoiceCompletionService) buildCompletionPrompt(ocrText string, missingFields []string, partialInvoice *models.Invoice) string {
	var prompt strings.Builder

	prompt.WriteString("Analyze this invoice and extract the missing information:\n\n")

	// Current invoice data for context
	prompt.WriteString("Current invoice data:\n")
	if partialInvoice.Vendor != "" {
		prompt.WriteString(fmt.Sprintf("Vendor: %s\n", partialInvoice.Vendor))
	}
	if partialInvoice.Customer != "" {
		prompt.WriteString(fmt.Sprintf("Customer: %s\n", partialInvoice.Customer))
	}
	if partialInvoice.InvoiceNumber != "" {
		prompt.WriteString(fmt.Sprintf("Invoice Number: %s\n", partialInvoice.InvoiceNumber))
	}
	if partialInvoice.GrossAmount > 0 {
		prompt.WriteString(fmt.Sprintf("Gross Amount: %.2f %s\n", float64(partialInvoice.GrossAmount)/100, partialInvoice.Currency))
	}

	prompt.WriteString("\nOCR Text:\n")
	prompt.WriteString(ocrText)

	prompt.WriteString("\n\nReturn JSON with these fields (include only missing fields):\n")
	prompt.WriteString("{\n")

	// Always include type since it's critical and rarely provided by Document AI
	if contains(missingFields, "type") {
		prompt.WriteString(`  "type": "PAYABLE or RECEIVABLE (REQUIRED)",` + "\n")
		prompt.WriteString(`  "type_confidence": "confidence score 0-1",` + "\n")
		prompt.WriteString(`  "type_reasoning": "brief explanation of determination",` + "\n")
	}

	// Always include accounting summary (it's always useful for German accounting)
	prompt.WriteString(`  "accounting_summary": "German description of goods/services and Kontierungsvorschlag",` + "\n")

	// Add other missing fields
	for _, field := range missingFields {
		switch field {
		case "vendor":
			prompt.WriteString(`  "vendor": "vendor/supplier company name",` + "\n")
		case "customer":
			prompt.WriteString(`  "customer": "customer/buyer company name",` + "\n")
		case "invoice_number":
			prompt.WriteString(`  "invoice_number": "invoice or reference number",` + "\n")
		case "issue_date":
			prompt.WriteString(`  "issue_date": "YYYY-MM-DD",` + "\n")
		case "due_date":
			prompt.WriteString(`  "due_date": "YYYY-MM-DD",` + "\n")
		case "net_amount":
			prompt.WriteString(`  "net_amount": "amount before tax as string",` + "\n")
		case "vat_amount":
			prompt.WriteString(`  "vat_amount": "tax amount as string",` + "\n")
		case "gross_amount":
			prompt.WriteString(`  "gross_amount": "total amount as string",` + "\n")
		case "currency":
			prompt.WriteString(`  "currency": "currency code like EUR, USD",` + "\n")
		case "reference":
			prompt.WriteString(`  "reference": "purchase order or reference number",` + "\n")
		case "description":
			prompt.WriteString(`  "description": "brief invoice description",` + "\n")
		}
	}

	prompt.WriteString("}")

	return prompt.String()
}

// mergeCompletionResults merges ChatGPT results into the invoice
func (s *DefaultInvoiceCompletionService) mergeCompletionResults(invoice *models.Invoice, response *ChatGPTResponse, missingFields []string, confidence map[string]float32) error {
	// Type field (always merge if missing since it's critical)
	if contains(missingFields, "type") && response.Type != "" {
		invoice.Type = response.Type
		
		// Parse confidence from string
		typeConfidence := float32(0.5) // default
		if conf, err := strconv.ParseFloat(response.TypeConfidence, 32); err == nil {
			typeConfidence = float32(conf)
		}
		confidence["type"] = typeConfidence
		
		s.log.Info().
			Str("type", response.Type).
			Float32("confidence", typeConfidence).
			Str("reasoning", response.TypeReasoning).
			Msg("Invoice type determined")
	}

	// Vendor
	if contains(missingFields, "vendor") && response.Vendor != "" {
		invoice.Vendor = response.Vendor
		confidence["vendor"] = 0.8 // Default confidence for text fields
	}

	// Customer
	if contains(missingFields, "customer") && response.Customer != "" {
		invoice.Customer = response.Customer
		confidence["customer"] = 0.8
	}

	// Invoice Number
	if contains(missingFields, "invoice_number") && response.InvoiceNumber != "" {
		invoice.InvoiceNumber = response.InvoiceNumber
		confidence["invoice_number"] = 0.9
	}

	// Issue Date
	if contains(missingFields, "issue_date") && response.IssueDate != "" {
		if date, err := time.Parse("2006-01-02", response.IssueDate); err == nil {
			invoice.IssueDate = date
			confidence["issue_date"] = 0.8
		} else {
			s.log.Warn().Err(err).Str("date", response.IssueDate).Msg("Failed to parse issue date")
		}
	}

	// Due Date
	if contains(missingFields, "due_date") && response.DueDate != "" {
		if date, err := time.Parse("2006-01-02", response.DueDate); err == nil {
			invoice.DueDate = date
			confidence["due_date"] = 0.8
		} else {
			s.log.Warn().Err(err).Str("date", response.DueDate).Msg("Failed to parse due date")
		}
	}

	// Amounts
	if contains(missingFields, "net_amount") && response.NetAmount != "" {
		if amount, err := s.parseAmount(response.NetAmount); err == nil {
			invoice.NetAmount = amount
			confidence["net_amount"] = 0.7
		} else {
			s.log.Warn().Err(err).Str("amount", response.NetAmount).Msg("Failed to parse net amount")
		}
	}

	if contains(missingFields, "vat_amount") && response.VATAmount != "" {
		if amount, err := s.parseAmount(response.VATAmount); err == nil {
			invoice.VATAmount = amount
			confidence["vat_amount"] = 0.7
		} else {
			s.log.Warn().Err(err).Str("amount", response.VATAmount).Msg("Failed to parse VAT amount")
		}
	}

	if contains(missingFields, "gross_amount") && response.GrossAmount != "" {
		if amount, err := s.parseAmount(response.GrossAmount); err == nil {
			invoice.GrossAmount = amount
			confidence["gross_amount"] = 0.7
		} else {
			s.log.Warn().Err(err).Str("amount", response.GrossAmount).Msg("Failed to parse gross amount")
		}
	}

	// Currency
	if contains(missingFields, "currency") && response.Currency != "" {
		invoice.Currency = strings.ToUpper(response.Currency)
		confidence["currency"] = 0.9
	}

	// Reference
	if contains(missingFields, "reference") && response.Reference != "" {
		invoice.Reference = response.Reference
		confidence["reference"] = 0.8
	}

	// Description
	if contains(missingFields, "description") && response.Description != "" {
		invoice.Description = response.Description
		confidence["description"] = 0.8
	}

	// Accounting Summary (always apply if provided)
	if response.AccountingSummary != "" {
		invoice.AccountingSummary = response.AccountingSummary
		confidence["accounting_summary"] = 0.8
		s.log.Info().
			Str("summary", response.AccountingSummary).
			Msg("German accounting summary generated")
	}

	// Update timestamps
	invoice.UpdatedAt = time.Now()

	return nil
}

// parseAmount parses amount string handling European format
func (s *DefaultInvoiceCompletionService) parseAmount(amountStr string) (int64, error) {
	// Clean the amount string
	cleaned := strings.TrimSpace(amountStr)
	cleaned = strings.ReplaceAll(cleaned, ",", ".")
	cleaned = strings.ReplaceAll(cleaned, " ", "")
	cleaned = strings.ReplaceAll(cleaned, "€", "")
	cleaned = strings.ReplaceAll(cleaned, "$", "")
	cleaned = strings.ReplaceAll(cleaned, "EUR", "")
	cleaned = strings.ReplaceAll(cleaned, "USD", "")

	amount, err := strconv.ParseFloat(cleaned, 64)
	if err != nil {
		return 0, fmt.Errorf("unable to parse amount: %s", amountStr)
	}

	return int64(amount * 100), nil
}

// validateCompletedInvoice performs final validation on the completed invoice
func (s *DefaultInvoiceCompletionService) validateCompletedInvoice(invoice *models.Invoice) error {
	// Validate type field
	if invoice.Type != "PAYABLE" && invoice.Type != "RECEIVABLE" {
		return fmt.Errorf("invalid invoice type: %s, must be PAYABLE or RECEIVABLE", invoice.Type)
	}

	// Ensure we have basic required fields
	if invoice.InvoiceNumber == "" {
		return fmt.Errorf("invoice number is still missing after completion")
	}

	if invoice.GrossAmount <= 0 {
		return fmt.Errorf("gross amount is still missing or invalid after completion")
	}

	// Calculate missing amounts if we have enough information
	s.calculateMissingAmounts(invoice)

	return nil
}

// calculateMissingAmounts calculates missing amount fields if possible
func (s *DefaultInvoiceCompletionService) calculateMissingAmounts(invoice *models.Invoice) {
	// If we have net and VAT, calculate gross
	if invoice.NetAmount > 0 && invoice.VATAmount > 0 && invoice.GrossAmount == 0 {
		invoice.GrossAmount = invoice.NetAmount + invoice.VATAmount
		s.log.Debug().Msg("Calculated gross amount from net + VAT")
	}
	// If we have gross and VAT, calculate net
	if invoice.GrossAmount > 0 && invoice.VATAmount > 0 && invoice.NetAmount == 0 {
		invoice.NetAmount = invoice.GrossAmount - invoice.VATAmount
		s.log.Debug().Msg("Calculated net amount from gross - VAT")
	}
	// If we have gross and net, calculate VAT
	if invoice.GrossAmount > 0 && invoice.NetAmount > 0 && invoice.VATAmount == 0 {
		invoice.VATAmount = invoice.GrossAmount - invoice.NetAmount
		s.log.Debug().Msg("Calculated VAT amount from gross - net")
	}
}

// contains checks if a string slice contains a value
func contains(slice []string, item string) bool {
	for _, s := range slice {
		if s == item {
			return true
		}
	}
	return false
}

// getString safely extracts a string value from a map[string]interface{}
func getString(m map[string]interface{}, key string) string {
	if value, exists := m[key]; exists && value != nil {
		if str, ok := value.(string); ok {
			return str
		}
	}
	return ""
}

// Helper functions for environment parsing
func parseIntEnv(key string, defaultValue int) int {
	if value := os.Getenv(key); value != "" {
		if parsed, err := strconv.Atoi(value); err == nil {
			return parsed
		}
	}
	return defaultValue
}

func parseFloatEnv(key string, defaultValue float32) float32 {
	if value := os.Getenv(key); value != "" {
		if parsed, err := strconv.ParseFloat(value, 32); err == nil {
			return float32(parsed)
		}
	}
	return defaultValue
}