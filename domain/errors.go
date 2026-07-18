package domain

import "fmt"

// ErrorCode is a stable machine-readable gateway error code.
type ErrorCode string

const (
	ErrInvalidRequest   ErrorCode = "invalid_request"
	ErrUnauthorized     ErrorCode = "unauthorized"
	ErrModelNotFound    ErrorCode = "model_not_found"
	ErrRiskRejected     ErrorCode = "risk_rejected"
	ErrRateLimited      ErrorCode = "rate_limited"
	ErrAllProvidersDown ErrorCode = "all_providers_down"
	ErrUpstream         ErrorCode = "upstream_error"
)

// GatewayError is a domain error carrying a code and an HTTP-mappable status.
type GatewayError struct {
	Code    ErrorCode
	Status  int
	Message string
}

func (e *GatewayError) Error() string {
	return fmt.Sprintf("%s: %s", e.Code, e.Message)
}

// NewError builds a GatewayError.
func NewError(code ErrorCode, status int, msg string) *GatewayError {
	return &GatewayError{Code: code, Status: status, Message: msg}
}
