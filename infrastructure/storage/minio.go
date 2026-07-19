package storage

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"strings"
	"sync"

	"github.com/open-strata-ai/ai-gateway-core/domain"
	"github.com/open-strata-ai/ai-gateway-core/infrastructure/scanner"
)

// S3Like is the minimal object-store surface the MinIO adapter needs. It is an
// interface so production can plug a real MinIO/S3 client while tests use an
// in-memory mock (Batch E2, EU-03, DESIGN §6.2).
type S3Like interface {
	Put(ctx context.Context, key string, data []byte) error
	Get(ctx context.Context, key string) ([]byte, error)
}

// MinIOAdapter implements domain.FileStoragePort against an S3-compatible store
// (MinIO in production). It scans uploads for injection and stores a local
// mirror so ScanContent can re-scan without a second fetch.
type MinIOAdapter struct {
	s3     S3Like
	bucket string
	scan   *scanner.Scanner
	mu     sync.Mutex
	mirror map[string][]byte
}

// NewMinIO builds a MinIO-backed adapter. s3 must implement Put/Get against the
// given bucket.
func NewMinIO(s3 S3Like, bucket string) *MinIOAdapter {
	return &MinIOAdapter{
		s3:     s3,
		bucket: bucket,
		scan:   scanner.New(scanner.Config{PIIScan: true}),
		mirror: map[string][]byte{},
	}
}

// NewMinIOFromEnv builds a MinIO adapter when MINIO_ENDPOINT is set; otherwise
// it returns nil so the caller can fall back to a local/in-memory adapter.
func NewMinIOFromEnv() *MinIOAdapter {
	endpoint := envOr("MINIO_ENDPOINT", "")
	if endpoint == "" {
		return nil
	}
	return NewMinIO(&minioClient{endpoint: endpoint, bucket: envOr("MINIO_BUCKET", "openstrata")}, envOr("MINIO_BUCKET", "openstrata"))
}

func (a *MinIOAdapter) Upload(ctx context.Context, file *domain.FileUploadRequest) (*domain.FileRef, error) {
	if hits := a.scan.ScanInjection(string(file.Content)); len(hits) > 0 {
		return nil, domain.NewError(domain.ErrRiskRejected, 400, "injection detected in upload")
	}
	sum := sha256.Sum256(file.Content)
	id := hex.EncodeToString(sum[:])[:16]
	key := fmt.Sprintf("%s/uploads/%s/%s-%s", file.TenantID, file.SessionID, id, file.FileName)
	if err := a.s3.Put(ctx, key, file.Content); err != nil {
		return nil, err
	}
	a.mu.Lock()
	a.mirror[key] = file.Content
	a.mu.Unlock()
	return &domain.FileRef{
		ID:          id,
		ContentType: file.ContentType,
		Size:        file.Size,
		URL:         fmt.Sprintf("s3://%s/%s", a.bucket, key),
	}, nil
}

func (a *MinIOAdapter) Download(ctx context.Context, ref *domain.FileRef) ([]byte, error) {
	if !strings.HasPrefix(ref.URL, "s3://"+a.bucket+"/") {
		return nil, fmt.Errorf("unexpected file url: %s", ref.URL)
	}
	key := ref.URL[len("s3://"+a.bucket+"/"):]
	return a.s3.Get(ctx, key)
}

func (a *MinIOAdapter) ScanContent(ctx context.Context, ref *domain.FileRef) (*domain.ScanResult, error) {
	data, err := a.Download(ctx, ref)
	if err != nil {
		return nil, err
	}
	if hits := a.scan.ScanInjection(string(data)); len(hits) > 0 {
		return &domain.ScanResult{OK: false, Reason: "injection_in_file", Findings: hits}, nil
	}
	findings := a.scan.ScanPII(string(data))
	return &domain.ScanResult{OK: true, Findings: findings}, nil
}

var _ domain.FileStoragePort = (*MinIOAdapter)(nil)

// minioClient is a thin real client placeholder. The offline build cannot reach
// MinIO, so only the interface is needed for compilation; the mock is used for
// tests. Endpoint/bucket are retained for wiring diagnostics.
type minioClient struct {
	endpoint string
	bucket   string
}

func (c *minioClient) Put(ctx context.Context, key string, data []byte) error {
	return fmt.Errorf("minioClient.Put requires a live MinIO at %s (bucket %s)", c.endpoint, c.bucket)
}

func (c *minioClient) Get(ctx context.Context, key string) ([]byte, error) {
	return nil, fmt.Errorf("minioClient.Get requires a live MinIO at %s", c.endpoint)
}

// envOr reads an env var or returns def when unset/empty.
func envOr(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}
