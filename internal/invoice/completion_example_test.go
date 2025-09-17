package invoice_test

import (
	"context"
	"fmt"
	"log"
	"os"
	"time"

	"tools/internal/invoice"
	"tools/pkg/models"
)

// ExampleInvoiceCompletion demonstrates basic usage of the invoice completion service.
func ExampleInvoiceCompletion() {
	// Load .env file (using godotenv in main)
	// This should be done in your main() function:
	//
	// if err := godotenv.Load(); err != nil {
	//     log.Printf("Warning: Could not load .env file: %v", err)
	// }

	// Create context with timeout
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()

	// Create completion service - dependencies handled internally from environment
	completionService, err := invoice.NewInvoiceCompletionService(ctx)
	if err != nil {
		log.Fatal(err)
	}

	// First process with Document AI to get partial invoice data
	processor, err := invoice.NewDocumentAIInvoiceProcessor(ctx)
	if err != nil {
		log.Fatal(err)
	}

	// Open PDF file
	pdfFile, err := os.Open("sample_invoice.pdf")
	if err != nil {
		log.Fatalf("Failed to open PDF: %v", err)
	}
	defer pdfFile.Close()

	// Process with Document AI first
	partialInvoice, err := processor.ProcessInvoice(ctx, pdfFile)
	if err != nil {
		log.Fatalf("Failed to process with Document AI: %v", err)
	}

	fmt.Printf("Document AI extracted:\n")
	fmt.Printf("  Invoice Number: %s\n", partialInvoice.InvoiceNumber)
	fmt.Printf("  Vendor: %s\n", partialInvoice.Vendor)
	fmt.Printf("  Type: %s (likely missing)\n", partialInvoice.Type)
	fmt.Printf("  Amount: %.2f %s\n", float64(partialInvoice.GrossAmount)/100, partialInvoice.Currency)

	// Check if completion is needed
	isComplete, missingFields := completionService.ValidateInvoice(partialInvoice)
	if isComplete {
		fmt.Println("Invoice is already complete!")
		return
	}

	fmt.Printf("Missing fields: %v\n", missingFields)

	// Re-open file for completion service
	pdfFile2, err := os.Open("sample_invoice.pdf")
	if err != nil {
		log.Fatal(err)
	}
	defer pdfFile2.Close()

	// Complete the invoice using OCR + ChatGPT
	completedInvoice, err := completionService.CompleteInvoice(ctx, partialInvoice, pdfFile2)
	if err != nil {
		log.Fatalf("Failed to complete invoice: %v", err)
	}

	fmt.Printf("\nCompleted invoice:\n")
	fmt.Printf("  Invoice Number: %s\n", completedInvoice.InvoiceNumber)
	fmt.Printf("  Type: %s (DETERMINED)\n", completedInvoice.Type)
	fmt.Printf("  Vendor: %s\n", completedInvoice.Vendor)
	fmt.Printf("  Customer: %s\n", completedInvoice.Customer)
	fmt.Printf("  Amount: %.2f %s\n", float64(completedInvoice.GrossAmount)/100, completedInvoice.Currency)
	fmt.Printf("  Issue Date: %s\n", completedInvoice.IssueDate.Format("2006-01-02"))
	if !completedInvoice.DueDate.IsZero() {
		fmt.Printf("  Due Date: %s\n", completedInvoice.DueDate.Format("2006-01-02"))
	}
}

// ExampleInvoiceCompletionWithConfidence demonstrates completion with confidence scores.
func ExampleInvoiceCompletionWithConfidence() {
	ctx := context.Background()

	// Create services
	processor, err := invoice.NewDocumentAIInvoiceProcessor(ctx)
	if err != nil {
		log.Fatal(err)
	}

	completionService, err := invoice.NewInvoiceCompletionService(ctx)
	if err != nil {
		log.Fatal(err)
	}

	// Open PDF file
	pdfFile, err := os.Open("sample_invoice.pdf")
	if err != nil {
		log.Fatal(err)
	}
	defer pdfFile.Close()

	// Process with Document AI first
	partialInvoice, documentAIConfidence, err := processor.ProcessInvoiceWithConfidence(ctx, pdfFile)
	if err != nil {
		log.Fatal(err)
	}

	// Re-open file for completion
	pdfFile2, err := os.Open("sample_invoice.pdf")
	if err != nil {
		log.Fatal(err)
	}
	defer pdfFile2.Close()

	// Complete with confidence scores
	completedInvoice, completionConfidence, err := completionService.CompleteInvoiceWithConfidence(ctx, partialInvoice, pdfFile2)
	if err != nil {
		log.Fatal(err)
	}

	// Display results with confidence scores
	fmt.Printf("Invoice Processing Results:\n")
	fmt.Printf("  Type: %s (completion confidence: %.1f%%)\n",
		completedInvoice.Type, completionConfidence["type"]*100)
	fmt.Printf("  Invoice Number: %s (Document AI confidence: %.1f%%)\n",
		completedInvoice.InvoiceNumber, documentAIConfidence["invoice_id"]*100)
	fmt.Printf("  Vendor: %s (Document AI confidence: %.1f%%)\n",
		completedInvoice.Vendor, documentAIConfidence["supplier_name"]*100)

	// Show all completion confidence scores
	fmt.Printf("\nCompletion Service Confidence Scores:\n")
	for field, conf := range completionConfidence {
		fmt.Printf("  %s: %.1f%%\n", field, conf*100)
	}
}

// ExampleManualInvoiceCompletion demonstrates completing a manually created invoice.
func ExampleManualInvoiceCompletion() {
	ctx := context.Background()

	// Create a partially filled invoice (simulating manual data entry)
	partialInvoice := &models.Invoice{
		ID:            "MANUAL-001",
		InvoiceNumber: "INV-12345",
		GrossAmount:   150000, // €1500.00 in cents
		Currency:      "EUR",
		CreatedAt:     time.Now(),
		UpdatedAt:     time.Now(),
		// Missing: Type, Vendor, Customer, dates
	}

	// Create completion service
	completionService, err := invoice.NewInvoiceCompletionService(ctx)
	if err != nil {
		log.Fatal(err)
	}

	// Check what's missing
	isComplete, missingFields := completionService.ValidateInvoice(partialInvoice)
	fmt.Printf("Invoice complete: %v\n", isComplete)
	fmt.Printf("Missing fields: %v\n", missingFields)

	// Open PDF file for completion
	pdfFile, err := os.Open("invoice_manual.pdf")
	if err != nil {
		log.Fatalf("Failed to open PDF: %v", err)
	}
	defer pdfFile.Close()

	// Complete the missing fields
	completedInvoice, confidence, err := completionService.CompleteInvoiceWithConfidence(ctx, partialInvoice, pdfFile)
	if err != nil {
		log.Fatalf("Failed to complete invoice: %v", err)
	}

	fmt.Printf("\nCompleted Invoice:\n")
	fmt.Printf("  Type: %s (confidence: %.1f%%)\n", completedInvoice.Type, confidence["type"]*100)
	fmt.Printf("  Vendor: %s\n", completedInvoice.Vendor)
	fmt.Printf("  Customer: %s\n", completedInvoice.Customer)
	if !completedInvoice.IssueDate.IsZero() {
		fmt.Printf("  Issue Date: %s\n", completedInvoice.IssueDate.Format("2006-01-02"))
	}
}

// ExampleInvoiceTypeValidation demonstrates type validation and determination.
func ExampleInvoiceTypeValidation() {
	// Example of different invoice types
	
	// PAYABLE invoice (we owe money)
	payableInvoice := &models.Invoice{
		Type:          "PAYABLE",
		InvoiceNumber: "BILL-001",
		Vendor:        "External Supplier Co.",
		Customer:      "Our Company",
		GrossAmount:   50000, // €500.00
		Currency:      "EUR",
	}

	// RECEIVABLE invoice (customer owes us)
	receivableInvoice := &models.Invoice{
		Type:          "RECEIVABLE", 
		InvoiceNumber: "INV-001",
		Vendor:        "Our Company",
		Customer:      "Client ABC Corp",
		GrossAmount:   100000, // €1000.00
		Currency:      "EUR",
	}

	// Create completion service for validation
	ctx := context.Background()
	completionService, err := invoice.NewInvoiceCompletionService(ctx)
	if err != nil {
		log.Fatal(err)
	}

	// Validate both invoices
	fmt.Printf("PAYABLE Invoice Validation:\n")
	isValid, missing := completionService.ValidateInvoice(payableInvoice)
	fmt.Printf("  Valid: %v, Missing: %v\n", isValid, missing)

	fmt.Printf("RECEIVABLE Invoice Validation:\n")
	isValid, missing = completionService.ValidateInvoice(receivableInvoice)
	fmt.Printf("  Valid: %v, Missing: %v\n", isValid, missing)

	// Example of invalid type
	invalidInvoice := &models.Invoice{
		Type:          "INVALID_TYPE", // This will fail validation
		InvoiceNumber: "INV-BAD",
		GrossAmount:   1000,
		Currency:      "EUR",
	}

	fmt.Printf("Invalid Type Validation:\n")
	isValid, missing = completionService.ValidateInvoice(invalidInvoice)
	fmt.Printf("  Valid: %v, Missing: %v\n", isValid, missing)
}

// ExampleBatchInvoiceCompletion demonstrates processing multiple invoices with completion.
func ExampleBatchInvoiceCompletion() {
	ctx := context.Background()

	// Create services
	processor, err := invoice.NewDocumentAIInvoiceProcessor(ctx)
	if err != nil {
		log.Fatal(err)
	}

	completionService, err := invoice.NewInvoiceCompletionService(ctx)
	if err != nil {
		log.Fatal(err)
	}

	// Process multiple files
	invoiceFiles := []string{"invoice1.pdf", "invoice2.pdf", "invoice3.pdf"}

	for _, filename := range invoiceFiles {
		func(filename string) {
			fmt.Printf("\nProcessing: %s\n", filename)

			file, err := os.Open(filename)
			if err != nil {
				log.Printf("Failed to open %s: %v", filename, err)
				return
			}
			defer file.Close()

			// Step 1: Document AI processing
			partialInvoice, err := processor.ProcessInvoice(ctx, file)
			if err != nil {
				log.Printf("Document AI failed for %s: %v", filename, err)
				return
			}

			fmt.Printf("  Document AI: Type='%s', Vendor='%s'\n", partialInvoice.Type, partialInvoice.Vendor)

			// Step 2: Check if completion needed
			isComplete, missingFields := completionService.ValidateInvoice(partialInvoice)
			if isComplete {
				fmt.Printf("  Invoice already complete!\n")
				return
			}

			fmt.Printf("  Missing: %v\n", missingFields)

			// Step 3: Complete using OCR + ChatGPT
			file2, err := os.Open(filename)
			if err != nil {
				log.Printf("Failed to re-open %s: %v", filename, err)
				return
			}
			defer file2.Close()

			fileCtx, cancel := context.WithTimeout(ctx, 2*time.Minute)
			defer cancel()

			completedInvoice, confidence, err := completionService.CompleteInvoiceWithConfidence(fileCtx, partialInvoice, file2)
			if err != nil {
				log.Printf("Completion failed for %s: %v", filename, err)
				return
			}

			fmt.Printf("  Completed: Type='%s' (%.1f%%), Vendor='%s', Customer='%s'\n",
				completedInvoice.Type, confidence["type"]*100,
				completedInvoice.Vendor, completedInvoice.Customer)

		}(filename)
	}
}