package pos

import (
	"context"
	"fmt"
	"net/http"
	"time"

	"github.com/boracomet/go-pos/core"
)

// hookedProvider is the provider Use returns: every operation runs through Run,
// so the plugin hooks and the event listeners apply. Provider-specific methods
// are reached with RawProvider instead.
type hookedProvider struct {
	payment *Payment
	id      string
	raw     core.Provider
}

var _ core.Provider = (*hookedProvider)(nil)

func (h *hookedProvider) Name() string { return h.raw.Name() }

func (h *hookedProvider) CreatePayment(ctx context.Context, request *core.PaymentRequest) (*core.PaymentResponse, error) {
	return resultOf[core.PaymentResponse](h.run(ctx, core.OperationCreatePayment, request))
}

func (h *hookedProvider) InitThreeDSPayment(ctx context.Context, request *core.ThreeDSPaymentRequest) (*core.ThreeDSInitResponse, error) {
	return resultOf[core.ThreeDSInitResponse](h.run(ctx, core.OperationInitThreeDSPayment, request))
}

func (h *hookedProvider) CompleteThreeDSPayment(ctx context.Context, callbackData any) (*core.PaymentResponse, error) {
	return resultOf[core.PaymentResponse](h.run(ctx, core.OperationCompleteThreeDSPayment, callbackData))
}

func (h *hookedProvider) Refund(ctx context.Context, request *core.RefundRequest) (*core.RefundResponse, error) {
	return resultOf[core.RefundResponse](h.run(ctx, core.OperationRefund, request))
}

func (h *hookedProvider) Cancel(ctx context.Context, request *core.CancelRequest) (*core.CancelResponse, error) {
	return resultOf[core.CancelResponse](h.run(ctx, core.OperationCancel, request))
}

func (h *hookedProvider) GetPayment(ctx context.Context, paymentID string) (*core.PaymentResponse, error) {
	return resultOf[core.PaymentResponse](h.run(ctx, core.OperationGetPayment, paymentID))
}

func (h *hookedProvider) Authorize(ctx context.Context, request *core.PaymentRequest) (*core.PaymentResponse, error) {
	return resultOf[core.PaymentResponse](h.run(ctx, core.OperationAuthorize, request))
}

func (h *hookedProvider) InitThreeDSAuthorize(ctx context.Context, request *core.ThreeDSPaymentRequest) (*core.ThreeDSInitResponse, error) {
	return resultOf[core.ThreeDSInitResponse](h.run(ctx, core.OperationInitThreeDSAuthorize, request))
}

func (h *hookedProvider) Capture(ctx context.Context, request *core.CaptureRequest) (*core.PaymentResponse, error) {
	return resultOf[core.PaymentResponse](h.run(ctx, core.OperationCapture, request))
}

func (h *hookedProvider) VoidAuthorization(ctx context.Context, request *core.VoidAuthorizationRequest) (*core.CancelResponse, error) {
	return resultOf[core.CancelResponse](h.run(ctx, core.OperationVoidAuthorization, request))
}

func (h *hookedProvider) SaveCard(ctx context.Context, request *core.SaveCardRequest) (*core.SaveCardResponse, error) {
	return resultOf[core.SaveCardResponse](h.run(ctx, core.OperationSaveCard, request))
}

func (h *hookedProvider) ListCards(ctx context.Context, request *core.ListCardsRequest) (*core.ListCardsResponse, error) {
	return resultOf[core.ListCardsResponse](h.run(ctx, core.OperationListCards, request))
}

func (h *hookedProvider) DeleteCard(ctx context.Context, request *core.DeleteCardRequest) (*core.DeleteCardResponse, error) {
	return resultOf[core.DeleteCardResponse](h.run(ctx, core.OperationDeleteCard, request))
}

func (h *hookedProvider) BinCheck(ctx context.Context, binNumber string) (*core.BinCheckResponse, error) {
	return resultOf[core.BinCheckResponse](h.run(ctx, core.OperationBinCheck, binNumber))
}

func (h *hookedProvider) InstallmentInfo(ctx context.Context, request *core.InstallmentInfoRequest) (*core.InstallmentInfoResponse, error) {
	return resultOf[core.InstallmentInfoResponse](h.run(ctx, core.OperationInstallmentInfo, request))
}

// run keeps the provider id: a call made on a provider is never routed elsewhere.
func (h *hookedProvider) run(ctx context.Context, operation core.PaymentOperation, request any) (any, error) {
	return h.payment.Run(ctx, operation, h.id, request, core.RunOptions{Routable: false})
}

// pluginContext is what plugins get in Init, Methods and endpoints.
type pluginContext struct {
	payment *Payment
}

var _ core.PluginContext = pluginContext{}

func (c pluginContext) ProviderIDs() []string         { return c.payment.EnabledProviders() }
func (c pluginContext) ErrorCodes() map[string]string { return c.payment.ErrorCodes() }
func (c pluginContext) DefaultProvider() string       { return c.payment.defaultProvider }
func (c pluginContext) Mode() core.Mode               { return c.payment.mode }
func (c pluginContext) Logger() core.Logger           { return c.payment.logger }

func (c pluginContext) Use(providerID string) (core.Provider, error) {
	return c.payment.Use(providerID)
}

func (c pluginContext) Emit(ctx context.Context, event *core.PaymentEvent) error {
	return c.payment.emitEvent(ctx, event)
}

func (c pluginContext) Endpoints() []core.EndpointInfo {
	endpoints := c.payment.endpoints
	infos := make([]core.EndpointInfo, 0, len(endpoints))
	for _, endpoint := range endpoints {
		infos = append(infos, core.EndpointInfo{
			PluginID:   endpoint.PluginID,
			Name:       endpoint.Name,
			Method:     endpoint.Method,
			Path:       endpoint.Path,
			Privileged: endpoint.Privileged,
			OpenAPI:    endpoint.OpenAPI,
		})
	}
	return infos
}

// ready runs the plugins' Init once, in order. When one fails the operation fails
// and the next operation retries from that plugin on.
func (p *Payment) ready(ctx context.Context) error {
	p.initMu.Lock()
	defer p.initMu.Unlock()
	for ; p.initialized < len(p.plugins); p.initialized++ {
		plugin := p.plugins[p.initialized]
		if plugin.Init == nil {
			continue
		}
		if err := plugin.Init(ctx, p.pluginCtx); err != nil {
			return err
		}
	}
	return nil
}

// emitEvent notifies the listeners of the event type, then the listeners of every
// event, in registration order. The first failure stops the rest and is reported
// with the result, so a completed payment is never lost.
func (p *Payment) emitEvent(ctx context.Context, event *core.PaymentEvent) error {
	p.listenerMu.RLock()
	entries := p.listeners[core.PaymentEventName(event.Type)]
	all := p.listeners[core.EventAll]
	listeners := make([]core.PaymentEventListener, 0, len(entries)+len(all))
	for _, entry := range entries {
		listeners = append(listeners, entry.listener)
	}
	for _, entry := range all {
		listeners = append(listeners, entry.listener)
	}
	p.listenerMu.RUnlock()

	for _, listener := range listeners {
		if err := guarded(func() error { return listener(ctx, event) }); err != nil {
			return core.NewEventListenerError(event, event.Result, err)
		}
	}
	return nil
}

// InterceptRequest runs the plugins' OnRequest hooks before routing. The first
// response short-circuits the request.
func (p *Payment) InterceptRequest(ctx context.Context, request *core.HandlerRequest, rctx core.RequestContext) (*core.HandlerResponse, error) {
	for _, hook := range p.requestHooks {
		var response *core.HandlerResponse
		err := guarded(func() error {
			var hookErr error
			response, hookErr = hook.hook(ctx, request, rctx)
			return hookErr
		})
		if err != nil {
			// Fail closed: the hook is a guard (rate limit, IP filter) that could not
			// decide. Only its id is logged, never the request.
			if p.logger != nil {
				p.logger.Error(fmt.Sprintf("onRequest of plugin '%s' failed", hook.pluginID), err, nil)
			}
			return &core.HandlerResponse{
				Status:  http.StatusServiceUnavailable,
				Headers: map[string]string{"Content-Type": "application/json"},
				Body: map[string]any{
					"error":     true,
					"message":   "Service unavailable",
					"timestamp": time.Now().UTC().Format("2006-01-02T15:04:05.000Z"),
				},
			}, nil
		}
		if response != nil {
			return response, nil
		}
	}
	return nil, nil
}

// Respond runs the plugins' OnResponse hooks on a handler response. A hook that
// fails is logged and the response is sent unchanged.
func (p *Payment) Respond(ctx context.Context, response *core.HandlerResponse, rctx core.ResponseContext) (*core.HandlerResponse, error) {
	for _, hook := range p.responseHooks {
		var replacement *core.HandlerResponse
		err := guarded(func() error {
			var hookErr error
			replacement, hookErr = hook.hook(ctx, response, rctx)
			return hookErr
		})
		if err != nil {
			if p.logger != nil {
				p.logger.Error(fmt.Sprintf("onResponse of plugin '%s' failed", hook.pluginID), err, nil)
			}
			continue
		}
		if replacement != nil {
			response = replacement
		}
	}
	return response, nil
}

// guarded turns a panic in a plugin callback into an error: a faulty listener or
// interception hook must not take down the caller.
func guarded(call func() error) (err error) {
	defer func() {
		if recovered := recover(); recovered != nil {
			err = fmt.Errorf("%v", recovered)
		}
	}()
	return call()
}
