# Invoice Processing Package

This package provides invoice processing capabilities using Google Document AI's specialized invoice parser. It extracts structured data from PDF invoices and converts them to the application's Invoice model.

## Features

- **Automated Invoice Processing**: Extract key invoice fields automatically using Google Document AI
- **Structured Data Extraction**: Convert unstructured PDFs to structured Invoice models
- **Confidence Scoring**: Get confidence scores for each extracted field
- **Multi-Currency Support**: Handle invoices in different currencies
- **Flexible Authentication**: Support for multiple credential methods
- **Comprehensive Error Handling**: Detailed error types for different failure scenarios
- **Field Validation**: Validate extracted data and calculate missing fields

## Prerequisites

1. **Google Cloud Project**: You need a Google Cloud project with Document AI API enabled
2. **Document AI Processor**: An invoice parser processor in your project
3. **Service Account**: Credentials with Document AI access
4. **Environment Variables**: Configure authentication and project settings

## Environment Variables

Set these environment variables for the service:

```bash
# Required: Your Google Cloud project ID
export GOOGLE_PROJECT_ID="your-project-id"

# Required: Processing location (us, eu, etc.)
export GOOGLE_LOCATION="us"

# Required: One of the following authentication methods
# Option 1: Path to service account JSON file
export GOOGLE_APPLICATION_CREDENTIALS="/path/to/service-account-key.json"

# Option 2: Inline JSON credentials
export GOOGLE_CREDENTIALS='{"type":"service_account","project_id":"your-project",...}'

# Optional: Specific processor ID (uses default if not set)
export GOOGLE_PROCESSOR_ID="your-processor-id"

# Optional: Specific processor version
export GOOGLE_PROCESSOR_VERSION="your-processor-version"
```

## Document AI Setup

### 1. Enable Document AI API

```bash
gcloud services enable documentai.googleapis.com --project=your-project-id
```

### 2. Create Invoice Processor

1. Go to the [Document AI Console](https://console.cloud.google.com/ai/document-ai)
2. Create a new processor
3. Select "Invoice Parser" as the processor type
4. Choose your preferred location (us, eu, etc.)
5. Note the processor ID for your configuration

### 3. Set up Service Account

```bash
# Create service account
gcloud iam service-accounts create document-ai-invoices \
    --display-name="Document AI Invoice Processing"

# Grant necessary permissions
gcloud projects add-iam-policy-binding your-project-id \
    --member="serviceAccount:document-ai-invoices@your-project-id.iam.gserviceaccount.com" \
    --role="roles/documentai.apiUser"

# Create and download key
gcloud iam service-accounts keys create service-account-key.json \
    --iam-account=document-ai-invoices@your-project-id.iam.gserviceaccount.com
```

## Quick Start

```go
package main

import (
    "context"
    "fmt"
    "log"
    "os"
    "time"
    
    "tools/internal/invoice"
    "github.com/joho/godotenv"
)

func main() {
    // Load environment variables
    if err := godotenv.Load(); err != nil {
        log.Printf("Warning: Could not load .env file: %v", err)
    }
    
    // Create context with timeout
    ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
    defer cancel()
    
    // Create invoice processor
    processor, err := invoice.NewDocumentAIInvoiceProcessor(ctx)
    if err != nil {
        log.Fatalf("Failed to create processor: %v", err)
    }
    
    // Open PDF file
    pdfFile, err := os.Open("invoice.pdf")
    if err != nil {
        log.Fatalf("Failed to open PDF: %v", err)
    }
    defer pdfFile.Close()
    
    // Process invoice
    invoiceData, err := processor.ProcessInvoice(ctx, pdfFile)
    if err != nil {
        log.Fatalf("Failed to process invoice: %v", err)
    }
    
    // Use the extracted data
    fmt.Printf("Invoice %s: %s owes %.2f %s\n",
        invoiceData.InvoiceNumber,
        invoiceData.Customer, 
        float64(invoiceData.GrossAmount)/100,
        invoiceData.Currency)
}
```

## API Reference

### InvoiceProcessor Interface

```go
type InvoiceProcessor interface {
    // ProcessInvoice extracts structured data from an invoice PDF
    ProcessInvoice(ctx context.Context, pdfData io.Reader) (*models.Invoice, error)
    
    // ProcessInvoiceWithConfidence extracts data with confidence scores
    ProcessInvoiceWithConfidence(ctx context.Context, pdfData io.Reader) (*models.Invoice, map[string]float32, error)
}
```

### DocumentAIConfig

```go
type DocumentAIConfig struct {
    ProjectID        string        // Google Cloud project ID
    Location         string        // Processing location (us, eu, etc.)
    ProcessorID      string        // Document AI processor ID
    Timeout          time.Duration // Processing timeout
    ProcessorVersion string        // Specific processor version
}
```

### Constructors

```go
// Create with environment-based configuration
func NewDocumentAIInvoiceProcessor(ctx context.Context) (InvoiceProcessor, error)

// Create with explicit configuration (for testing)
func NewDocumentAIInvoiceProcessorWithConfig(config DocumentAIConfig, client *documentai.DocumentProcessorClient) InvoiceProcessor
```

## Field Mapping

The service maps Document AI entities to Invoice model fields:

| Document AI Entity | Invoice Field | Description |
|-------------------|---------------|-------------|
| `invoice_id` | `InvoiceNumber` | Invoice identifier |
| `supplier_name` | `Vendor` | Vendor/supplier name |
| `buyer_name` | `Customer` | Customer/buyer name |
| `invoice_date` | `IssueDate` | Invoice issue date |
| `due_date` | `DueDate` | Payment due date |
| `net_amount` | `NetAmount` | Amount before tax (in cents) |
| `total_tax_amount` | `VATAmount` | VAT/tax amount (in cents) |
| `total_amount` | `GrossAmount` | Total amount (in cents) |
| `currency` | `Currency` | Currency code |
| `purchase_order` | `Reference` | Reference/PO number |

## Error Handling

The package provides comprehensive error handling:

```go
// Standard errors
var (
    ErrInvalidPDF           = errors.New("invalid or corrupted PDF document")
    ErrProcessingFailed     = errors.New("document AI processing failed")
    ErrMissingRequiredField = errors.New("missing required invoice field")
    ErrInvalidCredentials   = errors.New("invalid Google Cloud credentials")
    ErrMissingCredentials   = errors.New("missing Google Cloud credentials")
    ErrProcessorNotFound    = errors.New("Document AI processor not found")
    ErrQuotaExceeded       = errors.New("Document AI API quota exceeded")
    ErrDocumentTooLarge    = errors.New("document exceeds maximum size limit")
)
```

Example error handling:

```go
invoice, err := processor.ProcessInvoice(ctx, pdfReader)
if err != nil {
    switch {
    case errors.Is(err, invoice.ErrInvalidPDF):
        log.Println("Invalid PDF format")
    case errors.Is(err, invoice.ErrQuotaExceeded):
        log.Println("API quota exceeded")
    case errors.Is(err, invoice.ErrMissingCredentials):
        log.Println("Please configure Google Cloud credentials")
    default:
        log.Printf("Processing failed: %v", err)
    }
    return
}
```

## Advanced Usage

### Processing with Confidence Scores

```go
invoice, confidence, err := processor.ProcessInvoiceWithConfidence(ctx, pdfReader)
if err != nil {
    log.Fatal(err)
}

// Check confidence for critical fields
if confidence["total_amount"] < 0.8 {
    log.Printf("Low confidence for total amount: %.1f%%", confidence["total_amount"]*100)
}

// Display all confidence scores
for field, conf := range confidence {
    fmt.Printf("%s: %.1f%%\n", field, conf*100)
}
```

### Batch Processing

```go
files := []string{"inv1.pdf", "inv2.pdf", "inv3.pdf"}

// Create processor once and reuse
processor, err := invoice.NewDocumentAIInvoiceProcessor(ctx)
if err != nil {
    log.Fatal(err)
}

for _, filename := range files {
    file, err := os.Open(filename)
    if err != nil {
        log.Printf("Failed to open %s: %v", filename, err)
        continue
    }
    defer file.Close()
    
    fileCtx, cancel := context.WithTimeout(ctx, 90*time.Second)
    defer cancel()
    
    invoice, err := processor.ProcessInvoice(fileCtx, file)
    if err != nil {
        log.Printf("Failed to process %s: %v", filename, err)
        continue
    }
    
    fmt.Printf("Processed %s: %.2f %s\n", 
        filename, float64(invoice.GrossAmount)/100, invoice.Currency)
}
```

### Custom Configuration

```go
config := invoice.DocumentAIConfig{
    ProjectID:   "my-project",
    Location:    "eu",
    ProcessorID: "my-processor-id",
    Timeout:     120 * time.Second,
}

// Note: You would need to create the client separately for testing
client, err := documentai.NewDocumentProcessorClient(ctx, options...)
processor := invoice.NewDocumentAIInvoiceProcessorWithConfig(config, client)
```

## Limitations

### Document AI API Limitations

- **Maximum file size**: 20MB per document
- **Supported formats**: PDF, TIFF, GIF, JPEG, PNG, BMP, WEBP
- **Processing time**: 5-15 seconds per invoice (varies by complexity)
- **Quota limits**: Check Google Cloud Console for current limits

### Invoice Format Requirements

- Document must be a valid invoice or receipt
- Text should be machine-readable (not handwritten)
- Critical fields (amounts, dates) should be clearly visible
- Multi-page invoices are supported but may affect processing time

### Field Extraction Accuracy

- **High accuracy fields**: Total amounts, dates, invoice numbers
- **Medium accuracy fields**: Tax amounts, vendor names, line items
- **Variable accuracy**: Customer names, addresses, complex layouts

## Performance Tips

1. **Reuse Processor**: Create one processor instance and reuse it
2. **Appropriate Timeouts**: Set realistic timeouts (90-120 seconds)
3. **File Size**: Keep PDFs under 10MB for best performance
4. **Concurrent Processing**: Use goroutines for batch processing
5. **Error Handling**: Implement retry logic for transient failures

## Troubleshooting

### Authentication Issues

```
Error: missing Google Cloud credentials
```

**Solution**: Set `GOOGLE_APPLICATION_CREDENTIALS` or `GOOGLE_CREDENTIALS` environment variable.

### Processor Not Found

```
Error: Document AI processor not found
```

**Solutions**:
- Verify `GOOGLE_PROCESSOR_ID` is correct
- Check processor exists in specified location
- Ensure service account has access

### Quota Exceeded

```
Error: Document AI API quota exceeded
```

**Solutions**:
- Check quotas in Google Cloud Console
- Request quota increase if needed
- Implement exponential backoff retry

### Low Confidence Scores

**Possible causes**:
- Poor scan quality
- Unusual invoice format
- Handwritten text
- Language not supported

**Solutions**:
- Improve scan quality
- Use standardized invoice templates
- Validate extracted data
- Implement confidence thresholds

## License

This package is part of the Tools CLI project and follows the same license terms.