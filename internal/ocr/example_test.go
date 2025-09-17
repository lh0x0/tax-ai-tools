package ocr_test

import (
	"context"
	"fmt"
	"log"
	"os"
	"strings"
	"time"

	"tools/internal/ocr"
)

// Example demonstrates basic usage of the OCR service.
func Example() {
	// Load .env file (using godotenv in main)
	// This should be done in your main() function:
	// 
	// if err := godotenv.Load(); err != nil {
	//     log.Printf("Warning: Could not load .env file: %v", err)
	// }

	// Create context with timeout for OCR processing
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	// Create service - credentials handled internally from environment
	ocrService, err := ocr.NewGoogleVisionOCRService(ctx)
	if err != nil {
		log.Fatalf("Failed to create OCR service: %v", err)
	}

	// Open PDF file
	pdfFile, err := os.Open("sample_invoice.pdf")
	if err != nil {
		log.Fatalf("Failed to open PDF: %v", err)
	}
	defer pdfFile.Close()

	// Process PDF with basic text extraction
	text, err := ocrService.ProcessPDF(ctx, pdfFile)
	if err != nil {
		log.Fatalf("Failed to process PDF: %v", err)
	}

	fmt.Printf("Extracted text (%d characters):\n%s\n", len(text), text)
}

// ExampleWithMetadata demonstrates OCR processing with detailed metadata.
func ExampleWithMetadata() {
	ctx := context.Background()

	// Create service
	ocrService, err := ocr.NewGoogleVisionOCRService(ctx)
	if err != nil {
		log.Fatalf("Failed to create OCR service: %v", err)
	}

	// Open PDF file
	pdfFile, err := os.Open("sample_invoice.pdf")
	if err != nil {
		log.Fatalf("Failed to open PDF: %v", err)
	}
	defer pdfFile.Close()

	// Process PDF with metadata
	result, err := ocrService.ProcessPDFWithMetadata(ctx, pdfFile)
	if err != nil {
		log.Fatalf("Failed to process PDF: %v", err)
	}

	// Display results
	fmt.Printf("OCR Results:\n")
	fmt.Printf("  Pages processed: %d\n", result.PageCount)
	fmt.Printf("  Confidence: %.2f%%\n", result.Confidence*100)
	fmt.Printf("  Languages: %s\n", strings.Join(result.LanguageCodes, ", "))
	fmt.Printf("  Processing time: %v\n", result.ProcessingDuration)
	fmt.Printf("  Processed at: %v\n", result.ProcessedAt.Format(time.RFC3339))
	fmt.Printf("\nExtracted text:\n%s\n", result.Text)
}

// ExampleErrorHandling demonstrates proper error handling patterns.
func ExampleErrorHandling() {
	ctx := context.Background()

	// Create service
	ocrService, err := ocr.NewGoogleVisionOCRService(ctx)
	if err != nil {
		// Handle credential errors
		if err == ocr.ErrMissingCredentials {
			log.Fatalf("Please set GOOGLE_APPLICATION_CREDENTIALS or GOOGLE_CREDENTIALS environment variable")
		}
		log.Fatalf("Failed to create OCR service: %v", err)
	}

	// Open PDF file
	pdfFile, err := os.Open("large_document.pdf")
	if err != nil {
		log.Fatalf("Failed to open PDF: %v", err)
	}
	defer pdfFile.Close()

	// Process PDF with error handling
	result, err := ocrService.ProcessPDFWithMetadata(ctx, pdfFile)
	if err != nil {
		// Handle specific OCR errors
		switch {
		case err == ocr.ErrPDFTooLarge:
			log.Printf("PDF is too large for processing. Maximum size is 20MB.")
			return
		case err == ocr.ErrTooManyPages:
			log.Printf("PDF has too many pages. Maximum is 5 pages for synchronous processing.")
			return
		case err == ocr.ErrInvalidPDF:
			log.Printf("The file is not a valid PDF document.")
			return
		case err == ocr.ErrEmptyDocument:
			log.Printf("No readable text found in the document.")
			return
		default:
			log.Fatalf("OCR processing failed: %v", err)
		}
	}

	fmt.Printf("Successfully processed %d pages\n", result.PageCount)
}

// ExampleWithTesting demonstrates how to use the service with dependency injection for testing.
func ExampleWithTesting() {
	// In your tests, you can inject a mock client:
	// mockClient := &mockVisionClient{} // Your mock implementation
	// ocrService := ocr.NewGoogleVisionOCRServiceWithClient(mockClient)
	
	// In production code, use the environment-based constructor:
	ctx := context.Background()
	ocrService, err := ocr.NewGoogleVisionOCRService(ctx)
	if err != nil {
		log.Fatalf("Failed to create OCR service: %v", err)
	}

	// Use the service normally
	_ = ocrService
}

// ExampleBatchProcessing demonstrates processing multiple PDF files.
func ExampleBatchProcessing() {
	ctx := context.Background()

	// Create service once and reuse
	ocrService, err := ocr.NewGoogleVisionOCRService(ctx)
	if err != nil {
		log.Fatalf("Failed to create OCR service: %v", err)
	}

	// Process multiple files
	pdfFiles := []string{"invoice1.pdf", "invoice2.pdf", "invoice3.pdf"}
	
	for _, filename := range pdfFiles {
		func(filename string) {
			file, err := os.Open(filename)
			if err != nil {
				log.Printf("Failed to open %s: %v", filename, err)
				return
			}
			defer file.Close()

			// Create context with timeout for each file
			fileCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
			defer cancel()

			result, err := ocrService.ProcessPDFWithMetadata(fileCtx, file)
			if err != nil {
				log.Printf("Failed to process %s: %v", filename, err)
				return
			}

			fmt.Printf("%s: %d pages, %.1f%% confidence, %d chars\n", 
				filename, result.PageCount, result.Confidence*100, len(result.Text))
		}(filename)
	}
}