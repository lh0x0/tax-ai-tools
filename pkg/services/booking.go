package services

import (
	"context"
	"io"
	"time"

	"tools/pkg/models"
)

// BookingService defines the interface for generating DATEV accounting entries
type BookingService interface {
	// GenerateBooking creates a DATEV booking entry from a completed invoice
	GenerateBooking(ctx context.Context, invoice *models.Invoice) (*DATEVBooking, error)

	// GenerateBookingFromPDF processes PDF, extracts invoice data, and generates booking
	GenerateBookingFromPDF(ctx context.Context, pdfData io.Reader) (*DATEVBooking, *models.Invoice, error)
}

// DATEVBooking represents a complete DATEV accounting entry
type DATEVBooking struct {
	// Core booking information
	BookingText     string  `json:"booking_text"`     // Buchungstext (max 60 chars)
	DebitAccount    string  `json:"debit_account"`    // Sollkonto (SKR03)
	CreditAccount   string  `json:"credit_account"`   // Habenkonto (SKR03)
	Amount          float64 `json:"amount"`           // Betrag in EUR
	TaxKey          string  `json:"tax_key"`          // Steuerschlüssel
	CostCenter      string  `json:"cost_center"`      // Kostenstelle (optional)
	BookingDate     time.Time `json:"booking_date"`   // Buchungsdatum
	DocumentNumber  string  `json:"document_number"`  // Belegnummer
	AccountingPeriod string `json:"accounting_period"` // Buchungsperiode (MMYYYY)
	
	// Additional information
	Explanation     string `json:"explanation"`      // Erläuterung der Buchung
	
	// Account descriptions for display
	DebitAccountName  string `json:"debit_account_name"`  // Name des Sollkontos
	CreditAccountName string `json:"credit_account_name"` // Name des Habenkontos
	TaxKeyDescription string `json:"tax_key_description"` // Beschreibung des Steuerschlüssels
	
	// Metadata
	GeneratedAt   time.Time `json:"generated_at"`   // Timestamp of generation
	ContenrahmenType string `json:"kontenrahmen_type"` // SKR03 or SKR04
}