package session_test

import (
	"context"
	"testing"

	"github.com/open-strata-ai/ai-gateway-core/application/chat"
	"github.com/open-strata-ai/ai-gateway-core/application/ratelimit"
	"github.com/open-strata-ai/ai-gateway-core/application/riskcontrol"
	"github.com/open-strata-ai/ai-gateway-core/application/routing"
	"github.com/open-strata-ai/ai-gateway-core/application/session"
	"github.com/open-strata-ai/ai-gateway-core/domain"
	"github.com/open-strata-ai/ai-gateway-core/infrastructure/cache"
	"github.com/open-strata-ai/ai-gateway-core/infrastructure/catalog"
	"github.com/open-strata-ai/ai-gateway-core/infrastructure/provider"
	"github.com/open-strata-ai/ai-gateway-core/infrastructure/repository/memory"
	"github.com/open-strata-ai/ai-gateway-core/infrastructure/storage"
)

func newChatSvc() *chat.Service {
	cat := catalog.NewWithCards(
		domain.ModelCard{ModelID: "cloud-qwen-max", Source: domain.SourceThirdParty, Capability: domain.CapChat, Health: domain.HealthHealthy, RateLimit: domain.RateLimit{QPSPerTenant: 100, TPMPerTenant: 1000000}},
	)
	reg := provider.NewRegistry()
	reg.Register("cloud-qwen-max", provider.New(provider.Config{Kind: provider.KindQwen, ModelID: "cloud-qwen-max"}))
	return chat.New(chat.Deps{
		Router:    routing.New(cat, routing.Config{DefaultModel: "cloud-qwen-max"}),
		Catalog:   cat,
		Limiter:   ratelimit.New(ratelimit.Config{}),
		Breaker:   nil,
		Risk:      riskcontrol.New(riskcontrol.Config{PIIScan: true}),
		Cache:     cache.New(false),
		Providers: reg,
	}, chat.Config{})
}

func newSessionSvc() (*session.Service, *memory.SessionRepository, *catalog.AgentInMemory) {
	repo := memory.NewSessionRepository()
	agents := catalog.NewAgentInMemory()
	agents.Put(domain.AgentSummary{ID: "a1", Name: "Helper", Status: "published"})
	svc := session.New(session.Deps{
		Chat:     newChatSvc(),
		Security: nil,
		Storage:  storage.NewMemory(),
		Sessions: repo,
		Agents:   agents,
	})
	return svc, repo, agents
}

func TestOpenSession_Saves(t *testing.T) {
	svc, _, _ := newSessionSvc()
	sess, err := svc.OpenSession(context.Background(), &domain.OpenSessionRequest{TenantID: "t1", AgentID: "a1"})
	if err != nil {
		t.Fatalf("unexpected err: %v", err)
	}
	if sess.ID == "" || sess.TenantID != "t1" || sess.AgentID != "a1" {
		t.Fatalf("bad session: %+v", sess)
	}
}

func TestSendMessage_StreamsAndPersists(t *testing.T) {
	svc, repo, _ := newSessionSvc()
	sess, _ := svc.OpenSession(context.Background(), &domain.OpenSessionRequest{TenantID: "t1", AgentID: "a1"})
	ch, err := svc.SendMessage(context.Background(), &session.SendMessageRequest{
		SessionID: sess.ID,
		Message:   domain.Message{Role: domain.RoleUser, Content: "hello"},
	})
	if err != nil {
		t.Fatalf("unexpected err: %v", err)
	}
	var content string
	var gotUsage bool
	for c := range ch {
		if c.Delta != "" {
			content = c.Delta
		}
		if c.Done && c.Usage.TotalTokens > 0 {
			gotUsage = true
		}
	}
	if content == "" {
		t.Fatalf("expected streamed content")
	}
	if !gotUsage {
		t.Fatalf("expected usage on terminal chunk")
	}
	hist, err := svc.GetChatHistory(context.Background(), sess.ID)
	if err != nil {
		t.Fatalf("history err: %v", err)
	}
	if len(hist) != 2 {
		t.Fatalf("want 2 messages, got %d", len(hist))
	}
	if _, ok := repo.Get(sess.ID); !ok {
		t.Fatalf("session not persisted in repo")
	}
}

func TestSendMessage_BlocksNonUser(t *testing.T) {
	svc, _, _ := newSessionSvc()
	sess, _ := svc.OpenSession(context.Background(), &domain.OpenSessionRequest{TenantID: "t1", AgentID: "a1"})
	_, err := svc.SendMessage(context.Background(), &session.SendMessageRequest{
		SessionID: sess.ID,
		Message:   domain.Message{Role: domain.RoleAssistant, Content: "hi"},
	})
	if err == nil {
		t.Fatalf("expected error for non-user message")
	}
}

func TestUploadFile_ReturnsRef(t *testing.T) {
	svc, _, _ := newSessionSvc()
	ref, err := svc.UploadFile(context.Background(), &domain.FileUploadRequest{
		TenantID: "t1", SessionID: "s1", FileName: "doc.txt", ContentType: "text/plain", Size: 3, Content: []byte("abc"),
	})
	if err != nil {
		t.Fatalf("unexpected err: %v", err)
	}
	if ref.ID == "" || ref.Size != 3 {
		t.Fatalf("bad ref: %+v", ref)
	}
}

func TestListAvailableAgents(t *testing.T) {
	svc, _, _ := newSessionSvc()
	agents, err := svc.ListAvailableAgents(context.Background(), "t1")
	if err != nil {
		t.Fatalf("unexpected err: %v", err)
	}
	if len(agents) != 1 || agents[0].ID != "a1" {
		t.Fatalf("want 1 published agent, got %+v", agents)
	}
}
