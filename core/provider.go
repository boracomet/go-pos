package core

import (
	"context"
	"time"
)

// ProviderConfig holds the fields shared by all providers. Credentials are
// defined in each provider's own configuration type.
type ProviderConfig struct {
	BaseURL string
	Locale  string
	Logger  Logger
	Retry   *RetryConfig
	// Doer overrides the transport used for API calls. Defaults to http.DefaultClient.
	Doer Doer
	// Validate requests before calling the provider (card, amounts, basket,
	// required fields). Defaults to true.
	Validate *bool
}

// Provider is the contract every payment gateway implements. The six payment
// operations are mandatory; BaseProvider supplies NOT_SUPPORTED defaults for
// the optional ones, so a provider only overrides what it actually supports.
type Provider interface {
	Name() string

	CreatePayment(ctx context.Context, request *PaymentRequest) (*PaymentResponse, error)
	InitThreeDSPayment(ctx context.Context, request *ThreeDSPaymentRequest) (*ThreeDSInitResponse, error)
	CompleteThreeDSPayment(ctx context.Context, callbackData any) (*PaymentResponse, error)
	Refund(ctx context.Context, request *RefundRequest) (*RefundResponse, error)
	Cancel(ctx context.Context, request *CancelRequest) (*CancelResponse, error)
	GetPayment(ctx context.Context, paymentID string) (*PaymentResponse, error)

	Authorize(ctx context.Context, request *PaymentRequest) (*PaymentResponse, error)
	InitThreeDSAuthorize(ctx context.Context, request *ThreeDSPaymentRequest) (*ThreeDSInitResponse, error)
	Capture(ctx context.Context, request *CaptureRequest) (*PaymentResponse, error)
	VoidAuthorization(ctx context.Context, request *VoidAuthorizationRequest) (*CancelResponse, error)

	SaveCard(ctx context.Context, request *SaveCardRequest) (*SaveCardResponse, error)
	ListCards(ctx context.Context, request *ListCardsRequest) (*ListCardsResponse, error)
	DeleteCard(ctx context.Context, request *DeleteCardRequest) (*DeleteCardResponse, error)

	BinCheck(ctx context.Context, binNumber string) (*BinCheckResponse, error)
	InstallmentInfo(ctx context.Context, request *InstallmentInfoRequest) (*InstallmentInfoResponse, error)
}

// BaseProvider carries the shared configuration and helpers. Embed it by value
// and set ErrorCodes to the provider's documented error-code table.
type BaseProvider struct {
	ProviderName string
	Config       ProviderConfig
	// ErrorCodes maps the provider's documented raw codes to normalized codes.
	// Codes missing from the table resolve to UNKNOWN.
	ErrorCodes map[string]PaymentErrorCode
}

// NewBaseProvider applies the shared defaults: locale "tr", a nop logger and
// validation enabled.
func NewBaseProvider(name string, config ProviderConfig) BaseProvider {
	if config.Locale == "" {
		config.Locale = "tr"
	}
	if config.Logger == nil {
		config.Logger = NopLogger{}
	}
	return BaseProvider{ProviderName: name, Config: config}
}

// Name returns the provider identifier used in logs and errors.
func (b BaseProvider) Name() string { return b.ProviderName }

// ValidationEnabled reports whether requests are validated before the call.
func (b BaseProvider) ValidationEnabled() bool {
	return b.Config.Validate == nil || *b.Config.Validate
}

// ValidatePayment returns a ValidationError naming the invalid fields, unless
// validation is disabled. Call it before building the provider request.
func (b BaseProvider) ValidatePayment(request *PaymentRequest, rules PaymentValidationRules) error {
	if !b.ValidationEnabled() {
		return nil
	}
	return ValidatePaymentRequest(request, rules, b.ProviderName)
}

// ValidateRefund validates a refund amount unless validation is disabled.
func (b BaseProvider) ValidateRefund(request *RefundRequest) error {
	if !b.ValidationEnabled() || request == nil {
		return nil
	}
	return ValidateRefundAmount(request.Price, b.ProviderName)
}

// ValidateCapture validates a capture request unless validation is disabled.
func (b BaseProvider) ValidateCapture(request *CaptureRequest) error {
	if !b.ValidationEnabled() {
		return nil
	}
	return ValidateCaptureRequest(request, b.ProviderName)
}

// ResolveErrorCode maps a raw provider code through the provider's table.
func (b BaseProvider) ResolveErrorCode(raw string) PaymentErrorCode {
	return ResolveErrorCode(raw, b.ErrorCodes)
}

// ErrorCodeFor returns the normalized code for a result: failures and
// NETWORK_ERROR results get one, everything else gets the empty code.
func (b BaseProvider) ErrorCodeFor(status PaymentStatus, rawCode string) PaymentErrorCode {
	if status != StatusFailure && rawCode != NetworkErrorCode {
		return ""
	}
	return b.ResolveErrorCode(rawCode)
}

// CardOf returns the request's card, or a ValidationError when it is missing.
func (b BaseProvider) CardOf(request *PaymentRequest) (*PaymentCard, error) {
	if request == nil || request.PaymentCard == nil {
		return nil, NewValidationError("paymentCard is required", b.ProviderName, []ValidationIssue{
			{Path: "paymentCard", Message: "is required"},
		})
	}
	return request.PaymentCard, nil
}

// NotSupported builds the error for a feature this provider does not implement.
func (b BaseProvider) NotSupported(feature string) *BaseError {
	return NotSupported(b.ProviderName, feature, "")
}

// NewHTTPClient builds the provider's HTTP client with the configured logger,
// retry policy (idempotent requests only) and transport.
func (b BaseProvider) NewHTTPClient(timeout time.Duration, headers map[string]string) *HTTPClient {
	return NewHTTPClient(HTTPClientOptions{
		Provider: b.ProviderName,
		BaseURL:  b.Config.BaseURL,
		Timeout:  timeout,
		Headers:  headers,
		Doer:     b.Config.Doer,
		Logger:   b.Config.Logger,
		Retry:    b.Config.Retry,
	})
}

// Authorize blocks the amount on the card without charging it. Charge it later
// with Capture, or release it with VoidAuthorization.
func (b BaseProvider) Authorize(context.Context, *PaymentRequest) (*PaymentResponse, error) {
	return nil, b.NotSupported("Pre-authorization")
}

// InitThreeDSAuthorize starts a pre-authorization with 3D Secure.
func (b BaseProvider) InitThreeDSAuthorize(context.Context, *ThreeDSPaymentRequest) (*ThreeDSInitResponse, error) {
	return nil, b.NotSupported("Pre-authorization")
}

// Capture charges all or part of a pre-authorized amount.
func (b BaseProvider) Capture(context.Context, *CaptureRequest) (*PaymentResponse, error) {
	return nil, b.NotSupported("Capture")
}

// VoidAuthorization releases a pre-authorization; nothing is charged.
func (b BaseProvider) VoidAuthorization(context.Context, *VoidAuthorizationRequest) (*CancelResponse, error) {
	return nil, b.NotSupported("Voiding a pre-authorization")
}

// SaveCard stores a card with the provider without charging it.
func (b BaseProvider) SaveCard(context.Context, *SaveCardRequest) (*SaveCardResponse, error) {
	return nil, b.NotSupported("Card storage")
}

// ListCards returns a customer's saved cards.
func (b BaseProvider) ListCards(context.Context, *ListCardsRequest) (*ListCardsResponse, error) {
	return nil, b.NotSupported("Card storage")
}

// DeleteCard removes a saved card.
func (b BaseProvider) DeleteCard(context.Context, *DeleteCardRequest) (*DeleteCardResponse, error) {
	return nil, b.NotSupported("Card storage")
}

// BinCheck looks up a BIN.
func (b BaseProvider) BinCheck(context.Context, string) (*BinCheckResponse, error) {
	return nil, NotSupported(b.ProviderName, "BIN check", "")
}

// InstallmentInfo queries installment options.
func (b BaseProvider) InstallmentInfo(context.Context, *InstallmentInfoRequest) (*InstallmentInfoResponse, error) {
	return nil, NotSupported(b.ProviderName, "Installment info", "")
}
