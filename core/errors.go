package core

import "fmt"

// BaseError is the base library error. Code is a stable machine-readable
// identifier such as NOT_SUPPORTED or VALIDATION_ERROR; Provider is set when known.
type BaseError struct {
	Message  string
	Code     string
	Provider string
}

func (e *BaseError) Error() string { return e.Message }

// NewError builds a base library error.
func NewError(message, code string) *BaseError {
	return &BaseError{Message: message, Code: code}
}

// ValidationIssue names one invalid field by its dotted path, e.g.
// "paymentCard.cardNumber" or "basketItems[0].price".
type ValidationIssue struct {
	Path    string `json:"path"`
	Message string `json:"message"`
}

// ValidationError reports input rejected before calling the provider.
type ValidationError struct {
	BaseError
	Issues []ValidationIssue
}

// NewValidationError builds a validation error naming the invalid fields.
func NewValidationError(message string, provider string, issues []ValidationIssue) *ValidationError {
	return &ValidationError{
		BaseError: BaseError{Message: message, Code: "VALIDATION_ERROR", Provider: provider},
		Issues:    issues,
	}
}

// Field returns the path of the first invalid field, if known.
func (e *ValidationError) Field() string {
	if len(e.Issues) == 0 {
		return ""
	}
	return e.Issues[0].Path
}

// Unwrap exposes the base error so errors.As can match *BaseError.
func (e *ValidationError) Unwrap() error { return &e.BaseError }

// ConfigurationError reports a provider or merchant configuration problem.
type ConfigurationError struct {
	BaseError
}

// NewConfigurationError builds a configuration error.
func NewConfigurationError(message, provider string) *ConfigurationError {
	return &ConfigurationError{BaseError{Message: message, Code: "CONFIGURATION_ERROR", Provider: provider}}
}

// Unwrap exposes the base error so errors.As can match *BaseError.
func (e *ConfigurationError) Unwrap() error { return &e.BaseError }

// ProviderNotEnabledError reports a call to a provider that is not configured.
type ProviderNotEnabledError struct {
	BaseError
}

// NewProviderNotEnabledError builds the error for an unconfigured provider.
func NewProviderNotEnabledError(provider string) *ProviderNotEnabledError {
	return &ProviderNotEnabledError{BaseError{
		Message:  fmt.Sprintf("Provider '%s' is not enabled or configured", provider),
		Code:     "PROVIDER_NOT_ENABLED",
		Provider: provider,
	}}
}

// Unwrap exposes the base error so errors.As can match *BaseError.
func (e *ProviderNotEnabledError) Unwrap() error { return &e.BaseError }

// PaymentFailedError reports a payment the provider declined.
type PaymentFailedError struct {
	BaseError
	ErrorCode   string
	RawResponse any
}

// NewPaymentFailedError builds a payment failure carrying the provider's raw code.
func NewPaymentFailedError(message, errorCode string, rawResponse any, provider string) *PaymentFailedError {
	return &PaymentFailedError{
		BaseError:   BaseError{Message: message, Code: "PAYMENT_FAILED", Provider: provider},
		ErrorCode:   errorCode,
		RawResponse: rawResponse,
	}
}

// Unwrap exposes the base error so errors.As can match *BaseError.
func (e *PaymentFailedError) Unwrap() error { return &e.BaseError }

// NotSupported builds the error for a feature the provider does not implement.
func NotSupported(provider, feature, detail string) *BaseError {
	msg := feature + " is not supported by this provider"
	if detail != "" {
		msg += ": " + detail
	}
	return &BaseError{Message: msg, Code: "NOT_SUPPORTED", Provider: provider}
}
