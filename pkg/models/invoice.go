package models

import "time"

type Invoice struct {
	// Core identifiers
	ID            string // Unique invoice identifier
	InvoiceNumber string // Human-readable invoice number
	Type          string // "RECEIVABLE" (customer invoice) or "PAYABLE" (supplier invoice)

	// Parties
	Vendor   string // Vendor/supplier name (for payable) or your company name (for receivable)
	Customer string // Customer name (for receivable) or your company name (for payable)

	// Dates
	IssueDate   time.Time  // Date invoice was issued
	DueDate     time.Time  // Payment due date
	PaymentDate *time.Time // Actual payment date (nil if unpaid)

	// Amounts (store as cents/smallest currency unit to avoid float issues)
	NetAmount   int64  // Amount before tax
	VATAmount   int64  // VAT/tax amount
	GrossAmount int64  // Total amount (net + VAT)
	Currency    string // Currency code (EUR, USD, etc.)

	// Status
	IsPaid bool // Payment status flag

	// Optional metadata
	Reference        string    // External reference number
	Description      string    // Brief description/notes
	AccountingSummary string   // German accounting summary describing goods/services and suggested categorization
	CreatedAt        time.Time // Record creation timestamp
	UpdatedAt        time.Time // Last update timestamp
}