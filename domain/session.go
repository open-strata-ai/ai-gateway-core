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

// ModelBinding ties an Agent to a concrete upstream model (DESIGN §4.3.5).
type ModelBinding struct {
	Model    string `json:"model"`
	Provider string `json:"provider"`
}

// StateNode / Transition / StateMachine describe a declarative agent behaviour
// graph (DESIGN §4.3.5). They mirror the portal's AgentSpec contract.
type StateNode struct {
	ID   string `json:"id"`
	Label string `json:"label"`
	Type string `json:"type"` // start|end|normal
}

type Transition struct {
	From  string `json:"from"`
	To    string `json:"to"`
	Event string `json:"event"`
}

type StateMachine struct {
	Initial     string        `json:"initial"`
	States      []StateNode  `json:"states"`
	Transitions []Transition `json:"transitions"`
}

// Guardrail is a single safety rule attached to an Agent.
type Guardrail struct {
	ID          string `json:"id"`
	Type        string `json:"type"` // injection|pii|rate-limit|custom
	Description string `json:"description"`
}

// AgentSpec is a user-authored agent definition persisted by the gateway
// (EU-05 authoring). It is the writable counterpart of the read-only
// AgentSummary catalog projection. Wire tags are camelCase to match the
// portal AgentSpec contract 1:1.
type AgentSpec struct {
	ID           string        `json:"id"`
	TenantID     string        `json:"tenantId"`
	Name         string        `json:"name"`
	Description  string        `json:"description"`
	ModelBinding *ModelBinding `json:"modelBinding,omitempty"`
	StateMachine *StateMachine `json:"stateMachine,omitempty"`
	Guardrails   []Guardrail   `json:"guardrails,omitempty"`
	Status       string        `json:"status"` // draft|published|deprecated
	CreatedAt    int64         `json:"createdAt"`
	UpdatedAt    int64         `json:"updatedAt"`
}

// AgentRepository persists user-authored AgentSpecs (DESIGN §8: agent table).
// The production adapter is PostgreSQL; the offline stand-in is in-memory.
type AgentRepository interface {
	Save(a AgentSpec) error
	Get(id string) (AgentSpec, bool)
	List(tenantID string) []AgentSpec
	Delete(id string) error
}

// CreateAgentRequest is the payload for authoring a new AgentSpec.
type CreateAgentRequest struct {
	TenantID    string
	Name        string         `json:"name"`
	Description string         `json:"description"`
	ModelBinding *ModelBinding `json:"modelBinding,omitempty"`
	StateMachine *StateMachine `json:"stateMachine,omitempty"`
	Guardrails  []Guardrail    `json:"guardrails,omitempty"`
}

// UpdateAgentRequest carries editable fields for an existing AgentSpec.
type UpdateAgentRequest struct {
	Name         string         `json:"name"`
	Description  string         `json:"description"`
	ModelBinding *ModelBinding `json:"modelBinding,omitempty"`
	StateMachine *StateMachine `json:"stateMachine,omitempty"`
	Guardrails   []Guardrail    `json:"guardrails,omitempty"`
	Status       string         `json:"status"`
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
