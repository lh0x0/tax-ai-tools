package cmd

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/spf13/cobra"
	"github.com/rs/zerolog"
	"tools/internal/booking"
	"tools/internal/logger"
	"tools/pkg/models"
	"tools/pkg/services"
)

var datevCmd = &cobra.Command{
	Use:   "datev [pdf-file]",
	Short: "Generate DATEV booking entries from PDF invoices using AI",
	Long: `Process PDF invoices and generate DATEV accounting entries using ChatGPT.

This command processes invoices through Document AI, completes missing information
using OCR and ChatGPT (including German accounting summaries), and then generates
appropriate DATEV booking entries according to SKR03 accounting standards.

The tool uses ChatGPT to determine the correct:
- Chart of accounts (SKR03 account numbers)
- Tax keys (Steuerschlüssel)
- Booking text (Buchungstext)
- Cost centers (Kostenstellen)
- Accounting explanations

Required environment variables:
  GOOGLE_APPLICATION_CREDENTIALS - Path to service account JSON file, OR
  GOOGLE_CREDENTIALS - Inline JSON credentials string
  GOOGLE_CLOUD_PROJECT - Your Google Cloud project ID
  GOOGLE_CLOUD_LOCATION - Processing location (us, eu, etc.)
  DOCUMENT_AI_PROCESSOR_ID - Your Document AI invoice processor ID
  OPENAI_API_KEY - OpenAI API key for ChatGPT
  COMPANY_NAME - Your company name for invoice type determination`,
	Example: `  # Generate DATEV booking from PDF (console output)
  tools datev invoice.pdf

  # Generate booking with JSON output
  tools datev invoice.pdf --json

  # Show detailed explanations
  tools datev invoice.pdf --verbose

  # Force invoice type (wenn ChatGPT die Richtung falsch erkennt)
  tools datev invoice.pdf --type payable     # Eingangsrechnung
  tools datev invoice.pdf --type receivable  # Ausgangsrechnung

  # Use different chart of accounts (future feature)
  tools datev invoice.pdf --skr 04`,
	Args: cobra.ExactArgs(1),
	RunE: runDatev,
}

func init() {
	rootCmd.AddCommand(datevCmd)

	datevCmd.Flags().String("skr", "03", "Kontenrahmen (03=SKR03, 04=SKR04)")
	datevCmd.Flags().String("type", "", "Rechnungstyp (payable=Eingangsrechnung, receivable=Ausgangsrechnung)")
	datevCmd.Flags().Bool("json", false, "Output as JSON format")
	datevCmd.Flags().Bool("verbose", false, "Show detailed explanation and reasoning")
}

func runDatev(cmd *cobra.Command, args []string) error {
	log := logger.WithComponent("datev")

	// Get flags
	skr, _ := cmd.Flags().GetString("skr")
	invoiceType, _ := cmd.Flags().GetString("type")
	jsonOutput, _ := cmd.Flags().GetBool("json")
	verbose, _ := cmd.Flags().GetBool("verbose")

	pdfPath := args[0]

	log.Info().
		Str("file", pdfPath).
		Str("skr", skr).
		Str("type", invoiceType).
		Bool("json", jsonOutput).
		Bool("verbose", verbose).
		Msg("Starting DATEV booking generation")

	// Validate SKR parameter
	if skr != "03" {
		return fmt.Errorf("only SKR03 is currently supported, got: %s", skr)
	}

	// Validate invoice type parameter if provided
	if invoiceType != "" {
		invoiceType = strings.ToUpper(invoiceType)
		if invoiceType != "PAYABLE" && invoiceType != "RECEIVABLE" {
			return fmt.Errorf("invalid invoice type: %s (must be 'payable' or 'receivable')", invoiceType)
		}
	}

	// Validate and get file info
	fileInfo, err := validateDatevPDFFile(pdfPath, log)
	if err != nil {
		return err
	}

	// Create context with timeout
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()

	// Create booking service
	bookingService, err := createBookingService(ctx, skr, log)
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
		Msg("Processing PDF for DATEV booking generation")

	// Generate booking from PDF
	startTime := time.Now()
	var booking *services.DATEVBooking
	var invoice *models.Invoice

	if invoiceType != "" {
		booking, invoice, err = bookingService.GenerateBookingFromPDFWithType(ctx, pdfFile, invoiceType)
	} else {
		booking, invoice, err = bookingService.GenerateBookingFromPDF(ctx, pdfFile)
	}
	if err != nil {
		return handleDatevError(err, log)
	}

	processingDuration := time.Since(startTime)

	log.Info().
		Str("invoice_number", invoice.InvoiceNumber).
		Str("debit_account", booking.DebitAccount).
		Str("credit_account", booking.CreditAccount).
		Float64("amount", booking.Amount).
		Dur("duration", processingDuration).
		Msg("DATEV booking generated successfully")

	// Output results
	if jsonOutput {
		return outputDatevJSON(booking, invoice, processingDuration)
	} else {
		return outputDatevConsole(booking, invoice, verbose, processingDuration)
	}
}

// validateDatevPDFFile validates the PDF file for DATEV processing
func validateDatevPDFFile(pdfPath string, log zerolog.Logger) (os.FileInfo, error) {
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

	return fileInfo, nil
}

// createBookingService creates the appropriate booking service based on SKR type
func createBookingService(ctx context.Context, skr string, log zerolog.Logger) (services.BookingService, error) {
	switch skr {
	case "03":
		service, err := booking.NewSKR03BookingService(ctx)
		if err != nil {
			if strings.Contains(err.Error(), "OPENAI_API_KEY") {
				log.Error().
					Err(err).
					Msg("OpenAI API key not configured")
				return nil, fmt.Errorf("missing OpenAI API key. Please set:\\n" +
					"  OPENAI_API_KEY=your-openai-api-key\\n" +
					"Original error: %w", err)
			}
			log.Error().
				Err(err).
				Msg("Failed to create SKR03 booking service")
			return nil, fmt.Errorf("failed to create SKR03 booking service: %w", err)
		}

		log.Debug().Msg("SKR03 booking service created successfully")
		return service, nil

	default:
		return nil, fmt.Errorf("unsupported chart of accounts: SKR%s", skr)
	}
}

// handleDatevError provides user-friendly error messages for DATEV processing failures
func handleDatevError(err error, log zerolog.Logger) error {
	log.Error().Err(err).Msg("DATEV booking generation failed")

	errStr := err.Error()

	switch {
	case strings.Contains(errStr, "OPENAI_API_KEY"):
		return fmt.Errorf("OpenAI API key not configured. Please set OPENAI_API_KEY environment variable")
	case strings.Contains(errStr, "Document AI"):
		return fmt.Errorf("invoice processing failed. Please check your Google Cloud configuration")
	case strings.Contains(errStr, "invalid") && strings.Contains(errStr, "account"):
		return fmt.Errorf("ChatGPT returned invalid account numbers. Please try again")
	case strings.Contains(errStr, "ChatGPT"):
		return fmt.Errorf("AI booking generation failed. Please check your OpenAI API configuration")
	default:
		return fmt.Errorf("DATEV booking generation failed: %w", err)
	}
}

// outputDatevJSON outputs the booking results as JSON
func outputDatevJSON(booking *services.DATEVBooking, invoice *models.Invoice, duration time.Duration) error {
	output := map[string]interface{}{
		"booking":  booking,
		"invoice":  invoice,
		"metadata": map[string]interface{}{
			"processing_duration_ms": duration.Milliseconds(),
			"generated_at":          time.Now(),
			"tool_version":          "1.0.0",
		},
	}

	jsonData, err := json.MarshalIndent(output, "", "  ")
	if err != nil {
		return fmt.Errorf("failed to create JSON output: %w", err)
	}

	fmt.Println(string(jsonData))
	return nil
}

// outputDatevConsole outputs the booking results in a formatted console display
func outputDatevConsole(booking *services.DATEVBooking, invoice *models.Invoice, verbose bool, duration time.Duration) error {
	// Header
	fmt.Println(strings.Repeat("=", 80))
	fmt.Println("                           DATEV BUCHUNGSVORSCHLAG")
	fmt.Println(strings.Repeat("=", 80))
	fmt.Println()

	// Invoice Information Section
	fmt.Println("=== RECHNUNGSDATEN ===")
	fmt.Printf("Rechnungsnummer: %s\n", invoice.InvoiceNumber)
	
	invoiceType := "UNBEKANNT"
	if invoice.Type == "PAYABLE" {
		invoiceType = "EINGANGSRECHNUNG"
	} else if invoice.Type == "RECEIVABLE" {
		invoiceType = "AUSGANGSRECHNUNG"
	}
	fmt.Printf("Typ: %s\n", invoiceType)
	
	if invoice.Vendor != "" {
		fmt.Printf("Lieferant: %s\n", invoice.Vendor)
	}
	if invoice.Customer != "" {
		fmt.Printf("Kunde: %s\n", invoice.Customer)
	}

	// Format amounts
	netAmount := float64(invoice.NetAmount) / 100
	vatAmount := float64(invoice.VATAmount) / 100
	grossAmount := float64(invoice.GrossAmount) / 100

	if invoice.NetAmount > 0 && invoice.VATAmount > 0 {
		fmt.Printf("Betrag: %.2f EUR (Netto: %.2f EUR, MwSt: %.2f EUR)\n", 
			grossAmount, netAmount, vatAmount)
	} else {
		fmt.Printf("Betrag: %.2f EUR\n", grossAmount)
	}

	if !invoice.IssueDate.IsZero() {
		fmt.Printf("Rechnungsdatum: %s\n", invoice.IssueDate.Format("02.01.2006"))
	}
	if !invoice.DueDate.IsZero() {
		fmt.Printf("Fälligkeitsdatum: %s\n", invoice.DueDate.Format("02.01.2006"))
	}

	// Show accounting summary if available
	if invoice.AccountingSummary != "" {
		fmt.Printf("Beschreibung: %s\n", invoice.AccountingSummary)
	}

	fmt.Println()

	// Booking Information Section
	fmt.Printf("=== DATEV BUCHUNGSVORSCHLAG (%s) ===\n", booking.ContenrahmenType)
	fmt.Printf("Sollkonto: %s - %s\n", booking.DebitAccount, booking.DebitAccountName)
	fmt.Printf("Habenkonto: %s - %s\n", booking.CreditAccount, booking.CreditAccountName)
	fmt.Printf("Betrag: %.2f EUR\n", booking.Amount)
	fmt.Printf("Steuerschlüssel: %s (%s)\n", booking.TaxKey, booking.TaxKeyDescription)
	fmt.Printf("Buchungstext: %s\n", booking.BookingText)
	fmt.Printf("Belegnummer: %s\n", booking.DocumentNumber)
	fmt.Printf("Buchungsdatum: %s\n", booking.BookingDate.Format("02.01.2006"))
	fmt.Printf("Buchungsperiode: %s\n", booking.AccountingPeriod)
	
	if booking.CostCenter != "" {
		fmt.Printf("Kostenstelle: %s\n", booking.CostCenter)
	} else {
		fmt.Printf("Kostenstelle: -\n")
	}

	fmt.Println()

	// Explanation
	if booking.Explanation != "" {
		fmt.Printf("Erläuterung: %s\n", booking.Explanation)
		fmt.Println()
	}

	// Verbose information
	if verbose {
		fmt.Println("=== DETAILLIERTE INFORMATIONEN ===")
		fmt.Printf("Verarbeitungszeit: %.2f Sekunden\n", duration.Seconds())
		fmt.Printf("Generiert am: %s\n", booking.GeneratedAt.Format("02.01.2006 15:04:05"))
		fmt.Println()

		if booking.Explanation != "" {
			fmt.Println("=== BUCHUNGSLOGIK ===")
			fmt.Printf("%s\n", booking.Explanation)
			fmt.Println()
		}
	}

	// Footer
	fmt.Println(strings.Repeat("=", 80))
	fmt.Println("Hinweis: Dies ist ein KI-generierter Buchungsvorschlag.")
	fmt.Println("Bitte prüfen Sie die Buchung vor der Übernahme in DATEV.")
	fmt.Println(strings.Repeat("=", 80))

	return nil
}