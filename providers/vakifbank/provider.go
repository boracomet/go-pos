package vakifbank

import (
	"context"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/boracomet/go-pos/core"
)

// Provider is the VakıfBank virtual POS (VPOS 7/24).
//
// CreatePayment sends a VPOS Sale (non-3D); InitThreeDSPayment enrolls the card
// with the MPI and returns an auto-submitting form to the issuer's ACS;
// CompleteThreeDSPayment charges with a VPOS Sale carrying MpiTransactionId, so
// the card and amount come from the bank's own MPI record. Refund and Cancel use
// the VPOS Refund and Cancel transactions. GetPayment is not supported.
//
// PaymentID is your order id: the ConversationID you pass, or a generated one.
// It is the MPI VerifyEnrollmentRequestId and the VPOS TransactionId.
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
	if cfg.Password == "" {
		missing = append(missing, "password")
	}
	if cfg.TerminalNo == "" {
		missing = append(missing, "terminalNo")
	}
	if len(missing) > 0 {
		return nil, core.NewConfigurationError(
			"VakıfBank configuration is missing: "+strings.Join(missing, ", "), "vakifbank")
	}
	if cfg.BaseURL == "" {
		return nil, core.NewConfigurationError("VakıfBank baseUrl is required", "vakifbank")
	}

	p := &Provider{BaseProvider: core.NewBaseProvider("vakifbank", cfg.ProviderConfig), cfg: cfg}
	p.ErrorCodes = errorCodeTable()
	p.client = p.NewHTTPClient(30*time.Second, map[string]string{
		"Content-Type": "application/x-www-form-urlencoded",
	})
	return p, nil
}

func (p *Provider) mpiURL() string {
	if p.cfg.MPIURL != "" {
		return p.cfg.MPIURL
	}
	if p.cfg.TestMode {
		return MPITestURL
	}
	return MPIProductionURL
}

func (p *Provider) vpos(ctx context.Context, fields Fields) (map[string]string, error) {
	request := Fields{
		{Key: "MerchantId", Value: p.cfg.MerchantID},
		{Key: "Password", Value: p.cfg.Password},
		{Key: "TerminalNo", Value: p.cfg.TerminalNo},
	}
	request = append(request, fields...)

	response, err := p.client.Post(ctx, EndpointVPOS, BuildRequestBody("VposRequest", request),
		core.RequestConfig{ResponseType: "text"})
	if err != nil {
		return nil, err
	}
	text, _ := response.Data.(string)
	return ParseXML(text, "VposResponse")
}

// CreatePayment charges the card without 3D Secure (VPOS Sale).
func (p *Provider) CreatePayment(ctx context.Context, req *core.PaymentRequest) (*core.PaymentResponse, error) {
	orderID := orderIDOf(req)
	res, err := p.createPayment(ctx, req, orderID)
	if err != nil {
		res = core.ApplyFailure("vakifbank", err, "Payment failed", &core.PaymentResponse{
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
	if err := p.ValidatePayment(req, core.PaymentValidationRules{
		Card: true, Required: []string{"buyer.ip"},
	}); err != nil {
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
	expiry, err := FormatVPOSExpiry(card.ExpireMonth, card.ExpireYear)
	if err != nil {
		return nil, err
	}

	fields := Fields{
		{Key: "TransactionType", Value: "Sale"},
		{Key: "TransactionId", Value: orderID},
		{Key: "OrderId", Value: orderID},
		{Key: "CurrencyAmount", Value: amount},
		{Key: "CurrencyCode", Value: currency},
		{Key: "Pan", Value: stripWhitespace(card.CardNumber)},
		{Key: "Cvv", Value: card.CVC},
		{Key: "Expiry", Value: expiry},
	}
	// Never sent empty: VPOS rejects an empty installment count.
	if installment > 0 {
		fields = append(fields, Field{Key: "NumberOfInstallments", Value: strconv.Itoa(installment)})
	}
	fields = append(fields,
		Field{Key: "CardHoldersName", Value: card.CardHolderName},
		Field{Key: "ClientIp", Value: req.Buyer.IP},
		Field{Key: "TransactionDeviceSource", Value: "0"},
	)

	result, err := p.vpos(ctx, fields)
	if err != nil {
		return nil, err
	}

	res := &core.PaymentResponse{PaymentID: orderID, ConversationID: orderID, RawResponse: result}
	if IsApproved(result) {
		res.Status = core.StatusSuccess
	} else {
		res.Status = core.StatusFailure
		res.ErrorCode, res.ErrorMessage = result["ResultCode"], result["ResultDetail"]
	}
	return res, nil
}

// InitThreeDSPayment enrolls the card with the MPI and returns an auto-submitting
// form that sends the customer's browser to the issuer's ACS. Only enrolled
// cards (VERes Status Y) continue.
func (p *Provider) InitThreeDSPayment(ctx context.Context, req *core.ThreeDSPaymentRequest) (*core.ThreeDSInitResponse, error) {
	orderID := threeDSOrderIDOf(req)
	res, err := p.initThreeDSPayment(ctx, req, orderID)
	if err != nil {
		res = core.ApplyFailure("vakifbank", err, "3DS initialization failed", &core.ThreeDSInitResponse{
			PaymentID: orderID, ConversationID: orderID,
		})
	}
	res.Code = p.ErrorCodeFor(res.Status, res.ErrorCode)
	return res, nil
}

func (p *Provider) initThreeDSPayment(ctx context.Context, req *core.ThreeDSPaymentRequest, orderID string) (*core.ThreeDSInitResponse, error) {
	if req == nil {
		return nil, errNilRequest("vakifbank")
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
		return nil, core.NewValidationError("callbackUrl is required for 3D Secure payments", "vakifbank", nil)
	}
	cardNumber := stripWhitespace(card.CardNumber)
	installment, err := FormatInstallment(req.Installment)
	if err != nil {
		return nil, err
	}
	expiry, err := FormatMPIExpiry(card.ExpireMonth, card.ExpireYear)
	if err != nil {
		return nil, err
	}
	amount, err := FormatAmount(paidPriceOf(&req.PaymentRequest))
	if err != nil {
		return nil, err
	}
	currency, err := CurrencyCode(req.Currency)
	if err != nil {
		return nil, err
	}

	form := Fields{
		{Key: "MerchantId", Value: p.cfg.MerchantID},
		{Key: "MerchantPassword", Value: p.cfg.Password},
		{Key: "VerifyEnrollmentRequestId", Value: orderID},
		{Key: "Pan", Value: cardNumber},
		{Key: "ExpiryDate", Value: expiry},
		{Key: "PurchaseAmount", Value: amount},
		{Key: "Currency", Value: currency},
		{Key: "BrandName", Value: BrandName(cardNumber)},
		{Key: "SuccessUrl", Value: req.CallbackURL},
		{Key: "FailureUrl", Value: req.CallbackURL},
	}
	if installment > 0 {
		form = append(form, Field{Key: "InstallmentCount", Value: strconv.Itoa(installment)})
	}
	// Returned unchanged with the callback; VPOS needs the customer IP then.
	if IsIP(req.Buyer.IP) {
		form = append(form, Field{Key: "SessionInfo", Value: req.Buyer.IP})
	}

	response, err := p.client.Post(ctx, p.mpiURL(), encodeForm(form), core.RequestConfig{ResponseType: "text"})
	if err != nil {
		return nil, err
	}
	text, _ := response.Data.(string)
	enrollment, err := ParseXML(text, "IPaySecure")
	if err != nil {
		return nil, err
	}

	status := enrollment["Status"]
	paReq, sent := enrollment["PaReq"]
	if !sent {
		paReq = enrollment["PAReq"]
	}
	acsURL, termURL, md := enrollment["ACSUrl"], enrollment["TermUrl"], enrollment["MD"]
	if status != "Y" || paReq == "" || acsURL == "" || termURL == "" || md == "" {
		errorCode := enrollment["ErrorCode"]
		if errorCode == "" {
			errorCode = "VERES_" + status
		}
		if code := enrollment["MessageErrorCode"]; code != "" && code != "200" {
			errorCode = code
		}
		message := enrollment["ErrorMessage"]
		if message == "" {
			if status == "N" {
				message = "The card is not enrolled in 3D Secure"
			} else {
				message = "MPI enrollment failed"
			}
		}
		return &core.ThreeDSInitResponse{
			Status:         core.StatusFailure,
			PaymentID:      orderID,
			ConversationID: orderID,
			ErrorCode:      errorCode,
			ErrorMessage:   message,
			RawResponse:    enrollment,
		}, nil
	}

	inputs := hiddenInputs(Fields{
		{Key: "PaReq", Value: paReq},
		{Key: "TermUrl", Value: termURL},
		{Key: "MD", Value: md},
	})
	html := `<!DOCTYPE html><html><head><meta charset="UTF-8"></head><body>` +
		`<form id="vakifbank-3d-form" method="POST" action="` + escapeHTML(acsURL) + `">` + inputs + `</form>` +
		`<script>document.getElementById("vakifbank-3d-form").submit();</script>` +
		`</body></html>`

	return &core.ThreeDSInitResponse{
		Status:             core.StatusPending,
		ThreeDSHTMLContent: html,
		PaymentID:          orderID,
		ConversationID:     orderID,
	}, nil
}

// CompleteThreeDSPayment finishes the payment after the SuccessUrl / FailureUrl POST.
//
// The POST is not signed. The card is charged with a VPOS Sale carrying
// MpiTransactionId (the order id sent with the enrollment); VPOS reads the card,
// amount and 3D Secure result from its own MPI record, so callback values cannot
// change them. Only Status Y is charged.
//
// PaymentID and ConversationID are set only for a payment VPOS approved, and on
// a timeout after VPOS was reached; a failure never carries ids from the callback.
func (p *Provider) CompleteThreeDSPayment(ctx context.Context, callbackData any) (*core.PaymentResponse, error) {
	data := callbackFields(callbackData)
	res, reached, err := p.completeThreeDSPayment(ctx, data)
	if err != nil {
		res = core.ApplyFailure("vakifbank", err, "3D Secure completion failed", &core.PaymentResponse{})
		if reached && res.Status == core.StatusPending {
			orderID := data["VerifyEnrollmentRequestId"]
			res.PaymentID, res.ConversationID = orderID, orderID
		}
	}
	res.Code = p.ErrorCodeFor(res.Status, res.ErrorCode)
	return res, nil
}

func (p *Provider) completeThreeDSPayment(ctx context.Context, data map[string]string) (*core.PaymentResponse, bool, error) {
	orderID := data["VerifyEnrollmentRequestId"]
	merchantID, merchantSent := data["MerchantId"]
	if orderID == "" || (merchantSent && merchantID != p.cfg.MerchantID) {
		return &core.PaymentResponse{
			Status:       core.StatusFailure,
			ErrorCode:    "INVALID_HASH",
			ErrorMessage: "Callback belongs to a different merchant or has no order id",
			RawResponse:  data,
		}, false, nil
	}
	if data["Status"] != "Y" {
		errorCode := data["ErrorCode"]
		if errorCode == "" {
			errorCode = "MD_STATUS_" + data["Status"]
		}
		message := data["ErrorMessage"]
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
	eci, sent := data["Eci"]
	if !sent {
		eci = data["ECI"]
	}
	cavv, sent := data["Cavv"]
	if !sent {
		cavv = data["CAVV"]
	}
	installment := callbackInstallment(data["InstallmentCount"])

	fields := Fields{
		{Key: "TransactionType", Value: "Sale"},
		{Key: "TransactionId", Value: orderID},
		{Key: "OrderId", Value: orderID},
		{Key: "MpiTransactionId", Value: orderID},
	}
	if eci != "" {
		fields = append(fields, Field{Key: "ECI", Value: eci})
	}
	if cavv != "" {
		fields = append(fields, Field{Key: "CAVV", Value: cavv})
	}
	if installment > 1 {
		fields = append(fields, Field{Key: "NumberOfInstallments", Value: formatNumber(installment)})
	}
	clientIP := "127.0.0.1"
	if IsIP(data["SessionInfo"]) {
		clientIP = data["SessionInfo"]
	} else if IsIP(data["ClientIp"]) {
		clientIP = data["ClientIp"]
	}
	fields = append(fields,
		Field{Key: "ClientIp", Value: clientIP},
		Field{Key: "TransactionDeviceSource", Value: "0"},
	)

	result, err := p.vpos(ctx, fields)
	if err != nil {
		return nil, true, err
	}

	raw := map[string]any{"callback": data, "payment": result}
	if !IsApproved(result) {
		return &core.PaymentResponse{
			Status:       core.StatusFailure,
			ErrorCode:    result["ResultCode"],
			ErrorMessage: result["ResultDetail"],
			RawResponse:  raw,
		}, true, nil
	}
	return &core.PaymentResponse{
		Status: core.StatusSuccess, PaymentID: orderID, ConversationID: orderID, RawResponse: raw,
	}, true, nil
}

// Refund refunds a sale in full or in part (VPOS Refund). PaymentID is the order id.
func (p *Provider) Refund(ctx context.Context, req *core.RefundRequest) (*core.RefundResponse, error) {
	res, err := p.refund(ctx, req)
	if err != nil {
		extra := &core.RefundResponse{}
		if req != nil {
			extra.ConversationID = req.ConversationID
		}
		res = core.ApplyFailure("vakifbank", err, "Refund failed", extra)
	}
	res.Code = p.ErrorCodeFor(res.Status, res.ErrorCode)
	return res, nil
}

func (p *Provider) refund(ctx context.Context, req *core.RefundRequest) (*core.RefundResponse, error) {
	if req == nil {
		return nil, errNilRequest("vakifbank")
	}
	if err := p.ValidateRefund(req); err != nil {
		return nil, err
	}
	amount, err := FormatAmount(req.Price)
	if err != nil {
		return nil, err
	}
	fields, err := p.vpos(ctx, Fields{
		{Key: "TransactionType", Value: "Refund"},
		{Key: "ReferenceTransactionId", Value: req.PaymentID},
		{Key: "CurrencyAmount", Value: amount},
		{Key: "ClientIp", Value: req.IP},
	})
	if err != nil {
		return nil, err
	}

	res := &core.RefundResponse{ConversationID: req.ConversationID, RawResponse: fields}
	if IsApproved(fields) {
		res.Status, res.RefundID = core.StatusSuccess, fields["TransactionId"]
	} else {
		res.Status = core.StatusFailure
		res.ErrorCode, res.ErrorMessage = fields["ResultCode"], fields["ResultDetail"]
	}
	return res, nil
}

// Cancel reverses a sale before end of day (VPOS Cancel). PaymentID is the order id.
func (p *Provider) Cancel(ctx context.Context, req *core.CancelRequest) (*core.CancelResponse, error) {
	res, err := p.cancel(ctx, req)
	if err != nil {
		extra := &core.CancelResponse{}
		if req != nil {
			extra.ConversationID = req.ConversationID
		}
		res = core.ApplyFailure("vakifbank", err, "Cancel failed", extra)
	}
	res.Code = p.ErrorCodeFor(res.Status, res.ErrorCode)
	return res, nil
}

func (p *Provider) cancel(ctx context.Context, req *core.CancelRequest) (*core.CancelResponse, error) {
	if req == nil {
		return nil, errNilRequest("vakifbank")
	}
	fields, err := p.vpos(ctx, Fields{
		{Key: "TransactionType", Value: "Cancel"},
		{Key: "ReferenceTransactionId", Value: req.PaymentID},
		{Key: "ClientIp", Value: req.IP},
	})
	if err != nil {
		return nil, err
	}

	res := &core.CancelResponse{ConversationID: req.ConversationID, RawResponse: fields}
	if IsApproved(fields) {
		res.Status, res.TransactionID = core.StatusSuccess, fields["TransactionId"]
	} else {
		res.Status = core.StatusFailure
		res.ErrorCode, res.ErrorMessage = fields["ResultCode"], fields["ResultDetail"]
	}
	return res, nil
}

// GetPayment is not supported: VakıfBank's record search by order id returns the
// order's successful sale (or its latest attempt) but not the cancel and refund
// records, which carry no order id. It cannot tell whether a paid order was later
// cancelled or refunded, so it is not offered as the payment status.
func (p *Provider) GetPayment(context.Context, string) (*core.PaymentResponse, error) {
	return nil, core.NotSupported("vakifbank", "Payment status",
		"VakıfBank's record search by order id does not return cancel or refund records")
}

// callbackInstallment mirrors Number(value): an absent or unparsable count is
// not a number and never selects an installment.
func callbackInstallment(value string) float64 {
	n, err := strconv.ParseFloat(strings.TrimSpace(value), 64)
	if err != nil {
		return 0
	}
	return n
}

func formatNumber(value float64) string {
	return strconv.FormatFloat(value, 'f', -1, 64)
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
