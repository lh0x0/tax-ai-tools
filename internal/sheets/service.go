package sheets

import (
	"context"
	"fmt"
	"os"
	"regexp"
	"strings"
	"time"

	"github.com/rs/zerolog"
	"golang.org/x/oauth2/google"
	"google.golang.org/api/option"
	"google.golang.org/api/sheets/v4"
	"tools/internal/logger"
	"tools/pkg/models"
	"tools/pkg/services"
)

// Service handles Google Sheets operations
type Service struct {
	sheetsService *sheets.Service
	spreadsheetID string
	log           zerolog.Logger
}

// BatchRow represents a row to be written to the sheet
type BatchRow struct {
	Filename         string
	InvoiceNumber    string
	Date             string
	VendorCustomer   string
	NetAmount        float64
	VATAmount        float64
	GrossAmount      float64
	Currency         string
	DebitAccount     string
	CreditAccount    string
	TaxKey           string
	BookingText      string
	CostCenter       string
	Description      string
	DueDate          string
	Status           string
	ProcessedAt      string
}

// NewSheetsService creates a new Google Sheets service
func NewSheetsService(ctx context.Context, sheetURL string) (*Service, error) {
	const op = "NewSheetsService"

	log := logger.WithComponent("sheets")

	// Extract spreadsheet ID from URL
	spreadsheetID, err := extractSpreadsheetID(sheetURL)
	if err != nil {
		return nil, fmt.Errorf("%s: failed to extract spreadsheet ID: %w", op, err)
	}

	log.Debug().Str("spreadsheet_id", spreadsheetID).Msg("Extracted spreadsheet ID")

	// Get Google credentials
	var creds []byte
	if credsFile := os.Getenv("GOOGLE_APPLICATION_CREDENTIALS"); credsFile != "" {
		creds, err = os.ReadFile(credsFile)
		if err != nil {
			return nil, fmt.Errorf("%s: failed to read credentials file: %w", op, err)
		}
	} else if credsJSON := os.Getenv("GOOGLE_CREDENTIALS"); credsJSON != "" {
		creds = []byte(credsJSON)
	} else {
		return nil, fmt.Errorf("%s: neither GOOGLE_APPLICATION_CREDENTIALS nor GOOGLE_CREDENTIALS is set", op)
	}

	// Create Google Sheets service
	config, err := google.JWTConfigFromJSON(creds, sheets.SpreadsheetsScope)
	if err != nil {
		return nil, fmt.Errorf("%s: failed to parse credentials: %w", op, err)
	}

	client := config.Client(ctx)
	sheetsService, err := sheets.NewService(ctx, option.WithHTTPClient(client))
	if err != nil {
		return nil, fmt.Errorf("%s: failed to create sheets service: %w", op, err)
	}

	return &Service{
		sheetsService: sheetsService,
		spreadsheetID: spreadsheetID,
		log:           log,
	}, nil
}

// extractSpreadsheetID extracts the spreadsheet ID from a Google Sheets URL
func extractSpreadsheetID(url string) (string, error) {
	// Pattern for Google Sheets URLs
	re := regexp.MustCompile(`/spreadsheets/d/([a-zA-Z0-9-_]+)`)
	matches := re.FindStringSubmatch(url)
	
	if len(matches) < 2 {
		return "", fmt.Errorf("invalid Google Sheets URL format")
	}
	
	return matches[1], nil
}

// WriteBatchResults writes batch processing results to the specified sheet
func (s *Service) WriteBatchResults(ctx context.Context, results []BatchResult, sheetName string) error {
	const op = "WriteBatchResults"

	s.log.Info().
		Str("sheet", sheetName).
		Int("rows", len(results)).
		Msg("Writing batch results to Google Sheet")

	// Convert results to rows
	rows, err := s.convertResultsToRows(results)
	if err != nil {
		return fmt.Errorf("%s: failed to convert results to rows: %w", op, err)
	}

	// Ensure sheet exists and get headers
	err = s.ensureSheetWithHeaders(ctx, sheetName)
	if err != nil {
		return fmt.Errorf("%s: failed to ensure sheet exists: %w", op, err)
	}

	// Prepare values for batch update
	var values [][]interface{}
	for _, row := range rows {
		values = append(values, s.rowToValues(row))
	}

	// Write to sheet
	valueRange := &sheets.ValueRange{
		Values: values,
	}

	_, err = s.sheetsService.Spreadsheets.Values.Append(
		s.spreadsheetID,
		sheetName+"!A:Q", // A to Q covers all our columns
		valueRange,
	).ValueInputOption("USER_ENTERED").Context(ctx).Do()

	if err != nil {
		return fmt.Errorf("%s: failed to append values to sheet: %w", op, err)
	}

	s.log.Info().
		Int("rows_written", len(values)).
		Msg("Successfully wrote batch results to Google Sheet")

	return nil
}

// BatchResult represents the result of processing a single PDF (imported from cmd package concept)
type BatchResult struct {
	Filename string
	Invoice  *models.Invoice
	Booking  *services.DATEVBooking
	Error    error
	Status   string
}

// convertResultsToRows converts BatchResult slice to BatchRow slice
func (s *Service) convertResultsToRows(results []BatchResult) ([]BatchRow, error) {
	var rows []BatchRow
	processedAt := time.Now().Format("02.01.2006 15:04:05")

	for _, result := range results {
		row := BatchRow{
			Filename:    result.Filename,
			Status:      result.Status,
			ProcessedAt: processedAt,
		}

		// Handle error cases
		if result.Error != nil {
			row.Description = fmt.Sprintf("Fehler: %s", result.Error.Error())
			rows = append(rows, row)
			continue
		}

		// Fill invoice data
		if result.Invoice != nil {
			row.InvoiceNumber = result.Invoice.InvoiceNumber
			row.Currency = s.normalizeCurrency(result.Invoice.Currency)
			row.NetAmount = float64(result.Invoice.NetAmount) / 100
			row.VATAmount = float64(result.Invoice.VATAmount) / 100
			row.GrossAmount = float64(result.Invoice.GrossAmount) / 100
			row.Description = result.Invoice.AccountingSummary
			
			if result.Invoice.Type == "PAYABLE" {
				row.VendorCustomer = result.Invoice.Vendor
			} else {
				row.VendorCustomer = result.Invoice.Customer
			}

			if !result.Invoice.IssueDate.IsZero() {
				row.Date = result.Invoice.IssueDate.Format("02.01.2006")
			}
			if !result.Invoice.DueDate.IsZero() {
				row.DueDate = result.Invoice.DueDate.Format("02.01.2006")
			}
		}

		// Fill booking data
		if result.Booking != nil {
			row.DebitAccount = result.Booking.DebitAccount
			row.CreditAccount = result.Booking.CreditAccount
			row.TaxKey = result.Booking.TaxKey
			row.BookingText = result.Booking.BookingText
			row.CostCenter = result.Booking.CostCenter
		}

		rows = append(rows, row)
	}

	return rows, nil
}

// rowToValues converts BatchRow to interface{} slice for Google Sheets
func (s *Service) rowToValues(row BatchRow) []interface{} {
	return []interface{}{
		row.Filename,         // A: Datei
		row.InvoiceNumber,    // B: Rechnungsnr
		row.Date,             // C: Datum
		row.VendorCustomer,   // D: Lieferant/Kunde
		row.NetAmount,        // E: Netto
		row.VATAmount,        // F: MwSt
		row.GrossAmount,      // G: Brutto
		row.Currency,         // H: Währung
		row.DebitAccount,     // I: Sollkonto
		row.CreditAccount,    // J: Habenkonto
		row.TaxKey,           // K: Steuerschlüssel
		row.BookingText,      // L: Buchungstext
		row.CostCenter,       // M: Kostenstelle
		row.Description,      // N: Beschreibung
		row.DueDate,          // O: Fälligkeit
		row.Status,           // P: Status
		row.ProcessedAt,      // Q: Verarbeitet
	}
}

// ensureSheetWithHeaders ensures the sheet exists and has proper headers
func (s *Service) ensureSheetWithHeaders(ctx context.Context, sheetName string) error {
	const op = "ensureSheetWithHeaders"

	// Check if sheet exists
	spreadsheet, err := s.sheetsService.Spreadsheets.Get(s.spreadsheetID).Context(ctx).Do()
	if err != nil {
		return fmt.Errorf("%s: failed to get spreadsheet: %w", op, err)
	}

	// Look for existing sheet
	var sheetExists bool
	var sheetID int64
	for _, sheet := range spreadsheet.Sheets {
		if sheet.Properties.Title == sheetName {
			sheetExists = true
			sheetID = sheet.Properties.SheetId
			break
		}
	}

	// Create sheet if it doesn't exist
	if !sheetExists {
		s.log.Info().Str("sheet", sheetName).Msg("Creating new sheet")
		
		addSheetReq := &sheets.AddSheetRequest{
			Properties: &sheets.SheetProperties{
				Title: sheetName,
			},
		}

		batchUpdateReq := &sheets.BatchUpdateSpreadsheetRequest{
			Requests: []*sheets.Request{
				{AddSheet: addSheetReq},
			},
		}

		resp, err := s.sheetsService.Spreadsheets.BatchUpdate(s.spreadsheetID, batchUpdateReq).Context(ctx).Do()
		if err != nil {
			return fmt.Errorf("%s: failed to create sheet: %w", op, err)
		}
		
		sheetID = resp.Replies[0].AddSheet.Properties.SheetId
	}

	// Check if headers exist
	headerRange := fmt.Sprintf("%s!A1:Q1", sheetName)
	resp, err := s.sheetsService.Spreadsheets.Values.Get(s.spreadsheetID, headerRange).Context(ctx).Do()
	if err != nil {
		return fmt.Errorf("%s: failed to get headers: %w", op, err)
	}

	// Add headers if they don't exist or are empty
	if len(resp.Values) == 0 || len(resp.Values[0]) == 0 {
		s.log.Info().Str("sheet", sheetName).Msg("Adding headers to sheet")
		
		headers := [][]interface{}{
			{
				"Datei", "Rechnungsnr", "Datum", "Lieferant/Kunde", "Netto", 
				"MwSt", "Brutto", "Währung", "Sollkonto", "Habenkonto", 
				"Steuerschlüssel", "Buchungstext", "Kostenstelle", "Beschreibung", 
				"Fälligkeit", "Status", "Verarbeitet",
			},
		}

		valueRange := &sheets.ValueRange{Values: headers}
		_, err = s.sheetsService.Spreadsheets.Values.Update(
			s.spreadsheetID,
			headerRange,
			valueRange,
		).ValueInputOption("RAW").Context(ctx).Do()

		if err != nil {
			return fmt.Errorf("%s: failed to add headers: %w", op, err)
		}

		// Format headers (bold)
		err = s.formatHeaders(ctx, sheetID, sheetName)
		if err != nil {
			s.log.Warn().Err(err).Msg("Failed to format headers, continuing anyway")
		}
	}

	return nil
}

// formatHeaders makes the header row bold and applies basic formatting
func (s *Service) formatHeaders(ctx context.Context, sheetID int64, sheetName string) error {
	const op = "formatHeaders"

	requests := []*sheets.Request{
		// Make header row bold
		{
			RepeatCell: &sheets.RepeatCellRequest{
				Range: &sheets.GridRange{
					SheetId:       sheetID,
					StartRowIndex: 0,
					EndRowIndex:   1,
					StartColumnIndex: 0,
					EndColumnIndex: 17, // A to Q
				},
				Cell: &sheets.CellData{
					UserEnteredFormat: &sheets.CellFormat{
						TextFormat: &sheets.TextFormat{
							Bold: true,
						},
						BackgroundColor: &sheets.Color{
							Red:   0.9,
							Green: 0.9,
							Blue:  0.9,
						},
					},
				},
				Fields: "userEnteredFormat(textFormat,backgroundColor)",
			},
		},
		// Auto-resize columns
		{
			AutoResizeDimensions: &sheets.AutoResizeDimensionsRequest{
				Dimensions: &sheets.DimensionRange{
					SheetId:    sheetID,
					Dimension:  "COLUMNS",
					StartIndex: 0,
					EndIndex:   17,
				},
			},
		},
	}

	batchUpdateReq := &sheets.BatchUpdateSpreadsheetRequest{Requests: requests}
	_, err := s.sheetsService.Spreadsheets.BatchUpdate(s.spreadsheetID, batchUpdateReq).Context(ctx).Do()
	if err != nil {
		return fmt.Errorf("%s: failed to format headers: %w", op, err)
	}

	return nil
}

// normalizeCurrency standardizes currency codes to consistent format
func (s *Service) normalizeCurrency(currency string) string {
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

// ReadRange reads values from a specified range in the spreadsheet
func (s *Service) ReadRange(ctx context.Context, rangeSpec string) ([][]interface{}, error) {
	const op = "ReadRange"

	s.log.Debug().
		Str("range", rangeSpec).
		Msg("Reading range from spreadsheet")

	resp, err := s.sheetsService.Spreadsheets.Values.Get(s.spreadsheetID, rangeSpec).Context(ctx).Do()
	if err != nil {
		return nil, fmt.Errorf("%s: failed to read range %s: %w", op, rangeSpec, err)
	}

	s.log.Debug().
		Int("rows", len(resp.Values)).
		Str("range", rangeSpec).
		Msg("Successfully read range from spreadsheet")

	return resp.Values, nil
}