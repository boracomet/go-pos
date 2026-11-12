package garanti

import (
	"context"
	"strings"
	"time"

	"github.com/boracomet/go-pos/core"
)

// Provider is the Garanti BBVA virtual POS (GVP, XML API version 512).
//
//   - CreatePayment:          XML sales (non-3D)
//   - InitThreeDSPayment:     auto-submitting form to the 3D engine (3D, 3D_PAY,
//     or GARANTI_PAY for GarantiPay without card details)
//   - CompleteThreeDSPayment: verifies the callback hash; 3D provisions with XML
//     sales, 3D_PAY and GARANTI_PAY are confirmed with an order inquiry
//   - Refund refund, Cancel void (PROVRFN user), GetPayment orderinq
//
// PaymentID is the order id: the ConversationID passed in, or a generated one.
type Provider struct {
	core.BaseProvider
	cfg    Config
	client *core.HTTPClient
}

// New builds a Garanti BBVA provider. BaseURL defaults to the production host, or
// the test host when TestMode is set.
func New(cfg Config) (*Provider, error) {
	if cfg.BaseURL == "" {
		cfg.BaseURL = ProductionURL
		if cfg.TestMode {
			cfg.BaseURL = SandboxURL
		}
	}
	if cfg.SecurityLevel == "" {
		cfg.SecurityLevel = Level3D
	}

	var missing []string
	if cfg.MerchantID == "" {
		missing = append(missing, "merchantId")
	}
	if cfg.TerminalID == "" {
		missing = append(missing, "terminalId")
	}
	if cfg.Password == "" {
		missing = append(missing, "password")
	}
	if cfg.StoreKey == "" {
		missing = append(missing, "storeKey")
	}
	if len(missing) > 0 {
		return nil, core.NewConfigurationError("Garanti BBVA configuration is missing: "+strings.Join(missing, ", "), "garanti")
	}
	if cfg.BaseURL == "" {
		return nil, core.NewConfigurationError("Garanti BBVA baseUrl is required", "garanti")
	}
	known := false
	for _, level := range SecurityLevels {
		if level == cfg.SecurityLevel {
			known = true
			break
		}
	}
	if !known {
		return nil, core.NewConfigurationError("Unknown Garanti BBVA securityLevel: "+string(cfg.SecurityLevel), "garanti")
	}

	p := &Provider{BaseProvider: core.NewBaseProvider("garanti", cfg.ProviderConfig), cfg: cfg}
	p.ErrorCodes = core.ISO8583ErrorCodes
	p.client = p.NewHTTPClient(30*time.Second, map[string]string{"Content-Type": "application/xml"})
	return p, nil
}

func (p *Provider) mode() string {
	if p.cfg.TestMode {
		return "TEST"
	}
	return "PROD"
}

func (p *Provider) provUserID() string {
	if p.cfg.ProvUserID != "" {
		return p.cfg.ProvUserID
	}
	return "PROVAUT"
}

func (p *Provider) lang() string {
	if strings.HasPrefix(strings.ToLower(p.Config.Locale), "en") {
		return "en"
	}
	return "tr"
}

type cardFields struct {
	Number     string
	ExpireDate string
	CVV2       string
}

type apiRequest struct {
	orderID      string
	amount       string
	currencyCode string
	ip           string
	transaction  []XMLField
	card         *cardFields
	refund       bool
	retryable    bool
}

// api sends a GVPSRequest signed with HashData. Cancel and refund use the
// PROVRFN user and its password.
func (p *Provider) api(ctx context.Context, request apiRequest) (XMLResponse, error) {
	userID, password := p.provUserID(), p.cfg.Password
	if request.refund {
		userID = "PROVRFN"
		if p.cfg.RefundUserID != "" {
			userID = p.cfg.RefundUserID
		}
		if p.cfg.RefundPassword != "" {
			password = p.cfg.RefundPassword
		}
	}
	securityData, err := CreateSecurityData(password, p.cfg.TerminalID)
	if err != nil {
		return XMLResponse{}, err
	}
	cardNumber := ""
	if request.card != nil {
		cardNumber = request.card.Number
	}
	hashData, err := CreateHashData(HashDataFields{
		OrderID:      request.orderID,
		TerminalID:   p.cfg.TerminalID,
		CardNumber:   cardNumber,
		Amount:       request.amount,
		CurrencyCode: request.currencyCode,
		SecurityData: securityData,
	})
	if err != nil {
		return XMLResponse{}, err
	}

	fields := []XMLField{
		{Name: "Mode", Value: p.mode()},
		{Name: "Version", Value: APIVersion},
		{Name: "Terminal", Children: []XMLField{
			{Name: "ProvUserID", Value: userID},
			{Name: "HashData", Value: hashData},
			{Name: "UserID", Value: userID},
			{Name: "ID", Value: p.cfg.TerminalID},
			{Name: "MerchantID", Value: p.cfg.MerchantID},
		}},
		{Name: "Customer", Children: []XMLField{{Name: "IPAddress", Value: request.ip}}},
	}
	if request.card != nil {
		fields = append(fields, XMLField{Name: "Card", Children: []XMLField{
			{Name: "Number", Value: request.card.Number},
			{Name: "ExpireDate", Value: request.card.ExpireDate},
			{Name: "CVV2", Value: request.card.CVV2},
		}})
	}
	fields = append(fields,
		XMLField{Name: "Order", Children: []XMLField{{Name: "OrderID", Value: request.orderID}}},
		XMLField{Name: "Transaction", Children: append(request.transaction,
			XMLField{Name: "Amount", Value: request.amount},
			XMLField{Name: "CurrencyCode", Value: request.currencyCode},
		)},
	)

	response, err := p.client.Post(ctx, EndpointAPI, BuildXML(fields), core.RequestConfig{
		ResponseType: "text",
		Retryable:    request.retryable,
	})
	if err != nil {
		return XMLResponse{}, err
	}
	text, _ := response.Data.(string)
	return ParseXMLResponse(text)
}

// orderInquiry queries the order (orderinq). Read-only, so retryable.
func (p *Provider) orderInquiry(ctx context.Context, orderID, ip string) (XMLResponse, error) {
	return p.api(ctx, apiRequest{
		orderID:      orderID,
		amount:       "1",
		currencyCode: "949",
		ip:           ip,
		transaction: []XMLField{
			{Name: "Type", Value: "orderinq"},
			{Name: "ListPageNum", Value: "0"},
			{Name: "CardholderPresentCode", Value: ""},
			{Name: "MotoInd", Value: "N"},
		},
		retryable: true,
	})
}

// errorOf returns the raw error code: ReasonCode (ISO 8583 for bank declines),
// else Code.
func errorOf(result XMLResponse) (errorCode, errorMessage string) {
	response := result.Response
	return firstNonEmpty(response["ReasonCode"], response["Code"]),
		firstNonEmpty(response["ErrorMsg"], response["SysErrMsg"], response["Message"])
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
	out := core.ApplyFailure("garanti", err, fallback, &core.PaymentResponse{
		PaymentID: paymentID, ConversationID: paymentID,
	})
	out.Code = p.ErrorCodeFor(out.Status, out.ErrorCode)
	return out, nil
}

func (p *Provider) failInit(err error, fallback, paymentID string) (*core.ThreeDSInitResponse, error) {
	out := core.ApplyFailure("garanti", err, fallback, &core.ThreeDSInitResponse{
		PaymentID: paymentID, ConversationID: paymentID,
	})
	out.Code = p.ErrorCodeFor(out.Status, out.ErrorCode)
	return out, nil
}

func (p *Provider) failRefund(err error, fallback, conversationID string) (*core.RefundResponse, error) {
	out := core.ApplyFailure("garanti", err, fallback, &core.RefundResponse{ConversationID: conversationID})
	out.Code = p.ErrorCodeFor(out.Status, out.ErrorCode)
	return out, nil
}

func (p *Provider) failCancel(err error, fallback, conversationID string) (*core.CancelResponse, error) {
	out := core.ApplyFailure("garanti", err, fallback, &core.CancelResponse{ConversationID: conversationID})
	out.Code = p.ErrorCodeFor(out.Status, out.ErrorCode)
	return out, nil
}

// failComplete keeps the ids only for an order from the merchant's records, so it
// can be checked with GetPayment; the callback's own order is not trusted.
func (p *Provider) failComplete(err error, expectedOrderID string, reached bool) (*core.PaymentResponse, error) {
	out := core.ApplyFailure("garanti", err, "3D Secure completion failed", &core.PaymentResponse{})
	out.Code = p.ErrorCodeFor(out.Status, out.ErrorCode)
	if reached && out.Status == core.StatusPending && expectedOrderID != "" {
		out.PaymentID, out.ConversationID = expectedOrderID, expectedOrderID
	}
	return out, nil
}

func (p *Provider) result(status core.PaymentStatus, errorCode, errorMessage string, raw any) *core.PaymentResponse {
	out := &core.PaymentResponse{
		Status: status, ErrorCode: errorCode, ErrorMessage: errorMessage, RawResponse: raw,
	}
	out.Code = p.ErrorCodeFor(status, errorCode)
	return out
}

// CreatePayment charges the card without 3D Secure (XML sales).
func (p *Provider) CreatePayment(ctx context.Context, request *core.PaymentRequest) (*core.PaymentResponse, error) {
	conversationID := ""
	if request != nil {
		conversationID = request.ConversationID
	}
	orderID := orderIDOf(conversationID)

	if request == nil {
		return p.failPayment(core.NewValidationError("request is required", "garanti", nil), "Payment failed", orderID)
	}
	if err := p.ValidatePayment(request, core.PaymentValidationRules{Card: true, Required: []string{"buyer.ip"}}); err != nil {
		return p.failPayment(err, "Payment failed", orderID)
	}
	card, err := p.CardOf(request)
	if err != nil {
		return p.failPayment(err, "Payment failed", orderID)
	}
	expiry, err := FormatExpiry(card.ExpireMonth, card.ExpireYear)
	if err != nil {
		return p.failPayment(err, "Payment failed", orderID)
	}
	amount, err := FormatAmount(paidPriceOf(request))
	if err != nil {
		return p.failPayment(err, "Payment failed", orderID)
	}
	currency, err := CurrencyCode(request.Currency)
	if err != nil {
		return p.failPayment(err, "Payment failed", orderID)
	}
	installment, err := FormatInstallment(request.Installment)
	if err != nil {
		return p.failPayment(err, "Payment failed", orderID)
	}

	result, err := p.api(ctx, apiRequest{
		orderID:      orderID,
		amount:       amount,
		currencyCode: currency,
		ip:           request.Buyer.IP,
		card: &cardFields{
			Number:     compactCardNumber(card.CardNumber),
			ExpireDate: expiry.Month + expiry.Year,
			CVV2:       card.CVC,
		},
		transaction: []XMLField{
			{Name: "Type", Value: "sales"},
			{Name: "InstallmentCnt", Value: installment},
			{Name: "CardholderPresentCode", Value: "0"},
			{Name: "MotoInd", Value: "N"},
		},
	})
	if err != nil {
		return p.failPayment(err, "Payment failed", orderID)
	}

	out := &core.PaymentResponse{PaymentID: orderID, ConversationID: orderID, RawResponse: result}
	if IsApproved(result) {
		out.Status = core.StatusSuccess
	} else {
		out.Status = core.StatusFailure
		out.ErrorCode, out.ErrorMessage = errorOf(result)
	}
	out.Code = p.ErrorCodeFor(out.Status, out.ErrorCode)
	return out, nil
}

// InitThreeDSPayment builds the auto-submitting form for the Garanti BBVA 3D engine.
func (p *Provider) InitThreeDSPayment(ctx context.Context, request *core.ThreeDSPaymentRequest) (*core.ThreeDSInitResponse, error) {
	return p.InitThreeDSPaymentWithOptions(ctx, request, InitOptions{})
}

// InitThreeDSPaymentWithOptions builds the 3D form with an optional failUrl. With
// GARANTI_PAY the form opens the GarantiPay page instead (gpdatarequest) and the
// request needs no card.
func (p *Provider) InitThreeDSPaymentWithOptions(ctx context.Context, request *core.ThreeDSPaymentRequest, options InitOptions) (*core.ThreeDSInitResponse, error) {
	conversationID := ""
	if request != nil {
		conversationID = request.ConversationID
	}
	orderID := orderIDOf(conversationID)

	if request == nil {
		return p.failInit(core.NewValidationError("request is required", "garanti", nil), "3DS initialization failed", orderID)
	}
	garantiPay := p.cfg.SecurityLevel == LevelGarantiPay
	if err := p.ValidatePayment(&request.PaymentRequest, core.PaymentValidationRules{
		Card: !garantiPay, Required: []string{"buyer.ip"},
	}); err != nil {
		return p.failInit(err, "3DS initialization failed", orderID)
	}
	// The outer CallbackURL shadows the embedded one; both name the same field.
	callbackURL := request.CallbackURL
	if callbackURL == "" {
		callbackURL = request.PaymentRequest.CallbackURL
	}
	if callbackURL == "" {
		return p.failInit(core.NewValidationError("callbackUrl is required for 3D Secure payments", "garanti", nil),
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
	installment, err := FormatInstallment(request.Installment)
	if err != nil {
		return p.failInit(err, "3DS initialization failed", orderID)
	}

	level, txnType := string(p.cfg.SecurityLevel), "sales"
	if garantiPay {
		level, txnType = "CUSTOM_PAY", "gpdatarequest"
	}
	failURL := options.FailURL
	if failURL == "" {
		failURL = callbackURL
	}

	var fields formFields
	fields.set("secure3dsecuritylevel", level)
	fields.set("mode", p.mode())
	fields.set("apiversion", APIVersion)
	fields.set("terminalprovuserid", p.provUserID())
	fields.set("terminaluserid", p.provUserID())
	fields.set("terminalmerchantid", p.cfg.MerchantID)
	fields.set("terminalid", p.cfg.TerminalID)
	fields.set("txntype", txnType)
	fields.set("txnamount", amount)
	fields.set("txncurrencycode", currency)
	fields.set("txninstallmentcount", installment)
	fields.set("orderid", orderID)
	fields.set("successurl", callbackURL)
	fields.set("errorurl", failURL)
	fields.set("customeripaddress", request.Buyer.IP)
	fields.set("customeremailaddress", request.Buyer.Email)

	if garantiPay {
		fields.set("txnsubtype", "sales")
		fields.set("garantipay", "Y")
		// Let the customer use Bonus, partner (FBB), cheque and mile points.
		fields.set("bnsuseflag", "Y")
		fields.set("fbbuseflag", "Y")
		fields.set("chequeuseflag", "Y")
		fields.set("mileuseflag", "Y")
		fields.set("companyname", p.cfg.CompanyName)
		fields.set("lang", p.lang())
		fields.set("txntimestamp", time.Now().UTC().Format("2006-01-02T15:04:05.000Z"))
	} else {
		card, err := p.CardOf(&request.PaymentRequest)
		if err != nil {
			return p.failInit(err, "3DS initialization failed", orderID)
		}
		expiry, err := FormatExpiry(card.ExpireMonth, card.ExpireYear)
		if err != nil {
			return p.failInit(err, "3DS initialization failed", orderID)
		}
		fields.set("cardnumber", compactCardNumber(card.CardNumber))
		fields.set("cardexpiredatemonth", expiry.Month)
		fields.set("cardexpiredateyear", expiry.Year)
		fields.set("cardcvv2", card.CVC)
	}

	securityData, err := CreateSecurityData(p.cfg.Password, p.cfg.TerminalID)
	if err != nil {
		return p.failInit(err, "3DS initialization failed", orderID)
	}
	hash, err := Create3DHash(ThreeDHashFields{
		TerminalID:   p.cfg.TerminalID,
		OrderID:      orderID,
		Amount:       fields.get("txnamount"),
		CurrencyCode: fields.get("txncurrencycode"),
		SuccessURL:   fields.get("successurl"),
		ErrorURL:     fields.get("errorurl"),
		Type:         fields.get("txntype"),
		Installment:  fields.get("txninstallmentcount"),
		StoreKey:     p.cfg.StoreKey,
		SecurityData: securityData,
	})
	if err != nil {
		return p.failInit(err, "3DS initialization failed", orderID)
	}
	fields.set("secure3dhash", hash)

	action := p.Config.BaseURL + EndpointGate
	html := `<!DOCTYPE html><html><head><meta charset="UTF-8"></head><body>` +
		`<form id="garanti-3d-form" method="POST" action="` + escapeHTML(action) + `">` + fields.inputs() + `</form>` +
		`<script>document.getElementById("garanti-3d-form").submit();</script>` +
		`</body></html>`

	return &core.ThreeDSInitResponse{
		Status:             core.StatusPending,
		ThreeDSHTMLContent: html,
		PaymentID:          orderID,
		ConversationID:     orderID,
	}, nil
}

// CompleteThreeDSPayment completes the payment after the successurl / errorurl POST.
func (p *Provider) CompleteThreeDSPayment(ctx context.Context, callbackData any) (*core.PaymentResponse, error) {
	return p.CompleteThreeDSPaymentWithContext(ctx, callbackData, CompleteContext{})
}

// CompleteThreeDSPaymentWithContext completes the payment with the order from the
// merchant's own records, which is never read from the callback body.
//
// The hash is rebuilt from the fields named in hashparams, whose values are joined
// without a separator, so a valid hash alone does not pin each value. The order
// therefore comes from the merchant's records or from the bank: 3D provisions XML
// sales for the context order and amount, which it requires, and the bank's answer
// decides; 3D_PAY is checked with an order inquiry, against the context amount
// when given. A callback that fails the hash, names another terminal or order, or
// claims a payment the bank does not confirm is rejected as INVALID_HASH.
//
// PaymentID and ConversationID are set only for a payment the bank confirmed, and
// on a timeout only when the context order was given; a failure never carries ids
// taken from the callback.
func (p *Provider) CompleteThreeDSPaymentWithContext(ctx context.Context, callbackData any, order CompleteContext) (*core.PaymentResponse, error) {
	data := callbackFields(callbackData)
	untrusted := func(message string, raw any) (*core.PaymentResponse, error) {
		return p.result(core.StatusFailure, "INVALID_HASH", message, raw), nil
	}

	// Error callbacks can come unsigned (a timed-out GarantiPay page posts no
	// hash). A failure that claims nothing is reported with its code; ids are
	// never taken from it.
	if data["hash"] == "" && data["hashparams"] == "" &&
		data["procreturncode"] != "00" && data["response"] != "Approved" {
		errorCode := data["procreturncode"]
		if errorCode == "" {
			errorCode = "MD_STATUS_" + data["mdstatus"]
		}
		return p.result(core.StatusFailure, errorCode,
			firstNonEmpty(data["errmsg"], data["mderrormessage"], "3D Secure payment failed"), callbackData), nil
	}
	if !VerifyCallback(data, p.cfg.StoreKey) {
		return untrusted("Invalid 3D Secure callback hash", callbackData)
	}
	orderID := firstNonEmpty(data["orderid"], data["oid"])
	merchantID, hasMerchantID := data["terminalmerchantid"]
	if orderID == "" ||
		(data["oid"] != "" && data["orderid"] != "" && data["oid"] != data["orderid"]) ||
		data["terminalid"] != p.cfg.TerminalID ||
		(hasMerchantID && merchantID != p.cfg.MerchantID) {
		return untrusted("Callback belongs to a different terminal or order", callbackData)
	}
	if order.OrderID != "" && order.OrderID != orderID {
		return untrusted("Callback belongs to another order than yours", callbackData)
	}

	reached := false
	if p.cfg.SecurityLevel == Level3D {
		accepted := p.cfg.AcceptedMdStatuses
		if accepted == nil {
			accepted = []string{"1"}
		}
		if data["mdstatus"] == "" || !contains(accepted, data["mdstatus"]) {
			return p.result(core.StatusFailure, "MD_STATUS_"+data["mdstatus"],
				firstNonEmpty(data["mderrormessage"], "3D Secure authentication failed"), callbackData), nil
		}

		// The bank charges what this request says, so the order and amount must be
		// the merchant's, not values a re-split callback could carry.
		if order.OrderID == "" || order.Amount == "" {
			return p.failComplete(core.NewValidationError(
				"completeThreeDSPayment() needs the order from your records: "+
					"completeThreeDSPayment(callbackBody, { orderId, amount, currency })", "garanti", nil),
				order.OrderID, reached)
		}
		amount, err := FormatAmount(order.Amount)
		if err != nil {
			return p.failComplete(err, order.OrderID, reached)
		}
		currency, err := CurrencyCode(order.Currency)
		if err != nil {
			return p.failComplete(err, order.OrderID, reached)
		}
		installment := data["txninstallmentcount"]
		if order.Installment != nil {
			installment, err = FormatInstallment(*order.Installment)
			if err != nil {
				return p.failComplete(err, order.OrderID, reached)
			}
		}

		reached = true
		result, err := p.api(ctx, apiRequest{
			orderID:      orderID,
			amount:       amount,
			currencyCode: currency,
			ip:           data["customeripaddress"],
			transaction: []XMLField{
				{Name: "Type", Value: "sales"},
				{Name: "InstallmentCnt", Value: installment},
				{Name: "CardholderPresentCode", Value: "13"},
				{Name: "MotoInd", Value: "N"},
				{Name: "Secure3D", Children: []XMLField{
					{Name: "AuthenticationCode", Value: data["cavv"]},
					{Name: "SecurityLevel", Value: data["eci"]},
					{Name: "TxnID", Value: data["xid"]},
					{Name: "Md", Value: data["md"]},
				}},
			},
		})
		if err != nil {
			return p.failComplete(err, order.OrderID, reached)
		}
		raw := map[string]any{"callback": callbackData, "payment": result}
		if !IsApproved(result) {
			errorCode, errorMessage := errorOf(result)
			return p.result(core.StatusFailure, errorCode, errorMessage, raw), nil
		}
		out := p.result(core.StatusSuccess, "", "", raw)
		out.PaymentID, out.ConversationID = orderID, orderID
		return out, nil
	}

	// 3D_PAY and GARANTI_PAY: the bank already charged the card.
	if data["procreturncode"] != "00" {
		errorCode := data["procreturncode"]
		if errorCode == "" {
			errorCode = "MD_STATUS_" + data["mdstatus"]
		}
		return p.result(core.StatusFailure, errorCode,
			firstNonEmpty(data["errmsg"], data["mderrormessage"], "3D Secure payment failed"), callbackData), nil
	}

	reached = true
	ip, hasIP := data["customeripaddress"]
	if !hasIP {
		ip = "127.0.0.1"
	}
	inquiry, err := p.orderInquiry(ctx, orderID, ip)
	if err != nil {
		return p.failComplete(err, order.OrderID, reached)
	}
	expectedAmount, hasExpectedAmount := data["txnamount"]
	if order.Amount != "" {
		expectedAmount, err = FormatAmount(order.Amount)
		if err != nil {
			return p.failComplete(err, order.OrderID, reached)
		}
		hasExpectedAmount = true
	}
	if !IsCapturedSale(inquiry) || (hasExpectedAmount && inquiry.OrderInquiry["AuthAmount"] != expectedAmount) {
		return untrusted("3D Secure callback does not match the order at the bank",
			map[string]any{"callback": callbackData, "inquiry": inquiry})
	}
	out := p.result(core.StatusSuccess, "", "", map[string]any{"callback": callbackData, "inquiry": inquiry})
	out.PaymentID, out.ConversationID = orderID, orderID
	return out, nil
}

// Refund refunds a payment (XML refund, PROVRFN user). A lower amount than the
// sale is a partial refund. PaymentID is the order id.
func (p *Provider) Refund(ctx context.Context, request *core.RefundRequest) (*core.RefundResponse, error) {
	conversationID := ""
	if request != nil {
		conversationID = request.ConversationID
	}
	if request == nil {
		return p.failRefund(core.NewValidationError("request is required", "garanti", nil), "Refund failed", conversationID)
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

	result, err := p.api(ctx, apiRequest{
		orderID:      request.PaymentID,
		amount:       amount,
		currencyCode: currency,
		ip:           request.IP,
		refund:       true,
		transaction: []XMLField{
			{Name: "Type", Value: "refund"},
			{Name: "InstallmentCnt", Value: ""},
			{Name: "CardholderPresentCode", Value: "0"},
			{Name: "MotoInd", Value: "N"},
		},
	})
	if err != nil {
		return p.failRefund(err, "Refund failed", conversationID)
	}

	out := &core.RefundResponse{ConversationID: conversationID, RawResponse: result}
	if IsApproved(result) {
		out.Status, out.RefundID = core.StatusSuccess, result.Transaction["RetrefNum"]
	} else {
		out.Status = core.StatusFailure
		out.ErrorCode, out.ErrorMessage = errorOf(result)
	}
	out.Code = p.ErrorCodeFor(out.Status, out.ErrorCode)
	return out, nil
}

// Cancel voids a payment the same day (XML void, PROVRFN user). PaymentID is the
// order id.
func (p *Provider) Cancel(ctx context.Context, request *core.CancelRequest) (*core.CancelResponse, error) {
	conversationID := ""
	if request != nil {
		conversationID = request.ConversationID
	}
	if request == nil {
		return p.failCancel(core.NewValidationError("request is required", "garanti", nil), "Cancel failed", conversationID)
	}
	// The amount only has to match HashData; Garanti voids the whole sale.
	amount := "1"
	if request.Price != "" {
		formatted, err := FormatAmount(request.Price)
		if err != nil {
			return p.failCancel(err, "Cancel failed", conversationID)
		}
		amount = formatted
	}
	currency, err := CurrencyCode(request.Currency)
	if err != nil {
		return p.failCancel(err, "Cancel failed", conversationID)
	}

	result, err := p.api(ctx, apiRequest{
		orderID:      request.PaymentID,
		amount:       amount,
		currencyCode: currency,
		ip:           request.IP,
		refund:       true,
		transaction: []XMLField{
			{Name: "Type", Value: "void"},
			{Name: "InstallmentCnt", Value: ""},
			{Name: "CardholderPresentCode", Value: "0"},
			{Name: "MotoInd", Value: "N"},
		},
	})
	if err != nil {
		return p.failCancel(err, "Cancel failed", conversationID)
	}

	out := &core.CancelResponse{ConversationID: conversationID, RawResponse: result}
	if IsApproved(result) {
		out.Status, out.TransactionID = core.StatusSuccess, result.Transaction["RetrefNum"]
	} else {
		out.Status = core.StatusFailure
		out.ErrorCode, out.ErrorMessage = errorOf(result)
	}
	out.Code = p.ErrorCodeFor(out.Status, out.ErrorCode)
	return out, nil
}

// GetPayment queries the order (orderinq). PaymentID is the order id.
func (p *Provider) GetPayment(ctx context.Context, paymentID string) (*core.PaymentResponse, error) {
	inquiry, err := p.orderInquiry(ctx, paymentID, "127.0.0.1")
	if err != nil {
		return p.failPayment(err, "Get payment failed", paymentID)
	}

	out := &core.PaymentResponse{PaymentID: paymentID, ConversationID: paymentID, RawResponse: inquiry}
	if !IsApproved(inquiry) {
		out.Status = core.StatusFailure
		out.ErrorCode, out.ErrorMessage = errorOf(inquiry)
		out.Code = p.ErrorCodeFor(out.Status, out.ErrorCode)
		return out, nil
	}
	out.Status = MapOrderStatus(inquiry.OrderInquiry["Status"])
	if out.Status == core.StatusFailure {
		out.ErrorCode = firstNonEmpty(inquiry.OrderInquiry["ReasonCode"], inquiry.OrderInquiry["Code"])
		out.ErrorMessage = inquiry.OrderInquiry["SysErrMsg"]
	}
	out.Code = p.ErrorCodeFor(out.Status, out.ErrorCode)
	return out, nil
}

// MapOrderStatus maps OrderInqResult Status: APPROVED, DECLINED, VOID (cancelled),
// REFUND (refunded; AuthAmount is what remains).
func MapOrderStatus(status string) core.PaymentStatus {
	switch status {
	case "APPROVED":
		return core.StatusSuccess
	case "DECLINED":
		return core.StatusFailure
	case "VOID", "REFUND":
		return core.StatusCancelled
	default:
		return core.StatusPending
	}
}

// IsCapturedSale reports an approved sale in an order inquiry answer.
func IsCapturedSale(inquiry XMLResponse) bool {
	order := inquiry.OrderInquiry
	return IsApproved(inquiry) &&
		order["Status"] == "APPROVED" &&
		order["ChargeType"] == "S" &&
		numberAboveZero(order["AuthAmount"])
}
