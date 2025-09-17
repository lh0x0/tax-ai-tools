# OCR Package

This package provides OCR (Optical Character Recognition) capabilities using Google Cloud Vision API for processing PDF documents and extracting text with metadata.

## Features

- **PDF Text Extraction**: Extract text from PDF documents with high accuracy
- **Metadata Support**: Get confidence scores, page counts, language detection, and processing times
- **Error Handling**: Comprehensive error types for different failure scenarios
- **Context Support**: Full context support for cancellation and timeouts
- **Flexible Authentication**: Support for both file-based and inline credentials
- **Testing Support**: Dependency injection for easy testing

## Prerequisites

1. **Google Cloud Project**: You need a Google Cloud project with Vision API enabled
2. **Credentials**: Service account credentials with Vision API access
3. **Environment Variables**: Configure authentication (see below)

## Environment Variables

Set one of the following for authentication:

```bash
# Option 1: Path to service account JSON file
export GOOGLE_APPLICATION_CREDENTIALS="/path/to/service-account-key.json"

# Option 2: Inline JSON credentials
export GOOGLE_CREDENTIALS='{"type":"service_account","project_id":"your-project",...}'

# Required: Your Google Cloud project ID
export GOOGLE_CLOUD_PROJECT="your-project-id"
```

## API Limitations

Google Cloud Vision API has the following limits for synchronous processing:

- **Maximum file size**: 20MB
- **Maximum pages**: 5 pages
- **Supported formats**: PDF, TIFF
- **Processing time**: Typically 1-10 seconds per page

For larger documents, consider:
- Splitting into smaller files
- Using asynchronous processing with Cloud Storage
- Preprocessing to reduce file size

## Quick Start

```go
package main

import (
    "context"
    "log"
    "os"
    "time"
    
    "tools/internal/ocr"
    "github.com/joho/godotenv"
)

func main() {
    // Load environment variables
    if err := godotenv.Load(); err != nil {
        log.Printf("Warning: Could not load .env file: %v", err)
    }
    
    // Create context with timeout
    ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
    defer cancel()
    
    // Create OCR service
    ocrService, err := ocr.NewGoogleVisionOCRService(ctx)
    if err != nil {
        log.Fatalf("Failed to create OCR service: %v", err)
    }
    
    // Open PDF file
    pdfFile, err := os.Open("document.pdf")
    if err != nil {
        log.Fatalf("Failed to open PDF: %v", err)
    }
    defer pdfFile.Close()
    
    // Extract text
    text, err := ocrService.ProcessPDF(ctx, pdfFile)
    if err != nil {
        log.Fatalf("Failed to process PDF: %v", err)
    }
    
    log.Printf("Extracted %d characters of text", len(text))
}
```

## API Reference

### OCRService Interface

```go
type OCRService interface {
    // ProcessPDF extracts text from a PDF document
    ProcessPDF(ctx context.Context, pdfData io.Reader) (string, error)
    
    // ProcessPDFWithMetadata extracts text with additional metadata
    ProcessPDFWithMetadata(ctx context.Context, pdfData io.Reader) (*OCRResult, error)
}
```

### OCRResult Struct

```go
type OCRResult struct {
    Text               string        `json:"text"`                // Extracted text
    PageCount          int           `json:"page_count"`          // Number of pages
    Confidence         float32       `json:"confidence"`          // Average confidence (0.0-1.0)
    ProcessedAt        time.Time     `json:"processed_at"`        // Processing timestamp
    LanguageCodes      []string      `json:"language_codes"`      // Detected languages
    ProcessingDuration time.Duration `json:"processing_duration"` // Processing time
}
```

### Constructors

```go
// Create service with environment-based credentials
func NewGoogleVisionOCRService(ctx context.Context) (OCRService, error)

// Create service with explicit client (for testing)
func NewGoogleVisionOCRServiceWithClient(client *vision.ImageAnnotatorClient) OCRService
```

## Error Handling

The package provides specific error types for different failure scenarios:

```go
// Standard errors
var (
    ErrPDFTooLarge        = errors.New("PDF file size exceeds 20MB limit")
    ErrInvalidPDF         = errors.New("invalid or corrupted PDF document")
    ErrOCRFailed          = errors.New("OCR processing failed")
    ErrMissingCredentials = errors.New("missing Google Cloud credentials")
    ErrTooManyPages       = errors.New("PDF has too many pages (max 5)")
    ErrEmptyDocument      = errors.New("document contains no readable text")
    ErrContextCanceled    = errors.New("OCR processing was canceled")
)
```

Example error handling:

```go
result, err := ocrService.ProcessPDFWithMetadata(ctx, pdfReader)
if err != nil {
    switch {
    case errors.Is(err, ocr.ErrPDFTooLarge):
        log.Println("PDF is too large, try splitting it")
    case errors.Is(err, ocr.ErrTooManyPages):
        log.Println("PDF has too many pages, try async processing")
    case errors.Is(err, ocr.ErrMissingCredentials):
        log.Println("Please configure Google Cloud credentials")
    default:
        log.Printf("OCR failed: %v", err)
    }
    return
}
```

## Advanced Usage

### Processing with Metadata

```go
result, err := ocrService.ProcessPDFWithMetadata(ctx, pdfReader)
if err != nil {
    log.Fatalf("OCR failed: %v", err)
}

fmt.Printf("Results:\n")
fmt.Printf("  Pages: %d\n", result.PageCount)
fmt.Printf("  Confidence: %.1f%%\n", result.Confidence*100)
fmt.Printf("  Languages: %v\n", result.LanguageCodes)
fmt.Printf("  Duration: %v\n", result.ProcessingDuration)
fmt.Printf("  Text length: %d chars\n", len(result.Text))
```

### Batch Processing

```go
files := []string{"doc1.pdf", "doc2.pdf", "doc3.pdf"}

for _, filename := range files {
    file, err := os.Open(filename)
    if err != nil {
        log.Printf("Failed to open %s: %v", filename, err)
        continue
    }
    defer file.Close()
    
    // Use timeout per file
    fileCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
    defer cancel()
    
    result, err := ocrService.ProcessPDFWithMetadata(fileCtx, file)
    if err != nil {
        log.Printf("Failed to process %s: %v", filename, err)
        continue
    }
    
    log.Printf("%s: %d pages, %.1f%% confidence", 
        filename, result.PageCount, result.Confidence*100)
}
```

### Testing

For unit tests, inject a mock client:

```go
func TestOCRService(t *testing.T) {
    mockClient := &mockVisionClient{
        // Configure mock responses
    }
    
    service := ocr.NewGoogleVisionOCRServiceWithClient(mockClient)
    
    // Test your code
    result, err := service.ProcessPDF(ctx, pdfReader)
    // Assert results...
}
```

## Performance Tips

1. **Reuse Service**: Create the OCR service once and reuse it
2. **Context Timeouts**: Use appropriate timeouts for your use case
3. **File Size**: Keep PDFs under 20MB for best performance
4. **Page Count**: Limit to 5 pages for synchronous processing
5. **Concurrent Processing**: Process multiple files concurrently with goroutines

## Troubleshooting

### Authentication Issues

```
Error: missing Google Cloud credentials
```

**Solution**: Set `GOOGLE_APPLICATION_CREDENTIALS` or `GOOGLE_CREDENTIALS` environment variable.

### File Size Issues

```
Error: PDF file size exceeds the maximum limit (20MB)
```

**Solutions**:
- Compress the PDF
- Split into smaller files
- Use asynchronous processing

### API Quota Issues

```
Error: OCR processing failed: quota exceeded
```

**Solutions**:
- Check Google Cloud Console for quota limits
- Request quota increase
- Implement retry logic with exponential backoff

### Empty Results

```
Error: document contains no readable text
```

**Possible causes**:
- Scanned document with poor quality
- Image-only PDF without text layer
- Corrupted PDF file

**Solutions**:
- Improve scan quality
- Try different PDF
- Use image preprocessing

## License

This package is part of the Tools CLI project and follows the same license terms.