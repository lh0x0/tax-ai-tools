package ocr

import (
	"context"
	"fmt"
	"io"
	"os"
	"strings"
	"time"

	vision "cloud.google.com/go/vision/v2/apiv1"
	"cloud.google.com/go/vision/v2/apiv1/visionpb"
	"google.golang.org/api/option"
)

const (
	// MaxFileSizeBytes is the maximum file size for synchronous processing (20MB)
	MaxFileSizeBytes = 20 * 1024 * 1024

	// MaxPagesSync is the maximum number of pages for synchronous processing
	MaxPagesSync = 5
)

// GoogleVisionOCRService implements OCRService using Google Cloud Vision API.
type GoogleVisionOCRService struct {
	client *vision.ImageAnnotatorClient
}

// NewGoogleVisionOCRService creates a new OCR service with credentials from environment.
// It expects either GOOGLE_APPLICATION_CREDENTIALS path or GOOGLE_CREDENTIALS JSON in env.
func NewGoogleVisionOCRService(ctx context.Context) (OCRService, error) {
	const op = "NewGoogleVisionOCRService"

	var client *vision.ImageAnnotatorClient
	var err error

	// Check for inline credentials first
	if credJSON := os.Getenv("GOOGLE_CREDENTIALS"); credJSON != "" {
		client, err = vision.NewImageAnnotatorClient(ctx, option.WithCredentialsJSON([]byte(credJSON)))
		if err != nil {
			return nil, WrapOCRError(op, err, "failed to create client with GOOGLE_CREDENTIALS")
		}
	} else if credFile := os.Getenv("GOOGLE_APPLICATION_CREDENTIALS"); credFile != "" {
		// Use credentials file
		client, err = vision.NewImageAnnotatorClient(ctx, option.WithCredentialsFile(credFile))
		if err != nil {
			return nil, WrapOCRError(op, err, "failed to create client with GOOGLE_APPLICATION_CREDENTIALS")
		}
	} else {
		// Try default credentials as fallback
		client, err = vision.NewImageAnnotatorClient(ctx)
		if err != nil {
			return nil, WrapOCRError(op, ErrMissingCredentials, "no credentials found in environment")
		}
	}

	return &GoogleVisionOCRService{
		client: client,
	}, nil
}

// NewGoogleVisionOCRServiceWithClient creates a new OCR service with an explicit client (for testing).
func NewGoogleVisionOCRServiceWithClient(client *vision.ImageAnnotatorClient) OCRService {
	return &GoogleVisionOCRService{
		client: client,
	}
}

// ProcessPDF extracts text from a PDF document.
func (g *GoogleVisionOCRService) ProcessPDF(ctx context.Context, pdfData io.Reader) (string, error) {
	result, err := g.ProcessPDFWithMetadata(ctx, pdfData)
	if err != nil {
		return "", err
	}
	return result.Text, nil
}

// ProcessPDFWithMetadata extracts text from a PDF document with additional metadata.
func (g *GoogleVisionOCRService) ProcessPDFWithMetadata(ctx context.Context, pdfData io.Reader) (*OCRResult, error) {
	const op = "ProcessPDFWithMetadata"
	startTime := time.Now()

	// Read PDF data
	pdfBytes, err := io.ReadAll(pdfData)
	if err != nil {
		return nil, WrapOCRError(op, err, "failed to read PDF data")
	}

	// Validate file size
	if len(pdfBytes) > MaxFileSizeBytes {
		return nil, WrapOCRError(op, ErrPDFTooLarge, fmt.Sprintf("file size: %d bytes", len(pdfBytes)))
	}

	// Validate PDF header
	if len(pdfBytes) < 4 || string(pdfBytes[:4]) != "%PDF" {
		return nil, WrapOCRError(op, ErrInvalidPDF, "missing PDF header")
	}

	// Prepare the request
	req := &visionpb.BatchAnnotateFilesRequest{
		Requests: []*visionpb.AnnotateFileRequest{
			{
				InputConfig: &visionpb.InputConfig{
					GcsSource: nil, // We're using inline content
					Content:   pdfBytes,
					MimeType:  "application/pdf",
				},
				Features: []*visionpb.Feature{
					{
						Type: visionpb.Feature_DOCUMENT_TEXT_DETECTION,
					},
				},
				Pages: nil, // Process all pages
			},
		},
	}

	// Call the Vision API
	resp, err := g.client.BatchAnnotateFiles(ctx, req)
	if err != nil {
		return nil, WrapOCRError(op, ErrOCRFailed, fmt.Sprintf("Vision API call failed: %v", err))
	}

	// Check for API errors
	if len(resp.Responses) == 0 {
		return nil, WrapOCRError(op, ErrOCRFailed, "no response from Vision API")
	}

	fileResp := resp.Responses[0]
	if fileResp.Error != nil {
		return nil, WrapOCRError(op, ErrOCRFailed, fmt.Sprintf("Vision API error: %s", fileResp.Error.Message))
	}

	// Process the response
	result, err := g.processVisionResponse(fileResp)
	if err != nil {
		return nil, WrapOCRError(op, err, "failed to process Vision API response")
	}

	// Set processing duration
	result.ProcessedAt = time.Now()
	result.ProcessingDuration = result.ProcessedAt.Sub(startTime)

	return result, nil
}

// processVisionResponse processes the Vision API response and extracts text with metadata.
func (g *GoogleVisionOCRService) processVisionResponse(fileResp *visionpb.AnnotateFileResponse) (*OCRResult, error) {
	if len(fileResp.Responses) == 0 {
		return nil, ErrEmptyDocument
	}

	var allText strings.Builder
	var confidenceSum float32
	var confidenceCount int
	var languageSet = make(map[string]bool)
	pageCount := len(fileResp.Responses)

	// Check page limit
	if pageCount > MaxPagesSync {
		return nil, WrapOCRError("processVisionResponse", ErrTooManyPages, fmt.Sprintf("document has %d pages", pageCount))
	}

	for pageIdx, page := range fileResp.Responses {
		if page.Error != nil {
			return nil, fmt.Errorf("error processing page %d: %s", pageIdx+1, page.Error.Message)
		}

		// Extract full text annotation
		if page.FullTextAnnotation != nil {
			// Add page separator (except for first page)
			if pageIdx > 0 {
				allText.WriteString("\n\n--- Page ")
				allText.WriteString(fmt.Sprintf("%d", pageIdx+1))
				allText.WriteString(" ---\n\n")
			}

			// Add text content
			allText.WriteString(page.FullTextAnnotation.Text)

			// Collect confidence scores from text annotations
			for _, textAnnotation := range page.TextAnnotations {
				if textAnnotation.Confidence > 0 {
					confidenceSum += textAnnotation.Confidence
					confidenceCount++
				}
			}

			// Collect language information
			for _, pageInfo := range page.FullTextAnnotation.Pages {
				for _, block := range pageInfo.Blocks {
					for _, paragraph := range block.Paragraphs {
						for _, word := range paragraph.Words {
							for _, symbol := range word.Symbols {
								if symbol.Property != nil && symbol.Property.DetectedLanguages != nil {
									for _, lang := range symbol.Property.DetectedLanguages {
										if lang.LanguageCode != "" {
											languageSet[lang.LanguageCode] = true
										}
									}
								}
							}
						}
					}
				}
			}
		}
	}

	// Calculate average confidence
	var avgConfidence float32
	if confidenceCount > 0 {
		avgConfidence = confidenceSum / float32(confidenceCount)
	}

	// Convert language set to slice
	var languages []string
	for lang := range languageSet {
		languages = append(languages, lang)
	}

	// Check if we extracted any text
	extractedText := allText.String()
	if strings.TrimSpace(extractedText) == "" {
		return nil, ErrEmptyDocument
	}

	return &OCRResult{
		Text:          extractedText,
		PageCount:     pageCount,
		Confidence:    avgConfidence,
		LanguageCodes: languages,
	}, nil
}

// Close closes the underlying Vision client.
func (g *GoogleVisionOCRService) Close() error {
	if g.client != nil {
		return g.client.Close()
	}
	return nil
}
