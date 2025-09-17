package booking

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
	"tools/internal/invoice"
	"tools/internal/logger"
	"tools/pkg/models"
	"tools/pkg/services"
)

// SKR03BookingService implements BookingService using SKR03 and ChatGPT
type SKR03BookingService struct {
	openaiClient      *openai.Client
	invoiceCompletion invoice.InvoiceCompletionService
	log               zerolog.Logger
}

// ChatGPTBookingResponse represents the structured response from ChatGPT for booking generation
type ChatGPTBookingResponse struct {
	DebitAccount        string `json:"sollkonto"`
	DebitAccountName    string `json:"sollkonto_name"`
	CreditAccount       string `json:"habenkonto"`
	CreditAccountName   string `json:"habenkonto_name"`
	TaxKey              string `json:"steuerschluessel"`
	TaxKeyDescription   string `json:"steuerschluessel_beschreibung"`
	BookingText         string `json:"buchungstext"`
	CostCenter          string `json:"kostenstelle"`
	Explanation         string `json:"erlaeuterung"`
	ReasoningDebit      string `json:"begruendung_sollkonto"`
	ReasoningCredit     string `json:"begruendung_habenkonto"`
	ReasoningTax        string `json:"begruendung_steuer"`
}

// NewSKR03BookingService creates a new SKR03 booking service with dependencies from environment
func NewSKR03BookingService(ctx context.Context) (services.BookingService, error) {
	const op = "NewSKR03BookingService"

	// Get OpenAI API key
	apiKey := os.Getenv("OPENAI_API_KEY")
	if apiKey == "" {
		return nil, fmt.Errorf("%s: OPENAI_API_KEY environment variable is required", op)
	}

	// Create OpenAI client
	openaiClient := openai.NewClient(apiKey)

	// Create invoice completion service for PDF processing
	invoiceCompletion, err := invoice.NewInvoiceCompletionService(ctx)
	if err != nil {
		return nil, fmt.Errorf("%s: failed to create invoice completion service: %w", op, err)
	}

	return &SKR03BookingService{
		openaiClient:      openaiClient,
		invoiceCompletion: invoiceCompletion,
		log:               logger.WithComponent("skr03-booking"),
	}, nil
}

// GenerateBooking creates a DATEV booking entry from a completed invoice
func (s *SKR03BookingService) GenerateBooking(ctx context.Context, invoice *models.Invoice) (*services.DATEVBooking, error) {
	const op = "GenerateBooking"

	s.log.Info().
		Str("invoice_id", invoice.ID).
		Str("type", invoice.Type).
		Str("vendor", invoice.Vendor).
		Float64("amount", float64(invoice.GrossAmount)/100).
		Msg("Generating DATEV booking for invoice")

	// Convert invoice to JSON for ChatGPT
	invoiceJSON, err := json.MarshalIndent(invoice, "", "  ")
	if err != nil {
		return nil, fmt.Errorf("%s: failed to marshal invoice to JSON: %w", op, err)
	}

	// Generate booking using ChatGPT
	bookingResponse, err := s.generateBookingWithChatGPT(ctx, string(invoiceJSON), invoice)
	if err != nil {
		return nil, fmt.Errorf("%s: ChatGPT booking generation failed: %w", op, err)
	}

	// Convert to DATEV booking
	datevBooking := s.convertToDatevBooking(bookingResponse, invoice)

	s.log.Info().
		Str("debit_account", datevBooking.DebitAccount).
		Str("credit_account", datevBooking.CreditAccount).
		Str("tax_key", datevBooking.TaxKey).
		Str("booking_text", datevBooking.BookingText).
		Msg("DATEV booking generated successfully")

	return datevBooking, nil
}

// GenerateBookingFromPDF processes PDF, extracts invoice data, and generates booking
func (s *SKR03BookingService) GenerateBookingFromPDF(ctx context.Context, pdfData io.Reader) (*services.DATEVBooking, *models.Invoice, error) {
	const op = "GenerateBookingFromPDF"

	s.log.Info().Msg("Processing PDF for DATEV booking generation")

	// Buffer the PDF data since we need to read it multiple times
	pdfBytes, err := io.ReadAll(pdfData)
	if err != nil {
		return nil, nil, fmt.Errorf("%s: failed to read PDF data: %w", op, err)
	}

	// Create Document AI processor
	processor, err := invoice.NewDocumentAIInvoiceProcessor(ctx)
	if err != nil {
		return nil, nil, fmt.Errorf("%s: failed to create Document AI processor: %w", op, err)
	}

	// Extract invoice data with Document AI
	partialInvoice, err := processor.ProcessInvoice(ctx, bytes.NewReader(pdfBytes))
	if err != nil {
		return nil, nil, fmt.Errorf("%s: Document AI processing failed: %w", op, err)
	}

	s.log.Info().
		Str("invoice_number", partialInvoice.InvoiceNumber).
		Str("vendor", partialInvoice.Vendor).
		Msg("Invoice extracted with Document AI")

	// Complete invoice with missing fields and accounting summary
	completedInvoice, err := s.invoiceCompletion.CompleteInvoice(ctx, partialInvoice, bytes.NewReader(pdfBytes))
	if err != nil {
		s.log.Warn().Err(err).Msg("Invoice completion failed, using Document AI result only")
		completedInvoice = partialInvoice
	}

	s.log.Info().
		Str("type", completedInvoice.Type).
		Str("accounting_summary", completedInvoice.AccountingSummary).
		Msg("Invoice completion finished")

	// Generate booking from completed invoice
	booking, err := s.GenerateBooking(ctx, completedInvoice)
	if err != nil {
		return nil, nil, fmt.Errorf("%s: booking generation failed: %w", op, err)
	}

	return booking, completedInvoice, nil
}

// GenerateBookingFromPDFWithType processes PDF, extracts invoice data, and generates booking with type override
func (s *SKR03BookingService) GenerateBookingFromPDFWithType(ctx context.Context, pdfData io.Reader, typeOverride string) (*services.DATEVBooking, *models.Invoice, error) {
	const op = "GenerateBookingFromPDFWithType"

	s.log.Info().
		Str("type_override", typeOverride).
		Msg("Processing PDF for DATEV booking generation with type override")

	// Buffer the PDF data since we need to read it multiple times
	pdfBytes, err := io.ReadAll(pdfData)
	if err != nil {
		return nil, nil, fmt.Errorf("%s: failed to read PDF data: %w", op, err)
	}

	// Create Document AI processor
	processor, err := invoice.NewDocumentAIInvoiceProcessor(ctx)
	if err != nil {
		return nil, nil, fmt.Errorf("%s: failed to create Document AI processor: %w", op, err)
	}

	// Extract invoice data with Document AI
	partialInvoice, err := processor.ProcessInvoice(ctx, bytes.NewReader(pdfBytes))
	if err != nil {
		return nil, nil, fmt.Errorf("%s: Document AI processing failed: %w", op, err)
	}

	s.log.Info().
		Str("invoice_number", partialInvoice.InvoiceNumber).
		Str("vendor", partialInvoice.Vendor).
		Msg("Invoice extracted with Document AI")

	// Complete invoice with missing fields but override the type
	completedInvoice, err := s.invoiceCompletion.CompleteInvoice(ctx, partialInvoice, bytes.NewReader(pdfBytes))
	if err != nil {
		s.log.Warn().Err(err).Msg("Invoice completion failed, using Document AI result only")
		completedInvoice = partialInvoice
	}

	// Override the type if provided
	if typeOverride != "" {
		originalType := completedInvoice.Type
		completedInvoice.Type = typeOverride
		s.log.Info().
			Str("original_type", originalType).
			Str("override_type", typeOverride).
			Msg("Invoice type overridden by user")
	}

	s.log.Info().
		Str("type", completedInvoice.Type).
		Str("accounting_summary", completedInvoice.AccountingSummary).
		Msg("Invoice completion finished with type override")

	// Generate booking from completed invoice
	booking, err := s.GenerateBooking(ctx, completedInvoice)
	if err != nil {
		return nil, nil, fmt.Errorf("%s: booking generation failed: %w", op, err)
	}

	return booking, completedInvoice, nil
}

// generateBookingWithChatGPT uses ChatGPT to generate booking information
func (s *SKR03BookingService) generateBookingWithChatGPT(ctx context.Context, invoiceJSON string, invoice *models.Invoice) (*ChatGPTBookingResponse, error) {
	const op = "generateBookingWithChatGPT"

	prompt := s.buildBookingPrompt(invoiceJSON, invoice)

	s.log.Debug().
		Int("prompt_length", len(prompt)).
		Str("invoice_type", invoice.Type).
		Msg("Sending booking request to ChatGPT")

	resp, err := s.openaiClient.CreateChatCompletion(ctx, openai.ChatCompletionRequest{
		Model:       "gpt-4",
		Temperature: 0.1,
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
		MaxTokens: 1500,
	})

	if err != nil {
		return nil, fmt.Errorf("%s: ChatGPT request failed: %w", op, err)
	}

	if len(resp.Choices) == 0 {
		return nil, fmt.Errorf("%s: no response choices from ChatGPT", op)
	}

	content := resp.Choices[0].Message.Content
	s.log.Debug().
		Str("response", content).
		Msg("Received ChatGPT booking response")

	// Parse JSON response
	var bookingResponse ChatGPTBookingResponse
	if err := json.Unmarshal([]byte(content), &bookingResponse); err != nil {
		s.log.Error().
			Err(err).
			Str("response", content).
			Msg("Failed to parse ChatGPT JSON response")
		return nil, fmt.Errorf("%s: failed to parse ChatGPT JSON response: %w (response: %s)", op, err, content)
	}

	// Validate required fields
	if err := s.validateBookingResponse(&bookingResponse); err != nil {
		return nil, fmt.Errorf("%s: invalid booking response: %w", op, err)
	}

	s.log.Info().
		Str("debit_account", bookingResponse.DebitAccount).
		Str("credit_account", bookingResponse.CreditAccount).
		Str("tax_key", bookingResponse.TaxKey).
		Msg("ChatGPT booking response validated")

	return &bookingResponse, nil
}

// getSystemPrompt returns the system prompt for ChatGPT booking generation
func (s *SKR03BookingService) getSystemPrompt() string {
	return `Du bist ein Experte für deutsches Rechnungswesen und DATEV-Buchungen nach SKR03 (Standardkontenrahmen 03).

Deine Aufgabe ist es, für Eingangs- und Ausgangsrechnungen korrekte Buchungssätze zu erstellen.

WICHTIGE REGELN:
- Verwende ausschließlich gültige SKR03-Kontonummern (4-stellig)
- Für Eingangsrechnungen (PAYABLE): Aufwand/Anlagen im Soll, Verbindlichkeiten im Haben
- Für Ausgangsrechnungen (RECEIVABLE): Forderungen im Soll, Erlöse im Haben
- Berücksichtige die korrekte Vorsteuer/Umsatzsteuer je nach Rechnungstyp
- Buchungstext maximal 60 Zeichen
- Begründe deine Kontenwahl fachlich korrekt

SKR03 WICHTIGE KONTEN:
- 0000-0999: Anlagevermögen
- 1000-1999: Umlaufvermögen
- 2000-2999: Eigenkapital
- 3000-3999: Fremdkapital (Verbindlichkeiten)
- 4000-4999: Betriebliche Erträge
- 5000-7999: Betriebliche Aufwendungen
- 8000-8999: Steuern
- 9000-9999: Nicht betriebliche Erträge/Aufwendungen

STEUERSCHLÜSSEL:
- 0: Steuerfrei
- 9: 19% Vorsteuer (Eingangsrechnungen)
- 3: 19% Umsatzsteuer (Ausgangsrechnungen)
- 5: 7% Vorsteuer
- 2: 7% Umsatzsteuer

CRITICAL: Antworte AUSSCHLIESSLICH mit gültigem JSON. Kein Text vor oder nach dem JSON.
- Keine Erklärungen außerhalb des JSON
- Keine Markdown-Formatierung
- Keine trailing commas
- Validiere die JSON-Syntax bevor du antwortest`
}

// buildBookingPrompt creates the user prompt for ChatGPT
func (s *SKR03BookingService) buildBookingPrompt(invoiceJSON string, invoice *models.Invoice) string {
	var prompt strings.Builder

	prompt.WriteString("Erstelle einen DATEV-Buchungssatz nach SKR03 für folgende Rechnung.\n")
	prompt.WriteString("Verwende ausschließlich gültige SKR03-Konten.\n\n")

	prompt.WriteString("Rechnung (JSON):\n")
	prompt.WriteString(invoiceJSON)
	prompt.WriteString("\n\n")

	// Add context about invoice type
	if invoice.Type == "PAYABLE" {
		prompt.WriteString("Dies ist eine EINGANGSRECHNUNG (wir schulden dem Lieferanten Geld).\n")
	} else if invoice.Type == "RECEIVABLE" {
		prompt.WriteString("Dies ist eine AUSGANGSRECHNUNG (Kunde schuldet uns Geld).\n")
	}

	prompt.WriteString("\nGib folgende Buchungsinformationen als JSON zurück:\n")
	prompt.WriteString("{\n")
	prompt.WriteString(`  "sollkonto": "4-stellige SKR03 Kontonummer",` + "\n")
	prompt.WriteString(`  "sollkonto_name": "Bezeichnung des Sollkontos",` + "\n")
	prompt.WriteString(`  "habenkonto": "4-stellige SKR03 Kontonummer",` + "\n")
	prompt.WriteString(`  "habenkonto_name": "Bezeichnung des Habenkontos",` + "\n")
	prompt.WriteString(`  "steuerschluessel": "Steuerschlüssel (0,2,3,5,9)",` + "\n")
	prompt.WriteString(`  "steuerschluessel_beschreibung": "Beschreibung des Steuerschlüssels",` + "\n")
	prompt.WriteString(`  "buchungstext": "Buchungstext max 60 Zeichen",` + "\n")
	prompt.WriteString(`  "kostenstelle": "Kostenstelle falls zutreffend oder leer",` + "\n")
	prompt.WriteString(`  "erlaeuterung": "Ausführliche Erläuterung der Buchung",` + "\n")
	prompt.WriteString(`  "begruendung_sollkonto": "Warum wurde dieses Sollkonto gewählt",` + "\n")
	prompt.WriteString(`  "begruendung_habenkonto": "Warum wurde dieses Habenkonto gewählt",` + "\n")
	prompt.WriteString(`  "begruendung_steuer": "Warum wurde dieser Steuerschlüssel gewählt"` + "\n")
	prompt.WriteString("}\n\n")
	prompt.WriteString("WICHTIG: Antworte NUR mit dem JSON-Object. Keine zusätzlichen Texte oder Erklärungen!")

	return prompt.String()
}

// validateBookingResponse validates the ChatGPT booking response
func (s *SKR03BookingService) validateBookingResponse(response *ChatGPTBookingResponse) error {
	if response.DebitAccount == "" {
		return fmt.Errorf("missing debit account (Sollkonto)")
	}
	if response.CreditAccount == "" {
		return fmt.Errorf("missing credit account (Habenkonto)")
	}
	if response.TaxKey == "" {
		return fmt.Errorf("missing tax key (Steuerschlüssel)")
	}
	if response.BookingText == "" {
		return fmt.Errorf("missing booking text (Buchungstext)")
	}

	// Validate account number format (4 digits)
	if !s.isValidSKR03Account(response.DebitAccount) {
		return fmt.Errorf("invalid debit account format: %s (must be 4-digit SKR03 account)", response.DebitAccount)
	}
	if !s.isValidSKR03Account(response.CreditAccount) {
		return fmt.Errorf("invalid credit account format: %s (must be 4-digit SKR03 account)", response.CreditAccount)
	}

	// Validate booking text length
	if len(response.BookingText) > 60 {
		return fmt.Errorf("booking text too long: %d characters (max 60)", len(response.BookingText))
	}

	return nil
}

// isValidSKR03Account checks if the account number is a valid 4-digit SKR03 account
func (s *SKR03BookingService) isValidSKR03Account(account string) bool {
	if len(account) != 4 {
		return false
	}
	// Check if all characters are digits
	if _, err := strconv.Atoi(account); err != nil {
		return false
	}
	return true
}

// convertToDatevBooking converts ChatGPT response to DATEVBooking struct
func (s *SKR03BookingService) convertToDatevBooking(response *ChatGPTBookingResponse, invoice *models.Invoice) *services.DATEVBooking {
	now := time.Now()
	
	// Use invoice issue date for booking date, fallback to today
	bookingDate := invoice.IssueDate
	if bookingDate.IsZero() {
		bookingDate = now
	}

	// Generate accounting period (MMYYYY)
	accountingPeriod := fmt.Sprintf("%02d%d", bookingDate.Month(), bookingDate.Year())

	return &services.DATEVBooking{
		BookingText:       response.BookingText,
		DebitAccount:      response.DebitAccount,
		CreditAccount:     response.CreditAccount,
		Amount:           float64(invoice.GrossAmount) / 100, // Convert cents to EUR
		TaxKey:           response.TaxKey,
		CostCenter:       response.CostCenter,
		BookingDate:      bookingDate,
		DocumentNumber:   invoice.InvoiceNumber,
		AccountingPeriod: accountingPeriod,
		Explanation:      response.Explanation,
		
		DebitAccountName:  response.DebitAccountName,
		CreditAccountName: response.CreditAccountName,
		TaxKeyDescription: response.TaxKeyDescription,
		
		GeneratedAt:      now,
		ContenrahmenType: "SKR03",
	}
}