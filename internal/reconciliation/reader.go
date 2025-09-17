package reconciliation

import (
	"context"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/rs/zerolog"
	"tools/internal/logger"
	"tools/internal/sheets"
)

// DataReader handles reading reconciliation data from Google Sheets
type DataReader struct {
	sheetsService *sheets.Service
	log           zerolog.Logger
}

// NewDataReader creates a new data reader for Google Sheets
func NewDataReader(sheetsService *sheets.Service) *DataReader {
	return &DataReader{
		sheetsService: sheetsService,
		log:           logger.WithComponent("reconciliation-reader"),
	}
}

// ReadBankTransactions reads bank transactions from the "Bank" sheet
func (dr *DataReader) ReadBankTransactions(ctx context.Context) ([]BankTransaction, error) {
	const op = "ReadBankTransactions"
	const sheetName = "Bank"

	dr.log.Info().Str("sheet", sheetName).Msg("Reading bank transactions")

	// Read data from Bank sheet
	// Expected columns: A=Datum, B=Transaktionstyp, C=Beschreibung, D=EREF, E=MREF, 
	// F=CRED, G=SVWZ, H=Empfänger/Absender, I=BIC, J=IBAN, K=Betrag
	values, err := dr.sheetsService.ReadRange(ctx, sheetName+"!A:K")
	if err != nil {
		return nil, fmt.Errorf("%s: failed to read Bank sheet: %w", op, err)
	}

	if len(values) == 0 {
		return nil, fmt.Errorf("%s: Bank sheet is empty", op)
	}

	// Skip header row and parse data
	var transactions []BankTransaction
	for i, row := range values[1:] {
		rowNum := i + 2 // Account for header and 0-based indexing

		if len(row) < 11 {
			dr.log.Warn().
				Int("row", rowNum).
				Int("columns", len(row)).
				Msg("Skipping bank transaction row with insufficient columns")
			continue
		}

		transaction, err := dr.parseBankTransaction(row, rowNum)
		if err != nil {
			dr.log.Warn().
				Err(err).
				Int("row", rowNum).
				Msg("Failed to parse bank transaction, skipping")
			continue
		}

		transactions = append(transactions, transaction)
	}

	dr.log.Info().
		Int("total_rows", len(values)-1).
		Int("parsed_transactions", len(transactions)).
		Str("sheet", sheetName).
		Msg("Bank transactions read successfully")

	return transactions, nil
}

// ReadInvoices reads invoices from the specified sheet (Kreditoren or Debitoren)
func (dr *DataReader) ReadInvoices(ctx context.Context, sheetName string) ([]InvoiceRow, error) {
	const op = "ReadInvoices"

	dr.log.Info().Str("sheet", sheetName).Msg("Reading invoices")

	// Read data from the sheet
	// Expected columns from DATEV batch processing:
	// A=Datei, B=Rechnungsnr, C=Datum, D=Lieferant/Kunde, E=Netto, F=MwSt, G=Brutto, H=Währung
	values, err := dr.sheetsService.ReadRange(ctx, sheetName+"!A:H")
	if err != nil {
		return nil, fmt.Errorf("%s: failed to read %s sheet: %w", op, sheetName, err)
	}

	if len(values) == 0 {
		return nil, fmt.Errorf("%s: %s sheet is empty", op, sheetName)
	}

	// Determine invoice type based on sheet name
	invoiceType := "PAYABLE" // Default for Kreditoren
	if sheetName == "Debitoren" {
		invoiceType = "RECEIVABLE"
	}

	// Skip header row and parse data
	var invoices []InvoiceRow
	for i, row := range values[1:] {
		rowNum := i + 2 // Account for header and 0-based indexing

		if len(row) < 8 {
			dr.log.Warn().
				Int("row", rowNum).
				Int("columns", len(row)).
				Str("sheet", sheetName).
				Msg("Skipping invoice row with insufficient columns")
			continue
		}

		invoice, err := dr.parseInvoiceRow(row, rowNum, invoiceType)
		if err != nil {
			dr.log.Warn().
				Err(err).
				Int("row", rowNum).
				Str("sheet", sheetName).
				Msg("Failed to parse invoice, skipping")
			continue
		}

		invoices = append(invoices, invoice)
	}

	dr.log.Info().
		Int("total_rows", len(values)-1).
		Int("parsed_invoices", len(invoices)).
		Str("sheet", sheetName).
		Msg("Invoices read successfully")

	return invoices, nil
}

// parseBankTransaction parses a single bank transaction row
func (dr *DataReader) parseBankTransaction(row []interface{}, rowNum int) (BankTransaction, error) {
	const op = "parseBankTransaction"

	// Parse date (column A)
	dateStr := getString(row, 0)
	date, err := dr.parseGermanDate(dateStr)
	if err != nil {
		return BankTransaction{}, fmt.Errorf("%s: invalid date '%s' in row %d: %w", op, dateStr, rowNum, err)
	}

	// Parse amount (column K - index 10)
	amountStr := getString(row, 10)
	amount, err := dr.parseGermanAmount(amountStr)
	if err != nil {
		return BankTransaction{}, fmt.Errorf("%s: invalid amount '%s' in row %d: %w", op, amountStr, rowNum, err)
	}

	transaction := BankTransaction{
		Date:         date,
		Type:         getString(row, 1),  // Transaktionstyp
		Description:  getString(row, 2),  // Beschreibung
		EREF:         getString(row, 3),  // EREF
		MREF:         getString(row, 4),  // MREF
		CRED:         getString(row, 5),  // CRED
		SVWZ:         getString(row, 6),  // SVWZ
		CounterParty: getString(row, 7),  // Empfänger/Absender
		BIC:          getString(row, 8),  // BIC
		IBAN:         getString(row, 9),  // IBAN
		Amount:       amount,             // Betrag
	}

	return transaction, nil
}

// parseInvoiceRow parses a single invoice row
func (dr *DataReader) parseInvoiceRow(row []interface{}, rowNum int, invoiceType string) (InvoiceRow, error) {
	const op = "parseInvoiceRow"

	// Parse date (column C)
	dateStr := getString(row, 2)
	date, err := dr.parseGermanDate(dateStr)
	if err != nil {
		dr.log.Warn().
			Str("date_str", dateStr).
			Int("row", rowNum).
			Msg("Invalid invoice date, using zero date")
		date = time.Time{} // Use zero date for invalid dates
	}

	// Parse amounts (columns E, F, G)
	netAmountStr := getString(row, 4)
	vatAmountStr := getString(row, 5)
	grossAmountStr := getString(row, 6)

	netAmount, err := dr.parseGermanAmount(netAmountStr)
	if err != nil {
		dr.log.Warn().
			Str("net_amount_str", netAmountStr).
			Int("row", rowNum).
			Msg("Invalid net amount, using 0")
		netAmount = 0
	}

	vatAmount, err := dr.parseGermanAmount(vatAmountStr)
	if err != nil {
		dr.log.Warn().
			Str("vat_amount_str", vatAmountStr).
			Int("row", rowNum).
			Msg("Invalid VAT amount, using 0")
		vatAmount = 0
	}

	grossAmount, err := dr.parseGermanAmount(grossAmountStr)
	if err != nil {
		return InvoiceRow{}, fmt.Errorf("%s: invalid gross amount '%s' in row %d: %w", op, grossAmountStr, rowNum, err)
	}

	// Get currency (column H), default to EUR
	currency := getString(row, 7)
	if currency == "" {
		currency = "EUR"
	}

	// Get counterparty (column D)
	counterParty := getString(row, 3)

	invoice := InvoiceRow{
		InvoiceNumber: getString(row, 1), // Rechnungsnr
		Date:          date,
		NetAmount:     netAmount,
		VATAmount:     vatAmount,
		GrossAmount:   grossAmount,
		Currency:      currency,
		Type:          invoiceType,
	}

	// Set vendor or customer based on type
	if invoiceType == "PAYABLE" {
		invoice.Vendor = counterParty
	} else {
		invoice.Customer = counterParty
	}

	return invoice, nil
}

// parseGermanDate parses German date format (DD.MM.YYYY)
func (dr *DataReader) parseGermanDate(dateStr string) (time.Time, error) {
	if dateStr == "" {
		return time.Time{}, fmt.Errorf("empty date string")
	}

	// Clean the date string
	cleaned := strings.TrimSpace(dateStr)

	// Try different German date formats
	formats := []string{
		"02.01.2006",     // DD.MM.YYYY
		"2.1.2006",       // D.M.YYYY
		"02.01.06",       // DD.MM.YY
		"2.1.06",         // D.M.YY
		"2006-01-02",     // ISO format (fallback)
	}

	for _, format := range formats {
		if date, err := time.Parse(format, cleaned); err == nil {
			return date, nil
		}
	}

	return time.Time{}, fmt.Errorf("unable to parse date: %s", dateStr)
}

// parseGermanAmount parses German amount format (comma as decimal, negative with minus)
func (dr *DataReader) parseGermanAmount(amountStr string) (float64, error) {
	if amountStr == "" {
		return 0, nil // Empty amount is treated as 0
	}

	// Clean the amount string
	cleaned := strings.TrimSpace(amountStr)

	// Handle negative amounts
	isNegative := strings.HasPrefix(cleaned, "-")
	if isNegative {
		cleaned = strings.TrimPrefix(cleaned, "-")
		cleaned = strings.TrimSpace(cleaned)
	}

	// Remove currency symbols and spaces
	cleaned = strings.ReplaceAll(cleaned, " ", "")
	cleaned = strings.ReplaceAll(cleaned, "€", "")
	cleaned = strings.ReplaceAll(cleaned, "EUR", "")
	cleaned = strings.ReplaceAll(cleaned, "USD", "")

	// Handle German number format
	// German format: thousands separator = dot, decimal separator = comma
	// Examples: "1.234,56" = 1234.56, "1234,56" = 1234.56, "1234" = 1234
	if strings.Contains(cleaned, ",") {
		// Check if we have both dot and comma (full German format: 1.234,56)
		if strings.Contains(cleaned, ".") && strings.Contains(cleaned, ",") {
			// Remove thousands separators (dots)
			cleaned = strings.ReplaceAll(cleaned, ".", "")
			// Replace decimal separator (comma) with dot
			cleaned = strings.ReplaceAll(cleaned, ",", ".")
		} else {
			// Only comma present - likely decimal separator
			parts := strings.Split(cleaned, ",")
			if len(parts) == 2 && len(parts[1]) <= 2 {
				// Replace comma with dot for decimal
				cleaned = strings.ReplaceAll(cleaned, ",", ".")
			}
		}
	}

	// Parse the cleaned amount
	amount, err := strconv.ParseFloat(cleaned, 64)
	if err != nil {
		return 0, fmt.Errorf("unable to parse amount: %s (cleaned: %s)", amountStr, cleaned)
	}

	// Apply negative sign if present
	if isNegative {
		amount = -amount
	}

	return amount, nil
}

// getString safely extracts a string value from a row slice
func getString(row []interface{}, index int) string {
	if index >= len(row) || row[index] == nil {
		return ""
	}
	return strings.TrimSpace(fmt.Sprintf("%v", row[index]))
}