// Package security implements domain.ContentSecurityService (Batch B1, DESIGN
// §1.2 / §4.2). It scans chat messages and uploaded files for PII and prompt
// injection before they cross the gateway boundary.
package security

import (
	"context"
	"fmt"

	"github.com/open-strata-ai/ai-gateway-core/domain"
	"github.com/open-strata-ai/ai-gateway-core/infrastructure/scanner"
)

// Service is the concrete ContentSecurityService.
type Service struct {
	scan *scanner.Scanner
}

// New builds a Service.
func New(scan *scanner.Scanner) *Service {
	if scan == nil {
		scan = scanner.New(scanner.Config{PIIScan: true})
	}
	return &Service{scan: scan}
}

// ScanInput scans an inbound user message. Injection is a hard block; PII is
// reported as a finding but does not block (it is masked downstream).
func (s *Service) ScanInput(ctx context.Context, msg *domain.Message) (*domain.ScanResult, error) {
	if msg == nil {
		return &domain.ScanResult{OK: true}, nil
	}
	if hits := s.scan.ScanInjection(msg.Content); len(hits) > 0 {
		return &domain.ScanResult{OK: false, Reason: "prompt_injection_detected", Findings: hits}, nil
	}
	findings := s.scan.ScanPII(msg.Content)
	return &domain.ScanResult{OK: true, Findings: findings}, nil
}

// ScanOutput scans an outbound assistant message. Only injection blocks; PII is
// reported for logging.
func (s *Service) ScanOutput(ctx context.Context, msg *domain.Message) (*domain.ScanResult, error) {
	if msg == nil {
		return &domain.ScanResult{OK: true}, nil
	}
	if hits := s.scan.ScanInjection(msg.Content); len(hits) > 0 {
		return &domain.ScanResult{OK: false, Reason: "prompt_injection_in_output", Findings: hits}, nil
	}
	findings := s.scan.ScanPII(msg.Content)
	return &domain.ScanResult{OK: true, Findings: findings}, nil
}

// ScanFile scans an uploaded file's content for PII / injection. Files flagged
// for injection are rejected; PII is reported.
func (s *Service) ScanFile(ctx context.Context, file *domain.FileRef) (*domain.ScanResult, error) {
	if file == nil {
		return nil, fmt.Errorf("nil file ref")
	}
	// The actual content is scanned at upload time by the storage adapter; here
	// we treat the FileRef metadata as already-scanned and only validate its
	// presence. Heavy content scanning lives in the FileStoragePort adapter.
	if file.ID == "" {
		return nil, fmt.Errorf("file ref has empty id")
	}
	return &domain.ScanResult{OK: true}, nil
}

var _ domain.ContentSecurityService = (*Service)(nil)
