package yapikredi

import (
	"context"
	"slices"
	"strings"
	"time"

	"github.com/boracomet/go-pos/core"
)

const (
	providerName = "yapikredi"
	// failureLabel is the name used in failure messages.
	failureLabel = "Yapı Kredi"
)

// SandboxBaseURL is the Posnet test host.
const SandboxBaseURL = "https://setmpos.ykb.com"

// ProductionBaseURL is the Posnet production host.
const ProductionBaseURL = "https://posnet.yapikredi.com.tr"

// WithDefaults fills the base URL for the mode, as the provider factory does.
func WithDefaults(cfg Config, sandbox bool) Config {
	if cfg.BaseURL == "" {
		if sandbox {
			cfg.BaseURL = SandboxBaseURL
		} else {
			cfg.BaseURL = ProductionBaseURL
		}
	}
	return cfg
}

// Provider is the Yapı Kredi virtual POS (Posnet XML API).
//
//   - CreatePayment:          XML sale (non-3D)
//   - InitThreeDSPayment:     oosRequestData, then an auto-submitting form to the
//     3D service with the bank's encrypted data
//   - CompleteThreeDSPayment: oosResolveMerchantData then oosTranData, both signed
//     with a MAC over the order id, amount and currency
//   - Refund:                 return
//   - Cancel:                 reverse
//   - GetPayment:             agreement, by order id
//
// PaymentID of a successful payment is the bank's host log key, which Refund and
// Cancel take; ConversationID is the merchant's order id (at most 20 letters or
// digits for a 3D Secure payment, 24 for a non-3D sale).
type Provider struct {
	core.BaseProvider
	config Config
	client *core.HTTPClient
}

var _ core.Provider = (*Provider)(nil)

// New builds the provider, rejecting a configuration without credentials or base URL.
func New(cfg Config) (*Provider, error) {
	var missing []string
	if cfg.MerchantID == "" {
		missing = append(missing, "merchantId")
	}
	if cfg.TerminalID == "" {
		missing = append(missing, "terminalId")
	}
	if cfg.PosnetID == "" {
		missing = append(missing, "posnetId")
	}
	if cfg.EncKey == "" {
		missing = append(missing, "encKey")
	}
	if len(missing) > 0 {
		return nil, core.NewConfigurationError(
			"Yapı Kredi configuration is missing: "+strings.Join(missing, ", "), providerName)
	}
	if cfg.BaseURL == "" {
		return nil, core.NewConfigurationError("Yapı Kredi baseUrl is required", providerName)
	}

	p := &Provider{
		BaseProvider: core.NewBaseProvider(providerName, cfg.ProviderConfig),
		config:       cfg,
	}
	p.ErrorCodes = ErrorCodes
	// The bank may take up to 45 seconds and recommends a 1 minute timeout
	p.client = p.NewHTTPClient(60*time.Second, map[string]string{
		"Content-Type": "application/x-www-form-urlencoded; charset=utf-8",
	})
	return p, nil
}

// generateOrderID returns 20 uppercase hex characters: 80 random bits fit both
// Posnet's 20-character XID and its 24-character order id.
func generateOrderID() (string, error) {
	value, err := core.RandomHex(10)
	if err != nil {
		return "", err
	}
	return strings.ToUpper(value), nil
}

func (p *Provider) lang() string {
	if strings.HasPrefix(strings.ToLower(orDefault(p.Config.Locale, "tr")), "en") {
		return "en"
	}
	return "tr"
}

// api posts a posnetRequest and parses the posnetResponse. No request is
// retryable: a timeout after the bank received it would charge or refund twice.
func (p *Provider) api(ctx context.Context, fields []XMLField) (XMLResponse, error) {
	body := BuildRequestBody(append([]XMLField{
		{Name: "mid", Value: p.config.MerchantID},
		{Name: "tid", Value: p.config.TerminalID},
	}, fields...))
	response, err := p.client.Post(ctx, EndpointAPI, body, core.RequestConfig{ResponseType: "text"})
	if err != nil {
		return XMLResponse{}, err
	}
	text, _ := response.Data.(string)
	return ParseXMLResponse(text)
}

func errorOf(result XMLResponse) (string, string) {
	return result.Fields["respCode"], result.Fields["respText"]
}

func (p *Provider) paymentFailure(err error, orderID string) *core.PaymentResponse {
	result := core.ApplyFailure(failureLabel, err, "Payment failed", &core.PaymentResponse{})
	result.ConversationID = orderID
	result.Code = p.ErrorCodeFor(result.Status, result.ErrorCode)
	return result
}

// CreatePayment charges the card without 3D Secure (XML sale).
func (p *Provider) CreatePayment(ctx context.Context, request *core.PaymentRequest) (*core.PaymentResponse, error) {
	if request == nil {
		request = &core.PaymentRequest{}
	}
	orderID := request.ConversationID
	if orderID == "" {
		generated, err := generateOrderID()
		if err != nil {
			return nil, err
		}
		orderID = generated
	}
	result, err := p.sale(ctx, request, orderID)
	if err != nil {
		return p.paymentFailure(err, orderID), nil
	}
	response := &core.PaymentResponse{ConversationID: orderID, RawResponse: result}
	if IsApproved(result) {
		response.Status = core.StatusSuccess
		response.PaymentID = result.Fields["hostlogkey"]
	} else {
		response.Status = core.StatusFailure
		response.ErrorCode, response.ErrorMessage = errorOf(result)
	}
	response.Code = p.ErrorCodeFor(response.Status, response.ErrorCode)
	return response, nil
}

func (p *Provider) sale(ctx context.Context, request *core.PaymentRequest, orderID string) (XMLResponse, error) {
	if err := p.ValidatePayment(request, core.PaymentValidationRules{Card: true}); err != nil {
		return XMLResponse{}, err
	}
	card, err := p.CardOf(request)
	if err != nil {
		return XMLResponse{}, err
	}
	amount, err := FormatAmount(orDefault(request.PaidPrice, request.Price))
	if err != nil {
		return XMLResponse{}, err
	}
	currency, err := CurrencyCode(request.Currency)
	if err != nil {
		return XMLResponse{}, err
	}
	expDate, err := FormatExpiry(card.ExpireMonth, card.ExpireYear)
	if err != nil {
		return XMLResponse{}, err
	}
	installment, err := FormatInstallment(request.Installment)
	if err != nil {
		return XMLResponse{}, err
	}
	bankOrderID, err := ToOrderID(orderID)
	if err != nil {
		return XMLResponse{}, err
	}
	return p.api(ctx, []XMLField{
		{Name: "tranDateRequired", Value: "1"},
		{Name: "sale", Children: []XMLField{
			{Name: "amount", Value: amount},
			{Name: "ccno", Value: whitespaceRe.ReplaceAllString(card.CardNumber, "")},
			{Name: "currencyCode", Value: currency},
			{Name: "cvc", Value: card.CVC},
			{Name: "expDate", Value: expDate},
			{Name: "installment", Value: installment},
			{Name: "orderID", Value: bankOrderID},
		}},
	})
}

// InitThreeDSPayment starts a 3D Secure payment: the card is sent to
// oosRequestData, and the bank's encrypted answer is posted from the customer's
// browser to the 3D service.
func (p *Provider) InitThreeDSPayment(ctx context.Context, request *core.ThreeDSPaymentRequest) (*core.ThreeDSInitResponse, error) {
	if request == nil {
		request = &core.ThreeDSPaymentRequest{}
	}
	orderID := request.ConversationID
	fail := func(err error) *core.ThreeDSInitResponse {
		result := core.ApplyFailure(failureLabel, err, "3DS initialization failed", &core.ThreeDSInitResponse{})
		result.ConversationID = orderID
		result.Code = p.ErrorCodeFor(result.Status, result.ErrorCode)
		return result
	}
	if orderID == "" {
		generated, err := generateOrderID()
		if err != nil {
			return nil, err
		}
		orderID = generated
	}
	result, err := p.oosRequestData(ctx, request, orderID)
	if err != nil {
		return fail(err), nil
	}
	data := result.OOSRequestData
	if !IsApproved(result) || data["data1"] == "" || data["data2"] == "" || data["sign"] == "" {
		response := &core.ThreeDSInitResponse{Status: core.StatusFailure, ConversationID: orderID, RawResponse: result}
		response.ErrorCode, response.ErrorMessage = errorOf(result)
		response.Code = p.ErrorCodeFor(response.Status, response.ErrorCode)
		return response, nil
	}

	fields := []struct{ name, value string }{
		{"mid", p.config.MerchantID},
		{"posnetID", p.config.PosnetID},
		{"posnetData", data["data1"]},
		{"posnetData2", data["data2"]},
		{"digest", data["sign"]},
		{"merchantReturnURL", request.CallbackURL},
		{"lang", p.lang()},
		{"url", ""},
		{"vftCode", ""},
		{"useJokerVadaa", "1"},
	}
	var inputs strings.Builder
	for _, field := range fields {
		inputs.WriteString(`<input type="hidden" name="` + escapeHTML(field.name) + `" value="` + escapeHTML(field.value) + `">`)
	}
	action := p.config.BaseURL + EndpointGate
	html := `<!DOCTYPE html><html><head><meta charset="UTF-8"></head><body>` +
		`<form id="yapikredi-3d-form" method="POST" action="` + escapeHTML(action) + `">` + inputs.String() + `</form>` +
		`<script>document.getElementById("yapikredi-3d-form").submit();</script>` +
		`</body></html>`

	response := &core.ThreeDSInitResponse{
		Status:             core.StatusPending,
		ThreeDSHTMLContent: html,
		ConversationID:     orderID,
	}
	response.Code = p.ErrorCodeFor(response.Status, response.ErrorCode)
	return response, nil
}

func (p *Provider) oosRequestData(ctx context.Context, request *core.ThreeDSPaymentRequest, orderID string) (XMLResponse, error) {
	if err := p.ValidatePayment(&request.PaymentRequest, core.PaymentValidationRules{Card: true}); err != nil {
		return XMLResponse{}, err
	}
	card, err := p.CardOf(&request.PaymentRequest)
	if err != nil {
		return XMLResponse{}, err
	}
	if request.CallbackURL == "" {
		return XMLResponse{}, core.NewValidationError("callbackUrl is required for 3D Secure payments", providerName, nil)
	}
	expDate, err := FormatExpiry(card.ExpireMonth, card.ExpireYear)
	if err != nil {
		return XMLResponse{}, err
	}
	amount, err := FormatAmount(orDefault(request.PaidPrice, request.Price))
	if err != nil {
		return XMLResponse{}, err
	}
	currency, err := CurrencyCode(request.Currency)
	if err != nil {
		return XMLResponse{}, err
	}
	installment, err := FormatInstallment(request.Installment)
	if err != nil {
		return XMLResponse{}, err
	}
	xid, err := ToXID(orderID)
	if err != nil {
		return XMLResponse{}, err
	}
	return p.api(ctx, []XMLField{
		{Name: "oosRequestData", Children: []XMLField{
			{Name: "posnetid", Value: p.config.PosnetID},
			{Name: "ccno", Value: whitespaceRe.ReplaceAllString(card.CardNumber, "")},
			{Name: "expDate", Value: expDate},
			{Name: "cvc", Value: card.CVC},
			{Name: "amount", Value: amount},
			{Name: "currencyCode", Value: currency},
			{Name: "installment", Value: installment},
			{Name: "XID", Value: xid},
			{Name: "cardHolderName", Value: card.CardHolderName},
			{Name: "tranType", Value: "Sale"},
		}},
	})
}

// CompleteThreeDSPayment completes the payment after the merchantReturnURL POST.
//
// Pass the callback together with the order from the merchant's own records:
// a CompleteRequest carrying BankPacket, MerchantPacket, Sign, OrderID, Amount
// and Currency. The bank resolves its encrypted packets with
// oosResolveMerchantData; the MAC over the order id, amount and currency must
// match, and the resolved XID, amount and currency must equal the order. The
// card is charged with oosTranData only for an accepted mdStatus, and the bank's
// answer decides.
//
// PaymentID (host log key) and ConversationID are set only for a payment the bank
// approved, and on a timeout after the bank was reached.
//
// callbackData is a CompleteRequest, or the callback POST as a map[string]string
// or map[string]any carrying the same fields.
func (p *Provider) CompleteThreeDSPayment(ctx context.Context, callbackData any) (*core.PaymentResponse, error) {
	data := completeFields(callbackData)
	raw := callbackData
	if raw == nil {
		raw = map[string]any{}
	}
	if data.BankPacket == "" || data.MerchantPacket == "" || data.Sign == "" {
		return p.completeRejected(raw, "INVALID_HASH",
			"Callback is missing BankPacket, MerchantPacket or Sign", "", ""), nil
	}
	if data.OrderID == "" || data.Amount == "" {
		return p.completeRejected(raw, "VALIDATION_ERROR",
			"Yapı Kredi needs the order: call completeThreeDSPayment({ ...callbackBody, orderId, amount, currency }) with values from your records",
			"", ""), nil
	}

	orderID := data.OrderID
	reached := false
	fail := func(err error) *core.PaymentResponse {
		result := core.ApplyFailure(failureLabel, err, "3D Secure completion failed", &core.PaymentResponse{})
		// A timeout while charging: the outcome is unknown
		if reached && result.Status == core.StatusPending {
			result.ConversationID = orderID
		}
		result.Code = p.ErrorCodeFor(result.Status, result.ErrorCode)
		return result
	}

	xid, err := ToXID(orderID)
	if err != nil {
		return fail(err), nil
	}
	minorAmount, err := FormatAmount(data.Amount)
	if err != nil {
		return fail(err), nil
	}
	currency, err := CurrencyCode(data.Currency)
	if err != nil {
		return fail(err), nil
	}
	macFields := MACFields{
		XID:        xid,
		Amount:     minorAmount,
		Currency:   currency,
		MerchantID: p.config.MerchantID,
		TerminalID: p.config.TerminalID,
		EncKey:     p.config.EncKey,
	}
	mac, err := CreateMac(macFields)
	if err != nil {
		return fail(err), nil
	}
	packets := []XMLField{
		{Name: "bankData", Value: data.BankPacket},
		{Name: "merchantData", Value: data.MerchantPacket},
		{Name: "sign", Value: data.Sign},
		{Name: "wpAmount", Value: "0"},
		{Name: "mac", Value: mac},
	}

	resolved, err := p.api(ctx, []XMLField{{Name: "oosResolveMerchantData", Children: packets}})
	if err != nil {
		return fail(err), nil
	}
	answer := resolved.OOSResolveMerchantData
	matches := IsApproved(resolved) && answer["xid"] == xid && answer["amount"] == minorAmount
	if matches {
		if value, present := answer["currency"]; present && value != currency {
			matches = false
		}
	}
	if matches && answer["mac"] != "" {
		expected, err := CreateResponseMac(answer["mdStatus"], macFields)
		if err != nil {
			return fail(err), nil
		}
		matches = core.SafeEqual(answer["mac"], expected)
	}
	if !matches {
		return p.completeRejected(map[string]any{"callback": raw, "resolve": resolved},
			"INVALID_HASH", "The 3D Secure result does not match the order", "", ""), nil
	}

	accepted := p.config.AcceptedMdStatuses
	if accepted == nil {
		accepted = []string{"1"}
	}
	mdStatus := answer["mdStatus"]
	if mdStatus == "" || !slices.Contains(accepted, mdStatus) {
		message := orDefault(answer["mdErrorMessage"], "3D Secure authentication failed")
		return p.completeRejected(map[string]any{"callback": raw, "resolve": resolved},
			"MD_STATUS_"+mdStatus, message, "", ""), nil
	}

	reached = true
	payment, err := p.api(ctx, []XMLField{{Name: "oosTranData", Children: packets}})
	if err != nil {
		return fail(err), nil
	}
	rawAll := map[string]any{"callback": raw, "resolve": resolved, "payment": payment}
	if !IsApproved(payment) {
		code, message := errorOf(payment)
		return p.completeRejected(rawAll, code, message, "", ""), nil
	}
	if payment.Fields["mac"] != "" {
		expected, err := CreateResponseMac(payment.Fields["hostlogkey"], macFields)
		if err != nil {
			return fail(err), nil
		}
		if !core.SafeEqual(payment.Fields["mac"], expected) {
			// The charge answer did not come from the bank as sent: the outcome is unknown
			return p.completeRejected(rawAll, "INVALID_HASH",
				"The charge answer MAC does not match; check the order with getPayment()",
				core.StatusPending, orderID), nil
		}
	}
	result := &core.PaymentResponse{
		Status:         core.StatusSuccess,
		PaymentID:      payment.Fields["hostlogkey"],
		ConversationID: orderID,
		RawResponse:    rawAll,
	}
	result.Code = p.ErrorCodeFor(result.Status, result.ErrorCode)
	return result, nil
}

// completeRejected builds a completion result the bank did not confirm. Only the
// charge answer with a mismatching MAC keeps the order id, as a pending outcome.
func (p *Provider) completeRejected(raw any, code, message string, status core.PaymentStatus, orderID string) *core.PaymentResponse {
	if status == "" {
		status = core.StatusFailure
	}
	result := &core.PaymentResponse{
		Status:         status,
		ErrorCode:      code,
		ErrorMessage:   message,
		ConversationID: orderID,
		RawResponse:    raw,
	}
	result.Code = p.ErrorCodeFor(result.Status, result.ErrorCode)
	return result
}

// Refund refunds a payment (XML return). PaymentID is the host log key of the payment.
func (p *Provider) Refund(ctx context.Context, request *core.RefundRequest) (*core.RefundResponse, error) {
	if request == nil {
		request = &core.RefundRequest{}
	}
	result, err := p.returnPayment(ctx, request)
	if err != nil {
		response := core.ApplyFailure(failureLabel, err, "Refund failed", &core.RefundResponse{})
		response.ConversationID = request.ConversationID
		response.Code = p.ErrorCodeFor(response.Status, response.ErrorCode)
		return response, nil
	}
	response := &core.RefundResponse{ConversationID: request.ConversationID, RawResponse: result}
	if IsApproved(result) {
		response.Status = core.StatusSuccess
		response.RefundID = result.Fields["hostlogkey"]
	} else {
		response.Status = core.StatusFailure
		response.ErrorCode, response.ErrorMessage = errorOf(result)
	}
	response.Code = p.ErrorCodeFor(response.Status, response.ErrorCode)
	return response, nil
}

func (p *Provider) returnPayment(ctx context.Context, request *core.RefundRequest) (XMLResponse, error) {
	if err := p.ValidateRefund(request); err != nil {
		return XMLResponse{}, err
	}
	amount, err := FormatAmount(request.Price)
	if err != nil {
		return XMLResponse{}, err
	}
	currency, err := CurrencyCode(request.Currency)
	if err != nil {
		return XMLResponse{}, err
	}
	return p.api(ctx, []XMLField{
		{Name: "tranDateRequired", Value: "1"},
		{Name: "return", Children: []XMLField{
			{Name: "amount", Value: amount},
			{Name: "currencyCode", Value: currency},
			{Name: "transaction", Value: "sale"},
			{Name: "hostLogKey", Value: request.PaymentID},
		}},
	})
}

// Cancel voids a payment the same day (XML reverse). PaymentID is the host log
// key of the payment.
func (p *Provider) Cancel(ctx context.Context, request *core.CancelRequest) (*core.CancelResponse, error) {
	if request == nil {
		request = &core.CancelRequest{}
	}
	result, err := p.api(ctx, []XMLField{
		{Name: "tranDateRequired", Value: "1"},
		{Name: "reverse", Children: []XMLField{
			{Name: "transaction", Value: "sale"},
			{Name: "hostLogKey", Value: request.PaymentID},
		}},
	})
	if err != nil {
		response := core.ApplyFailure(failureLabel, err, "Cancel failed", &core.CancelResponse{})
		response.ConversationID = request.ConversationID
		response.Code = p.ErrorCodeFor(response.Status, response.ErrorCode)
		return response, nil
	}
	response := &core.CancelResponse{ConversationID: request.ConversationID, RawResponse: result}
	if IsApproved(result) {
		response.Status = core.StatusSuccess
		response.TransactionID = result.Fields["hostlogkey"]
	} else {
		response.Status = core.StatusFailure
		response.ErrorCode, response.ErrorMessage = errorOf(result)
	}
	response.Code = p.ErrorCodeFor(response.Status, response.ErrorCode)
	return response, nil
}

func (p *Provider) agreement(ctx context.Context, orderID string) (XMLResponse, error) {
	return p.api(ctx, []XMLField{
		{Name: "agreement", Children: []XMLField{{Name: "orderID", Value: orderID}}},
	})
}

// GetPayment reads the order status (XML agreement). Unlike Refund and Cancel, it
// takes the merchant's order id (ConversationID), as the bank's inquiry does: the
// non-3D order id is tried first, then the 3D Secure one ("TDS_" + XID).
//
// A successful sale is a success, or cancelled once the order has a successful
// reverse or return; an order without a successful sale is a failure.
func (p *Provider) GetPayment(ctx context.Context, paymentID string) (*core.PaymentResponse, error) {
	result, err := p.inquiry(ctx, paymentID)
	if err != nil {
		response := core.ApplyFailure(failureLabel, err, "Get payment failed", &core.PaymentResponse{})
		response.ConversationID = paymentID
		response.Code = p.ErrorCodeFor(response.Status, response.ErrorCode)
		return response, nil
	}
	response := &core.PaymentResponse{ConversationID: paymentID, RawResponse: result}
	if !IsApproved(result) || len(result.Transactions) == 0 {
		response.Status = core.StatusFailure
		if IsApproved(result) {
			response.ErrorCode, response.ErrorMessage = "E219", "No transaction found for the order"
		} else {
			response.ErrorCode, response.ErrorMessage = errorOf(result)
		}
		response.Code = p.ErrorCodeFor(response.Status, response.ErrorCode)
		return response, nil
	}

	var sale map[string]string
	reversed := false
	for _, transaction := range result.Transactions {
		if transaction["txnStatus"] != "1" {
			continue
		}
		state := strings.ToLower(transaction["state"])
		if sale == nil {
			switch state {
			case "sale", "vft_sale", "capture", "authorization":
				sale = transaction
			}
		}
		if strings.Contains(state, "_reverse") || state == "return" || state == "vft_return" {
			reversed = true
		}
	}
	switch {
	case reversed:
		response.Status = core.StatusCancelled
	case sale != nil:
		response.Status = core.StatusSuccess
	default:
		response.Status = core.StatusFailure
	}
	if sale != nil {
		response.PaymentID = orDefault(sale["hostLogKey"], sale["hostlogkey"])
	}
	response.Code = p.ErrorCodeFor(response.Status, response.ErrorCode)
	return response, nil
}

func (p *Provider) inquiry(ctx context.Context, paymentID string) (XMLResponse, error) {
	bankOrderID, err := ToOrderID(paymentID)
	if err != nil {
		return XMLResponse{}, err
	}
	result, err := p.agreement(ctx, bankOrderID)
	if err != nil {
		return XMLResponse{}, err
	}
	if !IsApproved(result) || len(result.Transactions) == 0 {
		threeDSOrderID, err := ToThreeDSOrderID(paymentID)
		if err != nil {
			return XMLResponse{}, err
		}
		threeDS, err := p.agreement(ctx, threeDSOrderID)
		if err != nil {
			return XMLResponse{}, err
		}
		if IsApproved(threeDS) || !IsApproved(result) {
			result = threeDS
		}
	}
	return result, nil
}

// completeFields reads the completion input. Only string values are read: the
// bank posts form fields, and a non-string value is not a field the provider accepts.
func completeFields(data any) CompleteRequest {
	switch value := data.(type) {
	case nil:
		return CompleteRequest{}
	case CompleteRequest:
		return value
	case *CompleteRequest:
		if value == nil {
			return CompleteRequest{}
		}
		return *value
	case CallbackFields:
		return CompleteRequest{CallbackFields: value}
	case map[string]string:
		return completeFromStrings(value)
	case map[string]any:
		fields := make(map[string]string, len(value))
		for key, item := range value {
			if text, ok := item.(string); ok {
				fields[key] = text
			}
		}
		return completeFromStrings(fields)
	default:
		return CompleteRequest{}
	}
}

func completeFromStrings(fields map[string]string) CompleteRequest {
	return CompleteRequest{
		CallbackFields: CallbackFields{
			BankPacket:     fields["BankPacket"],
			MerchantPacket: fields["MerchantPacket"],
			Sign:           fields["Sign"],
		},
		OrderID:  fields["orderId"],
		Amount:   fields["amount"],
		Currency: fields["currency"],
	}
}
