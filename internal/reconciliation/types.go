package reconciliation

import (
	"time"
)

// BankTransaction represents a bank transaction from the Bank sheet
type BankTransaction struct {
	Date         time.Time // Datum - column A
	Type         string    // Transaktionstyp - column B
	Description  string    // Beschreibung - column C
	EREF         string    // End-to-End Reference - column D
	MREF         string    // Mandate Reference - column E
	CRED         string    // Creditor ID - column F
	SVWZ         string    // Verwendungszweck - column G
	CounterParty string    // Empfänger/Absender - column H
	BIC          string    // Bank Identifier Code - column I
	IBAN         string    // International Bank Account Number - column J
	Amount       float64   // Betrag (negative for outgoing, positive for incoming) - column K
}

// InvoiceRow represents an invoice from Kreditoren or Debitoren sheets
type InvoiceRow struct {
	InvoiceNumber string    // Rechnungsnr - column B
	Date          time.Time // Datum - column C
	Vendor        string    // Lieferant (for Kreditoren) - column D
	Customer      string    // Kunde (for Debitoren) - column D
	NetAmount     float64   // Netto - column E
	VATAmount     float64   // MwSt - column F
	GrossAmount   float64   // Brutto - column G
	Currency      string    // Währung - column H
	Type          string    // "PAYABLE" for Kreditoren, "RECEIVABLE" for Debitoren
}

// ReconciliationData holds all data read from Google Sheets
type ReconciliationData struct {
	BankTransactions    []BankTransaction
	PayableInvoices     []InvoiceRow
	ReceivableInvoices  []InvoiceRow
}

// GetCounterParty returns the appropriate counterparty name based on invoice type
func (ir *InvoiceRow) GetCounterParty() string {
	if ir.Type == "PAYABLE" {
		return ir.Vendor
	}
	return ir.Customer
}

// IsOutgoing returns true if this is an outgoing transaction (negative amount)
func (bt *BankTransaction) IsOutgoing() bool {
	return bt.Amount < 0
}

// IsIncoming returns true if this is an incoming transaction (positive amount)
func (bt *BankTransaction) IsIncoming() bool {
	return bt.Amount > 0
}

// AbsAmount returns the absolute value of the transaction amount
func (bt *BankTransaction) AbsAmount() float64 {
	if bt.Amount < 0 {
		return -bt.Amount
	}
	return bt.Amount
}