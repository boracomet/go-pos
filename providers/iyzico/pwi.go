package iyzico

import (
	"context"

	"github.com/boracomet/go-pos/core"
)

// InitPWIPayment starts a protected bank transfer (Payment With IBAN,
// /payment/iyzipos/item/initialize). The customer sees an IBAN and the payment
// details; the funds reach the merchant once the transfer is approved.
func (p *Provider) InitPWIPayment(ctx context.Context, req *core.PWIPaymentRequest) (*core.PWIPaymentInitResponse, error) {
	if req == nil {
		return p.failPWIInit(missingRequest()), nil
	}
	if err := p.ValidatePayment(pwiPaymentRequest(req), orderRules); err != nil {
		return p.failPWIInit(err), nil
	}

	resp, err := p.sendRequest(ctx, "/payment/iyzipos/item/initialize", apiPWIRequest{
		Locale:          p.locale(),
		ConversationID:  req.ConversationID,
		Price:           req.Price,
		PaidPrice:       req.PaidPrice,
		Currency:        req.Currency,
		BasketID:        req.BasketID,
		PaymentGroup:    "PRODUCT",
		CallbackURL:     req.CallbackURL,
		Buyer:           mapBuyer(req.Buyer),
		ShippingAddress: mapAddress(req.ShippingAddress),
		BillingAddress:  mapAddress(req.BillingAddress),
		BasketItems:     mapBasketItems(req.BasketItems),
	}, requestOptions{})
	if err != nil {
		return p.failPWIInit(err), nil
	}

	var body apiPWIInitResponse
	decodeBody(resp, &body)
	out := &core.PWIPaymentInitResponse{
		Status:          mapStatus(body.Status),
		HTMLContent:     body.HTMLContent,
		Token:           body.Token,
		TokenExpireTime: body.TokenExpireTime,
		PaymentPageURL:  body.PaymentPageURL,
		ConversationID:  body.ConversationID,
		ErrorCode:       body.ErrorCode,
		ErrorMessage:    body.ErrorMessage,
		RawResponse:     resp.Data,
	}
	out.Code = p.ErrorCodeFor(out.Status, out.ErrorCode)
	return out, nil
}

func (p *Provider) failPWIInit(err error) *core.PWIPaymentInitResponse {
	out := core.ApplyFailure("iyzico", err, "PWI payment initialization failed", &core.PWIPaymentInitResponse{})
	out.Code = p.ErrorCodeFor(out.Status, out.ErrorCode)
	return out
}

// RetrievePWIPayment queries a protected bank transfer
// (/payment/iyzipos/item/detail): WAITING stays pending, SUCCESS succeeds.
func (p *Provider) RetrievePWIPayment(ctx context.Context, token, conversationID string) (*core.PWIPaymentRetrieveResponse, error) {
	resp, err := p.sendRequest(ctx, "/payment/iyzipos/item/detail", apiTokenDetailRequest{
		Locale:         p.locale(),
		ConversationID: conversationID,
		Token:          token,
	}, requestOptions{})
	if err != nil {
		out := core.ApplyFailure("iyzico", err, "PWI payment retrieve failed", &core.PWIPaymentRetrieveResponse{})
		out.Code = p.ErrorCodeFor(out.Status, out.ErrorCode)
		return out, nil
	}

	var body apiPWIRetrieveResponse
	decodeBody(resp, &body)
	out := &core.PWIPaymentRetrieveResponse{
		Status:                       mapPaymentStatus(body.Status, body.PaymentStatus),
		Token:                        body.Token,
		CallbackURL:                  body.CallbackURL,
		PaymentStatus:                body.PaymentStatus,
		PaymentID:                    body.PaymentID,
		Price:                        body.Price,
		PaidPrice:                    body.PaidPrice,
		Currency:                     body.Currency,
		BasketID:                     body.BasketID,
		MerchantCommissionRate:       body.MerchantCommissionRate,
		MerchantCommissionRateAmount: body.MerchantCommissionRateAmount,
		IyziCommissionRateAmount:     body.IyziCommissionRateAmount,
		IyziCommissionFee:            body.IyziCommissionFee,
		IBAN:                         body.IBAN,
		BankName:                     body.BankName,
		BuyerName:                    body.BuyerName,
		BuyerSurname:                 body.BuyerSurname,
		BuyerEmail:                   body.BuyerEmail,
		ConversationID:               body.ConversationID,
		ErrorCode:                    body.ErrorCode,
		ErrorMessage:                 body.ErrorMessage,
		RawResponse:                  resp.Data,
	}
	out.Code = p.ErrorCodeFor(out.Status, out.ErrorCode)
	return out, nil
}

func pwiPaymentRequest(req *core.PWIPaymentRequest) *core.PaymentRequest {
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
