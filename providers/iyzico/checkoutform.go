package iyzico

import (
	"context"

	"github.com/boracomet/go-pos/core"
)

// InitCheckoutForm starts a hosted checkout form
// (/payment/iyzipos/checkoutform/initialize/auth/ecom).
func (p *Provider) InitCheckoutForm(ctx context.Context, req *core.CheckoutFormRequest) (*core.CheckoutFormInitResponse, error) {
	if req == nil {
		return p.failCheckoutFormInit(missingRequest()), nil
	}
	if err := p.ValidatePayment(checkoutFormPaymentRequest(req), orderRules); err != nil {
		return p.failCheckoutFormInit(err), nil
	}

	resp, err := p.sendRequest(ctx, "/payment/iyzipos/checkoutform/initialize/auth/ecom", apiCheckoutFormRequest{
		Locale:              p.locale(),
		ConversationID:      req.ConversationID,
		Price:               req.Price,
		PaidPrice:           req.PaidPrice,
		Currency:            req.Currency,
		BasketID:            req.BasketID,
		PaymentGroup:        "PRODUCT",
		PaymentChannel:      "WEB",
		CallbackURL:         req.CallbackURL,
		EnabledInstallments: req.EnabledInstallments,
		Buyer:               mapBuyer(req.Buyer),
		ShippingAddress:     mapAddress(req.ShippingAddress),
		BillingAddress:      mapAddress(req.BillingAddress),
		BasketItems:         mapBasketItems(req.BasketItems),
	}, requestOptions{})
	if err != nil {
		return p.failCheckoutFormInit(err), nil
	}

	var body apiCheckoutFormInitResponse
	decodeBody(resp, &body)
	out := &core.CheckoutFormInitResponse{
		Status:              mapStatus(body.Status),
		CheckoutFormContent: body.CheckoutFormContent,
		PaymentPageURL:      body.PaymentPageURL,
		Token:               body.Token,
		TokenExpireTime:     body.TokenExpireTime,
		ConversationID:      body.ConversationID,
		ErrorCode:           body.ErrorCode,
		ErrorMessage:        body.ErrorMessage,
		RawResponse:         resp.Data,
	}
	out.Code = p.ErrorCodeFor(out.Status, out.ErrorCode)
	return out, nil
}

func (p *Provider) failCheckoutFormInit(err error) *core.CheckoutFormInitResponse {
	out := core.ApplyFailure("iyzico", err, "Checkout form initialization failed", &core.CheckoutFormInitResponse{})
	out.Code = p.ErrorCodeFor(out.Status, out.ErrorCode)
	return out
}

// RetrieveCheckoutForm queries the result of a hosted checkout form
// (/payment/iyzipos/checkoutform/auth/ecom/detail).
func (p *Provider) RetrieveCheckoutForm(ctx context.Context, token, conversationID string) (*core.CheckoutFormRetrieveResponse, error) {
	resp, err := p.sendRequest(ctx, "/payment/iyzipos/checkoutform/auth/ecom/detail", apiTokenDetailRequest{
		Locale:         p.locale(),
		ConversationID: conversationID,
		Token:          token,
	}, requestOptions{})
	if err != nil {
		out := core.ApplyFailure("iyzico", err, "Retrieve checkout form failed", &core.CheckoutFormRetrieveResponse{})
		out.Code = p.ErrorCodeFor(out.Status, out.ErrorCode)
		return out, nil
	}

	var body apiCheckoutFormRetrieveResponse
	decodeBody(resp, &body)
	out := &core.CheckoutFormRetrieveResponse{
		// Status reflects the payment itself, not just the API call.
		Status:                       mapPaymentStatus(body.Status, body.PaymentStatus),
		PaymentID:                    body.PaymentID,
		PaymentStatus:                body.PaymentStatus,
		Price:                        body.Price,
		PaidPrice:                    body.PaidPrice,
		Currency:                     body.Currency,
		BasketID:                     body.BasketID,
		Installment:                  body.Installment,
		BinNumber:                    body.BinNumber,
		LastFourDigits:               body.LastFourDigits,
		CardType:                     body.CardType,
		CardAssociation:              body.CardAssociation,
		CardFamily:                   body.CardFamily,
		CardToken:                    body.CardToken,
		CardUserKey:                  body.CardUserKey,
		FraudStatus:                  body.FraudStatus,
		MerchantCommissionRate:       body.MerchantCommissionRate,
		MerchantCommissionRateAmount: body.MerchantCommissionRateAmount,
		IyziCommissionRateAmount:     body.IyziCommissionRateAmount,
		IyziCommissionFee:            body.IyziCommissionFee,
		PaymentTransactionID:         body.PaymentTransactionID,
		ConversationID:               body.ConversationID,
		ErrorCode:                    body.ErrorCode,
		ErrorMessage:                 body.ErrorMessage,
		RawResponse:                  resp.Data,
	}
	out.Code = p.ErrorCodeFor(out.Status, out.ErrorCode)
	return out, nil
}

func checkoutFormPaymentRequest(req *core.CheckoutFormRequest) *core.PaymentRequest {
	return &core.PaymentRequest{
		Price:           req.Price,
		PaidPrice:       req.PaidPrice,
		Currency:        req.Currency,
		BasketID:        req.BasketID,
		Buyer:           req.Buyer,
		ShippingAddress: req.ShippingAddress,
		BillingAddress:  req.BillingAddress,
		BasketItems:     req.BasketItems,
		CallbackURL:     req.CallbackURL,
		ConversationID:  req.ConversationID,
	}
}
