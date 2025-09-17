package cmd

import (
	"context"
	"fmt"
	"os"
	"time"

	"github.com/spf13/cobra"
	"tools/internal/logger"
	"tools/internal/reconciliation"
	"tools/internal/sheets"
)

var reconcileCmd = &cobra.Command{
	Use:   "reconcile",
	Short: "Reconcile bank transactions with invoices",
	Long: `Reconcile bank transactions with payable and receivable invoices from Google Sheets.

This command reads bank transactions from the "Bank" sheet and matches them with
invoices from "Kreditoren" (payables) and "Debitoren" (receivables) sheets.

Required environment variables:
  GOOGLE_APPLICATION_CREDENTIALS - Path to service account JSON file, OR
  GOOGLE_CREDENTIALS - Inline JSON credentials string
  GOOGLE_SHEET_URL - Google Sheets URL containing Bank, Kreditoren, Debitoren sheets`,
	Example: `  # Basic reconciliation
  tools reconcile

  # Reconciliation with specific cutoff date
  tools reconcile --cutoff-date 2025-06-30

  # Dry run with custom batch size
  tools reconcile --cutoff-date 2025-06-30 --batch-size 50 --dry-run`,
	RunE: runReconcile,
}

func init() {
	rootCmd.AddCommand(reconcileCmd)

	reconcileCmd.Flags().String("cutoff-date", "", "Cutoff date for analysis (format: YYYY-MM-DD, default: today)")
	reconcileCmd.Flags().Bool("dry-run", false, "Analyze but don't create output sheets")
	reconcileCmd.Flags().Int("batch-size", 10, "Number of transactions to process in each batch")
}

func runReconcile(cmd *cobra.Command, args []string) error {
	log := logger.WithComponent("reconcile")

	// Get flags
	cutoffDateStr, _ := cmd.Flags().GetString("cutoff-date")
	dryRun, _ := cmd.Flags().GetBool("dry-run")
	batchSize, _ := cmd.Flags().GetInt("batch-size")

	// Parse cutoff date
	var cutoffDate time.Time
	if cutoffDateStr == "" {
		cutoffDate = time.Now()
	} else {
		parsedDate, err := time.Parse("2006-01-02", cutoffDateStr)
		if err != nil {
			return fmt.Errorf("invalid cutoff date format. Use YYYY-MM-DD: %w", err)
		}
		cutoffDate = parsedDate
	}

	// Validate batch size
	if batchSize <= 0 {
		return fmt.Errorf("batch size must be positive")
	}

	// Check required environment variables
	sheetURL := os.Getenv("GOOGLE_SHEET_URL")
	if sheetURL == "" {
		return fmt.Errorf("GOOGLE_SHEET_URL environment variable is required")
	}

	log.Info().
		Str("cutoff_date", cutoffDate.Format("2006-01-02")).
		Bool("dry_run", dryRun).
		Int("batch_size", batchSize).
		Str("sheet_url", sheetURL).
		Msg("Starting bank reconciliation")

	// Create context
	ctx := context.Background()

	// Initialize Google Sheets client
	sheetsService, err := sheets.NewSheetsService(ctx, sheetURL)
	if err != nil {
		return fmt.Errorf("failed to initialize Google Sheets service: %w", err)
	}

	log.Info().Msg("Google Sheets service initialized successfully")

	// Validate required sheets exist
	requiredSheets := []string{"Bank", "Kreditoren", "Debitoren"}
	if err := validateSheetsExist(ctx, sheetsService, requiredSheets); err != nil {
		return fmt.Errorf("sheet validation failed: %w", err)
	}

	log.Info().Strs("sheets", requiredSheets).Msg("All required sheets validated")

	// Initialize data reader
	dataReader := reconciliation.NewDataReader(sheetsService)

	// Read and process data
	if err := processReconciliation(ctx, dataReader, cutoffDate, batchSize, dryRun); err != nil {
		return fmt.Errorf("reconciliation processing failed: %w", err)
	}

	log.Info().Msg("Bank reconciliation completed successfully")
	return nil
}

// validateSheetsExist checks that all required sheets exist in the spreadsheet
func validateSheetsExist(ctx context.Context, sheetsService *sheets.Service, requiredSheets []string) error {
	const op = "validateSheetsExist"
	log := logger.WithComponent("reconcile-validation")

	log.Info().Strs("required_sheets", requiredSheets).Msg("Validating sheet existence")

	for _, sheetName := range requiredSheets {
		log.Debug().Str("sheet", sheetName).Msg("Checking sheet existence")

		// Try to read a small range to verify sheet exists
		_, err := sheetsService.ReadRange(ctx, sheetName+"!A1:A1")
		if err != nil {
			return fmt.Errorf("%s: sheet '%s' does not exist or is not accessible: %w", op, sheetName, err)
		}

		log.Debug().Str("sheet", sheetName).Msg("Sheet exists and is accessible")
	}

	return nil
}

// processReconciliation performs the main reconciliation logic
func processReconciliation(ctx context.Context, dataReader *reconciliation.DataReader, cutoffDate time.Time, batchSize int, dryRun bool) error {
	const op = "processReconciliation"
	log := logger.WithComponent("reconcile-process")

	log.Info().
		Str("cutoff_date", cutoffDate.Format("2006-01-02")).
		Int("batch_size", batchSize).
		Bool("dry_run", dryRun).
		Msg("Starting reconciliation processing")

	// Read bank transactions
	bankTransactions, err := dataReader.ReadBankTransactions(ctx)
	if err != nil {
		return fmt.Errorf("%s: failed to read bank transactions: %w", op, err)
	}
	log.Info().Int("bank_transactions", len(bankTransactions)).Msg("Bank transactions read successfully")

	// Read payable invoices
	payableInvoices, err := dataReader.ReadInvoices(ctx, "Kreditoren")
	if err != nil {
		return fmt.Errorf("%s: failed to read payable invoices: %w", op, err)
	}
	log.Info().Int("payable_invoices", len(payableInvoices)).Msg("Payable invoices read successfully")

	// Read receivable invoices
	receivableInvoices, err := dataReader.ReadInvoices(ctx, "Debitoren")
	if err != nil {
		return fmt.Errorf("%s: failed to read receivable invoices: %w", op, err)
	}
	log.Info().Int("receivable_invoices", len(receivableInvoices)).Msg("Receivable invoices read successfully")

	// Create reconciliation data structure
	data := reconciliation.ReconciliationData{
		BankTransactions:   bankTransactions,
		PayableInvoices:    payableInvoices,
		ReceivableInvoices: receivableInvoices,
	}

	// Process reconciliation
	if err := performReconciliation(data, cutoffDate, batchSize, dryRun); err != nil {
		return fmt.Errorf("%s: failed to perform reconciliation: %w", op, err)
	}

	return nil
}


// performReconciliation performs the actual reconciliation logic
func performReconciliation(
	data reconciliation.ReconciliationData,
	cutoffDate time.Time,
	batchSize int,
	dryRun bool,
) error {
	const op = "performReconciliation"
	log := logger.WithComponent("reconcile-engine")

	log.Info().
		Int("bank_transactions", len(data.BankTransactions)).
		Int("payable_invoices", len(data.PayableInvoices)).
		Int("receivable_invoices", len(data.ReceivableInvoices)).
		Str("cutoff_date", cutoffDate.Format("2006-01-02")).
		Int("batch_size", batchSize).
		Bool("dry_run", dryRun).
		Msg("Starting reconciliation analysis")

	// TODO: Implement actual reconciliation logic
	// - Match transactions with invoices
	// - Apply cutoff date filtering
	// - Process in batches
	// - Generate reconciliation report

	log.Info().Msg("Reconciliation analysis completed (placeholder)")

	if dryRun {
		log.Info().Msg("Dry run mode: No output sheets created")
	} else {
		log.Info().Msg("TODO: Create output sheets with reconciliation results")
	}

	return nil
}