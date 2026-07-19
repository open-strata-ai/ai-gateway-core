package storage

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"testing"

	"github.com/open-strata-ai/ai-gateway-core/domain"
)

// memS3 is an in-memory S3Like used to verify MinIOAdapter offline (E2).
type memS3 struct {
	mu   sync.Mutex
	data map[string][]byte
}

func newMemS3() *memS3 { return &memS3{data: map[string][]byte{}} }

func (m *memS3) Put(ctx context.Context, key string, data []byte) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.data[key] = data
	return nil
}

func (m *memS3) Get(ctx context.Context, key string) ([]byte, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	d, ok := m.data[key]
	if !ok {
		return nil, fmt.Errorf("object not found: %s", key)
	}
	return d, nil
}

func TestMinIO_UploadAndDownload(t *testing.T) {
	a := NewMinIO(newMemS3(), "bucket")
	ref, err := a.Upload(context.Background(), &domain.FileUploadRequest{
		TenantID: "t1", SessionID: "s1", FileName: "x.txt",
		ContentType: "text/plain", Size: 5, Content: []byte("hello"),
	})
	if err != nil {
		t.Fatalf("upload: %v", err)
	}
	if ref.ID == "" {
		t.Fatal("expected non-empty file id")
	}
	if !strings.HasPrefix(ref.URL, "s3://bucket/") {
		t.Fatalf("expected s3 url, got %s", ref.URL)
	}
	data, err := a.Download(context.Background(), ref)
	if err != nil {
		t.Fatalf("download: %v", err)
	}
	if string(data) != "hello" {
		t.Fatalf("content mismatch: %q", data)
	}
}

func TestMinIO_ScanContentClean(t *testing.T) {
	a := NewMinIO(newMemS3(), "bucket")
	ref, err := a.Upload(context.Background(), &domain.FileUploadRequest{
		TenantID: "t1", SessionID: "s1", FileName: "safe.txt",
		Content: []byte("hello world"),
	})
	if err != nil {
		t.Fatalf("upload: %v", err)
	}
	res, err := a.ScanContent(context.Background(), ref)
	if err != nil {
		t.Fatalf("scan: %v", err)
	}
	if !res.OK {
		t.Fatalf("expected clean file, got %+v", res)
	}
}
