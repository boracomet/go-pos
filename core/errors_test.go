package core

import (
	"errors"
	"testing"
)

func TestBaseError(t *testing.T) {
	t.Run("sets message and code", func(t *testing.T) {
		err := NewError("test message", "TEST_CODE")
		if err.Message != "test message" {
			t.Errorf("Message = %q, want %q", err.Message, "test message")
		}
		if err.Code != "TEST_CODE" {
			t.Errorf("Code = %q, want %q", err.Code, "TEST_CODE")
		}
		if err.Error() != "test message" {
			t.Errorf("Error() = %q, want %q", err.Error(), "test message")
		}
	})

	t.Run("sets the optional provider field", func(t *testing.T) {
		err := &BaseError{Message: "msg", Code: "CODE", Provider: "iyzico"}
		if err.Provider != "iyzico" {
			t.Errorf("Provider = %q, want %q", err.Provider, "iyzico")
		}
	})

	t.Run("satisfies the error contract", func(t *testing.T) {
		var err error = NewError("msg", "CODE")
		var base *BaseError
		if !errors.As(err, &base) {
			t.Fatalf("errors.As(*BaseError) = false, want true")
		}
		if base.Code != "CODE" {
			t.Errorf("Code = %q, want %q", base.Code, "CODE")
		}
	})
}

func TestProviderNotEnabledError(t *testing.T) {
	t.Run("formats the message with the provider name", func(t *testing.T) {
		err := NewProviderNotEnabledError("paytr")
		if err.Message != "Provider 'paytr' is not enabled or configured" {
			t.Errorf("Message = %q", err.Message)
		}
		if err.Code != "PROVIDER_NOT_ENABLED" {
			t.Errorf("Code = %q, want PROVIDER_NOT_ENABLED", err.Code)
		}
		if err.Provider != "paytr" {
			t.Errorf("Provider = %q, want paytr", err.Provider)
		}
	})

	t.Run("unwraps to the base error", func(t *testing.T) {
		var err error = NewProviderNotEnabledError("iyzico")
		var notEnabled *ProviderNotEnabledError
		if !errors.As(err, &notEnabled) {
			t.Fatalf("errors.As(*ProviderNotEnabledError) = false, want true")
		}
		var base *BaseError
		if !errors.As(err, &base) {
			t.Fatalf("errors.As(*BaseError) = false, want true")
		}
		if base.Provider != "iyzico" {
			t.Errorf("Provider = %q, want iyzico", base.Provider)
		}
	})
}

func TestPaymentFailedError(t *testing.T) {
	t.Run("sets code to PAYMENT_FAILED", func(t *testing.T) {
		err := NewPaymentFailedError("Payment declined", "", nil, "")
		if err.Code != "PAYMENT_FAILED" {
			t.Errorf("Code = %q, want PAYMENT_FAILED", err.Code)
		}
		if err.Message != "Payment declined" {
			t.Errorf("Message = %q, want %q", err.Message, "Payment declined")
		}
	})

	t.Run("sets the optional errorCode, rawResponse and provider", func(t *testing.T) {
		raw := map[string]any{"status": "failure"}
		err := NewPaymentFailedError("declined", "ERR_001", raw, "akbank")
		if err.ErrorCode != "ERR_001" {
			t.Errorf("ErrorCode = %q, want ERR_001", err.ErrorCode)
		}
		record, ok := err.RawResponse.(map[string]any)
		if !ok || record["status"] != "failure" {
			t.Errorf("RawResponse = %#v, want the raw body", err.RawResponse)
		}
		if err.Provider != "akbank" {
			t.Errorf("Provider = %q, want akbank", err.Provider)
		}
	})

	t.Run("unwraps to the base error", func(t *testing.T) {
		var err error = NewPaymentFailedError("failed", "", nil, "")
		var failed *PaymentFailedError
		if !errors.As(err, &failed) {
			t.Fatalf("errors.As(*PaymentFailedError) = false, want true")
		}
		var base *BaseError
		if !errors.As(err, &base) {
			t.Fatalf("errors.As(*BaseError) = false, want true")
		}
	})
}

func TestValidationError(t *testing.T) {
	t.Run("sets code to VALIDATION_ERROR", func(t *testing.T) {
		err := NewValidationError("invalid price", "", nil)
		if err.Code != "VALIDATION_ERROR" {
			t.Errorf("Code = %q, want VALIDATION_ERROR", err.Code)
		}
	})

	t.Run("unwraps to the base error", func(t *testing.T) {
		var err error = NewValidationError("bad input", "", nil)
		var validation *ValidationError
		if !errors.As(err, &validation) {
			t.Fatalf("errors.As(*ValidationError) = false, want true")
		}
		var base *BaseError
		if !errors.As(err, &base) {
			t.Fatalf("errors.As(*BaseError) = false, want true")
		}
	})

	t.Run("Field names the first invalid path", func(t *testing.T) {
		err := NewValidationError("Invalid request", "iyzico", []ValidationIssue{
			{Path: "price", Message: "must be greater than 0"},
			{Path: "buyer.foo", Message: "is required"},
		})
		if err.Field() != "price" {
			t.Errorf("Field() = %q, want price", err.Field())
		}
		if empty := NewValidationError("no issues", "", nil); empty.Field() != "" {
			t.Errorf("Field() = %q, want an empty string", empty.Field())
		}
	})
}

func TestConfigurationError(t *testing.T) {
	t.Run("sets code to CONFIGURATION_ERROR", func(t *testing.T) {
		err := NewConfigurationError("missing apiKey", "")
		if err.Code != "CONFIGURATION_ERROR" {
			t.Errorf("Code = %q, want CONFIGURATION_ERROR", err.Code)
		}
	})

	t.Run("unwraps to the base error and keeps the provider", func(t *testing.T) {
		var err error = NewConfigurationError("bad config", "iyzico")
		var configuration *ConfigurationError
		if !errors.As(err, &configuration) {
			t.Fatalf("errors.As(*ConfigurationError) = false, want true")
		}
		var base *BaseError
		if !errors.As(err, &base) {
			t.Fatalf("errors.As(*BaseError) = false, want true")
		}
		if configuration.Provider != "iyzico" {
			t.Errorf("Provider = %q, want iyzico", configuration.Provider)
		}
	})
}

func TestNotSupported(t *testing.T) {
	err := NotSupported("paytr", "Card storage", "")
	if err.Code != "NOT_SUPPORTED" {
		t.Errorf("Code = %q, want NOT_SUPPORTED", err.Code)
	}
	if err.Message != "Card storage is not supported by this provider" {
		t.Errorf("Message = %q", err.Message)
	}
	if err.Provider != "paytr" {
		t.Errorf("Provider = %q, want paytr", err.Provider)
	}
	detailed := NotSupported("paytr", "Capture", "only full captures")
	if detailed.Message != "Capture is not supported by this provider: only full captures" {
		t.Errorf("Message = %q", detailed.Message)
	}
}
