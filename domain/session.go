package domain

// This file adds the session / file / content-security domain types required by
// Batch B1 (EU-01 Chat, EU-04 Chat History, EU-03 File Upload, DV-14 Tools).
// They are intentionally framework-free and live in the domain layer (DDD, DESIGN §3).

// TokenChunk is a single streaming fragment emitted by ChatSessionAppService.
// It aliases ChatChunk so the HTTP SSE writer can reuse the same wire shape.
type TokenChunk = ChatChunk

// ScanType enumerates the direction of a content scan.
type ScanType string

const (
	ScanInput  ScanType = "input"
	ScanOutput ScanType = "output"
	ScanFile   ScanType = "file"
)

// ScanResult reports the outcome of a content security scan.
type ScanResult struct {
	OK       bool     // false => blocked / rejected
	Reason   string   // machine-readable reason when OK=false
	Findings []string // human-readable findings (e.g. matched PII patterns)
}

// Session is a chat session between an end user and an Agent (EU-04).
type Session struct {
	ID        string    `json:"id"`
	TenantID  string    `json:"tenant_id"`
	AgentID   string    `json:"agent_id"`
	Messages  []Message `json:"messages"`
	CreatedAt int64     `json:"created_at"`
}

// AgentSummary is the lightweight catalog entry returned by ListAvailableAgents
// (EU-05). The authoritative source is ai-platform-api; the gateway caches a
// read-only projection here.
type AgentSummary struct {
	ID          string `json:"id"`
	Name        string `json:"name"`
	Description string `json:"description"`
	Status      string `json:"status"` // draft|published|deprecated
}

// FileRef is the handle returned after a successful upload (EU-03). It is passed
// to the Agent runtime as an attachment reference.
type FileRef struct {
	ID          string `json:"id"`
	ContentType string `json:"content_type"`
	Size        int64  `json:"size"`
	URL         string `json:"url"`
}

// OpenSessionRequest opens a new chat session for (tenantID, agentID).
type OpenSessionRequest struct {
	TenantID string `json:"tenant_id"`
	AgentID  string `json:"agent_id"`
}

// FileUploadRequest carries an inbound multipart upload in normalized form.
type FileUploadRequest struct {
	TenantID    string
	SessionID   string
	FileName    string
	ContentType string
	Size        int64
	Content     []byte
}

// SessionRepository persists chat sessions (DESIGN §8: chat session table).
// The production adapter is PostgreSQL; the offline stand-in is in-memory.
type SessionRepository interface {
	Save(s Session) error
	Get(id string) (Session, bool)
	AppendMessage(id string, m Message) error
	ListByTenant(tenantID string) []Session
}
