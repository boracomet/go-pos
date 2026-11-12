package iyzico

import (
	"context"
	"net/http"
	"net/url"

	"github.com/boracomet/go-pos/core"
)

// finishSubscription maps the API status of a decoded subscription response.
// The decoded Status still holds the provider's raw status when it is called.
func (p *Provider) finishSubscription(status *core.PaymentStatus, code *core.PaymentErrorCode, errorCode string) {
	*status = mapStatus(string(*status))
	*code = p.ErrorCodeFor(*status, errorCode)
}

func (p *Provider) subscriptionLocale(requestLocale string) string {
	if requestLocale != "" {
		return requestLocale
	}
	return p.locale()
}

// InitializeSubscription starts a subscription (/v2/subscription/initialize).
func (p *Provider) InitializeSubscription(ctx context.Context, req *core.SubscriptionInitializeRequest) (*core.SubscriptionInitializeResponse, error) {
	if req == nil {
		return p.failSubscriptionInitialize(missingRequest()), nil
	}

	var shipping *apiAddress
	if req.Customer.ShippingAddress != nil {
		mapped := mapAddress(*req.Customer.ShippingAddress)
		shipping = &mapped
	}
	body := apiSubscriptionInitializeRequest{
		Locale:                    p.subscriptionLocale(req.Locale),
		ConversationID:            req.ConversationID,
		PricingPlanReferenceCode:  req.PricingPlanReferenceCode,
		SubscriptionInitialStatus: req.SubscriptionInitialStatus,
		Customer: apiSubscriptionCustomer{
			Name:            req.Customer.Name,
			Surname:         req.Customer.Surname,
			Email:           req.Customer.Email,
			GSMNumber:       req.Customer.GSMNumber,
			IdentityNumber:  req.Customer.IdentityNumber,
			BillingAddress:  mapAddress(req.Customer.BillingAddress),
			ShippingAddress: shipping,
		},
		PaymentCard: apiSubscriptionCard{
			CardHolderName: req.PaymentCard.CardHolderName,
			CardNumber:     req.PaymentCard.CardNumber,
			ExpireMonth:    req.PaymentCard.ExpireMonth,
			ExpireYear:     req.PaymentCard.ExpireYear,
			CVC:            req.PaymentCard.CVC,
		},
	}

	resp, err := p.sendRequest(ctx, "/v2/subscription/initialize", body, requestOptions{})
	if err != nil {
		return p.failSubscriptionInitialize(err), nil
	}
	out := &core.SubscriptionInitializeResponse{}
	decodeBody(resp, out)
	p.finishSubscription(&out.Status, &out.Code, out.ErrorCode)
	return out, nil
}

func (p *Provider) failSubscriptionInitialize(err error) *core.SubscriptionInitializeResponse {
	out := core.ApplyFailure("iyzico", err, "Subscription initialization failed", &core.SubscriptionInitializeResponse{})
	out.Code = p.ErrorCodeFor(out.Status, out.ErrorCode)
	return out
}

// CancelSubscription cancels a subscription
// (/v2/subscription/subscriptions/{referenceCode}/cancel).
func (p *Provider) CancelSubscription(ctx context.Context, req *core.SubscriptionCancelRequest) (*core.SubscriptionCancelResponse, error) {
	if req == nil {
		return p.failSubscriptionCancel(missingRequest()), nil
	}
	endpoint := "/v2/subscription/subscriptions/" + url.PathEscape(req.SubscriptionReferenceCode) + "/cancel"
	resp, err := p.sendRequest(ctx, endpoint, struct{}{}, requestOptions{})
	if err != nil {
		return p.failSubscriptionCancel(err), nil
	}
	out := &core.SubscriptionCancelResponse{}
	decodeBody(resp, out)
	p.finishSubscription(&out.Status, &out.Code, out.ErrorCode)
	return out, nil
}

func (p *Provider) failSubscriptionCancel(err error) *core.SubscriptionCancelResponse {
	out := core.ApplyFailure("iyzico", err, "Subscription cancellation failed", &core.SubscriptionCancelResponse{})
	out.Code = p.ErrorCodeFor(out.Status, out.ErrorCode)
	return out
}

// UpgradeSubscription moves a subscription to another pricing plan
// (/v2/subscription/subscriptions/{referenceCode}/upgrade).
func (p *Provider) UpgradeSubscription(ctx context.Context, req *core.SubscriptionUpgradeRequest) (*core.SubscriptionUpgradeResponse, error) {
	if req == nil {
		return p.failSubscriptionUpgrade(missingRequest()), nil
	}
	endpoint := "/v2/subscription/subscriptions/" + url.PathEscape(req.SubscriptionReferenceCode) + "/upgrade"
	resp, err := p.sendRequest(ctx, endpoint, apiSubscriptionUpgradeRequest{
		NewPricingPlanReferenceCode: req.NewPricingPlanReferenceCode,
		UseTrial:                    req.UseTrial,
		ResetRecurrenceCount:        req.ResetRecurrenceCount,
	}, requestOptions{})
	if err != nil {
		return p.failSubscriptionUpgrade(err), nil
	}
	out := &core.SubscriptionUpgradeResponse{}
	decodeBody(resp, out)
	p.finishSubscription(&out.Status, &out.Code, out.ErrorCode)
	return out, nil
}

func (p *Provider) failSubscriptionUpgrade(err error) *core.SubscriptionUpgradeResponse {
	out := core.ApplyFailure("iyzico", err, "Subscription upgrade failed", &core.SubscriptionUpgradeResponse{})
	out.Code = p.ErrorCodeFor(out.Status, out.ErrorCode)
	return out
}

// RetrieveSubscription reads a subscription
// (GET /v2/subscription/subscriptions/{referenceCode}).
func (p *Provider) RetrieveSubscription(ctx context.Context, req *core.SubscriptionRetrieveRequest) (*core.SubscriptionRetrieveResponse, error) {
	if req == nil {
		return p.failSubscriptionRetrieve(missingRequest()), nil
	}
	endpoint := "/v2/subscription/subscriptions/" + url.PathEscape(req.SubscriptionReferenceCode)
	resp, err := p.sendRequest(ctx, endpoint, struct{}{}, requestOptions{Method: http.MethodGet, Retryable: true})
	if err != nil {
		return p.failSubscriptionRetrieve(err), nil
	}
	out := &core.SubscriptionRetrieveResponse{}
	decodeBody(resp, out)
	p.finishSubscription(&out.Status, &out.Code, out.ErrorCode)
	return out, nil
}

func (p *Provider) failSubscriptionRetrieve(err error) *core.SubscriptionRetrieveResponse {
	out := core.ApplyFailure("iyzico", err, "Subscription retrieve failed", &core.SubscriptionRetrieveResponse{})
	out.Code = p.ErrorCodeFor(out.Status, out.ErrorCode)
	return out
}

// UpdateSubscriptionCard starts a hosted card-update form
// (/v2/subscription/card-update/checkoutform/initialize).
func (p *Provider) UpdateSubscriptionCard(ctx context.Context, req *core.SubscriptionCardUpdateRequest) (*core.SubscriptionCardUpdateResponse, error) {
	if req == nil {
		return p.failSubscriptionCardUpdate(missingRequest()), nil
	}
	resp, err := p.sendRequest(ctx, "/v2/subscription/card-update/checkoutform/initialize", apiSubscriptionCardUpdateRequest{
		Locale:                    p.subscriptionLocale(req.Locale),
		ConversationID:            req.ConversationID,
		SubscriptionReferenceCode: req.SubscriptionReferenceCode,
		CallbackURL:               req.CallbackURL,
	}, requestOptions{})
	if err != nil {
		return p.failSubscriptionCardUpdate(err), nil
	}
	out := &core.SubscriptionCardUpdateResponse{}
	decodeBody(resp, out)
	p.finishSubscription(&out.Status, &out.Code, out.ErrorCode)
	return out, nil
}

func (p *Provider) failSubscriptionCardUpdate(err error) *core.SubscriptionCardUpdateResponse {
	out := core.ApplyFailure("iyzico", err, "Card update initialization failed", &core.SubscriptionCardUpdateResponse{})
	out.Code = p.ErrorCodeFor(out.Status, out.ErrorCode)
	return out
}

// CreateSubscriptionProduct creates a subscription product (/v2/subscription/products).
func (p *Provider) CreateSubscriptionProduct(ctx context.Context, req *core.SubscriptionProductCreateRequest) (*core.SubscriptionProductResponse, error) {
	if req == nil {
		return p.failSubscriptionProduct(missingRequest()), nil
	}
	resp, err := p.sendRequest(ctx, "/v2/subscription/products", apiSubscriptionProductRequest{
		Locale:         p.subscriptionLocale(req.Locale),
		ConversationID: req.ConversationID,
		Name:           req.Name,
		Description:    req.Description,
	}, requestOptions{})
	if err != nil {
		return p.failSubscriptionProduct(err), nil
	}
	out := &core.SubscriptionProductResponse{}
	decodeBody(resp, out)
	p.finishSubscription(&out.Status, &out.Code, out.ErrorCode)
	return out, nil
}

func (p *Provider) failSubscriptionProduct(err error) *core.SubscriptionProductResponse {
	out := core.ApplyFailure("iyzico", err, "Product creation failed", &core.SubscriptionProductResponse{})
	out.Code = p.ErrorCodeFor(out.Status, out.ErrorCode)
	return out
}

// CreatePricingPlan creates a pricing plan for a product
// (/v2/subscription/products/{referenceCode}/pricing-plans).
func (p *Provider) CreatePricingPlan(ctx context.Context, req *core.PricingPlanCreateRequest) (*core.PricingPlanResponse, error) {
	if req == nil {
		return p.failPricingPlan(missingRequest()), nil
	}
	currency := req.Currency
	if currency == "" {
		currency = "TRY"
	}
	endpoint := "/v2/subscription/products/" + url.PathEscape(req.ProductReferenceCode) + "/pricing-plans"
	resp, err := p.sendRequest(ctx, endpoint, apiPricingPlanRequest{
		Locale:               p.subscriptionLocale(req.Locale),
		ConversationID:       req.ConversationID,
		ProductReferenceCode: req.ProductReferenceCode,
		Name:                 req.Name,
		Price:                req.Price,
		Currency:             currency,
		PaymentInterval:      req.PaymentInterval,
		PaymentIntervalCount: req.PaymentIntervalCount,
		TrialPeriodDays:      req.TrialPeriodDays,
		RecurrenceCount:      req.RecurrenceCount,
	}, requestOptions{})
	if err != nil {
		return p.failPricingPlan(err), nil
	}
	out := &core.PricingPlanResponse{}
	decodeBody(resp, out)
	p.finishSubscription(&out.Status, &out.Code, out.ErrorCode)
	return out, nil
}

func (p *Provider) failPricingPlan(err error) *core.PricingPlanResponse {
	out := core.ApplyFailure("iyzico", err, "Pricing plan creation failed", &core.PricingPlanResponse{})
	out.Code = p.ErrorCodeFor(out.Status, out.ErrorCode)
	return out
}
