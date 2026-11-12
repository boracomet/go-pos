package qnb

import (
	"context"
	"net/url"
	"slices"
	"strings"
	"time"

	"github.com/boracomet/go-pos/core"
)

// Provider is the QNB Finansbank virtual POS (PayFor).
//
// CreatePayment sends an XML Auth (NonSecure); InitThreeDSPayment returns an
// auto-submitting form to the 3D gateway (3DModel or 3DPay);
// CompleteThreeDSPayment verifies the ResponseHash and confirms the result with
// the bank. Refund, Cancel and GetPayment use Refund, Void and OrderInquiry.
//
// PaymentID is your order id: the ConversationID you pass, or a generated one.
type Provider struct {
	core.BaseProvider
	cfg    Config
	client *core.HTTPClient
}

var _ core.Provider = (*Provider)(nil)

// New builds the provider, validating the credentials and the base URL.
func New(cfg Config) (*Provider, error) {
	var missing []string
	if cfg.MerchantID == "" {
		missing = append(missing, "merchantId")
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
		return nil, core.NewConfigurationError("QNB configuration is missing: "+strings.Join(missing, ", "), "qnb")
	}
	if cfg.BaseURL == "" {
		return nil, core.NewConfigurationError("QNB baseUrl is required", "qnb")
	}
	if cfg.SecureType != "" && !slices.Contains(SecureTypes, cfg.SecureType) {
		return nil, core.NewConfigurationError("Unknown QNB secureType: "+cfg.SecureType, "qnb")
	}

	p := &Provider{BaseProvider: core.NewBaseProvider("qnb", cfg.ProviderConfig), cfg: cfg}
	p.ErrorCodes = errorCodeTable()
	p.client = p.NewHTTPClient(30*time.Second, map[string]string{"Content-Type": "application/xml"})
	return p, nil
}

func (p *Provider) secureType() string {
	if p.cfg.SecureType != "" {
		return p.cfg.SecureType
	}
	return "3DModel"
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

func (p *Provider) credentials() Credentials {
	return Credentials{
		MerchantID:   p.cfg.MerchantID,
		MerchantPass: p.cfg.MerchantPass,
		UserCode:     p.cfg.UserCode,
	}
}

func (p *Provider) api(ctx context.Context, fields Fields, retryable bool) (map[string]string, error) {
	request := Fields{
		{Key: "MbrId", Value: MbrID},
		{Key: "MerchantId", Value: p.cfg.MerchantID},
		{Key: "UserCode", Value: p.cfg.UserCode},
		{Key: "UserPass", Value: p.cfg.UserPass},
	}
	request = append(request, fields...)
	request = append(request, Field{Key: "Lang", Value: p.lang()})

	response, err := p.client.Post(ctx, EndpointAPI, BuildXML(request), core.RequestConfig{
		ResponseType: "text",
		Retryable:    retryable,
	})
	if err != nil {
		return nil, err
	}
	text, _ := response.Data.(string)
	return ParseXMLResponse(text)
}

func (p *Provider) orderInquiry(ctx context.Context, orderID string) (map[string]string, error) {
	return p.api(ctx, Fields{
		{Key: "OrgOrderId", Value: orderID},
		{Key: "SecureType", Value: "Inquiry"},
		{Key: "TxnType", Value: "OrderInquiry"},
		{Key: "Currency", Value: "949"},
	}, true)
}

// CreatePayment charges the card without 3D Secure (XML Auth, NonSecure).
func (p *Provider) CreatePayment(ctx context.Context, req *core.PaymentRequest) (*core.PaymentResponse, error) {
	orderID := orderIDOf(req)
	res, err := p.createPayment(ctx, req, orderID)
	if err != nil {
		res = core.ApplyFailure("qnb", err, "Payment failed", &core.PaymentResponse{
			PaymentID: orderID, ConversationID: orderID,
		})
	}
	res.Code = p.ErrorCodeFor(res.Status, res.ErrorCode)
	return res, nil
}

func (p *Provider) createPayment(ctx context.Context, req *core.PaymentRequest, orderID string) (*core.PaymentResponse, error) {
	if _, err := ValidateOrderID(orderID); err != nil {
		return nil, err
	}
	if err := p.ValidatePayment(req, core.PaymentValidationRules{Card: true}); err != nil {
		return nil, err
	}
	card, err := p.CardOf(req)
	if err != nil {
		return nil, err
	}
	installment, err := FormatInstallment(req.Installment)
	if err != nil {
		return nil, err
	}
	amount, err := FormatAmount(paidPriceOf(req))
	if err != nil {
		return nil, err
	}
	currency, err := CurrencyCode(req.Currency)
	if err != nil {
		return nil, err
	}
	expiry, err := FormatExpiry(card.ExpireMonth, card.ExpireYear)
	if err != nil {
		return nil, err
	}

	fields, err := p.api(ctx, Fields{
		{Key: "OrderId", Value: orderID},
		{Key: "SecureType", Value: "NonSecure"},
		{Key: "TxnType", Value: "Auth"},
		{Key: "InstallmentCount", Value: installment},
		{Key: "PurchAmount", Value: amount},
		{Key: "Currency", Value: currency},
		{Key: "CardHolderName", Value: card.CardHolderName},
		{Key: "Pan", Value: stripWhitespace(card.CardNumber)},
		{Key: "Expiry", Value: expiry},
		{Key: "Cvv2", Value: card.CVC},
		{Key: "MOTO", Value: "0"},
	}, false)
	if err != nil {
		return nil, err
	}

	res := &core.PaymentResponse{PaymentID: orderID, ConversationID: orderID, RawResponse: fields}
	if IsApproved(fields) {
		res.Status = core.StatusSuccess
	} else {
		res.Status = core.StatusFailure
		res.ErrorCode, res.ErrorMessage = fields["ProcReturnCode"], fields["ErrMsg"]
	}
	return res, nil
}

// InitThreeDSPayment returns an auto-submitting form to the 3D gateway, signed
// with the MerchantPass hash. The API password is never put in the form.
func (p *Provider) InitThreeDSPayment(ctx context.Context, req *core.ThreeDSPaymentRequest) (*core.ThreeDSInitResponse, error) {
	orderID := threeDSOrderIDOf(req)
	res, err := p.initThreeDSPayment(req, orderID)
	if err != nil {
		res = core.ApplyFailure("qnb", err, "3DS initialization failed", &core.ThreeDSInitResponse{
			PaymentID: orderID, ConversationID: orderID,
		})
	}
	res.Code = p.ErrorCodeFor(res.Status, res.ErrorCode)
	return res, nil
}

func (p *Provider) initThreeDSPayment(req *core.ThreeDSPaymentRequest, orderID string) (*core.ThreeDSInitResponse, error) {
	if req == nil {
		return nil, errNilRequest("qnb")
	}
	if _, err := ValidateOrderID(orderID); err != nil {
		return nil, err
	}
	if err := p.ValidatePayment(&req.PaymentRequest, core.PaymentValidationRules{Card: true}); err != nil {
		return nil, err
	}
	card, err := p.CardOf(&req.PaymentRequest)
	if err != nil {
		return nil, err
	}
	if req.CallbackURL == "" {
		return nil, core.NewValidationError("callbackUrl is required for 3D Secure payments", "qnb", nil)
	}
	installment, err := FormatInstallment(req.Installment)
	if err != nil {
		return nil, err
	}
	currency, err := CurrencyCode(req.Currency)
	if err != nil {
		return nil, err
	}
	amount, err := FormatAmount(paidPriceOf(&req.PaymentRequest))
	if err != nil {
		return nil, err
	}
	expiry, err := FormatExpiry(card.ExpireMonth, card.ExpireYear)
	if err != nil {
		return nil, err
	}
	rnd, err := core.RandomHex(10)
	if err != nil {
		return nil, err
	}

	fields := Fields{
		{Key: "MbrId", Value: MbrID},
		{Key: "MerchantID", Value: p.cfg.MerchantID},
		{Key: "UserCode", Value: p.cfg.UserCode},
		{Key: "SecureType", Value: p.secureType()},
		{Key: "TxnType", Value: "Auth"},
		{Key: "InstallmentCount", Value: installment},
		{Key: "Currency", Value: currency},
		{Key: "OkUrl", Value: req.CallbackURL},
		{Key: "FailUrl", Value: req.CallbackURL},
		{Key: "OrderId", Value: orderID},
		{Key: "PurchAmount", Value: amount},
		{Key: "Lang", Value: p.lang()},
		{Key: "Rnd", Value: rnd},
		{Key: "Pan", Value: stripWhitespace(card.CardNumber)},
		{Key: "Expiry", Value: expiry},
		{Key: "Cvv2", Value: card.CVC},
		{Key: "CardHolderName", Value: card.CardHolderName},
	}
	fields = append(fields, Field{Key: "Hash", Value: Create3DHash(HashFields{
		OrderID:      orderID,
		Amount:       amount,
		OkURL:        req.CallbackURL,
		FailURL:      req.CallbackURL,
		TxnType:      "Auth",
		Installment:  installment,
		Rnd:          rnd,
		MerchantPass: p.cfg.MerchantPass,
	})})

	action := p.cfg.BaseURL + EndpointGate
	html := `<!DOCTYPE html><html><head><meta charset="UTF-8"></head><body>` +
		`<form id="qnb-3d-form" method="POST" action="` + escapeHTML(action) + `">` + hiddenInputs(fields) + `</form>` +
		`<script>document.getElementById("qnb-3d-form").submit();</script>` +
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
// The ResponseHash joins its values without a separator, so a valid hash alone
// does not pin each value; the result is confirmed with the bank. 3DModel
// charges with 3DModelPayment and the bank's answer decides; 3DPay is checked
// with OrderInquiry (status Y, amount equal to the callback). A callback with an
// invalid hash, for another merchant, or claiming a payment the bank does not
// confirm is rejected as INVALID_HASH.
//
// PaymentID and ConversationID are set only for a payment the bank confirmed,
// and on a timeout after the bank was reached for this order.
func (p *Provider) CompleteThreeDSPayment(ctx context.Context, callbackData any) (*core.PaymentResponse, error) {
	data := callbackFields(callbackData)
	res, reached, err := p.completeThreeDSPayment(ctx, data)
	if err != nil {
		res = core.ApplyFailure("qnb", err, "3D Secure completion failed", &core.PaymentResponse{})
		if reached && res.Status == core.StatusPending {
			res.PaymentID, res.ConversationID = data["OrderId"], data["OrderId"]
		}
	}
	res.Code = p.ErrorCodeFor(res.Status, res.ErrorCode)
	return res, nil
}

func (p *Provider) completeThreeDSPayment(ctx context.Context, data map[string]string) (*core.PaymentResponse, bool, error) {
	untrusted := func(message string, raw any) *core.PaymentResponse {
		return &core.PaymentResponse{
			Status: core.StatusFailure, ErrorCode: "INVALID_HASH", ErrorMessage: message, RawResponse: raw,
		}
	}

	if !VerifyCallback(data, p.credentials()) {
		return untrusted("Invalid 3D Secure callback hash", data), false, nil
	}
	orderID := data["OrderId"]
	if merchantID, sent := data["MerchantID"]; sent && merchantID != p.cfg.MerchantID {
		return untrusted("Callback belongs to a different merchant", data), false, nil
	}
	if data["3DStatus"] != "1" {
		errorCode := data["ProcReturnCode"]
		if errorCode == "" {
			errorCode = "MD_STATUS_" + data["3DStatus"]
		}
		message := data["ErrMsg"]
		if message == "" {
			message = "3D Secure authentication failed"
		}
		return &core.PaymentResponse{
			Status: core.StatusFailure, ErrorCode: errorCode, ErrorMessage: message, RawResponse: data,
		}, false, nil
	}

	if _, err := ValidateOrderID(orderID); err != nil {
		return nil, false, err
	}

	if p.secureType() == "3DModel" {
		if data["RequestGuid"] == "" {
			return untrusted("Callback has no RequestGuid", data), false, nil
		}
		payment, err := p.api(ctx, Fields{
			{Key: "RequestGuid", Value: data["RequestGuid"]},
			{Key: "OrderId", Value: orderID},
			{Key: "SecureType", Value: "3DModelPayment"},
		}, false)
		if err != nil {
			return nil, true, err
		}
		raw := map[string]any{"callback": data, "payment": payment}
		if !IsApproved(payment) {
			return &core.PaymentResponse{
				Status:       core.StatusFailure,
				ErrorCode:    payment["ProcReturnCode"],
				ErrorMessage: payment["ErrMsg"],
				RawResponse:  raw,
			}, true, nil
		}
		return &core.PaymentResponse{
			Status: core.StatusSuccess, PaymentID: orderID, ConversationID: orderID, RawResponse: raw,
		}, true, nil
	}

	// 3DPay: the bank already charged the card.
	if !IsApproved(data) {
		return &core.PaymentResponse{
			Status:       core.StatusFailure,
			ErrorCode:    data["ProcReturnCode"],
			ErrorMessage: data["ErrMsg"],
			RawResponse:  data,
		}, false, nil
	}
	inquiry, err := p.orderInquiry(ctx, orderID)
	if err != nil {
		return nil, true, err
	}
	amount, amountSent := data["PurchAmount"]
	if !IsCapturedSale(inquiry) || (amountSent && inquiry["PurchAmount"] != amount) {
		return untrusted("3D Secure callback does not match the order at the bank",
			map[string]any{"callback": data, "inquiry": inquiry}), true, nil
	}
	return &core.PaymentResponse{
		Status:         core.StatusSuccess,
		PaymentID:      orderID,
		ConversationID: orderID,
		RawResponse:    map[string]any{"callback": data, "inquiry": inquiry},
	}, true, nil
}

// Refund refunds a sale of an earlier day (XML Refund); a lower amount is a
// partial refund. PaymentID is the order id.
func (p *Provider) Refund(ctx context.Context, req *core.RefundRequest) (*core.RefundResponse, error) {
	res, err := p.refund(ctx, req)
	if err != nil {
		extra := &core.RefundResponse{}
		if req != nil {
			extra.ConversationID = req.ConversationID
		}
		res = core.ApplyFailure("qnb", err, "Refund failed", extra)
	}
	res.Code = p.ErrorCodeFor(res.Status, res.ErrorCode)
	return res, nil
}

func (p *Provider) refund(ctx context.Context, req *core.RefundRequest) (*core.RefundResponse, error) {
	if req == nil {
		return nil, errNilRequest("qnb")
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
		{Key: "OrgOrderId", Value: req.PaymentID},
		{Key: "SecureType", Value: "NonSecure"},
		{Key: "TxnType", Value: "Refund"},
		{Key: "PurchAmount", Value: amount},
		{Key: "Currency", Value: currency},
	}, false)
	if err != nil {
		return nil, err
	}

	res := &core.RefundResponse{ConversationID: req.ConversationID, RawResponse: fields}
	if IsApproved(fields) {
		res.Status, res.RefundID = core.StatusSuccess, fields["HostRefNum"]
	} else {
		res.Status = core.StatusFailure
		res.ErrorCode, res.ErrorMessage = fields["ProcReturnCode"], fields["ErrMsg"]
	}
	return res, nil
}

// Cancel reverses a same-day sale (XML Void). PaymentID is the order id.
func (p *Provider) Cancel(ctx context.Context, req *core.CancelRequest) (*core.CancelResponse, error) {
	res, err := p.cancel(ctx, req)
	if err != nil {
		extra := &core.CancelResponse{}
		if req != nil {
			extra.ConversationID = req.ConversationID
		}
		res = core.ApplyFailure("qnb", err, "Cancel failed", extra)
	}
	res.Code = p.ErrorCodeFor(res.Status, res.ErrorCode)
	return res, nil
}

func (p *Provider) cancel(ctx context.Context, req *core.CancelRequest) (*core.CancelResponse, error) {
	if req == nil {
		return nil, errNilRequest("qnb")
	}
	currency, err := CurrencyCode(req.Currency)
	if err != nil {
		return nil, err
	}
	fields, err := p.api(ctx, Fields{
		{Key: "OrgOrderId", Value: req.PaymentID},
		{Key: "SecureType", Value: "NonSecure"},
		{Key: "TxnType", Value: "Void"},
		{Key: "Currency", Value: currency},
	}, false)
	if err != nil {
		return nil, err
	}

	res := &core.CancelResponse{ConversationID: req.ConversationID, RawResponse: fields}
	if IsApproved(fields) {
		res.Status, res.TransactionID = core.StatusSuccess, fields["HostRefNum"]
	} else {
		res.Status = core.StatusFailure
		res.ErrorCode, res.ErrorMessage = fields["ProcReturnCode"], fields["ErrMsg"]
	}
	return res, nil
}

// GetPayment reads the order status with OrderInquiry. PaymentID is the order id.
func (p *Provider) GetPayment(ctx context.Context, paymentID string) (*core.PaymentResponse, error) {
	res, err := p.getPayment(ctx, paymentID)
	if err != nil {
		res = core.ApplyFailure("qnb", err, "Get payment failed", &core.PaymentResponse{
			PaymentID: paymentID, ConversationID: paymentID,
		})
	}
	res.Code = p.ErrorCodeFor(res.Status, res.ErrorCode)
	return res, nil
}

func (p *Provider) getPayment(ctx context.Context, paymentID string) (*core.PaymentResponse, error) {
	inquiry, err := p.orderInquiry(ctx, paymentID)
	if err != nil {
		return nil, err
	}
	res := &core.PaymentResponse{PaymentID: paymentID, ConversationID: paymentID, RawResponse: inquiry}
	if !IsApproved(inquiry) {
		res.Status = core.StatusFailure
		res.ErrorCode, res.ErrorMessage = inquiry["ProcReturnCode"], inquiry["ErrMsg"]
		return res, nil
	}
	res.Status = MapOrderStatus(inquiry)
	return res, nil
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
