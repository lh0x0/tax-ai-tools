package cmd

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/spf13/cobra"
	"github.com/rs/zerolog"
	"tools/internal/logger"
	"tools/internal/sheets"
	"tools/pkg/models"
	"tools/pkg/services"
)

var datevBatchCmd = &cobra.Command{
	Use:   "datev-batch [folder-path]",
	Short: "Process all PDFs in a folder and write DATEV bookings to Google Sheets",
	Long: `Process all PDF invoices in a folder, generate DATEV bookings, and write results to Google Sheets.

This command processes all PDF files in the specified folder through Document AI, 
completes missing information using OCR and ChatGPT, generates DATEV booking entries 
according to SKR03 standards, and writes the results to a Google Sheet.

The tool writes to different sheets based on invoice type:
- payable (Eingangsrechnungen) → "Kreditoren" sheet
- receivable (Ausgangsrechnungen) → "Debitoren" sheet

Required environment variables:
  GOOGLE_APPLICATION_CREDENTIALS - Path to service account JSON file, OR
  GOOGLE_CREDENTIALS - Inline JSON credentials string
  GOOGLE_CLOUD_PROJECT - Your Google Cloud project ID
  GOOGLE_CLOUD_LOCATION - Processing location (us, eu, etc.)
  DOCUMENT_AI_PROCESSOR_ID - Your Document AI invoice processor ID
  OPENAI_API_KEY - OpenAI API key for ChatGPT
  COMPANY_NAME - Your company name for invoice type determination
  GOOGLE_SHEET_URL - Google Sheets URL to write results

Optional environment variables:
  BATCH_WORKERS - Number of parallel workers (default: 12)`,
	Example: `  # Process all PDFs as Eingangsrechnungen
  tools datev-batch ./invoices --type payable

  # Process as Ausgangsrechnungen with verbose output
  tools datev-batch ./invoices --type receivable --verbose

  # Dry run to test processing without writing to sheet
  tools datev-batch ./invoices --type payable --dry-run

  # Use different chart of accounts
  tools datev-batch ./invoices --type payable --skr 03`,
	Args: cobra.ExactArgs(1),
	RunE: runDATEVBatch,
}

// BatchResult represents the result of processing a single PDF
type BatchResult struct {
	Filename  string
	Invoice   *models.Invoice
	Booking   *services.DATEVBooking
	Error     error
	Status    string // "success", "warning", "error"
	Index     int    // Original order index
}

// WorkerJob represents a PDF processing job
type WorkerJob struct {
	FilePath string
	Index    int
}

func init() {
	rootCmd.AddCommand(datevBatchCmd)

	datevBatchCmd.Flags().String("type", "", "Rechnungstyp (payable=Eingangsrechnungen, receivable=Ausgangsrechnungen) [REQUIRED]")
	datevBatchCmd.Flags().String("skr", "03", "Kontenrahmen (03=SKR03, 04=SKR04)")
	datevBatchCmd.Flags().Bool("dry-run", false, "Process files but don't write to Google Sheet")
	datevBatchCmd.Flags().Bool("verbose", false, "Show detailed processing information")
	
	datevBatchCmd.MarkFlagRequired("type")
}

func runDATEVBatch(cmd *cobra.Command, args []string) error {
	log := logger.WithComponent("datev-batch")

	// Get flags
	folderPath := args[0]
	invoiceType, _ := cmd.Flags().GetString("type")
	skr, _ := cmd.Flags().GetString("skr")
	dryRun, _ := cmd.Flags().GetBool("dry-run")
	verbose, _ := cmd.Flags().GetBool("verbose")

	// Validate and normalize invoice type
	invoiceType = strings.ToUpper(invoiceType)
	if invoiceType != "PAYABLE" && invoiceType != "RECEIVABLE" {
		return fmt.Errorf("invalid invoice type: %s (must be 'payable' or 'receivable')", invoiceType)
	}

	// Validate SKR parameter
	if skr != "03" {
		return fmt.Errorf("only SKR03 is currently supported, got: %s", skr)
	}

	// Validate folder path
	folderInfo, err := os.Stat(folderPath)
	if err != nil {
		return fmt.Errorf("folder not found: %s", folderPath)
	}
	if !folderInfo.IsDir() {
		return fmt.Errorf("path is not a directory: %s", folderPath)
	}

	log.Info().
		Str("folder", folderPath).
		Str("type", invoiceType).
		Str("skr", skr).
		Bool("dry_run", dryRun).
		Bool("verbose", verbose).
		Msg("Starting DATEV batch processing")

	// Print header
	fmt.Println(strings.Repeat("=", 80))
	fmt.Println("                         DATEV BATCH PROCESSING")
	fmt.Println(strings.Repeat("=", 80))
	fmt.Printf("Ordner: %s\n", folderPath)
	
	invoiceTypeGerman := "Eingangsrechnungen"
	sheetName := "Kreditoren"
	if invoiceType == "RECEIVABLE" {
		invoiceTypeGerman = "Ausgangsrechnungen"
		sheetName = "Debitoren"
	}
	fmt.Printf("Typ: %s (%s)\n", invoiceTypeGerman, strings.ToLower(invoiceType))
	fmt.Printf("Kontenrahmen: SKR%s\n", skr)
	if dryRun {
		fmt.Printf("Modus: Dry Run (keine Google Sheets Aktualisierung)\n")
	}
	fmt.Println()

	// Create context with timeout
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Minute)
	defer cancel()

	// Create booking service
	bookingService, err := createBookingService(ctx, skr, log)
	if err != nil {
		return err
	}

	// Find all PDF files
	pdfFiles, err := findPDFFiles(folderPath)
	if err != nil {
		return fmt.Errorf("failed to find PDF files: %w", err)
	}

	if len(pdfFiles) == 0 {
		fmt.Println("Keine PDF-Dateien im Ordner gefunden.")
		return nil
	}

	// Get number of workers from environment or use default
	numWorkers := getNumWorkers()
	fmt.Printf("Verarbeite %d PDFs mit %d parallelen Workern...\n", len(pdfFiles), numWorkers)
	fmt.Println()

	// Process all PDFs in parallel
	results := processPDFsInParallel(ctx, pdfFiles, invoiceType, bookingService, numWorkers, log, verbose)

	fmt.Println()

	// Count results
	successCount := 0
	warningCount := 0
	errorCount := 0
	for _, result := range results {
		switch result.Status {
		case "success":
			successCount++
		case "warning":
			warningCount++
		case "error":
			errorCount++
		}
	}

	// Print summary
	fmt.Println(strings.Repeat("=", 50))
	fmt.Println("                 ERGEBNIS")
	fmt.Println(strings.Repeat("=", 50))
	fmt.Printf("Erfolgreich: %d\n", successCount)
	if warningCount > 0 {
		fmt.Printf("Mit Warnungen: %d\n", warningCount)
	}
	if errorCount > 0 {
		fmt.Printf("Fehler: %d\n", errorCount)
	}
	fmt.Println()

	// Write to Google Sheets if not dry run
	if !dryRun {
		googleSheetURL := os.Getenv("GOOGLE_SHEET_URL")
		if googleSheetURL == "" {
			return fmt.Errorf("GOOGLE_SHEET_URL environment variable is required")
		}

		fmt.Println("Schreibe Daten in Google Sheet...")
		
		// Create Google Sheets service
		sheetsService, err := sheets.NewSheetsService(ctx, googleSheetURL)
		if err != nil {
			return fmt.Errorf("failed to create Google Sheets service: %w", err)
		}

		// Convert results to sheets format
		sheetResults := make([]sheets.BatchResult, len(results))
		for i, result := range results {
			sheetResults[i] = sheets.BatchResult{
				Filename: result.Filename,
				Invoice:  result.Invoice,
				Booking:  result.Booking,
				Error:    result.Error,
				Status:   result.Status,
			}
		}

		// Write to sheet
		err = sheetsService.WriteBatchResults(ctx, sheetResults, sheetName)
		if err != nil {
			return fmt.Errorf("failed to write to Google Sheet: %w", err)
		}
		
		fmt.Printf("Sheet: %s\n", sheetName)
		fmt.Printf("Zeilen hinzugefügt: %d\n", successCount+warningCount)
		fmt.Printf("URL: %s\n", googleSheetURL)
	}

	fmt.Println(strings.Repeat("=", 80))

	log.Info().
		Int("total", len(pdfFiles)).
		Int("success", successCount).
		Int("warnings", warningCount).
		Int("errors", errorCount).
		Msg("DATEV batch processing completed")

	return nil
}

// findPDFFiles finds all PDF files in the specified folder
func findPDFFiles(folderPath string) ([]string, error) {
	var pdfFiles []string

	err := filepath.Walk(folderPath, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}

		if !info.IsDir() && strings.HasSuffix(strings.ToLower(info.Name()), ".pdf") {
			pdfFiles = append(pdfFiles, path)
		}

		return nil
	})

	return pdfFiles, err
}

// processSinglePDF processes a single PDF file and returns the result
func processSinglePDF(ctx context.Context, pdfPath string, invoiceType string, bookingService services.BookingService, log zerolog.Logger, verbose bool) BatchResult {
	result := BatchResult{
		Status:   "error",
	}

	// Open PDF file
	pdfFile, err := os.Open(pdfPath)
	if err != nil {
		result.Error = fmt.Errorf("failed to open PDF file: %w", err)
		return result
	}
	defer pdfFile.Close()

	// Process with booking service with type override
	booking, invoice, err := bookingService.GenerateBookingFromPDFWithType(ctx, pdfFile, invoiceType)
	if err != nil {
		result.Error = fmt.Errorf("booking generation failed: %w", err)
		return result
	}

	result.Invoice = invoice
	result.Booking = booking
	result.Status = "success"

	// Check for potential warnings
	if invoice.NetAmount == 0 && invoice.VATAmount == 0 {
		result.Status = "warning"
	}

	if verbose {
		log.Info().
			Str("file", result.Filename).
			Str("invoice_number", invoice.InvoiceNumber).
			Str("vendor", invoice.Vendor).
			Float64("amount", float64(invoice.GrossAmount)/100).
			Str("debit_account", booking.DebitAccount).
			Str("credit_account", booking.CreditAccount).
			Msg("PDF processed successfully")
	}

	return result
}

// getNumWorkers returns the number of workers from environment or default
func getNumWorkers() int {
	if workersStr := os.Getenv("BATCH_WORKERS"); workersStr != "" {
		if workers, err := strconv.Atoi(workersStr); err == nil && workers > 0 {
			return workers
		}
	}
	return 12 // Default number of workers
}

// processPDFsInParallel processes PDFs using a worker pool pattern
func processPDFsInParallel(ctx context.Context, pdfFiles []string, invoiceType string, bookingService services.BookingService, numWorkers int, log zerolog.Logger, verbose bool) []BatchResult {
	// Create job channel and result slice
	jobs := make(chan WorkerJob, len(pdfFiles))
	results := make([]BatchResult, len(pdfFiles))
	
	// Create progress tracking
	var processedCount int
	var mu sync.Mutex
	
	// Start workers
	var wg sync.WaitGroup
	for w := 0; w < numWorkers; w++ {
		wg.Add(1)
		go func(workerID int) {
			defer wg.Done()
			
			for job := range jobs {
				log.Debug().
					Int("worker", workerID).
					Str("file", job.FilePath).
					Int("index", job.Index+1).
					Msg("Worker processing PDF")

				result := processSinglePDF(ctx, job.FilePath, invoiceType, bookingService, log, verbose)
				result.Index = job.Index
				result.Filename = filepath.Base(job.FilePath)
				
				// Store result in correct position
				results[job.Index] = result
				
				// Update progress safely
				mu.Lock()
				processedCount++
				currentCount := processedCount
				mu.Unlock()
				
				// Show progress
				status := getStatusEmoji(result.Status)
				mu.Lock()
				fmt.Printf("[%d/%d] %s - %s", currentCount, len(pdfFiles), filepath.Base(job.FilePath), status)
				
				if result.Error != nil {
					fmt.Printf(" (%s)", result.Error.Error())
				} else if result.Invoice != nil {
					fmt.Printf(" (€%.2f)", float64(result.Invoice.GrossAmount)/100)
				}
				fmt.Println()
				mu.Unlock()
			}
		}(w)
	}
	
	// Send jobs
	for i, pdfFile := range pdfFiles {
		jobs <- WorkerJob{
			FilePath: pdfFile,
			Index:    i,
		}
	}
	close(jobs)
	
	// Wait for all workers to complete
	wg.Wait()
	
	return results
}

// getStatusEmoji returns an emoji for the processing status
func getStatusEmoji(status string) string {
	switch status {
	case "success":
		return "✅"
	case "warning":
		return "⚠️"
	case "error":
		return "❌"
	default:
		return "❓"
	}
}