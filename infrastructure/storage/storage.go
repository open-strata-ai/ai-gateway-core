// Package storage implements domain.FileStoragePort (Batch B1, EU-03, DESIGN
// §6.2). Production uses MinIO; the offline stand-ins are LocalAdapter (disk)
// and MemoryAdapter (tests).
package storage

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"sync"

	"github.com/open-strata-ai/ai-gateway-core/domain"
	"github.com/open-strata-ai/ai-gateway-core/infrastructure/scanner"
)

// LocalAdapter stores uploads on the local filesystem under BaseDir. It scans
// content with the embedded scanner and rejects injection attempts.
type LocalAdapter struct {
	baseDir string
	scan    *scanner.Scanner
	mu      sync.Mutex
}

// NewLocal builds a disk-backed adapter rooted at baseDir.
func NewLocal(baseDir string, scan *scanner.Scanner) *LocalAdapter {
	if scan == nil {
		scan = scanner.New(scanner.Config{PIIScan: true})
	}
	return &LocalAdapter{baseDir: baseDir, scan: scan}
}

func (a *LocalAdapter) Upload(ctx context.Context, file *domain.FileUploadRequest) (*domain.FileRef, error) {
	if hits := a.scan.ScanInjection(string(file.Content)); len(hits) > 0 {
		return nil, domain.NewError(domain.ErrRiskRejected, 400, "injection detected in upload")
	}
	sum := sha256.Sum256(file.Content)
	id := hex.EncodeToString(sum[:])[:16]
	rel := filepath.Join(file.TenantID, "uploads", file.SessionID, id+"-"+file.FileName)
	path := filepath.Join(a.baseDir, rel)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return nil, err
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	if err := os.WriteFile(path, file.Content, 0o600); err != nil {
		return nil, err
	}
	return &domain.FileRef{
		ID:          id,
		ContentType: file.ContentType,
		Size:        file.Size,
		URL:         "file://" + path,
	}, nil
}

func (a *LocalAdapter) Download(ctx context.Context, ref *domain.FileRef) ([]byte, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	p := ref.URL[len("file://"):]
	return os.ReadFile(p)
}

func (a *LocalAdapter) ScanContent(ctx context.Context, ref *domain.FileRef) (*domain.ScanResult, error) {
	data, err := a.Download(ctx, ref)
	if err != nil {
		return nil, err
	}
	findings := a.scan.ScanPII(string(data))
	injection := a.scan.ScanInjection(string(data))
	if len(injection) > 0 {
		return &domain.ScanResult{OK: false, Reason: "injection_in_file", Findings: injection}, nil
	}
	return &domain.ScanResult{OK: true, Findings: findings}, nil
}

var _ domain.FileStoragePort = (*LocalAdapter)(nil)

// MemoryAdapter is an in-memory FileStoragePort used by offline tests. It does
// not touch disk.
type MemoryAdapter struct {
	mu    sync.Mutex
	files map[string][]byte
	scan  *scanner.Scanner
}

// NewMemory builds an in-memory adapter.
func NewMemory() *MemoryAdapter {
	return &MemoryAdapter{files: map[string][]byte{}, scan: scanner.New(scanner.Config{PIIScan: true})}
}

func (a *MemoryAdapter) Upload(ctx context.Context, file *domain.FileUploadRequest) (*domain.FileRef, error) {
	if hits := a.scan.ScanInjection(string(file.Content)); len(hits) > 0 {
		return nil, domain.NewError(domain.ErrRiskRejected, 400, "injection detected in upload")
	}
	sum := sha256.Sum256(file.Content)
	id := hex.EncodeToString(sum[:])[:16]
	a.mu.Lock()
	a.files[id] = file.Content
	a.mu.Unlock()
	return &domain.FileRef{
		ID:          id,
		ContentType: file.ContentType,
		Size:        file.Size,
		URL:         fmt.Sprintf("mem://%s/%s", file.TenantID, id),
	}, nil
}

func (a *MemoryAdapter) Download(ctx context.Context, ref *domain.FileRef) ([]byte, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	data, ok := a.files[ref.ID]
	if !ok {
		return nil, fmt.Errorf("file not found: %s", ref.ID)
	}
	return data, nil
}

func (a *MemoryAdapter) ScanContent(ctx context.Context, ref *domain.FileRef) (*domain.ScanResult, error) {
	data, err := a.Download(ctx, ref)
	if err != nil {
		return nil, err
	}
	findings := a.scan.ScanPII(string(data))
	if hits := a.scan.ScanInjection(string(data)); len(hits) > 0 {
		return &domain.ScanResult{OK: false, Reason: "injection_in_file", Findings: hits}, nil
	}
	return &domain.ScanResult{OK: true, Findings: findings}, nil
}

var _ domain.FileStoragePort = (*MemoryAdapter)(nil)
