// Package provider — real upstream HTTP transports (ACL, DESIGN §6.3).
//
// This file plugs genuine LLM providers into the Adapter.Transport SPI. It keeps
// to the standard library only. Two protocol families are implemented:
//
//   - OpenAI-compatible chat/completions — used by both OpenAI (api.openai.com)
//     and Alibaba DashScope/Qwen (dashscope.aliyuncs.com/compatible-mode). The
//     wire schema is identical, so one client serves both; only base URL +
//     API-key env differ.
//   - Anthropic Claude messages API — different request/response shape.
//
// Selection + credentials come from the environment so no secrets live in code:
//
//	OPENAI_API_KEY      (+ optional OPENAI_BASE_URL,    default https://api.openai.com/v1)
//	DASHSCOPE_API_KEY   (+ optional DASHSCOPE_BASE_URL, default https://dashscope.aliyuncs.com/compatible-mode/v1)
//	ANTHROPIC_API_KEY   (+ optional ANTHROPIC_BASE_URL, default https://api.anthropic.com/v1)
//
// If the relevant key is unset, TransportFromEnv returns nil and the Adapter
// falls back to the deterministic echo stub (offline-friendly).
package provider

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/open-strata-ai/ai-gateway-core/domain"
)

// defaultHTTPTimeout bounds a single upstream call.
const defaultHTTPTimeout = 60 * time.Second

// sharedHTTPClient is reused across adapters (connection pooling).
var sharedHTTPClient = &http.Client{Timeout: defaultHTTPTimeout}

// TransportFromEnv returns a real upstream Transport for the given protocol
// family + internal model ID, reading credentials from the environment. It
// returns nil when no credentials are configured, so the caller can fall back
// to the echo stub.
//
// The internal model ID (e.g. "cloud-qwen-max", "cloud-gpt-4o") is mapped to the
// upstream model name by stripping a leading "cloud-" / "local-" prefix
// ("cloud-qwen-max" -> "qwen-max", "cloud-gpt-4o" -> "gpt-4o"). An explicit
// override may be supplied via UPSTREAM_MODEL_<UPPER_SNAKE> env if needed.
func TransportFromEnv(kind Kind, modelID string) Transport {
	upstream := upstreamModelName(modelID)
	switch kind {
	case KindOpenAI:
		if key := os.Getenv("OPENAI_API_KEY"); key != "" {
			base := envOr("OPENAI_BASE_URL", "https://api.openai.com/v1")
			return openAICompatibleTransport(modelID, upstream, base, key)
		}
	case KindQwen:
		if key := os.Getenv("DASHSCOPE_API_KEY"); key != "" {
			base := envOr("DASHSCOPE_BASE_URL", "https://dashscope.aliyuncs.com/compatible-mode/v1")
			return openAICompatibleTransport(modelID, upstream, base, key)
		}
		// DashScope also accepts an OpenAI-compatible key via OPENAI_* if the
		// operator points OPENAI_BASE_URL at the compatible endpoint.
	case KindClaude:
		if key := os.Getenv("ANTHROPIC_API_KEY"); key != "" {
			base := envOr("ANTHROPIC_BASE_URL", "https://api.anthropic.com/v1")
			return claudeTransport(modelID, upstream, base, key)
		}
	case KindSelfHosted:
		// vLLM/TGI usually expose an OpenAI-compatible server; wire it if a base
		// URL is provided (no key required for most local deployments).
		if base := os.Getenv("SELF_HOSTED_BASE_URL"); base != "" {
			return openAICompatibleTransport(modelID, upstream, base, os.Getenv("SELF_HOSTED_API_KEY"))
		}
	}
	return nil
}

// upstreamModelName maps an internal model ID to the provider's model name.
func upstreamModelName(modelID string) string {
	// Allow an explicit override: UPSTREAM_MODEL_CLOUD_QWEN_MAX=qwen-max
	envKey := "UPSTREAM_MODEL_" + strings.ToUpper(strings.NewReplacer("-", "_", ".", "_").Replace(modelID))
	if v := os.Getenv(envKey); v != "" {
		return v
	}
	m := modelID
	for _, p := range []string{"cloud-", "local-", "self-hosted-"} {
		if strings.HasPrefix(m, p) {
			m = strings.TrimPrefix(m, p)
			break
		}
	}
	return m
}

func envOr(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}

// ── OpenAI-compatible (OpenAI + DashScope/Qwen) ──────────────────────────────

type openAIMessage struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

type openAIChatRequest struct {
	Model       string          `json:"model"`
	Messages    []openAIMessage `json:"messages"`
	Temperature float32         `json:"temperature,omitempty"`
	MaxTokens   int             `json:"max_tokens,omitempty"`
	Stream      bool            `json:"stream"`
}

type openAIChatResponse struct {
	Choices []struct {
		Message      openAIMessage `json:"message"`
		FinishReason string        `json:"finish_reason"`
	} `json:"choices"`
	Usage struct {
		PromptTokens     int `json:"prompt_tokens"`
		CompletionTokens int `json:"completion_tokens"`
		TotalTokens      int `json:"total_tokens"`
	} `json:"usage"`
	Error *struct {
		Message string `json:"message"`
		Type    string `json:"type"`
	} `json:"error"`
}

func openAICompatibleTransport(internalModel, upstreamModel, baseURL, apiKey string) Transport {
	endpoint := strings.TrimRight(baseURL, "/") + "/chat/completions"
	return func(ctx context.Context, req domain.ChatRequest) (*domain.ChatResponse, error) {
		payload := openAIChatRequest{
			Model:       upstreamModel,
			Messages:    toOpenAIMessages(req.Messages),
			Temperature: req.Temperature,
			MaxTokens:   req.MaxTokens,
			Stream:      false,
		}
		body, err := json.Marshal(payload)
		if err != nil {
			return nil, fmt.Errorf("marshal request: %w", err)
		}
		httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(body))
		if err != nil {
			return nil, fmt.Errorf("build request: %w", err)
		}
		httpReq.Header.Set("Content-Type", "application/json")
		if apiKey != "" {
			httpReq.Header.Set("Authorization", "Bearer "+apiKey)
		}

		resp, err := sharedHTTPClient.Do(httpReq)
		if err != nil {
			return nil, fmt.Errorf("upstream call: %w", err)
		}
		defer resp.Body.Close()
		raw, _ := io.ReadAll(io.LimitReader(resp.Body, 4<<20))

		var parsed openAIChatResponse
		if err := json.Unmarshal(raw, &parsed); err != nil {
			return nil, fmt.Errorf("decode response (status %d): %w", resp.StatusCode, err)
		}
		if resp.StatusCode < 200 || resp.StatusCode >= 300 {
			msg := http.StatusText(resp.StatusCode)
			if parsed.Error != nil && parsed.Error.Message != "" {
				msg = parsed.Error.Message
			}
			return nil, fmt.Errorf("upstream %d: %s", resp.StatusCode, msg)
		}
		if len(parsed.Choices) == 0 {
			return nil, fmt.Errorf("upstream returned no choices")
		}
		return &domain.ChatResponse{
			Model:        internalModel,
			Content:      parsed.Choices[0].Message.Content,
			FinishReason: orString(parsed.Choices[0].FinishReason, "stop"),
			Usage: domain.TokenUsage{
				PromptTokens:     parsed.Usage.PromptTokens,
				CompletionTokens: parsed.Usage.CompletionTokens,
				TotalTokens:      parsed.Usage.TotalTokens,
			},
		}, nil
	}
}

func toOpenAIMessages(msgs []domain.Message) []openAIMessage {
	out := make([]openAIMessage, 0, len(msgs))
	for _, m := range msgs {
		out = append(out, openAIMessage{Role: string(m.Role), Content: m.Content})
	}
	return out
}

// ── Anthropic Claude messages API ────────────────────────────────────────────

type claudeContentBlock struct {
	Type string `json:"type"`
	Text string `json:"text"`
}

type claudeRequest struct {
	Model     string          `json:"model"`
	System    string          `json:"system,omitempty"`
	Messages  []openAIMessage `json:"messages"`
	MaxTokens int             `json:"max_tokens"`
}

type claudeResponse struct {
	Content    []claudeContentBlock `json:"content"`
	StopReason string               `json:"stop_reason"`
	Usage      struct {
		InputTokens  int `json:"input_tokens"`
		OutputTokens int `json:"output_tokens"`
	} `json:"usage"`
	Error *struct {
		Message string `json:"message"`
	} `json:"error"`
}

func claudeTransport(internalModel, upstreamModel, baseURL, apiKey string) Transport {
	endpoint := strings.TrimRight(baseURL, "/") + "/messages"
	return func(ctx context.Context, req domain.ChatRequest) (*domain.ChatResponse, error) {
		system, turns := splitSystem(req.Messages)
		maxTok := req.MaxTokens
		if maxTok == 0 {
			maxTok = 1024
		}
		payload := claudeRequest{
			Model:     upstreamModel,
			System:    system,
			Messages:  turns,
			MaxTokens: maxTok,
		}
		body, err := json.Marshal(payload)
		if err != nil {
			return nil, fmt.Errorf("marshal request: %w", err)
		}
		httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(body))
		if err != nil {
			return nil, fmt.Errorf("build request: %w", err)
		}
		httpReq.Header.Set("Content-Type", "application/json")
		httpReq.Header.Set("x-api-key", apiKey)
		httpReq.Header.Set("anthropic-version", envOr("ANTHROPIC_VERSION", "2023-06-01"))

		resp, err := sharedHTTPClient.Do(httpReq)
		if err != nil {
			return nil, fmt.Errorf("upstream call: %w", err)
		}
		defer resp.Body.Close()
		raw, _ := io.ReadAll(io.LimitReader(resp.Body, 4<<20))

		var parsed claudeResponse
		if err := json.Unmarshal(raw, &parsed); err != nil {
			return nil, fmt.Errorf("decode response (status %d): %w", resp.StatusCode, err)
		}
		if resp.StatusCode < 200 || resp.StatusCode >= 300 {
			msg := http.StatusText(resp.StatusCode)
			if parsed.Error != nil && parsed.Error.Message != "" {
				msg = parsed.Error.Message
			}
			return nil, fmt.Errorf("upstream %d: %s", resp.StatusCode, msg)
		}
		var sb strings.Builder
		for _, b := range parsed.Content {
			if b.Type == "text" {
				sb.WriteString(b.Text)
			}
		}
		return &domain.ChatResponse{
			Model:        internalModel,
			Content:      sb.String(),
			FinishReason: orString(parsed.StopReason, "stop"),
			Usage: domain.TokenUsage{
				PromptTokens:     parsed.Usage.InputTokens,
				CompletionTokens: parsed.Usage.OutputTokens,
				TotalTokens:      parsed.Usage.InputTokens + parsed.Usage.OutputTokens,
			},
		}, nil
	}
}

// splitSystem pulls system messages into Claude's dedicated top-level field.
func splitSystem(msgs []domain.Message) (string, []openAIMessage) {
	var sys []string
	var turns []openAIMessage
	for _, m := range msgs {
		if m.Role == domain.RoleSystem {
			sys = append(sys, m.Content)
			continue
		}
		turns = append(turns, openAIMessage{Role: string(m.Role), Content: m.Content})
	}
	return strings.Join(sys, "\n\n"), turns
}

func orString(v, def string) string {
	if v == "" {
		return def
	}
	return v
}
