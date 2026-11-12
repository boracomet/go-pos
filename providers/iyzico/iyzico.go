// Package iyzico implements the iyzico payment gateway: direct and 3D Secure
// payments, pre-authorization, refunds, cancellations, card storage, BIN and
// installment queries, hosted checkout forms, protected bank transfers (PWI)
// and subscriptions.
package iyzico

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/boracomet/go-pos/core"
)

// API hosts. iyzico requires an explicit BaseURL; pick one of these.
const (
	SandboxURL    = "https://sandbox-api.iyzipay.com"
	ProductionURL = "https://api.iyzipay.com"
)

const requestTimeout = 30 * time.Second

// Config holds the iyzico credentials and the shared provider settings.
type Config struct {
	core.ProviderConfig
	APIKey    string
	SecretKey string
}

// Provider calls the iyzico API.
type Provider struct {
	core.BaseProvider
	config Config
	client *core.HTTPClient
}

var (
	_ core.Provider             = (*Provider)(nil)
	_ core.CheckoutFormProvider = (*Provider)(nil)
	_ core.PWIProvider          = (*Provider)(nil)
	_ core.SubscriptionProvider = (*Provider)(nil)
)

// orderRules are the fields iyzico rejects a request without.
var orderRules = core.PaymentValidationRules{
	BasketRequired:     true,
	BasketMatchesPrice: true,
	Required: []string{
		"buyer.id",
		"buyer.name",
		"buyer.surname",
		"buyer.email",
		"buyer.identityNumber",
		"buyer.registrationAddress",
		"buyer.city",
		"buyer.country",
		"buyer.ip",
		"billingAddress.contactName",
		"billingAddress.city",
		"billingAddress.country",
		"billingAddress.address",
	},
}

var cardPaymentRules = core.PaymentValidationRules{
	Card:               true,
	StoredCard:         true,
	SaveCard:           true,
	BasketRequired:     orderRules.BasketRequired,
	BasketMatchesPrice: orderRules.BasketMatchesPrice,
	Required:           orderRules.Required,
}

// New builds an iyzico provider. APIKey, SecretKey and BaseURL are required.
func New(cfg Config) (*Provider, error) {
	var missing []string
	if cfg.APIKey == "" {
		missing = append(missing, "apiKey")
	}
	if cfg.SecretKey == "" {
		missing = append(missing, "secretKey")
	}
	if len(missing) > 0 {
		return nil, core.NewConfigurationError("iyzico configuration is missing: "+strings.Join(missing, ", "), "iyzico")
	}
	if cfg.BaseURL == "" {
		return nil, core.NewConfigurationError("iyzico baseUrl is required", "iyzico")
	}

	base := core.NewBaseProvider("iyzico", cfg.ProviderConfig)
	base.ErrorCodes = ErrorCodes
	return &Provider{
		BaseProvider: base,
		config:       cfg,
		client:       base.NewHTTPClient(requestTimeout, nil),
	}, nil
}

// requestOptions selects the HTTP method and whether the call may be retried.
// Only read-only queries are retryable.
type requestOptions struct {
	Method    string
	Retryable bool
}

// sendRequest signs and sends one API call. The signature covers the random
// key, the URI path and the exact JSON body sent ("{}" for bodiless requests).
func (p *Provider) sendRequest(ctx context.Context, endpoint string, data any, opts requestOptions) (*core.Response, error) {
	method := opts.Method
	if method == "" {
		method = http.MethodPost
	}
	requestBody, err := marshalBody(data)
	if err != nil {
		return nil, err
	}
	headers, err := CreateHeaders(p.config.APIKey, p.config.SecretKey, endpoint, requestBody, "")
	if err != nil {
		return nil, err
	}
	headers["Content-Type"] = "application/json"

	cfg := core.RequestConfig{
		Method:    method,
		URL:       endpoint,
		Headers:   headers,
		Retryable: opts.Retryable,
	}
	if method != http.MethodGet {
		cfg.Data = requestBody
	}
	return p.client.Request(ctx, cfg)
}

// marshalBody serializes a request body without HTML escaping, so the bytes
// signed are the bytes sent.
func marshalBody(data any) (string, error) {
	if data == nil {
		return "{}", nil
	}
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(data); err != nil {
		return "", err
	}
	return strings.TrimSuffix(buf.String(), "\n"), nil
}

// decodeBody fills out from the response body. A body that is not JSON leaves
// the zero value, whose empty status maps to PENDING.
func decodeBody(resp *core.Response, out any) {
	if resp == nil {
		return
	}
	_ = json.Unmarshal([]byte(resp.Text), out)
}

func missingRequest() error {
	return core.NewValidationError("request is required", "iyzico", []core.ValidationIssue{
		{Path: "request", Message: "is required"},
	})
}

func mapStatus(apiStatus string) core.PaymentStatus {
	switch apiStatus {
	case "success":
		return core.StatusSuccess
	case "failure":
		return core.StatusFailure
	default:
		return core.StatusPending
	}
}

// mapPaymentStatus reflects the payment itself, not just the API call:
// INIT_THREEDS, CALLBACK_THREEDS, WAITING and BKM_POS_SELECTED stay pending.
func mapPaymentStatus(apiStatus, paymentStatus string) core.PaymentStatus {
	if apiStatus != "success" {
		return core.StatusFailure
	}
	switch strings.ToUpper(paymentStatus) {
	case "SUCCESS":
		return core.StatusSuccess
	case "FAILURE":
		return core.StatusFailure
	default:
		return core.StatusPending
	}
}

func (p *Provider) locale() string { return p.Config.Locale }

// mapPaymentCard sends card data, or a stored card's tokens. With saveCard,
// iyzico registers the card and returns its tokens.
func (p *Provider) mapPaymentCard(request *core.PaymentRequest) (apiPaymentCard, error) {
	if request != nil && request.StoredCard != nil {
		return apiPaymentCard{
			CardUserKey: request.StoredCard.CustomerToken,
			CardToken:   request.StoredCard.CardToken,
		}, nil
	}
	card, err := p.CardOf(request)
	if err != nil {
		return apiPaymentCard{}, err
	}
	save := (request.SaveCard != nil && request.SaveCard.Enabled) ||
		(card.RegisterCard != nil && *card.RegisterCard)
	register := 0
	if save {
		register = 1
	}
	mapped := apiPaymentCard{
		CardHolderName: card.CardHolderName,
		CardNumber:     card.CardNumber,
		ExpireMonth:    card.ExpireMonth,
		ExpireYear:     card.ExpireYear,
		CVC:            card.CVC,
		RegisterCard:   &register,
	}
	if save && request.SaveCard != nil {
		mapped.CardAlias = request.SaveCard.Alias
		mapped.CardUserKey = request.SaveCard.CustomerToken
	}
	return mapped, nil
}

// savedCardOf returns the tokens of a card saved during a payment.
func savedCardOf(cardUserKey, cardToken string) *core.SavedCardTokens {
	if cardUserKey == "" {
		return nil
	}
	return &core.SavedCardTokens{CustomerToken: cardUserKey, CardToken: cardToken}
}

func mapBuyer(buyer core.Buyer) apiBuyer {
	return apiBuyer{
		ID:                  buyer.ID,
		Name:                buyer.Name,
		Surname:             buyer.Surname,
		GSMNumber:           buyer.GSMNumber,
		Email:               buyer.Email,
		IdentityNumber:      buyer.IdentityNumber,
		RegistrationAddress: buyer.RegistrationAddress,
		IP:                  buyer.IP,
		City:                buyer.City,
		Country:             buyer.Country,
		ZipCode:             buyer.ZipCode,
	}
}

func mapAddress(address core.Address) apiAddress {
	return apiAddress{
		ContactName: address.ContactName,
		City:        address.City,
		Country:     address.Country,
		Address:     address.Address,
		ZipCode:     address.ZipCode,
	}
}

func mapBasketItems(items []core.BasketItem) []apiBasketItem {
	out := make([]apiBasketItem, 0, len(items))
	for _, item := range items {
		out = append(out, apiBasketItem{
			ID:        item.ID,
			Name:      item.Name,
			Category1: item.Category1,
			Category2: item.Category2,
			ItemType:  item.ItemType,
			Price:     item.Price,
		})
	}
	return out
}

func (p *Provider) mapToIyzicoRequest(request *core.PaymentRequest, card apiPaymentCard) apiPaymentRequest {
	return apiPaymentRequest{
		Locale:          p.locale(),
		ConversationID:  request.ConversationID,
		Price:           request.Price,
		PaidPrice:       request.PaidPrice,
		Currency:        request.Currency,
		Installment:     1,
		BasketID:        request.BasketID,
		PaymentChannel:  "WEB",
		PaymentGroup:    "PRODUCT",
		PaymentCard:     card,
		Buyer:           mapBuyer(request.Buyer),
		ShippingAddress: mapAddress(request.ShippingAddress),
		BillingAddress:  mapAddress(request.BillingAddress),
		BasketItems:     mapBasketItems(request.BasketItems),
	}
}

// CreatePayment charges a card without 3D Secure (/payment/auth).
func (p *Provider) CreatePayment(ctx context.Context, req *core.PaymentRequest) (*core.PaymentResponse, error) {
	return p.cardPayment(ctx, "/payment/auth", req, "Payment failed")
}

// Authorize blocks the amount without charging it (/payment/preauth).
func (p *Provider) Authorize(ctx context.Context, req *core.PaymentRequest) (*core.PaymentResponse, error) {
	return p.cardPayment(ctx, "/payment/preauth", req, "Pre-authorization failed")
}

func (p *Provider) cardPayment(ctx context.Context, path string, req *core.PaymentRequest, fallback string) (*core.PaymentResponse, error) {
	if err := p.ValidatePayment(req, cardPaymentRules); err != nil {
		return p.failPayment(err, fallback), nil
	}
	card, err := p.mapPaymentCard(req)
	if err != nil {
		return p.failPayment(err, fallback), nil
	}
	resp, err := p.sendRequest(ctx, path, p.mapToIyzicoRequest(req, card), requestOptions{})
	if err != nil {
		return p.failPayment(err, fallback), nil
	}

	var body apiPaymentResponse
	decodeBody(resp, &body)
	out := &core.PaymentResponse{
		Status:         mapStatus(body.Status),
		PaymentID:      body.PaymentID,
		ConversationID: body.ConversationID,
		ErrorCode:      body.ErrorCode,
		ErrorMessage:   body.ErrorMessage,
		ErrorGroup:     body.ErrorGroup,
		StoredCard:     savedCardOf(body.CardUserKey, body.CardToken),
		RawResponse:    resp.Data,
	}
	out.Code = p.ErrorCodeFor(out.Status, out.ErrorCode)
	return out, nil
}

func (p *Provider) failPayment(err error, fallback string) *core.PaymentResponse {
	out := core.ApplyFailure("iyzico", err, fallback, &core.PaymentResponse{})
	out.Code = p.ErrorCodeFor(out.Status, out.ErrorCode)
	return out
}

// InitThreeDSPayment starts a 3D Secure payment (/payment/3dsecure/initialize).
func (p *Provider) InitThreeDSPayment(ctx context.Context, req *core.ThreeDSPaymentRequest) (*core.ThreeDSInitResponse, error) {
	return p.threeDSInit(ctx, "/payment/3dsecure/initialize", req, "3DS initialization failed")
}

// InitThreeDSAuthorize starts a 3D Secure pre-authorization
// (/payment/3dsecure/initialize/preauth). Complete it with CompleteThreeDSPayment.
func (p *Provider) InitThreeDSAuthorize(ctx context.Context, req *core.ThreeDSPaymentRequest) (*core.ThreeDSInitResponse, error) {
	return p.threeDSInit(ctx, "/payment/3dsecure/initialize/preauth", req, "3DS pre-authorization failed")
}

func (p *Provider) threeDSInit(ctx context.Context, path string, req *core.ThreeDSPaymentRequest, fallback string) (*core.ThreeDSInitResponse, error) {
	if req == nil {
		return p.failThreeDSInit(missingRequest(), fallback), nil
	}
	if err := p.ValidatePayment(&req.PaymentRequest, cardPaymentRules); err != nil {
		return p.failThreeDSInit(err, fallback), nil
	}
	card, err := p.mapPaymentCard(&req.PaymentRequest)
	if err != nil {
		return p.failThreeDSInit(err, fallback), nil
	}
	body := p.mapToIyzicoRequest(&req.PaymentRequest, card)
	body.CallbackURL = req.CallbackURL

	resp, err := p.sendRequest(ctx, path, body, requestOptions{})
	if err != nil {
		return p.failThreeDSInit(err, fallback), nil
	}

	var parsed apiThreeDSInitResponse
	decodeBody(resp, &parsed)

	// threeDSHtmlContent arrives Base64 encoded; keep the raw value if it is not.
	htmlContent := parsed.ThreeDSHTMLContent
	if htmlContent != "" {
		if decoded, err := core.FromBase64(htmlContent); err == nil {
			htmlContent = decoded
		}
	}

	out := &core.ThreeDSInitResponse{
		Status:             mapStatus(parsed.Status),
		ThreeDSHTMLContent: htmlContent,
		PaymentID:          parsed.PaymentID,
		ConversationID:     parsed.ConversationID,
		ErrorCode:          parsed.ErrorCode,
		ErrorMessage:       parsed.ErrorMessage,
		RawResponse:        resp.Data,
	}
	out.Code = p.ErrorCodeFor(out.Status, out.ErrorCode)
	return out, nil
}

func (p *Provider) failThreeDSInit(err error, fallback string) *core.ThreeDSInitResponse {
	out := core.ApplyFailure("iyzico", err, fallback, &core.ThreeDSInitResponse{})
	out.Code = p.ErrorCodeFor(out.Status, out.ErrorCode)
	return out
}

// CompleteThreeDSPayment completes a 3D Secure payment.
//
// The callback POST is not signed, so its values are untrusted: only the
// response of /payment/3dsecure/auth decides the result. PaymentID and
// ConversationID come from iyzico's response of a successful payment only, so a
// forged callback cannot name an existing order.
func (p *Provider) CompleteThreeDSPayment(ctx context.Context, callbackData any) (*core.PaymentResponse, error) {
	callback := parseCallback(callbackData)

	// iyzico posts status=success and mdStatus=1 only when 3D authentication
	// succeeded. Anything else must not be authorized.
	if callback.paymentID == "" || callback.status != "success" || callback.mdStatus != "1" {
		code := "INVALID_CALLBACK"
		if callback.hasMDStatus {
			code = "MD_STATUS_" + callback.mdStatus
		}
		out := &core.PaymentResponse{
			Status:       core.StatusFailure,
			ErrorCode:    code,
			ErrorMessage: "3D Secure authentication failed",
			RawResponse:  callbackData,
		}
		out.Code = p.ErrorCodeFor(out.Status, out.ErrorCode)
		return out, nil
	}

	resp, err := p.sendRequest(ctx, "/payment/3dsecure/auth", apiThreeDSAuthRequest{
		Locale:           p.locale(),
		ConversationID:   callback.conversationID,
		PaymentID:        callback.paymentID,
		ConversationData: callback.conversationData,
	}, requestOptions{})
	if err != nil {
		return p.failPayment(err, "3DS completion failed"), nil
	}

	var body apiPaymentResponse
	decodeBody(resp, &body)
	status := mapStatus(body.Status)
	succeeded := status == core.StatusSuccess

	out := &core.PaymentResponse{
		Status:       status,
		ErrorCode:    body.ErrorCode,
		ErrorMessage: body.ErrorMessage,
		ErrorGroup:   body.ErrorGroup,
		StoredCard:   savedCardOf(body.CardUserKey, body.CardToken),
		RawResponse:  resp.Data,
	}
	if succeeded {
		out.PaymentID = body.PaymentID
		out.ConversationID = body.ConversationID
	}
	out.Code = p.ErrorCodeFor(out.Status, out.ErrorCode)
	return out, nil
}

// Refund refunds an amount of the whole payment (/v2/payment/refund), or one
// basket item when PaymentTransactionID is set (/payment/refund).
func (p *Provider) Refund(ctx context.Context, req *core.RefundRequest) (*core.RefundResponse, error) {
	if req == nil {
		return p.failRefund(missingRequest(), "Refund failed"), nil
	}
	if err := p.ValidateRefund(req); err != nil {
		return p.failRefund(err, "Refund failed"), nil
	}

	body := apiRefundRequest{
		Locale:         p.locale(),
		ConversationID: req.ConversationID,
		Price:          req.Price,
		Currency:       req.Currency,
		IP:             req.IP,
	}
	endpoint := "/v2/payment/refund"
	if req.PaymentTransactionID != "" {
		body.PaymentTransactionID = req.PaymentTransactionID
		endpoint = "/payment/refund"
	} else {
		body.PaymentID = req.PaymentID
	}

	resp, err := p.sendRequest(ctx, endpoint, body, requestOptions{})
	if err != nil {
		return p.failRefund(err, "Refund failed"), nil
	}

	var parsed apiRefundResponse
	decodeBody(resp, &parsed)
	refundID := parsed.PaymentTransactionID
	if refundID == "" {
		refundID = parsed.PaymentID
	}
	out := &core.RefundResponse{
		Status:         mapStatus(parsed.Status),
		RefundID:       refundID,
		ConversationID: parsed.ConversationID,
		ErrorCode:      parsed.ErrorCode,
		ErrorMessage:   parsed.ErrorMessage,
		RawResponse:    resp.Data,
	}
	out.Code = p.ErrorCodeFor(out.Status, out.ErrorCode)
	return out, nil
}

func (p *Provider) failRefund(err error, fallback string) *core.RefundResponse {
	out := core.ApplyFailure("iyzico", err, fallback, &core.RefundResponse{})
	out.Code = p.ErrorCodeFor(out.Status, out.ErrorCode)
	return out
}

// Cancel cancels a payment (/payment/cancel).
func (p *Provider) Cancel(ctx context.Context, req *core.CancelRequest) (*core.CancelResponse, error) {
	if req == nil {
		return p.failCancel(missingRequest(), "Cancel failed"), nil
	}
	resp, err := p.sendRequest(ctx, "/payment/cancel", apiCancelRequest{
		Locale:         p.locale(),
		ConversationID: req.ConversationID,
		PaymentID:      req.PaymentID,
		IP:             req.IP,
	}, requestOptions{})
	if err != nil {
		return p.failCancel(err, "Cancel failed"), nil
	}

	var body apiCancelResponse
	decodeBody(resp, &body)
	out := &core.CancelResponse{
		Status:         mapStatus(body.Status),
		ConversationID: body.ConversationID,
		ErrorCode:      body.ErrorCode,
		ErrorMessage:   body.ErrorMessage,
		RawResponse:    resp.Data,
	}
	out.Code = p.ErrorCodeFor(out.Status, out.ErrorCode)
	return out, nil
}

func (p *Provider) failCancel(err error, fallback string) *core.CancelResponse {
	out := core.ApplyFailure("iyzico", err, fallback, &core.CancelResponse{})
	out.Code = p.ErrorCodeFor(out.Status, out.ErrorCode)
	return out
}

// Capture charges a pre-authorization (/payment/postauth). Amount becomes the
// charged paidPrice; a lower amount is a partial capture.
func (p *Provider) Capture(ctx context.Context, req *core.CaptureRequest) (*core.PaymentResponse, error) {
	if req == nil {
		return p.failPayment(missingRequest(), "Capture failed"), nil
	}
	if err := p.ValidateCapture(req); err != nil {
		return p.failPayment(err, "Capture failed"), nil
	}
	currency := req.Currency
	if currency == "" {
		currency = "TRY"
	}
	resp, err := p.sendRequest(ctx, "/payment/postauth", apiCaptureRequest{
		Locale:         p.locale(),
		ConversationID: req.ConversationID,
		PaymentID:      req.PaymentID,
		IP:             req.IP,
		PaidPrice:      req.Amount,
		Currency:       currency,
	}, requestOptions{})
	if err != nil {
		return p.failPayment(err, "Capture failed"), nil
	}

	var body apiPaymentResponse
	decodeBody(resp, &body)
	paymentID := body.PaymentID
	if paymentID == "" {
		paymentID = req.PaymentID
	}
	out := &core.PaymentResponse{
		Status:         mapStatus(body.Status),
		PaymentID:      paymentID,
		ConversationID: body.ConversationID,
		ErrorCode:      body.ErrorCode,
		ErrorMessage:   body.ErrorMessage,
		ErrorGroup:     body.ErrorGroup,
		RawResponse:    resp.Data,
	}
	out.Code = p.ErrorCodeFor(out.Status, out.ErrorCode)
	return out, nil
}

// VoidAuthorization releases a pre-authorization; iyzico voids it with /payment/cancel.
func (p *Provider) VoidAuthorization(ctx context.Context, req *core.VoidAuthorizationRequest) (*core.CancelResponse, error) {
	if req == nil {
		return p.failCancel(missingRequest(), "Cancel failed"), nil
	}
	return p.Cancel(ctx, &core.CancelRequest{
		PaymentID:      req.PaymentID,
		IP:             req.IP,
		ConversationID: req.ConversationID,
	})
}

// GetPayment queries a payment (/payment/detail).
func (p *Provider) GetPayment(ctx context.Context, paymentID string) (*core.PaymentResponse, error) {
	resp, err := p.sendRequest(ctx, "/payment/detail", apiPaymentDetailRequest{
		Locale:    p.locale(),
		PaymentID: paymentID,
	}, requestOptions{Retryable: true})
	if err != nil {
		return p.failPayment(err, "Get payment failed"), nil
	}

	var body apiPaymentResponse
	decodeBody(resp, &body)
	out := &core.PaymentResponse{
		Status:         mapStatus(body.Status),
		PaymentID:      body.PaymentID,
		ConversationID: body.ConversationID,
		ErrorCode:      body.ErrorCode,
		ErrorMessage:   body.ErrorMessage,
		ErrorGroup:     body.ErrorGroup,
		RawResponse:    resp.Data,
	}
	out.Code = p.ErrorCodeFor(out.Status, out.ErrorCode)
	return out, nil
}

// SaveCard stores a card without charging it (/cardstorage/card). Omit
// CustomerToken to create a new customer (cardUserKey).
func (p *Provider) SaveCard(ctx context.Context, req *core.SaveCardRequest) (*core.SaveCardResponse, error) {
	if req == nil {
		return p.failSaveCard(missingRequest()), nil
	}
	resp, err := p.sendRequest(ctx, "/cardstorage/card", apiSaveCardRequest{
		Locale:         p.locale(),
		ConversationID: req.ConversationID,
		ExternalID:     req.ExternalID,
		Email:          req.Email,
		CardUserKey:    req.CustomerToken,
		Card: apiSaveCardDetails{
			CardAlias:      req.Alias,
			CardHolderName: req.Card.CardHolderName,
			CardNumber:     req.Card.CardNumber,
			ExpireMonth:    req.Card.ExpireMonth,
			ExpireYear:     req.Card.ExpireYear,
		},
	}, requestOptions{})
	if err != nil {
		return p.failSaveCard(err), nil
	}

	var body apiCardResponse
	decodeBody(resp, &body)
	status := mapStatus(body.Status)
	out := &core.SaveCardResponse{
		Status:        status,
		CustomerToken: body.CardUserKey,
		ErrorCode:     body.ErrorCode,
		ErrorMessage:  body.ErrorMessage,
		RawResponse:   resp.Data,
	}
	if status == core.StatusSuccess {
		out.Card = storedCardOf(body.apiStoredCardDetails)
	}
	out.Code = p.ErrorCodeFor(out.Status, out.ErrorCode)
	return out, nil
}

func (p *Provider) failSaveCard(err error) *core.SaveCardResponse {
	out := core.ApplyFailure("iyzico", err, "Saving the card failed", &core.SaveCardResponse{})
	out.Code = p.ErrorCodeFor(out.Status, out.ErrorCode)
	return out
}

// ListCards returns a customer's saved cards (/cardstorage/cards).
func (p *Provider) ListCards(ctx context.Context, req *core.ListCardsRequest) (*core.ListCardsResponse, error) {
	if req == nil {
		return p.failListCards(missingRequest()), nil
	}
	resp, err := p.sendRequest(ctx, "/cardstorage/cards", apiListCardsRequest{
		Locale:      p.locale(),
		CardUserKey: req.CustomerToken,
	}, requestOptions{Retryable: true})
	if err != nil {
		return p.failListCards(err), nil
	}

	var body apiCardListResponse
	decodeBody(resp, &body)
	customerToken := body.CardUserKey
	if customerToken == "" {
		customerToken = req.CustomerToken
	}
	cards := make([]core.StoredCard, 0, len(body.CardDetails))
	for _, details := range body.CardDetails {
		cards = append(cards, *storedCardOf(details))
	}
	out := &core.ListCardsResponse{
		Status:        mapStatus(body.Status),
		CustomerToken: customerToken,
		Cards:         cards,
		ErrorCode:     body.ErrorCode,
		ErrorMessage:  body.ErrorMessage,
		RawResponse:   resp.Data,
	}
	out.Code = p.ErrorCodeFor(out.Status, out.ErrorCode)
	return out, nil
}

func (p *Provider) failListCards(err error) *core.ListCardsResponse {
	out := core.ApplyFailure("iyzico", err, "Listing cards failed", &core.ListCardsResponse{})
	out.Cards = []core.StoredCard{}
	out.Code = p.ErrorCodeFor(out.Status, out.ErrorCode)
	return out
}

// DeleteCard removes a saved card (DELETE /cardstorage/card).
func (p *Provider) DeleteCard(ctx context.Context, req *core.DeleteCardRequest) (*core.DeleteCardResponse, error) {
	if req == nil {
		return p.failDeleteCard(missingRequest()), nil
	}
	resp, err := p.sendRequest(ctx, "/cardstorage/card", apiDeleteCardRequest{
		Locale:         p.locale(),
		ConversationID: req.ConversationID,
		CardUserKey:    req.CustomerToken,
		CardToken:      req.CardToken,
	}, requestOptions{Method: http.MethodDelete})
	if err != nil {
		return p.failDeleteCard(err), nil
	}

	var body apiResponse
	decodeBody(resp, &body)
	out := &core.DeleteCardResponse{
		Status:       mapStatus(body.Status),
		ErrorCode:    body.ErrorCode,
		ErrorMessage: body.ErrorMessage,
		RawResponse:  resp.Data,
	}
	out.Code = p.ErrorCodeFor(out.Status, out.ErrorCode)
	return out, nil
}

func (p *Provider) failDeleteCard(err error) *core.DeleteCardResponse {
	out := core.ApplyFailure("iyzico", err, "Deleting the card failed", &core.DeleteCardResponse{})
	out.Code = p.ErrorCodeFor(out.Status, out.ErrorCode)
	return out
}

func storedCardOf(details apiStoredCardDetails) *core.StoredCard {
	return &core.StoredCard{
		CardToken:       details.CardToken,
		Alias:           details.CardAlias,
		BinNumber:       details.BinNumber,
		LastFourDigits:  details.LastFourDigits,
		CardType:        details.CardType,
		CardAssociation: details.CardAssociation,
		CardFamily:      details.CardFamily,
		BankName:        details.CardBankName,
	}
}

// BinCheck looks up a BIN (/payment/bin/check). It returns an error when the
// lookup fails.
func (p *Provider) BinCheck(ctx context.Context, binNumber string) (*core.BinCheckResponse, error) {
	conversationID, err := core.RandomHex(8)
	if err != nil {
		return nil, err
	}
	resp, err := p.sendRequest(ctx, "/payment/bin/check", apiBinCheckRequest{
		Locale:         p.locale(),
		ConversationID: conversationID,
		BinNumber:      binNumber,
	}, requestOptions{Retryable: true})
	if err != nil {
		return nil, err
	}

	var body apiBinCheckResponse
	decodeBody(resp, &body)
	if body.Status != "success" {
		message := body.ErrorMessage
		if message == "" {
			message = "BIN check failed"
		}
		return nil, errors.New(message)
	}

	out := &core.BinCheckResponse{
		BinNumber:       body.BinNumber,
		CardType:        body.CardType,
		CardAssociation: body.CardAssociation,
		CardFamily:      body.CardFamily,
		BankName:        body.BankName,
		BankCode:        body.BankCode,
		Commercial:      body.Commercial == 1,
		RawResponse:     resp.Data,
	}
	if out.BinNumber == "" {
		out.BinNumber = binNumber
	}
	return out, nil
}

// InstallmentInfo queries installment options for a BIN and an amount
// (/payment/iyzipos/installment).
func (p *Provider) InstallmentInfo(ctx context.Context, req *core.InstallmentInfoRequest) (*core.InstallmentInfoResponse, error) {
	if req == nil {
		return p.failInstallmentInfo(missingRequest()), nil
	}
	resp, err := p.sendRequest(ctx, "/payment/iyzipos/installment", apiInstallmentInfoRequest{
		Locale:         p.locale(),
		ConversationID: req.ConversationID,
		BinNumber:      req.BinNumber,
		Price:          req.Price,
	}, requestOptions{})
	if err != nil {
		return p.failInstallmentInfo(err), nil
	}

	var body apiInstallmentInfoResponse
	decodeBody(resp, &body)
	out := &core.InstallmentInfoResponse{
		Status:             mapStatus(body.Status),
		InstallmentDetails: body.InstallmentDetails,
		ConversationID:     body.ConversationID,
		ErrorCode:          body.ErrorCode,
		ErrorMessage:       body.ErrorMessage,
		RawResponse:        resp.Data,
	}
	out.Code = p.ErrorCodeFor(out.Status, out.ErrorCode)
	return out, nil
}

func (p *Provider) failInstallmentInfo(err error) *core.InstallmentInfoResponse {
	out := core.ApplyFailure("iyzico", err, "Installment info request failed", &core.InstallmentInfoResponse{})
	out.Code = p.ErrorCodeFor(out.Status, out.ErrorCode)
	return out
}

// threeDSCallback holds the normalized, untrusted callback fields.
type threeDSCallback struct {
	status           string
	paymentID        string
	conversationID   string
	conversationData string
	mdStatus         string
	hasMDStatus      bool
}

func parseCallback(data any) threeDSCallback {
	fields := callbackFields(data)
	mdStatus, present := fields["mdStatus"]
	return threeDSCallback{
		status:           stringOf(fields["status"]),
		paymentID:        stringOf(fields["paymentId"]),
		conversationID:   stringOf(fields["conversationId"]),
		conversationData: stringOf(fields["conversationData"]),
		mdStatus:         stringOf(mdStatus),
		hasMDStatus:      present,
	}
}

// callbackFields accepts the callback as a struct, a form or a decoded JSON body.
func callbackFields(data any) map[string]any {
	switch d := data.(type) {
	case nil:
		return nil
	case map[string]any:
		return d
	case map[string]string:
		fields := make(map[string]any, len(d))
		for k, v := range d {
			fields[k] = v
		}
		return fields
	case url.Values:
		fields := make(map[string]any, len(d))
		for k, v := range d {
			if len(v) > 0 {
				fields[k] = v[0]
			}
		}
		return fields
	case ThreeDSCallbackData:
		fields := map[string]any{
			"status":           d.Status,
			"paymentId":        d.PaymentID,
			"conversationId":   d.ConversationID,
			"conversationData": d.ConversationData,
		}
		if d.MDStatus != nil {
			fields["mdStatus"] = d.MDStatus
		}
		return fields
	case *ThreeDSCallbackData:
		if d == nil {
			return nil
		}
		return callbackFields(*d)
	case string:
		return decodeCallbackJSON([]byte(d))
	case []byte:
		return decodeCallbackJSON(d)
	default:
		return nil
	}
}

func decodeCallbackJSON(raw []byte) map[string]any {
	var fields map[string]any
	if err := json.Unmarshal(raw, &fields); err != nil {
		return nil
	}
	return fields
}

// stringOf renders a callback value as the provider's form encoding does.
func stringOf(value any) string {
	switch v := value.(type) {
	case nil:
		return ""
	case string:
		return v
	case json.Number:
		return v.String()
	case float64:
		return strconv.FormatFloat(v, 'f', -1, 64)
	case bool:
		return strconv.FormatBool(v)
	case int:
		return strconv.Itoa(v)
	case int64:
		return strconv.FormatInt(v, 10)
	default:
		return ""
	}
}
