package provider

import (
	"context"
	"testing"

	"github.com/open-strata-ai/ai-gateway-core/domain"
)

func TestAdapter_ChatEcho(t *testing.T) {
	a := New(Config{Kind: KindOpenAI, ModelID: "cloud-gpt-4o"})
	resp, err := a.Chat(context.Background(), domain.ChatRequest{
		Messages: []domain.Message{{Role: domain.RoleUser, Content: "ping"}},
	})
	if err != nil {
		t.Fatalf("unexpected: %v", err)
	}
	if resp.Model != "cloud-gpt-4o" || resp.Content != "echo: ping" {
		t.Fatalf("unexpected response: %+v", resp)
	}
	if resp.Usage.TotalTokens == 0 {
		t.Fatalf("expected token usage accounting")
	}
}

func TestAdapter_ClaudeHoistsSystem(t *testing.T) {
	// Claude ACL: system message must be hoisted to the front (§6.3).
	captured := domain.ChatRequest{}
	a := New(Config{Kind: KindClaude, ModelID: "cloud-claude", Transport: func(_ context.Context, req domain.ChatRequest) (*domain.ChatResponse, error) {
		captured = req
		return &domain.ChatResponse{Model: req.Model, Content: "ok"}, nil
	}})
	_, err := a.Chat(context.Background(), domain.ChatRequest{Messages: []domain.Message{
		{Role: domain.RoleUser, Content: "hi"},
		{Role: domain.RoleSystem, Content: "be terse"},
	}})
	if err != nil {
		t.Fatalf("unexpected: %v", err)
	}
	if captured.Messages[0].Role != domain.RoleSystem {
		t.Fatalf("system message should be hoisted first, got %v", captured.Messages[0].Role)
	}
}

func TestAdapter_ChatStream(t *testing.T) {
	a := New(Config{Kind: KindOpenAI, ModelID: "m"})
	ch, err := a.ChatStream(context.Background(), domain.ChatRequest{Messages: []domain.Message{{Role: domain.RoleUser, Content: "x"}}})
	if err != nil {
		t.Fatalf("unexpected: %v", err)
	}
	var got int
	var sawDone bool
	for chunk := range ch {
		got++
		if chunk.Done {
			sawDone = true
		}
	}
	if got == 0 || !sawDone {
		t.Fatalf("expected chunks with a terminal done frame, got=%d done=%v", got, sawDone)
	}
}

func TestAdapter_FailingStub(t *testing.T) {
	a := New(Config{Kind: KindOpenAI, ModelID: "m", Transport: FailingStub()})
	if _, err := a.Chat(context.Background(), domain.ChatRequest{}); err == nil {
		t.Fatalf("expected failure")
	}
}

func TestAdapter_EmbedAndRerank(t *testing.T) {
	a := New(Config{Kind: KindOpenAI, ModelID: "e"})
	emb, err := a.Embed(context.Background(), domain.EmbedRequest{Input: []string{"a", "bb"}})
	if err != nil || len(emb.Embeddings) != 2 {
		t.Fatalf("embed failed: %v %+v", err, emb)
	}
	rr, err := a.Rerank(context.Background(), domain.RerankRequest{Query: "cat dog", Documents: []string{"cat", "fish"}})
	if err != nil || len(rr.Results) != 2 {
		t.Fatalf("rerank failed: %v %+v", err, rr)
	}
	// doc0 ("cat") overlaps the query, doc1 ("fish") does not → score0 > score1
	if rr.Results[0].Score <= rr.Results[1].Score {
		t.Fatalf("expected doc0 to score higher: %+v", rr.Results)
	}
}
