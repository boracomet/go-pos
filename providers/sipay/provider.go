// Package sipay implements the Sipay ccpayment API, also used by QNBpay.
//
// Flow: paySmart2D charges a card without 3D Secure; paySmart3D returns the 3D
// Secure page and posts the result to return_url (success) or cancel_url
// (failure). Only the hash key of that callback is trusted: it is decrypted with
// the app secret and its content decides the result. Refunds use refund, status
// queries checkstatus, pre-authorizations confirmPayment and BIN/installment
// queries getpos. Every request carries a Bearer token obtained with the app
// credentials and reused until it expires.
package sipay

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"net/url"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/boracomet/go-pos/core"
)

var cardRules = core.PaymentValidationRules{
	Card: true,
	Required: []string{
		"buyer.name",
		"buyer.surname",
		"buyer.email",
		"buyer.gsmNumber",
		"buyer.ip",
		"billingAddress.address",
	},
}

// tokenLifetime is how long a token is reused: tokens are valid for 2 hours, a
// new one is requested after 100 minutes.
const tokenLifetime = 100 * time.Minute

var errorCodes = map[string]core.PaymentErrorCode{
	"1":   core.CodeInvalidRequest, // basic validation
	"3":   core.CodeDuplicateOrder, // invoice id already processed
	"12":  core.CodeInvalidRequest, // items or currency invalid
	"13":  core.CodeInvalidRequest, // items do not add up to total
	"14":  core.CodeProviderError,  // merchant not found
	"30":  core.CodeProviderError,  // invalid credentials
	"31":  core.CodeInvalidRequest, // transaction not found
	"32":  core.CodeInvalidRequest, // invalid invoice id
	"33":  core.CodeInvalidRequest, // quantity must be an integer
	"34":  core.CodeProviderError,  // integration method not allowed
	"35":  core.CodeProviderError,  // no credit card payment option
	"36":  core.CodeProviderError,  // POS not found
	"37":  core.CodeProviderError,  // POS commission not set
	"38":  core.CodeProviderError,  // commission not set for currency
	"39":  core.CodeProviderError,  // commission not found
	"40":  core.CodeProviderError,  // installment not found, too many requests
	"41":  core.CodeCardDeclined,   // order or payment failed
	"42":  core.CodeInvalidRequest, // price lower than commission
	"43":  core.CodeProviderError,  // payment term not set
	"44":  core.CodeLimitExceeded,  // card blocked or daily limit
	"45":  core.CodeLimitExceeded,
	"46":  core.CodeLimitExceeded,
	"47":  core.CodeLimitExceeded,
	"48":  core.CodeLimitExceeded, // per-transaction minimum
	"49":  core.CodeProviderError, // refund failed
	"60":  core.CodeCardDeclined,  // card not allowed for merchant
	"68":  core.CodeProviderError, // request hash key mismatch
	"76":  core.CodeCardDeclined,  // foreign cards not allowed
	"77":  core.CodeCardDeclined,
	"85":  core.CodeInvalidRequest, // invalid character
	"90":  core.CodeProviderError,  // invalid hash key
	"99":  core.CodeProviderError,  // unknown error
	"101": core.CodeProviderError,  // unauthenticated (expired token)
	"102": core.CodeProviderError,  // POS not defined for installment
	"105": core.CodeCardDeclined,   // transaction not approved
	"108": core.CodeInvalidCard,    // invalid card number
	"110": core.CodeInvalidRequest, // partial refund not allowed
	"117": core.CodeDuplicateOrder, // invoice id already processed
	"119": core.CodeFraudSuspected, // declined for security reasons
}

var (
	unauthenticatedRe     = regexp.MustCompile(`(?i)unauthenticated`)
	unauthenticatedBodyRe = regexp.MustCompile(`(?i)"status_code"\s*:\s*101\b.*unauthenticated`)
)

func below500(status int) bool { return status < 500 }

// Provider is the Sipay (or QNBpay) payment provider.
type Provider struct {
	core.BaseProvider
	cfg         Config
	brand       Brand
	displayName string
	client      *core.HTTPClient

	mu           sync.Mutex
	token        string
	tokenExpires time.Time
	inflight     *tokenCall
}

// tokenCall is the shared in-flight token request: concurrent calls wait for the
// same token instead of requesting one each.
type tokenCall struct {
	done  chan struct{}
	value string
	err   error
}

var _ core.Provider = (*Provider)(nil)

// New builds the provider, requiring the app credentials, the merchant key and
// a base URL. Use SandboxURL / ProductionURL, or the QNBpay URLs with
// BrandQNBpay.
func New(cfg Config) (*Provider, error) {
	brand := cfg.Brand
	if brand == "" {
		brand = BrandSipay
	}
	name, display := providerName, sipayDisplayName
	switch brand {
	case BrandSipay:
	case BrandQNBpay:
		name, display = qnbpayProviderName, qnbpayDisplayName
	default:
		return nil, core.NewConfigurationError("Unknown Sipay brand: "+string(cfg.Brand), providerName)
	}

	missing := make([]string, 0, 3)
	if cfg.AppID == "" {
		missing = append(missing, "appId")
	}
	if cfg.AppSecret == "" {
		missing = append(missing, "appSecret")
	}
	if cfg.MerchantKey == "" {
		missing = append(missing, "merchantKey")
	}
	if len(missing) > 0 {
		return nil, core.NewConfigurationError(display+" configuration is missing: "+strings.Join(missing, ", "), name)
	}
	if cfg.BaseURL == "" {
		return nil, core.NewConfigurationError(display+" baseUrl is required", name)
	}

	base := core.NewBaseProvider(name, cfg.ProviderConfig)
	base.ErrorCodes = errorCodes
	return &Provider{
		BaseProvider: base,
		cfg:          cfg,
		brand:        brand,
		displayName:  display,
		client: base.NewHTTPClient(30*time.Second, map[string]string{
			"Content-Type": "application/json",
			"Accept":       "application/json",
		}),
	}, nil
}

// Brand returns the institution this provider talks to.
func (p *Provider) Brand() Brand { return p.brand }

func (p *Provider) language() string { return GetLanguage(p.Config.Locale) }

// codeFor resolves the normalized code of a result. The bank's ISO 8583 code is
// the most specific reason, but only when it is also the reported error code;
// otherwise Sipay's own status code is used.
func (p *Provider) codeFor(status core.PaymentStatus, errorCode string, raw any) core.PaymentErrorCode {
	if status != core.StatusFailure && errorCode != core.NetworkErrorCode {
		return ""
	}
	if bank := bankCodeOf(raw); bank != "" && bank == errorCode {
		if code, ok := core.ISO8583ErrorCodes[bank]; ok {
			return code
		}
	}
	return p.ErrorCodeFor(status, errorCode)
}

// applyFailure writes a failure into out. A refused token keeps the provider's
// status code and response instead of the transport error message.
func (p *Provider) applyFailure(err error, fallback string, out core.FailureResult) {
	var tok *tokenError
	if errors.As(err, &tok) {
		out.SetFailure(core.Failure{
			Status:       core.StatusFailure,
			ErrorCode:    tok.statusCode,
			ErrorMessage: tok.Message,
			RawResponse:  tok.response,
		})
		return
	}
	core.ApplyFailure(p.displayName, err, fallback, out)
}

func (p *Provider) getToken(ctx context.Context, renew bool) (string, error) {
	p.mu.Lock()
	if !renew && p.token != "" && time.Now().Before(p.tokenExpires) {
		value := p.token
		p.mu.Unlock()
		return value, nil
	}
	if p.inflight == nil {
		call := &tokenCall{done: make(chan struct{})}
		p.inflight = call
		p.mu.Unlock()

		value, err := p.requestToken(ctx)

		p.mu.Lock()
		call.value, call.err = value, err
		p.inflight = nil
		p.mu.Unlock()
		close(call.done)
		return value, err
	}
	call := p.inflight
	p.mu.Unlock()
	select {
	case <-call.done:
		return call.value, call.err
	case <-ctx.Done():
		return "", ctx.Err()
	}
}

func (p *Provider) requestToken(ctx context.Context) (string, error) {
	body, err := json.Marshal(Result{
		"app_id":     p.cfg.AppID,
		"app_secret": p.cfg.AppSecret,
		"app_lang":   p.language(),
	})
	if err != nil {
		return "", err
	}
	response, err := p.client.Post(ctx, EndpointToken, string(body), core.RequestConfig{
		Retryable:      true,
		ValidateStatus: below500,
	})
	if err != nil {
		return "", err
	}
	data := asResult(response.Data)
	if data == nil {
		data = Result{}
	}
	value := nonEmpty(nested(data, "data", "token"))
	if value == "" {
		return "", &tokenError{
			BaseError: core.BaseError{
				Message:  p.displayName + " did not issue a token: " + firstNonEmpty(nonEmpty(data["status_description"]), "unknown error"),
				Code:     "PROVIDER_ERROR",
				Provider: p.Name(),
			},
			statusCode: nonEmpty(data["status_code"]),
			response:   data,
		}
	}
	p.mu.Lock()
	p.token, p.tokenExpires = value, time.Now().Add(tokenLifetime)
	p.mu.Unlock()
	return value, nil
}

// unauthenticated reports a request rejected because the token expired early.
// Such a request was not processed, so it can be sent again with a new token.
func unauthenticated(response *core.Response) bool {
	if response.Status == 401 {
		return true
	}
	if text, ok := response.Data.(string); ok {
		return unauthenticatedBodyRe.MatchString(text)
	}
	record := asResult(response.Data)
	code, _ := jsNumber(record["status_code"])
	return code == 101 && unauthenticatedRe.MatchString(jsString(record["status_description"]))
}

// request POSTs JSON with the Bearer token, renewing it once when the answer
// says the token is no longer authenticated.
func (p *Provider) request(ctx context.Context, endpoint string, body any, retryable bool, responseType string) (any, error) {
	data, err := json.Marshal(body)
	if err != nil {
		return nil, err
	}
	send := func(token string) (*core.Response, error) {
		return p.client.Post(ctx, endpoint, string(data), core.RequestConfig{
			Retryable:      retryable,
			ResponseType:   responseType,
			Headers:        map[string]string{"Authorization": "Bearer " + token},
			ValidateStatus: below500,
		})
	}

	token, err := p.getToken(ctx, false)
	if err != nil {
		return nil, err
	}
	response, err := send(token)
	if err != nil {
		return nil, err
	}
	if unauthenticated(response) {
		token, err = p.getToken(ctx, true)
		if err != nil {
			return nil, err
		}
		response, err = send(token)
		if err != nil {
			return nil, err
		}
	}
	return response.Data, nil
}

func (p *Provider) paymentBody(req *core.PaymentRequest, invoiceID string, preAuth bool) (Result, error) {
	card, err := p.CardOf(req)
	if err != nil {
		return nil, err
	}
	total, err := FormatAmount(paidPriceOf(req))
	if err != nil {
		return nil, err
	}
	installment, err := FormatInstallment(req.Installment)
	if err != nil {
		return nil, err
	}
	currency, err := GetCurrency(req.Currency)
	if err != nil {
		return nil, err
	}
	month, year, err := FormatExpiry(card.ExpireMonth, card.ExpireYear)
	if err != nil {
		return nil, err
	}
	description := "Order " + invoiceID
	items, err := BuildItems(req.BasketItems, total, description)
	if err != nil {
		return nil, err
	}
	hash, err := CreatePaymentHash(total, installment, currency, p.cfg.MerchantKey, invoiceID, p.cfg.AppSecret)
	if err != nil {
		return nil, err
	}
	transactionType := "Auth"
	if preAuth {
		transactionType = "PreAuth"
	}

	body := Result{
		"cc_holder_name":      card.CardHolderName,
		"cc_no":               stripSpaces(card.CardNumber),
		"expiry_month":        month,
		"expiry_year":         year,
		"cvv":                 card.CVC,
		"currency_code":       currency,
		"installments_number": installment,
		"invoice_id":          invoiceID,
		"invoice_description": description,
		"name":                req.Buyer.Name,
		"surname":             req.Buyer.Surname,
		"total":               total,
		"merchant_key":        p.cfg.MerchantKey,
		"items":               items,
		"hash_key":            hash,
		"bill_email":          req.Buyer.Email,
		"bill_phone":          req.Buyer.GSMNumber,
		"ip":                  req.Buyer.IP,
		"transaction_type":    transactionType,
		"app_lang":            p.language(),
	}
	// Optional in the API: absent fields are not sent at all.
	putIfNotEmpty(body, "bill_address1", req.BillingAddress.Address)
	putIfNotEmpty(body, "bill_city", req.BillingAddress.City)
	putIfNotEmpty(body, "bill_postcode", req.BillingAddress.ZipCode)
	putIfNotEmpty(body, "bill_country", req.BillingAddress.Country)
	if p.cfg.SaleWebhookKey != "" {
		body["sale_web_hook_key"] = p.cfg.SaleWebhookKey
	}
	return body, nil
}

// CreatePayment charges a card without 3D Secure (paySmart2D), which Sipay
// enables per merchant. PaymentID is the invoice id: ConversationID, or a
// generated one.
func (p *Provider) CreatePayment(ctx context.Context, req *core.PaymentRequest) (*core.PaymentResponse, error) {
	return p.payNonSecure(ctx, req, false, "Payment failed")
}

// Authorize blocks the amount without 3D Secure (paySmart2D, PreAuth). Charge it
// with Capture or release it with VoidAuthorization.
func (p *Provider) Authorize(ctx context.Context, req *core.PaymentRequest) (*core.PaymentResponse, error) {
	return p.payNonSecure(ctx, req, true, "Pre-authorization failed")
}

func (p *Provider) payNonSecure(ctx context.Context, req *core.PaymentRequest, preAuth bool, fallback string) (*core.PaymentResponse, error) {
	invoiceID := invoiceIDOf(req)
	fail := func(err error) (*core.PaymentResponse, error) {
		resp := &core.PaymentResponse{}
		p.applyFailure(err, fallback, resp)
		resp.PaymentID, resp.ConversationID = invoiceID, invoiceID
		resp.Code = p.codeFor(resp.Status, resp.ErrorCode, resp.RawResponse)
		return resp, nil
	}

	if req == nil {
		return fail(requestRequired())
	}
	if err := p.ValidatePayment(req, cardRules); err != nil {
		return fail(err)
	}
	body, err := p.paymentBody(req, invoiceID, preAuth)
	if err != nil {
		return fail(err)
	}
	data, err := p.request(ctx, EndpointPay2D, body, false, "")
	if err != nil {
		return fail(err)
	}

	response := asResult(data)
	approved := isSuccessCode(response["status_code"]) && isOne(nested(response, "data", "payment_status"))
	resp := &core.PaymentResponse{
		Status:         statusOf(approved),
		PaymentID:      invoiceID,
		ConversationID: invoiceID,
		RawResponse:    data,
	}
	if !approved {
		resp.ErrorCode, resp.ErrorMessage = errorOf(asResult(response["data"]), response)
	}
	resp.Code = p.codeFor(resp.Status, resp.ErrorCode, resp.RawResponse)
	return resp, nil
}

// InitThreeDSPayment starts a 3D Secure payment (paySmart3D) and returns the
// page to render. Sipay posts the result to CallbackURL (return_url) on success
// and to the same URL on failure; pass that POST body to
// CompleteThreeDSPayment. Use InitThreeDSPaymentWithFailURL to send failures to
// a different URL.
func (p *Provider) InitThreeDSPayment(ctx context.Context, req *core.ThreeDSPaymentRequest) (*core.ThreeDSInitResponse, error) {
	return p.payThreeDS(ctx, threeDSRequestOf(req), false, "3DS initialization failed")
}

// InitThreeDSPaymentWithFailURL starts a 3D Secure payment with a separate
// cancel_url for failures.
func (p *Provider) InitThreeDSPaymentWithFailURL(ctx context.Context, req *ThreeDSRequest) (*core.ThreeDSInitResponse, error) {
	return p.payThreeDS(ctx, req, false, "3DS initialization failed")
}

// InitThreeDSAuthorize starts a 3D Secure pre-authorization (paySmart3D,
// PreAuth); complete it with CompleteThreeDSPayment.
func (p *Provider) InitThreeDSAuthorize(ctx context.Context, req *core.ThreeDSPaymentRequest) (*core.ThreeDSInitResponse, error) {
	return p.payThreeDS(ctx, threeDSRequestOf(req), true, "3DS initialization failed")
}

// InitThreeDSAuthorizeWithFailURL starts a 3D Secure pre-authorization with a
// separate cancel_url for failures.
func (p *Provider) InitThreeDSAuthorizeWithFailURL(ctx context.Context, req *ThreeDSRequest) (*core.ThreeDSInitResponse, error) {
	return p.payThreeDS(ctx, req, true, "3DS initialization failed")
}

func (p *Provider) payThreeDS(ctx context.Context, req *ThreeDSRequest, preAuth bool, fallback string) (*core.ThreeDSInitResponse, error) {
	invoiceID, callbackURL, failURL := "", "", ""
	if req != nil {
		invoiceID = req.ConversationID
		callbackURL = req.CallbackURL
		if callbackURL == "" {
			callbackURL = req.PaymentRequest.CallbackURL
		}
		failURL = req.FailURL
	}
	if invoiceID == "" {
		invoiceID = core.GenerateOrderID("")
	}
	fail := func(err error) (*core.ThreeDSInitResponse, error) {
		resp := &core.ThreeDSInitResponse{}
		p.applyFailure(err, fallback, resp)
		resp.PaymentID, resp.ConversationID = invoiceID, invoiceID
		resp.Code = p.codeFor(resp.Status, resp.ErrorCode, resp.RawResponse)
		return resp, nil
	}

	if req == nil {
		return fail(requestRequired())
	}
	if err := p.ValidatePayment(&req.PaymentRequest, cardRules); err != nil {
		return fail(err)
	}
	if callbackURL == "" {
		return fail(core.NewValidationError("callbackUrl is required for 3D Secure payments", "", nil))
	}
	body, err := p.paymentBody(&req.PaymentRequest, invoiceID, preAuth)
	if err != nil {
		return fail(err)
	}
	if failURL == "" {
		failURL = callbackURL
	}
	body["return_url"] = callbackURL
	body["cancel_url"] = failURL
	body["response_method"] = "POST"

	// paySmart3D answers with HTML: the 3D Secure page, or a form posting the
	// error to cancel_url. Some errors come back as JSON.
	data, err := p.request(ctx, EndpointPay3D, body, false, "text")
	if err != nil {
		return fail(err)
	}
	html, err := htmlOf(data)
	if err != nil {
		return fail(err)
	}
	if rejection := rejectionOf(html, p.displayName); rejection != nil {
		resp := &core.ThreeDSInitResponse{
			Status:         core.StatusFailure,
			PaymentID:      invoiceID,
			ConversationID: invoiceID,
			RawResponse:    rejection,
		}
		resp.ErrorCode, resp.ErrorMessage = errorOf(rejection, nil)
		resp.Code = p.codeFor(resp.Status, resp.ErrorCode, resp.RawResponse)
		return resp, nil
	}

	resp := &core.ThreeDSInitResponse{
		Status:             core.StatusPending,
		ThreeDSHTMLContent: html,
		PaymentID:          invoiceID,
		ConversationID:     invoiceID,
	}
	resp.Code = p.codeFor(resp.Status, resp.ErrorCode, resp.RawResponse)
	return resp, nil
}

// htmlOf renders a paySmart3D answer: text as it is, anything else as JSON.
func htmlOf(data any) (string, error) {
	if text, ok := data.(string); ok {
		return text, nil
	}
	if data == nil {
		return `""`, nil
	}
	encoded, err := json.Marshal(data)
	if err != nil {
		return "", err
	}
	return string(encoded), nil
}

// rejectionOf returns the error of a rejected paySmart3D request, or nil for a
// 3D Secure page.
func rejectionOf(body, name string) Result {
	text := strings.TrimSpace(body)
	if text == "" {
		return Result{"status_description": name + " returned an empty response"}
	}
	if strings.HasPrefix(text, "{") {
		var parsed Result
		if err := json.Unmarshal([]byte(text), &parsed); err != nil {
			return Result{"status_description": name + " returned an invalid response"}
		}
		merged := Result{}
		for key, value := range parsed {
			merged[key] = value
		}
		for key, value := range asResult(parsed["data"]) {
			merged[key] = value
		}
		return merged
	}
	fields := ParseForm(text)
	statusCode, present := fields["status_code"]
	failed := (present && !isSuccessCode(statusCode)) ||
		fields["sipay_status"] == "0" ||
		fields["payment_status"] == "0"
	if !failed {
		return nil
	}
	rejection := Result{}
	for key, value := range fields {
		rejection[key] = value
	}
	return rejection
}

// CompleteThreeDSPayment completes a 3D Secure payment from the fields Sipay
// posts to return_url or cancel_url.
//
// Only the hash key is trusted: it is decrypted with the app secret and its
// content (status, total, invoice id, order id, currency) decides the result. A
// callback without a valid hash key, or whose invoice id or order id differs
// from the hash key, is rejected as INVALID_HASH and carries no ids.
// RawResponse["verified"] holds the verified values: compare Total with the
// amount of the order.
func (p *Provider) CompleteThreeDSPayment(ctx context.Context, callbackData any) (*core.PaymentResponse, error) {
	fields := normalizeCallback(callbackData)
	verified := VerifyPaymentHash(jsString(fields["hash_key"]), p.cfg.AppSecret)

	invoiceMatches := verified != nil && equalIDs(jsString(fields["invoice_id"]), verified.InvoiceID)
	orderID := verified.OrderID
	if verified != nil {
		if value, present := fields["order_id"]; present && value != nil {
			orderID = jsString(value)
		}
	}
	orderMatches := verified != nil && equalIDs(orderID, verified.OrderID)

	if verified == nil || verified.InvoiceID == "" || !invoiceMatches || !orderMatches {
		resp := &core.PaymentResponse{
			Status:      core.StatusFailure,
			ErrorCode:   "INVALID_HASH",
			RawResponse: callbackData,
		}
		if verified != nil {
			resp.ErrorMessage = "The callback does not match its " + p.displayName + " hash key"
		} else {
			resp.ErrorMessage = "The " + p.displayName + " hash key is missing or invalid"
		}
		resp.Code = p.codeFor(resp.Status, resp.ErrorCode, resp.RawResponse)
		return resp, nil
	}

	approved := verified.Status == "1"
	resp := &core.PaymentResponse{
		Status:         statusOf(approved),
		PaymentID:      verified.InvoiceID,
		ConversationID: verified.InvoiceID,
		RawResponse:    Result{"callback": callbackData, "verified": verified},
	}
	if !approved {
		resp.ErrorCode, resp.ErrorMessage = errorOf(fields, nil)
	}
	resp.Code = p.codeFor(resp.Status, resp.ErrorCode, resp.RawResponse)
	return resp, nil
}

func (p *Provider) checkStatus(ctx context.Context, invoiceID string) (Result, error) {
	hash, err := CreateStatusHash(invoiceID, p.cfg.MerchantKey, p.cfg.AppSecret)
	if err != nil {
		return nil, err
	}
	data, err := p.request(ctx, EndpointCheckStatus, Result{
		"merchant_key":           p.cfg.MerchantKey,
		"invoice_id":             invoiceID,
		"include_pending_status": true,
		"show_refund_info":       "Y",
		"hash_key":               hash,
		"app_lang":               p.language(),
	}, true, "")
	if err != nil {
		return nil, err
	}
	result := asResult(data)
	if result == nil {
		result = Result{}
	}
	return result, nil
}

// GetPayment returns the payment status (checkstatus). paymentID is the invoice id.
func (p *Provider) GetPayment(ctx context.Context, paymentID string) (*core.PaymentResponse, error) {
	fail := func(err error) (*core.PaymentResponse, error) {
		resp := &core.PaymentResponse{}
		p.applyFailure(err, "Get payment failed", resp)
		resp.PaymentID, resp.ConversationID = paymentID, paymentID
		resp.Code = p.codeFor(resp.Status, resp.ErrorCode, resp.RawResponse)
		return resp, nil
	}

	data, err := p.checkStatus(ctx, paymentID)
	if err != nil {
		return fail(err)
	}
	status := MapTransactionStatus(jsString(data["transaction_status"]))
	resp := &core.PaymentResponse{
		Status:         status,
		PaymentID:      paymentID,
		ConversationID: paymentID,
		RawResponse:    data,
	}
	if status == core.StatusFailure {
		resp.ErrorCode, resp.ErrorMessage = errorOf(data, nil)
	}
	resp.Code = p.codeFor(resp.Status, resp.ErrorCode, resp.RawResponse)
	return resp, nil
}

func (p *Provider) refundAmount(ctx context.Context, invoiceID, amount string) (Result, error) {
	hash, err := CreateRefundHash(amount, invoiceID, p.cfg.MerchantKey, p.cfg.AppSecret)
	if err != nil {
		return nil, err
	}
	// The documentation lists app_id and app_secret too; the Bearer token and
	// hash key are enough, so the secret is not sent.
	data, err := p.request(ctx, EndpointRefund, Result{
		"invoice_id":   invoiceID,
		"amount":       amount,
		"merchant_key": p.cfg.MerchantKey,
		"hash_key":     hash,
		"app_lang":     p.language(),
	}, false, "")
	if err != nil {
		return nil, err
	}
	result := asResult(data)
	if result == nil {
		result = Result{}
	}
	return result, nil
}

// Refund refunds all or part of a payment. Sipay voids a refund of a same-day
// payment, so no separate cancel call is needed. paymentID is the invoice id.
func (p *Provider) Refund(ctx context.Context, req *core.RefundRequest) (*core.RefundResponse, error) {
	conversationID := ""
	if req != nil {
		conversationID = req.ConversationID
	}
	fail := func(err error) (*core.RefundResponse, error) {
		resp := &core.RefundResponse{}
		p.applyFailure(err, "Refund failed", resp)
		resp.ConversationID = conversationID
		resp.Code = p.codeFor(resp.Status, resp.ErrorCode, resp.RawResponse)
		return resp, nil
	}

	if req == nil {
		return fail(requestRequired())
	}
	if err := p.ValidateRefund(req); err != nil {
		return fail(err)
	}
	amount, err := FormatAmount(req.Price)
	if err != nil {
		return fail(err)
	}
	data, err := p.refundAmount(ctx, req.PaymentID, amount)
	if err != nil {
		return fail(err)
	}

	approved := isSuccessCode(data["status_code"])
	resp := &core.RefundResponse{
		Status:         statusOf(approved),
		ConversationID: req.ConversationID,
		RawResponse:    data,
	}
	if approved {
		resp.RefundID = firstNonEmpty(nonEmpty(data["ref_no"]), nonEmpty(data["ref_number"]))
	} else {
		resp.ErrorCode, resp.ErrorMessage = errorOf(data, nil)
	}
	resp.Code = p.codeFor(resp.Status, resp.ErrorCode, resp.RawResponse)
	return resp, nil
}

// remainingOf returns the amount still refundable: the transaction amount minus
// refunds, in the major unit.
func remainingOf(data Result) (string, error) {
	amount, ok := jsNumber(nullish(data["transaction_amount"], data["product_price"]))
	if !ok || amount <= 0 {
		return "", nil
	}
	refunded, _ := jsNumber(data["total_refunded_amount"])
	amountMinor, err := core.ToMinorUnits(numberText(amount), "amount")
	if err != nil {
		return "", err
	}
	refundedMinor, err := core.ToMinorUnits(numberText(refunded), "amount")
	if err != nil {
		return "", err
	}
	remaining := float64(amountMinor-refundedMinor) / 100
	if remaining <= 0 {
		return "", nil
	}
	return formatNumber(remaining)
}

// Cancel cancels a payment: it refunds what is left of it (a void on the same
// day), or releases it when it is an open pre-authorization. paymentID is the
// invoice id.
func (p *Provider) Cancel(ctx context.Context, req *core.CancelRequest) (*core.CancelResponse, error) {
	conversationID := ""
	if req != nil {
		conversationID = req.ConversationID
	}
	fail := func(err error) (*core.CancelResponse, error) {
		resp := &core.CancelResponse{}
		p.applyFailure(err, "Cancel failed", resp)
		resp.ConversationID = conversationID
		resp.Code = p.codeFor(resp.Status, resp.ErrorCode, resp.RawResponse)
		return resp, nil
	}

	if req == nil {
		return fail(requestRequired())
	}
	payment, err := p.checkStatus(ctx, req.PaymentID)
	if err != nil {
		return fail(err)
	}
	if isOpenPreAuth(payment) {
		data, err := p.confirm(ctx, req.PaymentID, ConfirmStatusVoid, payment, "")
		if err != nil {
			return fail(err)
		}
		return p.operationResult(data, conversationID), nil
	}

	amount := ""
	if req.Price != "" {
		value, err := core.ParseAmount(req.Price, "price")
		if err != nil {
			return fail(err)
		}
		amount, err = formatNumber(value)
		if err != nil {
			return fail(err)
		}
	} else {
		amount, err = remainingOf(payment)
		if err != nil {
			return fail(err)
		}
	}
	if MapTransactionStatus(jsString(payment["transaction_status"])) != core.StatusSuccess || amount == "" {
		resp := &core.CancelResponse{
			Status:         core.StatusFailure,
			ConversationID: conversationID,
			RawResponse:    payment,
		}
		resp.ErrorCode, resp.ErrorMessage = errorOf(payment, nil)
		if isSuccessCode(payment["status_code"]) {
			resp.ErrorCode = "NOT_CANCELLABLE"
			resp.ErrorMessage = p.displayName + " payment " + req.PaymentID + " is " +
				firstNonEmpty(presentText(payment, "transaction_status"), "not cancellable")
		}
		resp.Code = p.codeFor(resp.Status, resp.ErrorCode, resp.RawResponse)
		return resp, nil
	}

	data, err := p.refundAmount(ctx, req.PaymentID, amount)
	if err != nil {
		return fail(err)
	}
	return p.operationResult(data, conversationID), nil
}

func isOpenPreAuth(data Result) bool {
	return strings.ToLower(jsString(data["transaction_status"])) == "pre-authorization"
}

// confirm captures (status 1) or releases (status 2) a pre-authorization. An
// empty amount takes the whole transaction amount.
func (p *Provider) confirm(ctx context.Context, invoiceID string, status int, payment Result, amount string) (Result, error) {
	total := amount
	if total == "" {
		value, _ := jsNumber(payment["transaction_amount"])
		formatted, err := formatNumber(value)
		if err != nil {
			return nil, err
		}
		total = formatted
	}
	totalNumber, err := core.ParseAmount(total, "amount")
	if err != nil {
		return nil, err
	}
	hash, err := CreateConfirmHash(p.cfg.MerchantKey, invoiceID, status, p.cfg.AppSecret)
	if err != nil {
		return nil, err
	}
	data, err := p.request(ctx, EndpointConfirmPayment, Result{
		"invoice_id":   invoiceID,
		"merchant_key": p.cfg.MerchantKey,
		"status":       status,
		"total":        totalNumber,
		"hash_key":     hash,
		"app_lang":     p.language(),
	}, false, "")
	if err != nil {
		return nil, err
	}
	result := asResult(data)
	if result == nil {
		result = Result{}
	}
	return result, nil
}

// operationResult builds the cancellation result of a refund or confirmPayment.
func (p *Provider) operationResult(data Result, conversationID string) *core.CancelResponse {
	approved := isSuccessCode(data["status_code"])
	resp := &core.CancelResponse{
		Status:         statusOf(approved),
		ConversationID: conversationID,
		RawResponse:    data,
	}
	if approved {
		resp.TransactionID = firstNonEmpty(nonEmpty(data["ref_no"]), nonEmpty(data["ref_number"]))
	} else {
		resp.ErrorCode, resp.ErrorMessage = errorOf(data, nil)
	}
	resp.Code = p.codeFor(resp.Status, resp.ErrorCode, resp.RawResponse)
	return resp
}

// Capture charges all or part of a pre-authorized amount (confirmPayment,
// status 1). paymentID is the invoice id.
func (p *Provider) Capture(ctx context.Context, req *core.CaptureRequest) (*core.PaymentResponse, error) {
	paymentID, conversationID := "", ""
	if req != nil {
		paymentID = req.PaymentID
		conversationID = firstNonEmpty(req.ConversationID, req.PaymentID)
	}
	fail := func(err error) (*core.PaymentResponse, error) {
		resp := &core.PaymentResponse{}
		p.applyFailure(err, "Capture failed", resp)
		resp.PaymentID, resp.ConversationID = paymentID, conversationID
		resp.Code = p.codeFor(resp.Status, resp.ErrorCode, resp.RawResponse)
		return resp, nil
	}

	if req == nil {
		return fail(requestRequired())
	}
	if err := p.ValidateCapture(req); err != nil {
		return fail(err)
	}
	total, err := FormatAmount(req.Amount)
	if err != nil {
		return fail(err)
	}
	data, err := p.confirm(ctx, req.PaymentID, ConfirmStatusCapture, nil, total)
	if err != nil {
		return fail(err)
	}

	approved := isSuccessCode(data["status_code"])
	resp := &core.PaymentResponse{
		Status:         statusOf(approved),
		PaymentID:      paymentID,
		ConversationID: conversationID,
		RawResponse:    data,
	}
	if !approved {
		resp.ErrorCode, resp.ErrorMessage = errorOf(data, nil)
	}
	resp.Code = p.codeFor(resp.Status, resp.ErrorCode, resp.RawResponse)
	return resp, nil
}

// VoidAuthorization releases a pre-authorization (confirmPayment, status 2).
// paymentID is the invoice id.
func (p *Provider) VoidAuthorization(ctx context.Context, req *core.VoidAuthorizationRequest) (*core.CancelResponse, error) {
	conversationID := ""
	if req != nil {
		conversationID = req.ConversationID
	}
	fail := func(err error) (*core.CancelResponse, error) {
		resp := &core.CancelResponse{}
		p.applyFailure(err, "Voiding the pre-authorization failed", resp)
		resp.ConversationID = conversationID
		resp.Code = p.codeFor(resp.Status, resp.ErrorCode, resp.RawResponse)
		return resp, nil
	}

	if req == nil {
		return fail(requestRequired())
	}
	payment, err := p.checkStatus(ctx, req.PaymentID)
	if err != nil {
		return fail(err)
	}
	if !isOpenPreAuth(payment) {
		resp := &core.CancelResponse{
			Status:         core.StatusFailure,
			ConversationID: conversationID,
			RawResponse:    payment,
		}
		resp.ErrorCode, resp.ErrorMessage = errorOf(payment, nil)
		if isSuccessCode(payment["status_code"]) {
			resp.ErrorCode = "NOT_CANCELLABLE"
			resp.ErrorMessage = p.displayName + " payment " + req.PaymentID + " is not an open pre-authorization"
		}
		resp.Code = p.codeFor(resp.Status, resp.ErrorCode, resp.RawResponse)
		return resp, nil
	}
	data, err := p.confirm(ctx, req.PaymentID, ConfirmStatusVoid, payment, "")
	if err != nil {
		return fail(err)
	}
	return p.operationResult(data, conversationID), nil
}

func (p *Provider) getPos(ctx context.Context, binNumber, amount string) ([]Result, error) {
	bin := stripSpaces(binNumber)
	if !binRe.MatchString(bin) {
		return nil, core.NewValidationError("binNumber must be the first 6 to 8 digits of the card", "", nil)
	}
	price, err := core.ParseAmount(amount, "amount")
	if err != nil {
		return nil, err
	}
	data, err := p.request(ctx, EndpointGetPos, Result{
		"credit_card":   bin,
		"amount":        price,
		"currency_code": "TRY",
		"merchant_key":  p.cfg.MerchantKey,
		"app_lang":      p.language(),
	}, true, "")
	if err != nil {
		return nil, err
	}
	response := asResult(data)
	options := asOptions(response["data"])
	if !isSuccessCode(response["status_code"]) || len(options) == 0 {
		return nil, &core.BaseError{
			Message:  p.displayName + " getpos failed: " + firstNonEmpty(nonEmpty(response["status_description"]), "no installment options"),
			Code:     "PROVIDER_ERROR",
			Provider: p.Name(),
		}
	}
	return options, nil
}

// BinCheck returns the card type, scheme, program and bank of a BIN (getpos).
func (p *Provider) BinCheck(ctx context.Context, binNumber string) (*core.BinCheckResponse, error) {
	options, err := p.getPos(ctx, binNumber, "1")
	if err != nil {
		return nil, err
	}
	card := options[0]
	return &core.BinCheckResponse{
		BinNumber:       binNumber,
		CardType:        jsString(card["card_type"]),
		CardAssociation: strings.ToUpper(jsString(card["card_scheme"])),
		CardFamily:      jsString(card["card_program"]),
		BankName:        jsString(card["card_bank"]),
		BankCode:        bankCodeNumber(card["bank_code"]),
		Commercial:      isCommercial(card["is_commercial"]),
		RawResponse:     options,
	}, nil
}

// InstallmentInfo returns the installment options for a card (getpos), with the
// amount to charge for each count as calculated by Sipay.
func (p *Provider) InstallmentInfo(ctx context.Context, req *core.InstallmentInfoRequest) (*core.InstallmentInfoResponse, error) {
	conversationID := ""
	if req != nil {
		conversationID = req.ConversationID
	}
	fail := func(err error) (*core.InstallmentInfoResponse, error) {
		resp := &core.InstallmentInfoResponse{}
		p.applyFailure(err, "Installment info failed", resp)
		resp.ConversationID = conversationID
		resp.Code = p.codeFor(resp.Status, resp.ErrorCode, resp.RawResponse)
		return resp, nil
	}

	if req == nil {
		return fail(requestRequired())
	}
	priceMinor, err := core.ToMinorUnits(req.Price, "price")
	if err != nil {
		return fail(err)
	}
	if priceMinor <= 0 {
		return fail(core.NewValidationError("price must be greater than 0", "", nil))
	}
	options, err := p.getPos(ctx, req.BinNumber, numberText(float64(priceMinor)/100))
	if err != nil {
		return fail(err)
	}
	card := options[0]

	details := make([]core.InstallmentDetail, 0, len(options))
	for _, option := range options {
		count, _ := jsNumber(option["installments_number"])
		installmentNumber := int(count)
		if installmentNumber == 0 {
			installmentNumber = 1
		}
		amount := nullish(option["amount_to_be_paid"], option["payable_amount"])
		if amount == nil {
			amount = float64(priceMinor) / 100
		}
		totalMinor, err := core.ToMinorUnits(jsString(amount), "amount")
		if err != nil {
			return fail(err)
		}
		details = append(details, core.InstallmentDetail{
			InstallmentNumber: installmentNumber,
			TotalPrice:        float64(totalMinor) / 100,
			InstallmentPrice:  math.Round(float64(totalMinor)/float64(installmentNumber)) / 100,
		})
	}
	sort.SliceStable(details, func(i, j int) bool {
		return details[i].InstallmentNumber < details[j].InstallmentNumber
	})

	resp := &core.InstallmentInfoResponse{
		Status: core.StatusSuccess,
		InstallmentDetails: []core.InstallmentPrice{{
			BinNumber:         req.BinNumber,
			Price:             float64(priceMinor) / 100,
			CardType:          jsString(card["card_type"]),
			CardAssociation:   strings.ToUpper(jsString(card["card_scheme"])),
			CardFamilyName:    jsString(card["card_program"]),
			BankCode:          bankCodeNumber(card["bank_code"]),
			BankName:          jsString(card["card_bank"]),
			Commercial:        boolToInt(isCommercial(card["is_commercial"])),
			InstallmentPrices: details,
		}},
		ConversationID: conversationID,
		RawResponse:    options,
	}
	resp.Code = p.codeFor(resp.Status, resp.ErrorCode, resp.RawResponse)
	return resp, nil
}

func isCommercial(value any) bool {
	return strings.ToUpper(jsString(value)) == "TRUE"
}

func bankCodeNumber(value any) int {
	number, _ := jsNumber(value)
	return int(number)
}

// normalizeCallback reads the fields Sipay posted, keeping the distinction
// between an absent field and an empty one.
func normalizeCallback(data any) Result {
	fields := Result{}
	switch value := data.(type) {
	case nil:
	case map[string]any:
		for key, item := range value {
			fields[key] = item
		}
	case map[string]string:
		for key, item := range value {
			fields[key] = item
		}
	case url.Values:
		for key, item := range value {
			if len(item) > 0 {
				fields[key] = item[0]
			}
		}
	}
	return fields
}

// equalIDs compares callback ids in constant time. Two empty ids match, as an
// absent order id falls back to the (possibly empty) one in the hash key.
func equalIDs(a, b string) bool {
	if a == "" || b == "" {
		return a == b
	}
	return core.SafeEqual(a, b)
}

func threeDSRequestOf(req *core.ThreeDSPaymentRequest) *ThreeDSRequest {
	if req == nil {
		return nil
	}
	return &ThreeDSRequest{ThreeDSPaymentRequest: *req}
}

func invoiceIDOf(req *core.PaymentRequest) string {
	if req != nil && req.ConversationID != "" {
		return req.ConversationID
	}
	return core.GenerateOrderID("")
}

func paidPriceOf(req *core.PaymentRequest) string {
	if req.PaidPrice != "" {
		return req.PaidPrice
	}
	return req.Price
}

func putIfNotEmpty(body Result, key, value string) {
	if value != "" {
		body[key] = value
	}
}

func statusOf(approved bool) core.PaymentStatus {
	if approved {
		return core.StatusSuccess
	}
	return core.StatusFailure
}

func boolToInt(value bool) int {
	if value {
		return 1
	}
	return 0
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if value != "" {
			return value
		}
	}
	return ""
}

// nullish mirrors `a ?? b`: an absent or null field falls back to the next one.
func nullish(value, fallback any) any {
	if value == nil {
		return fallback
	}
	return value
}

// presentText reads a field that may be absent, as `record[key] ?? fallback`.
func presentText(record Result, key string) string {
	if value, present := record[key]; present && value != nil {
		return jsString(value)
	}
	return ""
}

func requestRequired() error {
	return core.NewValidationError("request is required", "", nil)
}

func numberText(value float64) string {
	return strconv.FormatFloat(value, 'f', -1, 64)
}

// formatNumber formats a numeric amount the way the API expects it, rejecting
// values that are not a finite amount.
func formatNumber(value float64) (string, error) {
	if math.IsNaN(value) {
		return "", core.NewValidationError("Invalid amount: NaN", "", nil)
	}
	return FormatAmount(numberText(value))
}

func asResult(value any) Result {
	if record, ok := value.(map[string]any); ok {
		return record
	}
	return nil
}

func asOptions(value any) []Result {
	list, ok := value.([]any)
	if !ok {
		return nil
	}
	options := make([]Result, 0, len(list))
	for _, item := range list {
		options = append(options, asResult(item))
	}
	return options
}

func nested(value any, keys ...string) any {
	current := value
	for _, key := range keys {
		record := asResult(current)
		if record == nil {
			return nil
		}
		current = record[key]
	}
	return current
}

// jsString renders a response field as text, the way String(value) does.
func jsString(value any) string {
	switch v := value.(type) {
	case nil:
		return ""
	case string:
		return v
	case bool:
		if v {
			return "true"
		}
		return "false"
	case float64:
		return numberText(v)
	case int:
		return strconv.Itoa(v)
	case json.Number:
		return v.String()
	default:
		return fmt.Sprint(v)
	}
}

// nonEmpty returns a trimmed field, or "" when it is absent or blank.
func nonEmpty(value any) string {
	if value == nil {
		return ""
	}
	return strings.TrimSpace(jsString(value))
}

// jsNumber mirrors Number(value): the second result is false for NaN, which
// never equals anything.
func jsNumber(value any) (float64, bool) {
	switch v := value.(type) {
	case nil:
		return 0, false
	case float64:
		return v, isFinite(v)
	case int:
		return float64(v), true
	case bool:
		if v {
			return 1, true
		}
		return 0, true
	case json.Number:
		number, err := v.Float64()
		return number, err == nil && isFinite(number)
	case string:
		text := strings.TrimSpace(v)
		if text == "" {
			return 0, true
		}
		number, err := strconv.ParseFloat(text, 64)
		return number, err == nil && isFinite(number)
	default:
		return 0, false
	}
}

func isFinite(value float64) bool {
	return !math.IsNaN(value) && !math.IsInf(value, 0)
}

// isOne reports the flag Sipay sends as 1, "1" or true.
func isOne(value any) bool {
	switch v := value.(type) {
	case bool:
		return v
	case string:
		return v == "1"
	case float64:
		return v == 1
	case int:
		return v == 1
	default:
		return false
	}
}

func isSuccessCode(value any) bool {
	number, ok := jsNumber(value)
	return ok && number == SuccessCode
}

// bankCodeOf finds the bank's ISO 8583 code wherever it sits in a raw response.
func bankCodeOf(raw any) string {
	var record Result
	switch value := raw.(type) {
	case map[string]any:
		record = value
	case map[string]string:
		record = Result{}
		for key, item := range value {
			record[key] = item
		}
	default:
		return ""
	}
	if record == nil {
		return ""
	}
	if code := nonEmpty(record["original_bank_error_code"]); code != "" {
		return code
	}
	if code := bankCodeOf(record["data"]); code != "" {
		return code
	}
	return bankCodeOf(record["callback"])
}

// errorOf returns the errorCode and errorMessage of a failed result: the bank's
// code first, then Sipay's.
func errorOf(data, fallback Result) (string, string) {
	source := data
	if source == nil {
		source = fallback
	}
	if source == nil {
		source = Result{}
	}
	outer := fallback
	if outer == nil {
		outer = Result{}
	}
	errorCode := firstNonEmpty(
		nonEmpty(source["original_bank_error_code"]),
		nonEmpty(source["error_code"]),
		nonEmpty(source["status_code"]),
		nonEmpty(outer["status_code"]),
	)
	errorMessage := firstNonEmpty(
		nonEmpty(source["original_bank_error_description"]),
		nonEmpty(source["error"]),
		nonEmpty(source["bank_status_description"]),
		nonEmpty(source["status_description"]),
		nonEmpty(source["message"]),
		nonEmpty(outer["status_description"]),
		nonEmpty(outer["message"]),
	)
	return errorCode, errorMessage
}
