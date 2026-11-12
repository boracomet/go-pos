package est

import (
	"context"
	"strconv"
	"strings"
	"time"

	"github.com/boracomet/go-pos/core"
)

// Provider is the EST (Asseco) virtual POS shared by İş Bankası, Ziraat,
// Halkbank, TEB, Şekerbank, Akbank's older virtual POS and Halkbank Macedonia.
// One provider, configured per bank.
//
//   - CreatePayment:          XML API Auth (non-3D)
//   - InitThreeDSPayment:     auto-submitting form to /fim/est3Dgate, hash ver3
//   - CompleteThreeDSPayment: verifies the callback hash; 3d and 3d_host charge
//     with XML Auth, 3d_pay and 3d_pay_hosting are confirmed with an order status query
//   - Refund Credit, Cancel Void, GetPayment order status (ORDERSTATUS)
//
// PaymentID is the order id (oid): the ConversationID passed in, or a generated one.
type Provider struct {
	core.BaseProvider
	cfg    Config
	client *core.HTTPClient
}

// New builds an EST provider. BaseURL defaults to the bank's host, or the shared
// test host when TestMode is set.
func New(cfg Config) (*Provider, error) {
	if cfg.BaseURL == "" {
		if urls, ok := DefaultURLs[cfg.Bank]; ok {
			cfg.BaseURL = urls.Production
			if cfg.TestMode {
				cfg.BaseURL = urls.Sandbox
			}
		}
	}
	if cfg.StoreType == "" {
		cfg.StoreType = Store3D
	}

	var missing []string
	if cfg.ClientID == "" {
		missing = append(missing, "clientId")
	}
	if cfg.Username == "" {
		missing = append(missing, "username")
	}
	if cfg.Password == "" {
		missing = append(missing, "password")
	}
	if cfg.StoreKey == "" {
		missing = append(missing, "storeKey")
	}
	if len(missing) > 0 {
		return nil, core.NewConfigurationError("EST configuration is missing: "+strings.Join(missing, ", "), "est")
	}
	if cfg.BaseURL == "" {
		return nil, core.NewConfigurationError("EST baseUrl (or a known bank) is required", "est")
	}
	known := false
	for _, storeType := range StoreTypes {
		if storeType == cfg.StoreType {
			known = true
			break
		}
	}
	if !known {
		return nil, core.NewConfigurationError("Unknown EST storeType: "+string(cfg.StoreType), "est")
	}

	p := &Provider{BaseProvider: core.NewBaseProvider("est", cfg.ProviderConfig), cfg: cfg}
	p.ErrorCodes = core.ISO8583ErrorCodes
	p.client = p.NewHTTPClient(30*time.Second, map[string]string{
		"Content-Type": "text/xml; charset=ISO-8859-9",
	})
	return p, nil
}

func (p *Provider) lang() string {
	if strings.HasPrefix(strings.ToLower(p.Config.Locale), "en") {
		return "en"
	}
	return "tr"
}

// api sends a CC5Request to the XML API with the API user's credentials.
func (p *Provider) api(ctx context.Context, fields []XMLField, retryable bool) (XMLResponse, error) {
	body := BuildXML(append([]XMLField{
		{Name: "Name", Value: p.cfg.Username},
		{Name: "Password", Value: p.cfg.Password},
		{Name: "ClientId", Value: p.cfg.ClientID},
	}, fields...))
	response, err := p.client.Post(ctx, EndpointAPI, body, core.RequestConfig{
		ResponseType: "text",
		Retryable:    retryable,
	})
	if err != nil {
		return XMLResponse{}, err
	}
	text, _ := response.Data.(string)
	return ParseXMLResponse(text)
}

func (p *Provider) extra(fields []XMLField) []XMLField {
	if p.cfg.Department != nil {
		return append(fields, XMLField{Name: "BOLUM", Value: strconv.Itoa(*p.cfg.Department)})
	}
	return fields
}

// orderStatus queries the order (ORDERSTATUS = SOR). Read-only, so retryable.
func (p *Provider) orderStatus(ctx context.Context, orderID string) (XMLResponse, error) {
	return p.api(ctx, []XMLField{
		{Name: "OrderId", Value: orderID},
		{Name: "Mode", Value: "P"},
		{Name: "Extra", Children: []XMLField{{Name: "ORDERSTATUS", Value: "SOR"}}},
	}, true)
}

func errorOf(fields map[string]string) (errorCode, errorMessage string) {
	message := fields["ErrMsg"]
	if message == "" {
		message = fields["mdErrorMsg"]
	}
	return fields["ProcReturnCode"], message
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if value != "" {
			return value
		}
	}
	return ""
}

func contains(values []string, value string) bool {
	for _, candidate := range values {
		if candidate == value {
			return true
		}
	}
	return false
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
	out := core.ApplyFailure("est", err, fallback, &core.PaymentResponse{
		PaymentID: paymentID, ConversationID: paymentID,
	})
	out.Code = p.ErrorCodeFor(out.Status, out.ErrorCode)
	return out, nil
}

func (p *Provider) failInit(err error, fallback, paymentID string) (*core.ThreeDSInitResponse, error) {
	out := core.ApplyFailure("est", err, fallback, &core.ThreeDSInitResponse{
		PaymentID: paymentID, ConversationID: paymentID,
	})
	out.Code = p.ErrorCodeFor(out.Status, out.ErrorCode)
	return out, nil
}

func (p *Provider) failRefund(err error, fallback, conversationID string) (*core.RefundResponse, error) {
	out := core.ApplyFailure("est", err, fallback, &core.RefundResponse{ConversationID: conversationID})
	out.Code = p.ErrorCodeFor(out.Status, out.ErrorCode)
	return out, nil
}

func (p *Provider) failCancel(err error, fallback, conversationID string) (*core.CancelResponse, error) {
	out := core.ApplyFailure("est", err, fallback, &core.CancelResponse{ConversationID: conversationID})
	out.Code = p.ErrorCodeFor(out.Status, out.ErrorCode)
	return out, nil
}

// failComplete keeps the order ids only when the bank was reached for this order
// and the outcome is unknown, so the order can be checked with GetPayment.
func (p *Provider) failComplete(err error, orderID string, reached bool) (*core.PaymentResponse, error) {
	out := core.ApplyFailure("est", err, "3D Secure completion failed", &core.PaymentResponse{})
	out.Code = p.ErrorCodeFor(out.Status, out.ErrorCode)
	if reached && out.Status == core.StatusPending {
		out.PaymentID, out.ConversationID = orderID, orderID
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

// CreatePayment charges the card without 3D Secure (XML API Auth).
func (p *Provider) CreatePayment(ctx context.Context, request *core.PaymentRequest) (*core.PaymentResponse, error) {
	conversationID := ""
	if request != nil {
		conversationID = request.ConversationID
	}
	orderID := orderIDOf(conversationID)

	if request == nil {
		return p.failPayment(core.NewValidationError("request is required", "est", nil), "Payment failed", orderID)
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

	inquiry, err := p.api(ctx, []XMLField{
		{Name: "Type", Value: "Auth"},
		{Name: "IPAddress", Value: request.Buyer.IP},
		{Name: "Email", Value: request.Buyer.Email},
		{Name: "OrderId", Value: orderID},
		{Name: "Total", Value: amount},
		{Name: "Currency", Value: currency},
		{Name: "Taksit", Value: installment},
		{Name: "Number", Value: compactCardNumber(card.CardNumber)},
		{Name: "Expires", Value: expiry.Month + "/" + expiry.Year},
		{Name: "Cvv2Val", Value: card.CVC},
		{Name: "Mode", Value: "P"},
		{Name: "BillTo", Children: []XMLField{
			{Name: "Name", Value: strings.TrimSpace(request.Buyer.Name + " " + request.Buyer.Surname)},
		}},
		{Name: "Extra", Children: p.extra([]XMLField{
			{Name: "CARDHOLDERNAME", Value: card.CardHolderName},
		})},
	}, false)
	if err != nil {
		return p.failPayment(err, "Payment failed", orderID)
	}

	fields := inquiry.Fields
	if IsApproved(fields) {
		out := p.result(core.StatusSuccess, "", "", fields)
		out.PaymentID, out.ConversationID = orderID, orderID
		return out, nil
	}
	errorCode, errorMessage := errorOf(fields)
	out := p.result(core.StatusFailure, errorCode, errorMessage, fields)
	out.PaymentID, out.ConversationID = orderID, orderID
	return out, nil
}

// InitThreeDSPayment builds the auto-submitting form for the bank's 3D gate.
func (p *Provider) InitThreeDSPayment(ctx context.Context, request *core.ThreeDSPaymentRequest) (*core.ThreeDSInitResponse, error) {
	return p.InitThreeDSPaymentWithOptions(ctx, request, InitOptions{})
}

// InitThreeDSPaymentWithOptions builds the 3D form with an optional failUrl and
// an alternative (Girogate) payment method instead of a card. With
// 3d_pay_hosting / 3d_host the bank collects the card on its own page, so no
// card data is sent.
func (p *Provider) InitThreeDSPaymentWithOptions(ctx context.Context, request *core.ThreeDSPaymentRequest, options InitOptions) (*core.ThreeDSInitResponse, error) {
	conversationID := ""
	if request != nil {
		conversationID = request.ConversationID
	}
	orderID := orderIDOf(conversationID)

	if request == nil {
		return p.failInit(core.NewValidationError("request is required", "est", nil), "3DS initialization failed", orderID)
	}
	girogate := options.Girogate
	if girogate != nil && IsMerchantChargedStoreType(p.cfg.StoreType) {
		return p.failInit(core.NewValidationError("Girogate needs the 3d_pay or 3d_pay_hosting store type", "est", nil),
			"3DS initialization failed", orderID)
	}
	hosted := IsHostedStoreType(p.cfg.StoreType)
	rules := core.PaymentValidationRules{}
	if !hosted && girogate == nil {
		rules.Card = true
	}
	if err := p.ValidatePayment(&request.PaymentRequest, rules); err != nil {
		return p.failInit(err, "3DS initialization failed", orderID)
	}
	// The outer CallbackURL shadows the embedded one; both name the same field.
	callbackURL := request.CallbackURL
	if callbackURL == "" {
		callbackURL = request.PaymentRequest.CallbackURL
	}
	if callbackURL == "" {
		return p.failInit(core.NewValidationError("callbackUrl is required for 3D Secure payments", "est", nil),
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
	random, err := GenerateRandom()
	if err != nil {
		return p.failInit(err, "3DS initialization failed", orderID)
	}

	var fields formFields
	fields.set("clientid", p.cfg.ClientID)
	fields.set("storetype", string(p.cfg.StoreType))
	fields.set("amount", amount)
	fields.set("oid", orderID)
	fields.set("okUrl", callbackURL)
	failURL := options.FailURL
	if failURL == "" {
		failURL = callbackURL
	}
	fields.set("failUrl", failURL)
	fields.set("TranType", "Auth")
	fields.set("currency", currency)
	fields.set("taksit", installment)
	fields.set("rnd", random)
	fields.set("lang", p.lang())
	fields.set("hashAlgorithm", "ver3")
	fields.set("refreshTime", "0")
	if request.Buyer.Email != "" {
		fields.set("Email", request.Buyer.Email)
	}

	if girogate != nil {
		girogateFields, err := BuildGirogateFields(GirogateOptions{
			Method:     string(girogate.Method),
			Country:    girogate.Country,
			Email:      firstNonEmpty(girogate.Email, request.Buyer.Email),
			Phone:      firstNonEmpty(girogate.Phone, request.Buyer.GSMNumber),
			ConsumerID: firstNonEmpty(girogate.ConsumerID, request.Buyer.ID),
		})
		if err != nil {
			return p.failInit(err, "3DS initialization failed", orderID)
		}
		fields = append(fields, girogateFields...)
	} else if !hosted {
		card, err := p.CardOf(&request.PaymentRequest)
		if err != nil {
			return p.failInit(err, "3DS initialization failed", orderID)
		}
		cardNumber := compactCardNumber(card.CardNumber)
		expiry, err := FormatExpiry(card.ExpireMonth, card.ExpireYear)
		if err != nil {
			return p.failInit(err, "3DS initialization failed", orderID)
		}
		fields.set("pan", cardNumber)
		fields.set("Ecom_Payment_Card_ExpDate_Month", expiry.Month)
		fields.set("Ecom_Payment_Card_ExpDate_Year", expiry.Year)
		fields.set("cv2", card.CVC)
		fields.set("cardType", CardType(cardNumber))
		fields.set("cardHolderName", card.CardHolderName)
	}

	hash, err := CreateHash(fields.values(), p.cfg.StoreKey)
	if err != nil {
		return p.failInit(err, "3DS initialization failed", orderID)
	}
	fields.set("hash", hash)

	action := p.Config.BaseURL + EndpointGate
	html := `<!DOCTYPE html><html><head><meta charset="UTF-8"></head><body>` +
		`<form id="est-3d-form" method="POST" action="` + escapeHTML(action) + `">` + fields.inputs() + `</form>` +
		`<script>document.getElementById("est-3d-form").submit();</script>` +
		`</body></html>`

	out := &core.ThreeDSInitResponse{
		Status:             core.StatusPending,
		ThreeDSHTMLContent: html,
		PaymentID:          orderID,
		ConversationID:     orderID,
	}
	return out, nil
}

// CompleteThreeDSPayment completes the payment after the okUrl / failUrl POST.
func (p *Provider) CompleteThreeDSPayment(ctx context.Context, callbackData any) (*core.PaymentResponse, error) {
	return p.CompleteThreeDSPaymentWithContext(ctx, callbackData, CompleteContext{})
}

// CompleteThreeDSPaymentWithContext completes the payment with the order from
// the merchant's own records, which never comes from the callback body.
//
// The ver3 hash covers the values but not the field names, so a valid hash alone
// does not pin which value is the order id. The result is therefore confirmed
// with the bank: 3d and 3d_host only authenticate the card, so they charge with
// XML Auth (the bank's answer decides); with 3d_pay and 3d_pay_hosting the bank
// charged the card, which is checked with an order status query. A callback that
// fails the hash, names another store, or claims a payment the bank does not
// confirm is rejected as INVALID_HASH.
//
// PaymentID and ConversationID are set only for a payment the bank confirmed,
// and on a timeout after the bank was reached for this order; a failure never
// carries ids taken from the callback.
func (p *Provider) CompleteThreeDSPaymentWithContext(ctx context.Context, callbackData any, order CompleteContext) (*core.PaymentResponse, error) {
	data := callbackFields(callbackData)
	untrusted := func(message string, raw any) (*core.PaymentResponse, error) {
		return p.result(core.StatusFailure, "INVALID_HASH", message, raw), nil
	}

	if !VerifyCallback(data, p.cfg.StoreKey) {
		return untrusted("Invalid 3D Secure callback hash", callbackData)
	}
	orderID := data["oid"]
	if orderID == "" || data["clientid"] != p.cfg.ClientID {
		return untrusted("Callback belongs to a different store or order", callbackData)
	}

	reached := false
	if IsMerchantChargedStoreType(p.cfg.StoreType) {
		accepted := p.cfg.AcceptedMdStatuses
		if accepted == nil {
			accepted = []string{"1"}
		}
		mdStatus := data["mdStatus"]
		if mdStatus == "" || !contains(accepted, mdStatus) {
			return p.result(core.StatusFailure, "MD_STATUS_"+mdStatus,
				firstNonEmpty(data["mdErrorMsg"], data["ErrMsg"], "3D Secure authentication failed"), callbackData), nil
		}

		reached = true
		payment, err := p.api(ctx, []XMLField{
			{Name: "Type", Value: "Auth"},
			{Name: "OrderId", Value: orderID},
			{Name: "Total", Value: data["amount"]},
			{Name: "Currency", Value: data["currency"]},
			{Name: "Taksit", Value: data["taksit"]},
			{Name: "Number", Value: data["md"]},
			{Name: "PayerTxnId", Value: data["xid"]},
			{Name: "PayerSecurityLevel", Value: data["eci"]},
			{Name: "PayerAuthenticationCode", Value: data["cavv"]},
			{Name: "Mode", Value: "P"},
			{Name: "Extra", Children: p.extra(nil)},
		}, false)
		if err != nil {
			return p.failComplete(err, orderID, reached)
		}
		fields := payment.Fields
		raw := map[string]any{"callback": callbackData, "payment": fields}
		if !IsApproved(fields) || (fields["OrderId"] != "" && fields["OrderId"] != orderID) {
			errorCode, errorMessage := errorOf(fields)
			return p.result(core.StatusFailure, errorCode, errorMessage, raw), nil
		}
		out := p.result(core.StatusSuccess, "", "", raw)
		out.PaymentID, out.ConversationID = orderID, orderID
		return out, nil
	}

	// A Girogate method that settles later (bank transfer, voucher): nothing is
	// paid yet. The hash cannot tell which order is pending, so the order id is
	// returned only when it matches the order from the merchant's records.
	if strings.ToUpper(data["Response"]) == "PENDING" {
		if order.OrderID != "" && order.OrderID != orderID {
			return untrusted("Callback belongs to a different order", callbackData)
		}
		out := p.result(core.StatusPending, "",
			firstNonEmpty(data["ErrMsg"], "Waiting for the payment method to confirm"), callbackData)
		if order.OrderID != "" {
			out.PaymentID, out.ConversationID = orderID, orderID
		}
		return out, nil
	}

	// 3d_pay / 3d_pay_hosting: the bank already charged the card.
	if !IsApproved(data) {
		errorCode, errorMessage := errorOf(data)
		if errorCode == "" {
			errorCode = "MD_STATUS_" + data["mdStatus"]
		}
		return p.result(core.StatusFailure, errorCode, errorMessage, callbackData), nil
	}

	reached = true
	inquiry, err := p.orderStatus(ctx, orderID)
	if err != nil {
		return p.failComplete(err, orderID, reached)
	}
	if !IsCapturedSale(inquiry) {
		return untrusted("3D Secure callback does not match the order at the bank",
			map[string]any{"callback": callbackData, "inquiry": inquiry})
	}
	out := p.result(core.StatusSuccess, "", "", map[string]any{"callback": callbackData, "inquiry": inquiry})
	out.PaymentID, out.ConversationID = orderID, orderID
	return out, nil
}

// Refund refunds a payment (XML API Credit). PaymentID is the order id.
func (p *Provider) Refund(ctx context.Context, request *core.RefundRequest) (*core.RefundResponse, error) {
	conversationID := ""
	if request != nil {
		conversationID = request.ConversationID
	}
	if request == nil {
		return p.failRefund(core.NewValidationError("request is required", "est", nil), "Refund failed", conversationID)
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

	inquiry, err := p.api(ctx, []XMLField{
		{Name: "Type", Value: "Credit"},
		{Name: "OrderId", Value: request.PaymentID},
		{Name: "Total", Value: amount},
		{Name: "Currency", Value: currency},
	}, false)
	if err != nil {
		return p.failRefund(err, "Refund failed", conversationID)
	}

	fields := inquiry.Fields
	out := &core.RefundResponse{ConversationID: conversationID, RawResponse: fields}
	if IsApproved(fields) {
		out.Status, out.RefundID = core.StatusSuccess, fields["TransId"]
	} else {
		out.Status = core.StatusFailure
		out.ErrorCode, out.ErrorMessage = errorOf(fields)
	}
	out.Code = p.ErrorCodeFor(out.Status, out.ErrorCode)
	return out, nil
}

// Cancel voids a payment before end of day (XML API Void). PaymentID is the order id.
func (p *Provider) Cancel(ctx context.Context, request *core.CancelRequest) (*core.CancelResponse, error) {
	conversationID := ""
	if request != nil {
		conversationID = request.ConversationID
	}
	if request == nil {
		return p.failCancel(core.NewValidationError("request is required", "est", nil), "Cancel failed", conversationID)
	}

	inquiry, err := p.api(ctx, []XMLField{
		{Name: "Type", Value: "Void"},
		{Name: "OrderId", Value: request.PaymentID},
	}, false)
	if err != nil {
		return p.failCancel(err, "Cancel failed", conversationID)
	}

	fields := inquiry.Fields
	out := &core.CancelResponse{ConversationID: conversationID, RawResponse: fields}
	if IsApproved(fields) {
		out.Status, out.TransactionID = core.StatusSuccess, fields["TransId"]
	} else {
		out.Status = core.StatusFailure
		out.ErrorCode, out.ErrorMessage = errorOf(fields)
	}
	out.Code = p.ErrorCodeFor(out.Status, out.ErrorCode)
	return out, nil
}

// GetPayment queries the order status (ORDERSTATUS). PaymentID is the order id.
func (p *Provider) GetPayment(ctx context.Context, paymentID string) (*core.PaymentResponse, error) {
	inquiry, err := p.orderStatus(ctx, paymentID)
	if err != nil {
		return p.failPayment(err, "Get payment failed", paymentID)
	}

	status := OrderStatus(inquiry)
	out := &core.PaymentResponse{
		Status: status, PaymentID: paymentID, ConversationID: paymentID, RawResponse: inquiry,
	}
	if status == core.StatusFailure {
		if inquiry.Extra["TRANS_STAT"] == "D" {
			out.ErrorCode, out.ErrorMessage = inquiry.Extra["PROC_RET_CD"], "Declined by the bank"
		} else {
			out.ErrorCode, out.ErrorMessage = errorOf(inquiry.Fields)
		}
	}
	out.Code = p.ErrorCodeFor(status, out.ErrorCode)
	return out, nil
}

// OrderStatus maps an order status answer: voided (TRANS_STAT V) or refunded
// (charge type C) orders are cancelled, declined ones (D) failed, captured sales
// succeeded. An answer without a record (unknown order) is a failure; anything
// else, such as a pre-authorization, is pending.
func OrderStatus(inquiry XMLResponse) core.PaymentStatus {
	fields, extra := inquiry.Fields, inquiry.Extra
	if extra["TRANS_STAT"] == "" {
		return core.StatusFailure
	}
	if extra["TRANS_STAT"] == "V" {
		return core.StatusCancelled
	}
	if extra["CHARGE_TYPE_CD"] == "C" {
		// The answer shows the last approved transaction: a (partial) refund.
		return core.StatusCancelled
	}
	if extra["TRANS_STAT"] == "D" {
		return core.StatusFailure
	}
	if !IsApproved(fields) {
		return core.StatusFailure
	}
	if IsCapturedSale(inquiry) {
		return core.StatusSuccess
	}
	return core.StatusPending
}

// IsCapturedSale reports a captured sale in an order status answer: TRANS_STAT
// S, C or A, charge type S (sale) and a captured amount.
func IsCapturedSale(inquiry XMLResponse) bool {
	return IsApproved(inquiry.Fields) &&
		contains([]string{"S", "C", "A"}, inquiry.Extra["TRANS_STAT"]) &&
		inquiry.Extra["CHARGE_TYPE_CD"] == "S" &&
		numberAboveZero(inquiry.Extra["CAPTURE_AMT"])
}
