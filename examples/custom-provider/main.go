// Command custom-provider adds a gateway the library does not ship with: embed
// core.BaseProvider, implement the operations the gateway actually has, and
// register it with core.DefineProvider.
//
//	go run ./examples/custom-provider
package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net/url"
	"time"

	"github.com/boracomet/go-pos"
	"github.com/boracomet/go-pos/core"
)

// MyPosConfig holds the credentials of the imaginary MyPos gateway.
type MyPosConfig struct {
	core.ProviderConfig
	TerminalID string
	SecretKey  string
}

type myPosResult struct {
	Approved      bool   `json:"approved"`
	TransactionID string `json:"transactionId"`
	ResponseCode  string `json:"responseCode"`
	Message       string `json:"message"`
}

// MyPos embeds BaseProvider, which supplies the optional operations as
// NOT_SUPPORTED, so only what the gateway has is written here.
type MyPos struct {
	core.BaseProvider
	config MyPosConfig
	client *core.HTTPClient
}

func NewMyPos(cfg MyPosConfig) (*MyPos, error) {
	if cfg.TerminalID == "" || cfg.SecretKey == "" {
		return nil, core.NewConfigurationError("MyPos: terminalId and secretKey are required", "mypos")
	}
	base := core.NewBaseProvider("mypos", cfg.ProviderConfig)
	base.ErrorCodes = map[string]core.PaymentErrorCode{
		"51": core.CodeInsufficientFunds,
		"54": core.CodeExpiredCard,
	}
	return &MyPos{
		BaseProvider: base,
		config:       cfg,
		client:       base.NewHTTPClient(30*time.Second, nil),
	}, nil
}

func (p *MyPos) CreatePayment(ctx context.Context, request *core.PaymentRequest) (*core.PaymentResponse, error) {
	// Card, amounts and required fields are checked before any API call.
	if err := p.ValidatePayment(request, core.PaymentValidationRules{
		Card:     true,
		Required: []string{"buyer.ip"},
	}); err != nil {
		var validation *core.ValidationError
		if errors.As(err, &validation) {
			return &core.PaymentResponse{
				Status:       core.StatusFailure,
				Code:         p.ResolveErrorCode("VALIDATION_ERROR"),
				ErrorCode:    "VALIDATION_ERROR",
				ErrorMessage: validation.Error(),
			}, nil
		}
		return nil, err
	}

	card, err := p.CardOf(request)
	if err != nil {
		return nil, err
	}
	body, err := json.Marshal(map[string]string{
		"terminalId": p.config.TerminalID,
		"orderId":    request.ConversationID,
		"amount":     request.PaidPrice,
		"cardNumber": card.CardNumber,
	})
	if err != nil {
		return nil, err
	}
	signature, err := core.HMAC(core.SHA256, []byte(p.config.SecretKey), body)
	if err != nil {
		return nil, err
	}

	resp, err := p.client.Post(ctx, "/payments", string(body), core.RequestConfig{
		Headers: map[string]string{"x-signature": core.ToHex(signature)},
	})
	if err != nil {
		var httpErr *core.HTTPError
		if errors.As(err, &httpErr) && httpErr.IsNetworkError {
			// No answer arrived: the charge may have gone through. Never report
			// it as failed, or the customer is charged twice on retry.
			return &core.PaymentResponse{
				Status:         core.StatusPending,
				Code:           core.CodeNetworkError,
				ErrorCode:      core.NetworkErrorCode,
				ConversationID: request.ConversationID,
			}, nil
		}
		return nil, err
	}

	var data myPosResult
	if err := json.Unmarshal([]byte(resp.Text), &data); err != nil {
		return nil, err
	}
	status := core.StatusFailure
	if data.Approved {
		status = core.StatusSuccess
	}
	return &core.PaymentResponse{
		Status:         status,
		PaymentID:      data.TransactionID,
		ConversationID: request.ConversationID,
		Code:           p.ErrorCodeFor(status, data.ResponseCode),
		ErrorCode:      data.ResponseCode,
		ErrorMessage:   data.Message,
		RawResponse:    data,
		HTTPStatus:     resp.Status,
	}, nil
}

func (p *MyPos) InitThreeDSPayment(context.Context, *core.ThreeDSPaymentRequest) (*core.ThreeDSInitResponse, error) {
	return nil, p.NotSupported("3D Secure")
}

func (p *MyPos) CompleteThreeDSPayment(context.Context, any) (*core.PaymentResponse, error) {
	return nil, p.NotSupported("3D Secure")
}

func (p *MyPos) Refund(ctx context.Context, request *core.RefundRequest) (*core.RefundResponse, error) {
	if err := p.ValidateRefund(request); err != nil {
		return nil, err
	}
	body, err := json.Marshal(map[string]string{
		"transactionId": request.PaymentID,
		"amount":        request.Price,
	})
	if err != nil {
		return nil, err
	}
	resp, err := p.client.Post(ctx, "/refunds", string(body), core.RequestConfig{})
	if err != nil {
		return nil, err
	}
	var data myPosResult
	if err := json.Unmarshal([]byte(resp.Text), &data); err != nil {
		return nil, err
	}
	status := core.StatusFailure
	if data.Approved {
		status = core.StatusSuccess
	}
	return &core.RefundResponse{
		Status:     status,
		RefundID:   data.TransactionID,
		Code:       p.ErrorCodeFor(status, data.ResponseCode),
		ErrorCode:  data.ResponseCode,
		HTTPStatus: resp.Status,
	}, nil
}

func (p *MyPos) Cancel(context.Context, *core.CancelRequest) (*core.CancelResponse, error) {
	return nil, p.NotSupported("Cancel")
}

func (p *MyPos) GetPayment(ctx context.Context, paymentID string) (*core.PaymentResponse, error) {
	// A read-only query, so it may be retried.
	resp, err := p.client.Get(ctx, "/payments/"+url.PathEscape(paymentID), core.RequestConfig{Retryable: true})
	if err != nil {
		return nil, err
	}
	var data myPosResult
	if err := json.Unmarshal([]byte(resp.Text), &data); err != nil {
		return nil, err
	}
	status := core.StatusFailure
	if data.Approved {
		status = core.StatusSuccess
	}
	return &core.PaymentResponse{
		Status:      status,
		PaymentID:   paymentID,
		RawResponse: data,
		HTTPStatus:  resp.Status,
	}, nil
}

// TerminalID exposes a gateway-specific member, reachable through RawProvider.
func (p *MyPos) TerminalID() string { return p.config.TerminalID }

// MyPosDefinition receives the shared setup: mode, logger, retry and transport.
func MyPosDefinition(cfg MyPosConfig) core.ProviderDefinition {
	return core.DefineProvider(func(setup core.ProviderSetupContext) (core.Provider, error) {
		if cfg.BaseURL == "" {
			cfg.BaseURL = "https://api.mypos.example"
			if setup.Mode == core.ModeSandbox {
				cfg.BaseURL = "https://test.mypos.example"
			}
		}
		if cfg.Logger == nil {
			cfg.Logger = setup.Logger
		}
		if cfg.Retry == nil {
			cfg.Retry = setup.Retry
		}
		if cfg.Doer == nil {
			cfg.Doer = setup.Doer
		}
		if cfg.Validate == nil {
			cfg.Validate = setup.Validate
		}
		return NewMyPos(cfg)
	})
}

func main() {
	payment, err := pos.New(pos.Config{
		Providers: map[string]core.ProviderEntry{
			"mypos": MyPosDefinition(MyPosConfig{TerminalID: "T1", SecretKey: "secret"}),
		},
		Mode: core.ModeSandbox,
	})
	if err != nil {
		log.Fatal(err)
	}

	fmt.Println("etkin sağlayıcılar:", payment.EnabledProviders())

	raw, err := payment.RawProvider("mypos")
	if err != nil {
		log.Fatal(err)
	}
	fmt.Println("terminal:", raw.(*MyPos).TerminalID())

	// The handler exposes the custom provider like any built-in one:
	// /api/pay/mypos/payment, /api/pay/mypos/callback, ...
	fmt.Println("sağlayıcı adı:", raw.Name())

	result, err := payment.CreatePayment(context.Background(), &core.PaymentRequest{
		Price:          "100.00",
		PaidPrice:      "100.00",
		Currency:       "TRY",
		ConversationID: "SIPARIS-1",
		Buyer:          core.Buyer{IP: "203.0.113.10"},
	})
	if err != nil {
		log.Fatal(err)
	}
	// No card was sent, so validation rejects it before any network call.
	fmt.Println("durum:", result.Status, "kod:", result.Code)
}
