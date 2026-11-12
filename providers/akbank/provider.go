package akbank

import (
	"context"
	"strconv"
	"strings"
	"time"

	"github.com/boracomet/go-pos/core"
)

// Provider is the Akbank Sanal POS (JSON API).
//
//   - CreatePayment:          txnCode 1000 (non-3D sale)
//   - Authorize:              txnCode 1004 (pre-authorization)
//   - InitThreeDSPayment:     3D_PAY form to the securepay gateway (txnCode 3000);
//     Akbank authenticates and charges, then POSTs the signed result to callbackUrl
//   - InitThreeDSAuthorize:   the same form with txnCode 3004
//   - CompleteThreeDSPayment: verifies the callback signature and confirms the
//     outcome with an order history query
//   - Refund 1002, Cancel 1003, Capture 1005, GetPayment 1010
//
// PaymentID is the order id: the ConversationID passed in, or a generated one.
type Provider struct {
	core.BaseProvider
	cfg    Config
	client *core.HTTPClient
}

// New builds an Akbank provider. BaseURL is the API host; the 3D gateway follows
// TestMode unless Gateway3DURL is set.
func New(cfg Config) (*Provider, error) {
	var missing []string
	if cfg.MerchantSafeID == "" {
		missing = append(missing, "merchantSafeId")
	}
	if cfg.TerminalSafeID == "" {
		missing = append(missing, "terminalSafeId")
	}
	if cfg.SecretKey == "" {
		missing = append(missing, "secretKey")
	}
	if len(missing) > 0 {
		return nil, core.NewConfigurationError("Akbank configuration is missing: "+strings.Join(missing, ", "), "akbank")
	}
	if cfg.BaseURL == "" {
		return nil, core.NewConfigurationError("Akbank baseUrl is required", "akbank")
	}

	p := &Provider{BaseProvider: core.NewBaseProvider("akbank", cfg.ProviderConfig), cfg: cfg}
	p.ErrorCodes = core.ISO8583ErrorCodes
	p.client = p.NewHTTPClient(30*time.Second, map[string]string{"Content-Type": "application/json"})
	return p, nil
}

func (p *Provider) gateway3DURL() string {
	if p.cfg.Gateway3DURL != "" {
		return p.cfg.Gateway3DURL
	}
	if p.cfg.TestMode {
		return Gateway3DTest
	}
	return Gateway3DProduction
}

func (p *Provider) lang() string {
	if strings.HasPrefix(strings.ToLower(p.Config.Locale), "en") {
		return "EN"
	}
	return "TR"
}

// baseRequest is the envelope every request starts with, in send order.
func (p *Provider) baseRequest(txnCode string) ([]jsonField, error) {
	randomNumber, err := GenerateRandomNumber()
	if err != nil {
		return nil, err
	}
	fields := []jsonField{
		{Name: "version", Value: APIVersion},
		{Name: "txnCode", Value: txnCode},
		{Name: "requestDateTime", Value: FormatDateTime(time.Now())},
		{Name: "randomNumber", Value: randomNumber},
		{Name: "terminal", Value: []jsonField{
			{Name: "merchantSafeId", Value: p.cfg.MerchantSafeID},
			{Name: "terminalSafeId", Value: p.cfg.TerminalSafeID},
		}},
	}
	if p.cfg.SubMerchantID != "" {
		fields = append(fields, jsonField{Name: "subMerchant", Value: []jsonField{
			{Name: "subMerchantId", Value: p.cfg.SubMerchantID},
		}})
	}
	return fields, nil
}

// process POSTs /transaction/process with the auth-hash header
// (base64(HMAC-SHA512(body, secretKey))). Akbank answers 4xx with a JSON body for
// validation errors, so those statuses resolve instead of failing.
func (p *Provider) process(ctx context.Context, body []jsonField, retryable bool) (APIResponse, error) {
	json := buildJSON(body)
	hash, err := Sign(json, p.cfg.SecretKey)
	if err != nil {
		return nil, err
	}
	response, err := p.client.Post(ctx, EndpointProcess, json, core.RequestConfig{
		Headers: map[string]string{
			"Content-Type": "application/json",
			"auth-hash":    hash,
		},
		Retryable:      retryable,
		ValidateStatus: func(status int) bool { return status < 500 },
	})
	if err != nil {
		return nil, err
	}
	if data, ok := response.Data.(map[string]any); ok {
		return APIResponse(data), nil
	}
	return APIResponse{}, nil
}

// akbankErrorCodes are the provider's own raw codes, on top of the ISO 8583 host
// codes in ErrorCodes.
var akbankErrorCodes = map[string]core.PaymentErrorCode{
	"TERMINAL_MISMATCH": core.CodeInvalidHash,
}

// errorCodeFor prefers the bank's ISO 8583 host response code, the most specific
// reason, and falls back to the Akbank response code (VPS-xxxx).
func (p *Provider) errorCodeFor(status core.PaymentStatus, rawCode string, raw any) core.PaymentErrorCode {
	if status != core.StatusFailure && rawCode != core.NetworkErrorCode {
		return ""
	}
	if host := hostResponseCode(raw); host != "" {
		if code, ok := core.ISO8583ErrorCodes[host]; ok {
			return code
		}
	}
	return core.ResolveErrorCode(rawCode, akbankErrorCodes)
}

func hostResponseCode(raw any) string {
	switch data := raw.(type) {
	case APIResponse:
		if value, ok := data["hostResponseCode"].(string); ok {
			return value
		}
	case map[string]any:
		if value, ok := data["hostResponseCode"].(string); ok {
			return value
		}
	case map[string]string:
		return data["hostResponseCode"]
	}
	return ""
}

// errorOf returns the raw error code and message of an API answer.
func errorOf(data APIResponse) (errorCode, errorMessage string) {
	if value, ok := data["responseCode"]; ok && value != nil {
		errorCode = stringOf(data, "responseCode")
	} else if value, ok := data["code"]; ok && value != nil {
		errorCode = stringOf(data, "code")
	}
	return errorCode, firstNonEmpty(
		stringOf(data, "hostMessage"), stringOf(data, "responseMessage"), stringOf(data, "message"))
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if value != "" {
			return value
		}
	}
	return ""
}

func orderIDOf(conversationID string) string {
	if conversationID != "" {
		return conversationID
	}
	return core.GenerateOrderID("")
}

func paidPriceOf(request *core.PaymentRequest) string {
	if request.PaidPrice != "" {
		return request.PaidPrice
	}
	return request.Price
}

func (p *Provider) failPayment(err error, fallback, paymentID string) (*core.PaymentResponse, error) {
	out := core.ApplyFailure("akbank", err, fallback, &core.PaymentResponse{
		PaymentID: paymentID, ConversationID: paymentID,
	})
	out.Code = p.errorCodeFor(out.Status, out.ErrorCode, out.RawResponse)
	return out, nil
}

func (p *Provider) failInit(err error, fallback, paymentID string) (*core.ThreeDSInitResponse, error) {
	out := core.ApplyFailure("akbank", err, fallback, &core.ThreeDSInitResponse{
		PaymentID: paymentID, ConversationID: paymentID,
	})
	out.Code = p.errorCodeFor(out.Status, out.ErrorCode, out.RawResponse)
	return out, nil
}

func (p *Provider) failRefund(err error, fallback, conversationID string) (*core.RefundResponse, error) {
	out := core.ApplyFailure("akbank", err, fallback, &core.RefundResponse{ConversationID: conversationID})
	out.Code = p.errorCodeFor(out.Status, out.ErrorCode, out.RawResponse)
	return out, nil
}

func (p *Provider) failCancel(err error, fallback, conversationID string) (*core.CancelResponse, error) {
	out := core.ApplyFailure("akbank", err, fallback, &core.CancelResponse{ConversationID: conversationID})
	out.Code = p.errorCodeFor(out.Status, out.ErrorCode, out.RawResponse)
	return out, nil
}

func (p *Provider) result(status core.PaymentStatus, errorCode, errorMessage string, raw any) *core.PaymentResponse {
	out := &core.PaymentResponse{
		Status: status, ErrorCode: errorCode, ErrorMessage: errorMessage, RawResponse: raw,
	}
	out.Code = p.errorCodeFor(status, errorCode, raw)
	return out
}

// completeResult keeps the callback's order id on every result, including a
// rejected one, so the merchant can look the order up.
func (p *Provider) completeResult(status core.PaymentStatus, errorCode, errorMessage, orderID string, raw any) *core.PaymentResponse {
	out := p.result(status, errorCode, errorMessage, raw)
	out.PaymentID, out.ConversationID = orderID, orderID
	return out
}

// CreatePayment charges the card without 3D Secure (txnCode 1000).
func (p *Provider) CreatePayment(ctx context.Context, request *core.PaymentRequest) (*core.PaymentResponse, error) {
	return p.cardTransaction(ctx, TxnSale, request)
}

// Authorize blocks the amount without charging it (txnCode 1004).
func (p *Provider) Authorize(ctx context.Context, request *core.PaymentRequest) (*core.PaymentResponse, error) {
	return p.cardTransaction(ctx, TxnPreAuth, request)
}

func (p *Provider) cardTransaction(ctx context.Context, txnCode string, request *core.PaymentRequest) (*core.PaymentResponse, error) {
	conversationID := ""
	if request != nil {
		conversationID = request.ConversationID
	}
	orderID := orderIDOf(conversationID)
	fallback := "Payment failed"
	if txnCode == TxnPreAuth {
		fallback = "Pre-authorization failed"
	}

	if request == nil {
		return p.failPayment(core.NewValidationError("request is required", "akbank", nil), fallback, orderID)
	}
	if err := p.ValidatePayment(request, core.PaymentValidationRules{Card: true, Required: []string{"buyer.ip"}}); err != nil {
		return p.failPayment(err, fallback, orderID)
	}
	card, err := p.CardOf(request)
	if err != nil {
		return p.failPayment(err, fallback, orderID)
	}
	expiry, err := FormatExpiry(card.ExpireMonth, card.ExpireYear)
	if err != nil {
		return p.failPayment(err, fallback, orderID)
	}
	amount, err := FormatAmount(paidPriceOf(request))
	if err != nil {
		return p.failPayment(err, fallback, orderID)
	}
	currency, err := CurrencyCode(request.Currency)
	if err != nil {
		return p.failPayment(err, fallback, orderID)
	}
	body, err := p.baseRequest(txnCode)
	if err != nil {
		return p.failPayment(err, fallback, orderID)
	}
	body = append(body,
		jsonField{Name: "card", Value: []jsonField{
			{Name: "cardNumber", Value: card.CardNumber},
			{Name: "cvv2", Value: card.CVC},
			{Name: "expireDate", Value: expiry},
		}},
		jsonField{Name: "transaction", Value: []jsonField{
			{Name: "amount", Value: amount},
			{Name: "currencyCode", Value: currency},
			{Name: "motoInd", Value: 0},
			{Name: "installCount", Value: installCount(request.Installment)},
		}},
		jsonField{Name: "customer", Value: []jsonField{
			{Name: "ipAddress", Value: request.Buyer.IP},
		}},
		jsonField{Name: "order", Value: []jsonField{{Name: "orderId", Value: orderID}}},
	)

	data, err := p.process(ctx, body, false)
	if err != nil {
		return p.failPayment(err, fallback, orderID)
	}

	out := &core.PaymentResponse{PaymentID: orderID, ConversationID: orderID, RawResponse: data}
	if stringOf(data, "responseCode") == SuccessCode {
		out.Status = core.StatusSuccess
	} else {
		out.Status = core.StatusFailure
		out.ErrorCode, out.ErrorMessage = errorOf(data)
	}
	out.Code = p.errorCodeFor(out.Status, out.ErrorCode, data)
	return out, nil
}

// InitThreeDSPayment builds the auto-submitting 3D_PAY form (txnCode 3000). Card
// data is posted from the customer's browser directly to Akbank.
func (p *Provider) InitThreeDSPayment(ctx context.Context, request *core.ThreeDSPaymentRequest) (*core.ThreeDSInitResponse, error) {
	return p.threeDSForm(ctx, TxnSecureSale, request, InitOptions{})
}

// InitThreeDSAuthorize builds the 3D_PAY pre-authorization form (txnCode 3004).
// The bank posts the result to callbackUrl; CompleteThreeDSPayment verifies it as
// usual.
func (p *Provider) InitThreeDSAuthorize(ctx context.Context, request *core.ThreeDSPaymentRequest) (*core.ThreeDSInitResponse, error) {
	return p.threeDSForm(ctx, TxnSecurePreAuth, request, InitOptions{})
}

// InitThreeDSPaymentWithOptions builds the 3D form with an optional failUrl.
func (p *Provider) InitThreeDSPaymentWithOptions(ctx context.Context, request *core.ThreeDSPaymentRequest, options InitOptions) (*core.ThreeDSInitResponse, error) {
	return p.threeDSForm(ctx, TxnSecureSale, request, options)
}

// InitThreeDSAuthorizeWithOptions builds the 3D pre-authorization form with an
// optional failUrl.
func (p *Provider) InitThreeDSAuthorizeWithOptions(ctx context.Context, request *core.ThreeDSPaymentRequest, options InitOptions) (*core.ThreeDSInitResponse, error) {
	return p.threeDSForm(ctx, TxnSecurePreAuth, request, options)
}

func (p *Provider) threeDSForm(ctx context.Context, txnCode string, request *core.ThreeDSPaymentRequest, options InitOptions) (*core.ThreeDSInitResponse, error) {
	conversationID := ""
	if request != nil {
		conversationID = request.ConversationID
	}
	orderID := orderIDOf(conversationID)

	if request == nil {
		return p.failInit(core.NewValidationError("request is required", "akbank", nil), "3DS initialization failed", orderID)
	}
	if err := p.ValidatePayment(&request.PaymentRequest, core.PaymentValidationRules{Card: true, Required: []string{"buyer.ip"}}); err != nil {
		return p.failInit(err, "3DS initialization failed", orderID)
	}
	card, err := p.CardOf(&request.PaymentRequest)
	if err != nil {
		return p.failInit(err, "3DS initialization failed", orderID)
	}
	// The outer CallbackURL shadows the embedded one; both name the same field.
	callbackURL := request.CallbackURL
	if callbackURL == "" {
		callbackURL = request.PaymentRequest.CallbackURL
	}
	if callbackURL == "" {
		return p.failInit(core.NewValidationError("callbackUrl is required for 3D Secure payments", "akbank", nil),
			"3DS initialization failed", orderID)
	}
	amount, err := FormatAmount(paidPriceOf(&request.PaymentRequest))
	if err != nil {
		return p.failInit(err, "3DS initialization failed", orderID)
	}
	currency, err := CurrencyCode(request.Currency)
	if err != nil {
		return p.failInit(err, "3DS initialization failed", orderID)
	}
	expiry, err := FormatExpiry(card.ExpireMonth, card.ExpireYear)
	if err != nil {
		return p.failInit(err, "3DS initialization failed", orderID)
	}
	randomNumber, err := GenerateRandomNumber()
	if err != nil {
		return p.failInit(err, "3DS initialization failed", orderID)
	}
	failURL := options.FailURL
	if failURL == "" {
		failURL = callbackURL
	}

	var fields formFields
	fields.set("paymentModel", "3D_PAY")
	fields.set("txnCode", txnCode)
	fields.set("merchantSafeId", p.cfg.MerchantSafeID)
	fields.set("terminalSafeId", p.cfg.TerminalSafeID)
	fields.set("orderId", orderID)
	fields.set("lang", p.lang())
	fields.set("amount", amount)
	fields.set("currencyCode", strconv.Itoa(currency))
	fields.set("installCount", strconv.Itoa(installCount(request.Installment)))
	fields.set("okUrl", callbackURL)
	fields.set("failUrl", failURL)
	fields.set("emailAddress", request.Buyer.Email)
	fields.set("creditCard", card.CardNumber)
	fields.set("expiredDate", expiry)
	fields.set("cvv", card.CVC)
	fields.set("randomNumber", randomNumber)
	fields.set("requestDateTime", FormatDateTime(time.Now()))
	if p.cfg.SubMerchantID != "" {
		fields.set("subMerchantId", p.cfg.SubMerchantID)
	}
	hash, err := Create3DFormHash(fields.values(), p.cfg.SecretKey)
	if err != nil {
		return p.failInit(err, "3DS initialization failed", orderID)
	}
	fields.set("hash", hash)

	html := `<!DOCTYPE html><html><head><meta charset="UTF-8"></head><body>` +
		`<form id="akbank-3d-form" method="POST" action="` + escapeHTML(p.gateway3DURL()) + `">` + fields.inputs() + `</form>` +
		`<script>document.getElementById("akbank-3d-form").submit();</script>` +
		`</body></html>`

	return &core.ThreeDSInitResponse{
		Status:             core.StatusPending,
		ThreeDSHTMLContent: html,
		PaymentID:          orderID,
		ConversationID:     orderID,
	}, nil
}

// CompleteThreeDSPayment verifies the okUrl/failUrl POST from Akbank.
//
// The signature must be valid and cover responseCode, orderId and the terminal
// ids. The signed values are concatenated without a separator, so a signature
// alone does not pin which value is the orderId: the outcome is confirmed with an
// order history query (txnCode 1010), and a callback whose result does not match
// Akbank's record for that order is rejected as INVALID_HASH. The payment
// succeeded only when the callback and the query both report an approved
// transaction for the order.
func (p *Provider) CompleteThreeDSPayment(ctx context.Context, callbackData any) (*core.PaymentResponse, error) {
	data := callbackFields(callbackData)
	orderID := data["orderId"]
	untrusted := func(message string, raw any) (*core.PaymentResponse, error) {
		return p.completeResult(core.StatusFailure, "INVALID_HASH", message, orderID, raw), nil
	}

	if !VerifyCallback(data, p.cfg.SecretKey) {
		return untrusted("Invalid 3D Secure callback signature", callbackData)
	}
	if data["merchantSafeId"] != p.cfg.MerchantSafeID || data["terminalSafeId"] != p.cfg.TerminalSafeID {
		return p.completeResult(core.StatusFailure, "TERMINAL_MISMATCH",
			"Callback belongs to a different terminal", orderID, callbackData), nil
	}

	approved := data["responseCode"] == SuccessCode

	body, err := p.baseRequest(TxnOrderHistory)
	if err != nil {
		return p.failPayment(err, "3D Secure result could not be confirmed", orderID)
	}
	body = append(body, jsonField{Name: "order", Value: []jsonField{{Name: "orderId", Value: orderID}}})
	inquiry, err := p.process(ctx, body, true)
	if err != nil {
		return p.failPayment(err, "3D Secure result could not be confirmed", orderID)
	}

	confirmed := false
	for _, tx := range detailsOf(inquiry) {
		if isApprovedPayment(tx, orderID) {
			confirmed = true
			break
		}
	}
	if approved != confirmed {
		return untrusted("3D Secure callback does not match the order at Akbank",
			map[string]any{"callback": callbackData, "inquiry": inquiry})
	}
	if approved {
		return p.completeResult(core.StatusSuccess, "", "", orderID, callbackData), nil
	}
	return p.completeResult(core.StatusFailure, data["responseCode"],
		firstNonEmpty(data["hostMessage"], data["responseMessage"]), orderID, callbackData), nil
}

// Refund refunds a payment (txnCode 1002). A lower amount than the sale is a
// partial refund. PaymentID is the order id.
func (p *Provider) Refund(ctx context.Context, request *core.RefundRequest) (*core.RefundResponse, error) {
	conversationID := ""
	if request != nil {
		conversationID = request.ConversationID
	}
	if request == nil {
		return p.failRefund(core.NewValidationError("request is required", "akbank", nil), "Refund failed", conversationID)
	}
	if err := p.ValidateRefund(request); err != nil {
		return p.failRefund(err, "Refund failed", conversationID)
	}
	amount, err := FormatAmount(request.Price)
	if err != nil {
		return p.failRefund(err, "Refund failed", conversationID)
	}
	currency, err := CurrencyCode(request.Currency)
	if err != nil {
		return p.failRefund(err, "Refund failed", conversationID)
	}
	body, err := p.baseRequest(TxnRefund)
	if err != nil {
		return p.failRefund(err, "Refund failed", conversationID)
	}
	body = append(body,
		jsonField{Name: "transaction", Value: []jsonField{
			{Name: "amount", Value: amount},
			{Name: "currencyCode", Value: currency},
		}},
		jsonField{Name: "order", Value: []jsonField{{Name: "orderId", Value: request.PaymentID}}},
	)

	data, err := p.process(ctx, body, false)
	if err != nil {
		return p.failRefund(err, "Refund failed", conversationID)
	}

	out := &core.RefundResponse{ConversationID: conversationID, RawResponse: data}
	if stringOf(data, "responseCode") == SuccessCode {
		out.Status = core.StatusSuccess
		transaction := objectOf(data, "transaction")
		out.RefundID = firstNonEmpty(stringOf(transaction, "rrn"), stringOf(transaction, "authCode"))
	} else {
		out.Status = core.StatusFailure
		out.ErrorCode, out.ErrorMessage = errorOf(data)
	}
	out.Code = p.errorCodeFor(out.Status, out.ErrorCode, data)
	return out, nil
}

// Cancel voids a payment (txnCode 1003). PaymentID is the order id.
func (p *Provider) Cancel(ctx context.Context, request *core.CancelRequest) (*core.CancelResponse, error) {
	conversationID := ""
	if request != nil {
		conversationID = request.ConversationID
	}
	if request == nil {
		return p.failCancel(core.NewValidationError("request is required", "akbank", nil), "Cancel failed", conversationID)
	}
	body, err := p.baseRequest(TxnVoid)
	if err != nil {
		return p.failCancel(err, "Cancel failed", conversationID)
	}
	body = append(body, jsonField{Name: "order", Value: []jsonField{{Name: "orderId", Value: request.PaymentID}}})

	data, err := p.process(ctx, body, false)
	if err != nil {
		return p.failCancel(err, "Cancel failed", conversationID)
	}

	out := &core.CancelResponse{ConversationID: conversationID, RawResponse: data}
	if stringOf(data, "responseCode") == SuccessCode {
		out.Status = core.StatusSuccess
		out.TransactionID = stringOf(objectOf(data, "transaction"), "rrn")
	} else {
		out.Status = core.StatusFailure
		out.ErrorCode, out.ErrorMessage = errorOf(data)
	}
	out.Code = p.errorCodeFor(out.Status, out.ErrorCode, data)
	return out, nil
}

// Capture charges all or part of a pre-authorization (txnCode 1005). Amount may
// be lower than the authorized amount.
func (p *Provider) Capture(ctx context.Context, request *core.CaptureRequest) (*core.PaymentResponse, error) {
	paymentID, conversationID := "", ""
	if request != nil {
		paymentID = request.PaymentID
		conversationID = request.ConversationID
		if conversationID == "" {
			conversationID = paymentID
		}
	}
	if request == nil {
		return p.failPayment(core.NewValidationError("request is required", "akbank", nil), "Capture failed", conversationID)
	}
	if err := p.ValidateCapture(request); err != nil {
		return p.failPayment(err, "Capture failed", conversationID)
	}
	amount, err := FormatAmount(request.Amount)
	if err != nil {
		return p.failPayment(err, "Capture failed", conversationID)
	}
	currency, err := CurrencyCode(request.Currency)
	if err != nil {
		return p.failPayment(err, "Capture failed", conversationID)
	}
	body, err := p.baseRequest(TxnPostAuth)
	if err != nil {
		return p.failPayment(err, "Capture failed", conversationID)
	}
	body = append(body,
		jsonField{Name: "order", Value: []jsonField{{Name: "orderId", Value: request.PaymentID}}},
		jsonField{Name: "transaction", Value: []jsonField{
			{Name: "amount", Value: amount},
			{Name: "currencyCode", Value: currency},
		}},
		jsonField{Name: "customer", Value: []jsonField{{Name: "ipAddress", Value: request.IP}}},
	)

	data, err := p.process(ctx, body, false)
	if err != nil {
		return p.failPayment(err, "Capture failed", conversationID)
	}

	out := &core.PaymentResponse{PaymentID: paymentID, ConversationID: conversationID, RawResponse: data}
	if stringOf(data, "responseCode") == SuccessCode {
		out.Status = core.StatusSuccess
	} else {
		out.Status = core.StatusFailure
		out.ErrorCode, out.ErrorMessage = errorOf(data)
	}
	out.Code = p.errorCodeFor(out.Status, out.ErrorCode, data)
	return out, nil
}

// VoidAuthorization releases a pre-authorization: Akbank voids it like any other
// transaction (txnCode 1003).
func (p *Provider) VoidAuthorization(ctx context.Context, request *core.VoidAuthorizationRequest) (*core.CancelResponse, error) {
	if request == nil {
		return p.Cancel(ctx, nil)
	}
	return p.Cancel(ctx, &core.CancelRequest{
		PaymentID: request.PaymentID, IP: request.IP, ConversationID: request.ConversationID,
	})
}

// InstallmentInfo is not available: the Sanal POS API has no installment-rate
// query. Installment plans come from the merchant's Akbank agreement.
func (p *Provider) InstallmentInfo(context.Context, *core.InstallmentInfoRequest) (*core.InstallmentInfoResponse, error) {
	err := core.NewError("Akbank's Sanal POS API has no installment-rate query. Installment plans come from your Akbank agreement: "+
		"send `installment` in the payment request and compute `paidPrice` from your contracted rates.", "NOT_SUPPORTED")
	err.Provider = "akbank"
	return nil, err
}

// GetPayment queries the order history (txnCode 1010). PaymentID is the order id.
func (p *Provider) GetPayment(ctx context.Context, paymentID string) (*core.PaymentResponse, error) {
	body, err := p.baseRequest(TxnOrderHistory)
	if err != nil {
		return p.failPayment(err, "Get payment failed", paymentID)
	}
	body = append(body, jsonField{Name: "order", Value: []jsonField{{Name: "orderId", Value: paymentID}}})

	data, err := p.process(ctx, body, true)
	if err != nil {
		return p.failPayment(err, "Get payment failed", paymentID)
	}

	if stringOf(data, "responseCode") != SuccessCode {
		errorCode, errorMessage := errorOf(data)
		out := p.result(core.StatusFailure, errorCode, errorMessage, data)
		out.PaymentID, out.ConversationID = paymentID, paymentID
		return out, nil
	}

	list := detailsOf(data)
	var sale TxnDetail
	for _, tx := range list {
		if code := stringOf(tx, "txnCode"); code == TxnSale || code == TxnSecureSale {
			sale = tx
			break
		}
	}
	if sale == nil && len(list) > 0 {
		sale = list[0]
	}

	out := p.result(MapTxnStatus(sale), "", "", data)
	if sale != nil && stringOf(sale, "responseCode") != SuccessCode {
		out.ErrorMessage = stringOf(sale, "responseMessage")
	}
	out.PaymentID, out.ConversationID = paymentID, paymentID
	return out, nil
}

// MapTxnStatus maps an order history entry: txnStatus N = completed, V = voided,
// R = refunded, S = failed.
func MapTxnStatus(tx TxnDetail) core.PaymentStatus {
	if tx == nil {
		return core.StatusPending
	}
	responseCode := stringOf(tx, "responseCode")
	if responseCode != "" && responseCode != SuccessCode {
		return core.StatusFailure
	}
	switch stringOf(tx, "txnStatus") {
	case "N":
		return core.StatusSuccess
	case "V", "R":
		return core.StatusCancelled
	case "S":
		return core.StatusFailure
	default:
		if responseCode == SuccessCode {
			return core.StatusSuccess
		}
		return core.StatusPending
	}
}

// isApprovedPayment reports an approved, not reversed sale or pre-authorization
// of this order.
func isApprovedPayment(tx TxnDetail, orderID string) bool {
	if orderID == "" {
		return false
	}
	if _, ok := tx["orderId"]; ok && stringOf(tx, "orderId") != orderID {
		return false
	}
	if _, ok := tx["txnCode"]; ok && !contains(PaymentTxnCodes, stringOf(tx, "txnCode")) {
		return false
	}
	return MapTxnStatus(tx) == core.StatusSuccess
}
