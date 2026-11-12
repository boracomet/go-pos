package plugins

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"

	"github.com/boracomet/go-pos/core"
)

// CaptchaProvider is the captcha service tokens are verified with.
type CaptchaProvider string

// The supported captcha services.
const (
	CaptchaProviderTurnstile CaptchaProvider = "cloudflare-turnstile"
	CaptchaProviderGoogle    CaptchaProvider = "google-recaptcha"
	CaptchaProviderHCaptcha  CaptchaProvider = "hcaptcha"
)

// CaptchaOptions configures the captcha plugin.
type CaptchaOptions struct {
	// Provider is the captcha service to verify tokens with.
	Provider CaptchaProvider
	// SecretKey is the secret key the captcha service issued.
	SecretKey string
	// MinScore is the minimum score required for Google reCAPTCHA v3, from 0.0 to
	// 1.0. Ignored for Turnstile and hCaptcha. Default: 0.5.
	MinScore *float64
	// Operations require captcha verification. Callbacks (3DS completion,
	// webhooks) are never verified. Default: createPayment, initThreeDSPayment,
	// authorize, initThreeDSAuthorize.
	Operations []core.PaymentOperation
	// GetToken extracts the token from a custom location in the request.
	GetToken func(request any) string
	// Doer is the transport used for verification, useful for tests or proxied
	// networks. Default: http.DefaultClient.
	Doer core.Doer
}

var captchaDefaultOperations = []core.PaymentOperation{
	core.OperationCreatePayment,
	core.OperationInitThreeDSPayment,
	core.OperationAuthorize,
	core.OperationInitThreeDSAuthorize,
}

var captchaSiteverifyURLs = map[CaptchaProvider]string{
	CaptchaProviderTurnstile: "https://challenges.cloudflare.com/turnstile/v0/siteverify",
	CaptchaProviderGoogle:    "https://www.google.com/recaptcha/api/siteverify",
	CaptchaProviderHCaptcha:  "https://api.hcaptcha.com/siteverify",
}

var captchaBodyTokenFields = []string{
	"captchaToken",
	"captcha",
	"cf-turnstile-response",
	"g-recaptcha-response",
	"h-captcha-response",
	"x-captcha-response",
}

var captchaHeaderTokenFields = []string{
	"x-captcha-response",
	"cf-turnstile-response",
	"g-recaptcha-response",
	"h-captcha-response",
}

// captchaExtractToken finds the token in the request body, then in its headers.
func captchaExtractToken(request any, custom func(any) string) string {
	if custom != nil {
		if token := custom(request); token != "" {
			return token
		}
	}
	for _, name := range captchaBodyTokenFields {
		if token, ok := pluginStringField(request, name); ok {
			if trimmed := strings.TrimSpace(token); trimmed != "" {
				return trimmed
			}
		}
	}
	if headers := pluginField(request, "headers"); headers != nil {
		for _, name := range captchaHeaderTokenFields {
			if token, ok := pluginStringField(headers, name); ok {
				if trimmed := strings.TrimSpace(token); trimmed != "" {
					return trimmed
				}
			}
		}
	}
	return ""
}

type captchaSiteverifyResponse struct {
	Success    bool     `json:"success"`
	Score      *float64 `json:"score"`
	ErrorCodes []string `json:"error-codes"`
}

type captchaVerification struct {
	valid  bool
	reason string
}

type captchaRuntime struct {
	provider      CaptchaProvider
	secretKey     string
	minScore      float64
	siteverifyURL string
	doer          core.Doer
}

func (r *captchaRuntime) verify(token, remoteIP string) captchaVerification {
	form := url.Values{}
	form.Set("secret", r.secretKey)
	form.Set("response", token)
	if remoteIP != "" {
		form.Set("remoteip", remoteIP)
	}
	request, err := http.NewRequestWithContext(context.Background(), http.MethodPost, r.siteverifyURL,
		strings.NewReader(form.Encode()))
	if err != nil {
		return captchaVerification{reason: err.Error()}
	}
	request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	response, err := r.doer.Do(request)
	if err != nil {
		return captchaVerification{reason: err.Error()}
	}
	if response.Body != nil {
		defer response.Body.Close()
	}
	if response.StatusCode < 200 || response.StatusCode > 299 {
		return captchaVerification{reason: fmt.Sprintf("HTTP %d", response.StatusCode)}
	}
	var body []byte
	if response.Body != nil {
		read, err := io.ReadAll(response.Body)
		if err != nil {
			return captchaVerification{reason: err.Error()}
		}
		body = read
	}
	var data captchaSiteverifyResponse
	if err := json.Unmarshal(body, &data); err != nil {
		return captchaVerification{reason: err.Error()}
	}
	if !data.Success {
		return captchaVerification{reason: strings.Join(data.ErrorCodes, ", ")}
	}
	if r.provider == CaptchaProviderGoogle && data.Score != nil && *data.Score < r.minScore {
		return captchaVerification{reason: fmt.Sprintf("Score %s below threshold %s",
			strconv.FormatFloat(*data.Score, 'g', -1, 64), strconv.FormatFloat(r.minScore, 'g', -1, 64))}
	}
	return captchaVerification{valid: true}
}

// captchaFailureResult builds the failure result of the operation's own result
// type, so the caller's type assertion still holds.
func captchaFailureResult(operation core.PaymentOperation, errorCode, errorMessage string) any {
	switch operation {
	case core.OperationInitThreeDSPayment, core.OperationInitThreeDSAuthorize:
		return &core.ThreeDSInitResponse{
			Status:       core.StatusFailure,
			ErrorCode:    errorCode,
			ErrorMessage: errorMessage,
		}
	case core.OperationRefund:
		return &core.RefundResponse{
			Status:       core.StatusFailure,
			ErrorCode:    errorCode,
			ErrorMessage: errorMessage,
		}
	case core.OperationCancel, core.OperationVoidAuthorization:
		return &core.CancelResponse{
			Status:       core.StatusFailure,
			ErrorCode:    errorCode,
			ErrorMessage: errorMessage,
		}
	default:
		return &core.PaymentResponse{
			Status:       core.StatusFailure,
			ErrorCode:    errorCode,
			ErrorMessage: errorMessage,
		}
	}
}

// Captcha verifies Cloudflare Turnstile, Google reCAPTCHA and hCaptcha tokens
// before payment operations run, protecting against automated card testing.
// Callbacks are never verified.
func Captcha(options CaptchaOptions) (core.Plugin, error) {
	siteverifyURL, known := captchaSiteverifyURLs[options.Provider]
	if options.Provider == "" || !known {
		return core.Plugin{}, core.NewConfigurationError(
			fmt.Sprintf("captcha: invalid or missing provider '%s'. Use 'cloudflare-turnstile', 'google-recaptcha', or 'hcaptcha'.",
				options.Provider), "")
	}
	if options.SecretKey == "" {
		return core.Plugin{}, core.NewConfigurationError("captcha: secretKey is required", "")
	}
	minScore := 0.5
	if options.MinScore != nil {
		minScore = *options.MinScore
	}
	operations := options.Operations
	if operations == nil {
		operations = captchaDefaultOperations
	}
	targetOps := make(map[core.PaymentOperation]struct{}, len(operations))
	for _, operation := range operations {
		targetOps[operation] = struct{}{}
	}
	runtime := &captchaRuntime{
		provider:      options.Provider,
		secretKey:     options.SecretKey,
		minScore:      minScore,
		siteverifyURL: siteverifyURL,
		doer:          pluginDoer(options.Doer),
	}

	return core.DefinePlugin(core.Plugin{
		ID:      PluginIDCaptcha,
		Options: map[string]any{"provider": options.Provider, "minScore": minScore},
		ErrorCodes: map[string]string{
			"CAPTCHA_MISSING": "Captcha token is required",
			"CAPTCHA_INVALID": "Captcha verification failed",
		},
		Hooks: core.PluginHooks{
			Before: []core.BeforeHook{
				{
					Matcher: func(ctx core.OperationContext) bool {
						_, targeted := targetOps[ctx.Operation]
						return targeted
					},
					Handler: func(ctx core.OperationContext) (*core.BeforeHookResult, error) {
						token := captchaExtractToken(ctx.Request, options.GetToken)
						if token == "" {
							return &core.BeforeHookResult{
								Result: captchaFailureResult(ctx.Operation, "CAPTCHA_MISSING", "Captcha token is required"),
							}, nil
						}
						verification := runtime.verify(token, pluginString(ctx.Request, "ip"))
						if !verification.valid {
							return &core.BeforeHookResult{
								Result: captchaFailureResult(ctx.Operation, "CAPTCHA_INVALID", "Captcha verification failed"),
							}, nil
						}
						return nil, nil
					},
				},
			},
		},
	}), nil
}
