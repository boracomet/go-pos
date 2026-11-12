package testing

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"regexp"
	"strings"
	"testing"

	"github.com/boracomet/go-pos/core"
)

// order is the fixture order at 100.00 with a single matching basket item.
func order() core.PaymentRequest {
	request := FixturePaymentRequest()
	request.Price = "100.00"
	request.PaidPrice = "100.00"
	items := FixtureBasketItems()
	items[0].Price = "100.00"
	request.BasketItems = items[:1]
	return request
}

func withCard(cardNumber string) *core.PaymentRequest {
	request := order()
	card := *request.PaymentCard
	card.CardNumber = cardNumber
	register := false
	card.RegisterCard = &register
	request.PaymentCard = &card
	return &request
}

func threeDSRequest(request *core.PaymentRequest, callbackURL string) *core.ThreeDSPaymentRequest {
	out := &core.ThreeDSPaymentRequest{PaymentRequest: *request}
	out.CallbackURL = callbackURL
	return out
}

func refundRequest(paymentID, price string) *core.RefundRequest {
	return &core.RefundRequest{PaymentID: paymentID, Price: price, Currency: "TRY", IP: "1.1.1.1"}
}

// forbiddenDoer fails the test if the mock ever reaches for the network.
type forbiddenDoer struct{ t *testing.T }

func (d forbiddenDoer) Do(*http.Request) (*http.Response, error) {
	d.t.Error("the mock provider must not touch the network")
	return nil, errors.New("network access in a mock test")
}

// formFieldRe reads the hidden inputs of the generated 3D Secure page.
var formFieldRe = regexp.MustCompile(`name="([^"]+)" value="([^"]*)"`)

func formFieldsOfHTML(html string) map[string]string {
	fields := map[string]string{}
	for _, match := range formFieldRe.FindAllStringSubmatch(html, -1) {
		fields[match[1]] = match[2]
	}
	return fields
}

func TestMagicCardsAreValidCardNumbers(t *testing.T) {
	cards := MagicCards()
	if len(cards) != 13 {
		t.Fatalf("MagicCards has %d entries, want 13", len(cards))
	}
	for name, number := range cards {
		if !core.Luhn(number) {
			t.Errorf("%s (%s) fails the Luhn check", name, number)
		}
	}
}

func TestMockProviderNeverUsesTheNetwork(t *testing.T) {
	ctx := context.Background()
	mock := NewMockProvider(MockProviderConfig{ProviderConfig: core.ProviderConfig{Doer: forbiddenDoer{t}}})

	paid, err := mock.CreatePayment(ctx, withCard(CardSuccess))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := mock.Refund(ctx, refundRequest(paid.PaymentID, "10.00")); err != nil {
		t.Fatal(err)
	}
	init, err := mock.InitThreeDSPayment(ctx, threeDSRequest(withCard(CardSuccess), "https://shop.test/cb"))
	if err != nil {
		t.Fatal(err)
	}
	fields, err := mock.ThreeDSCallback(ctx, init.PaymentID)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := mock.CompleteThreeDSPayment(ctx, fields); err != nil {
		t.Fatal(err)
	}
	if _, err := mock.BinCheck(ctx, "424242"); err != nil {
		t.Fatal(err)
	}
	if _, err := mock.InstallmentInfo(ctx, &core.InstallmentInfoRequest{BinNumber: "424242", Price: "100.00"}); err != nil {
		t.Fatal(err)
	}
}

func TestCreatePaymentKeepsThePaymentInMemory(t *testing.T) {
	ctx := context.Background()
	mock := NewMockProvider()

	result, err := mock.CreatePayment(ctx, withCard(CardSuccess))
	if err != nil {
		t.Fatal(err)
	}
	if result.Status != core.StatusSuccess {
		t.Fatalf("status = %q, want success", result.Status)
	}
	if result.PaymentID != "MOCKPAY000001" {
		t.Errorf("paymentId = %q, want MOCKPAY000001", result.PaymentID)
	}

	fetched, err := mock.GetPayment(ctx, result.PaymentID)
	if err != nil {
		t.Fatal(err)
	}
	if fetched.Status != core.StatusSuccess {
		t.Errorf("getPayment status = %q, want success", fetched.Status)
	}

	record := mock.GetRecord(ctx, result.PaymentID)
	if record == nil {
		t.Fatal("getRecord returned nil")
	}
	if record.State != StateSucceeded || record.Amount != 10000 || record.CapturedAmount != 10000 || record.CardLastFour != "4242" {
		t.Errorf("record = %+v, want succeeded/10000/10000/4242", record)
	}
	if record.ConversationID != "123456789" {
		t.Errorf("conversationId = %q, want 123456789", record.ConversationID)
	}
	if record.CreatedAt == "" || !strings.HasSuffix(record.CreatedAt, "Z") {
		t.Errorf("createdAt = %q, want an ISO UTC timestamp", record.CreatedAt)
	}
}

func TestMagicCardsDeclineWithTheirErrorCodes(t *testing.T) {
	ctx := context.Background()
	mock := NewMockProvider()
	declines := []struct {
		card string
		code core.PaymentErrorCode
	}{
		{CardDeclined, core.CodeCardDeclined},
		{CardInsufficientFunds, core.CodeInsufficientFunds},
		{CardExpired, core.CodeExpiredCard},
		{CardInvalidCVC, core.CodeInvalidCVC},
		{CardFraudSuspected, core.CodeFraudSuspected},
		{CardLimitExceeded, core.CodeLimitExceeded},
		{CardProviderError, core.CodeProviderError},
	}
	for _, decline := range declines {
		result, err := mock.CreatePayment(ctx, withCard(decline.card))
		if err != nil {
			t.Fatal(err)
		}
		if result.Status != core.StatusFailure {
			t.Errorf("%s: status = %q, want failure", decline.card, result.Status)
		}
		if result.Code != decline.code {
			t.Errorf("%s: code = %q, want %q", decline.card, result.Code, decline.code)
		}
		if result.ErrorMessage == "" {
			t.Errorf("%s: errorMessage is empty", decline.card)
		}
		fetched, err := mock.GetPayment(ctx, result.PaymentID)
		if err != nil {
			t.Fatal(err)
		}
		if fetched.Code != decline.code {
			t.Errorf("%s: getPayment code = %q, want %q", decline.card, fetched.Code, decline.code)
		}
	}
}

func TestCreatePaymentValidatesLikeARealProvider(t *testing.T) {
	mock := NewMockProvider()
	result, err := mock.CreatePayment(context.Background(), withCard("4242424242424241"))
	if err != nil {
		t.Fatal(err)
	}
	if result.Status != core.StatusFailure || result.Code != core.CodeInvalidRequest {
		t.Fatalf("result = %+v, want a failure with INVALID_REQUEST", result)
	}
	if result.ErrorCode != "VALIDATION_ERROR" {
		t.Errorf("errorCode = %q, want VALIDATION_ERROR", result.ErrorCode)
	}
}

func TestThreeDSRequiredCard(t *testing.T) {
	ctx := context.Background()
	mock := NewMockProvider()

	direct, err := mock.CreatePayment(ctx, withCard(CardThreeDSRequired))
	if err != nil {
		t.Fatal(err)
	}
	if direct.Status != core.StatusFailure || direct.Code != core.CodeCardDeclined || direct.ErrorCode != "THREEDS_REQUIRED" {
		t.Fatalf("direct = %+v, want failure/CARD_DECLINED/THREEDS_REQUIRED", direct)
	}

	init, err := mock.InitThreeDSPayment(ctx, threeDSRequest(withCard(CardThreeDSRequired), "https://shop.test/cb"))
	if err != nil {
		t.Fatal(err)
	}
	if init.Status != core.StatusPending {
		t.Fatalf("init status = %q, want pending", init.Status)
	}
	fields, err := mock.ThreeDSCallback(ctx, init.PaymentID)
	if err != nil {
		t.Fatal(err)
	}
	result, err := mock.CompleteThreeDSPayment(ctx, fields)
	if err != nil {
		t.Fatal(err)
	}
	if result.Status != core.StatusSuccess {
		t.Errorf("result status = %q, want success", result.Status)
	}
}

func TestLostResponseIsPendingWhileThePaymentWentThrough(t *testing.T) {
	ctx := context.Background()
	mock := NewMockProvider()

	result, err := mock.CreatePayment(ctx, withCard(CardNetworkError))
	if err != nil {
		t.Fatal(err)
	}
	if result.Status != core.StatusPending || result.ErrorCode != core.NetworkErrorCode || result.Code != core.CodeNetworkError {
		t.Fatalf("result = %+v, want pending/NETWORK_ERROR", result)
	}
	if result.ErrorMessage != lostResponseMessage {
		t.Errorf("errorMessage = %q, want the lost-response message", result.ErrorMessage)
	}
	fetched, err := mock.GetPayment(ctx, result.PaymentID)
	if err != nil {
		t.Fatal(err)
	}
	if fetched.Status != core.StatusSuccess {
		t.Errorf("getPayment status = %q, want success", fetched.Status)
	}
}

func TestRefundFailsCardRejectsRefundsAndCancels(t *testing.T) {
	ctx := context.Background()
	mock := NewMockProvider()

	paid, err := mock.CreatePayment(ctx, withCard(CardRefundFails))
	if err != nil {
		t.Fatal(err)
	}
	if paid.Status != core.StatusSuccess {
		t.Fatalf("status = %q, want success", paid.Status)
	}
	refund, err := mock.Refund(ctx, refundRequest(paid.PaymentID, "10.00"))
	if err != nil {
		t.Fatal(err)
	}
	if refund.Status != core.StatusFailure || refund.Code != core.CodeProviderError {
		t.Errorf("refund = %+v, want failure/PROVIDER_ERROR", refund)
	}
	cancel, err := mock.Cancel(ctx, &core.CancelRequest{PaymentID: paid.PaymentID, IP: "1.1.1.1"})
	if err != nil {
		t.Fatal(err)
	}
	if cancel.Status != core.StatusFailure || cancel.Code != core.CodeProviderError {
		t.Errorf("cancel = %+v, want failure/PROVIDER_ERROR", cancel)
	}
}

func TestRefundsArePartialAndCancelsNeedAnUntouchedPayment(t *testing.T) {
	ctx := context.Background()
	mock := NewMockProvider()
	paid, err := mock.CreatePayment(ctx, withCard(CardSuccess))
	if err != nil {
		t.Fatal(err)
	}

	if got, err := mock.Refund(ctx, refundRequest(paid.PaymentID, "40.00")); err != nil {
		t.Fatal(err)
	} else if got.Status != core.StatusSuccess {
		t.Fatalf("first refund status = %q, want success", got.Status)
	}
	if got, err := mock.Refund(ctx, refundRequest(paid.PaymentID, "60.01")); err != nil {
		t.Fatal(err)
	} else if got.Code != core.CodeInvalidRequest {
		t.Errorf("oversized refund code = %q, want INVALID_REQUEST", got.Code)
	}
	if got, err := mock.Refund(ctx, refundRequest(paid.PaymentID, "60.00")); err != nil {
		t.Fatal(err)
	} else if got.Status != core.StatusSuccess {
		t.Fatalf("second refund status = %q, want success", got.Status)
	}

	record := mock.GetRecord(ctx, paid.PaymentID)
	if record == nil || record.RefundedAmount != 10000 {
		t.Fatalf("record = %+v, want refundedAmount 10000", record)
	}
	if len(record.Refunds) != 2 || record.Refunds[0].Amount != 4000 || record.Refunds[1].Amount != 6000 {
		t.Errorf("refunds = %+v, want [4000 6000]", record.Refunds)
	}
	if got, err := mock.Cancel(ctx, &core.CancelRequest{PaymentID: paid.PaymentID, IP: "1.1.1.1"}); err != nil {
		t.Fatal(err)
	} else if got.Status != core.StatusFailure {
		t.Errorf("cancel of a refunded payment = %q, want failure", got.Status)
	}

	other, err := mock.CreatePayment(ctx, withCard(CardSuccess))
	if err != nil {
		t.Fatal(err)
	}
	if got, err := mock.Cancel(ctx, &core.CancelRequest{PaymentID: other.PaymentID, IP: "1.1.1.1"}); err != nil {
		t.Fatal(err)
	} else if got.Status != core.StatusSuccess {
		t.Fatalf("cancel status = %q, want success", got.Status)
	}
	if got, err := mock.GetPayment(ctx, other.PaymentID); err != nil {
		t.Fatal(err)
	} else if got.Status != core.StatusCancelled {
		t.Errorf("getPayment status = %q, want cancelled", got.Status)
	}
}

func TestForcedOutcomesApplyToTheNextOperationOnly(t *testing.T) {
	ctx := context.Background()
	mock := NewMockProvider()
	mock.FailNext(ctx, OperationPayment, core.CodeLimitExceeded).NetworkErrorNext(ctx, OperationRefund)

	first, err := mock.CreatePayment(ctx, withCard(CardSuccess))
	if err != nil {
		t.Fatal(err)
	}
	if first.Code != core.CodeLimitExceeded {
		t.Errorf("first code = %q, want LIMIT_EXCEEDED", first.Code)
	}
	second, err := mock.CreatePayment(ctx, withCard(CardSuccess))
	if err != nil {
		t.Fatal(err)
	}
	if second.Status != core.StatusSuccess {
		t.Fatalf("second status = %q, want success", second.Status)
	}

	refund, err := mock.Refund(ctx, refundRequest(second.PaymentID, "5.00"))
	if err != nil {
		t.Fatal(err)
	}
	if refund.Status != core.StatusPending || refund.ErrorCode != core.NetworkErrorCode {
		t.Errorf("refund = %+v, want pending/NETWORK_ERROR", refund)
	}
	if record := mock.GetRecord(ctx, second.PaymentID); record == nil || record.RefundedAmount != 500 {
		t.Errorf("record = %+v, want refundedAmount 500", record)
	}

	mock.FailNext(ctx, OperationThreeDSInit, "")
	init, err := mock.InitThreeDSPayment(ctx, threeDSRequest(withCard(CardSuccess), "https://x.test"))
	if err != nil {
		t.Fatal(err)
	}
	if init.Status != core.StatusFailure || init.Code != core.CodeCardDeclined {
		t.Errorf("init = %+v, want failure/CARD_DECLINED", init)
	}

	mock.Reset(ctx)
	if payments := mock.Payments(ctx); len(payments) != 0 {
		t.Errorf("payments after reset = %d, want 0", len(payments))
	}
	if mock.GetRecord(ctx, second.PaymentID) != nil {
		t.Error("reset kept a payment record")
	}
}

func TestPreAuthorizationCaptureAndVoid(t *testing.T) {
	ctx := context.Background()
	mock := NewMockProvider()

	auth, err := mock.Authorize(ctx, withCard(CardSuccess))
	if err != nil {
		t.Fatal(err)
	}
	record := mock.GetRecord(ctx, auth.PaymentID)
	if record == nil || record.State != StateAuthorized || record.CapturedAmount != 0 {
		t.Fatalf("record = %+v, want authorized with capturedAmount 0", record)
	}

	oversized, err := mock.Capture(ctx, &core.CaptureRequest{PaymentID: auth.PaymentID, Amount: "100.01", IP: "1.1.1.1"})
	if err != nil {
		t.Fatal(err)
	}
	if oversized.Code != core.CodeInvalidRequest {
		t.Errorf("oversized capture code = %q, want INVALID_REQUEST", oversized.Code)
	}
	captured, err := mock.Capture(ctx, &core.CaptureRequest{PaymentID: auth.PaymentID, Amount: "80.00", IP: "1.1.1.1"})
	if err != nil {
		t.Fatal(err)
	}
	if captured.Status != core.StatusSuccess {
		t.Fatalf("capture status = %q, want success", captured.Status)
	}
	record = mock.GetRecord(ctx, auth.PaymentID)
	if record == nil || record.State != StateSucceeded || record.CapturedAmount != 8000 {
		t.Fatalf("record = %+v, want succeeded with capturedAmount 8000", record)
	}
	refund, err := mock.Refund(ctx, refundRequest(auth.PaymentID, "80.01"))
	if err != nil {
		t.Fatal(err)
	}
	if refund.Code != core.CodeInvalidRequest {
		t.Errorf("oversized refund code = %q, want INVALID_REQUEST", refund.Code)
	}

	other, err := mock.Authorize(ctx, withCard(CardSuccess))
	if err != nil {
		t.Fatal(err)
	}
	voided, err := mock.VoidAuthorization(ctx, &core.VoidAuthorizationRequest{PaymentID: other.PaymentID, IP: "1.1.1.1"})
	if err != nil {
		t.Fatal(err)
	}
	if voided.Status != core.StatusSuccess || voided.VoidID != other.PaymentID {
		t.Errorf("void = %+v, want success with the payment id", voided)
	}
	if got, err := mock.GetPayment(ctx, other.PaymentID); err != nil {
		t.Fatal(err)
	} else if got.Status != core.StatusCancelled {
		t.Errorf("getPayment status = %q, want cancelled", got.Status)
	}
}

func TestStoredCardsKeepTheCardBehaviour(t *testing.T) {
	ctx := context.Background()
	mock := NewMockProvider()

	request := withCard(CardSuccess)
	request.SaveCard = &core.SaveCardOption{Alias: "Work"}
	first, err := mock.CreatePayment(ctx, request)
	if err != nil {
		t.Fatal(err)
	}
	if first.StoredCard == nil || first.StoredCard.CustomerToken == "" || first.StoredCard.CardToken == "" {
		t.Fatalf("storedCard = %+v, want both tokens", first.StoredCard)
	}
	tokens := first.StoredCard

	byToken := order()
	byToken.PaymentCard = nil
	byToken.StoredCard = &core.StoredCardReference{CustomerToken: tokens.CustomerToken, CardToken: tokens.CardToken}
	again, err := mock.CreatePayment(ctx, &byToken)
	if err != nil {
		t.Fatal(err)
	}
	if again.Status != core.StatusSuccess {
		t.Errorf("payment by token = %q, want success", again.Status)
	}

	declined, err := mock.SaveCard(ctx, &core.SaveCardRequest{
		CustomerToken: tokens.CustomerToken,
		Card: core.PaymentCard{
			CardHolderName: "A B",
			CardNumber:     CardInsufficientFunds,
			ExpireMonth:    "12",
			ExpireYear:     "2030",
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if declined.Card == nil {
		t.Fatal("saveCard returned no card")
	}
	byToken.StoredCard = &core.StoredCardReference{CustomerToken: tokens.CustomerToken, CardToken: declined.Card.CardToken}
	if got, err := mock.CreatePayment(ctx, &byToken); err != nil {
		t.Fatal(err)
	} else if got.Code != core.CodeInsufficientFunds {
		t.Errorf("payment with the declined card = %q, want INSUFFICIENT_FUNDS", got.Code)
	}

	listed, err := mock.ListCards(ctx, &core.ListCardsRequest{CustomerToken: tokens.CustomerToken})
	if err != nil {
		t.Fatal(err)
	}
	if len(listed.Cards) != 2 {
		t.Fatalf("cards = %d, want 2", len(listed.Cards))
	}
	if listed.Cards[0].Alias != "Work" || listed.Cards[0].LastFourDigits != "4242" {
		t.Errorf("first card = %+v, want Work/4242", listed.Cards[0])
	}
	if listed.Cards[1].Alias != "" || listed.Cards[1].LastFourDigits != "9995" {
		t.Errorf("second card = %+v, want no alias/9995", listed.Cards[1])
	}
	encoded, err := json.Marshal(listed)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(encoded), "scenario") {
		t.Error("listCards leaked the internal scenario")
	}

	if got, err := mock.DeleteCard(ctx, &core.DeleteCardRequest{CustomerToken: tokens.CustomerToken, CardToken: tokens.CardToken}); err != nil {
		t.Fatal(err)
	} else if got.Status != core.StatusSuccess {
		t.Errorf("deleteCard = %q, want success", got.Status)
	}
	byToken.StoredCard = &core.StoredCardReference{CustomerToken: tokens.CustomerToken, CardToken: tokens.CardToken}
	if got, err := mock.CreatePayment(ctx, &byToken); err != nil {
		t.Fatal(err)
	} else if got.Code != core.CodeInvalidRequest {
		t.Errorf("payment with a deleted card = %q, want INVALID_REQUEST", got.Code)
	}
	if got, err := mock.DeleteCard(ctx, &core.DeleteCardRequest{CustomerToken: tokens.CustomerToken, CardToken: tokens.CardToken}); err != nil {
		t.Fatal(err)
	} else if got.Code != core.CodeInvalidRequest {
		t.Errorf("deleting an unknown card = %q, want INVALID_REQUEST", got.Code)
	}
}

func TestBinCheckAndInstallmentInfo(t *testing.T) {
	ctx := context.Background()
	mock := NewMockProvider()

	bin, err := mock.BinCheck(ctx, "555555")
	if err != nil {
		t.Fatal(err)
	}
	if bin.CardAssociation != "MASTER_CARD" || bin.BankName != "Mock Bank" || bin.BankCode != 999 {
		t.Errorf("binCheck = %+v, want MASTER_CARD/Mock Bank/999", bin)
	}
	if bin.BinNumber != "555555" || bin.CardType != "CREDIT_CARD" || bin.CardFamily != "Mock" || bin.Commercial {
		t.Errorf("binCheck = %+v, want the mock defaults", bin)
	}

	info, err := mock.InstallmentInfo(ctx, &core.InstallmentInfoRequest{BinNumber: "424242", Price: "100.00"})
	if err != nil {
		t.Fatal(err)
	}
	if len(info.InstallmentDetails) != 1 {
		t.Fatalf("installmentDetails = %d, want 1", len(info.InstallmentDetails))
	}
	detail := info.InstallmentDetails[0]
	want := []struct {
		count int
		total float64
	}{
		{1, 100}, {2, 102}, {3, 103}, {6, 105},
	}
	if len(detail.InstallmentPrices) != len(want) {
		t.Fatalf("installmentPrices = %d, want %d", len(detail.InstallmentPrices), len(want))
	}
	for i, expected := range want {
		got := detail.InstallmentPrices[i]
		if got.InstallmentNumber != expected.count || got.TotalPrice != expected.total {
			t.Errorf("installment %d = %+v, want %d/%v", i, got, expected.count, expected.total)
		}
	}
	if detail.Price != 100 || detail.BinNumber != "424242" || detail.CardAssociation != "VISA" {
		t.Errorf("detail = %+v, want price 100, bin 424242, VISA", detail)
	}
	if detail.InstallmentPrices[1].InstallmentPrice != 51 {
		t.Errorf("installmentPrice = %v, want 51", detail.InstallmentPrices[1].InstallmentPrice)
	}
}

func TestCustomInstallmentRates(t *testing.T) {
	mock := NewMockProvider(MockProviderConfig{InstallmentRates: map[int]float64{3: 4, 1: 0}})
	info, err := mock.InstallmentInfo(context.Background(), &core.InstallmentInfoRequest{BinNumber: "424242", Price: "100.00"})
	if err != nil {
		t.Fatal(err)
	}
	prices := info.InstallmentDetails[0].InstallmentPrices
	if len(prices) != 2 || prices[0].InstallmentNumber != 1 || prices[1].InstallmentNumber != 3 {
		t.Fatalf("prices = %+v, want 1 and 3 sorted", prices)
	}
	if prices[1].TotalPrice != 104 {
		t.Errorf("totalPrice = %v, want 104", prices[1].TotalPrice)
	}
}

func TestCallbacksFromAnotherInstanceOrTamperedWithAreRejected(t *testing.T) {
	ctx := context.Background()
	mock := NewMockProvider()
	init, err := mock.InitThreeDSPayment(ctx, threeDSRequest(withCard(CardSuccess), "https://x.test"))
	if err != nil {
		t.Fatal(err)
	}

	genuine, err := mock.ThreeDSCallback(ctx, init.PaymentID)
	if err != nil {
		t.Fatal(err)
	}
	foreign, err := NewMockProvider().ThreeDSCallback(ctx, init.PaymentID)
	if err != nil {
		t.Fatal(err)
	}
	if got, err := mock.CompleteThreeDSPayment(ctx, foreign); err != nil {
		t.Fatal(err)
	} else if got.Code != core.CodeInvalidHash {
		t.Errorf("foreign callback code = %q, want INVALID_HASH", got.Code)
	}

	declined, err := mock.ThreeDSCallback(ctx, init.PaymentID, false)
	if err != nil {
		t.Fatal(err)
	}
	tampered := map[string]string{}
	for name, value := range declined {
		tampered[name] = value
	}
	tampered["mdStatus"] = "1"
	if got, err := mock.CompleteThreeDSPayment(ctx, tampered); err != nil {
		t.Fatal(err)
	} else if got.Code != core.CodeInvalidHash {
		t.Errorf("tampered callback code = %q, want INVALID_HASH", got.Code)
	}
	if got, err := mock.CompleteThreeDSPayment(ctx, genuine); err != nil {
		t.Fatal(err)
	} else if got.Status != core.StatusSuccess {
		t.Errorf("genuine callback status = %q, want success", got.Status)
	}
}

func TestCallbackSignatureIsBoundToTheInstanceSecret(t *testing.T) {
	ctx := context.Background()
	first := NewMockProvider(MockProviderConfig{Secret: "shared-secret"})
	second := NewMockProvider(MockProviderConfig{Secret: "shared-secret"})
	init, err := first.InitThreeDSPayment(ctx, threeDSRequest(withCard(CardSuccess), "https://x.test"))
	if err != nil {
		t.Fatal(err)
	}

	fields, err := second.ThreeDSCallback(ctx, init.PaymentID)
	if err != nil {
		t.Fatal(err)
	}
	if got, err := first.CompleteThreeDSPayment(ctx, fields); err != nil {
		t.Fatal(err)
	} else if got.Code != core.CodeInvalidRequest {
		t.Errorf("code = %q, want INVALID_REQUEST for an unknown payment", got.Code)
	}
	if !core.SafeEqual(fields["signature"], fields["signature"]) {
		t.Error("SafeEqual rejected identical signatures")
	}
	if core.SafeEqual(fields["signature"], "") {
		t.Error("SafeEqual accepted an empty signature")
	}
}

func TestThreeDSCallbackOfAFailedCardIsDeclined(t *testing.T) {
	ctx := context.Background()
	mock := NewMockProvider()
	init, err := mock.InitThreeDSPayment(ctx, threeDSRequest(withCard(CardThreeDSFailed), "https://x.test"))
	if err != nil {
		t.Fatal(err)
	}
	fields, err := mock.ThreeDSCallback(ctx, init.PaymentID)
	if err != nil {
		t.Fatal(err)
	}
	if fields["mdStatus"] != "0" {
		t.Fatalf("mdStatus = %q, want 0", fields["mdStatus"])
	}
	result, err := mock.CompleteThreeDSPayment(ctx, fields)
	if err != nil {
		t.Fatal(err)
	}
	if result.Status != core.StatusFailure || result.Code != core.CodeThreeDSFailed {
		t.Errorf("result = %+v, want failure/THREEDS_FAILED", result)
	}
}

func TestThreeDSInitRequiresACallbackURL(t *testing.T) {
	mock := NewMockProvider()
	init, err := mock.InitThreeDSPayment(context.Background(), threeDSRequest(withCard(CardSuccess), ""))
	if err != nil {
		t.Fatal(err)
	}
	if init.Status != core.StatusFailure || init.ErrorCode != "VALIDATION_ERROR" {
		t.Fatalf("init = %+v, want a VALIDATION_ERROR failure", init)
	}
	if init.ErrorMessage != "callbackUrl is required" {
		t.Errorf("errorMessage = %q, want 'callbackUrl is required'", init.ErrorMessage)
	}
}

func TestThreeDSInitBuildsAnAutoSubmittingForm(t *testing.T) {
	mock := NewMockProvider()
	init, err := mock.InitThreeDSPayment(context.Background(),
		threeDSRequest(withCard(CardSuccess), `https://shop.test/cb?a=1&b=2`))
	if err != nil {
		t.Fatal(err)
	}
	html := init.ThreeDSHTMLContent
	if !strings.Contains(html, `action="https://shop.test/cb?a=1&amp;b=2"`) {
		t.Errorf("html does not escape the action: %s", html)
	}
	if !strings.Contains(html, `<form id="mock-3ds" method="POST"`) {
		t.Errorf("html has no form: %s", html)
	}
	if !strings.Contains(html, `document.getElementById("mock-3ds").submit();`) {
		t.Errorf("html does not auto-submit: %s", html)
	}
	fields := formFieldsOfHTML(html)
	for _, name := range []string{"paymentId", "conversationId", "mdStatus", "signature"} {
		if _, ok := fields[name]; !ok {
			t.Errorf("html is missing the %s field", name)
		}
	}
	if fields["paymentId"] != init.PaymentID || fields["mdStatus"] != "1" {
		t.Errorf("fields = %v, want the payment id and mdStatus 1", fields)
	}
}

func TestInvalidInputBecomesAValidationErrorResult(t *testing.T) {
	ctx := context.Background()
	mock := NewMockProvider()
	paid, err := mock.CreatePayment(ctx, withCard(CardSuccess))
	if err != nil {
		t.Fatal(err)
	}

	refund, err := mock.Refund(ctx, refundRequest(paid.PaymentID, "abc"))
	if err != nil {
		t.Fatal(err)
	}
	if refund.Status != core.StatusFailure || refund.ErrorCode != "VALIDATION_ERROR" || refund.Code != core.CodeInvalidRequest {
		t.Errorf("refund = %+v, want failure/VALIDATION_ERROR/INVALID_REQUEST", refund)
	}
	capture, err := mock.Capture(ctx, &core.CaptureRequest{PaymentID: paid.PaymentID, Amount: "", IP: "1.1.1.1"})
	if err != nil {
		t.Fatal(err)
	}
	if capture.Code != core.CodeInvalidRequest {
		t.Errorf("capture code = %q, want INVALID_REQUEST", capture.Code)
	}
	saved, err := mock.SaveCard(ctx, &core.SaveCardRequest{})
	if err != nil {
		t.Fatal(err)
	}
	if saved.Status != core.StatusFailure || saved.ErrorCode != "VALIDATION_ERROR" {
		t.Errorf("saveCard = %+v, want a VALIDATION_ERROR failure", saved)
	}
	if saved.ErrorMessage != "card.cardNumber is required" {
		t.Errorf("errorMessage = %q, want 'card.cardNumber is required'", saved.ErrorMessage)
	}
	installments, err := mock.InstallmentInfo(ctx, &core.InstallmentInfoRequest{BinNumber: "424242", Price: "abc"})
	if err != nil {
		t.Fatal(err)
	}
	if installments.ErrorCode != "VALIDATION_ERROR" {
		t.Errorf("installmentInfo = %+v, want a VALIDATION_ERROR failure", installments)
	}
}

func TestUnknownPaymentAndGuards(t *testing.T) {
	ctx := context.Background()
	mock := NewMockProvider()

	if got, err := mock.GetPayment(ctx, "MOCKPAY999999"); err != nil {
		t.Fatal(err)
	} else if got.Code != core.CodeInvalidRequest ||
		got.ErrorMessage != "Unknown payment MOCKPAY999999" {
		t.Errorf("getPayment = %+v, want 'Unknown payment MOCKPAY999999'", got)
	}
	if got, err := mock.Capture(ctx, &core.CaptureRequest{PaymentID: "x", Amount: "1.00", IP: "1.1.1.1"}); err != nil {
		t.Fatal(err)
	} else if got.ErrorMessage != "Only an authorized pre-authorization can be captured" {
		t.Errorf("capture message = %q", got.ErrorMessage)
	}
	if got, err := mock.VoidAuthorization(ctx, &core.VoidAuthorizationRequest{PaymentID: "x", IP: "1.1.1.1"}); err != nil {
		t.Fatal(err)
	} else if got.ErrorMessage != "Only an authorized pre-authorization can be voided" {
		t.Errorf("void message = %q", got.ErrorMessage)
	}
	if got, err := mock.Refund(ctx, refundRequest("x", "1.00")); err != nil {
		t.Fatal(err)
	} else if got.ErrorMessage != "Only a successful payment can be refunded" {
		t.Errorf("refund message = %q", got.ErrorMessage)
	}
	if got, err := mock.Cancel(ctx, &core.CancelRequest{PaymentID: "x", IP: "1.1.1.1"}); err != nil {
		t.Fatal(err)
	} else if got.ErrorMessage != "Only a successful payment without refunds can be cancelled" {
		t.Errorf("cancel message = %q", got.ErrorMessage)
	}
}

func TestCompleteThreeDSPaymentIsIdempotent(t *testing.T) {
	ctx := context.Background()
	mock := NewMockProvider()
	init, err := mock.InitThreeDSPayment(ctx, threeDSRequest(withCard(CardSuccess), "https://x.test"))
	if err != nil {
		t.Fatal(err)
	}
	fields, err := mock.ThreeDSCallback(ctx, init.PaymentID)
	if err != nil {
		t.Fatal(err)
	}

	first, err := mock.CompleteThreeDSPayment(ctx, fields)
	if err != nil {
		t.Fatal(err)
	}
	second, err := mock.CompleteThreeDSPayment(ctx, fields)
	if err != nil {
		t.Fatal(err)
	}
	if first.Status != core.StatusSuccess || second.Status != core.StatusSuccess {
		t.Fatalf("statuses = %q/%q, want success twice", first.Status, second.Status)
	}
	if len(mock.Payments(ctx)) != 1 {
		t.Errorf("payments = %d, want 1", len(mock.Payments(ctx)))
	}
}

func TestCompleteThreeDSPaymentAcceptsParsedJSONAndFormValues(t *testing.T) {
	ctx := context.Background()
	mock := NewMockProvider()
	init, err := mock.InitThreeDSPayment(ctx, threeDSRequest(withCard(CardSuccess), "https://x.test"))
	if err != nil {
		t.Fatal(err)
	}
	fields, err := mock.ThreeDSCallback(ctx, init.PaymentID)
	if err != nil {
		t.Fatal(err)
	}

	asJSON := map[string]any{}
	for name, value := range fields {
		asJSON[name] = value
	}
	if got, err := mock.CompleteThreeDSPayment(ctx, asJSON); err != nil {
		t.Fatal(err)
	} else if got.Status != core.StatusSuccess {
		t.Errorf("json callback status = %q, want success", got.Status)
	}
	if got, err := mock.CompleteThreeDSPayment(ctx, nil); err != nil {
		t.Fatal(err)
	} else if got.Code != core.CodeInvalidHash {
		t.Errorf("empty callback code = %q, want INVALID_HASH", got.Code)
	}
}

func TestPaymentsAreReturnedOldestFirst(t *testing.T) {
	ctx := context.Background()
	mock := NewMockProvider()
	first, err := mock.CreatePayment(ctx, withCard(CardSuccess))
	if err != nil {
		t.Fatal(err)
	}
	second, err := mock.CreatePayment(ctx, withCard(CardSuccessMastercard))
	if err != nil {
		t.Fatal(err)
	}

	payments := mock.Payments(ctx)
	if len(payments) != 2 || payments[0].PaymentID != first.PaymentID || payments[1].PaymentID != second.PaymentID {
		t.Fatalf("payments = %+v, want insertion order", payments)
	}
	if payments[1].CardLastFour != "4444" {
		t.Errorf("cardLastFour = %q, want 4444", payments[1].CardLastFour)
	}
}

func TestAssociationOf(t *testing.T) {
	cases := map[string]string{
		"424242": "VISA",
		"555555": "MASTER_CARD",
		"222100": "MASTER_CARD",
		"272099": "MASTER_CARD",
		"979200": "TROY",
		"650000": "TROY",
		"378282": "AMERICAN_EXPRESS",
		"340000": "AMERICAN_EXPRESS",
		"601100": "UNKNOWN",
		"":       "UNKNOWN",
	}
	for number, want := range cases {
		if got := associationOf(number); got != want {
			t.Errorf("associationOf(%q) = %q, want %q", number, got, want)
		}
	}
}

func TestToMinorRejectsInvalidAmounts(t *testing.T) {
	if got, err := toMinor("12.50"); err != nil {
		t.Fatal(err)
	} else if got != 1250 {
		t.Errorf("toMinor(12.50) = %d, want 1250", got)
	}
	if got, err := toMinor("1.005"); err != nil {
		t.Fatal(err)
	} else if got != 101 {
		t.Errorf("toMinor(1.005) = %d, want 101 (half up)", got)
	}
	for _, invalid := range []string{"", "abc", "-1"} {
		if _, err := toMinor(invalid); err == nil {
			t.Errorf("toMinor(%q) accepted invalid input", invalid)
		}
	}
}
