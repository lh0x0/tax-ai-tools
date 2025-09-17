package services

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"strings"
	"time"

	"github.com/rs/zerolog"
	"github.com/sashabaranov/go-openai"
	"tools/internal/logger"
	"tools/internal/reconciliation"
)

// ReconciliationService defines the interface for invoice-transaction reconciliation
type ReconciliationService interface {
	ReconcileAll(ctx context.Context, invoices []reconciliation.InvoiceRow, transactions []reconciliation.BankTransaction, cutoffDate time.Time) (*ReconciliationResult, error)
}

// ReconciliationResult contains the results of a reconciliation process
type ReconciliationResult struct {
	MatchedInvoices        map[string]string                    // Invoice ID -> Transaction ID
	UnmatchedInvoices      []reconciliation.InvoiceRow          // Invoices that couldn't be matched
	UnmatchedTransactions  []reconciliation.BankTransaction     // Transactions that couldn't be matched
	TotalInvoices          int                                  // Total number of invoices processed
	TotalTransactions      int                                  // Total number of transactions processed
	MatchedCount           int                                  // Number of successful matches
	ProcessingTime         time.Duration                        // Time taken for reconciliation
}

// MatchResult represents the result of matching a single invoice
type MatchResult struct {
	Matched          bool    `json:"matched"`
	TransactionIndex int     `json:"transaction_index"`
	Confidence       float64 `json:"confidence"`
	Reason           string  `json:"reason"`
}

// ChatGPTReconciliationService implements ReconciliationService using ChatGPT for matching
type ChatGPTReconciliationService struct {
	openaiClient *openai.Client
	log          zerolog.Logger
}

// NewChatGPTReconciliationService creates a new ChatGPT-based reconciliation service
func NewChatGPTReconciliationService(openaiClient *openai.Client) *ChatGPTReconciliationService {
	return &ChatGPTReconciliationService{
		openaiClient: openaiClient,
		log:          logger.WithComponent("reconciliation-chatgpt"),
	}
}

// ReconcileAll processes all invoices sequentially, matching them with bank transactions
func (s *ChatGPTReconciliationService) ReconcileAll(ctx context.Context, invoices []reconciliation.InvoiceRow, transactions []reconciliation.BankTransaction, cutoffDate time.Time) (*ReconciliationResult, error) {
	const op = "ReconcileAll"
	startTime := time.Now()

	s.log.Info().
		Int("invoices", len(invoices)).
		Int("transactions", len(transactions)).
		Str("cutoff_date", cutoffDate.Format("2006-01-02")).
		Msg("Starting ChatGPT-based reconciliation")

	result := &ReconciliationResult{
		MatchedInvoices:       make(map[string]string),
		UnmatchedInvoices:     []reconciliation.InvoiceRow{},
		UnmatchedTransactions: []reconciliation.BankTransaction{},
		TotalInvoices:         len(invoices),
		TotalTransactions:     len(transactions),
		MatchedCount:          0,
	}

	// Filter transactions by cutoff date
	filteredTransactions := s.filterTransactionsByCutoff(transactions, cutoffDate)
	s.log.Info().
		Int("original_transactions", len(transactions)).
		Int("filtered_transactions", len(filteredTransactions)).
		Msg("Applied cutoff date filter")

	// Track which transactions have been matched to avoid double-matching
	usedTransactionIndices := make(map[int]bool)

	// Process each invoice individually
	for i, invoice := range invoices {
		s.log.Debug().
			Int("invoice_index", i).
			Str("invoice_number", invoice.InvoiceNumber).
			Str("counterparty", invoice.GetCounterParty()).
			Float64("gross_amount", invoice.GrossAmount).
			Msg("Processing invoice")

		// Find candidate transactions for this invoice
		candidates := s.findCandidateTransactions(invoice, filteredTransactions, usedTransactionIndices)
		
		if len(candidates) == 0 {
			s.log.Debug().
				Str("invoice_number", invoice.InvoiceNumber).
				Msg("No candidate transactions found for invoice")
			result.UnmatchedInvoices = append(result.UnmatchedInvoices, invoice)
			continue
		}

		s.log.Debug().
			Int("candidates", len(candidates)).
			Str("invoice_number", invoice.InvoiceNumber).
			Msg("Found candidate transactions")

		// Use ChatGPT to match this invoice with candidates
		matchResult, err := s.matchInvoiceWithChatGPT(ctx, invoice, candidates)
		if err != nil {
			s.log.Warn().
				Err(err).
				Str("invoice_number", invoice.InvoiceNumber).
				Msg("Failed to get ChatGPT match result, treating as unmatched")
			result.UnmatchedInvoices = append(result.UnmatchedInvoices, invoice)
			continue
		}

		if matchResult.Matched && matchResult.TransactionIndex >= 0 && matchResult.TransactionIndex < len(candidates) {
			// Get the actual transaction from the candidates
			matchedTransaction := candidates[matchResult.TransactionIndex].Transaction
			actualIndex := candidates[matchResult.TransactionIndex].OriginalIndex

			// Create unique IDs for tracking
			invoiceID := s.generateInvoiceID(invoice)
			transactionID := s.generateTransactionID(matchedTransaction)

			result.MatchedInvoices[invoiceID] = transactionID
			result.MatchedCount++
			usedTransactionIndices[actualIndex] = true

			s.log.Info().
				Str("invoice_number", invoice.InvoiceNumber).
				Str("counterparty", invoice.GetCounterParty()).
				Float64("invoice_amount", invoice.GrossAmount).
				Float64("transaction_amount", matchedTransaction.Amount).
				Float64("confidence", matchResult.Confidence).
				Str("reason", matchResult.Reason).
				Msg("Successfully matched invoice with transaction")
		} else {
			result.UnmatchedInvoices = append(result.UnmatchedInvoices, invoice)
			s.log.Debug().
				Str("invoice_number", invoice.InvoiceNumber).
				Bool("matched", matchResult.Matched).
				Msg("Invoice not matched by ChatGPT")
		}
	}

	// Add unmatched transactions to result
	for i, transaction := range filteredTransactions {
		if !usedTransactionIndices[i] {
			result.UnmatchedTransactions = append(result.UnmatchedTransactions, transaction)
		}
	}

	result.ProcessingTime = time.Since(startTime)

	s.log.Info().
		Int("total_invoices", result.TotalInvoices).
		Int("matched_count", result.MatchedCount).
		Int("unmatched_invoices", len(result.UnmatchedInvoices)).
		Int("unmatched_transactions", len(result.UnmatchedTransactions)).
		Dur("processing_time", result.ProcessingTime).
		Msg("Reconciliation completed")

	return result, nil
}

// TransactionCandidate represents a transaction candidate with its original index
type TransactionCandidate struct {
	Transaction   reconciliation.BankTransaction
	OriginalIndex int
}

// filterTransactionsByCutoff filters transactions to only include those before the cutoff date
func (s *ChatGPTReconciliationService) filterTransactionsByCutoff(transactions []reconciliation.BankTransaction, cutoffDate time.Time) []reconciliation.BankTransaction {
	var filtered []reconciliation.BankTransaction
	for _, transaction := range transactions {
		if transaction.Date.Before(cutoffDate) || transaction.Date.Equal(cutoffDate) {
			filtered = append(filtered, transaction)
		}
	}
	return filtered
}

// findCandidateTransactions finds transactions that could potentially match an invoice based on amount
func (s *ChatGPTReconciliationService) findCandidateTransactions(invoice reconciliation.InvoiceRow, transactions []reconciliation.BankTransaction, usedIndices map[int]bool) []TransactionCandidate {
	var candidates []TransactionCandidate
	
	// Convert invoice amount to cents for precise comparison
	invoiceAmountCents := int64(math.Round(invoice.GrossAmount * 100))
	tolerance := int64(math.Round(math.Abs(invoice.GrossAmount) * 0.01 * 100)) // 1% tolerance in cents
	
	s.log.Debug().
		Float64("invoice_amount", invoice.GrossAmount).
		Int64("invoice_amount_cents", invoiceAmountCents).
		Int64("tolerance_cents", tolerance).
		Str("invoice_type", invoice.Type).
		Msg("Searching for candidate transactions")

	for i, transaction := range transactions {
		// Skip already matched transactions
		if usedIndices[i] {
			continue
		}
		
		// Convert transaction amount to cents
		transactionAmountCents := int64(math.Round(transaction.Amount * 100))
		
		// Determine expected transaction direction based on invoice type
		var isCandidate bool
		if invoice.Type == "PAYABLE" {
			// For payables, we expect negative bank amounts (outgoing payments)
			// Match absolute values since we're paying out
			expectedAmountCents := -invoiceAmountCents
			if transactionAmountCents < 0 {
				diff := int64(math.Abs(float64(transactionAmountCents - expectedAmountCents)))
				isCandidate = diff <= tolerance
			}
		} else if invoice.Type == "RECEIVABLE" {
			// For receivables, we expect positive bank amounts (incoming payments)
			expectedAmountCents := invoiceAmountCents
			if transactionAmountCents > 0 {
				diff := int64(math.Abs(float64(transactionAmountCents - expectedAmountCents)))
				isCandidate = diff <= tolerance
			}
		}
		
		if isCandidate {
			candidates = append(candidates, TransactionCandidate{
				Transaction:   transaction,
				OriginalIndex: i,
			})
			
			s.log.Debug().
				Float64("transaction_amount", transaction.Amount).
				Str("transaction_counterparty", transaction.CounterParty).
				Str("transaction_description", transaction.Description).
				Time("transaction_date", transaction.Date).
				Msg("Added candidate transaction")
		}
	}
	
	return candidates
}

// matchInvoiceWithChatGPT uses ChatGPT to determine the best match for an invoice
func (s *ChatGPTReconciliationService) matchInvoiceWithChatGPT(ctx context.Context, invoice reconciliation.InvoiceRow, candidates []TransactionCandidate) (*MatchResult, error) {
	const op = "matchInvoiceWithChatGPT"
	
	if len(candidates) == 0 {
		return &MatchResult{Matched: false}, nil
	}
	
	// Prepare invoice data for prompt
	invoiceJSON, err := json.MarshalIndent(map[string]interface{}{
		"rechnungsnummer": invoice.InvoiceNumber,
		"datum":          invoice.Date.Format("02.01.2006"),
		"lieferant_kunde": invoice.GetCounterParty(),
		"netto":          invoice.NetAmount,
		"mwst":           invoice.VATAmount,
		"brutto":         invoice.GrossAmount,
		"waehrung":       invoice.Currency,
		"typ":            invoice.Type,
	}, "", "  ")
	if err != nil {
		return nil, fmt.Errorf("%s: failed to marshal invoice JSON: %w", op, err)
	}
	
	// Prepare candidates data for prompt
	var candidatesData []map[string]interface{}
	for _, candidate := range candidates {
		candidatesData = append(candidatesData, map[string]interface{}{
			"datum":               candidate.Transaction.Date.Format("02.01.2006"),
			"transaktionstyp":     candidate.Transaction.Type,
			"beschreibung":        candidate.Transaction.Description,
			"empfaenger_absender": candidate.Transaction.CounterParty,
			"betrag":              candidate.Transaction.Amount,
			"verwendungszweck":    candidate.Transaction.SVWZ,
			"eref":                candidate.Transaction.EREF,
			"mref":                candidate.Transaction.MREF,
			"iban":                candidate.Transaction.IBAN,
			"bic":                 candidate.Transaction.BIC,
		})
	}
	
	candidatesJSON, err := json.MarshalIndent(candidatesData, "", "  ")
	if err != nil {
		return nil, fmt.Errorf("%s: failed to marshal candidates JSON: %w", op, err)
	}
	
	// Create the German prompt for ChatGPT
	prompt := fmt.Sprintf(`Prüfe ob eine dieser Banktransaktionen zur Rechnung passt:

RECHNUNG:
%s

MÖGLICHE TRANSAKTIONEN:
%s

Analysiere folgende Kriterien:
1. Stimmt der Betrag überein (mit kleiner Toleranz für Rundungsfehler)?
2. Passt das Datum zusammen (Rechnung vor oder am Tag der Transaktion)?
3. Stimmt der Empfänger/Absender mit dem Lieferanten/Kunden überein?
4. Gibt der Verwendungszweck Hinweise auf die Rechnung?

Antworte nur mit JSON im folgenden Format:
{
  "matched": true/false,
  "transaction_index": 0,
  "confidence": 0.95,
  "reason": "Betrag und Lieferant stimmen überein"
}

Wenn keine Transaktion passt, setze "matched": false und "transaction_index": -1.`, string(invoiceJSON), string(candidatesJSON))

	s.log.Debug().
		Str("invoice_number", invoice.InvoiceNumber).
		Int("candidates_count", len(candidates)).
		Msg("Sending invoice matching request to ChatGPT")

	// Send request to ChatGPT
	resp, err := s.openaiClient.CreateChatCompletion(ctx, openai.ChatCompletionRequest{
		Model: openai.GPT4oMini,
		Messages: []openai.ChatCompletionMessage{
			{
				Role:    openai.ChatMessageRoleUser,
				Content: prompt,
			},
		},
		Temperature: 0.1,
		MaxTokens:   1000,
	})
	if err != nil {
		return nil, fmt.Errorf("%s: ChatGPT request failed: %w", op, err)
	}
	
	if len(resp.Choices) == 0 {
		return nil, fmt.Errorf("%s: no response choices from ChatGPT", op)
	}
	
	response := resp.Choices[0].Message.Content

	// Parse the JSON response
	var matchResult MatchResult
	cleanedResponse := strings.TrimSpace(response)
	
	// Handle case where ChatGPT returns response wrapped in markdown code blocks
	if strings.HasPrefix(cleanedResponse, "```json") {
		cleanedResponse = strings.TrimPrefix(cleanedResponse, "```json")
		cleanedResponse = strings.TrimSuffix(cleanedResponse, "```")
		cleanedResponse = strings.TrimSpace(cleanedResponse)
	} else if strings.HasPrefix(cleanedResponse, "```") {
		cleanedResponse = strings.TrimPrefix(cleanedResponse, "```")
		cleanedResponse = strings.TrimSuffix(cleanedResponse, "```")
		cleanedResponse = strings.TrimSpace(cleanedResponse)
	}
	
	if err := json.Unmarshal([]byte(cleanedResponse), &matchResult); err != nil {
		s.log.Warn().
			Err(err).
			Str("response", cleanedResponse).
			Str("invoice_number", invoice.InvoiceNumber).
			Msg("Failed to parse ChatGPT response as JSON")
		return &MatchResult{Matched: false}, nil
	}
	
	s.log.Debug().
		Str("invoice_number", invoice.InvoiceNumber).
		Bool("matched", matchResult.Matched).
		Int("transaction_index", matchResult.TransactionIndex).
		Float64("confidence", matchResult.Confidence).
		Str("reason", matchResult.Reason).
		Msg("Received ChatGPT matching result")
	
	return &matchResult, nil
}

// generateInvoiceID creates a unique identifier for an invoice
func (s *ChatGPTReconciliationService) generateInvoiceID(invoice reconciliation.InvoiceRow) string {
	if invoice.InvoiceNumber != "" {
		return fmt.Sprintf("%s_%s_%s", invoice.Type, invoice.InvoiceNumber, invoice.Date.Format("20060102"))
	}
	return fmt.Sprintf("%s_%s_%s_%.2f", invoice.Type, invoice.GetCounterParty(), invoice.Date.Format("20060102"), invoice.GrossAmount)
}

// generateTransactionID creates a unique identifier for a transaction
func (s *ChatGPTReconciliationService) generateTransactionID(transaction reconciliation.BankTransaction) string {
	return fmt.Sprintf("TXN_%s_%.2f_%s", transaction.Date.Format("20060102"), transaction.Amount, transaction.CounterParty)
}