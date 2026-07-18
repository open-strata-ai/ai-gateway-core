// Package riskcontrol implements domain.RiskController (DESIGN §5.5 / §4.7.4):
// prompt-injection detection, PII detection + masking, and egress control.
package riskcontrol

import (
	"regexp"
	"strings"

	"github.com/open-strata-ai/ai-gateway-core/domain"
)

// Scanner performs basic risk control. It is enabled by default (R9).
type Scanner struct {
	piiScan       bool
	injectionRes  []*regexp.Regexp
	piiRes        []*regexp.Regexp
}

// Config configures a Scanner.
type Config struct {
	PIIScan bool
}

var defaultInjectionPatterns = []string{
	`(?i)ignore\b.{0,30}\binstructions\b`,
	`(?i)disregard\b.{0,30}\b(prompt|instructions)\b`,
	`(?i)you are now (a|an) `,
	`(?i)system prompt\s*[:=]`,
	`(?i)reveal\b.{0,20}\bprompt\b`,
}

var defaultPIIPatterns = []string{
	`\b[0-9]{3}-[0-9]{2}-[0-9]{4}\b`,                    // US SSN
	`\b[0-9]{13,19}\b`,                                  // long card-like number
	`\b[A-Za-z0-9._%+\-]+@[A-Za-z0-9.\-]+\.[A-Za-z]{2,}\b`, // email
}

// New builds a Scanner with the default rule sets.
func New(cfg Config) *Scanner {
	s := &Scanner{piiScan: cfg.PIIScan}
	for _, p := range defaultInjectionPatterns {
		s.injectionRes = append(s.injectionRes, regexp.MustCompile(p))
	}
	for _, p := range defaultPIIPatterns {
		s.piiRes = append(s.piiRes, regexp.MustCompile(p))
	}
	return s
}

// Inspect scans the request. ok=false rejects (injection). PII is masked in the
// returned request when piiScan is on. denyEgress is enforced by the router, but
// is echoed here for auditing symmetry.
func (s *Scanner) Inspect(req domain.ChatRequest, denyEgress bool) (domain.ChatRequest, bool, string) {
	for _, m := range req.Messages {
		if m.Role != domain.RoleUser {
			continue
		}
		for _, re := range s.injectionRes {
			if re.MatchString(m.Content) {
				return req, false, "prompt_injection_detected"
			}
		}
	}

	if s.piiScan {
		masked := make([]domain.Message, len(req.Messages))
		for i, m := range req.Messages {
			masked[i] = domain.Message{Role: m.Role, Content: s.maskPII(m.Content)}
		}
		req.Messages = masked
	}
	return req, true, ""
}

func (s *Scanner) maskPII(content string) string {
	out := content
	for _, re := range s.piiRes {
		out = re.ReplaceAllStringFunc(out, func(match string) string {
			return "[REDACTED:" + strings.Repeat("*", 4) + "]"
		})
	}
	return out
}

var _ domain.RiskController = (*Scanner)(nil)
