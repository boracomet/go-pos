// Command plugins shows the three extension points: a before hook that reroutes
// an operation, an event listener, and a custom provider — plus the members and
// error codes a plugin adds to the payment object.
//
// It runs offline: the providers are the in-memory mock and a local custom one.
//
//	go run ./examples/plugins
package main

import (
	"context"
	"fmt"
	"log"

	"github.com/boracomet/go-pos"
	"github.com/boracomet/go-pos/core"
	"github.com/boracomet/go-pos/testing"
)

// routeAbove sends every payment above a limit to another provider and adds a
// "limits" member to the payment object. In production the target would be a
// real gateway, e.g. "paytr".
func routeAbove(limit, target string) core.Plugin {
	limitMinor, err := core.ToMinorUnits(limit, "limit")
	if err != nil {
		log.Fatal(err)
	}
	return core.DefinePlugin(core.Plugin{
		ID: "route-above",
		Hooks: core.PluginHooks{
			Before: []core.BeforeHook{{
				// Only routable calls can be sent to another provider: a call
				// made on a specific provider stays on it.
				Matcher: func(ctx core.OperationContext) bool {
					return ctx.Operation == core.OperationCreatePayment && ctx.Routable
				},
				Handler: func(ctx core.OperationContext) (*core.BeforeHookResult, error) {
					request, ok := ctx.Request.(*core.PaymentRequest)
					if !ok {
						return nil, nil
					}
					paid, err := core.ToMinorUnits(request.PaidPrice, "paidPrice")
					if err != nil {
						return nil, err
					}
					if paid > limitMinor {
						return &core.BeforeHookResult{Provider: target}, nil
					}
					return nil, nil
				},
			}},
		},
		Methods: func(context.Context, core.PluginContext) (map[string]any, error) {
			return map[string]any{"limits": map[string]string{"above": limit, "target": target}}, nil
		},
		ErrorCodes: map[string]string{"LIMIT_NOT_SET": "No limit configured"},
	})
}

// auditLog only listens to events.
func auditLog() core.Plugin {
	return core.DefinePlugin(core.Plugin{
		ID: "audit-log",
		Events: core.PaymentEventListeners{
			core.EventAll: func(_ context.Context, event *core.PaymentEvent) error {
				fmt.Println("olay:", event.Type, event.Provider, event.PaymentID)
				return nil
			},
			core.EventRefundFailed: func(_ context.Context, event *core.PaymentEvent) error {
				fmt.Println("iade başarısız:", event.Code)
				return nil
			},
		},
	})
}

// echoPos is a minimal custom provider: it approves everything and echoes the
// order id back. BaseProvider supplies the rest as NOT_SUPPORTED.
type echoPos struct {
	core.BaseProvider
	terminal string
}

func (p *echoPos) CreatePayment(_ context.Context, request *core.PaymentRequest) (*core.PaymentResponse, error) {
	return &core.PaymentResponse{
		Status:         core.StatusSuccess,
		ConversationID: request.ConversationID,
	}, nil
}

func (p *echoPos) InitThreeDSPayment(context.Context, *core.ThreeDSPaymentRequest) (*core.ThreeDSInitResponse, error) {
	return nil, p.NotSupported("3D Secure")
}

func (p *echoPos) CompleteThreeDSPayment(context.Context, any) (*core.PaymentResponse, error) {
	return nil, p.NotSupported("3D Secure")
}

func (p *echoPos) Refund(context.Context, *core.RefundRequest) (*core.RefundResponse, error) {
	return nil, p.NotSupported("Refund")
}

func (p *echoPos) Cancel(context.Context, *core.CancelRequest) (*core.CancelResponse, error) {
	return nil, p.NotSupported("Cancel")
}

func (p *echoPos) GetPayment(context.Context, string) (*core.PaymentResponse, error) {
	return nil, p.NotSupported("GetPayment")
}

func echoPosDefinition(terminal string) core.ProviderDefinition {
	return core.DefineProvider(func(setup core.ProviderSetupContext) (core.Provider, error) {
		return &echoPos{
			BaseProvider: core.NewBaseProvider("echopos", core.ProviderConfig{Logger: setup.Logger}),
			terminal:     terminal,
		}, nil
	})
}

// charge only uses the operations, so it accepts any payment object.
func charge(ctx context.Context, payment *pos.Payment, request *core.PaymentRequest) (*core.PaymentResponse, error) {
	return payment.CreatePayment(ctx, request)
}

func requestFor(amount, orderID string) *core.PaymentRequest {
	request := testing.FixturePaymentRequest()
	request.Price = amount
	request.PaidPrice = amount
	request.ConversationID = orderID
	for i := range request.BasketItems {
		request.BasketItems[i].Price = amount
	}
	return &request
}

func main() {
	mock := testing.NewMockProvider()
	payment, err := pos.New(pos.Config{
		Providers: map[string]core.ProviderEntry{
			"mock":    mock,
			"echopos": echoPosDefinition("T1"),
		},
		DefaultProvider: "mock",
		Plugins:         []core.Plugin{routeAbove("5000.00", "echopos"), auditLog()},
	})
	if err != nil {
		log.Fatal(err)
	}

	ctx := context.Background()

	// Below the limit: stays on the default provider.
	fmt.Println("100.00 TL (limit altı):")
	result, err := charge(ctx, payment, requestFor("100.00", "SIPARIS-1"))
	if err != nil {
		log.Fatal(err)
	}
	fmt.Println("  sonuç:", result.Status, "ödeme:", result.PaymentID)

	// Above it: the before hook reroutes to echopos, which returns no payment id.
	fmt.Println("6000.00 TL (limit üstü):")
	result, err = charge(ctx, payment, requestFor("6000.00", "SIPARIS-2"))
	if err != nil {
		log.Fatal(err)
	}
	fmt.Println("  sonuç:", result.Status, "sipariş:", result.ConversationID)

	// A call made on a specific provider is not routable, so the hook leaves it.
	fmt.Println("6000.00 TL, doğrudan mock:")
	direct, err := payment.Use("mock")
	if err != nil {
		log.Fatal(err)
	}
	result, err = direct.CreatePayment(ctx, requestFor("6000.00", "SIPARIS-3"))
	if err != nil {
		log.Fatal(err)
	}
	fmt.Println("  sonuç:", result.Status, "ödeme:", result.PaymentID)

	// Queue a failure for the next mock payment.
	fmt.Println("bir sonraki mock ödemesi başarısız:")
	mock.FailNext(ctx, testing.OperationPayment, core.CodeInsufficientFunds)
	result, err = direct.CreatePayment(ctx, requestFor("100.00", "SIPARIS-4"))
	if err != nil {
		log.Fatal(err)
	}
	fmt.Println("  sonuç:", result.Status, "kod:", result.Code)

	// Plugin members and error codes.
	limits, _ := payment.Method("limits")
	fmt.Println("limit üyesi:", limits)
	fmt.Println("eklenti hata kodları:", payment.ErrorCodes())

	// Events: On returns an unsubscribe func.
	off := payment.On(core.EventPaymentFailed, func(_ context.Context, event *core.PaymentEvent) error {
		fmt.Println("  dinleyici:", event.Type, event.Code)
		return nil
	})
	defer off()

	fmt.Println("sağlayıcılar:", payment.EnabledProviders())
}
