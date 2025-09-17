package invoice

import (
	"fmt"
	"math"

	"github.com/rs/zerolog"
	"tools/internal/logger"
	"tools/pkg/models"
)

// AmountValidation handles validation and reconciliation of amounts from different sources
type AmountValidation struct {
	log zerolog.Logger
}

// NewAmountValidation creates a new amount validation service
func NewAmountValidation() *AmountValidation {
	return &AmountValidation{
		log: logger.WithComponent("amount-validation"),
	}
}

// AmountSource represents where an amount came from
type AmountSource struct {
	NetAmount   int64
	VATAmount   int64
	GrossAmount int64
	Source      string // "document_ai", "chatgpt", "calculated"
	Confidence  float32
}

// AmountValidationResult contains the validated amounts and any warnings
type AmountValidationResult struct {
	FinalAmounts    *models.Invoice
	Warnings        []string
	HasDiscrepancy  bool
	MaxDiscrepancy  float64 // Percentage
}

// ValidateAndReconcileAmounts compares amounts from different sources and selects the best
func (av *AmountValidation) ValidateAndReconcileAmounts(
	documentAI *AmountSource,
	chatGPT *AmountSource,
	invoice *models.Invoice,
) *AmountValidationResult {
	result := &AmountValidationResult{
		FinalAmounts: &models.Invoice{},
		Warnings:     []string{},
	}

	// Copy base invoice data
	*result.FinalAmounts = *invoice

	av.log.Debug().
		Interface("document_ai", documentAI).
		Interface("chatgpt", chatGPT).
		Msg("Starting amount validation and reconciliation")

	// Validate each amount type
	result.FinalAmounts.NetAmount = av.selectBestAmount("net", documentAI.NetAmount, chatGPT.NetAmount, documentAI, chatGPT, result)
	result.FinalAmounts.VATAmount = av.selectBestAmount("vat", documentAI.VATAmount, chatGPT.VATAmount, documentAI, chatGPT, result)
	result.FinalAmounts.GrossAmount = av.selectBestAmount("gross", documentAI.GrossAmount, chatGPT.GrossAmount, documentAI, chatGPT, result)

	// Perform cross-validation of amounts
	av.crossValidateAmounts(result)

	// Calculate missing amounts if possible
	av.calculateMissingAmounts(result.FinalAmounts, result)

	av.log.Info().
		Int64("final_net", result.FinalAmounts.NetAmount).
		Int64("final_vat", result.FinalAmounts.VATAmount).
		Int64("final_gross", result.FinalAmounts.GrossAmount).
		Bool("has_discrepancy", result.HasDiscrepancy).
		Float64("max_discrepancy_pct", result.MaxDiscrepancy).
		Strs("warnings", result.Warnings).
		Msg("Amount validation completed")

	return result
}

// selectBestAmount chooses the best amount from two sources
func (av *AmountValidation) selectBestAmount(
	amountType string,
	documentAIAmount, chatGPTAmount int64,
	documentAI, chatGPT *AmountSource,
	result *AmountValidationResult,
) int64 {
	// If both sources have amounts, compare them
	if documentAIAmount > 0 && chatGPTAmount > 0 {
		discrepancy := av.calculateDiscrepancy(documentAIAmount, chatGPTAmount)
		
		if discrepancy > result.MaxDiscrepancy {
			result.MaxDiscrepancy = discrepancy
		}

		if discrepancy <= 5.0 {
			// Within 5% tolerance - prefer Document AI (higher confidence)
			av.log.Debug().
				Str("type", amountType).
				Int64("document_ai", documentAIAmount).
				Int64("chatgpt", chatGPTAmount).
				Float64("discrepancy_pct", discrepancy).
				Msg("Amounts within tolerance, using Document AI")
			return documentAIAmount
		} else {
			// Significant discrepancy - add warning and choose based on confidence
			warning := fmt.Sprintf("%s amount discrepancy: Document AI=%.2f, ChatGPT=%.2f (%.1f%% difference)",
				amountType,
				float64(documentAIAmount)/100,
				float64(chatGPTAmount)/100,
				discrepancy)
			result.Warnings = append(result.Warnings, warning)
			result.HasDiscrepancy = true

			// Choose source with higher confidence
			if documentAI.Confidence >= chatGPT.Confidence {
				av.log.Warn().
					Str("type", amountType).
					Float64("discrepancy_pct", discrepancy).
					Str("chosen", "document_ai").
					Float32("confidence", documentAI.Confidence).
					Msg("Significant discrepancy, choosing Document AI (higher confidence)")
				return documentAIAmount
			} else {
				av.log.Warn().
					Str("type", amountType).
					Float64("discrepancy_pct", discrepancy).
					Str("chosen", "chatgpt").
					Float32("confidence", chatGPT.Confidence).
					Msg("Significant discrepancy, choosing ChatGPT (higher confidence)")
				return chatGPTAmount
			}
		}
	}

	// Only one source has amount - use it if available (including negative amounts for credit notes)
	if documentAIAmount != 0 {
		av.log.Debug().
			Str("type", amountType).
			Int64("amount", documentAIAmount).
			Str("source", "document_ai_only").
			Msg("Using Document AI amount (only source)")
		return documentAIAmount
	}
	
	if chatGPTAmount != 0 {
		av.log.Debug().
			Str("type", amountType).
			Int64("amount", chatGPTAmount).
			Str("source", "chatgpt_only").
			Msg("Using ChatGPT amount (only source)")
		return chatGPTAmount
	}

	// No amount from either source
	return 0
}

// calculateDiscrepancy returns the percentage difference between two amounts
func (av *AmountValidation) calculateDiscrepancy(amount1, amount2 int64) float64 {
	if amount1 == 0 && amount2 == 0 {
		return 0.0
	}
	
	if amount1 == 0 || amount2 == 0 {
		return 100.0 // One is zero, other is not
	}

	larger := float64(maxInt64(amount1, amount2))
	smaller := float64(minInt64(amount1, amount2))
	
	return math.Abs((larger-smaller)/larger) * 100
}

// crossValidateAmounts performs mathematical validation of the three amounts
func (av *AmountValidation) crossValidateAmounts(result *AmountValidationResult) {
	invoice := result.FinalAmounts
	
	// Only validate if we have at least 2 amounts
	nonZeroCount := 0
	if invoice.NetAmount > 0 {
		nonZeroCount++
	}
	if invoice.VATAmount > 0 {
		nonZeroCount++
	}
	if invoice.GrossAmount > 0 {
		nonZeroCount++
	}

	if nonZeroCount < 2 {
		return // Not enough data for cross-validation
	}

	// Check if Net + VAT ≈ Gross (within 2 cents tolerance)
	if invoice.NetAmount > 0 && invoice.VATAmount > 0 && invoice.GrossAmount > 0 {
		calculated := invoice.NetAmount + invoice.VATAmount
		difference := abs(calculated - invoice.GrossAmount)
		
		if difference > 2 { // More than 2 cents difference
			warning := fmt.Sprintf("Amount calculation error: Net(%.2f) + VAT(%.2f) = %.2f, but Gross=%.2f (difference: %.2f)",
				float64(invoice.NetAmount)/100,
				float64(invoice.VATAmount)/100,
				float64(calculated)/100,
				float64(invoice.GrossAmount)/100,
				float64(difference)/100)
			result.Warnings = append(result.Warnings, warning)
			result.HasDiscrepancy = true

			av.log.Warn().
				Int64("net", invoice.NetAmount).
				Int64("vat", invoice.VATAmount).
				Int64("gross", invoice.GrossAmount).
				Int64("calculated", calculated).
				Int64("difference", difference).
				Msg("Amount calculation discrepancy detected")
		}
	}
}

// calculateMissingAmounts fills in missing amounts where possible
func (av *AmountValidation) calculateMissingAmounts(invoice *models.Invoice, result *AmountValidationResult) {
	// If we have net and VAT, calculate gross
	if invoice.NetAmount > 0 && invoice.VATAmount > 0 && invoice.GrossAmount == 0 {
		invoice.GrossAmount = invoice.NetAmount + invoice.VATAmount
		result.Warnings = append(result.Warnings, "Gross amount calculated from Net + VAT")
		av.log.Info().
			Int64("calculated_gross", invoice.GrossAmount).
			Msg("Calculated missing gross amount")
	}

	// If we have gross and VAT, calculate net
	if invoice.GrossAmount > 0 && invoice.VATAmount > 0 && invoice.NetAmount == 0 {
		invoice.NetAmount = invoice.GrossAmount - invoice.VATAmount
		result.Warnings = append(result.Warnings, "Net amount calculated from Gross - VAT")
		av.log.Info().
			Int64("calculated_net", invoice.NetAmount).
			Msg("Calculated missing net amount")
	}

	// If we have gross and net, calculate VAT
	if invoice.GrossAmount > 0 && invoice.NetAmount > 0 && invoice.VATAmount == 0 {
		invoice.VATAmount = invoice.GrossAmount - invoice.NetAmount
		result.Warnings = append(result.Warnings, "VAT amount calculated from Gross - Net")
		av.log.Info().
			Int64("calculated_vat", invoice.VATAmount).
			Msg("Calculated missing VAT amount")
	}
}

// Helper functions
func maxInt64(a, b int64) int64 {
	if a > b {
		return a
	}
	return b
}

func minInt64(a, b int64) int64 {
	if a < b {
		return a
	}
	return b
}

func abs(x int64) int64 {
	if x < 0 {
		return -x
	}
	return x
}