// Package httpapi exposes the OpenAI-compatible HTTP surface (DESIGN §7.1 / SPECS
// §7.1, R1). It uses the standard library net/http; production runs the hot path
// on Hertz/go-zero behind Higress.
package httpapi

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"

	"github.com/open-strata-ai/ai-gateway-core/application/chat"
	"github.com/open-strata-ai/ai-gateway-core/application/session"
	"github.com/open-strata-ai/ai-gateway-core/domain"
)

// UsageReporter fetches per-tenant usage metrics for the portal /usage page.
// Implemented by the billing ACL client; optional (nil → /usage returns 501 and
// the portal degrades to its static fallback).
type UsageReporter interface {
	Usage(ctx context.Context, tenantID string) (*UsageMetrics, error)
}

// UsageMetrics mirrors the portal UsageMetrics contract (Token/QPS/Vector/Cost).
type UsageMetrics struct {
	TokenBudget float64 `json:"tokenBudget"`
	TokenUsed   float64 `json:"tokenUsed"`
	QPSQuota    float64 `json:"qpsQuota"`
	QPSCurrent  float64 `json:"qpsCurrent"`
	VectorQuota float64 `json:"vectorQuota"`
	VectorUsed  float64 `json:"vectorUsed"`
	CostBudget  float64 `json:"costBudget"`
	CostActual  float64 `json:"costActual"`
	Source      string  `json:"source"`
}

// Handler wires the chat use case and catalog to HTTP endpoints.
type Handler struct {
	chat      *chat.Service
	catalog   domain.ModelCatalog
	auth      domain.AuthPort
	session   session.ChatSessionAppService
	agents    domain.AgentCatalog
	agentRepo domain.AgentRepository
	usage     UsageReporter
}

// New builds a Handler. sessionSvc and agents may be nil (endpoints then 501).
func New(chatSvc *chat.Service, catalog domain.ModelCatalog, auth domain.AuthPort, sessionSvc session.ChatSessionAppService, agents domain.AgentCatalog, agentRepo domain.AgentRepository) *Handler {
	return &Handler{chat: chatSvc, catalog: catalog, auth: auth, session: sessionSvc, agents: agents, agentRepo: agentRepo}
}

// SetUsageReporter attaches an optional usage/billing reporter for GET /usage.
func (h *Handler) SetUsageReporter(u UsageReporter) { h.usage = u }

// Routes returns the gateway HTTP handler with all endpoints registered.
// It is wrapped with CORS so the browser-based portal (served from a
// different origin, e.g. localhost:5174) can call the gateway directly.
// In production Higress terminates CORS at the edge.
func (h *Handler) Routes() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/v1/chat/completions", h.chatCompletions)
	mux.HandleFunc("/v1/embeddings", h.embeddings)
	mux.HandleFunc("/v1/rerank", h.rerank)
	mux.HandleFunc("/v1/models", h.models)
	mux.HandleFunc("/v1/healthz", h.healthz)
	mux.HandleFunc("/metrics", h.metrics)
	mux.HandleFunc("/v1/chat/sessions", h.openSession)
	mux.HandleFunc("/v1/chat/sessions/messages", h.sendMessage)
	mux.HandleFunc("/v1/chat/sessions/history", h.chatHistory)
	mux.HandleFunc("/v1/files/upload", h.fileUpload)
	mux.HandleFunc("/v1/agents", h.handleAgents)
	mux.HandleFunc("/v1/agents/", h.handleAgentByID)
	mux.HandleFunc("/usage", h.usageMetrics)
	return withCORS(mux)
}

// withCORS reflects the caller's Origin and answers preflight OPTIONS so the
// browser-based portal can call the gateway cross-origin during local dev. It is
// permissive by design: production terminates CORS at Higress, not here.
func withCORS(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if origin := r.Header.Get("Origin"); origin != "" {
			w.Header().Set("Access-Control-Allow-Origin", origin)
			w.Header().Set("Access-Control-Allow-Credentials", "true")
			w.Header().Set("Access-Control-Allow-Methods", "GET, POST, PATCH, DELETE, OPTIONS")
			w.Header().Set("Access-Control-Allow-Headers", "Content-Type, Authorization, X-Tenant-Id")
			w.Header().Set("Vary", "Origin")
		}
		if r.Method == http.MethodOptions {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		next.ServeHTTP(w, r)
	})
}

// usageMetrics handles GET /usage (portal usage dashboard, DESIGN §7.3). It is a
// BFF passthrough to the billing service via the optional UsageReporter.
func (h *Handler) usageMetrics(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeErr(w, domain.NewError(domain.ErrInvalidRequest, http.StatusMethodNotAllowed, "GET required"))
		return
	}
	if h.usage == nil {
		writeErr(w, domain.NewError(domain.ErrUpstream, http.StatusNotImplemented, "usage reporter not configured"))
		return
	}
	tenant, _, err := h.resolveTenant(r)
	if err != nil {
		writeErr(w, domain.NewError(domain.ErrUnauthorized, http.StatusUnauthorized, err.Error()))
		return
	}
	m, err := h.usage.Usage(r.Context(), tenant)
	if err != nil {
		writeErr(w, domain.NewError(domain.ErrUpstream, http.StatusBadGateway, err.Error()))
		return
	}
	writeJSON(w, http.StatusOK, m)
}

type chatReq struct {
	Model       string           `json:"model"`
	Messages    []domain.Message `json:"messages"`
	Temperature float32          `json:"temperature"`
	MaxTokens   int              `json:"max_tokens"`
	Stream      bool             `json:"stream"`
}

func (h *Handler) chatCompletions(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeErr(w, domain.NewError(domain.ErrInvalidRequest, http.StatusMethodNotAllowed, "POST required"))
		return
	}
	tenant, _, err := h.resolveTenant(r)
	if err != nil {
		writeErr(w, domain.NewError(domain.ErrUnauthorized, http.StatusUnauthorized, err.Error()))
		return
	}
	var body chatReq
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeErr(w, domain.NewError(domain.ErrInvalidRequest, http.StatusBadRequest, "invalid JSON body"))
		return
	}
	req := domain.ChatRequest{
		Model:       body.Model,
		Messages:    body.Messages,
		Temperature: body.Temperature,
		MaxTokens:   body.MaxTokens,
		Stream:      body.Stream,
		TenantID:    tenant,
		Capability:  domain.CapChat,
	}

	if body.Stream {
		h.streamChat(w, r, req)
		return
	}
	resp, err := h.chat.Complete(r.Context(), req)
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, resp)
}

func (h *Handler) streamChat(w http.ResponseWriter, r *http.Request, req domain.ChatRequest) {
	flusher, ok := w.(http.Flusher)
	if !ok {
		writeErr(w, domain.NewError(domain.ErrUpstream, http.StatusInternalServerError, "streaming unsupported"))
		return
	}
	// The offline pipeline resolves a full response then emits it as one SSE frame
	// plus a terminal frame (production streams provider SSE shards).
	resp, err := h.chat.Complete(r.Context(), req)
	if err != nil {
		writeErr(w, err)
		return
	}
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.WriteHeader(http.StatusOK)
	writeSSE(w, domain.ChatChunk{Delta: resp.Content, Done: false})
	flusher.Flush()
	writeSSE(w, domain.ChatChunk{Usage: resp.Usage, Done: true})
	flusher.Flush()
}

func (h *Handler) embeddings(w http.ResponseWriter, r *http.Request) {
	writeErr(w, domain.NewError(domain.ErrInvalidRequest, http.StatusNotImplemented,
		"embeddings handler is a Phase-2 stub; call the provider adapter directly"))
}

func (h *Handler) rerank(w http.ResponseWriter, r *http.Request) {
	writeErr(w, domain.NewError(domain.ErrInvalidRequest, http.StatusNotImplemented,
		"rerank handler is a Phase-2 stub; call the provider adapter directly"))
}

type modelsResp struct {
	Object string           `json:"object"`
	Data   []map[string]any `json:"data"`
}

func (h *Handler) models(w http.ResponseWriter, r *http.Request) {
	tenant, _, err := h.resolveTenant(r)
	if err != nil {
		writeErr(w, domain.NewError(domain.ErrUnauthorized, http.StatusUnauthorized, err.Error()))
		return
	}
	// Non-nil slice so the response is `[]` (not `null`) when the catalog is empty.
	data := make([]map[string]any, 0)
	for _, cap := range []string{domain.CapChat, domain.CapEmbedding, domain.CapRerank, domain.CapVision, domain.CapAudio} {
		for _, card := range h.catalog.ListByCapability(cap, tenant) {
			data = append(data, map[string]any{
				"id":         card.ModelID,
				"object":     "model",
				"name":       card.ModelID,
				"capability": card.Capability,
				"source":     card.Source,
				"provider":   card.Source,
				"health":     string(card.Health),
			})
		}
	}
	writeJSON(w, http.StatusOK, modelsResp{Object: "list", Data: data})
}

func (h *Handler) healthz(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

func (h *Handler) metrics(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/plain; version=0.0.4")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write([]byte("# gateway metrics exposed by production Prometheus registry\n"))
}

func (h *Handler) resolveTenant(r *http.Request) (string, string, error) {
	return h.auth.Resolve(r.Context(), r.Header.Get("Authorization"), r.Header.Get("X-Tenant-Id"))
}

// openSession handles POST /v1/chat/sessions (EU-01 session start).
func (h *Handler) openSession(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeErr(w, domain.NewError(domain.ErrInvalidRequest, http.StatusMethodNotAllowed, "POST required"))
		return
	}
	if h.session == nil {
		writeErr(w, domain.NewError(domain.ErrUpstream, http.StatusNotImplemented, "session service not configured"))
		return
	}
	tenant, _, err := h.resolveTenant(r)
	if err != nil {
		writeErr(w, domain.NewError(domain.ErrUnauthorized, http.StatusUnauthorized, err.Error()))
		return
	}
	var body domain.OpenSessionRequest
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeErr(w, domain.NewError(domain.ErrInvalidRequest, http.StatusBadRequest, "invalid JSON body"))
		return
	}
	body.TenantID = tenant
	sess, err := h.session.OpenSession(r.Context(), &body)
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, sess)
}

// chatHistory handles GET /v1/chat/sessions/history?session_id=... (EU-04).
func (h *Handler) chatHistory(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeErr(w, domain.NewError(domain.ErrInvalidRequest, http.StatusMethodNotAllowed, "GET required"))
		return
	}
	if h.session == nil {
		writeErr(w, domain.NewError(domain.ErrUpstream, http.StatusNotImplemented, "session service not configured"))
		return
	}
	id := r.URL.Query().Get("session_id")
	if id == "" {
		writeErr(w, domain.NewError(domain.ErrInvalidRequest, http.StatusBadRequest, "session_id required"))
		return
	}
	msgs, err := h.session.GetChatHistory(r.Context(), id)
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"session_id": id, "messages": msgs})
}

// sendMessage handles POST /v1/chat/sessions/messages: a single turn for an
// open session. It runs the full chat pipeline (real LLM) and persists both the
// user and assistant turns, then returns the assistant reply + token usage (EU-01
// session + EU-04 history + real completion).
func (h *Handler) sendMessage(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeErr(w, domain.NewError(domain.ErrInvalidRequest, http.StatusMethodNotAllowed, "POST required"))
		return
	}
	if h.session == nil {
		writeErr(w, domain.NewError(domain.ErrUpstream, http.StatusNotImplemented, "session service not configured"))
		return
	}
	tenant, _, err := h.resolveTenant(r)
	if err != nil {
		writeErr(w, domain.NewError(domain.ErrUnauthorized, http.StatusUnauthorized, err.Error()))
		return
	}
	var body struct {
		SessionID string         `json:"session_id"`
		Message   domain.Message `json:"message"`
		Model     string         `json:"model"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeErr(w, domain.NewError(domain.ErrInvalidRequest, http.StatusBadRequest, "invalid JSON body"))
		return
	}
	if body.SessionID == "" {
		writeErr(w, domain.NewError(domain.ErrInvalidRequest, http.StatusBadRequest, "session_id required"))
		return
	}
	if body.Message.Role == "" {
		body.Message.Role = domain.RoleUser
	}
	ch, err := h.session.SendMessage(r.Context(), &session.SendMessageRequest{
		SessionID: body.SessionID,
		Message:   body.Message,
		Model:     body.Model,
	})
	if err != nil {
		writeErr(w, err)
		return
	}
	var content strings.Builder
	var usage domain.TokenUsage
	got := false
	for chunk := range ch {
		if chunk.Delta != "" {
			content.WriteString(chunk.Delta)
		}
		if chunk.Done {
			usage = chunk.Usage
		}
		got = true
	}
	if !got {
		writeErr(w, domain.NewError(domain.ErrUpstream, http.StatusInternalServerError, "session produced no response"))
		return
	}
	_ = tenant
	writeJSON(w, http.StatusOK, map[string]any{
		"content": content.String(),
		"usage":   usage,
	})
}

// fileUpload handles POST /v1/files/upload (EU-03).
func (h *Handler) fileUpload(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeErr(w, domain.NewError(domain.ErrInvalidRequest, http.StatusMethodNotAllowed, "POST required"))
		return
	}
	if h.session == nil {
		writeErr(w, domain.NewError(domain.ErrUpstream, http.StatusNotImplemented, "session service not configured"))
		return
	}
	tenant, _, err := h.resolveTenant(r)
	if err != nil {
		writeErr(w, domain.NewError(domain.ErrUnauthorized, http.StatusUnauthorized, err.Error()))
		return
	}
	if err := r.ParseMultipartForm(10 << 20); err != nil {
		writeErr(w, domain.NewError(domain.ErrInvalidRequest, http.StatusBadRequest, "invalid multipart form"))
		return
	}
	file, header, err := r.FormFile("file")
	if err != nil {
		writeErr(w, domain.NewError(domain.ErrInvalidRequest, http.StatusBadRequest, "missing file field"))
		return
	}
	defer file.Close()
	content := make([]byte, header.Size)
	if _, err := file.Read(content); err != nil {
		writeErr(w, domain.NewError(domain.ErrUpstream, http.StatusInternalServerError, "read upload failed"))
		return
	}
	ref, err := h.session.UploadFile(r.Context(), &domain.FileUploadRequest{
		TenantID:    tenant,
		SessionID:   r.FormValue("session_id"),
		FileName:    header.Filename,
		ContentType: header.Header.Get("Content-Type"),
		Size:        header.Size,
		Content:     content,
	})
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, ref)
}

// handleAgents handles GET /v1/agents (list: user specs ∪ cataloged) and
// POST /v1/agents (create a user-authored spec, EU-05 authoring).
func (h *Handler) handleAgents(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		h.listAgents(w, r)
	case http.MethodPost:
		h.createAgent(w, r)
	default:
		writeErr(w, domain.NewError(domain.ErrInvalidRequest, http.StatusMethodNotAllowed, "GET/POST required"))
	}
}

// listAgents returns the tenant's persisted AgentSpecs plus the published
// catalog entries, de-duplicated by id (user specs win).
func (h *Handler) listAgents(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeErr(w, domain.NewError(domain.ErrInvalidRequest, http.StatusMethodNotAllowed, "GET required"))
		return
	}
	if h.session == nil {
		writeErr(w, domain.NewError(domain.ErrUpstream, http.StatusNotImplemented, "session service not configured"))
		return
	}
	tenant, _, err := h.resolveTenant(r)
	if err != nil {
		writeErr(w, domain.NewError(domain.ErrUnauthorized, http.StatusUnauthorized, err.Error()))
		return
	}
	seen := map[string]bool{}
	var agents []domain.AgentSpec
	if userAgents, uerr := h.session.ListAgents(r.Context(), tenant); uerr == nil {
		for _, a := range userAgents {
			seen[a.ID] = true
			agents = append(agents, a)
		}
	}
	if h.agents != nil {
		for _, sum := range h.agents.ListAvailable(tenant) {
			if seen[sum.ID] {
				continue
			}
			agents = append(agents, domain.AgentSpec{
				ID:          sum.ID,
				Name:        sum.Name,
				Description: sum.Description,
				Status:      sum.Status,
			})
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{"agents": agents})
}

// createAgent handles POST /v1/agents (EU-05 authoring).
func (h *Handler) createAgent(w http.ResponseWriter, r *http.Request) {
	if h.session == nil || h.agentRepo == nil {
		writeErr(w, domain.NewError(domain.ErrUpstream, http.StatusNotImplemented, "agent repository not configured"))
		return
	}
	tenant, _, err := h.resolveTenant(r)
	if err != nil {
		writeErr(w, domain.NewError(domain.ErrUnauthorized, http.StatusUnauthorized, err.Error()))
		return
	}
	var body domain.CreateAgentRequest
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeErr(w, domain.NewError(domain.ErrInvalidRequest, http.StatusBadRequest, "invalid JSON body"))
		return
	}
	body.TenantID = tenant
	spec, err := h.session.CreateAgent(r.Context(), &body)
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, spec)
}

// handleAgentByID handles GET/PATCH/DELETE /v1/agents/{id}.
func (h *Handler) handleAgentByID(w http.ResponseWriter, r *http.Request) {
	if h.session == nil || h.agentRepo == nil {
		writeErr(w, domain.NewError(domain.ErrUpstream, http.StatusNotImplemented, "agent repository not configured"))
		return
	}
	id := strings.TrimPrefix(r.URL.Path, "/v1/agents/")
	if id == "" {
		writeErr(w, domain.NewError(domain.ErrInvalidRequest, http.StatusBadRequest, "agent id required"))
		return
	}
	switch r.Method {
	case http.MethodGet:
		spec, err := h.session.GetAgent(r.Context(), id)
		if err != nil {
			writeErr(w, err)
			return
		}
		writeJSON(w, http.StatusOK, spec)
	case http.MethodPatch:
		var body domain.UpdateAgentRequest
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			writeErr(w, domain.NewError(domain.ErrInvalidRequest, http.StatusBadRequest, "invalid JSON body"))
			return
		}
		spec, err := h.session.UpdateAgent(r.Context(), id, &body)
		if err != nil {
			writeErr(w, err)
			return
		}
		writeJSON(w, http.StatusOK, spec)
	case http.MethodDelete:
		if err := h.session.DeleteAgent(r.Context(), id); err != nil {
			writeErr(w, err)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	default:
		writeErr(w, domain.NewError(domain.ErrInvalidRequest, http.StatusMethodNotAllowed, "GET/PATCH/DELETE required"))
	}
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func writeSSE(w http.ResponseWriter, chunk domain.ChatChunk) {
	b, _ := json.Marshal(chunk)
	_, _ = w.Write([]byte("data: "))
	_, _ = w.Write(b)
	_, _ = w.Write([]byte("\n\n"))
}

type errBody struct {
	Error struct {
		Code    string `json:"code"`
		Message string `json:"message"`
	} `json:"error"`
}

func writeErr(w http.ResponseWriter, err error) {
	status := http.StatusInternalServerError
	code := string(domain.ErrUpstream)
	msg := err.Error()
	if ge, ok := err.(*domain.GatewayError); ok {
		status = ge.Status
		code = string(ge.Code)
		msg = ge.Message
	}
	var body errBody
	body.Error.Code = code
	body.Error.Message = msg
	writeJSON(w, status, body)
}
