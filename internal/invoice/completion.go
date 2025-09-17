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
	return fmt.Sprintf(`Du analysierst Rechnungen für %s. Deine wichtigste Aufgabe ist die korrekte Bestimmung des Rechnungstyps.

KRITISCH: Bestimme ob diese Rechnung PAYABLE oder RECEIVABLE ist:

** PAYABLE (Eingangsrechnung) = WIR MÜSSEN ZAHLEN **
- Rechnung VON einem Lieferanten AN unser Unternehmen
- Wir sind der Käufer/Rechnungsempfänger
- Zahlungsanweisungen: Geld soll AN den Lieferanten/Verkäufer
- Bankverbindung gehört dem Verkäufer/Lieferanten
- Typische Begriffe: "Rechnung an", "Invoice To", "Bill To" + unser Firmenname
- Lieferant/Verkäufer ist NICHT unser Unternehmen

** RECEIVABLE (Ausgangsrechnung) = WIR BEKOMMEN GELD **
- Rechnung VON unserem Unternehmen AN einen Kunden
- Wir sind der Verkäufer/Rechnungssteller
- Zahlungsanweisungen: Geld soll AN unser Unternehmen
- Bankverbindung gehört uns
- Typische Begriffe: "From" + unser Firmenname, wir sind der Absender
- Kunde ist NICHT unser Unternehmen

ENTSCHEIDUNGSHILFEN:
1. Wer stellt die Rechnung? (From/Absender) → Wenn wir = RECEIVABLE
2. Wer soll zahlen? (To/Empfänger) → Wenn wir = PAYABLE  
3. Wessen Bankdaten stehen drauf? → Wenn unsere = RECEIVABLE
4. Deutsche Begriffe: "Lieferant", "Anbieter", "Verkäufer" → meist PAYABLE für uns

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

IMPORTANT: Return ONLY valid JSON with NO trailing commas. 
- Use null for missing values
- Amounts should be in the original currency format (e.g., "580.00" for 580 euros)
- Dates should be in YYYY-MM-DD format
- Ensure the JSON is perfectly formatted with no syntax errors
- Do NOT add a trailing comma after the last field`,
		s.config.CompanyName,
		s.config.CompanyName,
		strings.Join(s.config.CompanyAliases, ", "))
}

// buildCompletionPrompt creates the user prompt for ChatGPT
func (s *DefaultInvoiceCompletionService) buildCompletionPrompt(ocrText string, missingFields []string, partialInvoice *models.Invoice) string {
	var prompt strings.Builder

	prompt.WriteString("Analysiere diese Rechnung und extrahiere die fehlenden Informationen:\n\n")

	// Current invoice data for context with type hints
	prompt.WriteString("Bereits extrahierte Daten:\n")
	if partialInvoice.Vendor != "" {
		prompt.WriteString(fmt.Sprintf("Vendor/Lieferant: %s\n", partialInvoice.Vendor))
		// Type hint: If we already have a vendor, this is likely PAYABLE
		if contains(missingFields, "type") {
			prompt.WriteString("HINWEIS: Da bereits ein Vendor/Lieferant erkannt wurde, ist dies wahrscheinlich eine PAYABLE Rechnung (Eingangsrechnung)\n")
		}
	}
	if partialInvoice.Customer != "" {
		prompt.WriteString(fmt.Sprintf("Customer/Kunde: %s\n", partialInvoice.Customer))
		// Type hint: If we already have a customer, this is likely RECEIVABLE  
		if contains(missingFields, "type") {
			prompt.WriteString("HINWEIS: Da bereits ein Customer/Kunde erkannt wurde, ist dies wahrscheinlich eine RECEIVABLE Rechnung (Ausgangsrechnung)\n")
		}
	}
	if partialInvoice.InvoiceNumber != "" {
		prompt.WriteString(fmt.Sprintf("Rechnungsnummer: %s\n", partialInvoice.InvoiceNumber))
	}
	if partialInvoice.GrossAmount > 0 {
		prompt.WriteString(fmt.Sprintf("Bruttobetrag: %.2f %s\n", float64(partialInvoice.GrossAmount)/100, partialInvoice.Currency))
	}

	// Add company context for type determination
	if contains(missingFields, "type") {
		prompt.WriteString(fmt.Sprintf("\nFIRMEN-KONTEXT für Typ-Bestimmung:\n"))
		prompt.WriteString(fmt.Sprintf("Unser Unternehmen: %s\n", s.config.CompanyName))
		if len(s.config.CompanyAliases) > 0 {
			prompt.WriteString(fmt.Sprintf("Unsere Aliases: %s\n", strings.Join(s.config.CompanyAliases, ", ")))
		}
		prompt.WriteString("→ Wenn unser Name im 'Bill To'/'Rechnung an' steht = PAYABLE (wir zahlen)\n")
		prompt.WriteString("→ Wenn unser Name im 'From'/'Von' steht = RECEIVABLE (wir bekommen Geld)\n\n")
	}

	prompt.WriteString("\nOCR Text:\n")
	prompt.WriteString(ocrText)

	prompt.WriteString("\n\nGib JSON zurück mit diesen Feldern (nur fehlende Felder):\n")
	prompt.WriteString("{\n")

	// Always include type since it's critical and rarely provided by Document AI
	if contains(missingFields, "type") {
		prompt.WriteString(`  "type": "PAYABLE oder RECEIVABLE (ERFORDERLICH - siehe Entscheidungshilfen oben)",` + "\n")
		prompt.WriteString(`  "type_confidence": "Konfidenz-Score 0-1 (0.9+ für eindeutige Indikatoren)",` + "\n")
		prompt.WriteString(`  "type_reasoning": "Deutsche Begründung der Typ-Bestimmung mit konkreten Textstellen",` + "\n")
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

	prompt.WriteString("}\n\n")
	prompt.WriteString("WICHTIG: Stelle sicher dass das JSON KEINE trailing comma nach dem letzten Feld hat. Prüfe die JSON-Syntax sorgfältig!\n")
	prompt.WriteString("AUSSCHLIESSLICH gültiges JSON ohne Text davor oder danach!")

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
		invoice.Currency = s.normalizeCurrency(response.Currency)
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

// parseAmount parses amount string handling both German and English formats
func (s *DefaultInvoiceCompletionService) parseAmount(amountStr string) (int64, error) {
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

// validateCompletedInvoice performs final validation on the completed invoice
func (s *DefaultInvoiceCompletionService) validateCompletedInvoice(invoice *models.Invoice) error {
	// Validate type field
	if invoice.Type != "PAYABLE" && invoice.Type != "RECEIVABLE" {
		return fmt.Errorf("invalid invoice type: %s, must be PAYABLE or RECEIVABLE", invoice.Type)
	}

	// Ensure we have basic required fields
	// Note: Invoice number is not strictly required for certain document types
	// (e.g., membership fees, exam fees, etc.)
	
	// Check for valid amounts - allow negative amounts for credit notes/refunds
	if invoice.GrossAmount == 0 && invoice.NetAmount == 0 && invoice.VATAmount == 0 {
		// All amounts are zero - likely no amount information found
		return fmt.Errorf("no amount information found after completion")
	}
	
	// Allow negative amounts for credit notes, refunds, returns
	if invoice.GrossAmount < 0 || invoice.NetAmount < 0 {
		s.log.Info().
			Int64("gross_amount", invoice.GrossAmount).
			Int64("net_amount", invoice.NetAmount).
			Str("summary", invoice.AccountingSummary).
			Msg("Detected credit note or refund with negative amounts")
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

// normalizeCurrency standardizes currency codes to consistent format
func (s *DefaultInvoiceCompletionService) normalizeCurrency(currency string) string {
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