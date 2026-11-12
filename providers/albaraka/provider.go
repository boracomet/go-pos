package albaraka

import (
	"context"
	"encoding/json"
	"errors"
	"math"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/boracomet/go-pos/core"
)

const (
	providerName = "albaraka"
	// failureLabel is the name used in failure messages.
	failureLabel = "Albaraka"
)

// SandboxBaseURL is the e-POS test host.
const SandboxBaseURL = "https://epostest.albarakaturk.com.tr"

// ProductionBaseURL is the e-POS production host.
const ProductionBaseURL = "https://epos.albarakaturk.com.tr"

// MACParams lists the values a MAC covers, in the order they are joined.
const (
	macParamsCard      = "MerchantNo:TerminalNo:CardNo:Cvc2:ExpireDate:Amount"
	macParamsThreeDS   = "MerchantNo:TerminalNo:SecureTransactionId:CavvData:Eci:MdStatus"
	macParamsReference = "MerchantNo:TerminalNo:ReferenceCode:OrderId"
	macParamsCapture   = "MerchantNo:TerminalNo:ReferenceCode"
	macParamsInquiry   = "MerchantNo:TerminalNo:OrderId"
)

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

// Provider is the Albaraka Türk virtual POS (e-POS, Posnet JSON API).
//
//   - CreatePayment:          Sale (non-3D)
//   - InitThreeDSPayment:     auto-submitting form to the 3D verification page
//   - CompleteThreeDSPayment: charges with Sale and the 3D data; needs the order
//     id, amount and currency from the merchant's records
//   - Refund:                 Return
//   - Cancel:                 Reverse
//   - GetPayment:             TransactionInquiry, by order id
//   - Authorize / InitThreeDSAuthorize: Auth, Capture: Capture,
//     VoidAuthorization: Reverse of the Auth
//
// PaymentID is the bank's ReferenceCode, which Refund and Cancel take;
// ConversationID is the merchant's order id.
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
			"Albaraka configuration is missing: "+strings.Join(missing, ", "), providerName)
	}
	if cfg.BaseURL == "" {
		return nil, core.NewConfigurationError("Albaraka baseUrl is required", providerName)
	}

	p := &Provider{
		BaseProvider: core.NewBaseProvider(providerName, cfg.ProviderConfig),
		config:       cfg,
	}
	p.ErrorCodes = ErrorCodes
	p.client = p.NewHTTPClient(30*time.Second, map[string]string{"Content-Type": "application/json"})
	return p, nil
}

// api calls a JSON API operation. The service rejects requests that leave out any
// of its data members, so the callers send them, as null when unused.
func (p *Provider) api(ctx context.Context, operation string, body any) (APIResponse, any, error) {
	payload, err := json.Marshal(body)
	if err != nil {
		return APIResponse{}, nil, err
	}
	response, err := p.client.Post(ctx, EndpointAPI+"/"+operation, string(payload), core.RequestConfig{})
	if err != nil {
		return APIResponse{}, nil, err
	}
	switch response.Data.(type) {
	case map[string]any, []any:
	default:
		return APIResponse{}, nil, errors.New("Albaraka returned an unexpected response")
	}
	var envelope APIResponse
	_ = json.Unmarshal([]byte(response.Text), &envelope)
	return envelope, response.Data, nil
}

func errorOf(response APIResponse) (string, string) {
	if response.ServiceResponseData == nil {
		return "", ""
	}
	return response.ServiceResponseData.ResponseCode, response.ServiceResponseData.ResponseDescription
}

func stringValue(value *string) string {
	if value == nil {
		return ""
	}
	return *value
}

func (p *Provider) paymentResult(response APIResponse, raw any, orderID string) *core.PaymentResponse {
	result := &core.PaymentResponse{ConversationID: orderID, RawResponse: raw}
	if IsApproved(response) {
		result.Status = core.StatusSuccess
		result.PaymentID = stringValue(response.ReferenceCode)
	} else {
		result.Status = core.StatusFailure
		result.ErrorCode, result.ErrorMessage = errorOf(response)
	}
	result.Code = p.ErrorCodeFor(result.Status, result.ErrorCode)
	return result
}

func (p *Provider) paymentFailure(err error, orderID string) *core.PaymentResponse {
	result := core.ApplyFailure(failureLabel, err, "Payment failed", &core.PaymentResponse{})
	result.ConversationID = orderID
	result.Code = p.ErrorCodeFor(result.Status, result.ErrorCode)
	return result
}

// CreatePayment charges the card without 3D Secure (JSON Sale).
func (p *Provider) CreatePayment(ctx context.Context, request *core.PaymentRequest) (*core.PaymentResponse, error) {
	return p.pay(ctx, request, TransactionSale)
}

// Authorize blocks the amount on the card without charging it (JSON Auth).
// Charge it later with Capture, or release it with VoidAuthorization.
func (p *Provider) Authorize(ctx context.Context, request *core.PaymentRequest) (*core.PaymentResponse, error) {
	return p.pay(ctx, request, TransactionAuth)
}

func (p *Provider) pay(ctx context.Context, request *core.PaymentRequest, operation TransactionType) (*core.PaymentResponse, error) {
	if request == nil {
		request = &core.PaymentRequest{}
	}
	orderID := request.ConversationID
	if orderID == "" {
		generated, err := GenerateOrderID()
		if err != nil {
			return nil, err
		}
		orderID = generated
	}
	response, raw, err := p.sale(ctx, request, operation, orderID)
	if err != nil {
		return p.paymentFailure(err, orderID), nil
	}
	return p.paymentResult(response, raw, orderID), nil
}

func (p *Provider) sale(ctx context.Context, request *core.PaymentRequest, operation TransactionType, orderID string) (APIResponse, any, error) {
	bankOrderID, err := FormatOrderID(orderID)
	if err != nil {
		return APIResponse{}, nil, err
	}
	if err := p.ValidatePayment(request, core.PaymentValidationRules{Card: true}); err != nil {
		return APIResponse{}, nil, err
	}
	card, err := p.CardOf(request)
	if err != nil {
		return APIResponse{}, nil, err
	}
	cardNo := whitespaceRe.ReplaceAllString(card.CardNumber, "")
	expireDate, err := FormatExpiry(card.ExpireMonth, card.ExpireYear)
	if err != nil {
		return APIResponse{}, nil, err
	}
	amount, err := FormatAmount(orDefault(request.PaidPrice, request.Price))
	if err != nil {
		return APIResponse{}, nil, err
	}
	currency, err := CurrencyCode(request.Currency)
	if err != nil {
		return APIResponse{}, nil, err
	}
	installment, err := FormatInstallment(installmentCount(request.Installment))
	if err != nil {
		return APIResponse{}, nil, err
	}
	mac, err := CreateMac([]string{
		p.config.MerchantID,
		p.config.TerminalID,
		cardNo,
		card.CVC,
		expireDate,
		strconv.FormatInt(amount, 10),
	}, p.config.EncKey)
	if err != nil {
		return APIResponse{}, nil, err
	}

	return p.api(ctx, string(operation), saleBody{
		ApiType:               "JSON",
		ApiVersion:            "V100",
		MACParams:             macParamsCard,
		MerchantNo:            p.config.MerchantID,
		TerminalNo:            p.config.TerminalID,
		CardInformationData:   CardInformationData{CardNo: cardNo, ExpireDate: expireDate, Cvc2: card.CVC, CardHolderName: card.CardHolderName},
		IsMailOrder:           "N",
		PaymentInstrumentType: "CARD",
		Amount:                amount,
		CurrencyCode:          currency,
		OrderId:               bankOrderID,
		InstallmentCount:      installment.Count,
		InstallmentType:       installment.Type,
		MAC:                   mac,
	})
}

// InitThreeDSPayment returns an auto-submitting form to the 3D verification page.
// The bank POSTs the result to CallbackURL; complete it with CompleteThreeDSPayment.
func (p *Provider) InitThreeDSPayment(ctx context.Context, request *core.ThreeDSPaymentRequest) (*core.ThreeDSInitResponse, error) {
	return p.payThreeDS(request, TransactionSale)
}

// InitThreeDSAuthorize starts a pre-authorization with 3D Secure. Complete it with
// a CompleteRequest whose TransactionType is Auth.
func (p *Provider) InitThreeDSAuthorize(ctx context.Context, request *core.ThreeDSPaymentRequest) (*core.ThreeDSInitResponse, error) {
	return p.payThreeDS(request, TransactionAuth)
}

func (p *Provider) payThreeDS(request *core.ThreeDSPaymentRequest, transactionType TransactionType) (*core.ThreeDSInitResponse, error) {
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
		generated, err := GenerateOrderID()
		if err != nil {
			return nil, err
		}
		orderID = generated
	}
	bankOrderID, err := FormatOrderID(orderID)
	if err != nil {
		return fail(err), nil
	}
	if err := p.ValidatePayment(&request.PaymentRequest, core.PaymentValidationRules{Card: true}); err != nil {
		return fail(err), nil
	}
	if request.CallbackURL == "" {
		return fail(core.NewValidationError("callbackUrl is required for 3D Secure payments", providerName, nil)), nil
	}
	card, err := p.CardOf(&request.PaymentRequest)
	if err != nil {
		return fail(err), nil
	}
	cardNo := whitespaceRe.ReplaceAllString(card.CardNumber, "")
	expireDate, err := FormatExpiry(card.ExpireMonth, card.ExpireYear)
	if err != nil {
		return fail(err), nil
	}
	minor, err := FormatAmount(orDefault(request.PaidPrice, request.Price))
	if err != nil {
		return fail(err), nil
	}
	amount := strconv.FormatInt(minor, 10)
	currency, err := CurrencyCode(request.Currency)
	if err != nil {
		return fail(err), nil
	}
	installment, err := FormatInstallment(installmentCount(request.Installment))
	if err != nil {
		return fail(err), nil
	}
	mac, err := CreateMac([]string{
		p.config.MerchantID, p.config.TerminalID, cardNo, card.CVC, expireDate, amount,
	}, p.config.EncKey)
	if err != nil {
		return fail(err), nil
	}

	fields := []formField{
		{"MerchantNo", p.config.MerchantID},
		{"TerminalNo", p.config.TerminalID},
		{"PosnetID", p.config.PosnetID},
		{"TransactionType", string(transactionType)},
		{"OrderId", bankOrderID},
		{"Amount", amount},
		{"CurrencyCode", currency},
		{"MerchantReturnURL", request.CallbackURL},
		{"InstallmentCount", strconv.Itoa(installment.Count)},
		{"InstallmentType", installment.Type},
		{"Language", language(p.Config.Locale)},
		{"TxnState", "INITIAL"},
		{"OpenNewWindow", "0"},
		{"CardNo", cardNo},
		{"ExpiredDate", expireDate},
		{"Cvv", card.CVC},
		{"CardHolderName", card.CardHolderName},
		{"MacParams", macParamsCard},
		{"UseOOS", "0"},
		{"MAC", mac},
	}
	var inputs strings.Builder
	for _, item := range fields {
		inputs.WriteString(`<input type="hidden" name="` + escapeHTML(item.name) + `" value="` + escapeHTML(item.value) + `">`)
	}
	action := p.config.BaseURL + EndpointGate
	html := `<!DOCTYPE html><html><head><meta charset="UTF-8"></head><body>` +
		`<form id="albaraka-3d-form" method="POST" action="` + escapeHTML(action) + `">` + inputs.String() + `</form>` +
		`<script>document.getElementById("albaraka-3d-form").submit();</script>` +
		`</body></html>`

	result := &core.ThreeDSInitResponse{
		Status:             core.StatusPending,
		ThreeDSHTMLContent: html,
		ConversationID:     orderID,
	}
	result.Code = p.ErrorCodeFor(result.Status, result.ErrorCode)
	return result, nil
}

// CompleteThreeDSPayment completes the payment after the MerchantReturnURL POST.
//
// The callback is not signed: when MdStatus is accepted, the payment is charged
// with JSON Sale (or Auth) and the 3D data, for the order id, amount and currency
// from the merchant's records, and the bank's answer decides.
//
// PaymentID and ConversationID are set only for a payment the bank approved, and
// on a timeout after the bank was reached; a failure carries no ids.
//
// callbackData is a CompleteRequest, or the callback POST as a map[string]string
// or map[string]any carrying the same fields.
func (p *Provider) CompleteThreeDSPayment(ctx context.Context, callbackData any) (*core.PaymentResponse, error) {
	data := completeFields(callbackData)
	raw := callbackData
	if raw == nil {
		raw = map[string]any{}
	}
	accepted := p.config.AcceptedMdStatuses
	if len(accepted) == 0 {
		accepted = []string{"1"}
	}
	if data.MdStatus == "" || !slices.Contains(accepted, data.MdStatus) {
		result := &core.PaymentResponse{
			Status:       core.StatusFailure,
			ErrorCode:    "MD_STATUS_" + data.MdStatus,
			ErrorMessage: orDefault(data.MdErrorMessage, "3D Secure authentication failed"),
			RawResponse:  raw,
		}
		result.Code = p.ErrorCodeFor(result.Status, result.ErrorCode)
		return result, nil
	}

	orderID := data.OrderID
	reached := false
	response, payment, err := p.charge(ctx, data, &reached)
	if err != nil {
		result := core.ApplyFailure(failureLabel, err, "3D Secure completion failed", &core.PaymentResponse{})
		// A timeout after the bank was asked to charge: the outcome is unknown.
		if reached && result.Status == core.StatusPending {
			result.ConversationID = orderID
		}
		result.Code = p.ErrorCodeFor(result.Status, result.ErrorCode)
		return result, nil
	}

	rawResponse := map[string]any{"callback": raw, "payment": payment}
	if !IsApproved(response) {
		result := &core.PaymentResponse{Status: core.StatusFailure, RawResponse: rawResponse}
		result.ErrorCode, result.ErrorMessage = errorOf(response)
		result.Code = p.ErrorCodeFor(result.Status, result.ErrorCode)
		return result, nil
	}
	result := &core.PaymentResponse{
		Status:         core.StatusSuccess,
		PaymentID:      stringValue(response.ReferenceCode),
		ConversationID: orderID,
		RawResponse:    rawResponse,
	}
	result.Code = p.ErrorCodeFor(result.Status, result.ErrorCode)
	return result, nil
}

// charge sends the 3D data with the order values from the merchant's records.
// reached reports whether the bank was asked to charge, so a timeout afterwards
// leaves the outcome unknown instead of failed.
func (p *Provider) charge(ctx context.Context, data CompleteRequest, reached *bool) (APIResponse, any, error) {
	if data.OrderID == "" || data.Amount == "" {
		return APIResponse{}, nil, core.NewValidationError(
			"completeThreeDSPayment() needs orderId and amount from your records: "+
				"completeThreeDSPayment({ ...callbackBody, orderId, amount, currency })", providerName, nil)
	}
	var missing []string
	for _, item := range []struct{ name, value string }{
		{"SecureTransactionId", data.SecureTransactionId},
		{"CAVV", data.CAVV},
		{"ECI", data.ECI},
		{"MD", data.MD},
	} {
		if item.value == "" {
			missing = append(missing, item.name)
		}
	}
	if len(missing) > 0 {
		return APIResponse{}, nil, core.NewValidationError(
			"3D Secure callback is missing: "+strings.Join(missing, ", "), providerName, nil)
	}
	bankOrderID, err := FormatOrderID(data.OrderID)
	if err != nil {
		return APIResponse{}, nil, err
	}

	*reached = true
	amount, err := FormatAmount(data.Amount)
	if err != nil {
		return APIResponse{}, nil, err
	}
	currency, err := CurrencyCode(data.Currency)
	if err != nil {
		return APIResponse{}, nil, err
	}
	installment, err := ParseInstallment(data.Installment)
	if err != nil {
		return APIResponse{}, nil, err
	}
	mac, err := CreateMac([]string{
		p.config.MerchantID,
		p.config.TerminalID,
		data.SecureTransactionId,
		data.CAVV,
		data.ECI,
		data.MdStatus,
	}, p.config.EncKey)
	if err != nil {
		return APIResponse{}, nil, err
	}
	operation := TransactionSale
	if data.TransactionType == string(TransactionAuth) {
		operation = TransactionAuth
	}

	return p.api(ctx, string(operation), completeBody{
		ApiType:               "JSON",
		ApiVersion:            "V100",
		MerchantNo:            p.config.MerchantID,
		TerminalNo:            p.config.TerminalID,
		PaymentInstrumentType: "CARD",
		IsEncrypted:           "N",
		IsTDSecureMerchant:    "Y",
		IsMailOrder:           "N",
		ThreeDSecureData: ThreeDSecureData{
			SecureTransactionId: data.SecureTransactionId,
			CavvData:            data.CAVV,
			Eci:                 data.ECI,
			MdStatus:            mdStatusNumber(data.MdStatus),
			MD:                  data.MD,
		},
		MACParams:        macParamsThreeDS,
		Amount:           amount,
		CurrencyCode:     currency,
		PointAmount:      0,
		OrderId:          bankOrderID,
		InstallmentCount: installment.Count,
		InstallmentType:  installment.Type,
		MAC:              mac,
	})
}

// referenceMac signs Return, Reverse and Capture: MerchantNo + TerminalNo + ReferenceCode.
func (p *Provider) referenceMac(referenceCode string) (string, error) {
	return CreateMac([]string{p.config.MerchantID, p.config.TerminalID, referenceCode}, p.config.EncKey)
}

// Refund refunds a payment (JSON Return); a lower amount is a partial refund.
// PaymentID is the ReferenceCode of the payment.
func (p *Provider) Refund(ctx context.Context, request *core.RefundRequest) (*core.RefundResponse, error) {
	if request == nil {
		request = &core.RefundRequest{}
	}
	fail := func(err error) *core.RefundResponse {
		result := core.ApplyFailure(failureLabel, err, "Refund failed", &core.RefundResponse{})
		result.ConversationID = request.ConversationID
		result.Code = p.ErrorCodeFor(result.Status, result.ErrorCode)
		return result
	}

	if err := p.ValidateRefund(request); err != nil {
		return fail(err), nil
	}
	amount, err := FormatAmount(request.Price)
	if err != nil {
		return fail(err), nil
	}
	currency, err := CurrencyCode(request.Currency)
	if err != nil {
		return fail(err), nil
	}
	mac, err := p.referenceMac(request.PaymentID)
	if err != nil {
		return fail(err), nil
	}
	response, raw, err := p.api(ctx, "Return", returnBody{
		ApiType:         "JSON",
		ApiVersion:      "v100",
		MACParams:       macParamsReference,
		MerchantNo:      p.config.MerchantID,
		TerminalNo:      p.config.TerminalID,
		ReferenceCode:   request.PaymentID,
		TransactionType: string(TransactionSale),
		Amount:          amount,
		CurrencyCode:    currency,
		MAC:             mac,
	})
	if err != nil {
		return fail(err), nil
	}

	result := &core.RefundResponse{
		Status:         core.StatusFailure,
		ConversationID: request.ConversationID,
		RawResponse:    raw,
	}
	if IsApproved(response) {
		result.Status = core.StatusSuccess
		result.RefundID = stringValue(response.ReferenceCode)
	} else {
		result.ErrorCode, result.ErrorMessage = errorOf(response)
	}
	result.Code = p.ErrorCodeFor(result.Status, result.ErrorCode)
	return result, nil
}

func (p *Provider) reverse(ctx context.Context, referenceCode string, transactionType TransactionType, conversationID string) (*core.CancelResponse, error) {
	fail := func(err error) *core.CancelResponse {
		result := core.ApplyFailure(failureLabel, err, "Cancel failed", &core.CancelResponse{})
		result.ConversationID = conversationID
		result.Code = p.ErrorCodeFor(result.Status, result.ErrorCode)
		return result
	}

	mac, err := p.referenceMac(referenceCode)
	if err != nil {
		return fail(err), nil
	}
	response, raw, err := p.api(ctx, "Reverse", reverseBody{
		ApiType:         "JSON",
		ApiVersion:      "v100",
		MACParams:       macParamsReference,
		MerchantNo:      p.config.MerchantID,
		TerminalNo:      p.config.TerminalID,
		ReferenceCode:   referenceCode,
		TransactionType: string(transactionType),
		MAC:             mac,
	})
	if err != nil {
		return fail(err), nil
	}

	result := &core.CancelResponse{
		Status:         core.StatusFailure,
		ConversationID: conversationID,
		RawResponse:    raw,
	}
	if IsApproved(response) {
		result.Status = core.StatusSuccess
		result.TransactionID = stringValue(response.ReferenceCode)
	} else {
		result.ErrorCode, result.ErrorMessage = errorOf(response)
	}
	result.Code = p.ErrorCodeFor(result.Status, result.ErrorCode)
	return result, nil
}

// Cancel voids a sale before end of day (JSON Reverse). PaymentID is the
// ReferenceCode of the payment.
func (p *Provider) Cancel(ctx context.Context, request *core.CancelRequest) (*core.CancelResponse, error) {
	if request == nil {
		request = &core.CancelRequest{}
	}
	return p.reverse(ctx, request.PaymentID, TransactionSale, request.ConversationID)
}

// VoidAuthorization releases a pre-authorization (JSON Reverse of the Auth).
func (p *Provider) VoidAuthorization(ctx context.Context, request *core.VoidAuthorizationRequest) (*core.CancelResponse, error) {
	if request == nil {
		request = &core.VoidAuthorizationRequest{}
	}
	return p.reverse(ctx, request.PaymentID, TransactionAuth, request.ConversationID)
}

// Capture charges a pre-authorization (JSON Capture); a lower amount captures
// part of it. PaymentID is the ReferenceCode of the Auth.
func (p *Provider) Capture(ctx context.Context, request *core.CaptureRequest) (*core.PaymentResponse, error) {
	if request == nil {
		request = &core.CaptureRequest{}
	}
	fail := func(err error) *core.PaymentResponse {
		result := core.ApplyFailure(failureLabel, err, "Capture failed", &core.PaymentResponse{})
		result.ConversationID = request.ConversationID
		result.Code = p.ErrorCodeFor(result.Status, result.ErrorCode)
		return result
	}

	if err := p.ValidateCapture(request); err != nil {
		return fail(err), nil
	}
	amount, err := FormatAmount(request.Amount)
	if err != nil {
		return fail(err), nil
	}
	currency, err := CurrencyCode(request.Currency)
	if err != nil {
		return fail(err), nil
	}
	mac, err := p.referenceMac(request.PaymentID)
	if err != nil {
		return fail(err), nil
	}
	response, raw, err := p.api(ctx, "Capture", captureBody{
		ApiType:          "JSON",
		ApiVersion:       "V100",
		MACParams:        macParamsCapture,
		MerchantNo:       p.config.MerchantID,
		TerminalNo:       p.config.TerminalID,
		ReferenceCode:    request.PaymentID,
		Amount:           amount,
		CurrencyCode:     currency,
		InstallmentCount: 0,
		InstallmentType:  "N",
		MAC:              mac,
	})
	if err != nil {
		return fail(err), nil
	}

	result := &core.PaymentResponse{
		Status:         core.StatusFailure,
		ConversationID: request.ConversationID,
		RawResponse:    raw,
	}
	if IsApproved(response) {
		result.Status = core.StatusSuccess
		result.PaymentID = orDefault(stringValue(response.ReferenceCode), request.PaymentID)
	} else {
		result.ErrorCode, result.ErrorMessage = errorOf(response)
	}
	result.Code = p.ErrorCodeFor(result.Status, result.ErrorCode)
	return result, nil
}

// GetPayment looks the order up with JSON TransactionInquiry. Unlike Refund and
// Cancel it takes the merchant's order id (ConversationID), as the bank does.
// An unknown order (E219) is a failure; a found order is mapped from its
// transactions, and stays pending when they carry no known status.
func (p *Provider) GetPayment(ctx context.Context, paymentID string) (*core.PaymentResponse, error) {
	fail := func(err error) *core.PaymentResponse {
		result := core.ApplyFailure(failureLabel, err, "Get payment failed", &core.PaymentResponse{})
		result.ConversationID = paymentID
		result.Code = p.ErrorCodeFor(result.Status, result.ErrorCode)
		return result
	}

	bankOrderID, err := FormatOrderID(paymentID)
	if err != nil {
		return fail(err), nil
	}
	mac, err := CreateMac([]string{p.config.MerchantID, p.config.TerminalID, bankOrderID}, p.config.EncKey)
	if err != nil {
		return fail(err), nil
	}
	response, raw, err := p.api(ctx, "TransactionInquiry", inquiryBody{
		ApiType:    "JSON",
		ApiVersion: "V100",
		MACParams:  macParamsInquiry,
		MerchantNo: p.config.MerchantID,
		TerminalNo: p.config.TerminalID,
		OrderId:    bankOrderID,
		MAC:        mac,
	})
	if err != nil {
		return fail(err), nil
	}

	result := &core.PaymentResponse{ConversationID: paymentID, RawResponse: raw}
	if IsApproved(response) {
		result.Status = OrderStatus(response.TransactionData)
		result.PaymentID = stringValue(response.ReferenceCode)
	} else {
		result.Status = core.StatusFailure
		result.ErrorCode, result.ErrorMessage = errorOf(response)
	}
	result.Code = p.ErrorCodeFor(result.Status, result.ErrorCode)
	return result, nil
}

// installmentCount reads the request's installment count; 0 means unset.
func installmentCount(count int) int {
	if count == 0 {
		return 1
	}
	return count
}

func language(locale string) string {
	if strings.HasPrefix(strings.ToLower(orDefault(locale, "tr")), "en") {
		return "en"
	}
	return "tr"
}

// mdStatusNumber renders MdStatus as the JSON number the bank expects. A value
// that is not a number is sent as null, as JavaScript's Number() would produce.
func mdStatusNumber(value string) any {
	number, err := strconv.ParseFloat(strings.TrimSpace(value), 64)
	if err != nil || math.IsNaN(number) || math.IsInf(number, 0) {
		return nil
	}
	if number == math.Trunc(number) {
		return int64(number)
	}
	return number
}

type formField struct {
	name  string
	value string
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
		encoded, err := json.Marshal(data)
		if err != nil {
			return CompleteRequest{}
		}
		var request CompleteRequest
		_ = json.Unmarshal(encoded, &request)
		return request
	}
}

func completeFromStrings(fields map[string]string) CompleteRequest {
	return CompleteRequest{
		MdStatus:            fields["MdStatus"],
		MdErrorMessage:      fields["MdErrorMessage"],
		SecureTransactionId: fields["SecureTransactionId"],
		CAVV:                fields["CAVV"],
		ECI:                 fields["ECI"],
		MD:                  fields["MD"],
		OrderID:             fields["orderId"],
		Amount:              fields["amount"],
		Currency:            fields["currency"],
		Installment:         fields["installment"],
		TransactionType:     fields["transactionType"],
	}
}

type saleBody struct {
	ApiType                string              `json:"ApiType"`
	ApiVersion             string              `json:"ApiVersion"`
	MACParams              string              `json:"MACParams"`
	MerchantNo             string              `json:"MerchantNo"`
	TerminalNo             string              `json:"TerminalNo"`
	CipheredData           any                 `json:"CipheredData"`
	DealerData             any                 `json:"DealerData"`
	IsEncrypted            any                 `json:"IsEncrypted"`
	PaymentFacilitatorData any                 `json:"PaymentFacilitatorData"`
	AdditionalInfoData     any                 `json:"AdditionalInfoData"`
	CardInformationData    CardInformationData `json:"CardInformationData"`
	IsMailOrder            string              `json:"IsMailOrder"`
	IsRecurring            any                 `json:"IsRecurring"`
	IsTDSecureMerchant     any                 `json:"IsTDSecureMerchant"`
	PaymentInstrumentType  string              `json:"PaymentInstrumentType"`
	ThreeDSecureData       any                 `json:"ThreeDSecureData"`
	Amount                 int64               `json:"Amount"`
	CurrencyCode           string              `json:"CurrencyCode"`
	OrderId                string              `json:"OrderId"`
	InstallmentCount       int                 `json:"InstallmentCount"`
	InstallmentType        string              `json:"InstallmentType"`
	KOICode                any                 `json:"KOICode"`
	MerchantMessageData    any                 `json:"MerchantMessageData"`
	PointAmount            any                 `json:"PointAmount"`
	MAC                    string              `json:"MAC"`
}

type completeBody struct {
	ApiType               string           `json:"ApiType"`
	ApiVersion            string           `json:"ApiVersion"`
	MerchantNo            string           `json:"MerchantNo"`
	TerminalNo            string           `json:"TerminalNo"`
	PaymentInstrumentType string           `json:"PaymentInstrumentType"`
	IsEncrypted           string           `json:"IsEncrypted"`
	IsTDSecureMerchant    string           `json:"IsTDSecureMerchant"`
	IsMailOrder           string           `json:"IsMailOrder"`
	ThreeDSecureData      ThreeDSecureData `json:"ThreeDSecureData"`
	MACParams             string           `json:"MACParams"`
	Amount                int64            `json:"Amount"`
	CurrencyCode          string           `json:"CurrencyCode"`
	PointAmount           int              `json:"PointAmount"`
	OrderId               string           `json:"OrderId"`
	InstallmentCount      int              `json:"InstallmentCount"`
	InstallmentType       string           `json:"InstallmentType"`
	MAC                   string           `json:"MAC"`
}

type returnBody struct {
	ApiType                string `json:"ApiType"`
	ApiVersion             string `json:"ApiVersion"`
	MACParams              string `json:"MACParams"`
	CipheredData           any    `json:"CipheredData"`
	DealerData             any    `json:"DealerData"`
	IsEncrypted            any    `json:"IsEncrypted"`
	PaymentFacilitatorData any    `json:"PaymentFacilitatorData"`
	OrderId                any    `json:"OrderId"`
	MerchantNo             string `json:"MerchantNo"`
	TerminalNo             string `json:"TerminalNo"`
	ReferenceCode          string `json:"ReferenceCode"`
	TransactionType        string `json:"TransactionType"`
	Amount                 int64  `json:"Amount"`
	CurrencyCode           string `json:"CurrencyCode"`
	MAC                    string `json:"MAC"`
}

type reverseBody struct {
	ApiType                string `json:"ApiType"`
	ApiVersion             string `json:"ApiVersion"`
	MACParams              string `json:"MACParams"`
	CipheredData           any    `json:"CipheredData"`
	DealerData             any    `json:"DealerData"`
	IsEncrypted            any    `json:"IsEncrypted"`
	PaymentFacilitatorData any    `json:"PaymentFacilitatorData"`
	OrderId                any    `json:"OrderId"`
	MerchantNo             string `json:"MerchantNo"`
	TerminalNo             string `json:"TerminalNo"`
	ReferenceCode          string `json:"ReferenceCode"`
	TransactionType        string `json:"TransactionType"`
	MAC                    string `json:"MAC"`
}

type captureBody struct {
	ApiType                string `json:"ApiType"`
	ApiVersion             string `json:"ApiVersion"`
	MACParams              string `json:"MACParams"`
	CipheredData           any    `json:"CipheredData"`
	DealerData             any    `json:"DealerData"`
	IsEncrypted            any    `json:"IsEncrypted"`
	PaymentFacilitatorData any    `json:"PaymentFacilitatorData"`
	MerchantNo             string `json:"MerchantNo"`
	TerminalNo             string `json:"TerminalNo"`
	ReferenceCode          string `json:"ReferenceCode"`
	Amount                 int64  `json:"Amount"`
	CurrencyCode           string `json:"CurrencyCode"`
	PointAmount            any    `json:"PointAmount"`
	InstallmentCount       int    `json:"InstallmentCount"`
	InstallmentType        string `json:"InstallmentType"`
	MAC                    string `json:"MAC"`
}

type inquiryBody struct {
	ApiType    string `json:"ApiType"`
	ApiVersion string `json:"ApiVersion"`
	MACParams  string `json:"MACParams"`
	MerchantNo string `json:"MerchantNo"`
	TerminalNo string `json:"TerminalNo"`
	OrderId    string `json:"OrderId"`
	MAC        string `json:"MAC"`
}
