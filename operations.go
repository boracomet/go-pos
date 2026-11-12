package pos

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/boracomet/go-pos/core"
)

// CreatePayment takes a payment without 3D Secure with the default provider.
func (p *Payment) CreatePayment(ctx context.Context, request *core.PaymentRequest) (*core.PaymentResponse, error) {
	return resultOf[core.PaymentResponse](p.call(ctx, core.OperationCreatePayment, request))
}

// InitThreeDSPayment starts a 3D Secure payment with the default provider.
func (p *Payment) InitThreeDSPayment(ctx context.Context, request *core.ThreeDSPaymentRequest) (*core.ThreeDSInitResponse, error) {
	return resultOf[core.ThreeDSInitResponse](p.call(ctx, core.OperationInitThreeDSPayment, request))
}

// CompleteThreeDSPayment completes a 3D Secure payment with the default provider.
// callbackData is the provider's callback: its own callback type, or the parsed
// body of the notification.
func (p *Payment) CompleteThreeDSPayment(ctx context.Context, callbackData any) (*core.PaymentResponse, error) {
	return resultOf[core.PaymentResponse](p.call(ctx, core.OperationCompleteThreeDSPayment, callbackData))
}

// Refund refunds a payment with the default provider.
func (p *Payment) Refund(ctx context.Context, request *core.RefundRequest) (*core.RefundResponse, error) {
	return resultOf[core.RefundResponse](p.call(ctx, core.OperationRefund, request))
}

// Cancel cancels a payment with the default provider.
func (p *Payment) Cancel(ctx context.Context, request *core.CancelRequest) (*core.CancelResponse, error) {
	return resultOf[core.CancelResponse](p.call(ctx, core.OperationCancel, request))
}

// Authorize blocks an amount without charging it, with the default provider.
func (p *Payment) Authorize(ctx context.Context, request *core.PaymentRequest) (*core.PaymentResponse, error) {
	return resultOf[core.PaymentResponse](p.call(ctx, core.OperationAuthorize, request))
}

// InitThreeDSAuthorize starts a 3D Secure pre-authorization with the default
// provider.
func (p *Payment) InitThreeDSAuthorize(ctx context.Context, request *core.ThreeDSPaymentRequest) (*core.ThreeDSInitResponse, error) {
	return resultOf[core.ThreeDSInitResponse](p.call(ctx, core.OperationInitThreeDSAuthorize, request))
}

// Capture charges a pre-authorization with the default provider.
func (p *Payment) Capture(ctx context.Context, request *core.CaptureRequest) (*core.PaymentResponse, error) {
	return resultOf[core.PaymentResponse](p.call(ctx, core.OperationCapture, request))
}

// VoidAuthorization releases a pre-authorization with the default provider.
func (p *Payment) VoidAuthorization(ctx context.Context, request *core.VoidAuthorizationRequest) (*core.CancelResponse, error) {
	return resultOf[core.CancelResponse](p.call(ctx, core.OperationVoidAuthorization, request))
}

// SaveCard stores a card with the default provider.
func (p *Payment) SaveCard(ctx context.Context, request *core.SaveCardRequest) (*core.SaveCardResponse, error) {
	return resultOf[core.SaveCardResponse](p.call(ctx, core.OperationSaveCard, request))
}

// ListCards lists a customer's saved cards with the default provider.
func (p *Payment) ListCards(ctx context.Context, request *core.ListCardsRequest) (*core.ListCardsResponse, error) {
	return resultOf[core.ListCardsResponse](p.call(ctx, core.OperationListCards, request))
}

// DeleteCard deletes a saved card with the default provider.
func (p *Payment) DeleteCard(ctx context.Context, request *core.DeleteCardRequest) (*core.DeleteCardResponse, error) {
	return resultOf[core.DeleteCardResponse](p.call(ctx, core.OperationDeleteCard, request))
}

// GetPayment queries a payment with the default provider.
func (p *Payment) GetPayment(ctx context.Context, paymentID string) (*core.PaymentResponse, error) {
	return resultOf[core.PaymentResponse](p.call(ctx, core.OperationGetPayment, paymentID))
}

// BinCheck looks up the first 6-8 digits of a card with the default provider.
func (p *Payment) BinCheck(ctx context.Context, binNumber string) (*core.BinCheckResponse, error) {
	return resultOf[core.BinCheckResponse](p.call(ctx, core.OperationBinCheck, binNumber))
}

// InstallmentInfo lists the installment options of a card with the default
// provider.
func (p *Payment) InstallmentInfo(ctx context.Context, request *core.InstallmentInfoRequest) (*core.InstallmentInfoResponse, error) {
	return resultOf[core.InstallmentInfoResponse](p.call(ctx, core.OperationInstallmentInfo, request))
}

// call runs an operation on the default provider. Only these calls are routable:
// a before hook may move them to another provider.
func (p *Payment) call(ctx context.Context, operation core.PaymentOperation, request any) (any, error) {
	return p.Run(ctx, operation, p.defaultProvider, request, core.RunOptions{Routable: true})
}

// Run executes an operation: the plugins' Init, before hooks, the provider, after
// hooks and events. A before hook may replace the request or short-circuit with a
// result; moving the call to another provider is only allowed when
// options.Routable is set.
func (p *Payment) Run(ctx context.Context, operation core.PaymentOperation, providerID string, request any, options core.RunOptions) (any, error) {
	if err := p.ready(ctx); err != nil {
		return nil, err
	}

	octx := core.OperationContext{
		Operation: operation,
		Request:   request,
		Provider:  providerID,
		Routable:  options.Routable,
	}
	var result any
	for _, hook := range p.beforeHooks {
		if hook.Matcher != nil && !hook.Matcher(octx) {
			continue
		}
		out, err := hook.Handler(octx)
		if err != nil {
			return nil, err
		}
		if out == nil {
			continue
		}
		if out.Provider != "" && out.Provider != octx.Provider {
			if !options.Routable {
				return nil, core.NewConfigurationError(fmt.Sprintf(
					"A plugin hook tried to move a '%s' call made on provider '%s' to '%s'. "+
						"Only calls made on the payment object can be routed.",
					operation, octx.Provider, out.Provider), octx.Provider)
			}
			if !p.IsProviderEnabled(out.Provider) {
				return nil, core.NewProviderNotEnabledError(out.Provider)
			}
			octx.Provider = out.Provider
		}
		if out.Request != nil {
			octx.Request = out.Request
		}
		if out.Result != nil {
			result = out.Result
			break
		}
	}

	if result == nil {
		provider, err := p.RawProvider(octx.Provider)
		if err != nil {
			return nil, err
		}
		result, err = callOperation(ctx, provider, operation, octx.Request)
		if err != nil {
			return nil, err
		}
	}

	after := core.OperationResultContext{OperationContext: octx, Result: result}
	for _, hook := range p.afterHooks {
		if hook.Matcher != nil && !hook.Matcher(after) {
			continue
		}
		out, err := hook.Handler(after)
		if err != nil {
			return nil, err
		}
		if out == nil {
			continue
		}
		if out.Result != nil {
			after.Result = out.Result
		}
		if out.Request != nil {
			after.Request = out.Request
		}
	}

	if options.Emit == nil || *options.Emit {
		if err := p.Emit(ctx, operation, after.Provider, after.Request, after.Result); err != nil {
			return nil, err
		}
	}
	return after.Result, nil
}

// Emit emits the event of an operation result, if it has one. A result whose
// signature did not verify emits nothing.
func (p *Payment) Emit(ctx context.Context, operation core.PaymentOperation, providerID string, request, result any) error {
	event := core.EventFor(operation, providerID, request, result)
	if event == nil {
		return nil
	}
	return p.emitEvent(ctx, event)
}

// callOperation runs one operation on a provider. The request is the operation's
// typed request, or a parsed body a hook or the handler passed instead.
func callOperation(ctx context.Context, provider core.Provider, operation core.PaymentOperation, request any) (any, error) {
	switch operation {
	case core.OperationCreatePayment:
		return runWith(ctx, request, provider.CreatePayment)
	case core.OperationInitThreeDSPayment:
		return runWith(ctx, request, provider.InitThreeDSPayment)
	case core.OperationCompleteThreeDSPayment:
		return provider.CompleteThreeDSPayment(ctx, request)
	case core.OperationAuthorize:
		return runWith(ctx, request, provider.Authorize)
	case core.OperationInitThreeDSAuthorize:
		return runWith(ctx, request, provider.InitThreeDSAuthorize)
	case core.OperationCapture:
		return runWith(ctx, request, provider.Capture)
	case core.OperationVoidAuthorization:
		return runWith(ctx, request, provider.VoidAuthorization)
	case core.OperationRefund:
		return runWith(ctx, request, provider.Refund)
	case core.OperationCancel:
		return runWith(ctx, request, provider.Cancel)
	case core.OperationGetPayment:
		return provider.GetPayment(ctx, stringRequest(request))
	case core.OperationSaveCard:
		return runWith(ctx, request, provider.SaveCard)
	case core.OperationListCards:
		return runWith(ctx, request, provider.ListCards)
	case core.OperationDeleteCard:
		return runWith(ctx, request, provider.DeleteCard)
	case core.OperationBinCheck:
		return provider.BinCheck(ctx, stringRequest(request))
	case core.OperationInstallmentInfo:
		return runWith(ctx, request, provider.InstallmentInfo)
	default:
		return nil, core.NewError(fmt.Sprintf("Unknown operation %s", operation), "INVALID_REQUEST")
	}
}

func runWith[Request any, Response any](
	ctx context.Context,
	request any,
	call func(context.Context, *Request) (Response, error),
) (any, error) {
	typed, err := requestAs[Request](request)
	if err != nil {
		return nil, err
	}
	return call(ctx, typed)
}

// requestAs accepts the typed request of an operation, or a parsed body.
func requestAs[T any](request any) (*T, error) {
	switch value := request.(type) {
	case *T:
		return value, nil
	case T:
		return &value, nil
	}
	data, err := json.Marshal(request)
	if err != nil {
		return nil, err
	}
	var out T
	if err := json.Unmarshal(data, &out); err != nil {
		return nil, core.NewError(fmt.Sprintf("Invalid %T request", out), "INVALID_REQUEST")
	}
	return &out, nil
}

func stringRequest(request any) string {
	if value, ok := request.(string); ok {
		return value
	}
	return ""
}

// resultOf converts an operation result to its type. A hook may have replaced it
// with a wrapper (a routed response) or a plain object, so the conversion goes
// through JSON when the types differ.
func resultOf[T any](result any, err error) (*T, error) {
	if err != nil {
		return nil, err
	}
	switch value := result.(type) {
	case nil:
		return nil, nil
	case *T:
		return value, nil
	case T:
		return &value, nil
	}
	data, err := json.Marshal(result)
	if err != nil {
		return nil, err
	}
	var out T
	if err := json.Unmarshal(data, &out); err != nil {
		return nil, core.NewError(fmt.Sprintf("Unexpected result of type %T", result), "PROVIDER_ERROR")
	}
	return &out, nil
}
