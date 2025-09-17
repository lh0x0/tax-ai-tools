package services

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"sort"
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
			s.log.Info().
				Str("invoice_number", invoice.InvoiceNumber).
				Str("counterparty", invoice.GetCounterParty()).
				Float64("amount", invoice.GrossAmount).
				Msg("Processing invoice: No candidate transactions found")
			result.UnmatchedInvoices = append(result.UnmatchedInvoices, invoice)
			continue
		}

		s.log.Info().
			Str("invoice_number", invoice.InvoiceNumber).
			Str("counterparty", invoice.GetCounterParty()).
			Float64("amount", invoice.GrossAmount).
			Int("candidates", len(candidates)).
			Msgf("Processing invoice %s: Found %d candidate transactions", invoice.InvoiceNumber, len(candidates))

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
				Time("transaction_date", matchedTransaction.Date).
				Float64("confidence", matchResult.Confidence).
				Str("reason", matchResult.Reason).
				Msgf("ChatGPT matched invoice %s with transaction from %s (confidence: %.2f)", 
					invoice.InvoiceNumber, 
					matchedTransaction.Date.Format("02.01.2006"), 
					matchResult.Confidence)
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

// TransactionCandidate represents a transaction candidate with its original index and scoring
type TransactionCandidate struct {
	Transaction   reconciliation.BankTransaction
	OriginalIndex int
	Score         float64 // Higher score = better match (amount precision + date proximity)
	DaysDiff      int     // Days difference between invoice and transaction
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

// findCandidateTransactions finds transactions that could potentially match an invoice based on amount and date
func (s *ChatGPTReconciliationService) findCandidateTransactions(invoice reconciliation.InvoiceRow, transactions []reconciliation.BankTransaction, usedIndices map[int]bool) []TransactionCandidate {
	var candidates []TransactionCandidate
	
	// Convert invoice amount to cents for precise comparison (German format: 1.234,56 -> 123456 cents)
	invoiceAmountCents := int64(math.Round(invoice.GrossAmount * 100))
	tolerance := int64(math.Round(math.Abs(invoice.GrossAmount) * 0.01 * 100)) // 1% tolerance in cents
	
	s.log.Debug().
		Float64("invoice_amount", invoice.GrossAmount).
		Int64("invoice_amount_cents", invoiceAmountCents).
		Int64("tolerance_cents", tolerance).
		Str("invoice_type", invoice.Type).
		Time("invoice_date", invoice.Date).
		Msg("Searching for candidate transactions with intelligent filtering")

	for i, transaction := range transactions {
		// Skip already matched transactions to avoid double-matching
		if usedIndices[i] {
			continue
		}
		
		// Convert transaction amount to cents for precise comparison
		transactionAmountCents := int64(math.Round(transaction.Amount * 100))
		
		// Determine expected transaction direction based on invoice type
		var isAmountMatch bool
		var amountDiff int64
		
		if invoice.Type == "PAYABLE" {
			// For payables, we expect negative bank amounts (outgoing payments)
			// Consider that payables are negative in bank
			expectedAmountCents := -invoiceAmountCents
			if transactionAmountCents < 0 {
				amountDiff = int64(math.Abs(float64(transactionAmountCents - expectedAmountCents)))
				isAmountMatch = amountDiff <= tolerance
			}
		} else if invoice.Type == "RECEIVABLE" {
			// For receivables, we expect positive bank amounts (incoming payments)
			expectedAmountCents := invoiceAmountCents
			if transactionAmountCents > 0 {
				amountDiff = int64(math.Abs(float64(transactionAmountCents - expectedAmountCents)))
				isAmountMatch = amountDiff <= tolerance
			}
		}
		
		if isAmountMatch {
			// Calculate date difference (prioritize transactions within 30 days of invoice date)
			daysDiff := int(math.Abs(transaction.Date.Sub(invoice.Date).Hours() / 24))
			
			// Calculate score: amount precision (90%) + date proximity (10%)
			amountPrecision := 1.0 - (float64(amountDiff) / float64(tolerance))
			if amountPrecision < 0 {
				amountPrecision = 0
			}
			
			dateScore := 1.0
			if daysDiff > 30 {
				// Penalize transactions more than 30 days away
				dateScore = math.Max(0.1, 1.0 - float64(daysDiff-30)/365.0)
			} else {
				// Bonus for transactions within 30 days
				dateScore = 1.0 - float64(daysDiff)/30.0*0.3
			}
			
			score := amountPrecision*0.9 + dateScore*0.1
			
			candidate := TransactionCandidate{
				Transaction:   transaction,
				OriginalIndex: i,
				Score:         score,
				DaysDiff:      daysDiff,
			}
			
			candidates = append(candidates, candidate)
			
			s.log.Debug().
				Float64("transaction_amount", transaction.Amount).
				Str("transaction_counterparty", transaction.CounterParty).
				Time("transaction_date", transaction.Date).
				Int("days_diff", daysDiff).
				Float64("score", score).
				Float64("amount_precision", amountPrecision).
				Float64("date_score", dateScore).
				Msg("Added candidate transaction with scoring")
		}
	}
	
	// Sort candidates by score (highest first) and limit to max 10
	sort.Slice(candidates, func(i, j int) bool {
		return candidates[i].Score > candidates[j].Score
	})
	
	// Limit to top 10 candidates per invoice
	maxCandidates := 10
	if len(candidates) > maxCandidates {
		s.log.Debug().
			Int("total_candidates", len(candidates)).
			Int("kept_candidates", maxCandidates).
			Msg("Limiting candidates to top 10 by score")
		candidates = candidates[:maxCandidates]
	}
	
	// Log the final candidate selection
	if len(candidates) > 0 {
		s.log.Debug().
			Int("final_candidates", len(candidates)).
			Float64("best_score", candidates[0].Score).
			Int("best_days_diff", candidates[0].DaysDiff).
			Time("best_transaction_date", candidates[0].Transaction.Date).
			Msg("Candidate selection completed")
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