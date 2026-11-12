package testing

import (
	"context"
	"errors"
	"fmt"
	"math"
	"net/url"
	"sort"
	"sync"
	"time"

	"github.com/boracomet/go-pos/core"
)

// MockProviderConfig configures MockProvider.
type MockProviderConfig struct {
	core.ProviderConfig
	// Secret signs 3D Secure callbacks. Default: random per instance, so callbacks
	// from another instance are rejected like forged ones.
	Secret string
	// InstallmentRates are the commission rates (%) by installment count used by
	// InstallmentInfo. Default: 1→0, 2→2, 3→3, 6→5.
	InstallmentRates map[int]float64
}

// MockProvider is an in-memory payment provider for application tests: no network
// access, no provider sandbox. Register it under the id "mock" and drive outcomes
// with the magic card numbers or with FailNext and NetworkErrorNext.
//
// It is safe for concurrent use.
type MockProvider struct {
	core.BaseProvider
	config    MockProviderConfig
	secret    string
	secretErr error

	mu       sync.RWMutex
	records  map[string]*paymentRecord
	order    []string
	cards    map[string][]savedCard
	queued   []queuedOutcome
	sequence int
}

// NewMockProvider builds an in-memory provider. Without a configuration it signs
// callbacks with a random secret and uses the default installment rates.
func NewMockProvider(config ...MockProviderConfig) *MockProvider {
	cfg := MockProviderConfig{}
	if len(config) > 0 {
		cfg = config[0]
	}
	secret, secretErr := cfg.Secret, error(nil)
	if secret == "" {
		secret, secretErr = core.RandomHex(16)
	}
	p := &MockProvider{
		BaseProvider: core.NewBaseProvider(string(core.ProviderMock), cfg.ProviderConfig),
		config:       cfg,
		secret:       secret,
		secretErr:    secretErr,
		records:      make(map[string]*paymentRecord),
		cards:        make(map[string][]savedCard),
	}
	p.ErrorCodes = errorCodeTable()
	return p
}

// Compile-time check that the mock is a provider.
var _ core.Provider = (*MockProvider)(nil)

// FailNext makes the next operation fail with code, whatever the card. An empty
// code means CARD_DECLINED; message defaults to the code's decline message. It
// returns p so calls can be chained.
func (p *MockProvider) FailNext(_ context.Context, operation MockOperation, code core.PaymentErrorCode, message ...string) *MockProvider {
	if code == "" {
		code = core.CodeCardDeclined
	}
	text := ""
	if len(message) > 0 {
		text = message[0]
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	p.queued = append(p.queued, queuedOutcome{operation: operation, scenario: declineScenario(code, text)})
	return p
}

// NetworkErrorNext makes the next operation go through while its response is
// lost: it returns PENDING with NETWORK_ERROR and GetPayment shows the real
// outcome. It returns p so calls can be chained.
func (p *MockProvider) NetworkErrorNext(_ context.Context, operation MockOperation) *MockProvider {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.queued = append(p.queued, queuedOutcome{operation: operation, scenario: scenario{kind: scenarioNetworkError}})
	return p
}

// Reset clears payments, saved cards and queued outcomes.
func (p *MockProvider) Reset(_ context.Context) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.records = make(map[string]*paymentRecord)
	p.cards = make(map[string][]savedCard)
	p.order = nil
	p.queued = nil
	p.sequence = 0
}

// GetRecord returns the mock bank's record of a payment, or nil when it is
// unknown.
func (p *MockProvider) GetRecord(_ context.Context, paymentID string) *MockPaymentRecord {
	p.mu.RLock()
	defer p.mu.RUnlock()
	record := p.records[paymentID]
	if record == nil {
		return nil
	}
	out := p.publicRecord(record)
	return &out
}

// Payments returns every payment, oldest first.
func (p *MockProvider) Payments(_ context.Context) []MockPaymentRecord {
	p.mu.RLock()
	defer p.mu.RUnlock()
	out := make([]MockPaymentRecord, 0, len(p.order))
	for _, id := range p.order {
		if record := p.records[id]; record != nil {
			out = append(out, p.publicRecord(record))
		}
	}
	return out
}

// ThreeDSCallback returns the signed callback the mock bank posts to callbackUrl
// after the 3D Secure page. Post it to the handler
// ("/mock/payment/complete-3ds") or pass it to CompleteThreeDSPayment. Passing
// approve=false simulates a failed or abandoned authentication.
func (p *MockProvider) ThreeDSCallback(_ context.Context, paymentID string, approve ...bool) (map[string]string, error) {
	approved := true
	if len(approve) > 0 {
		approved = approve[0]
	}
	p.mu.RLock()
	defer p.mu.RUnlock()
	return p.callbackLocked(paymentID, approved)
}

// CreatePayment charges a card.
func (p *MockProvider) CreatePayment(ctx context.Context, request *core.PaymentRequest) (*core.PaymentResponse, error) {
	return p.pay(ctx, request, false)
}

// Authorize blocks the amount on the card without charging it.
func (p *MockProvider) Authorize(ctx context.Context, request *core.PaymentRequest) (*core.PaymentResponse, error) {
	return p.pay(ctx, request, true)
}

// InitThreeDSPayment starts a payment that requires 3D Secure.
func (p *MockProvider) InitThreeDSPayment(ctx context.Context, request *core.ThreeDSPaymentRequest) (*core.ThreeDSInitResponse, error) {
	return p.initThreeDS(ctx, request, false)
}

// InitThreeDSAuthorize starts a pre-authorization that requires 3D Secure.
func (p *MockProvider) InitThreeDSAuthorize(ctx context.Context, request *core.ThreeDSPaymentRequest) (*core.ThreeDSInitResponse, error) {
	return p.initThreeDS(ctx, request, true)
}

// CompleteThreeDSPayment finishes a 3D Secure payment with the bank's callback
// data. A callback whose signature does not verify fails with INVALID_HASH.
func (p *MockProvider) CompleteThreeDSPayment(_ context.Context, callbackData any) (*core.PaymentResponse, error) {
	paymentID := callbackField(callbackData, "paymentId")
	mdStatus := callbackField(callbackData, "mdStatus")
	signature := callbackField(callbackData, "signature")

	p.mu.Lock()
	defer p.mu.Unlock()

	expected, err := p.sign(paymentID, mdStatus)
	if err != nil {
		return nil, err
	}
	if paymentID == "" || !core.SafeEqual(expected, signature) {
		out := &core.PaymentResponse{PaymentID: paymentID}
		out.SetFailure(p.failure(string(core.CodeInvalidHash), invalidSignatureMessage, callbackData))
		return out, nil
	}
	record := p.records[paymentID]
	if record == nil {
		out := &core.PaymentResponse{PaymentID: paymentID}
		out.SetFailure(p.failure(string(core.CodeInvalidRequest), fmt.Sprintf("Unknown payment %s", paymentID), nil))
		return out, nil
	}
	if record.State != StatePending3DS {
		return p.paymentResult(record), nil
	}

	forced := p.take(OperationThreeDSComplete)
	if mdStatus != "1" {
		outcome := declineScenario(core.CodeThreeDSFailed, "")
		if forced != nil {
			outcome = *forced
		}
		return p.settle(record, outcome, ""), nil
	}
	outcome := record.scenario
	if forced != nil {
		outcome = *forced
	}
	return p.settle(record, outcome, ""), nil
}

// Capture charges all or part of a pre-authorized amount.
func (p *MockProvider) Capture(_ context.Context, request *core.CaptureRequest) (*core.PaymentResponse, error) {
	if request == nil {
		request = &core.CaptureRequest{}
	}
	p.mu.Lock()
	defer p.mu.Unlock()

	if err := p.ValidateCapture(request); err != nil {
		return p.paymentValidationFailure(err, request.PaymentID, "")
	}
	record := p.records[request.PaymentID]
	forced := p.take(OperationCapture)
	if record == nil || record.State != StateAuthorized {
		out := &core.PaymentResponse{PaymentID: request.PaymentID}
		out.SetFailure(p.failure(string(core.CodeInvalidRequest), "Only an authorized pre-authorization can be captured", nil))
		return out, nil
	}
	amount, err := toMinor(request.Amount)
	if err != nil {
		return p.paymentValidationFailure(err, request.PaymentID, "")
	}
	if amount > record.Amount {
		out := &core.PaymentResponse{PaymentID: record.PaymentID}
		out.SetFailure(p.failure(string(core.CodeInvalidRequest), "Capture amount exceeds the authorized amount", nil))
		return out, nil
	}
	if forced != nil && forced.kind == scenarioDecline {
		out := &core.PaymentResponse{PaymentID: record.PaymentID}
		out.SetFailure(p.decline(*forced, nil, ""))
		return out, nil
	}
	record.State = StateSucceeded
	record.CapturedAmount = amount
	if forced != nil && forced.kind == scenarioNetworkError {
		out := &core.PaymentResponse{PaymentID: record.PaymentID}
		out.SetFailure(p.lost())
		return out, nil
	}
	return p.paymentResult(record), nil
}

// VoidAuthorization releases a pre-authorization; nothing is charged.
func (p *MockProvider) VoidAuthorization(_ context.Context, request *core.VoidAuthorizationRequest) (*core.CancelResponse, error) {
	if request == nil {
		request = &core.VoidAuthorizationRequest{}
	}
	p.mu.Lock()
	defer p.mu.Unlock()

	record := p.records[request.PaymentID]
	forced := p.take(OperationVoid)
	if record == nil || record.State != StateAuthorized {
		out := &core.CancelResponse{ConversationID: request.ConversationID}
		out.SetFailure(p.failure(string(core.CodeInvalidRequest), "Only an authorized pre-authorization can be voided", nil))
		return out, nil
	}
	if forced != nil && forced.kind == scenarioDecline {
		out := &core.CancelResponse{}
		out.SetFailure(p.decline(*forced, nil, ""))
		return out, nil
	}
	record.State = StateVoided
	if forced != nil && forced.kind == scenarioNetworkError {
		out := &core.CancelResponse{}
		out.SetFailure(p.lost())
		return out, nil
	}
	return &core.CancelResponse{
		Status:      core.StatusSuccess,
		VoidID:      record.PaymentID,
		RawResponse: p.publicRecord(record),
	}, nil
}

// Refund refunds all or part of a successful payment.
func (p *MockProvider) Refund(_ context.Context, request *core.RefundRequest) (*core.RefundResponse, error) {
	if request == nil {
		request = &core.RefundRequest{}
	}
	p.mu.Lock()
	defer p.mu.Unlock()

	if err := p.ValidateRefund(request); err != nil {
		if f, ok := p.validationFailure(err); ok {
			out := &core.RefundResponse{ConversationID: request.ConversationID}
			out.SetFailure(f)
			return out, nil
		}
		return nil, err
	}
	record := p.records[request.PaymentID]
	forced := p.take(OperationRefund)
	if record == nil || record.State != StateSucceeded {
		out := &core.RefundResponse{ConversationID: request.ConversationID}
		out.SetFailure(p.failure(string(core.CodeInvalidRequest), "Only a successful payment can be refunded", nil))
		return out, nil
	}
	amount, err := toMinor(request.Price)
	if err != nil {
		if f, ok := p.validationFailure(err); ok {
			out := &core.RefundResponse{ConversationID: request.ConversationID}
			out.SetFailure(f)
			return out, nil
		}
		return nil, err
	}
	if record.RefundedAmount+amount > record.CapturedAmount {
		out := &core.RefundResponse{ConversationID: request.ConversationID}
		out.SetFailure(p.failure(string(core.CodeInvalidRequest), "Refund amount exceeds the refundable amount", nil))
		return out, nil
	}
	if forced != nil && forced.kind == scenarioDecline {
		out := &core.RefundResponse{ConversationID: request.ConversationID}
		out.SetFailure(p.decline(*forced, nil, ""))
		return out, nil
	}
	if forced == nil && record.scenario.kind == scenarioRefundFails {
		out := &core.RefundResponse{ConversationID: request.ConversationID}
		out.SetFailure(p.decline(declineScenario(core.CodeProviderError, "Refund rejected by the mock bank"), nil, ""))
		return out, nil
	}
	refundID := p.nextID("MOCKREF")
	record.RefundedAmount += amount
	record.Refunds = append(record.Refunds, MockRefund{RefundID: refundID, Amount: amount})
	if forced != nil && forced.kind == scenarioNetworkError {
		out := &core.RefundResponse{ConversationID: request.ConversationID}
		out.SetFailure(p.lost())
		return out, nil
	}
	return &core.RefundResponse{
		Status:         core.StatusSuccess,
		RefundID:       refundID,
		ConversationID: request.ConversationID,
		RawResponse:    p.publicRecord(record),
	}, nil
}

// Cancel cancels a successful payment that has no refunds.
func (p *MockProvider) Cancel(_ context.Context, request *core.CancelRequest) (*core.CancelResponse, error) {
	if request == nil {
		request = &core.CancelRequest{}
	}
	p.mu.Lock()
	defer p.mu.Unlock()

	record := p.records[request.PaymentID]
	forced := p.take(OperationCancel)
	if record == nil || record.State != StateSucceeded || record.RefundedAmount > 0 {
		out := &core.CancelResponse{ConversationID: request.ConversationID}
		out.SetFailure(p.failure(string(core.CodeInvalidRequest), "Only a successful payment without refunds can be cancelled", nil))
		return out, nil
	}
	if forced != nil && forced.kind == scenarioDecline {
		out := &core.CancelResponse{ConversationID: request.ConversationID}
		out.SetFailure(p.decline(*forced, nil, ""))
		return out, nil
	}
	if forced == nil && record.scenario.kind == scenarioRefundFails {
		out := &core.CancelResponse{ConversationID: request.ConversationID}
		out.SetFailure(p.decline(declineScenario(core.CodeProviderError, "Cancel rejected by the mock bank"), nil, ""))
		return out, nil
	}
	record.State = StateCancelled
	if forced != nil && forced.kind == scenarioNetworkError {
		out := &core.CancelResponse{ConversationID: request.ConversationID}
		out.SetFailure(p.lost())
		return out, nil
	}
	return &core.CancelResponse{
		Status:         core.StatusSuccess,
		TransactionID:  record.PaymentID,
		ConversationID: request.ConversationID,
		RawResponse:    p.publicRecord(record),
	}, nil
}

// GetPayment returns the mock bank's current view of a payment.
func (p *MockProvider) GetPayment(_ context.Context, paymentID string) (*core.PaymentResponse, error) {
	p.mu.RLock()
	defer p.mu.RUnlock()
	record := p.records[paymentID]
	if record == nil {
		out := &core.PaymentResponse{PaymentID: paymentID}
		out.SetFailure(p.failure(string(core.CodeInvalidRequest), fmt.Sprintf("Unknown payment %s", paymentID), nil))
		return out, nil
	}
	return p.paymentResult(record), nil
}

// SaveCard stores a card without charging it. A stored card keeps the behaviour
// of its magic number.
func (p *MockProvider) SaveCard(_ context.Context, request *core.SaveCardRequest) (*core.SaveCardResponse, error) {
	if request == nil {
		request = &core.SaveCardRequest{}
	}
	p.mu.Lock()
	defer p.mu.Unlock()

	if request.Card.CardNumber == "" {
		out := &core.SaveCardResponse{}
		out.SetFailure(p.failure("VALIDATION_ERROR", "card.cardNumber is required", nil))
		return out, nil
	}
	customerToken := request.CustomerToken
	if customerToken == "" {
		customerToken = p.nextID("MOCKCUS")
	}
	saved := p.store(customerToken, request.Card.CardNumber, request.Card.ExpireMonth, request.Card.ExpireYear, request.Alias)
	return &core.SaveCardResponse{
		Status:        core.StatusSuccess,
		CustomerToken: customerToken,
		Card:          &saved.StoredCard,
	}, nil
}

// ListCards returns a customer's saved cards, oldest first.
func (p *MockProvider) ListCards(_ context.Context, request *core.ListCardsRequest) (*core.ListCardsResponse, error) {
	customerToken := ""
	if request != nil {
		customerToken = request.CustomerToken
	}
	p.mu.RLock()
	defer p.mu.RUnlock()
	stored := p.cards[customerToken]
	cards := make([]core.StoredCard, 0, len(stored))
	for _, card := range stored {
		cards = append(cards, card.StoredCard)
	}
	return &core.ListCardsResponse{
		Status:        core.StatusSuccess,
		CustomerToken: customerToken,
		Cards:         cards,
	}, nil
}

// DeleteCard removes a saved card.
func (p *MockProvider) DeleteCard(_ context.Context, request *core.DeleteCardRequest) (*core.DeleteCardResponse, error) {
	if request == nil {
		request = &core.DeleteCardRequest{}
	}
	p.mu.Lock()
	defer p.mu.Unlock()

	stored := p.cards[request.CustomerToken]
	remaining := make([]savedCard, 0, len(stored))
	for _, card := range stored {
		if card.CardToken != request.CardToken {
			remaining = append(remaining, card)
		}
	}
	if len(remaining) == len(stored) {
		out := &core.DeleteCardResponse{}
		out.SetFailure(p.failure(string(core.CodeInvalidRequest), "Unknown card", nil))
		return out, nil
	}
	p.cards[request.CustomerToken] = remaining
	return &core.DeleteCardResponse{Status: core.StatusSuccess}, nil
}

// BinCheck describes the issuer of a BIN.
func (p *MockProvider) BinCheck(_ context.Context, binNumber string) (*core.BinCheckResponse, error) {
	return &core.BinCheckResponse{
		BinNumber:       firstN(binNumber, 6),
		CardType:        "CREDIT_CARD",
		CardAssociation: associationOf(binNumber),
		CardFamily:      "Mock",
		BankName:        "Mock Bank",
		BankCode:        999,
		Commercial:      false,
	}, nil
}

// InstallmentInfo returns the installment options of a BIN for an amount.
func (p *MockProvider) InstallmentInfo(_ context.Context, request *core.InstallmentInfoRequest) (*core.InstallmentInfoResponse, error) {
	if request == nil {
		request = &core.InstallmentInfoRequest{}
	}
	p.mu.RLock()
	rates := p.config.InstallmentRates
	p.mu.RUnlock()
	if rates == nil {
		rates = map[int]float64{1: 0, 2: 2, 3: 3, 6: 5}
	}

	price, err := toMinor(request.Price)
	if err != nil {
		if f, ok := p.validationFailure(err); ok {
			out := &core.InstallmentInfoResponse{ConversationID: request.ConversationID}
			out.SetFailure(f)
			return out, nil
		}
		return nil, err
	}

	prices := make([]core.InstallmentDetail, 0, len(rates))
	for count, rate := range rates {
		total := int64(math.Round(float64(price) * (100 + rate) / 100))
		perInstallment := int64(0)
		if count != 0 {
			perInstallment = int64(math.Round(float64(total) / float64(count)))
		}
		commission := rate
		prices = append(prices, core.InstallmentDetail{
			InstallmentNumber: count,
			TotalPrice:        float64(total) / 100,
			InstallmentPrice:  float64(perInstallment) / 100,
			CommissionRate:    &commission,
		})
	}
	sort.Slice(prices, func(i, j int) bool { return prices[i].InstallmentNumber < prices[j].InstallmentNumber })

	return &core.InstallmentInfoResponse{
		Status:         core.StatusSuccess,
		ConversationID: request.ConversationID,
		InstallmentDetails: []core.InstallmentPrice{{
			BinNumber:         firstN(request.BinNumber, 6),
			Price:             float64(price) / 100,
			CardType:          "CREDIT_CARD",
			CardAssociation:   associationOf(request.BinNumber),
			CardFamilyName:    "Mock",
			BankCode:          999,
			BankName:          "Mock Bank",
			Commercial:        0,
			InstallmentPrices: prices,
		}},
	}, nil
}

// ---- Internals ----------------------------------------------------------

func (p *MockProvider) pay(_ context.Context, request *core.PaymentRequest, preAuth bool) (*core.PaymentResponse, error) {
	p.mu.Lock()
	defer p.mu.Unlock()

	record, err := p.openValidated(request, preAuth, false)
	if err != nil {
		return p.paymentValidationFailure(err, "", conversationIDOf(request))
	}
	outcome := record.scenario
	if forced := p.take(OperationPayment); forced != nil {
		outcome = *forced
	}
	if outcome.kind == scenarioThreeDSRequired {
		return p.settle(record, declineScenario(core.CodeCardDeclined, "3D Secure authentication is required for this card"), "THREEDS_REQUIRED"), nil
	}
	if outcome.kind == scenarioThreeDSFailed {
		outcome = successScenario()
	}
	return p.settle(record, outcome, ""), nil
}

func (p *MockProvider) initThreeDS(_ context.Context, request *core.ThreeDSPaymentRequest, preAuth bool) (*core.ThreeDSInitResponse, error) {
	var payment *core.PaymentRequest
	callbackURL := ""
	if request != nil {
		payment = &request.PaymentRequest
		callbackURL = request.CallbackURL
	}

	p.mu.Lock()
	defer p.mu.Unlock()

	record, err := p.openValidated(payment, preAuth, true)
	if err != nil {
		if f, ok := p.validationFailure(err); ok {
			out := &core.ThreeDSInitResponse{ConversationID: conversationIDOf(payment)}
			out.SetFailure(f)
			return out, nil
		}
		return nil, err
	}
	record.State = StatePending3DS

	forced := p.take(OperationThreeDSInit)
	if forced != nil && forced.kind == scenarioDecline {
		record.State = StateFailed
		record.ErrorCode = forced.code
		out := &core.ThreeDSInitResponse{PaymentID: record.PaymentID, ConversationID: record.ConversationID}
		out.SetFailure(p.decline(*forced, nil, ""))
		return out, nil
	}
	if forced != nil && forced.kind == scenarioNetworkError {
		out := &core.ThreeDSInitResponse{PaymentID: record.PaymentID, ConversationID: record.ConversationID}
		out.SetFailure(p.lost())
		return out, nil
	}

	fields, err := p.callbackLocked(record.PaymentID, true)
	if err != nil {
		return nil, err
	}
	return &core.ThreeDSInitResponse{
		Status:             core.StatusPending,
		PaymentID:          record.PaymentID,
		ConversationID:     record.ConversationID,
		ThreeDSHTMLContent: autoSubmitForm(callbackURL, formFieldsOf(fields)),
		RawResponse:        p.publicRecord(record),
	}, nil
}

// openValidated validates the request and opens the payment record. A 3D Secure
// payment also needs a callback URL.
func (p *MockProvider) openValidated(request *core.PaymentRequest, preAuth, threeDS bool) (*paymentRecord, error) {
	if err := p.ValidatePayment(request, mockRules); err != nil {
		return nil, err
	}
	if threeDS && (request == nil || request.CallbackURL == "") {
		return nil, core.NewValidationError("callbackUrl is required", p.ProviderName, nil)
	}
	return p.open(request, preAuth)
}

// open creates the record of a new payment. The caller holds the write lock.
func (p *MockProvider) open(request *core.PaymentRequest, preAuth bool) (*paymentRecord, error) {
	var cardNumber string
	var outcome scenario
	stored := request.StoredCard != nil
	if stored {
		saved := p.findCard(request.StoredCard.CustomerToken, request.StoredCard.CardToken)
		if saved == nil {
			return nil, core.NewValidationError("Unknown stored card", p.ProviderName, nil)
		}
		outcome = saved.scenario
		cardNumber = saved.BinNumber + "000000" + saved.LastFourDigits
	} else {
		card, err := p.CardOf(request)
		if err != nil {
			return nil, err
		}
		cardNumber = stripSpaces(card.CardNumber)
		outcome = cardScenarioOf(cardNumber)
	}

	amountText := request.Price
	if request.PaidPrice != "" {
		amountText = request.PaidPrice
	}
	amount, err := toMinor(amountText)
	if err != nil {
		return nil, err
	}
	currency := request.Currency
	if currency == "" {
		currency = string(core.CurrencyTRY)
	}
	installment := request.Installment
	if installment == 0 {
		installment = 1
	}
	captured := amount
	if preAuth {
		captured = 0
	}

	record := &paymentRecord{
		MockPaymentRecord: MockPaymentRecord{
			PaymentID:      p.nextID("MOCKPAY"),
			ConversationID: request.ConversationID,
			State:          StateFailed,
			Amount:         amount,
			CapturedAmount: captured,
			Currency:       currency,
			Installment:    installment,
			CardLastFour:   lastN(cardNumber, 4),
			PreAuth:        preAuth,
			Refunds:        []MockRefund{},
			CreatedAt:      time.Now().UTC().Format("2006-01-02T15:04:05.000Z"),
		},
		scenario: outcome,
		saveCard: request.SaveCard,
	}
	if !stored {
		record.cardNumber = cardNumber
	}
	if request.PaymentCard != nil {
		record.cardHolderName = request.PaymentCard.CardHolderName
		record.expireMonth = request.PaymentCard.ExpireMonth
		record.expireYear = request.PaymentCard.ExpireYear
	}
	p.records[record.PaymentID] = record
	p.order = append(p.order, record.PaymentID)
	return record, nil
}

// settle applies an outcome to a payment and returns its result. The caller holds
// the write lock.
func (p *MockProvider) settle(record *paymentRecord, outcome scenario, rawCode string) *core.PaymentResponse {
	if outcome.kind == scenarioDecline {
		record.State = StateFailed
		record.ErrorCode = outcome.code
		out := &core.PaymentResponse{PaymentID: record.PaymentID, ConversationID: record.ConversationID}
		out.SetFailure(p.decline(outcome, p.publicRecord(record), rawCode))
		return out
	}
	if record.PreAuth {
		record.State = StateAuthorized
	} else {
		record.State = StateSucceeded
	}
	storedCard := p.saveFromPayment(record)
	if outcome.kind == scenarioNetworkError {
		out := &core.PaymentResponse{PaymentID: record.PaymentID, ConversationID: record.ConversationID}
		out.SetFailure(p.lost())
		return out
	}
	out := p.paymentResult(record)
	out.StoredCard = storedCard
	return out
}

// saveFromPayment stores the card of a payment that asked for it. A non-nil
// SaveCard saves: core.SaveCardOption.Enabled is not part of the JSON contract,
// so an option carrying only an alias still saves the card.
func (p *MockProvider) saveFromPayment(record *paymentRecord) *core.SavedCardTokens {
	if record.saveCard == nil || record.cardNumber == "" {
		return nil
	}
	customerToken := record.saveCard.CustomerToken
	if customerToken == "" {
		customerToken = p.nextID("MOCKCUS")
	}
	saved := p.store(customerToken, record.cardNumber, record.expireMonth, record.expireYear, record.saveCard.Alias)
	return &core.SavedCardTokens{CustomerToken: customerToken, CardToken: saved.CardToken}
}

// store saves a card under a customer token. The caller holds the write lock.
func (p *MockProvider) store(customerToken, cardNumber, expireMonth, expireYear, alias string) savedCard {
	digits := stripSpaces(cardNumber)
	saved := savedCard{
		StoredCard: core.StoredCard{
			CardToken:       p.nextID("MOCKCARD"),
			Alias:           alias,
			BinNumber:       firstN(digits, 6),
			LastFourDigits:  lastN(digits, 4),
			ExpireMonth:     expireMonth,
			ExpireYear:      expireYear,
			CardType:        "CREDIT_CARD",
			CardAssociation: associationOf(digits),
			BankName:        "Mock Bank",
		},
		scenario: cardScenarioOf(digits),
	}
	p.cards[customerToken] = append(p.cards[customerToken], saved)
	return saved
}

func (p *MockProvider) findCard(customerToken, cardToken string) *savedCard {
	for i := range p.cards[customerToken] {
		if p.cards[customerToken][i].CardToken == cardToken {
			return &p.cards[customerToken][i]
		}
	}
	return nil
}

func (p *MockProvider) paymentResult(record *paymentRecord) *core.PaymentResponse {
	var status core.PaymentStatus
	switch record.State {
	case StateSucceeded, StateAuthorized:
		status = core.StatusSuccess
	case StatePending3DS:
		status = core.StatusPending
	case StateCancelled, StateVoided:
		status = core.StatusCancelled
	default:
		status = core.StatusFailure
	}
	out := &core.PaymentResponse{
		Status:         status,
		PaymentID:      record.PaymentID,
		ConversationID: record.ConversationID,
		RawResponse:    p.publicRecord(record),
	}
	if status == core.StatusFailure {
		code := record.ErrorCode
		if code == "" {
			code = core.CodeUnknown
		}
		out.ErrorCode = string(code)
		out.Code = p.ResolveErrorCode(string(code))
		out.ErrorMessage = declineMessages[code]
	}
	return out
}

// failure builds a FAILURE result. errorCode is the raw code, Code the
// normalized one; raw is carried as RawResponse.
func (p *MockProvider) failure(errorCode, errorMessage string, raw any) core.Failure {
	return core.Failure{
		Status:       core.StatusFailure,
		ErrorCode:    errorCode,
		Code:         p.ResolveErrorCode(errorCode),
		ErrorMessage: errorMessage,
		RawResponse:  raw,
	}
}

func (p *MockProvider) decline(outcome scenario, raw any, rawCode string) core.Failure {
	errorCode := rawCode
	if errorCode == "" {
		errorCode = string(outcome.code)
	}
	message := outcome.message
	if message == "" {
		message = declineMessages[outcome.code]
	}
	if message == "" {
		message = declinedFallbackMessage
	}
	return p.failure(errorCode, message, raw)
}

// lost is the result of an operation that went through whose response was lost.
func (p *MockProvider) lost() core.Failure {
	return core.Failure{
		Status:       core.StatusPending,
		ErrorCode:    core.NetworkErrorCode,
		Code:         p.ResolveErrorCode(core.NetworkErrorCode),
		ErrorMessage: lostResponseMessage,
	}
}

// validationFailure turns invalid input into a VALIDATION_ERROR result, as the
// other providers do. It reports false when err is not a ValidationError.
func (p *MockProvider) validationFailure(err error) (core.Failure, bool) {
	var validationErr *core.ValidationError
	if !errors.As(err, &validationErr) {
		return core.Failure{}, false
	}
	return p.failure("VALIDATION_ERROR", validationErr.Message, nil), true
}

func (p *MockProvider) paymentValidationFailure(err error, paymentID, conversationID string) (*core.PaymentResponse, error) {
	if f, ok := p.validationFailure(err); ok {
		out := &core.PaymentResponse{PaymentID: paymentID, ConversationID: conversationID}
		out.SetFailure(f)
		return out, nil
	}
	return nil, err
}

// take removes the queued outcome of an operation, if any. The caller holds the
// write lock.
func (p *MockProvider) take(operation MockOperation) *scenario {
	for i, entry := range p.queued {
		if entry.operation == operation {
			outcome := entry.scenario
			p.queued = append(p.queued[:i], p.queued[i+1:]...)
			return &outcome
		}
	}
	return nil
}

func (p *MockProvider) sign(paymentID, mdStatus string) (string, error) {
	if p.secretErr != nil {
		return "", p.secretErr
	}
	mac, err := core.HMAC(core.SHA256, []byte(p.secret), []byte(paymentID+":"+mdStatus))
	if err != nil {
		return "", err
	}
	return core.ToHex(mac), nil
}

// callbackLocked builds the signed callback of a payment. The caller holds the
// lock.
func (p *MockProvider) callbackLocked(paymentID string, approve bool) (map[string]string, error) {
	record := p.records[paymentID]
	conversationID := ""
	if record != nil {
		conversationID = record.ConversationID
		approve = approve && record.scenario.kind != scenarioThreeDSFailed
	}
	mdStatus := "0"
	if approve {
		mdStatus = "1"
	}
	signature, err := p.sign(paymentID, mdStatus)
	if err != nil {
		return nil, err
	}
	return map[string]string{
		"paymentId":      paymentID,
		"conversationId": conversationID,
		"mdStatus":       mdStatus,
		"signature":      signature,
	}, nil
}

func (p *MockProvider) nextID(prefix string) string {
	p.sequence++
	return fmt.Sprintf("%s%06d", prefix, p.sequence)
}

func (p *MockProvider) publicRecord(record *paymentRecord) MockPaymentRecord {
	out := record.MockPaymentRecord
	out.Refunds = make([]MockRefund, len(record.Refunds))
	copy(out.Refunds, record.Refunds)
	return out
}

// callbackFieldOrder keeps the generated 3D Secure form deterministic.
var callbackFieldOrder = []string{"paymentId", "conversationId", "mdStatus", "signature"}

func formFieldsOf(fields map[string]string) []formField {
	out := make([]formField, 0, len(fields))
	for _, name := range callbackFieldOrder {
		out = append(out, formField{name: name, value: fields[name]})
	}
	return out
}

func cardScenarioOf(cardNumber string) scenario {
	if outcome, ok := cardScenarios[cardNumber]; ok {
		return outcome
	}
	return successScenario()
}

// callbackField reads a string field of callback data: a parsed JSON body, a
// map of form values or url.Values.
func callbackField(data any, key string) string {
	switch body := data.(type) {
	case map[string]any:
		if value, ok := body[key].(string); ok {
			return value
		}
	case map[string]string:
		return body[key]
	case url.Values:
		return body.Get(key)
	}
	return ""
}

func conversationIDOf(request *core.PaymentRequest) string {
	if request == nil {
		return ""
	}
	return request.ConversationID
}

func firstN(value string, n int) string {
	if len(value) <= n {
		return value
	}
	return value[:n]
}

func lastN(value string, n int) string {
	if len(value) <= n {
		return value
	}
	return value[len(value)-n:]
}
