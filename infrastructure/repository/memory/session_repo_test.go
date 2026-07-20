package memory_test

import (
	"testing"

	"github.com/open-strata-ai/ai-gateway-core/domain"
	"github.com/open-strata-ai/ai-gateway-core/infrastructure/repository/memory"
)

func TestSessionRepo_SaveGetAppendList(t *testing.T) {
	r := memory.NewSessionRepository()
	s := domain.Session{ID: "s1", TenantID: "t1", AgentID: "a1"}
	if err := r.Save(s); err != nil {
		t.Fatalf("save err: %v", err)
	}
	got, ok := r.Get("s1")
	if !ok {
		t.Fatalf("session not found")
	}
	if got.ID != "s1" {
		t.Fatalf("bad session: %+v", got)
	}
	if err := r.AppendMessage("s1", domain.Message{Role: domain.RoleUser, Content: "hi"}); err != nil {
		t.Fatalf("append err: %v", err)
	}
	got, _ = r.Get("s1")
	if len(got.Messages) != 1 {
		t.Fatalf("want 1 message, got %d", len(got.Messages))
	}
	// a second tenant session must not appear in t1's list
	_ = r.Save(domain.Session{ID: "s2", TenantID: "t2"})
	if list := r.ListByTenant("t1"); len(list) != 1 {
		t.Fatalf("want 1 session for t1, got %d", len(list))
	}
}

func TestSessionRepo_AppendMissing(t *testing.T) {
	r := memory.NewSessionRepository()
	err := r.AppendMessage("nope", domain.Message{Role: domain.RoleUser, Content: "x"})
	if err == nil {
		t.Fatalf("expected error for missing session")
	}
}
