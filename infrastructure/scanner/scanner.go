// Package scanner implements content scanning for the gateway file-upload and
// chat pipelines (Batch B1, DESIGN §1.2 / §7.2). It detects PII and prompt
// injection with regex rule sets and is dependency-free so it runs offline.
package scanner

import "regexp"

// Scanner runs PII + injection rule sets over arbitrary text.
type Scanner struct {
	piiRes       []*regexp.Regexp
	injectionRes []*regexp.Regexp
	maskPII      bool
}

// Config tunes the Scanner.
type Config struct {
	PIIScan bool
}

var defaultInjectionPatterns = []string{
	`(?i)ignore\b.{0,30}\binstructions\b`,
	`(?i)disregard\b.{0,30}\b(prompt|instructions)\b`,
	`(?i)you are now (a|an) `,
	`(?i)system prompt\s*[:=]`,
	`(?i)reveal\b.{0,20}\b(secret|password|prompt)\b`,
}

var defaultPIIPatterns = []string{
	`\b[0-9]{3}-[0-9]{2}-[0-9]{4}\b`,                        // US SSN
	`\b[0-9]{13,19}\b`,                                      // long card-like number
	`\b[A-Za-z0-9._%+\-]+@[A-Za-z0-9.\-]+\.[A-Za-z]{2,}\b`,  // email
	`(?i)password\s*[:=]\s*\S+`,                             // password assignments
}

// New builds a Scanner with the default rule sets.
func New(cfg Config) *Scanner {
	s := &Scanner{maskPII: cfg.PIIScan}
	for _, p := range defaultInjectionPatterns {
		s.injectionRes = append(s.injectionRes, regexp.MustCompile(p))
	}
	for _, p := range defaultPIIPatterns {
		s.piiRes = append(s.piiRes, regexp.MustCompile(p))
	}
	return s
}

// ScanResult is the outcome of a scan.
type ScanResult struct {
	Blocked  bool
	Reason   string
	Findings []string
}

// ScanPII returns matched PII spans (empty when none). When piiScan is off the
// result is always empty.
func (s *Scanner) ScanPII(text string) []string {
	if !s.maskPII {
		return nil
	}
	var found []string
	for _, re := range s.piiRes {
		for _, m := range re.FindAllString(text, -1) {
			found = append(found, m)
		}
	}
	return found
}

// ScanInjection returns matched injection spans (empty when none).
func (s *Scanner) ScanInjection(text string) []string {
	var found []string
	for _, re := range s.injectionRes {
		for _, m := range re.FindAllString(text, -1) {
			found = append(found, m)
		}
	}
	return found
}

// MaskPII replaces matched PII spans with a fixed redaction token.
func (s *Scanner) MaskPII(text string) string {
	if !s.maskPII {
		return text
	}
	out := text
	for _, re := range s.piiRes {
		out = re.ReplaceAllString(out, "[REDACTED]")
	}
	return out
}
