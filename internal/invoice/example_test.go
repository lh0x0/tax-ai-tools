package invoice_test

import (
	"context"
	"fmt"
	"log"
	"os"
	"time"

	"tools/internal/invoice"
)

// Example demonstrates basic usage of the invoice processor.
func Example() {
	// Load .env file (using godotenv in main)
	// This should be done in your main() function:
	//
	// if err := godotenv.Load(); err != nil {
	//     log.Printf("Warning: Could not load .env file: %v", err)
	// }

	// Create context with timeout
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()

	// Create invoice processor - credentials handled internally from environment
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

	// Process invoice
	invoiceData, err := processor.ProcessInvoice(ctx, pdfFile)
	if err != nil {
		log.Fatalf("Failed to process invoice: %v", err)
	}

	fmt.Printf("Invoice %s: %s owes %.2f %s\n",
		invoiceData.InvoiceNumber,
		invoiceData.Customer,
		float64(invoiceData.GrossAmount)/100,
		invoiceData.Currency)

	fmt.Printf("Due date: %s\n", invoiceData.DueDate.Format("2006-01-02"))
}

// ExampleWithConfidence demonstrates invoice processing with confidence scores.
func ExampleWithConfidence() {
	ctx := context.Background()

	// Create processor
	processor, err := invoice.NewDocumentAIInvoiceProcessor(ctx)
	if err != nil {
		log.Fatal(err)
	}

	// Open PDF file
	pdfFile, err := os.Open("sample_invoice.pdf")
	if err != nil {
		log.Fatal(err)
	}
	defer pdfFile.Close()

	// Process invoice with confidence scores
	invoiceData, confidence, err := processor.ProcessInvoiceWithConfidence(ctx, pdfFile)
	if err != nil {
		log.Fatal(err)
	}

	// Display results
	fmt.Printf("Invoice Processing Results:\n")
	fmt.Printf("  Invoice Number: %s (confidence: %.1f%%)\n",
		invoiceData.InvoiceNumber, confidence["invoice_id"]*100)
	fmt.Printf("  Vendor: %s (confidence: %.1f%%)\n",
		invoiceData.Vendor, confidence["supplier_name"]*100)
	fmt.Printf("  Total Amount: %.2f %s (confidence: %.1f%%)\n",
		float64(invoiceData.GrossAmount)/100, invoiceData.Currency,
		confidence["total_amount"]*100)

	// Show all confidence scores
	fmt.Printf("\nAll extracted fields:\n")
	for field, conf := range confidence {
		fmt.Printf("  %s: %.1f%%\n", field, conf*100)
	}
}

// ExampleErrorHandling demonstrates proper error handling patterns.
func ExampleErrorHandling() {
	ctx := context.Background()

	// Create processor
	processor, err := invoice.NewDocumentAIInvoiceProcessor(ctx)
	if err != nil {
		// Handle credential and configuration errors
		if err == invoice.ErrMissingCredentials {
			log.Fatalf("Please set GOOGLE_APPLICATION_CREDENTIALS or GOOGLE_CREDENTIALS")
		}
		if err == invoice.ErrInvalidConfiguration {
			log.Fatalf("Please set GOOGLE_PROJECT_ID environment variable")
		}
		log.Fatalf("Failed to create processor: %v", err)
	}

	// Open PDF file
	pdfFile, err := os.Open("invoice.pdf")
	if err != nil {
		log.Fatalf("Failed to open PDF: %v", err)
	}
	defer pdfFile.Close()

	// Process invoice with comprehensive error handling
	invoiceData, err := processor.ProcessInvoice(ctx, pdfFile)
	if err != nil {
		// Handle specific invoice processing errors
		switch {
		case err == invoice.ErrInvalidPDF:
			log.Printf("The file is not a valid PDF document.")
			return
		case err == invoice.ErrDocumentTooLarge:
			log.Printf("PDF is too large for processing. Maximum size is 20MB.")
			return
		case err == invoice.ErrProcessorNotFound:
			log.Printf("Document AI processor not found. Check your GOOGLE_PROCESSOR_ID.")
			return
		case err == invoice.ErrQuotaExceeded:
			log.Printf("Document AI quota exceeded. Check your project quotas.")
			return
		case err == invoice.ErrMissingRequiredField:
			log.Printf("Could not extract required invoice fields.")
			return
		default:
			log.Fatalf("Invoice processing failed: %v", err)
		}
	}

	fmt.Printf("Successfully processed invoice: %s\n", invoiceData.InvoiceNumber)
}

// ExampleCustomConfiguration demonstrates using custom configuration.
func ExampleCustomConfiguration() {
	// Create custom configuration
	config := invoice.DocumentAIConfig{
		ProjectID:   "your-project-id",
		Location:    "eu", // European location
		ProcessorID: "your-processor-id",
		Timeout:     90 * time.Second,
	}

	// Note: In practice, you would create a client and pass it:
	// client, err := documentai.NewDocumentProcessorClient(ctx, ...)
	// processor := invoice.NewDocumentAIInvoiceProcessorWithConfig(config, client)

	fmt.Printf("Custom config: Project=%s, Location=%s\n", config.ProjectID, config.Location)
}

// ExampleBatchProcessing demonstrates processing multiple invoice files.
func ExampleBatchProcessing() {
	ctx := context.Background()

	// Create processor once and reuse
	processor, err := invoice.NewDocumentAIInvoiceProcessor(ctx)
	if err != nil {
		log.Fatal(err)
	}

	// Process multiple files
	invoiceFiles := []string{"invoice1.pdf", "invoice2.pdf", "invoice3.pdf"}

	for _, filename := range invoiceFiles {
		func(filename string) {
			file, err := os.Open(filename)
			if err != nil {
				log.Printf("Failed to open %s: %v", filename, err)
				return
			}
			defer file.Close()

			// Create context with timeout for each file
			fileCtx, cancel := context.WithTimeout(ctx, 90*time.Second)
			defer cancel()

			invoiceData, confidence, err := processor.ProcessInvoiceWithConfidence(fileCtx, file)
			if err != nil {
				log.Printf("Failed to process %s: %v", filename, err)
				return
			}

			fmt.Printf("%s: %s (%.2f %s) - avg confidence: %.1f%%\n",
				filename,
				invoiceData.InvoiceNumber,
				float64(invoiceData.GrossAmount)/100,
				invoiceData.Currency,
				averageConfidence(confidence)*100)
		}(filename)
	}
}

// averageConfidence calculates average confidence across all fields.
func averageConfidence(confidence map[string]float32) float32 {
	if len(confidence) == 0 {
		return 0
	}

	var sum float32
	for _, conf := range confidence {
		sum += conf
	}
	return sum / float32(len(confidence))
}

// ExampleInvoiceDataUsage demonstrates working with extracted invoice data.
func ExampleInvoiceDataUsage() {
	ctx := context.Background()

	processor, err := invoice.NewDocumentAIInvoiceProcessor(ctx)
	if err != nil {
		log.Fatal(err)
	}

	pdfFile, err := os.Open("sample_invoice.pdf")
	if err != nil {
		log.Fatal(err)
	}
	defer pdfFile.Close()

	invoiceData, err := processor.ProcessInvoice(ctx, pdfFile)
	if err != nil {
		log.Fatal(err)
	}

	// Work with the extracted data
	fmt.Printf("Processing invoice from %s\n", invoiceData.Vendor)

	// Check if payment is overdue
	if time.Now().After(invoiceData.DueDate) && !invoiceData.IsPaid {
		daysOverdue := int(time.Since(invoiceData.DueDate).Hours() / 24)
		fmt.Printf("WARNING: Payment is %d days overdue!\n", daysOverdue)
	}

	// Calculate payment details
	netAmount := float64(invoiceData.NetAmount) / 100
	vatAmount := float64(invoiceData.VATAmount) / 100
	grossAmount := float64(invoiceData.GrossAmount) / 100

	fmt.Printf("Payment breakdown:\n")
	fmt.Printf("  Net Amount: %.2f %s\n", netAmount, invoiceData.Currency)
	fmt.Printf("  VAT Amount: %.2f %s\n", vatAmount, invoiceData.Currency)
	fmt.Printf("  Total Amount: %.2f %s\n", grossAmount, invoiceData.Currency)

	if invoiceData.VATAmount > 0 && invoiceData.NetAmount > 0 {
		vatRate := (float64(invoiceData.VATAmount) / float64(invoiceData.NetAmount)) * 100
		fmt.Printf("  VAT Rate: %.1f%%\n", vatRate)
	}

	// Display dates
	fmt.Printf("Invoice Date: %s\n", invoiceData.IssueDate.Format("January 2, 2006"))
	fmt.Printf("Due Date: %s\n", invoiceData.DueDate.Format("January 2, 2006"))

	if invoiceData.Reference != "" {
		fmt.Printf("Reference: %s\n", invoiceData.Reference)
	}
}