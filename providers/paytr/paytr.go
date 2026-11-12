// Package paytr implements the PayTR payment gateway: iFrame and Direct API
// payments, non-3D synchronous payments, stored-card payments, refunds,
// cancellations, status queries, BIN and installment-rate lookups.
//
// PayTR delivers the payment result server-to-server to the notification
// (Bildirim) URL configured in the merchant panel. Pass that POST body to
// CompleteThreeDSPayment and answer with plain text "OK".
package paytr

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/boracomet/go-pos/core"
)

// DefaultBaseURL is the PayTR API host; there is no separate sandbox host.
const DefaultBaseURL = "https://www.paytr.com"

const requestTimeout = 30 * time.Second

// preauthNote explains why the pre-authorization family is unavailable.
const preauthNote = "PayTR pre-authorization is not implemented yet"

// ThreeDSMode selects how InitThreeDSPayment takes a new card.
const (
	// ModeIframe shows PayTR's payment page in an iFrame (default).
	ModeIframe = "iframe"
	// ModeDirect posts the card from the merchant's own checkout page. It needs
	// the Direct API enabled on the PayTR account.
	ModeDirect = "direct"
)

// Config holds the PayTR merchant credentials and the shared provider settings.
type Config struct {
	core.ProviderConfig
	MerchantID   string
	MerchantKey  string
	MerchantSalt string
	// TestMode sends test_mode=1. Nil means off.
	TestMode *bool
	// TimeoutLimit is the iFrame timeout in minutes. Defaults to 30.
	TimeoutLimit int
	// ThreeDSMode is ModeIframe (default) or ModeDirect.
	ThreeDSMode string
}

// Provider calls the PayTR API.
type Provider struct {
	core.BaseProvider
	config Config
	client *core.HTTPClient
}

var (
	_ core.Provider                  = (*Provider)(nil)
	_ core.TokenPaymentProvider      = (*Provider)(nil)
	_ core.PlainTextCallbackProvider = (*Provider)(nil)
)

// orderRules are the fields PayTR requires for card and iFrame payments.
var orderRules = core.PaymentValidationRules{
	BasketRequired: true,
	Required:       []string{"buyer.email", "buyer.ip", "buyer.name", "buyer.surname", "buyer.gsmNumber"},
}

var cardPaymentRules = core.PaymentValidationRules{
	Card:           true,
	StoredCard:     true,
	SaveCard:       true,
	BasketRequired: orderRules.BasketRequired,
	Required:       orderRules.Required,
}

var directThreeDSRules = core.PaymentValidationRules{
	Card:           true,
	SaveCard:       true,
	BasketRequired: orderRules.BasketRequired,
	Required:       orderRules.Required,
}

var storedCardThreeDSRules = core.PaymentValidationRules{
	StoredCard:     true,
	BasketRequired: orderRules.BasketRequired,
	Required:       orderRules.Required,
}

// New builds a PayTR provider. MerchantID, MerchantKey and MerchantSalt are required.
func New(cfg Config) (*Provider, error) {
	var missing []string
	if cfg.MerchantID == "" {
		missing = append(missing, "merchantId")
	}
	if cfg.MerchantKey == "" {
		missing = append(missing, "merchantKey")
	}
	if cfg.MerchantSalt == "" {
		missing = append(missing, "merchantSalt")
	}
	if len(missing) > 0 {
		return nil, core.NewConfigurationError("PayTR configuration is missing: "+strings.Join(missing, ", "), "paytr")
	}
	if cfg.BaseURL == "" {
		cfg.BaseURL = DefaultBaseURL
	}
	if cfg.ThreeDSMode != "" && cfg.ThreeDSMode != ModeIframe && cfg.ThreeDSMode != ModeDirect {
		return nil, core.NewConfigurationError("Unknown PayTR threeDSMode: "+cfg.ThreeDSMode, "paytr")
	}

	base := core.NewBaseProvider("paytr", cfg.ProviderConfig)
	base.ErrorCodes = ErrorCodes
	return &Provider{
		BaseProvider: base,
		config:       cfg,
		client: base.NewHTTPClient(requestTimeout, map[string]string{
			"Content-Type": "application/x-www-form-urlencoded",
		}),
	}, nil
}

// CallbackAcknowledgement answers PayTR's server-to-server notification. PayTR
// re-sends it until it receives "OK".
func (p *Provider) CallbackAcknowledgement() core.CallbackAcknowledgement {
	return core.CallbackAcknowledgement{
		Success: "OK",
		Failure: "PAYTR notification failed: bad hash",
	}
}

func (p *Provider) testMode() string {
	if p.config.TestMode != nil && *p.config.TestMode {
		return "1"
	}
	return "0"
}

func (p *Provider) lang() string {
	if strings.HasPrefix(strings.ToLower(p.Config.Locale), "en") {
		return "en"
	}
	return "tr"
}

func (p *Provider) timeoutLimit() string {
	if p.config.TimeoutLimit > 0 {
		return strconv.Itoa(p.config.TimeoutLimit)
	}
	return "30"
}

// post sends one urlencoded call. Only read-only queries are retryable.
func (p *Provider) post(ctx context.Context, path string, form *Form, retryable bool) (*core.Response, error) {
	return p.client.Request(ctx, core.RequestConfig{
		Method:    http.MethodPost,
		URL:       path,
		Data:      form.Encode(),
		Retryable: retryable,
	})
}

func decodeBody(resp *core.Response, out any) {
	if resp == nil {
		return
	}
	_ = json.Unmarshal([]byte(resp.Text), out)
}

func missingRequest() error {
	return core.NewValidationError("request is required", "paytr", []core.ValidationIssue{
		{Path: "request", Message: "is required"},
	})
}

// buildBasket normalizes quantities and renders the basket for one API: the
// iFrame API takes base64 JSON, the Direct API plain JSON.
func (p *Provider) buildBasket(items []BasketItem, iframe bool) (string, error) {
	basket := make([]BasketItem, 0, len(items))
	for _, item := range items {
		quantity := item.Quantity
		if quantity <= 0 {
			quantity = 1
		}
		price, err := FormatAmount(item.Price)
		if err != nil {
			return "", err
		}
		basket = append(basket, BasketItem{Name: item.Name, Price: price, Quantity: quantity})
	}
	if iframe {
		return FormatBasket(basket)
	}
	return FormatDirectBasket(basket)
}

// basketOf converts order lines to PayTR basket items. core.BasketItem carries
// no quantity, so every line is a single unit.
func basketOf(items []core.BasketItem) []BasketItem {
	basket := make([]BasketItem, 0, len(items))
	for _, item := range items {
		basket = append(basket, BasketItem{Name: item.Name, Price: item.Price, Quantity: 1})
	}
	return basket
}

func (p *Provider) resolveMerchantOid(conversationID string) (string, error) {
	merchantOid := conversationID
	if merchantOid == "" {
		merchantOid = core.GenerateOrderID("")
	}
	if err := AssertMerchantOid(merchantOid); err != nil {
		return "", err
	}
	return merchantOid, nil
}

// saveCardRequested reports whether the request asks PayTR to store the card.
// A SaveCardOption is either the boolean form (Enabled) or the object form
// (CustomerToken/Alias); both mean "store this card".
func saveCardRequested(request *core.PaymentRequest) bool {
	if request.SaveCard == nil {
		return false
	}
	return request.SaveCard.Enabled || request.SaveCard.CustomerToken != "" || request.SaveCard.Alias != ""
}

// cardFields renders the Direct API card fields: a stored card's tokens, or the
// card itself. With saveCard, PayTR stores the card (store_card=1) and returns
// the customer's utoken in the notification.
func (p *Provider) cardFields(request *core.PaymentRequest) (*Form, error) {
	fields := &Form{}
	if request.StoredCard != nil {
		fields.Set("utoken", request.StoredCard.CustomerToken).
			Set("ctoken", request.StoredCard.CardToken).
			Set("require_cvv", boolFlag(request.StoredCard.CVC != ""))
		if request.StoredCard.CVC != "" {
			fields.Set("cvv", request.StoredCard.CVC)
		}
		return fields, nil
	}
	card, err := p.CardOf(request)
	if err != nil {
		return nil, err
	}
	fields.Set("cc_owner", card.CardHolderName).
		Set("card_number", card.CardNumber).
		Set("expiry_month", padStart(card.ExpireMonth, 2)).
		Set("expiry_year", lastTwo(card.ExpireYear)).
		Set("cvv", card.CVC)
	if saveCardRequested(request) {
		fields.Set("store_card", "1")
		if request.SaveCard.CustomerToken != "" {
			fields.Set("utoken", request.SaveCard.CustomerToken)
		}
	}
	return fields, nil
}

func boolFlag(value bool) string {
	if value {
		return "1"
	}
	return "0"
}

func padStart(value string, length int) string {
	if len(value) >= length {
		return value
	}
	return strings.Repeat("0", length-len(value)) + value
}

func lastTwo(value string) string {
	if len(value) <= 2 {
		return value
	}
	return value[len(value)-2:]
}

func userAddress(request *core.PaymentRequest) string {
	if request.BillingAddress.Address != "" {
		return request.BillingAddress.Address
	}
	return request.ShippingAddress.Address
}

func installmentCount(installment int) string {
	if installment > 1 {
		return strconv.Itoa(installment)
	}
	return "0"
}

// CreatePayment charges a card without 3D Secure (Direct API, sync_mode=1).
// The PayTR account must be authorized for non-3D payments.
func (p *Provider) CreatePayment(ctx context.Context, req *core.PaymentRequest) (*core.PaymentResponse, error) {
	var merchantOid string
	if req == nil {
		return p.failPayment(missingRequest(), "Payment failed", ""), nil
	}
	if err := p.ValidatePayment(req, cardPaymentRules); err != nil {
		return p.failPayment(err, "Payment failed", ""), nil
	}
	merchantOid, err := p.resolveMerchantOid(req.ConversationID)
	if err != nil {
		return p.failPayment(err, "Payment failed", ""), nil
	}
	paidPrice := req.PaidPrice
	if paidPrice == "" {
		paidPrice = req.Price
	}
	paymentAmount, err := FormatAmount(paidPrice)
	if err != nil {
		return p.failPayment(err, "Payment failed", merchantOid), nil
	}
	currency, err := MapCurrency(req.Currency)
	if err != nil {
		return p.failPayment(err, "Payment failed", merchantOid), nil
	}
	count := installmentCount(req.Installment)

	params := DirectTokenParams{
		MerchantID:       p.config.MerchantID,
		UserIP:           req.Buyer.IP,
		MerchantOid:      merchantOid,
		Email:            req.Buyer.Email,
		PaymentAmount:    paymentAmount,
		PaymentType:      "card",
		InstallmentCount: count,
		Currency:         currency,
		TestMode:         p.testMode(),
		Non3D:            "1",
	}
	token, err := GenerateDirectToken(params, p.config.MerchantSalt, p.config.MerchantKey)
	if err != nil {
		return p.failPayment(err, "Payment failed", merchantOid), nil
	}
	basket, err := p.buildBasket(basketOf(req.BasketItems), false)
	if err != nil {
		return p.failPayment(err, "Payment failed", merchantOid), nil
	}
	cardFields, err := p.cardFields(req)
	if err != nil {
		return p.failPayment(err, "Payment failed", merchantOid), nil
	}

	body := &Form{}
	body.Set("merchant_id", params.MerchantID).
		Set("user_ip", params.UserIP).
		Set("merchant_oid", params.MerchantOid).
		Set("email", params.Email).
		Set("payment_amount", params.PaymentAmount).
		Set("payment_type", params.PaymentType).
		Set("installment_count", params.InstallmentCount).
		Set("currency", params.Currency).
		Set("test_mode", params.TestMode).
		Set("non_3d", params.Non3D).
		Set("paytr_token", token).
		Set("sync_mode", "1").
		Set("debug_on", p.testMode()).
		Set("client_lang", p.lang()).
		Set("user_name", req.Buyer.Name+" "+req.Buyer.Surname).
		Set("user_address", userAddress(req)).
		Set("user_phone", req.Buyer.GSMNumber).
		Set("user_basket", basket)
	for _, field := range cardFields.Fields() {
		body.Set(field.Name, field.Value)
	}

	resp, err := p.post(ctx, "/odeme", body, false)
	if err != nil {
		return p.failPayment(err, "Payment failed", merchantOid), nil
	}

	var data apiDirectPaymentResponse
	decodeBody(resp, &data)
	approved := data.Status == "success"

	out := &core.PaymentResponse{
		PaymentID:      merchantOid,
		ConversationID: merchantOid,
		RawResponse:    resp.Data,
	}
	if approved {
		out.Status = core.StatusSuccess
		out.StoredCard = savedCardOf(stringOf(data.Utoken))
	} else {
		out.Status = core.StatusFailure
		out.ErrorCode = firstNonEmpty(stringOf(data.FailedReasonCode), stringOf(data.ErrNo))
		out.ErrorMessage = firstNonEmpty(data.FailedReasonMsg, data.ErrMsg, data.Msg, data.Reason)
	}
	out.Code = p.ErrorCodeFor(out.Status, out.ErrorCode)
	return out, nil
}

func (p *Provider) failPayment(err error, fallback, merchantOid string) *core.PaymentResponse {
	out := core.ApplyFailure("PayTR", err, fallback, &core.PaymentResponse{})
	out.PaymentID, out.ConversationID = merchantOid, merchantOid
	out.Code = p.ErrorCodeFor(out.Status, out.ErrorCode)
	return out
}

// InitThreeDSPayment starts a payment whose result arrives at the notification
// URL. With ThreeDSMode "direct" the card is posted from the merchant's own
// checkout page; otherwise PayTR's page is shown in an iFrame.
//
// CallbackURL is where the customer's browser is redirected; it does not carry
// the payment result.
func (p *Provider) InitThreeDSPayment(ctx context.Context, req *core.ThreeDSPaymentRequest) (*core.ThreeDSInitResponse, error) {
	return p.InitThreeDSPaymentWithFailURL(ctx, req, "")
}

// InitThreeDSPaymentWithFailURL is InitThreeDSPayment with a separate browser
// redirect for a failed payment. An empty failURL falls back to CallbackURL.
func (p *Provider) InitThreeDSPaymentWithFailURL(ctx context.Context, req *core.ThreeDSPaymentRequest, failURL string) (*core.ThreeDSInitResponse, error) {
	if req == nil {
		return p.failThreeDSInit(missingRequest(), "3DS initialization failed", ""), nil
	}
	if req.StoredCard != nil {
		// Stored cards are charged through the Direct API form, not the iFrame.
		return p.initStoredCardPayment(ctx, req, failURL)
	}
	if p.config.ThreeDSMode == ModeDirect {
		return p.initDirectPayment(ctx, req, failURL)
	}
	return p.initIframePayment(ctx, req, failURL)
}

func (p *Provider) initIframePayment(ctx context.Context, req *core.ThreeDSPaymentRequest, failURL string) (*core.ThreeDSInitResponse, error) {
	var merchantOid string
	if err := p.ValidatePayment(&req.PaymentRequest, orderRules); err != nil {
		return p.failThreeDSInit(err, "3DS initialization failed", ""), nil
	}
	merchantOid, err := p.resolveMerchantOid(req.ConversationID)
	if err != nil {
		return p.failThreeDSInit(err, "3DS initialization failed", ""), nil
	}
	if req.CallbackURL == "" {
		return p.failThreeDSInit(callbackURLRequired(), "3DS initialization failed", merchantOid), nil
	}

	paidPrice := req.PaidPrice
	if paidPrice == "" {
		paidPrice = req.Price
	}
	paymentAmount, err := ConvertToKurus(paidPrice)
	if err != nil {
		return p.failThreeDSInit(err, "3DS initialization failed", merchantOid), nil
	}
	currency, err := MapCurrency(req.Currency)
	if err != nil {
		return p.failThreeDSInit(err, "3DS initialization failed", merchantOid), nil
	}
	userBasket, err := p.buildBasket(basketOf(req.BasketItems), true)
	if err != nil {
		return p.failThreeDSInit(err, "3DS initialization failed", merchantOid), nil
	}
	noInstallment := "0"
	if req.Installment == 1 {
		noInstallment = "1"
	}
	maxInstallment := installmentCount(req.Installment)

	token, err := GenerateIframeToken(IframeTokenParams{
		MerchantID:     p.config.MerchantID,
		UserIP:         req.Buyer.IP,
		MerchantOid:    merchantOid,
		Email:          req.Buyer.Email,
		PaymentAmount:  paymentAmount,
		UserBasket:     userBasket,
		NoInstallment:  noInstallment,
		MaxInstallment: maxInstallment,
		Currency:       currency,
		TestMode:       p.testMode(),
	}, p.config.MerchantSalt, p.config.MerchantKey)
	if err != nil {
		return p.failThreeDSInit(err, "3DS initialization failed", merchantOid), nil
	}

	body := &Form{}
	body.Set("merchant_id", p.config.MerchantID).
		Set("user_ip", req.Buyer.IP).
		Set("merchant_oid", merchantOid).
		Set("email", req.Buyer.Email).
		Set("payment_amount", paymentAmount).
		Set("paytr_token", token).
		Set("user_basket", userBasket).
		Set("debug_on", p.testMode()).
		Set("no_installment", noInstallment).
		Set("max_installment", maxInstallment).
		Set("user_name", req.Buyer.Name+" "+req.Buyer.Surname).
		Set("user_address", userAddress(&req.PaymentRequest)).
		Set("user_phone", req.Buyer.GSMNumber).
		Set("merchant_ok_url", req.CallbackURL).
		Set("merchant_fail_url", firstNonEmpty(failURL, req.CallbackURL)).
		Set("timeout_limit", p.timeoutLimit()).
		Set("currency", currency).
		Set("test_mode", p.testMode()).
		Set("lang", p.lang())

	resp, err := p.post(ctx, "/odeme/api/get-token", body, false)
	if err != nil {
		return p.failThreeDSInit(err, "3DS initialization failed", merchantOid), nil
	}

	var data apiIframeResponse
	decodeBody(resp, &data)
	if data.Status == "success" && data.Token != "" {
		iframeURL := strings.TrimRight(p.Config.BaseURL, "/") + "/odeme/guvenli/" + data.Token
		out := &core.ThreeDSInitResponse{
			Status:             core.StatusPending,
			ThreeDSHTMLContent: BuildIframeHTML(iframeURL),
			RedirectURL:        iframeURL,
			PaymentID:          merchantOid,
			ConversationID:     merchantOid,
			RawResponse:        resp.Data,
		}
		out.Code = p.ErrorCodeFor(out.Status, out.ErrorCode)
		return out, nil
	}

	out := &core.ThreeDSInitResponse{
		Status:         core.StatusFailure,
		PaymentID:      merchantOid,
		ConversationID: merchantOid,
		ErrorMessage:   firstNonEmpty(data.Reason, "Payment initialization failed"),
		RawResponse:    resp.Data,
	}
	out.Code = p.ErrorCodeFor(out.Status, out.ErrorCode)
	return out, nil
}

// initDirectPayment builds the auto-submitting form the customer's browser posts
// to PayTR. PayTR then runs 3D Secure and sends the result to the notification URL.
func (p *Provider) initDirectPayment(ctx context.Context, req *core.ThreeDSPaymentRequest, failURL string) (*core.ThreeDSInitResponse, error) {
	var merchantOid string
	if err := p.ValidatePayment(&req.PaymentRequest, directThreeDSRules); err != nil {
		return p.failThreeDSInit(err, "3DS initialization failed", ""), nil
	}
	merchantOid, err := p.resolveMerchantOid(req.ConversationID)
	if err != nil {
		return p.failThreeDSInit(err, "3DS initialization failed", ""), nil
	}
	if req.CallbackURL == "" {
		return p.failThreeDSInit(callbackURLRequired(), "3DS initialization failed", merchantOid), nil
	}
	paidPrice := req.PaidPrice
	if paidPrice == "" {
		paidPrice = req.Price
	}
	paymentAmount, err := FormatAmount(paidPrice)
	if err != nil {
		return p.failThreeDSInit(err, "3DS initialization failed", merchantOid), nil
	}
	currency, err := MapCurrency(req.Currency)
	if err != nil {
		return p.failThreeDSInit(err, "3DS initialization failed", merchantOid), nil
	}
	count := installmentCount(req.Installment)

	params := DirectTokenParams{
		MerchantID:       p.config.MerchantID,
		UserIP:           req.Buyer.IP,
		MerchantOid:      merchantOid,
		Email:            req.Buyer.Email,
		PaymentAmount:    paymentAmount,
		PaymentType:      "card",
		InstallmentCount: count,
		Currency:         currency,
		TestMode:         p.testMode(),
		Non3D:            "0",
	}
	token, err := GenerateDirectToken(params, p.config.MerchantSalt, p.config.MerchantKey)
	if err != nil {
		return p.failThreeDSInit(err, "3DS initialization failed", merchantOid), nil
	}
	basket, err := p.buildBasket(basketOf(req.BasketItems), false)
	if err != nil {
		return p.failThreeDSInit(err, "3DS initialization failed", merchantOid), nil
	}
	cardFields, err := p.cardFields(&req.PaymentRequest)
	if err != nil {
		return p.failThreeDSInit(err, "3DS initialization failed", merchantOid), nil
	}

	fields := &Form{}
	fields.Set("merchant_id", params.MerchantID).
		Set("user_ip", params.UserIP).
		Set("merchant_oid", params.MerchantOid).
		Set("email", params.Email).
		Set("payment_amount", params.PaymentAmount).
		Set("payment_type", params.PaymentType).
		Set("installment_count", params.InstallmentCount).
		Set("currency", params.Currency).
		Set("test_mode", params.TestMode).
		Set("non_3d", params.Non3D).
		Set("paytr_token", token).
		Set("client_lang", p.lang()).
		Set("debug_on", p.testMode()).
		Set("user_name", req.Buyer.Name+" "+req.Buyer.Surname).
		Set("user_address", userAddress(&req.PaymentRequest)).
		Set("user_phone", req.Buyer.GSMNumber).
		Set("user_basket", basket).
		Set("merchant_ok_url", req.CallbackURL).
		Set("merchant_fail_url", firstNonEmpty(failURL, req.CallbackURL))
	for _, field := range cardFields.Fields() {
		fields.Set(field.Name, field.Value)
	}

	out := &core.ThreeDSInitResponse{
		Status:             core.StatusPending,
		ThreeDSHTMLContent: BuildAutoSubmitForm(strings.TrimRight(p.Config.BaseURL, "/")+"/odeme", fields.Fields()),
		PaymentID:          merchantOid,
		ConversationID:     merchantOid,
	}
	out.Code = p.ErrorCodeFor(out.Status, out.ErrorCode)
	return out, nil
}

func (p *Provider) initStoredCardPayment(ctx context.Context, req *core.ThreeDSPaymentRequest, failURL string) (*core.ThreeDSInitResponse, error) {
	if err := p.ValidatePayment(&req.PaymentRequest, storedCardThreeDSRules); err != nil {
		return p.failThreeDSInit(err, "3DS initialization failed", ""), nil
	}
	stored := req.StoredCard
	paidPrice := req.PaidPrice
	if paidPrice == "" {
		paidPrice = req.Price
	}
	return p.tokenPayment(ctx, &TokenPaymentRequest{
		Utoken:         stored.CustomerToken,
		Ctoken:         stored.CardToken,
		CVV:            stored.CVC,
		Price:          paidPrice,
		CallbackURL:    req.CallbackURL,
		FailURL:        failURL,
		ConversationID: req.ConversationID,
		Buyer: TokenPaymentBuyer{
			Email:     req.Buyer.Email,
			Name:      req.Buyer.Name,
			Surname:   req.Buyer.Surname,
			IP:        req.Buyer.IP,
			GSMNumber: req.Buyer.GSMNumber,
			Address:   userAddress(&req.PaymentRequest),
		},
		BasketItems: basketOf(req.BasketItems),
		Currency:    req.Currency,
		Installment: req.Installment,
	})
}

func (p *Provider) failThreeDSInit(err error, fallback, merchantOid string) *core.ThreeDSInitResponse {
	out := core.ApplyFailure("PayTR", err, fallback, &core.ThreeDSInitResponse{})
	out.PaymentID, out.ConversationID = merchantOid, merchantOid
	out.Code = p.ErrorCodeFor(out.Status, out.ErrorCode)
	return out
}

func callbackURLRequired() error {
	return core.NewValidationError("callbackUrl is required", "paytr", []core.ValidationIssue{
		{Path: "callbackUrl", Message: "is required"},
	})
}

// CompleteThreeDSPayment verifies and maps the notification (Bildirim URL) POST.
//
// The endpoint must answer with plain text "OK", otherwise PayTR keeps re-sending
// the notification. PayTR may deliver the same notification more than once, so
// order updates must be idempotent.
func (p *Provider) CompleteThreeDSPayment(ctx context.Context, callbackData any) (*core.PaymentResponse, error) {
	callback := parseCallback(callbackData)
	valid, err := VerifyCallback(callback, p.config.MerchantSalt, p.config.MerchantKey)
	if err != nil {
		return p.failPayment(err, "Payment failed", callback.MerchantOid), nil
	}
	if !valid {
		out := &core.PaymentResponse{
			Status:         core.StatusFailure,
			PaymentID:      callback.MerchantOid,
			ConversationID: callback.MerchantOid,
			ErrorCode:      "INVALID_HASH",
			ErrorMessage:   "Invalid callback signature",
			RawResponse:    callbackData,
		}
		out.Code = p.ErrorCodeFor(out.Status, out.ErrorCode)
		return out, nil
	}

	approved := callback.Status == "success"
	out := &core.PaymentResponse{
		PaymentID:      callback.MerchantOid,
		ConversationID: callback.MerchantOid,
		RawResponse:    callbackData,
	}
	if approved {
		out.Status = core.StatusSuccess
		out.StoredCard = savedCardOf(callback.Utoken)
	} else {
		out.Status = core.StatusFailure
		out.ErrorCode = callback.FailedReasonCode
		out.ErrorMessage = callback.FailedReasonMsg
	}
	out.Code = p.ErrorCodeFor(out.Status, out.ErrorCode)
	return out, nil
}

// Refund refunds all or part of a payment. PaymentID is the merchant_oid and
// Price is in TL.
func (p *Provider) Refund(ctx context.Context, req *core.RefundRequest) (*core.RefundResponse, error) {
	if req == nil {
		return p.failRefund(missingRequest(), "Refund failed", ""), nil
	}
	if err := p.ValidateRefund(req); err != nil {
		return p.failRefund(err, "Refund failed", req.ConversationID), nil
	}
	returnAmount, err := FormatAmount(req.Price)
	if err != nil {
		return p.failRefund(err, "Refund failed", req.ConversationID), nil
	}
	token, err := GenerateRefundToken(p.config.MerchantID, req.PaymentID, returnAmount, p.config.MerchantSalt, p.config.MerchantKey)
	if err != nil {
		return p.failRefund(err, "Refund failed", req.ConversationID), nil
	}

	body := &Form{}
	body.Set("merchant_id", p.config.MerchantID).
		Set("merchant_oid", req.PaymentID).
		Set("return_amount", returnAmount).
		Set("paytr_token", token)

	resp, err := p.post(ctx, "/odeme/iade", body, false)
	if err != nil {
		return p.failRefund(err, "Refund failed", req.ConversationID), nil
	}

	var data apiRefundResponse
	decodeBody(resp, &data)
	out := &core.RefundResponse{ConversationID: req.ConversationID, RawResponse: resp.Data}
	if data.Status == "success" {
		out.Status = core.StatusSuccess
		out.RefundID = firstNonEmpty(data.ReferenceNo, data.MerchantOid)
	} else {
		out.Status = core.StatusFailure
		out.ErrorCode = stringOf(data.ErrNo)
		out.ErrorMessage = data.ErrMsg
	}
	out.Code = p.ErrorCodeFor(out.Status, out.ErrorCode)
	return out, nil
}

func (p *Provider) failRefund(err error, fallback, conversationID string) *core.RefundResponse {
	out := core.ApplyFailure("PayTR", err, fallback, &core.RefundResponse{})
	out.ConversationID = conversationID
	out.Code = p.ErrorCodeFor(out.Status, out.ErrorCode)
	return out
}

// Cancel refunds the payment in full: PayTR has no void endpoint. Without Price
// the paid amount is read with a status query.
func (p *Provider) Cancel(ctx context.Context, req *core.CancelRequest) (*core.CancelResponse, error) {
	if req == nil {
		return p.failCancel(missingRequest(), "Cancel failed", ""), nil
	}
	amount := req.Price
	if amount == "" {
		resp, err := p.queryStatus(ctx, req.PaymentID)
		if err != nil {
			return p.failCancel(err, "Cancel failed", req.ConversationID), nil
		}
		var status apiStatusResponse
		decodeBody(resp, &status)
		paymentAmount := stringOf(status.PaymentAmount)
		if status.Status != "success" || paymentAmount == "" {
			out := &core.CancelResponse{
				Status:         core.StatusFailure,
				ConversationID: req.ConversationID,
				ErrorMessage: firstNonEmpty(status.ErrMsg,
					"Could not determine payment amount for cancellation; pass price"),
				RawResponse: resp.Data,
			}
			out.Code = p.ErrorCodeFor(out.Status, out.ErrorCode)
			return out, nil
		}
		amount = strings.Replace(paymentAmount, ",", ".", 1)
	}

	refund, err := p.Refund(ctx, &core.RefundRequest{
		PaymentID:      req.PaymentID,
		Price:          amount,
		Currency:       firstNonEmpty(req.Currency, "TRY"),
		IP:             req.IP,
		ConversationID: req.ConversationID,
	})
	if err != nil {
		return p.failCancel(err, "Cancel failed", req.ConversationID), nil
	}

	out := &core.CancelResponse{
		Status:         refund.Status,
		TransactionID:  refund.RefundID,
		ConversationID: refund.ConversationID,
		ErrorCode:      refund.ErrorCode,
		ErrorMessage:   refund.ErrorMessage,
		RawResponse:    refund.RawResponse,
	}
	out.Code = p.ErrorCodeFor(out.Status, out.ErrorCode)
	return out, nil
}

func (p *Provider) failCancel(err error, fallback, conversationID string) *core.CancelResponse {
	out := core.ApplyFailure("PayTR", err, fallback, &core.CancelResponse{})
	out.ConversationID = conversationID
	out.Code = p.ErrorCodeFor(out.Status, out.ErrorCode)
	return out
}

// GetPayment queries a payment (/odeme/durum-sorgu). paymentID is the merchant_oid.
// A fully refunded payment is reported as cancelled.
func (p *Provider) GetPayment(ctx context.Context, paymentID string) (*core.PaymentResponse, error) {
	resp, err := p.queryStatus(ctx, paymentID)
	if err != nil {
		return p.failPayment(err, "Get payment failed", paymentID), nil
	}

	var data apiStatusResponse
	decodeBody(resp, &data)
	if data.Status != "success" {
		out := &core.PaymentResponse{
			Status:         core.StatusFailure,
			PaymentID:      paymentID,
			ConversationID: paymentID,
			ErrorCode:      stringOf(data.ErrNo),
			ErrorMessage:   data.ErrMsg,
			RawResponse:    resp.Data,
		}
		out.Code = p.ErrorCodeFor(out.Status, out.ErrorCode)
		return out, nil
	}

	paid := decimalNumber(firstPresent(data.PaymentAmount, data.PaymentTotal))
	var refunded float64
	for _, entry := range data.Returns {
		refunded += decimalNumber(firstPresent(entry.ReturnAmount, entry.RefundAmount))
	}
	status := core.StatusSuccess
	if paid > 0 && refunded >= paid {
		status = core.StatusCancelled
	}

	out := &core.PaymentResponse{
		Status:         status,
		PaymentID:      paymentID,
		ConversationID: paymentID,
		RawResponse:    resp.Data,
	}
	out.Code = p.ErrorCodeFor(out.Status, out.ErrorCode)
	return out, nil
}

func (p *Provider) queryStatus(ctx context.Context, merchantOid string) (*core.Response, error) {
	token, err := GenerateStatusToken(p.config.MerchantID, merchantOid, p.config.MerchantSalt, p.config.MerchantKey)
	if err != nil {
		return nil, err
	}
	body := &Form{}
	body.Set("merchant_id", p.config.MerchantID).
		Set("merchant_oid", merchantOid).
		Set("paytr_token", token)
	return p.post(ctx, "/odeme/durum-sorgu", body, true)
}

// SaveCard is not supported: PayTR stores cards only during a Direct API payment.
func (p *Provider) SaveCard(context.Context, *core.SaveCardRequest) (*core.SaveCardResponse, error) {
	return nil, core.NotSupported("paytr", "Saving a card without a payment",
		"PayTR stores cards during a Direct API payment; pass saveCard to CreatePayment()")
}

// ListCards returns a customer's saved cards (Kart Saklama, /odeme/capi/list).
func (p *Provider) ListCards(ctx context.Context, req *core.ListCardsRequest) (*core.ListCardsResponse, error) {
	if req == nil {
		return p.failListCards(missingRequest()), nil
	}
	token, err := GenerateCardListToken(req.CustomerToken, p.config.MerchantSalt, p.config.MerchantKey)
	if err != nil {
		return p.failListCards(err), nil
	}
	body := &Form{}
	body.Set("merchant_id", p.config.MerchantID).
		Set("utoken", req.CustomerToken).
		Set("paytr_token", token)

	resp, err := p.post(ctx, "/odeme/capi/list", body, true)
	if err != nil {
		return p.failListCards(err), nil
	}

	out := &core.ListCardsResponse{CustomerToken: req.CustomerToken, RawResponse: resp.Data}
	if list, ok := resp.Data.([]any); ok {
		out.Status = core.StatusSuccess
		out.Cards = make([]core.StoredCard, 0, len(list))
		for _, raw := range list {
			if fields, ok := raw.(map[string]any); ok {
				out.Cards = append(out.Cards, storedCardOf(fields))
			}
		}
	} else {
		fields, _ := resp.Data.(map[string]any)
		out.Status = core.StatusFailure
		out.Cards = []core.StoredCard{}
		out.ErrorCode = stringOf(fields["err_no"])
		out.ErrorMessage = firstNonEmpty(stringOf(fields["err_msg"]), stringOf(fields["reason"]), "Could not list stored cards")
	}
	out.Code = p.ErrorCodeFor(out.Status, out.ErrorCode)
	return out, nil
}

func (p *Provider) failListCards(err error) *core.ListCardsResponse {
	out := core.ApplyFailure("PayTR", err, "Listing cards failed", &core.ListCardsResponse{})
	out.Cards = []core.StoredCard{}
	out.Code = p.ErrorCodeFor(out.Status, out.ErrorCode)
	return out
}

// DeleteCard removes a saved card (/odeme/capi/delete).
func (p *Provider) DeleteCard(ctx context.Context, req *core.DeleteCardRequest) (*core.DeleteCardResponse, error) {
	if req == nil {
		return p.failDeleteCard(missingRequest()), nil
	}
	token, err := GenerateCardDeleteToken(req.CardToken, req.CustomerToken, p.config.MerchantSalt, p.config.MerchantKey)
	if err != nil {
		return p.failDeleteCard(err), nil
	}
	body := &Form{}
	body.Set("merchant_id", p.config.MerchantID).
		Set("ctoken", req.CardToken).
		Set("utoken", req.CustomerToken).
		Set("paytr_token", token)

	resp, err := p.post(ctx, "/odeme/capi/delete", body, false)
	if err != nil {
		return p.failDeleteCard(err), nil
	}

	fields, _ := resp.Data.(map[string]any)
	ok := stringOf(fields["status"]) == "success"
	out := &core.DeleteCardResponse{RawResponse: resp.Data}
	if ok {
		out.Status = core.StatusSuccess
	} else {
		out.Status = core.StatusFailure
		out.ErrorCode = stringOf(fields["err_no"])
		out.ErrorMessage = firstNonEmpty(stringOf(fields["err_msg"]), "Could not delete the card")
	}
	out.Code = p.ErrorCodeFor(out.Status, out.ErrorCode)
	return out, nil
}

func (p *Provider) failDeleteCard(err error) *core.DeleteCardResponse {
	out := core.ApplyFailure("PayTR", err, "Deleting the card failed", &core.DeleteCardResponse{})
	out.Code = p.ErrorCodeFor(out.Status, out.ErrorCode)
	return out
}

// CreatePaymentWithToken starts a payment with a stored card (utoken + ctoken).
// It returns an auto-submitting form the customer's browser posts to the Direct
// API; the result arrives at the notification URL like any other payment.
//
// request is a *TokenPaymentRequest or the parsed JSON body of an HTTP request.
func (p *Provider) CreatePaymentWithToken(ctx context.Context, request any) (*core.ThreeDSInitResponse, error) {
	req, err := tokenRequestOf(request)
	if err != nil {
		return p.failThreeDSInit(err, "Token payment failed", ""), nil
	}
	return p.tokenPayment(ctx, req)
}

func (p *Provider) tokenPayment(ctx context.Context, req *TokenPaymentRequest) (*core.ThreeDSInitResponse, error) {
	merchantOid, err := p.resolveMerchantOid(req.ConversationID)
	if err != nil {
		return p.failThreeDSInit(err, "Token payment failed", ""), nil
	}
	paymentAmount, err := FormatAmount(req.Price)
	if err != nil {
		return p.failThreeDSInit(err, "Token payment failed", merchantOid), nil
	}
	currency, err := MapCurrency(req.Currency)
	if err != nil {
		return p.failThreeDSInit(err, "Token payment failed", merchantOid), nil
	}
	count := installmentCount(req.Installment)

	params := DirectTokenParams{
		MerchantID:       p.config.MerchantID,
		UserIP:           req.Buyer.IP,
		MerchantOid:      merchantOid,
		Email:            req.Buyer.Email,
		PaymentAmount:    paymentAmount,
		PaymentType:      "card",
		InstallmentCount: count,
		Currency:         currency,
		TestMode:         p.testMode(),
		Non3D:            "0",
	}
	token, err := GenerateDirectToken(params, p.config.MerchantSalt, p.config.MerchantKey)
	if err != nil {
		return p.failThreeDSInit(err, "Token payment failed", merchantOid), nil
	}
	userBasket, err := p.buildBasket(req.BasketItems, false)
	if err != nil {
		return p.failThreeDSInit(err, "Token payment failed", merchantOid), nil
	}

	fields := &Form{}
	fields.Set("merchant_id", params.MerchantID).
		Set("user_ip", params.UserIP).
		Set("merchant_oid", params.MerchantOid).
		Set("email", params.Email).
		Set("payment_amount", params.PaymentAmount).
		Set("payment_type", params.PaymentType).
		Set("installment_count", params.InstallmentCount).
		Set("currency", params.Currency).
		Set("test_mode", params.TestMode).
		Set("non_3d", params.Non3D).
		Set("paytr_token", token).
		Set("utoken", req.Utoken).
		Set("ctoken", req.Ctoken).
		Set("require_cvv", boolFlag(req.CVV != ""))
	if req.CVV != "" {
		fields.Set("cvv", req.CVV)
	}
	fields.Set("client_lang", p.lang()).
		Set("debug_on", p.testMode()).
		Set("user_name", req.Buyer.Name+" "+req.Buyer.Surname).
		Set("user_address", req.Buyer.Address).
		Set("user_phone", req.Buyer.GSMNumber).
		Set("user_basket", userBasket).
		Set("merchant_ok_url", req.CallbackURL).
		Set("merchant_fail_url", firstNonEmpty(req.FailURL, req.CallbackURL))

	out := &core.ThreeDSInitResponse{
		Status:             core.StatusPending,
		ThreeDSHTMLContent: BuildAutoSubmitForm(strings.TrimRight(p.Config.BaseURL, "/")+"/odeme", fields.Fields()),
		PaymentID:          merchantOid,
		ConversationID:     merchantOid,
	}
	out.Code = p.ErrorCodeFor(out.Status, out.ErrorCode)
	return out, nil
}

func tokenRequestOf(request any) (*TokenPaymentRequest, error) {
	switch r := request.(type) {
	case nil:
		return nil, missingRequest()
	case *TokenPaymentRequest:
		if r == nil {
			return nil, missingRequest()
		}
		return r, nil
	case TokenPaymentRequest:
		return &r, nil
	case string:
		return decodeTokenRequest([]byte(r))
	case []byte:
		return decodeTokenRequest(r)
	default:
		raw, err := json.Marshal(request)
		if err != nil {
			return nil, err
		}
		return decodeTokenRequest(raw)
	}
}

func decodeTokenRequest(raw []byte) (*TokenPaymentRequest, error) {
	var out TokenPaymentRequest
	if err := json.Unmarshal(raw, &out); err != nil {
		return nil, core.NewValidationError("Invalid token payment request: "+err.Error(), "paytr", nil)
	}
	return &out, nil
}

// Authorize is not supported: PayTR has no pre-authorization.
func (p *Provider) Authorize(context.Context, *core.PaymentRequest) (*core.PaymentResponse, error) {
	return nil, core.NotSupported("paytr", "Pre-authorization", preauthNote)
}

// InitThreeDSAuthorize is not supported: PayTR has no pre-authorization.
func (p *Provider) InitThreeDSAuthorize(context.Context, *core.ThreeDSPaymentRequest) (*core.ThreeDSInitResponse, error) {
	return nil, core.NotSupported("paytr", "Pre-authorization", preauthNote)
}

// Capture is not supported: PayTR has no pre-authorization.
func (p *Provider) Capture(context.Context, *core.CaptureRequest) (*core.PaymentResponse, error) {
	return nil, core.NotSupported("paytr", "Capture", preauthNote)
}

// VoidAuthorization is not supported: PayTR has no pre-authorization.
func (p *Provider) VoidAuthorization(context.Context, *core.VoidAuthorizationRequest) (*core.CancelResponse, error) {
	return nil, core.NotSupported("paytr", "Voiding a pre-authorization", preauthNote)
}

// BinCheck looks up a BIN (/odeme/api/bin-detail). It returns an error when the
// lookup fails.
func (p *Provider) BinCheck(ctx context.Context, binNumber string) (*core.BinCheckResponse, error) {
	token, err := GenerateBinToken(binNumber, p.config.MerchantID, p.config.MerchantSalt, p.config.MerchantKey)
	if err != nil {
		return nil, err
	}
	body := &Form{}
	body.Set("merchant_id", p.config.MerchantID).
		Set("bin_number", binNumber).
		Set("paytr_token", token)

	resp, err := p.post(ctx, "/odeme/api/bin-detail", body, true)
	if err != nil {
		return nil, err
	}

	var data apiBinDetailResponse
	decodeBody(resp, &data)
	if data.Status != "success" {
		message := data.ErrMsg
		if message == "" {
			message = fmt.Sprintf("BIN check failed (%s)", data.Status)
		}
		return nil, core.NewError(message, "PROVIDER_ERROR")
	}

	return &core.BinCheckResponse{
		BinNumber:       binNumber,
		CardType:        data.CardType,
		CardAssociation: data.Schema,
		CardFamily:      data.Brand,
		BankName:        data.Bank,
		BankCode:        int(numberOf(data.BankCode)),
		Commercial:      data.BusinessCard == "y" || data.BusinessCard == "1",
		RawResponse:     resp.Data,
	}, nil
}

// InstallmentInfo computes installment options from the commission rates defined
// on the PayTR account (/odeme/taksit-oranlari) and the card family (BIN query).
func (p *Provider) InstallmentInfo(ctx context.Context, req *core.InstallmentInfoRequest) (*core.InstallmentInfoResponse, error) {
	if req == nil {
		return p.failInstallmentInfo(missingRequest()), nil
	}
	price, err := core.ParseAmount(req.Price, "price")
	if err != nil {
		return p.failInstallmentInfo(err), nil
	}
	bin, err := p.BinCheck(ctx, req.BinNumber)
	if err != nil {
		return p.failInstallmentInfo(err), nil
	}
	requestID, err := core.RandomHex(8)
	if err != nil {
		return p.failInstallmentInfo(err), nil
	}
	token, err := GenerateInstallmentRatesToken(p.config.MerchantID, requestID, p.config.MerchantSalt, p.config.MerchantKey)
	if err != nil {
		return p.failInstallmentInfo(err), nil
	}
	body := &Form{}
	body.Set("merchant_id", p.config.MerchantID).
		Set("request_id", requestID).
		Set("paytr_token", token)

	resp, err := p.post(ctx, "/odeme/taksit-oranlari", body, true)
	if err != nil {
		return p.failInstallmentInfo(err), nil
	}

	var rates apiInstallmentRatesResponse
	decodeBody(resp, &rates)
	if rates.Status != "success" {
		out := &core.InstallmentInfoResponse{
			Status:       core.StatusFailure,
			ErrorMessage: firstNonEmpty(rates.ErrMsg, "Installment rates query failed"),
			RawResponse:  resp.Data,
		}
		out.Code = p.ErrorCodeFor(out.Status, out.ErrorCode)
		return out, nil
	}

	familyRates := rates.Oranlar[strings.ToLower(bin.CardFamily)]
	prices := []core.InstallmentDetail{
		{InstallmentNumber: 1, TotalPrice: round2(price), InstallmentPrice: round2(price)},
	}
	for key, rate := range familyRates {
		count, ok := leadingInt(strings.Replace(key, "taksit_", "", 1))
		if !ok || count < 2 {
			continue
		}
		percent := numberOf(rate)
		if math.IsInf(percent, 0) || percent < 0 {
			continue
		}
		total := price * (1 + percent/100)
		prices = append(prices, core.InstallmentDetail{
			InstallmentNumber: int(count),
			TotalPrice:        round2(total),
			InstallmentPrice:  round2(total / float64(count)),
		})
	}
	sort.SliceStable(prices, func(i, j int) bool { return prices[i].InstallmentNumber < prices[j].InstallmentNumber })

	commercial := 0
	if bin.Commercial {
		commercial = 1
	}
	out := &core.InstallmentInfoResponse{
		Status: core.StatusSuccess,
		InstallmentDetails: []core.InstallmentPrice{{
			BinNumber:         req.BinNumber,
			Price:             price,
			CardType:          bin.CardType,
			CardAssociation:   bin.CardAssociation,
			CardFamilyName:    bin.CardFamily,
			BankCode:          bin.BankCode,
			BankName:          bin.BankName,
			Commercial:        commercial,
			InstallmentPrices: prices,
		}},
		ConversationID: req.ConversationID,
		RawResponse:    map[string]any{"bin": bin.RawResponse, "rates": resp.Data},
	}
	out.Code = p.ErrorCodeFor(out.Status, out.ErrorCode)
	return out, nil
}

func (p *Provider) failInstallmentInfo(err error) *core.InstallmentInfoResponse {
	out := core.ApplyFailure("PayTR", err, "Installment info failed", &core.InstallmentInfoResponse{})
	out.Code = p.ErrorCodeFor(out.Status, out.ErrorCode)
	return out
}

func round2(value float64) float64 { return math.Round(value*100) / 100 }

func savedCardOf(utoken string) *core.SavedCardTokens {
	if utoken == "" {
		return nil
	}
	return &core.SavedCardTokens{CustomerToken: utoken}
}

func storedCardOf(raw map[string]any) core.StoredCard {
	str := func(key string) string { return stringOf(raw[key]) }
	requiresCVC := str("require_cvv") == "1"
	return core.StoredCard{
		CardToken:       str("ctoken"),
		LastFourDigits:  str("last_4"),
		ExpireMonth:     str("month"),
		ExpireYear:      str("year"),
		BankName:        str("c_bank"),
		CardFamily:      str("c_brand"),
		CardType:        str("c_type"),
		CardAssociation: str("schema"),
		RequiresCVC:     &requiresCVC,
	}
}

// firstNonEmpty returns the first non-empty value, mirroring a chain of `||`.
func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if value != "" {
			return value
		}
	}
	return ""
}

// firstPresent returns the first value that is not null, mirroring `??`.
func firstPresent(values ...any) any {
	for _, value := range values {
		if value != nil {
			return value
		}
	}
	return nil
}

// stringOf renders a JSON value as a string; absent and null become "".
func stringOf(value any) string {
	switch v := value.(type) {
	case nil:
		return ""
	case string:
		return v
	case bool:
		return strconv.FormatBool(v)
	case float64:
		if v == math.Trunc(v) && math.Abs(v) < 1e15 {
			return strconv.FormatInt(int64(v), 10)
		}
		return strconv.FormatFloat(v, 'f', -1, 64)
	case json.Number:
		return v.String()
	default:
		return fmt.Sprint(v)
	}
}

// numberOf mirrors Number(value) || 0: an unparseable value becomes 0.
func numberOf(value any) float64 {
	switch v := value.(type) {
	case float64:
		if math.IsNaN(v) {
			return 0
		}
		return v
	case string:
		parsed, err := strconv.ParseFloat(strings.TrimSpace(v), 64)
		if err != nil || math.IsNaN(parsed) {
			return 0
		}
		return parsed
	default:
		return 0
	}
}

// decimalNumber parses a status-query amount, which may use a decimal comma
// ("1,16"). Only the first comma is a separator.
func decimalNumber(value any) float64 {
	return numberOf(strings.Replace(stringOf(value), ",", ".", 1))
}

// parseCallback accepts the notification as a struct, a form or a decoded JSON body.
func parseCallback(data any) CallbackData {
	switch d := data.(type) {
	case nil:
		return CallbackData{}
	case CallbackData:
		return d
	case *CallbackData:
		if d == nil {
			return CallbackData{}
		}
		return *d
	case url.Values:
		fields := make(map[string]any, len(d))
		for key, values := range d {
			if len(values) > 0 {
				fields[key] = values[0]
			}
		}
		return callbackFromFields(fields)
	case map[string]string:
		fields := make(map[string]any, len(d))
		for key, value := range d {
			fields[key] = value
		}
		return callbackFromFields(fields)
	case map[string]any:
		return callbackFromFields(d)
	case string:
		return callbackFromJSON([]byte(d))
	case []byte:
		return callbackFromJSON(d)
	default:
		raw, err := json.Marshal(data)
		if err != nil {
			return CallbackData{}
		}
		return callbackFromJSON(raw)
	}
}

func callbackFromJSON(raw []byte) CallbackData {
	var fields map[string]any
	if err := json.Unmarshal(raw, &fields); err != nil {
		return CallbackData{}
	}
	return callbackFromFields(fields)
}

func callbackFromFields(fields map[string]any) CallbackData {
	str := func(key string) string { return stringOf(fields[key]) }
	return CallbackData{
		MerchantOid:      str("merchant_oid"),
		Status:           str("status"),
		TotalAmount:      str("total_amount"),
		Hash:             str("hash"),
		FailedReasonCode: str("failed_reason_code"),
		FailedReasonMsg:  str("failed_reason_msg"),
		TestMode:         str("test_mode"),
		PaymentType:      str("payment_type"),
		Currency:         str("currency"),
		PaymentAmount:    str("payment_amount"),
		InstallmentCount: str("installment_count"),
		MerchantID:       str("merchant_id"),
		Utoken:           str("utoken"),
	}
}
