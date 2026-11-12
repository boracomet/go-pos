package kuveytturk

import (
	"context"
	"encoding/json"
	"math"
	"strconv"
	"strings"
	"time"

	"github.com/boracomet/go-pos/core"
)

const (
	providerName = "kuveytturk"
	// failureLabel is the name used in failure messages.
	failureLabel = "Kuveyt Türk"
)

// SandboxBaseURL is the KT Pay Gate test host.
const SandboxBaseURL = "https://boatest.kuveytturk.com.tr/boa.virtualpos.services"

// ProductionBaseURL is the KT Pay Gate production host.
const ProductionBaseURL = "https://sanalpos.kuveytturk.com.tr/ServiceGateWay"

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

// Provider is the Kuveyt Türk Sanal POS (KT Pay Gate, JSON API).
//
//   - InitThreeDSPayment:     Payment; returns the bank's 3D Secure page (HTML)
//   - CompleteThreeDSPayment: checks the successUrl POST, then charges with Provision
//   - Cancel:                 SaleReversal "Cancel" (same day, before end of day)
//   - Refund:                 SaleReversal "Drawback" (full) or "PartialDrawback"
//   - GetPayment:             GetTransactions by merchant order id
//
// PaymentID is the merchant order id (ConversationID, or a generated one). The
// bank's own order id is looked up with GetTransactions where it is needed.
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
	if cfg.CustomerID == "" {
		missing = append(missing, "customerId")
	}
	if cfg.Username == "" {
		missing = append(missing, "username")
	}
	if cfg.Password == "" {
		missing = append(missing, "password")
	}
	if len(missing) > 0 {
		return nil, core.NewConfigurationError(
			"Kuveyt Türk configuration is missing: "+strings.Join(missing, ", "), providerName)
	}
	if cfg.BaseURL == "" {
		return nil, core.NewConfigurationError("Kuveyt Türk baseUrl is required", providerName)
	}

	p := &Provider{
		BaseProvider: core.NewBaseProvider(providerName, cfg.ProviderConfig),
		config:       cfg,
	}
	p.ErrorCodes = ErrorCodes
	p.client = p.NewHTTPClient(30*time.Second, map[string]string{
		"Content-Type": "application/json",
		"Accept":       "application/json",
	})
	return p, nil
}

var cardRules = core.PaymentValidationRules{
	Card:     true,
	Required: []string{"buyer.ip", "buyer.email", "buyer.gsmNumber"},
}

func (p *Provider) credentials() HashCredentials {
	return HashCredentials{
		MerchantID: p.config.MerchantID,
		Username:   p.config.Username,
		Password:   p.config.Password,
	}
}

func (p *Provider) account() accountFields {
	return accountFields{
		MerchantId: p.config.MerchantID,
		CustomerId: p.config.CustomerID,
		Username:   p.config.Username,
	}
}

// post calls a KT Pay Gate endpoint. Only read-only queries are retryable.
// Statuses below 500 resolve, so a 4xx answer is read as a failure envelope.
func (p *Provider) post(ctx context.Context, endpoint string, body any, retryable bool) (APIResponse, any, error) {
	payload, err := json.Marshal(body)
	if err != nil {
		return APIResponse{}, nil, err
	}
	response, err := p.client.Post(ctx, endpoint, string(payload), core.RequestConfig{
		Retryable:      retryable,
		ValidateStatus: func(status int) bool { return status < 500 },
	})
	if err != nil {
		return APIResponse{}, nil, err
	}
	var envelope APIResponse
	decodeJSON(response.Text, &envelope)
	raw := response.Data
	if raw == nil {
		raw = map[string]any{}
	}
	return envelope, raw, nil
}

// decodeJSON fills out from a KT Pay Gate body. A body that is not the expected
// JSON object leaves out zero-valued, which the callers read as "not successful".
func decodeJSON(text string, out any) {
	_ = json.Unmarshal([]byte(text), out)
}

type transactionLookup struct {
	data APIResponse
	raw  any
	tx   *Transaction
}

// findTransaction looks up a merchant order id at the bank: the bank's order id,
// amount and status come from here, because callbacks are not signed. With
// orderID, only the transaction with that bank order id; otherwise the successful
// sale, if any. Transactions of another order are never picked.
func (p *Provider) findTransaction(ctx context.Context, merchantOrderID string, orderID *string) (transactionLookup, error) {
	hashData, err := CreateGetTransactionsHash(p.credentials(), merchantOrderID)
	if err != nil {
		return transactionLookup{}, err
	}
	envelope, raw, err := p.post(ctx, EndpointGetTransactions, getTransactionsBody{
		accountFields:   p.account(),
		MerchantOrderId: merchantOrderID,
		HashData:        hashData,
	}, true)
	if err != nil {
		return transactionLookup{}, err
	}

	var list TransactionList
	if len(envelope.Result) > 0 {
		decodeJSON(string(envelope.Result), &list)
	}
	matched := make([]Transaction, 0, len(list.Transactions))
	for _, item := range list.Transactions {
		if item.MerchantOrderId == merchantOrderID {
			matched = append(matched, item)
		}
	}

	var tx *Transaction
	if orderID != nil {
		for i := range matched {
			if matched[i].OrderId.String() == *orderID {
				tx = &matched[i]
				break
			}
		}
	} else {
		for i := range matched {
			if code, ok := StatusCode(matched[i].TransactionStatus); ok && code == 1 {
				tx = &matched[i]
				break
			}
		}
		if tx == nil && len(matched) > 0 {
			tx = &matched[0]
		}
	}
	return transactionLookup{data: envelope, raw: raw, tx: tx}, nil
}

// CreatePayment is not available: KT Pay Gate only processes 3D Secure payments.
func (p *Provider) CreatePayment(context.Context, *core.PaymentRequest) (*core.PaymentResponse, error) {
	return nil, &core.BaseError{
		Message:  "Kuveyt Türk KT Pay Gate only processes 3D Secure payments: use initThreeDSPayment().",
		Code:     "NOT_SUPPORTED",
		Provider: providerName,
	}
}

// InitThreeDSPayment sends the card to the Payment endpoint and returns the bank's
// verification page. The bank posts the result to CallbackURL (successUrl) or to
// the same URL on failure; complete it with CompleteThreeDSPayment.
func (p *Provider) InitThreeDSPayment(ctx context.Context, request *core.ThreeDSPaymentRequest) (*core.ThreeDSInitResponse, error) {
	return p.InitThreeDSPaymentWithFailURL(ctx, request, "")
}

// InitThreeDSPaymentWithFailURL is InitThreeDSPayment with the optional failUrl
// the Payment endpoint accepts; the shared request type has no field for it.
// An empty failUrl means CallbackURL.
func (p *Provider) InitThreeDSPaymentWithFailURL(ctx context.Context, request *core.ThreeDSPaymentRequest, failURL string) (*core.ThreeDSInitResponse, error) {
	if request == nil {
		request = &core.ThreeDSPaymentRequest{}
	}
	merchantOrderID := request.ConversationID
	if merchantOrderID == "" {
		merchantOrderID = core.GenerateOrderID("")
	}

	body, err := p.paymentBody(request, merchantOrderID, failURL)
	if err != nil {
		return p.initFailure(err, merchantOrderID), nil
	}

	// Payment answers with HTML: the 3D Secure page, or an auto-submitting form
	// to failUrl when the request is rejected.
	response, err := p.client.Post(ctx, EndpointPayment, body, core.RequestConfig{
		ResponseType:   "text",
		ValidateStatus: func(status int) bool { return status < 500 },
	})
	if err != nil {
		return p.initFailure(err, merchantOrderID), nil
	}
	html := response.Text
	fields := ParseForm(html)

	result := &core.ThreeDSInitResponse{
		PaymentID:      merchantOrderID,
		ConversationID: merchantOrderID,
	}
	if strings.TrimSpace(html) == "" || strings.ToLower(fields["Success"]) == "false" {
		result.Status = core.StatusFailure
		result.ErrorCode = orDefault(fields["ResponseCode"], "PROVIDER_ERROR")
		result.ErrorMessage = orDefault(fields["ResponseMessage"], "Kuveyt Türk returned an empty response")
		result.RawResponse = fields
	} else {
		result.Status = core.StatusPending
		result.ThreeDSHTMLContent = html
	}
	result.Code = p.ErrorCodeFor(result.Status, result.ErrorCode)
	return result, nil
}

func (p *Provider) paymentBody(request *core.ThreeDSPaymentRequest, merchantOrderID, failURL string) (string, error) {
	if err := p.ValidatePayment(&request.PaymentRequest, cardRules); err != nil {
		return "", err
	}
	card, err := p.CardOf(&request.PaymentRequest)
	if err != nil {
		return "", err
	}
	if request.CallbackURL == "" {
		return "", core.NewValidationError("callbackUrl is required for 3D Secure payments", providerName, nil)
	}
	successURL := request.CallbackURL
	if failURL == "" {
		failURL = request.CallbackURL
	}
	if strings.Contains(successURL, "&") || strings.Contains(failURL, "&") {
		return "", core.NewValidationError(
			`Kuveyt Türk does not accept "&" in callback URLs; use a single query parameter or a path segment`,
			providerName, nil)
	}

	amount, err := FormatAmount(orDefault(request.PaidPrice, request.Price))
	if err != nil {
		return "", err
	}
	phone, err := ParsePhone(request.Buyer.GSMNumber)
	if err != nil {
		return "", err
	}
	hashData, err := CreatePaymentHash(p.credentials(), merchantOrderID, amount, successURL, failURL)
	if err != nil {
		return "", err
	}
	currency, err := CurrencyCode(request.Currency)
	if err != nil {
		return "", err
	}
	installment, err := FormatInstallment(request.Installment)
	if err != nil {
		return "", err
	}
	expiry, err := FormatExpiry(card.ExpireMonth, card.ExpireYear)
	if err != nil {
		return "", err
	}
	paymentType := 1
	if p.config.PaymentType != nil {
		paymentType = *p.config.PaymentType
	}

	payload, err := json.Marshal(paymentBody{
		Language:         Language(p.Config.Locale),
		MerchantOrderId:  merchantOrderID,
		SuccessUrl:       successURL,
		FailUrl:          failURL,
		accountFields:    p.account(),
		HashData:         hashData,
		Amount:           amount,
		Currency:         currency,
		InstallmentCount: installment,
		PaymentType:      paymentType,
		Customer: paymentCustomer{
			FullName:       strings.TrimSpace(request.Buyer.Name + " " + request.Buyer.Surname),
			Phone:          phone,
			Email:          request.Buyer.Email,
			IdentityNumber: request.Buyer.IdentityNumber,
			IPAddress:      request.Buyer.IP,
		},
		Card: paymentCard{
			CardHolderName: card.CardHolderName,
			CardNumber:     whitespaceRe.ReplaceAllString(card.CardNumber, ""),
			Expiry:         expiry,
			SecurityCode:   card.CVC,
		},
	})
	if err != nil {
		return "", err
	}
	return string(payload), nil
}

func (p *Provider) initFailure(err error, merchantOrderID string) *core.ThreeDSInitResponse {
	result := core.ApplyFailure(failureLabel, err, "3DS initialization failed", &core.ThreeDSInitResponse{})
	result.PaymentID = merchantOrderID
	result.ConversationID = merchantOrderID
	result.Code = p.ErrorCodeFor(result.Status, result.ErrorCode)
	return result
}

// CompleteThreeDSPayment completes the payment after the successUrl POST.
//
// The callback is not signed, so it is never trusted on its own: the order is
// looked up at the bank, the bank's order id must match, and the payment succeeds
// only when Provision (which charges the card) succeeds. The amount sent with
// Provision is the bank's recorded amount, not a callback field.
//
// PaymentID and ConversationID are set only once the order is confirmed at the
// bank: on success, and on a timeout after the order was confirmed (the outcome
// is then unknown). A failure never carries ids taken from the callback, so a
// forged callback cannot name an existing order. A callback that does not match
// an order at the bank is rejected with INVALID_HASH.
//
// callbackData is a ThreeDSCallbackData, or the callback POST as a
// map[string]string or map[string]any.
func (p *Provider) CompleteThreeDSPayment(ctx context.Context, callbackData any) (*core.PaymentResponse, error) {
	fields := callbackStrings(callbackData)
	untrusted := func(message string, raw any) *core.PaymentResponse {
		result := &core.PaymentResponse{
			Status:       core.StatusFailure,
			ErrorCode:    "INVALID_HASH",
			ErrorMessage: message,
			RawResponse:  raw,
		}
		result.Code = p.ErrorCodeFor(result.Status, result.ErrorCode)
		return result
	}

	if strings.ToLower(fields["Success"]) != "true" {
		result := &core.PaymentResponse{
			Status:       core.StatusFailure,
			ErrorCode:    orDefault(fields["ResponseCode"], "INVALID_CALLBACK"),
			ErrorMessage: orDefault(fields["ResponseMessage"], "3D Secure verification failed"),
			RawResponse:  callbackData,
		}
		result.Code = p.ErrorCodeFor(result.Status, result.ErrorCode)
		return result, nil
	}

	merchantOrderID, orderID, md := fields["MerchantOrderId"], fields["OrderId"], fields["MD"]
	if merchantOrderID == "" || orderID == "" || md == "" {
		return untrusted("Callback is missing MerchantOrderId, OrderId or MD", callbackData), nil
	}

	lookup, err := p.findTransaction(ctx, merchantOrderID, &orderID)
	if err != nil {
		return p.completeFailure(err, false, merchantOrderID), nil
	}
	tx := lookup.tx
	if tx == nil || tx.FirstAmount == nil {
		return untrusted("The callback does not match a Kuveyt Türk transaction", map[string]any{
			"callback":     callbackData,
			"transactions": lookup.raw,
		}), nil
	}

	amount := strconv.FormatInt(int64(math.Round(*tx.FirstAmount)), 10)
	hashData, err := CreateProvisionHash(p.credentials(), merchantOrderID, amount)
	if err != nil {
		return p.completeFailure(err, true, merchantOrderID), nil
	}
	provision, raw, err := p.post(ctx, EndpointProvision, provisionBody{
		accountFields:   p.account(),
		MerchantOrderId: merchantOrderID,
		OrderId:         orderID,
		Amount:          amount,
		MD:              md,
		HashData:        hashData,
	}, false)
	if err != nil {
		return p.completeFailure(err, true, merchantOrderID), nil
	}

	rawResponse := map[string]any{"callback": callbackData, "provision": raw}
	if provision.Success == nil || !*provision.Success {
		result := &core.PaymentResponse{
			Status:       core.StatusFailure,
			ErrorCode:    provision.ResponseCode,
			ErrorMessage: provision.ResponseMessage,
			RawResponse:  rawResponse,
		}
		result.Code = p.ErrorCodeFor(result.Status, result.ErrorCode)
		return result, nil
	}
	return &core.PaymentResponse{
		Status:         core.StatusSuccess,
		PaymentID:      merchantOrderID,
		ConversationID: merchantOrderID,
		RawResponse:    rawResponse,
	}, nil
}

func (p *Provider) completeFailure(err error, confirmed bool, merchantOrderID string) *core.PaymentResponse {
	result := core.ApplyFailure(failureLabel, err, "3D Secure completion failed", &core.PaymentResponse{})
	// A timeout after the order was confirmed leaves the outcome unknown: keep
	// the confirmed ids so the order can be checked with GetPayment.
	if confirmed && result.Status == core.StatusPending {
		result.PaymentID = merchantOrderID
		result.ConversationID = merchantOrderID
	}
	result.Code = p.ErrorCodeFor(result.Status, result.ErrorCode)
	return result
}

func (p *Provider) saleReversal(ctx context.Context, merchantOrderID string, reversalType SaleReversalType, amount string, tx *Transaction) (APIResponse, any, error) {
	hashData, err := CreateSaleReversalHash(p.credentials(), merchantOrderID, reversalType, amount)
	if err != nil {
		return APIResponse{}, nil, err
	}
	return p.post(ctx, EndpointSaleReversal, saleReversalBody{
		accountFields:    p.account(),
		MerchantOrderId:  merchantOrderID,
		OrderId:          tx.OrderId.String(),
		SaleReversalType: reversalType,
		Amount:           amount,
		HashData:         hashData,
	}, false)
}

func notFound(data APIResponse) (string, string) {
	if data.Success != nil && !*data.Success {
		return data.ResponseCode, data.ResponseMessage
	}
	return "OrderIdNotFound", "No Kuveyt Türk transaction for this order id"
}

// Refund refunds after end of day. PaymentID is the merchant order id. Refunding
// the whole amount sends "Drawback", a lower amount "PartialDrawback".
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
	lookup, err := p.findTransaction(ctx, request.PaymentID, nil)
	if err != nil {
		return fail(err), nil
	}
	if lookup.tx == nil {
		code, message := notFound(lookup.data)
		result := &core.RefundResponse{
			Status:         core.StatusFailure,
			ConversationID: request.ConversationID,
			ErrorCode:      code,
			ErrorMessage:   message,
			RawResponse:    lookup.raw,
		}
		result.Code = p.ErrorCodeFor(result.Status, result.ErrorCode)
		return result, nil
	}

	tx := lookup.tx
	amount, err := core.ToMinorUnits(request.Price, "")
	if err != nil {
		return fail(err), nil
	}
	full := (tx.DrawbackAmount == nil || *tx.DrawbackAmount == 0) &&
		tx.FirstAmount != nil && amount == int64(math.Round(*tx.FirstAmount))
	reversalType := SaleReversalPartialDrawback
	if full {
		reversalType = SaleReversalDrawback
	}
	response, raw, err := p.saleReversal(ctx, request.PaymentID, reversalType, strconv.FormatInt(amount, 10), tx)
	if err != nil {
		return fail(err), nil
	}

	approved := response.Success != nil && *response.Success
	result := &core.RefundResponse{
		Status:         core.StatusFailure,
		ConversationID: request.ConversationID,
		RawResponse:    raw,
	}
	if approved {
		result.Status = core.StatusSuccess
		result.RefundID = response.BusinessKey
	} else {
		result.ErrorCode = response.ResponseCode
		result.ErrorMessage = response.ResponseMessage
	}
	result.Code = p.ErrorCodeFor(result.Status, result.ErrorCode)
	return result, nil
}

// Cancel voids a sale the same day ("Cancel"), before end of day. PaymentID is
// the merchant order id. Without Price, the bank's recorded amount is used.
func (p *Provider) Cancel(ctx context.Context, request *core.CancelRequest) (*core.CancelResponse, error) {
	if request == nil {
		request = &core.CancelRequest{}
	}
	fail := func(err error) *core.CancelResponse {
		result := core.ApplyFailure(failureLabel, err, "Cancel failed", &core.CancelResponse{})
		result.ConversationID = request.ConversationID
		result.Code = p.ErrorCodeFor(result.Status, result.ErrorCode)
		return result
	}

	lookup, err := p.findTransaction(ctx, request.PaymentID, nil)
	if err != nil {
		return fail(err), nil
	}
	if lookup.tx == nil {
		code, message := notFound(lookup.data)
		result := &core.CancelResponse{
			Status:         core.StatusFailure,
			ConversationID: request.ConversationID,
			ErrorCode:      code,
			ErrorMessage:   message,
			RawResponse:    lookup.raw,
		}
		result.Code = p.ErrorCodeFor(result.Status, result.ErrorCode)
		return result, nil
	}

	tx := lookup.tx
	amount := strconv.FormatInt(minorUnits(tx.FirstAmount), 10)
	if request.Price != "" {
		amount, err = FormatAmount(request.Price)
		if err != nil {
			return fail(err), nil
		}
	}
	response, raw, err := p.saleReversal(ctx, request.PaymentID, SaleReversalCancel, amount, tx)
	if err != nil {
		return fail(err), nil
	}

	approved := response.Success != nil && *response.Success
	result := &core.CancelResponse{
		Status:         core.StatusFailure,
		ConversationID: request.ConversationID,
		RawResponse:    raw,
	}
	if approved {
		result.Status = core.StatusSuccess
		result.TransactionID = response.BusinessKey
	} else {
		result.ErrorCode = response.ResponseCode
		result.ErrorMessage = response.ResponseMessage
	}
	result.Code = p.ErrorCodeFor(result.Status, result.ErrorCode)
	return result, nil
}

// GetPayment reports the order status via GetTransactions. paymentID is the
// merchant order id.
func (p *Provider) GetPayment(ctx context.Context, paymentID string) (*core.PaymentResponse, error) {
	fail := func(err error) *core.PaymentResponse {
		result := core.ApplyFailure(failureLabel, err, "Get payment failed", &core.PaymentResponse{})
		result.PaymentID = paymentID
		result.ConversationID = paymentID
		result.Code = p.ErrorCodeFor(result.Status, result.ErrorCode)
		return result
	}

	lookup, err := p.findTransaction(ctx, paymentID, nil)
	if err != nil {
		return fail(err), nil
	}
	result := &core.PaymentResponse{
		PaymentID:      paymentID,
		ConversationID: paymentID,
		RawResponse:    lookup.raw,
	}
	if lookup.data.Success == nil || !*lookup.data.Success {
		result.Status = core.StatusFailure
		result.ErrorCode = lookup.data.ResponseCode
		result.ErrorMessage = lookup.data.ResponseMessage
		result.Code = p.ErrorCodeFor(result.Status, result.ErrorCode)
		return result, nil
	}

	result.Status = MapTransactionStatus(lookup.tx)
	if result.Status == core.StatusFailure && lookup.tx != nil {
		result.ErrorCode = lookup.tx.ResponseCode
		result.ErrorMessage = lookup.tx.ResponseExplain
	}
	result.Code = p.ErrorCodeFor(result.Status, result.ErrorCode)
	return result, nil
}

// MapTransactionStatus reads TransactionStatus: "Başarılı (1)", "Başarısız (2)",
// "Zaman Aşımı (3)". A fully cancelled or refunded sale is reported as cancelled.
func MapTransactionStatus(tx *Transaction) core.PaymentStatus {
	if tx == nil {
		return core.StatusPending
	}
	code, ok := StatusCode(tx.TransactionStatus)
	if !ok {
		return core.StatusPending
	}
	switch code {
	case 1:
		first := minorUnits(tx.FirstAmount)
		reversed := minorUnits(tx.CancelAmount) + minorUnits(tx.DrawbackAmount)
		if first > 0 && reversed >= first {
			return core.StatusCancelled
		}
		return core.StatusSuccess
	case 2, 3:
		return core.StatusFailure
	default:
		return core.StatusPending
	}
}

func minorUnits(amount *float64) int64 {
	if amount == nil {
		return 0
	}
	return int64(math.Round(*amount))
}

// callbackStrings reads the callback fields by their exact names. Only string
// values are read: the bank posts form fields, and a non-string value is not a
// field the provider accepts.
func callbackStrings(data any) map[string]string {
	fields := make(map[string]string)
	switch value := data.(type) {
	case nil:
	case map[string]string:
		for key, item := range value {
			fields[key] = item
		}
	case map[string]any:
		for key, item := range value {
			if text, ok := item.(string); ok {
				fields[key] = text
			}
		}
	default:
		encoded, err := json.Marshal(data)
		if err != nil {
			return fields
		}
		var decoded map[string]any
		if json.Unmarshal(encoded, &decoded) != nil {
			return fields
		}
		for key, item := range decoded {
			if text, ok := item.(string); ok {
				fields[key] = text
			}
		}
	}
	return fields
}

type accountFields struct {
	MerchantId string `json:"merchantId"`
	CustomerId string `json:"customerId"`
	Username   string `json:"username"`
}

type paymentBody struct {
	Language        int    `json:"language"`
	MerchantOrderId string `json:"merchantOrderId"`
	SuccessUrl      string `json:"successUrl"`
	FailUrl         string `json:"failUrl"`
	accountFields
	HashData         string          `json:"hashData"`
	Amount           string          `json:"amount"`
	Currency         string          `json:"currency"`
	InstallmentCount int             `json:"installmentCount"`
	PaymentType      int             `json:"paymentType"`
	Customer         paymentCustomer `json:"customer"`
	Card             paymentCard     `json:"card"`
}

type paymentCustomer struct {
	FullName string `json:"fullName"`
	Phone
	Email          string `json:"email"`
	IdentityNumber string `json:"identityNumber,omitempty"`
	IPAddress      string `json:"ipAddress"`
}

type paymentCard struct {
	CardHolderName string `json:"cardHolderName"`
	CardNumber     string `json:"cardNumber"`
	Expiry
	SecurityCode string `json:"securityCode"`
}

type provisionBody struct {
	accountFields
	MerchantOrderId string `json:"merchantOrderId"`
	OrderId         string `json:"orderId"`
	Amount          string `json:"amount"`
	MD              string `json:"md"`
	HashData        string `json:"hashData"`
}

type getTransactionsBody struct {
	accountFields
	MerchantOrderId string `json:"merchantOrderId"`
	HashData        string `json:"hashData"`
}

type saleReversalBody struct {
	accountFields
	MerchantOrderId  string           `json:"merchantOrderId"`
	OrderId          string           `json:"orderId"`
	SaleReversalType SaleReversalType `json:"saleReversalType"`
	Amount           string           `json:"amount"`
	HashData         string           `json:"hashData"`
}
