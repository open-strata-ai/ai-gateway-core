// Package httpapi exposes the OpenAI-compatible HTTP surface (DESIGN §7.1 / SPECS
// §7.1, R1). It uses the standard library net/http; production runs the hot path
// on Hertz/go-zero behind Higress.
package httpapi

import (
	"encoding/json"
	"net/http"

	"github.com/open-strata-ai/ai-gateway-core/application/chat"
	"github.com/open-strata-ai/ai-gateway-core/application/session"
	"github.com/open-strata-ai/ai-gateway-core/domain"
)

// Handler wires the chat use case and catalog to HTTP endpoints.
type Handler struct {
	chat      *chat.Service
	catalog   domain.ModelCatalog
	auth      domain.AuthPort
	session   session.ChatSessionAppService
	agents    domain.AgentCatalog
}

// New builds a Handler. sessionSvc and agents may be nil (endpoints then 501).
func New(chatSvc *chat.Service, catalog domain.ModelCatalog, auth domain.AuthPort, sessionSvc session.ChatSessionAppService, agents domain.AgentCatalog) *Handler {
	return &Handler{chat: chatSvc, catalog: catalog, auth: auth, session: sessionSvc, agents: agents}
}

// Routes returns a ServeMux with all endpoints registered.
func (h *Handler) Routes() *http.ServeMux {
	mux := http.NewServeMux()
	mux.HandleFunc("/v1/chat/completions", h.chatCompletions)
	mux.HandleFunc("/v1/embeddings", h.embeddings)
	mux.HandleFunc("/v1/rerank", h.rerank)
	mux.HandleFunc("/v1/models", h.models)
	mux.HandleFunc("/v1/healthz", h.healthz)
	mux.HandleFunc("/metrics", h.metrics)
	mux.HandleFunc("/v1/chat/sessions", h.openSession)
	mux.HandleFunc("/v1/chat/sessions/history", h.chatHistory)
	mux.HandleFunc("/v1/files/upload", h.fileUpload)
	mux.HandleFunc("/v1/agents", h.listAgents)
	return mux
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
	Object string             `json:"object"`
	Data   []map[string]any   `json:"data"`
}

func (h *Handler) models(w http.ResponseWriter, r *http.Request) {
	tenant, _, err := h.resolveTenant(r)
	if err != nil {
		writeErr(w, domain.NewError(domain.ErrUnauthorized, http.StatusUnauthorized, err.Error()))
		return
	}
	var data []map[string]any
	for _, cap := range []string{domain.CapChat, domain.CapEmbedding, domain.CapRerank, domain.CapVision, domain.CapAudio} {
		for _, card := range h.catalog.ListByCapability(cap, tenant) {
			data = append(data, map[string]any{
				"id":         card.ModelID,
				"object":     "model",
				"capability": card.Capability,
				"source":     card.Source,
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

// listAgents handles GET /v1/agents (EU-05).
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
	agents, err := h.session.ListAvailableAgents(r.Context(), tenant)
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"agents": agents})
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
