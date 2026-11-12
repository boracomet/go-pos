package denizbank

import (
	"context"
	"net/url"
	"strings"
	"time"

	"github.com/boracomet/go-pos/core"
)

// Provider is the Denizbank virtual POS (InterVPos), 3D Model.
//
// CreatePayment sends Auth (NonSecure); InitThreeDSPayment returns an
// auto-submitting form to InterVPos (SecureType 3DModel);
// CompleteThreeDSPayment verifies the callback HASH and charges with Auth
// carrying the 3D Secure data, so the bank's answer decides. Refund and Cancel
// use Refund and Void. GetPayment is not supported.
//
// PaymentID is your order id: the ConversationID you pass, or a generated one.
type Provider struct {
	core.BaseProvider
	cfg    Config
	client *core.HTTPClient
}

var _ core.Provider = (*Provider)(nil)

// New builds the provider, validating the credentials, the base URL and the 3D model.
func New(cfg Config) (*Provider, error) {
	var missing []string
	if cfg.ShopCode == "" {
		missing = append(missing, "shopCode")
	}
	if cfg.UserCode == "" {
		missing = append(missing, "userCode")
	}
	if cfg.UserPass == "" {
		missing = append(missing, "userPass")
	}
	if cfg.MerchantPass == "" {
		missing = append(missing, "merchantPass")
	}
	if len(missing) > 0 {
		return nil, core.NewConfigurationError(
			"Denizbank configuration is missing: "+strings.Join(missing, ", "), "denizbank")
	}
	if cfg.BaseURL == "" {
		return nil, core.NewConfigurationError("Denizbank baseUrl is required", "denizbank")
	}
	if cfg.SecureType != "" && cfg.SecureType != "3DModel" {
		return nil, core.NewConfigurationError(
			"Denizbank secureType "+cfg.SecureType+" is not supported; only the 3D Model is", "denizbank")
	}

	p := &Provider{BaseProvider: core.NewBaseProvider("denizbank", cfg.ProviderConfig), cfg: cfg}
	p.ErrorCodes = errorCodeTable()
	p.client = p.NewHTTPClient(30*time.Second, map[string]string{
		"Content-Type": "application/x-www-form-urlencoded",
	})
	return p, nil
}

func (p *Provider) lang() string {
	locale := p.Config.Locale
	if locale == "" {
		locale = "tr"
	}
	if strings.HasPrefix(strings.ToLower(locale), "en") {
		return "EN"
	}
	return "TR"
}

func (p *Provider) api(ctx context.Context, fields Fields) (map[string]string, error) {
	request := Fields{
		{Key: "ShopCode", Value: p.cfg.ShopCode},
		{Key: "UserCode", Value: p.cfg.UserCode},
		{Key: "UserPass", Value: p.cfg.UserPass},
	}
	request = append(request, fields...)
	request = append(request, Field{Key: "Lang", Value: p.lang()})

	response, err := p.client.Post(ctx, Endpoint, encodeForm(request), core.RequestConfig{ResponseType: "text"})
	if err != nil {
		return nil, err
	}
	text, _ := response.Data.(string)
	return ParseResponse(text)
}

// CreatePayment charges the card without 3D Secure (Auth, NonSecure).
func (p *Provider) CreatePayment(ctx context.Context, req *core.PaymentRequest) (*core.PaymentResponse, error) {
	orderID := orderIDOf(req)
	res, err := p.createPayment(ctx, req, orderID)
	if err != nil {
		res = core.ApplyFailure("denizbank", err, "Payment failed", &core.PaymentResponse{
			PaymentID: orderID, ConversationID: orderID,
		})
	}
	res.Code = p.ErrorCodeFor(res.Status, res.ErrorCode)
	return res, nil
}

func (p *Provider) createPayment(ctx context.Context, req *core.PaymentRequest, orderID string) (*core.PaymentResponse, error) {
	if err := p.ValidatePayment(req, core.PaymentValidationRules{Card: true}); err != nil {
		return nil, err
	}
	card, err := p.CardOf(req)
	if err != nil {
		return nil, err
	}
	cardNumber := stripWhitespace(card.CardNumber)
	amount, err := FormatAmount(paidPriceOf(req))
	if err != nil {
		return nil, err
	}
	currency, err := CurrencyCode(req.Currency)
	if err != nil {
		return nil, err
	}
	installment, err := FormatInstallment(req.Installment)
	if err != nil {
		return nil, err
	}
	expiry, err := FormatExpiry(card.ExpireMonth, card.ExpireYear)
	if err != nil {
		return nil, err
	}

	fields, err := p.api(ctx, Fields{
		{Key: "TxnType", Value: "Auth"},
		{Key: "SecureType", Value: "NonSecure"},
		{Key: "OrderId", Value: orderID},
		{Key: "PurchAmount", Value: amount},
		{Key: "Currency", Value: currency},
		{Key: "InstallmentCount", Value: installment},
		{Key: "CardType", Value: CardType(cardNumber)},
		{Key: "Pan", Value: cardNumber},
		{Key: "Expiry", Value: expiry},
		{Key: "Cvv2", Value: card.CVC},
		{Key: "MOTO", Value: "0"},
	})
	if err != nil {
		return nil, err
	}

	res := &core.PaymentResponse{PaymentID: orderID, ConversationID: orderID, RawResponse: fields}
	if IsApproved(fields) {
		res.Status = core.StatusSuccess
	} else {
		res.Status = core.StatusFailure
		res.ErrorCode, res.ErrorMessage = errorOf(fields)
	}
	return res, nil
}

// InitThreeDSPayment returns an auto-submitting form to InterVPos (3D Model),
// signed with the store key hash. The API password is not put in the form.
func (p *Provider) InitThreeDSPayment(ctx context.Context, req *core.ThreeDSPaymentRequest) (*core.ThreeDSInitResponse, error) {
	orderID := threeDSOrderIDOf(req)
	res, err := p.initThreeDSPayment(req, orderID)
	if err != nil {
		res = core.ApplyFailure("denizbank", err, "3DS initialization failed", &core.ThreeDSInitResponse{
			PaymentID: orderID, ConversationID: orderID,
		})
	}
	res.Code = p.ErrorCodeFor(res.Status, res.ErrorCode)
	return res, nil
}

func (p *Provider) initThreeDSPayment(req *core.ThreeDSPaymentRequest, orderID string) (*core.ThreeDSInitResponse, error) {
	if req == nil {
		return nil, errNilRequest("denizbank")
	}
	if err := p.ValidatePayment(&req.PaymentRequest, core.PaymentValidationRules{Card: true}); err != nil {
		return nil, err
	}
	card, err := p.CardOf(&req.PaymentRequest)
	if err != nil {
		return nil, err
	}
	if req.CallbackURL == "" {
		return nil, core.NewValidationError("callbackUrl is required for 3D Secure payments", "denizbank", nil)
	}
	cardNumber := stripWhitespace(card.CardNumber)
	amount, err := FormatAmount(paidPriceOf(&req.PaymentRequest))
	if err != nil {
		return nil, err
	}
	rnd, err := core.RandomHex(16)
	if err != nil {
		return nil, err
	}
	currency, err := CurrencyCode(req.Currency)
	if err != nil {
		return nil, err
	}
	installment, err := FormatInstallment(req.Installment)
	if err != nil {
		return nil, err
	}
	expiry, err := FormatExpiry(card.ExpireMonth, card.ExpireYear)
	if err != nil {
		return nil, err
	}

	fields := Fields{
		{Key: "Version3D", Value: "2.0"},
		{Key: "ShopCode", Value: p.cfg.ShopCode},
		{Key: "TxnType", Value: "Auth"},
		{Key: "SecureType", Value: "3DModel"},
		{Key: "PurchAmount", Value: amount},
		{Key: "OrderId", Value: orderID},
		{Key: "OkUrl", Value: req.CallbackURL},
		{Key: "FailUrl", Value: req.CallbackURL},
		{Key: "Rnd", Value: rnd},
		{Key: "Lang", Value: p.lang()},
		{Key: "Currency", Value: currency},
		{Key: "InstallmentCount", Value: installment},
		{Key: "CardType", Value: CardType(cardNumber)},
		{Key: "Pan", Value: cardNumber},
		{Key: "Expiry", Value: expiry},
		{Key: "Cvv2", Value: card.CVC},
	}
	fields = append(fields, Field{Key: "Hash", Value: Create3DHash(HashFields{
		ShopCode:     p.cfg.ShopCode,
		OrderID:      orderID,
		Amount:       amount,
		OkURL:        req.CallbackURL,
		FailURL:      req.CallbackURL,
		TxnType:      "Auth",
		Installment:  installment,
		Rnd:          rnd,
		MerchantPass: p.cfg.MerchantPass,
	})})

	action := p.cfg.BaseURL + Endpoint
	html := `<!DOCTYPE html><html><head><meta charset="UTF-8"></head><body>` +
		`<form id="denizbank-3d-form" method="POST" action="` + escapeHTML(action) + `">` + hiddenInputs(fields) + `</form>` +
		`<script>document.getElementById("denizbank-3d-form").submit();</script>` +
		`</body></html>`

	return &core.ThreeDSInitResponse{
		Status:             core.StatusPending,
		ThreeDSHTMLContent: html,
		PaymentID:          orderID,
		ConversationID:     orderID,
	}, nil
}

// CompleteThreeDSPayment finishes the payment after the OkUrl / FailUrl POST.
//
// The HASH is rebuilt from the fields named in HASHPARAMS and must cover
// OrderId. Its values are joined without a separator, so a valid hash alone does
// not pin each value: the card is charged with Auth carrying the callback's 3D
// Secure data (MD, PayerTxnId, Eci, PayerAuthenticationCode) and the bank's
// answer decides. A callback with an invalid hash or for another shop is
// rejected as INVALID_HASH.
//
// PaymentID and ConversationID are set only for a payment the bank approved, and
// on a timeout after the bank was reached.
func (p *Provider) CompleteThreeDSPayment(ctx context.Context, callbackData any) (*core.PaymentResponse, error) {
	data := callbackFields(callbackData)
	res, reached, err := p.completeThreeDSPayment(ctx, data)
	if err != nil {
		res = core.ApplyFailure("denizbank", err, "3D Secure completion failed", &core.PaymentResponse{})
		if reached && res.Status == core.StatusPending {
			res.PaymentID, res.ConversationID = data["OrderId"], data["OrderId"]
		}
	}
	res.Code = p.ErrorCodeFor(res.Status, res.ErrorCode)
	return res, nil
}

func (p *Provider) completeThreeDSPayment(ctx context.Context, data map[string]string) (*core.PaymentResponse, bool, error) {
	untrusted := func(message string) *core.PaymentResponse {
		return &core.PaymentResponse{
			Status: core.StatusFailure, ErrorCode: "INVALID_HASH", ErrorMessage: message, RawResponse: data,
		}
	}

	if !VerifyCallback(data, p.cfg.MerchantPass) {
		return untrusted("Invalid 3D Secure callback hash"), false, nil
	}
	orderID := data["OrderId"]
	if shopCode, sent := data["ShopCode"]; sent && shopCode != p.cfg.ShopCode {
		return untrusted("Callback belongs to a different shop"), false, nil
	}
	status, statusSent := data["3DStatus"]
	if (statusSent && status != "1") || data["MD"] == "" {
		errorCode := data["ProcReturnCode"]
		if errorCode == "" {
			errorCode = "MD_STATUS_" + status
		}
		message := data["ErrorMessage"]
		if message == "" {
			message = "3D Secure authentication failed"
		}
		return &core.PaymentResponse{
			Status: core.StatusFailure, ErrorCode: errorCode, ErrorMessage: message, RawResponse: data,
		}, false, nil
	}

	payment, err := p.api(ctx, Fields{
		{Key: "TxnType", Value: "Auth"},
		{Key: "SecureType", Value: "NonSecure"},
		{Key: "OrderId", Value: orderID},
		{Key: "PurchAmount", Value: data["PurchAmount"]},
		{Key: "Currency", Value: data["Currency"]},
		{Key: "InstallmentCount", Value: data["InstallmentCount"]},
		{Key: "MD", Value: data["MD"]},
		{Key: "PayerTxnId", Value: data["PayerTxnId"]},
		{Key: "Eci", Value: data["Eci"]},
		{Key: "PayerAuthenticationCode", Value: data["PayerAuthenticationCode"]},
		{Key: "MOTO", Value: "0"},
	})
	if err != nil {
		return nil, true, err
	}

	raw := map[string]any{"callback": data, "payment": payment}
	if !IsApproved(payment) {
		errorCode, errorMessage := errorOf(payment)
		return &core.PaymentResponse{
			Status: core.StatusFailure, ErrorCode: errorCode, ErrorMessage: errorMessage, RawResponse: raw,
		}, true, nil
	}
	return &core.PaymentResponse{
		Status: core.StatusSuccess, PaymentID: orderID, ConversationID: orderID, RawResponse: raw,
	}, true, nil
}

// Refund refunds a sale (Refund); a lower amount is a partial refund. PaymentID
// is the order id.
func (p *Provider) Refund(ctx context.Context, req *core.RefundRequest) (*core.RefundResponse, error) {
	res, err := p.refund(ctx, req)
	if err != nil {
		extra := &core.RefundResponse{}
		if req != nil {
			extra.ConversationID = req.ConversationID
		}
		res = core.ApplyFailure("denizbank", err, "Refund failed", extra)
	}
	res.Code = p.ErrorCodeFor(res.Status, res.ErrorCode)
	return res, nil
}

func (p *Provider) refund(ctx context.Context, req *core.RefundRequest) (*core.RefundResponse, error) {
	if req == nil {
		return nil, errNilRequest("denizbank")
	}
	if err := p.ValidateRefund(req); err != nil {
		return nil, err
	}
	amount, err := FormatAmount(req.Price)
	if err != nil {
		return nil, err
	}
	currency, err := CurrencyCode(req.Currency)
	if err != nil {
		return nil, err
	}
	fields, err := p.api(ctx, Fields{
		{Key: "TxnType", Value: "Refund"},
		{Key: "SecureType", Value: "NonSecure"},
		{Key: "orgOrderId", Value: req.PaymentID},
		{Key: "PurchAmount", Value: amount},
		{Key: "Currency", Value: currency},
		{Key: "MOTO", Value: "0"},
	})
	if err != nil {
		return nil, err
	}

	res := &core.RefundResponse{ConversationID: req.ConversationID, RawResponse: fields}
	if IsApproved(fields) {
		res.Status, res.RefundID = core.StatusSuccess, referenceOf(fields)
	} else {
		res.Status = core.StatusFailure
		res.ErrorCode, res.ErrorMessage = errorOf(fields)
	}
	return res, nil
}

// Cancel reverses a same-day sale (Void). PaymentID is the order id.
func (p *Provider) Cancel(ctx context.Context, req *core.CancelRequest) (*core.CancelResponse, error) {
	res, err := p.cancel(ctx, req)
	if err != nil {
		extra := &core.CancelResponse{}
		if req != nil {
			extra.ConversationID = req.ConversationID
		}
		res = core.ApplyFailure("denizbank", err, "Cancel failed", extra)
	}
	res.Code = p.ErrorCodeFor(res.Status, res.ErrorCode)
	return res, nil
}

func (p *Provider) cancel(ctx context.Context, req *core.CancelRequest) (*core.CancelResponse, error) {
	if req == nil {
		return nil, errNilRequest("denizbank")
	}
	fields, err := p.api(ctx, Fields{
		{Key: "TxnType", Value: "Void"},
		{Key: "SecureType", Value: "NonSecure"},
		{Key: "orgOrderId", Value: req.PaymentID},
	})
	if err != nil {
		return nil, err
	}

	res := &core.CancelResponse{ConversationID: req.ConversationID, RawResponse: fields}
	if IsApproved(fields) {
		res.Status, res.TransactionID = core.StatusSuccess, referenceOf(fields)
	} else {
		res.Status = core.StatusFailure
		res.ErrorCode, res.ErrorMessage = errorOf(fields)
	}
	return res, nil
}

// GetPayment is not available: the InterVPos status query (StatusHistory) answer
// could not be verified.
func (p *Provider) GetPayment(context.Context, string) (*core.PaymentResponse, error) {
	return nil, &core.BaseError{
		Message:  "Order status queries are not supported for Denizbank yet.",
		Code:     "NOT_SUPPORTED",
		Provider: "denizbank",
	}
}

func errorOf(fields map[string]string) (string, string) {
	message := fields["ErrorMessage"]
	if message == "" {
		message = fields["ErrMsg"]
	}
	return fields["ProcReturnCode"], message
}

func referenceOf(fields map[string]string) string {
	if fields["TransId"] != "" {
		return fields["TransId"]
	}
	return fields["HostRefNum"]
}

var htmlEscaper = strings.NewReplacer("&", "&amp;", "<", "&lt;", ">", "&gt;", `"`, "&quot;", "'", "&#39;")

func escapeHTML(value string) string { return htmlEscaper.Replace(value) }

func hiddenInputs(fields Fields) string {
	var b strings.Builder
	for _, f := range fields {
		b.WriteString(`<input type="hidden" name="` + escapeHTML(f.Key) + `" value="` + escapeHTML(f.Value) + `">`)
	}
	return b.String()
}

// callbackFields normalizes the callback payload. A nil value counts as absent,
// an empty string as sent.
func callbackFields(data any) map[string]string {
	fields := make(map[string]string)
	switch value := data.(type) {
	case nil:
	case CallbackData:
		for k, v := range value {
			fields[k] = v
		}
	case map[string]string:
		for k, v := range value {
			fields[k] = v
		}
	case url.Values:
		for k, v := range value {
			if len(v) > 0 {
				fields[k] = v[0]
			}
		}
	case map[string][]string:
		for k, v := range value {
			if len(v) > 0 {
				fields[k] = v[0]
			}
		}
	case map[string]any:
		for k, v := range value {
			if v == nil {
				continue
			}
			if text, ok := v.(string); ok {
				fields[k] = text
			}
		}
	}
	return fields
}

func orderIDOf(req *core.PaymentRequest) string {
	if req != nil && req.ConversationID != "" {
		return req.ConversationID
	}
	return core.GenerateOrderID("")
}

func threeDSOrderIDOf(req *core.ThreeDSPaymentRequest) string {
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

func errNilRequest(provider string) error {
	return core.NewValidationError("Invalid request: request is required", provider,
		[]core.ValidationIssue{{Path: "request", Message: "is required"}})
}
