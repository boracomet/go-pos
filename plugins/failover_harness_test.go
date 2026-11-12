package plugins

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"sync"
	"testing"

	"github.com/boracomet/go-pos/core"
)

// This file is the shared harness of the six plugin ports that live in
// failover.go, fraud_rules.go, mcp.go, openapi.go, currency_conversion.go and
// refund_approval.go: request fixtures, a scripted provider, a facade that runs
// hooks and events the way core documents them, and a recording logger. Every
// identifier carries the failoverTest prefix so the other plugins of this
// package can define their own helpers.

const (
	failoverTestCardSuccess           = "4242424242424242"
	failoverTestCardMastercard        = "5555555555554444"
	failoverTestCardDeclined          = "4000000000000002"
	failoverTestCardExpired           = "4000000000000069"
	failoverTestCardInsufficientFunds = "4000000000009995"
	failoverTestCardProviderError     = "4000000000000119"
	failoverTestCardNetworkError      = "4000000000000341"
)

func failoverTestCard() core.PaymentCard {
	register := true
	return core.PaymentCard{
		CardHolderName: "John Doe",
		CardNumber:     "5528790000000008",
		ExpireMonth:    "12",
		ExpireYear:     "2030",
		CVC:            "123",
		RegisterCard:   &register,
	}
}

func failoverTestBuyer() core.Buyer {
	return core.Buyer{
		ID:                  "BY789",
		Name:                "John",
		Surname:             "Doe",
		GSMNumber:           "+905350000000",
		Email:               "john.doe@example.com",
		IdentityNumber:      "74300864791",
		RegistrationAddress: "Nidakule Goztepe, Merdivenkoy Mah. Bora Sok. No:1",
		IP:                  "85.34.78.112",
		City:                "Istanbul",
		Country:             "Turkey",
		ZipCode:             "34732",
	}
}

func failoverTestAddress() core.Address {
	return core.Address{
		ContactName: "John Doe",
		City:        "Istanbul",
		Country:     "Turkey",
		Address:     "Nidakule Goztepe, Merdivenkoy Mah. Bora Sok. No:1",
		ZipCode:     "34732",
	}
}

func failoverTestBasket() []core.BasketItem {
	return []core.BasketItem{
		{ID: "BI101", Name: "Binocular", Category1: "Collectibles", Category2: "Accessories", ItemType: string(core.BasketItemPhysical), Price: "0.3"},
		{ID: "BI102", Name: "Game code", Category1: "Game", Category2: "Online Game Items", ItemType: string(core.BasketItemVirtual), Price: "0.5"},
		{ID: "BI103", Name: "Usb", Category1: "Electronics", Category2: "Usb / Cable", ItemType: string(core.BasketItemPhysical), Price: "0.2"},
	}
}

func failoverTestRequest() *core.PaymentRequest {
	card := failoverTestCard()
	return &core.PaymentRequest{
		Price:           "1",
		PaidPrice:       "1.2",
		Currency:        string(core.CurrencyTRY),
		BasketID:        "B67832",
		PaymentCard:     &card,
		Buyer:           failoverTestBuyer(),
		ShippingAddress: failoverTestAddress(),
		BillingAddress:  failoverTestAddress(),
		BasketItems:     failoverTestBasket(),
		ConversationID:  "123456789",
	}
}

func failoverTestCardRequest(cardNumber string) *core.PaymentRequest {
	request := failoverTestRequest()
	card := failoverTestCard()
	card.CardNumber = cardNumber
	request.PaymentCard = &card
	return request
}

func failoverTestThreeDSRequest() *core.ThreeDSPaymentRequest {
	return &core.ThreeDSPaymentRequest{PaymentRequest: *failoverTestRequest()}
}

func failoverTestThreeDSRequestWithCallback() *core.ThreeDSPaymentRequest {
	request := failoverTestThreeDSRequest()
	request.CallbackURL = "https://example.com/callback"
	return request
}

func failoverTestRefundRequest() *core.RefundRequest {
	return &core.RefundRequest{
		PaymentID:      "12345678",
		Price:          "0.5",
		Currency:       string(core.CurrencyTRY),
		IP:             "85.34.78.112",
		ConversationID: "123456789",
	}
}

func failoverTestCancelRequest() *core.CancelRequest {
	return &core.CancelRequest{
		PaymentID:      "12345678",
		IP:             "85.34.78.112",
		ConversationID: "123456789",
	}
}

// failoverTestJSONMap is the JSON view of a value as a record.
func failoverTestJSONMap(value any) map[string]any {
	if value == nil {
		return map[string]any{}
	}
	if record, ok := value.(map[string]any); ok {
		return record
	}
	data, err := json.Marshal(value)
	if err != nil {
		return map[string]any{}
	}
	var out map[string]any
	if err := json.Unmarshal(data, &out); err != nil || out == nil {
		return map[string]any{}
	}
	return out
}

// failoverTestRecord is the JSON view of a result, so a test can read fields
// without knowing whether the plugin returned a typed struct or a map.
func failoverTestRecord(t *testing.T, value any) map[string]any {
	t.Helper()
	return failoverTestJSONMap(value)
}

// failoverTestJSON is the serialized form of a value, used to scan for leaked
// card data without depending on the concrete result type.
func failoverTestJSON(t *testing.T, value any) string {
	t.Helper()
	data, err := json.Marshal(value)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	return string(data)
}

func failoverTestText(record map[string]any, key string) string {
	if value, ok := record[key].(string); ok {
		return value
	}
	return ""
}

func failoverTestNumber(record map[string]any, key string) float64 {
	if value, ok := record[key].(float64); ok {
		return value
	}
	return 0
}

// failoverTestDigits keeps the digits of a card number, the way the mock provider
// matches its magic cards.
func failoverTestDigits(value string) string {
	var out strings.Builder
	for _, character := range value {
		if character >= '0' && character <= '9' {
			out.WriteRune(character)
		}
	}
	return out.String()
}

type failoverTestCall struct {
	Provider  string
	Operation core.PaymentOperation
	Request   any
}

type failoverTestPayment struct {
	response  *core.PaymentResponse
	refunded  bool
	cancelled bool
}

// failoverTestProvider is a scripted provider: respond answers an operation with
// the result it returns, and every other call falls back to a small payment
// state machine (magic cards, refunds of succeeded payments, forged callbacks).
type failoverTestProvider struct {
	core.BaseProvider
	id       string
	mu       sync.Mutex
	calls    []failoverTestCall
	payments map[string]*failoverTestPayment
	sequence int
	respond  func(operation core.PaymentOperation, request any) (any, error)
}

func failoverTestNewProvider(id string) *failoverTestProvider {
	return &failoverTestProvider{
		BaseProvider: core.NewBaseProvider(id, core.ProviderConfig{}),
		id:           id,
		payments:     map[string]*failoverTestPayment{},
	}
}

func (p *failoverTestProvider) record(operation core.PaymentOperation, request any) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.calls = append(p.calls, failoverTestCall{Provider: p.id, Operation: operation, Request: request})
}

// count is how many times an operation reached this provider.
func (p *failoverTestProvider) count(operation core.PaymentOperation) int {
	p.mu.Lock()
	defer p.mu.Unlock()
	total := 0
	for _, call := range p.calls {
		if call.Operation == operation {
			total++
		}
	}
	return total
}

func (p *failoverTestProvider) callsOf(operation core.PaymentOperation) []failoverTestCall {
	p.mu.Lock()
	defer p.mu.Unlock()
	out := make([]failoverTestCall, 0)
	for _, call := range p.calls {
		if call.Operation == operation {
			out = append(out, call)
		}
	}
	return out
}

func (p *failoverTestProvider) lastRequest(operation core.PaymentOperation) any {
	calls := p.callsOf(operation)
	if len(calls) == 0 {
		return nil
	}
	return calls[len(calls)-1].Request
}

// failoverTestScripted records a call and answers it from respond when set. The
// second result is false when the default behaviour should run.
func failoverTestScripted[T any](p *failoverTestProvider, operation core.PaymentOperation, request any) (*T, bool, error) {
	p.record(operation, request)
	if p.respond == nil {
		return nil, false, nil
	}
	result, err := p.respond(operation, request)
	if err != nil {
		return nil, true, err
	}
	if result == nil {
		return nil, false, nil
	}
	if typed, ok := result.(*T); ok {
		return typed, true, nil
	}
	if value, ok := result.(T); ok {
		return &value, true, nil
	}
	return nil, false, nil
}

func (p *failoverTestProvider) CreatePayment(_ context.Context, request *core.PaymentRequest) (*core.PaymentResponse, error) {
	if result, handled, err := failoverTestScripted[core.PaymentResponse](p, core.OperationCreatePayment, request); handled {
		return result, err
	}
	return p.pay(request)
}

func (p *failoverTestProvider) Authorize(_ context.Context, request *core.PaymentRequest) (*core.PaymentResponse, error) {
	if result, handled, err := failoverTestScripted[core.PaymentResponse](p, core.OperationAuthorize, request); handled {
		return result, err
	}
	return p.pay(request)
}

func (p *failoverTestProvider) InitThreeDSPayment(_ context.Context, request *core.ThreeDSPaymentRequest) (*core.ThreeDSInitResponse, error) {
	if result, handled, err := failoverTestScripted[core.ThreeDSInitResponse](p, core.OperationInitThreeDSPayment, request); handled {
		return result, err
	}
	return p.initThreeDS(&request.PaymentRequest)
}

func (p *failoverTestProvider) InitThreeDSAuthorize(_ context.Context, request *core.ThreeDSPaymentRequest) (*core.ThreeDSInitResponse, error) {
	if result, handled, err := failoverTestScripted[core.ThreeDSInitResponse](p, core.OperationInitThreeDSAuthorize, request); handled {
		return result, err
	}
	return p.initThreeDS(&request.PaymentRequest)
}

func (p *failoverTestProvider) CompleteThreeDSPayment(_ context.Context, callbackData any) (*core.PaymentResponse, error) {
	if result, handled, err := failoverTestScripted[core.PaymentResponse](p, core.OperationCompleteThreeDSPayment, callbackData); handled {
		return result, err
	}
	record := failoverTestJSONMap(callbackData)
	paymentID := failoverTestText(record, "paymentId")
	signature := failoverTestText(record, "signature")
	if signature != "sig:"+paymentID {
		return &core.PaymentResponse{
			Status:       core.StatusFailure,
			PaymentID:    paymentID,
			Code:         core.CodeInvalidHash,
			ErrorCode:    "INVALID_HASH",
			ErrorMessage: "Callback signature is invalid",
		}, nil
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	if payment, ok := p.payments[paymentID]; ok {
		payment.response.Status = core.StatusSuccess
		return payment.response, nil
	}
	return &core.PaymentResponse{Status: core.StatusSuccess, PaymentID: paymentID}, nil
}

func (p *failoverTestProvider) Capture(_ context.Context, request *core.CaptureRequest) (*core.PaymentResponse, error) {
	if result, handled, err := failoverTestScripted[core.PaymentResponse](p, core.OperationCapture, request); handled {
		return result, err
	}
	return &core.PaymentResponse{Status: core.StatusSuccess, PaymentID: request.PaymentID, ConversationID: request.ConversationID}, nil
}

func (p *failoverTestProvider) VoidAuthorization(_ context.Context, request *core.VoidAuthorizationRequest) (*core.CancelResponse, error) {
	if result, handled, err := failoverTestScripted[core.CancelResponse](p, core.OperationVoidAuthorization, request); handled {
		return result, err
	}
	return &core.CancelResponse{Status: core.StatusSuccess, ConversationID: request.ConversationID}, nil
}

func (p *failoverTestProvider) Refund(_ context.Context, request *core.RefundRequest) (*core.RefundResponse, error) {
	if result, handled, err := failoverTestScripted[core.RefundResponse](p, core.OperationRefund, request); handled {
		return result, err
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	payment, ok := p.payments[request.PaymentID]
	if !ok {
		return &core.RefundResponse{
			Status: core.StatusFailure, Code: core.CodeInvalidRequest,
			ErrorCode: "PAYMENT_NOT_FOUND", ErrorMessage: "Payment not found",
		}, nil
	}
	if payment.response.Status != core.StatusSuccess {
		return &core.RefundResponse{
			Status: core.StatusFailure, Code: core.CodeInvalidRequest,
			ErrorCode: "INVALID_REQUEST", ErrorMessage: "Only a succeeded payment can be refunded",
		}, nil
	}
	payment.refunded = true
	p.sequence++
	return &core.RefundResponse{
		Status:         core.StatusSuccess,
		RefundID:       fmt.Sprintf("REF-%d", p.sequence),
		ConversationID: request.ConversationID,
	}, nil
}

func (p *failoverTestProvider) Cancel(_ context.Context, request *core.CancelRequest) (*core.CancelResponse, error) {
	if result, handled, err := failoverTestScripted[core.CancelResponse](p, core.OperationCancel, request); handled {
		return result, err
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	payment, ok := p.payments[request.PaymentID]
	if !ok {
		return &core.CancelResponse{
			Status: core.StatusFailure, Code: core.CodeInvalidRequest,
			ErrorCode: "PAYMENT_NOT_FOUND", ErrorMessage: "Payment not found",
		}, nil
	}
	if payment.response.Status != core.StatusSuccess || payment.refunded {
		return &core.CancelResponse{
			Status: core.StatusFailure, Code: core.CodeInvalidRequest,
			ErrorCode: "INVALID_REQUEST", ErrorMessage: "Only a succeeded, unrefunded payment can be cancelled",
		}, nil
	}
	payment.cancelled = true
	payment.response.Status = core.StatusCancelled
	p.sequence++
	return &core.CancelResponse{
		Status:         core.StatusSuccess,
		TransactionID:  fmt.Sprintf("CNL-%d", p.sequence),
		ConversationID: request.ConversationID,
	}, nil
}

func (p *failoverTestProvider) GetPayment(_ context.Context, paymentID string) (*core.PaymentResponse, error) {
	if result, handled, err := failoverTestScripted[core.PaymentResponse](p, core.OperationGetPayment, paymentID); handled {
		return result, err
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	if payment, ok := p.payments[paymentID]; ok {
		return payment.response, nil
	}
	return &core.PaymentResponse{
		Status: core.StatusFailure, Code: core.CodeInvalidRequest,
		ErrorCode: "PAYMENT_NOT_FOUND", ErrorMessage: "Payment not found",
	}, nil
}

func (p *failoverTestProvider) BinCheck(_ context.Context, binNumber string) (*core.BinCheckResponse, error) {
	if result, handled, err := failoverTestScripted[core.BinCheckResponse](p, core.OperationBinCheck, binNumber); handled {
		return result, err
	}
	return &core.BinCheckResponse{
		BinNumber:       binNumber,
		CardType:        "CREDIT_CARD",
		CardAssociation: "VISA",
		CardFamily:      "Bonus",
		BankName:        "Mock Bank",
		BankCode:        999,
	}, nil
}

func (p *failoverTestProvider) InstallmentInfo(_ context.Context, request *core.InstallmentInfoRequest) (*core.InstallmentInfoResponse, error) {
	if result, handled, err := failoverTestScripted[core.InstallmentInfoResponse](p, core.OperationInstallmentInfo, request); handled {
		return result, err
	}
	price := 0.0
	if parsed, err := failoverTestParseAmount(request.Price); err == nil {
		price = parsed
	}
	details := make([]core.InstallmentDetail, 0, 4)
	for _, plan := range []struct {
		count int
		rate  float64
	}{{1, 0}, {2, 2}, {3, 3}, {6, 5}} {
		total := price * (1 + plan.rate/100)
		rate := plan.rate
		details = append(details, core.InstallmentDetail{
			InstallmentNumber: plan.count,
			TotalPrice:        total,
			InstallmentPrice:  total / float64(plan.count),
			CommissionRate:    &rate,
		})
	}
	return &core.InstallmentInfoResponse{
		Status:         core.StatusSuccess,
		ConversationID: request.ConversationID,
		InstallmentDetails: []core.InstallmentPrice{{
			BinNumber:         request.BinNumber,
			Price:             price,
			CardType:          "CREDIT_CARD",
			CardAssociation:   "VISA",
			BankCode:          999,
			BankName:          "Mock Bank",
			InstallmentPrices: details,
		}},
	}, nil
}

func failoverTestParseAmount(value string) (float64, error) {
	return strconv.ParseFloat(strings.TrimSpace(value), 64)
}

func (p *failoverTestProvider) pay(request *core.PaymentRequest) (*core.PaymentResponse, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	conversationID := ""
	if request != nil {
		conversationID = request.ConversationID
	}
	if request == nil || request.PaymentCard == nil {
		return &core.PaymentResponse{
			Status: core.StatusFailure, Code: core.CodeInvalidRequest,
			ErrorCode: "VALIDATION_ERROR", ErrorMessage: "paymentCard is required",
			ConversationID: conversationID,
		}, nil
	}
	p.sequence++
	paymentID := fmt.Sprintf("PAY-%d", p.sequence)
	response := &core.PaymentResponse{PaymentID: paymentID, ConversationID: conversationID}
	switch failoverTestDigits(request.PaymentCard.CardNumber) {
	case failoverTestCardDeclined:
		response.Status = core.StatusFailure
		response.Code = core.CodeCardDeclined
		response.ErrorCode = "CARD_DECLINED"
		response.ErrorMessage = "Card declined"
	case failoverTestCardExpired:
		response.Status = core.StatusFailure
		response.Code = core.CodeExpiredCard
		response.ErrorCode = "EXPIRED_CARD"
		response.ErrorMessage = "Card expired"
	case failoverTestCardInsufficientFunds:
		response.Status = core.StatusFailure
		response.Code = core.CodeInsufficientFunds
		response.ErrorCode = "INSUFFICIENT_FUNDS"
		response.ErrorMessage = "Insufficient funds"
	case failoverTestCardProviderError:
		response.Status = core.StatusFailure
		response.Code = core.CodeProviderError
		response.ErrorCode = "PROVIDER_ERROR"
		response.ErrorMessage = "Provider error"
	case failoverTestCardNetworkError:
		response.Status = core.StatusPending
		response.Code = core.CodeNetworkError
		response.ErrorCode = "NETWORK_ERROR"
		response.ErrorMessage = "No response arrived"
	default:
		response.Status = core.StatusSuccess
	}
	p.payments[paymentID] = &failoverTestPayment{response: response}
	return response, nil
}

func (p *failoverTestProvider) initThreeDS(request *core.PaymentRequest) (*core.ThreeDSInitResponse, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.sequence++
	paymentID := fmt.Sprintf("PAY-%d", p.sequence)
	conversationID := ""
	if request != nil {
		conversationID = request.ConversationID
	}
	p.payments[paymentID] = &failoverTestPayment{response: &core.PaymentResponse{
		Status: core.StatusPending, PaymentID: paymentID, ConversationID: conversationID,
	}}
	return &core.ThreeDSInitResponse{
		Status:             core.StatusPending,
		PaymentID:          paymentID,
		ConversationID:     conversationID,
		ThreeDSHTMLContent: "<html>3D Secure</html>",
	}, nil
}

// failoverTestHookedProvider is what PluginContext.Use returns: every operation
// runs with hooks and events, and is not routable, so a failover cannot recurse.
type failoverTestHookedProvider struct {
	core.Provider
	facade   *failoverTestFacade
	provider string
}

func (p *failoverTestHookedProvider) run(ctx context.Context, operation core.PaymentOperation, request any) (any, error) {
	return p.facade.Run(ctx, operation, p.provider, request, core.RunOptions{Routable: false})
}

func (p *failoverTestHookedProvider) CreatePayment(ctx context.Context, request *core.PaymentRequest) (*core.PaymentResponse, error) {
	result, err := p.run(ctx, core.OperationCreatePayment, request)
	if err != nil {
		return nil, err
	}
	return failoverAs[core.PaymentResponse](result)
}

func (p *failoverTestHookedProvider) Authorize(ctx context.Context, request *core.PaymentRequest) (*core.PaymentResponse, error) {
	result, err := p.run(ctx, core.OperationAuthorize, request)
	if err != nil {
		return nil, err
	}
	return failoverAs[core.PaymentResponse](result)
}

func (p *failoverTestHookedProvider) InitThreeDSPayment(ctx context.Context, request *core.ThreeDSPaymentRequest) (*core.ThreeDSInitResponse, error) {
	result, err := p.run(ctx, core.OperationInitThreeDSPayment, request)
	if err != nil {
		return nil, err
	}
	return failoverAs[core.ThreeDSInitResponse](result)
}

func (p *failoverTestHookedProvider) InitThreeDSAuthorize(ctx context.Context, request *core.ThreeDSPaymentRequest) (*core.ThreeDSInitResponse, error) {
	result, err := p.run(ctx, core.OperationInitThreeDSAuthorize, request)
	if err != nil {
		return nil, err
	}
	return failoverAs[core.ThreeDSInitResponse](result)
}

func (p *failoverTestHookedProvider) CompleteThreeDSPayment(ctx context.Context, callbackData any) (*core.PaymentResponse, error) {
	result, err := p.run(ctx, core.OperationCompleteThreeDSPayment, callbackData)
	if err != nil {
		return nil, err
	}
	return failoverAs[core.PaymentResponse](result)
}

func (p *failoverTestHookedProvider) Capture(ctx context.Context, request *core.CaptureRequest) (*core.PaymentResponse, error) {
	result, err := p.run(ctx, core.OperationCapture, request)
	if err != nil {
		return nil, err
	}
	return failoverAs[core.PaymentResponse](result)
}

func (p *failoverTestHookedProvider) VoidAuthorization(ctx context.Context, request *core.VoidAuthorizationRequest) (*core.CancelResponse, error) {
	result, err := p.run(ctx, core.OperationVoidAuthorization, request)
	if err != nil {
		return nil, err
	}
	return failoverAs[core.CancelResponse](result)
}

func (p *failoverTestHookedProvider) Refund(ctx context.Context, request *core.RefundRequest) (*core.RefundResponse, error) {
	result, err := p.run(ctx, core.OperationRefund, request)
	if err != nil {
		return nil, err
	}
	return failoverAs[core.RefundResponse](result)
}

func (p *failoverTestHookedProvider) Cancel(ctx context.Context, request *core.CancelRequest) (*core.CancelResponse, error) {
	result, err := p.run(ctx, core.OperationCancel, request)
	if err != nil {
		return nil, err
	}
	return failoverAs[core.CancelResponse](result)
}

func (p *failoverTestHookedProvider) GetPayment(ctx context.Context, paymentID string) (*core.PaymentResponse, error) {
	result, err := p.run(ctx, core.OperationGetPayment, paymentID)
	if err != nil {
		return nil, err
	}
	return failoverAs[core.PaymentResponse](result)
}

func (p *failoverTestHookedProvider) BinCheck(ctx context.Context, binNumber string) (*core.BinCheckResponse, error) {
	result, err := p.run(ctx, core.OperationBinCheck, binNumber)
	if err != nil {
		return nil, err
	}
	return failoverAs[core.BinCheckResponse](result)
}

func (p *failoverTestHookedProvider) InstallmentInfo(ctx context.Context, request *core.InstallmentInfoRequest) (*core.InstallmentInfoResponse, error) {
	result, err := p.run(ctx, core.OperationInstallmentInfo, request)
	if err != nil {
		return nil, err
	}
	return failoverAs[core.InstallmentInfoResponse](result)
}

type failoverTestSetup struct {
	Providers       []*failoverTestProvider
	DefaultProvider string
	Plugins         []core.Plugin
	Mode            core.Mode
	Logger          core.Logger
	Authorize       func(context.Context, core.HandlerContext) (bool, error)
	AllowedActions  []core.HandlerAction
	BasePath        string
}

// failoverTestFacade stands in for the payment facade: it owns the providers,
// runs the plugin hooks in registration order, emits events and serves the
// plugin endpoints through a real core.Handler.
type failoverTestFacade struct {
	mu              sync.Mutex
	providers       map[string]*failoverTestProvider
	providerOrder   []string
	defaultProvider string
	plugins         []core.Plugin
	mode            core.Mode
	logger          core.Logger
	listeners       map[core.PaymentEventName][]core.PaymentEventListener
	events          []*core.PaymentEvent
	methods         map[string]any
	errorCodes      map[string]string
	endpoints       []core.RegisteredEndpoint
	setup           failoverTestSetup
	handler         *core.Handler
}

func failoverTestNewFacade(t *testing.T, setup failoverTestSetup) *failoverTestFacade {
	t.Helper()
	facade := &failoverTestFacade{
		providers:  map[string]*failoverTestProvider{},
		plugins:    setup.Plugins,
		mode:       setup.Mode,
		logger:     setup.Logger,
		listeners:  map[core.PaymentEventName][]core.PaymentEventListener{},
		methods:    map[string]any{},
		errorCodes: map[string]string{},
		setup:      setup,
	}
	if facade.mode == "" {
		facade.mode = core.ModeSandbox
	}
	for _, provider := range setup.Providers {
		facade.providers[provider.id] = provider
		facade.providerOrder = append(facade.providerOrder, provider.id)
	}
	facade.defaultProvider = setup.DefaultProvider
	if facade.defaultProvider == "" && len(facade.providerOrder) > 0 {
		facade.defaultProvider = facade.providerOrder[0]
	}
	if facade.defaultProvider == "" {
		facade.defaultProvider = "mock"
	}
	ctx := context.Background()
	for _, plugin := range setup.Plugins {
		if plugin.Init != nil {
			if err := plugin.Init(ctx, facade.PluginContext()); err != nil {
				t.Fatalf("init %s: %v", plugin.ID, err)
			}
		}
		for code, message := range plugin.ErrorCodes {
			facade.errorCodes[code] = message
		}
		if plugin.Methods != nil {
			members, err := plugin.Methods(ctx, facade.PluginContext())
			if err != nil {
				t.Fatalf("methods of %s: %v", plugin.ID, err)
			}
			for name, member := range members {
				facade.methods[name] = member
			}
		}
		for name, endpoint := range plugin.Endpoints {
			facade.endpoints = append(facade.endpoints, core.RegisteredEndpoint{
				PaymentEndpoint: endpoint, PluginID: plugin.ID, Name: name,
			})
		}
		for event, listener := range plugin.Events {
			facade.listeners[event] = append(facade.listeners[event], listener)
		}
	}
	sort.SliceStable(facade.endpoints, func(i, j int) bool {
		return facade.endpoints[i].Path < facade.endpoints[j].Path
	})
	return facade
}

// failoverTestSingle builds a facade with one mock provider and one plugin.
func failoverTestSingle(t *testing.T, plugin core.Plugin, logger core.Logger) (*failoverTestFacade, *failoverTestProvider) {
	t.Helper()
	provider := failoverTestNewProvider("mock")
	facade := failoverTestNewFacade(t, failoverTestSetup{
		Providers: []*failoverTestProvider{provider},
		Plugins:   []core.Plugin{plugin},
		Logger:    logger,
		Authorize: func(context.Context, core.HandlerContext) (bool, error) { return true, nil },
	})
	return facade, provider
}

func (f *failoverTestFacade) ProviderIDs() []string {
	return append([]string{}, f.providerOrder...)
}

func (f *failoverTestFacade) ErrorCodes() map[string]string {
	out := make(map[string]string, len(f.errorCodes))
	for code, message := range f.errorCodes {
		out[code] = message
	}
	return out
}

func (f *failoverTestFacade) DefaultProvider() string { return f.defaultProvider }

func (f *failoverTestFacade) Mode() core.Mode { return f.mode }

func (f *failoverTestFacade) Logger() core.Logger { return f.logger }

func (f *failoverTestFacade) Use(providerID string) (core.Provider, error) {
	if _, ok := f.providers[providerID]; !ok {
		return nil, core.NewProviderNotEnabledError(providerID)
	}
	return &failoverTestHookedProvider{Provider: f.providers[providerID], facade: f, provider: providerID}, nil
}

func (f *failoverTestFacade) RawProvider(providerID string) (core.Provider, error) {
	provider, ok := f.providers[providerID]
	if !ok {
		return nil, core.NewProviderNotEnabledError(providerID)
	}
	return provider, nil
}

func (f *failoverTestFacade) IsProviderEnabled(providerID string) bool {
	_, ok := f.providers[providerID]
	return ok
}

func (f *failoverTestFacade) Endpoints() []core.RegisteredEndpoint {
	return append([]core.RegisteredEndpoint{}, f.endpoints...)
}

func (f *failoverTestFacade) PluginContext() core.PluginContext {
	return &failoverTestPluginContext{facade: f}
}

// failoverTestPluginContext is the plugin-facing view of the facade: it lists
// endpoints without their handlers, as core.PluginContext requires.
type failoverTestPluginContext struct {
	facade *failoverTestFacade
}

func (c *failoverTestPluginContext) ProviderIDs() []string { return c.facade.ProviderIDs() }

func (c *failoverTestPluginContext) ErrorCodes() map[string]string { return c.facade.ErrorCodes() }

func (c *failoverTestPluginContext) DefaultProvider() string { return c.facade.DefaultProvider() }

func (c *failoverTestPluginContext) Mode() core.Mode { return c.facade.Mode() }

func (c *failoverTestPluginContext) Logger() core.Logger { return c.facade.Logger() }

func (c *failoverTestPluginContext) Use(providerID string) (core.Provider, error) {
	return c.facade.Use(providerID)
}

func (c *failoverTestPluginContext) Emit(ctx context.Context, event *core.PaymentEvent) error {
	return c.facade.emitEvent(ctx, event)
}

func (c *failoverTestPluginContext) Endpoints() []core.EndpointInfo {
	out := make([]core.EndpointInfo, 0, len(c.facade.endpoints))
	for _, endpoint := range c.facade.endpoints {
		out = append(out, core.EndpointInfo{
			PluginID:   endpoint.PluginID,
			Name:       endpoint.Name,
			Method:     endpoint.Method,
			Path:       endpoint.Path,
			Privileged: endpoint.Privileged,
			OpenAPI:    endpoint.OpenAPI,
		})
	}
	return out
}

func (f *failoverTestFacade) emitEvent(ctx context.Context, event *core.PaymentEvent) error {
	f.mu.Lock()
	f.events = append(f.events, event)
	listeners := append([]core.PaymentEventListener{}, f.listeners[core.EventAll]...)
	listeners = append(listeners, f.listeners[core.PaymentEventName(event.Type)]...)
	f.mu.Unlock()
	for _, listener := range listeners {
		if err := listener(ctx, event); err != nil {
			return core.NewEventListenerError(event, event.Result, err)
		}
	}
	return nil
}

func (f *failoverTestFacade) Emit(ctx context.Context, operation core.PaymentOperation, providerID string, request, result any) error {
	event := core.EventFor(operation, providerID, request, result)
	if event == nil {
		return nil
	}
	return f.emitEvent(ctx, event)
}

func (f *failoverTestFacade) InterceptRequest(ctx context.Context, request *core.HandlerRequest, rctx core.RequestContext) (*core.HandlerResponse, error) {
	for _, plugin := range f.plugins {
		if plugin.OnRequest == nil {
			continue
		}
		response, err := plugin.OnRequest(ctx, request, rctx)
		if err != nil {
			return nil, err
		}
		if response != nil {
			return response, nil
		}
	}
	return nil, nil
}

func (f *failoverTestFacade) Respond(ctx context.Context, response *core.HandlerResponse, rctx core.ResponseContext) (*core.HandlerResponse, error) {
	current := response
	for _, plugin := range f.plugins {
		if plugin.OnResponse == nil {
			continue
		}
		replacement, err := plugin.OnResponse(ctx, current, rctx)
		if err != nil {
			if f.logger != nil {
				f.logger.Error("plugin OnResponse failed", err, nil)
			}
			continue
		}
		if replacement != nil {
			current = replacement
		}
	}
	return current, nil
}

// Run executes an operation: before hooks, the provider, after hooks, events.
func (f *failoverTestFacade) Run(ctx context.Context, operation core.PaymentOperation, providerID string, request any, options core.RunOptions) (any, error) {
	if providerID == "" {
		providerID = f.defaultProvider
	}
	octx := core.OperationContext{
		Operation: operation,
		Request:   request,
		Provider:  providerID,
		Routable:  options.Routable,
	}
	var result any
	shortCircuit := false
	for _, plugin := range f.plugins {
		for _, hook := range plugin.Hooks.Before {
			if hook.Matcher != nil && !hook.Matcher(octx) {
				continue
			}
			outcome, err := hook.Handler(octx)
			if err != nil {
				return nil, err
			}
			if outcome == nil {
				continue
			}
			if outcome.Provider != "" && octx.Routable {
				octx.Provider = outcome.Provider
			}
			if outcome.Request != nil {
				octx.Request = outcome.Request
			}
			if outcome.Result != nil {
				result = outcome.Result
				shortCircuit = true
			}
			if shortCircuit {
				break
			}
		}
		if shortCircuit {
			break
		}
	}
	if !shortCircuit {
		provider, ok := f.providers[octx.Provider]
		if !ok {
			return nil, core.NewProviderNotEnabledError(octx.Provider)
		}
		outcome, err := failoverCall(ctx, provider, operation, octx.Request)
		if err != nil {
			return nil, err
		}
		result = outcome
	}
	rctx := core.OperationResultContext{OperationContext: octx, Result: result}
	for _, plugin := range f.plugins {
		for _, hook := range plugin.Hooks.After {
			if hook.Matcher != nil && !hook.Matcher(rctx) {
				continue
			}
			outcome, err := hook.Handler(rctx)
			if err != nil {
				return nil, err
			}
			if outcome == nil {
				continue
			}
			if outcome.Result != nil {
				rctx.Result = outcome.Result
			}
			if outcome.Request != nil {
				rctx.Request = outcome.Request
			}
		}
	}
	if options.Emit == nil || *options.Emit {
		if err := f.Emit(ctx, operation, rctx.Provider, rctx.Request, rctx.Result); err != nil {
			return rctx.Result, err
		}
	}
	return rctx.Result, nil
}

// call runs a routable operation, the way the facade does.
func (f *failoverTestFacade) call(t *testing.T, operation core.PaymentOperation, request any) map[string]any {
	t.Helper()
	result, err := f.Run(context.Background(), operation, "", request, core.RunOptions{Routable: true})
	if err != nil {
		t.Fatalf("%s: %v", operation, err)
	}
	return failoverTestRecord(t, result)
}

func (f *failoverTestFacade) createPayment(t *testing.T, request *core.PaymentRequest) map[string]any {
	t.Helper()
	return f.call(t, core.OperationCreatePayment, request)
}

func (f *failoverTestFacade) initThreeDS(t *testing.T, request *core.ThreeDSPaymentRequest) map[string]any {
	t.Helper()
	return f.call(t, core.OperationInitThreeDSPayment, request)
}

func (f *failoverTestFacade) authorize(t *testing.T, request *core.PaymentRequest) map[string]any {
	t.Helper()
	return f.call(t, core.OperationAuthorize, request)
}

func (f *failoverTestFacade) refund(t *testing.T, request *core.RefundRequest) map[string]any {
	t.Helper()
	return f.call(t, core.OperationRefund, request)
}

func (f *failoverTestFacade) cancel(t *testing.T, request *core.CancelRequest) map[string]any {
	t.Helper()
	return f.call(t, core.OperationCancel, request)
}

func (f *failoverTestFacade) getPayment(t *testing.T, paymentID string) map[string]any {
	t.Helper()
	return f.call(t, core.OperationGetPayment, paymentID)
}

func (f *failoverTestFacade) completeThreeDS(t *testing.T, callback any) map[string]any {
	t.Helper()
	return f.call(t, core.OperationCompleteThreeDSPayment, callback)
}

// method returns a member a plugin added to the facade.
func (f *failoverTestFacade) method(t *testing.T, name string) any {
	t.Helper()
	member, ok := f.methods[name]
	if !ok {
		t.Fatalf("facade has no member %q", name)
	}
	return member
}

func (f *failoverTestFacade) eventCount(eventType core.PaymentEventType) int {
	f.mu.Lock()
	defer f.mu.Unlock()
	total := 0
	for _, event := range f.events {
		if event.Type == eventType {
			total++
		}
	}
	return total
}

func (f *failoverTestFacade) eventsOfType(eventType core.PaymentEventType) []*core.PaymentEvent {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([]*core.PaymentEvent, 0)
	for _, event := range f.events {
		if event.Type == eventType {
			out = append(out, event)
		}
	}
	return out
}

func (f *failoverTestFacade) on(eventType core.PaymentEventType, listener core.PaymentEventListener) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.listeners[core.PaymentEventName(eventType)] = append(f.listeners[core.PaymentEventName(eventType)], listener)
}

func (f *failoverTestFacade) basePath() string {
	if f.setup.BasePath != "" {
		return f.setup.BasePath
	}
	return "/api/pay"
}

func (f *failoverTestFacade) handlerFor(t *testing.T) *core.Handler {
	t.Helper()
	if f.handler == nil {
		authorize := f.setup.Authorize
		if authorize == nil {
			authorize = func(context.Context, core.HandlerContext) (bool, error) { return true, nil }
		}
		handler, err := core.NewHandler(f, core.HandlerOptions{
			BasePath:       f.basePath(),
			AllowedActions: f.setup.AllowedActions,
			Authorize:      authorize,
		})
		if err != nil {
			t.Fatalf("handler: %v", err)
		}
		f.handler = handler
	}
	return f.handler
}

// serve answers an HTTP request against the handler, with a path relative to the
// handler's base path.
func (f *failoverTestFacade) serve(t *testing.T, method, path string, body any, headers map[string]string) *core.HandlerResponse {
	t.Helper()
	request := &core.HandlerRequest{
		Method:  method,
		URL:     f.basePath() + path,
		Headers: headers,
		Body:    body,
	}
	response, err := f.handlerFor(t).Handle(context.Background(), request)
	if err != nil {
		t.Fatalf("%s %s: %v", method, path, err)
	}
	return response
}

func (f *failoverTestFacade) get(t *testing.T, path string) *core.HandlerResponse {
	t.Helper()
	return f.serve(t, "GET", path, nil, nil)
}

func (f *failoverTestFacade) post(t *testing.T, path string, body any) *core.HandlerResponse {
	t.Helper()
	return f.serve(t, "POST", path, body, map[string]string{"content-type": "application/json"})
}

// failoverTestLogger records every line, so a test can assert what was logged.
type failoverTestLogger struct {
	mu      sync.Mutex
	entries []failoverTestLogEntry
}

type failoverTestLogEntry struct {
	Level   string
	Message string
	Meta    map[string]any
	Err     error
}

func (l *failoverTestLogger) Debug(message string, meta map[string]any) {
	l.append("debug", message, meta, nil)
}

func (l *failoverTestLogger) Info(message string, meta map[string]any) {
	l.append("info", message, meta, nil)
}

func (l *failoverTestLogger) Error(message string, err error, meta map[string]any) {
	l.append("error", message, meta, err)
}

func (l *failoverTestLogger) append(level, message string, meta map[string]any, err error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.entries = append(l.entries, failoverTestLogEntry{Level: level, Message: message, Meta: meta, Err: err})
}

func (l *failoverTestLogger) all() []failoverTestLogEntry {
	l.mu.Lock()
	defer l.mu.Unlock()
	return append([]failoverTestLogEntry{}, l.entries...)
}

// failoverTestDigitRuns flattens every string a store, a logger or a callback
// received into its runs of 13 or more digits: a PAN must never appear.
func failoverTestDigitRuns(t *testing.T, value any) []string {
	t.Helper()
	data, err := json.Marshal(value)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	found := []string{}
	run := strings.Builder{}
	flush := func() {
		if run.Len() >= 13 {
			found = append(found, run.String())
		}
		run.Reset()
	}
	for _, character := range string(data) {
		if character >= '0' && character <= '9' {
			run.WriteRune(character)
			continue
		}
		flush()
	}
	flush()
	return found
}
