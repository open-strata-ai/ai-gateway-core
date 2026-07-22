// Package session implements domain "chat session" use cases (Batch B1, EU-01
// chat, EU-04 chat history, EU-03 file upload, EU-05 agent catalog, DESIGN §2.3
// / §3.2). It orchestrates the existing chat pipeline, content security, file
// storage, and session persistence.
package session

import (
	"context"
	"fmt"
	"time"

	"github.com/open-strata-ai/ai-gateway-core/application/chat"
	"github.com/open-strata-ai/ai-gateway-core/domain"
)

// ChatSessionAppService is the gateway session facade (DESIGN §3.2).
type ChatSessionAppService interface {
	OpenSession(ctx context.Context, req *domain.OpenSessionRequest) (*domain.Session, error)
	SendMessage(ctx context.Context, req *SendMessageRequest) (<-chan domain.TokenChunk, error)
	UploadFile(ctx context.Context, req *domain.FileUploadRequest) (*domain.FileRef, error)
	GetChatHistory(ctx context.Context, sessionID string) ([]domain.Message, error)
	ListAvailableAgents(ctx context.Context, tenantID string) ([]domain.AgentSummary, error)
	// EU-05 authoring: persist + read user-authored AgentSpecs.
	CreateAgent(ctx context.Context, req *domain.CreateAgentRequest) (*domain.AgentSpec, error)
	GetAgent(ctx context.Context, id string) (*domain.AgentSpec, error)
	ListAgents(ctx context.Context, tenantID string) ([]domain.AgentSpec, error)
	UpdateAgent(ctx context.Context, id string, req *domain.UpdateAgentRequest) (*domain.AgentSpec, error)
	DeleteAgent(ctx context.Context, id string) error
}

// SendMessageRequest carries a single turn for an open session.
type SendMessageRequest struct {
	SessionID string
	Message   domain.Message
	// Model is optional; when empty the gateway router uses the tenant default.
	Model string
}

// Deps are the collaborators of the session Service.
type Deps struct {
	Chat     *chat.Service
	Security domain.ContentSecurityService
	Storage  domain.FileStoragePort
	Sessions domain.SessionRepository
	Agents   domain.AgentCatalog
	AgentRepo domain.AgentRepository
	Tracer   domain.TracingPort
}

// Service implements ChatSessionAppService.
type Service struct {
	d   Deps
	now func() time.Time
	gen func() string
}

// New builds a session Service.
func New(d Deps) *Service {
	return &Service{d: d, now: time.Now, gen: defaultID}
}

func defaultID() string {
	return fmt.Sprintf("%d", time.Now().UnixNano())
}

// OpenSession creates a fresh chat session for (tenantID, agentID).
func (s *Service) OpenSession(ctx context.Context, req *domain.OpenSessionRequest) (*domain.Session, error) {
	if req.TenantID == "" || req.AgentID == "" {
		return nil, domain.NewError(domain.ErrInvalidRequest, 400, "tenant_id and agent_id are required")
	}
	sess := domain.Session{
		ID:        s.gen(),
		TenantID:  req.TenantID,
		AgentID:   req.AgentID,
		Messages:  nil,
		CreatedAt: s.now().Unix(),
	}
	if err := s.d.Sessions.Save(sess); err != nil {
		return nil, err
	}
	return &sess, nil
}

// SendMessage runs the full chat pipeline for a session turn and streams the
// assistant reply as TokenChunks.
func (s *Service) SendMessage(ctx context.Context, req *SendMessageRequest) (<-chan domain.TokenChunk, error) {
	sess, ok := s.d.Sessions.Get(req.SessionID)
	if !ok {
		return nil, domain.NewError(domain.ErrInvalidRequest, 404, "session not found")
	}
	if req.Message.Role != domain.RoleUser {
		return nil, domain.NewError(domain.ErrInvalidRequest, 400, "only user messages may be sent")
	}
	// 1) scan inbound content (EU-03/§1.2)
	if s.d.Security != nil {
		res, err := s.d.Security.ScanInput(ctx, &req.Message)
		if err != nil {
			return nil, err
		}
		if !res.OK {
			return nil, domain.NewError(domain.ErrRiskRejected, 400, res.Reason)
		}
	}
	// 2) persist the user turn
	if err := s.d.Sessions.AppendMessage(req.SessionID, req.Message); err != nil {
		return nil, err
	}
	sess.Messages = append(sess.Messages, req.Message)

	// 3) route through the existing chat pipeline (risk→cache→route→quota→provider)
	chatReq := domain.ChatRequest{
		TenantID:   sess.TenantID,
		Model:      req.Model,
		Capability: domain.CapChat,
		Messages:   sess.Messages,
	}
	resp, err := s.d.Chat.Complete(ctx, chatReq)
	if err != nil {
		return nil, err
	}

	// 4) scan outbound content
	if s.d.Security != nil {
		outMsg := &domain.Message{Role: domain.RoleAssistant, Content: resp.Content}
		if res, serr := s.d.Security.ScanOutput(ctx, outMsg); serr == nil && !res.OK {
			return nil, domain.NewError(domain.ErrRiskRejected, 400, res.Reason)
		}
	}

	// 5) persist the assistant turn
	if err := s.d.Sessions.AppendMessage(req.SessionID, domain.Message{Role: domain.RoleAssistant, Content: resp.Content}); err != nil {
		return nil, err
	}

	// 6) stream as chunks
	ch := make(chan domain.TokenChunk, 2)
	ch <- domain.TokenChunk{Delta: resp.Content, Done: false}
	ch <- domain.TokenChunk{Usage: resp.Usage, Done: true}
	close(ch)
	return ch, nil
}

// UploadFile scans + stores an uploaded file and returns a FileRef (EU-03).
func (s *Service) UploadFile(ctx context.Context, req *domain.FileUploadRequest) (*domain.FileRef, error) {
	if s.d.Storage == nil {
		return nil, fmt.Errorf("no file storage configured")
	}
	ref, err := s.d.Storage.Upload(ctx, req)
	if err != nil {
		return nil, err
	}
	// content security post-scan
	if s.d.Security != nil {
		if res, serr := s.d.Security.ScanFile(ctx, ref); serr == nil && !res.OK {
			return nil, domain.NewError(domain.ErrRiskRejected, 400, res.Reason)
		}
	}
	return ref, nil
}

// GetChatHistory returns the ordered message list for a session (EU-04).
func (s *Service) GetChatHistory(ctx context.Context, sessionID string) ([]domain.Message, error) {
	sess, ok := s.d.Sessions.Get(sessionID)
	if !ok {
		return nil, domain.NewError(domain.ErrInvalidRequest, 404, "session not found")
	}
	out := make([]domain.Message, len(sess.Messages))
	copy(out, sess.Messages)
	return out, nil
}

// ListAvailableAgents returns the tenant-visible Agent catalog (EU-05).
func (s *Service) ListAvailableAgents(ctx context.Context, tenantID string) ([]domain.AgentSummary, error) {
	if s.d.Agents == nil {
		return nil, nil
	}
	return s.d.Agents.ListAvailable(tenantID), nil
}

// CreateAgent persists a new user-authored AgentSpec (EU-05 authoring).
func (s *Service) CreateAgent(ctx context.Context, req *domain.CreateAgentRequest) (*domain.AgentSpec, error) {
	if s.d.AgentRepo == nil {
		return nil, domain.NewError(domain.ErrInvalidRequest, 404, "agent repository not configured")
	}
	if req.TenantID == "" {
		return nil, domain.NewError(domain.ErrInvalidRequest, 400, "tenant_id is required")
	}
	if req.Name == "" {
		return nil, domain.NewError(domain.ErrInvalidRequest, 400, "agent name is required")
	}
	now := s.now().Unix()
	spec := domain.AgentSpec{
		ID:           s.gen(),
		TenantID:     req.TenantID,
		Name:          req.Name,
		Description:  req.Description,
		ModelBinding: req.ModelBinding,
		StateMachine: req.StateMachine,
		Guardrails:   req.Guardrails,
		Status:       "draft",
		CreatedAt:    now,
		UpdatedAt:    now,
	}
	if err := s.d.AgentRepo.Save(spec); err != nil {
		return nil, err
	}
	return &spec, nil
}

// GetAgent returns a single AgentSpec by id (user repo first, then catalog).
func (s *Service) GetAgent(ctx context.Context, id string) (*domain.AgentSpec, error) {
	if s.d.AgentRepo != nil {
		if a, ok := s.d.AgentRepo.Get(id); ok {
			return &a, nil
		}
	}
	if s.d.Agents != nil {
		for _, sum := range s.d.Agents.ListAvailable("") {
			if sum.ID == id {
				return &domain.AgentSpec{
					ID:          sum.ID,
					Name:        sum.Name,
					Description: sum.Description,
					Status:      sum.Status,
				}, nil
			}
		}
	}
	return nil, domain.NewError(domain.ErrInvalidRequest, 404, "agent not found")
}

// ListAgents returns the tenant's persisted AgentSpecs (EU-05 authoring list).
func (s *Service) ListAgents(ctx context.Context, tenantID string) ([]domain.AgentSpec, error) {
	if s.d.AgentRepo == nil {
		return nil, nil
	}
	return s.d.AgentRepo.List(tenantID), nil
}

// UpdateAgent applies editable fields to an existing AgentSpec.
func (s *Service) UpdateAgent(ctx context.Context, id string, req *domain.UpdateAgentRequest) (*domain.AgentSpec, error) {
	if s.d.AgentRepo == nil {
		return nil, domain.NewError(domain.ErrInvalidRequest, 404, "agent not found")
	}
	existing, ok := s.d.AgentRepo.Get(id)
	if !ok {
		return nil, domain.NewError(domain.ErrInvalidRequest, 404, "agent not found")
	}
	existing.Name = req.Name
	existing.Description = req.Description
	existing.ModelBinding = req.ModelBinding
	existing.StateMachine = req.StateMachine
	existing.Guardrails = req.Guardrails
	if req.Status != "" {
		existing.Status = req.Status
	}
	existing.UpdatedAt = s.now().Unix()
	if err := s.d.AgentRepo.Save(existing); err != nil {
		return nil, err
	}
	return &existing, nil
}

// DeleteAgent removes a persisted AgentSpec.
func (s *Service) DeleteAgent(ctx context.Context, id string) error {
	if s.d.AgentRepo == nil {
		return domain.NewError(domain.ErrInvalidRequest, 404, "agent not found")
	}
	if _, ok := s.d.AgentRepo.Get(id); !ok {
		return domain.NewError(domain.ErrInvalidRequest, 404, "agent not found")
	}
	return s.d.AgentRepo.Delete(id)
}

var _ ChatSessionAppService = (*Service)(nil)
