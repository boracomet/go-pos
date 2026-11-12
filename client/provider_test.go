package client

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/boracomet/go-pos/core"
)

// fixedDoer answers every request with exactly the response it was given. Unlike
// the shared recording doer it substitutes no default body, so an empty answer
// stays empty.
type fixedDoer struct {
	status int
	body   string
}

func (d fixedDoer) Do(r *http.Request) (*http.Response, error) {
	status := d.status
	if status == 0 {
		status = http.StatusOK
	}
	return &http.Response{
		StatusCode: status,
		Status:     http.StatusText(status),
		Header:     http.Header{"Content-Type": []string{"application/json"}},
		Body:       io.NopCloser(strings.NewReader(d.body)),
		Request:    r,
	}, nil
}

// TestProviderClientDecodesEveryResult checks that each operation unmarshals the
// answer into its own result type, not just into a generic payment response.
func TestProviderClientDecodesEveryResult(t *testing.T) {
	ctx := context.Background()

	t.Run("authorize", func(t *testing.T) {
		doer := &recordingDoer{body: `{"status":"success","paymentId":"P1","conversationId":"O1"}`}
		result, err := testClient(doer, "/api/pay").Akbank.Authorize(ctx, &core.PaymentRequest{})
		if err != nil {
			t.Fatalf("Authorize() error = %v", err)
		}
		if result.Status != core.StatusSuccess || result.PaymentID != "P1" || result.ConversationID != "O1" {
			t.Errorf("Authorize() = %+v, want the decoded payment", result)
		}
	})

	t.Run("capture", func(t *testing.T) {
		doer := &recordingDoer{body: `{"status":"success","paymentId":"P1","code":"SUCCESS"}`}
		result, err := testClient(doer, "/api/pay").Garanti.Capture(ctx, &core.CaptureRequest{PaymentID: "P1", Amount: "1.00"})
		if err != nil {
			t.Fatalf("Capture() error = %v", err)
		}
		if result.Status != core.StatusSuccess || result.PaymentID != "P1" {
			t.Errorf("Capture() = %+v, want the decoded payment", result)
		}
		if doer.bodies[0] != `{"paymentId":"P1","amount":"1.00","ip":""}` {
			t.Errorf("body = %s, want the serialized capture request", doer.bodies[0])
		}
	})

	t.Run("void", func(t *testing.T) {
		doer := &recordingDoer{body: `{"status":"cancelled","transactionId":"T1","voidId":"V1"}`}
		result, err := testClient(doer, "/api/pay").Garanti.VoidAuthorization(ctx, &core.VoidAuthorizationRequest{PaymentID: "P1"})
		if err != nil {
			t.Fatalf("VoidAuthorization() error = %v", err)
		}
		if result.Status != core.StatusCancelled || result.TransactionID != "T1" || result.VoidID != "V1" {
			t.Errorf("VoidAuthorization() = %+v, want the decoded cancellation", result)
		}
	})

	t.Run("cancel", func(t *testing.T) {
		doer := &recordingDoer{body: `{"status":"cancelled","transactionId":"T1"}`}
		result, err := testClient(doer, "/api/pay").Sipay.Cancel(ctx, &core.CancelRequest{PaymentID: "P1"})
		if err != nil {
			t.Fatalf("Cancel() error = %v", err)
		}
		if result.Status != core.StatusCancelled || result.TransactionID != "T1" {
			t.Errorf("Cancel() = %+v, want the decoded cancellation", result)
		}
	})

	t.Run("refund", func(t *testing.T) {
		doer := &recordingDoer{body: `{"status":"success","refundId":"R1","conversationId":"O1"}`}
		result, err := testClient(doer, "/api/pay").ParamPOS.Refund(ctx, &core.RefundRequest{PaymentID: "P1", Price: "1.00"})
		if err != nil {
			t.Fatalf("Refund() error = %v", err)
		}
		if result.Status != core.StatusSuccess || result.RefundID != "R1" {
			t.Errorf("Refund() = %+v, want the decoded refund", result)
		}
	})

	t.Run("save card", func(t *testing.T) {
		doer := &recordingDoer{body: `{"status":"success","customerToken":"U1",` +
			`"card":{"cardToken":"C1","alias":"Work","binNumber":"552879","lastFourDigits":"0008","bankName":"Halkbank"}}`}
		result, err := testClient(doer, "/api/pay").Iyzico.SaveCard(ctx, &core.SaveCardRequest{})
		if err != nil {
			t.Fatalf("SaveCard() error = %v", err)
		}
		if result.Status != core.StatusSuccess || result.CustomerToken != "U1" {
			t.Fatalf("SaveCard() = %+v, want the decoded card", result)
		}
		if result.Card == nil || result.Card.CardToken != "C1" || result.Card.BankName != "Halkbank" {
			t.Errorf("card = %+v, want the stored card's details", result.Card)
		}
	})

	t.Run("list cards", func(t *testing.T) {
		doer := &recordingDoer{body: `{"status":"success","customerToken":"U1","cards":[` +
			`{"cardToken":"C1","lastFourDigits":"0008","requiresCvc":true},` +
			`{"cardToken":"C2","lastFourDigits":"4358","requiresCvc":false}]}`}
		result, err := testClient(doer, "/api/pay").Iyzico.ListCards(ctx, &core.ListCardsRequest{CustomerToken: "U1"})
		if err != nil {
			t.Fatalf("ListCards() error = %v", err)
		}
		if len(result.Cards) != 2 {
			t.Fatalf("ListCards() returned %d cards, want 2", len(result.Cards))
		}
		if result.Cards[0].CardToken != "C1" || result.Cards[1].CardToken != "C2" {
			t.Errorf("cards = %+v, want both tokens in order", result.Cards)
		}
		if result.Cards[0].RequiresCVC == nil || !*result.Cards[0].RequiresCVC {
			t.Errorf("requiresCvc = %v, want true", result.Cards[0].RequiresCVC)
		}
		if result.Cards[1].RequiresCVC == nil || *result.Cards[1].RequiresCVC {
			t.Errorf("requiresCvc = %v, want false", result.Cards[1].RequiresCVC)
		}
	})

	t.Run("delete card", func(t *testing.T) {
		doer := &recordingDoer{body: `{"status":"success"}`}
		result, err := testClient(doer, "/api/pay").Iyzico.DeleteCard(ctx, &core.DeleteCardRequest{CustomerToken: "U1", CardToken: "C1"})
		if err != nil {
			t.Fatalf("DeleteCard() error = %v", err)
		}
		if result.Status != core.StatusSuccess {
			t.Errorf("DeleteCard() = %+v, want success", result)
		}
	})

	t.Run("3DS init", func(t *testing.T) {
		doer := &recordingDoer{body: `{"status":"pending","threeDSHtmlContent":"<form></form>",` +
			`"paymentId":"P1","conversationId":"O1"}`}
		result, err := testClient(doer, "/api/pay").PayTR.InitThreeDSPayment(ctx, &core.ThreeDSPaymentRequest{})
		if err != nil {
			t.Fatalf("InitThreeDSPayment() error = %v", err)
		}
		if result.Status != core.StatusPending || result.ThreeDSHTMLContent != "<form></form>" {
			t.Errorf("InitThreeDSPayment() = %+v, want the bank form", result)
		}
		if result.PaymentID != "P1" {
			t.Errorf("paymentId = %q, want P1", result.PaymentID)
		}
	})

	t.Run("3DS pre-authorization init", func(t *testing.T) {
		doer := &recordingDoer{body: `{"status":"pending","redirectUrl":"https://bank.example/3d"}`}
		result, err := testClient(doer, "/api/pay").Akbank.InitThreeDSAuthorize(ctx, &core.ThreeDSPaymentRequest{})
		if err != nil {
			t.Fatalf("InitThreeDSAuthorize() error = %v", err)
		}
		if result.Status != core.StatusPending || result.RedirectURL != "https://bank.example/3d" {
			t.Errorf("InitThreeDSAuthorize() = %+v, want the redirect", result)
		}
	})

	t.Run("complete 3DS", func(t *testing.T) {
		doer := &recordingDoer{body: `{"status":"success","paymentId":"P1",` +
			`"storedCard":{"customerToken":"U1","cardToken":"C1"}}`}
		result, err := testClient(doer, "/api/pay").Mock.CompleteThreeDSPayment(ctx, map[string]any{"paymentId": "P1"})
		if err != nil {
			t.Fatalf("CompleteThreeDSPayment() error = %v", err)
		}
		if result.StoredCard == nil || result.StoredCard.CustomerToken != "U1" || result.StoredCard.CardToken != "C1" {
			t.Errorf("storedCard = %+v, want the tokens of the card just saved", result.StoredCard)
		}
		if doer.bodies[0] != `{"paymentId":"P1"}` {
			t.Errorf("body = %s, want the callback data", doer.bodies[0])
		}
	})

	t.Run("get payment", func(t *testing.T) {
		doer := &recordingDoer{body: `{"status":"success","paymentId":"P1","httpStatus":200,"attempts":1}`}
		result, err := testClient(doer, "/api/pay").Iyzico.GetPayment(ctx, "P1")
		if err != nil {
			t.Fatalf("GetPayment() error = %v", err)
		}
		if result.PaymentID != "P1" || result.HTTPStatus != 200 || result.Attempts != 1 {
			t.Errorf("GetPayment() = %+v, want the decoded payment", result)
		}
	})

	t.Run("bin check", func(t *testing.T) {
		doer := &recordingDoer{body: `{"binNumber":"552879","cardType":"CREDIT_CARD",` +
			`"cardAssociation":"MASTERCARD","cardFamily":"Bonus","bankName":"Halkbank","bankCode":12,"commercial":true}`}
		result, err := testClient(doer, "/api/pay").Iyzico.BinCheck(ctx, "552879")
		if err != nil {
			t.Fatalf("BinCheck() error = %v", err)
		}
		if result.BinNumber != "552879" || result.BankName != "Halkbank" || result.BankCode != 12 {
			t.Errorf("BinCheck() = %+v, want the decoded BIN", result)
		}
		if !result.Commercial || result.CardAssociation != "MASTERCARD" {
			t.Errorf("BinCheck() = %+v, want a commercial Mastercard", result)
		}
	})

	t.Run("installment info", func(t *testing.T) {
		doer := &recordingDoer{body: `{"status":"success","conversationId":"O1","installmentDetails":[` +
			`{"binNumber":"552879","price":100,"bankName":"Halkbank","installmentPrices":[` +
			`{"installmentNumber":1,"totalPrice":100,"installmentPrice":100},` +
			`{"installmentNumber":3,"totalPrice":103,"installmentPrice":34.33,"commissionRate":3}]}]}`}
		result, err := testClient(doer, "/api/pay").Iyzico.InstallmentInfo(ctx,
			&core.InstallmentInfoRequest{BinNumber: "552879", Price: "100.00"})
		if err != nil {
			t.Fatalf("InstallmentInfo() error = %v", err)
		}
		if len(result.InstallmentDetails) != 1 {
			t.Fatalf("InstallmentInfo() returned %d BINs, want 1", len(result.InstallmentDetails))
		}
		details := result.InstallmentDetails[0]
		if len(details.InstallmentPrices) != 2 {
			t.Fatalf("installmentPrices = %+v, want two options", details.InstallmentPrices)
		}
		three := details.InstallmentPrices[1]
		if three.InstallmentNumber != 3 || three.TotalPrice != 103 {
			t.Errorf("installment = %+v, want three payments totalling 103", three)
		}
		if three.CommissionRate == nil || *three.CommissionRate != 3 {
			t.Errorf("commissionRate = %v, want 3", three.CommissionRate)
		}
		if details.InstallmentPrices[0].CommissionRate != nil {
			t.Errorf("commissionRate = %v, want it absent for a single payment", details.InstallmentPrices[0].CommissionRate)
		}
	})
}

// TestProviderClientAcceptsAnEmptySuccessBody covers the operations the server
// answers with no content: the result stays zero-valued rather than failing to
// decode.
func TestProviderClientAcceptsAnEmptySuccessBody(t *testing.T) {
	for _, status := range []int{http.StatusOK, http.StatusNoContent} {
		doer := fixedDoer{status: status}
		c := New(Config{BaseURL: "/api/pay", Doer: doer})
		result, err := c.Iyzico.DeleteCard(context.Background(), &core.DeleteCardRequest{CustomerToken: "U1", CardToken: "C1"})
		if err != nil {
			t.Fatalf("status %d: DeleteCard() error = %v", status, err)
		}
		if result == nil {
			t.Fatalf("status %d: DeleteCard() returned no result", status)
		}
		if result.Status != "" {
			t.Errorf("status %d: result = %+v, want a zero-valued result", status, result)
		}
	}
}

// TestProviderClientRejectsANonJSONSuccessBody checks that a 2xx answer that is
// not a result is reported, not silently swallowed.
func TestProviderClientRejectsANonJSONSuccessBody(t *testing.T) {
	c := New(Config{BaseURL: "/api/pay", Doer: fixedDoer{status: http.StatusOK, body: "<html>maintenance</html>"}})
	result, err := c.Iyzico.CreatePayment(context.Background(), &core.PaymentRequest{})
	if err == nil {
		t.Fatal("CreatePayment() returned no error for a non-JSON body")
	}
	if result != nil {
		t.Errorf("CreatePayment() result = %+v, want nil", result)
	}
}

// TestProviderClientRejectsARedirect checks that only a 2xx answer is a result,
// even when the body looks like one.
func TestProviderClientRejectsARedirect(t *testing.T) {
	c := New(Config{
		BaseURL: "/api/pay",
		Doer:    fixedDoer{status: http.StatusFound, body: `{"status":"success"}`},
	})
	_, err := c.Iyzico.CreatePayment(context.Background(), &core.PaymentRequest{})
	var requestErr *RequestError
	if !errors.As(err, &requestErr) {
		t.Fatalf("error = %v, want a RequestError", err)
	}
	if requestErr.Status != http.StatusFound {
		t.Errorf("status = %d, want 302", requestErr.Status)
	}
}

// TestProviderClientSetsTheJSONContentType checks that a POST always declares
// JSON, while a GET keeps whatever the caller configured.
func TestProviderClientSetsTheJSONContentType(t *testing.T) {
	doer := &recordingDoer{}
	c := New(Config{
		BaseURL: "/api/pay",
		Doer:    doer,
		Headers: map[string]string{"Content-Type": "text/plain", "Authorization": "Bearer test-token"},
	})
	ctx := context.Background()

	if _, err := c.Iyzico.CreatePayment(ctx, &core.PaymentRequest{}); err != nil {
		t.Fatalf("CreatePayment() error = %v", err)
	}
	if got := doer.requests[0].Header.Get("Content-Type"); got != "application/json" {
		t.Errorf("POST Content-Type = %q, want application/json", got)
	}
	if got := doer.requests[0].Header.Get("Authorization"); got != "Bearer test-token" {
		t.Errorf("Authorization = %q, want the configured header", got)
	}

	if _, err := c.Iyzico.GetPayment(ctx, "P1"); err != nil {
		t.Fatalf("GetPayment() error = %v", err)
	}
	if got := doer.requests[1].Header.Get("Content-Type"); got != "text/plain" {
		t.Errorf("GET Content-Type = %q, want the configured header", got)
	}
	if got := doer.requests[1].Header.Get("Authorization"); got != "Bearer test-token" {
		t.Errorf("Authorization = %q, want the configured header on every call", got)
	}
}

// TestProviderClientUsesACustomProviderID checks the path of a provider the
// server was configured with under its own id.
func TestProviderClientUsesACustomProviderID(t *testing.T) {
	doer := &recordingDoer{}
	c := New(Config{BaseURL: "/api/pay", Doer: doer})
	provider := c.Use("my-pos")
	if provider.Provider() != "my-pos" {
		t.Errorf("Provider() = %q, want my-pos", provider.Provider())
	}
	if _, err := provider.CreatePayment(context.Background(), &core.PaymentRequest{}); err != nil {
		t.Fatalf("CreatePayment() error = %v", err)
	}
	if got, want := doer.lastURL(), "http://localhost/api/pay/my-pos/payment"; got != want {
		t.Errorf("url = %q, want %q", got, want)
	}
}

// TestProviderClientRoundTripsOverHTTP drives a real local server, so the whole
// path from the typed request to the typed result is exercised.
func TestProviderClientRoundTripsOverHTTP(t *testing.T) {
	var gotPath, gotMethod, gotBody string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		gotPath, gotMethod, gotBody = r.URL.Path, r.Method, string(raw)
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"status":"success","paymentId":"P9","conversationId":"O9"}`)
	}))
	t.Cleanup(server.Close)

	c := New(Config{BaseURL: server.URL + "/api/pay"})
	result, err := c.Mock.CreatePayment(context.Background(), &core.PaymentRequest{Price: "1.00", ConversationID: "O9"})
	if err != nil {
		t.Fatalf("CreatePayment() error = %v", err)
	}
	if result.PaymentID != "P9" || result.ConversationID != "O9" {
		t.Errorf("CreatePayment() = %+v, want the server's payment", result)
	}
	if gotPath != "/api/pay/mock/payment" || gotMethod != http.MethodPost {
		t.Errorf("request = %s %s, want POST /api/pay/mock/payment", gotMethod, gotPath)
	}
	if !strings.Contains(gotBody, `"price":"1.00"`) {
		t.Errorf("body = %s, want the serialized request", gotBody)
	}
}

// TestProviderClientHonoursContextCancellation checks that the caller's context
// reaches the transport.
func TestProviderClientHonoursContextCancellation(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(w, `{"status":"success"}`)
	}))
	t.Cleanup(server.Close)

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	c := New(Config{BaseURL: server.URL})
	if _, err := c.Iyzico.CreatePayment(ctx, &core.PaymentRequest{}); !errors.Is(err, context.Canceled) {
		t.Fatalf("CreatePayment() error = %v, want context.Canceled", err)
	}
	if _, err := c.Health(ctx); !errors.Is(err, context.Canceled) {
		t.Fatalf("Health() error = %v, want context.Canceled", err)
	}
}

// TestRequestErrorMessage covers both shapes of the error text: the server's
// message when it sent one, the HTTP status otherwise.
func TestRequestErrorMessage(t *testing.T) {
	withMessage := &RequestError{Status: http.StatusBadRequest, StatusText: "Bad Request", Message: "Invalid card"}
	if got := withMessage.Error(); got != "Invalid card" {
		t.Errorf("Error() = %q, want the server's message", got)
	}
	withoutMessage := &RequestError{Status: http.StatusNotFound, StatusText: "Not Found"}
	if got, want := withoutMessage.Error(), "HTTP 404: Not Found"; got != want {
		t.Errorf("Error() = %q, want %q", got, want)
	}
}

// TestStatusTextFallsBackToTheStandardReason covers a transport that reports no
// reason phrase of its own.
func TestStatusTextFallsBackToTheStandardReason(t *testing.T) {
	bare := &http.Response{StatusCode: http.StatusTeapot, Status: "418"}
	if got, want := statusText(bare), http.StatusText(http.StatusTeapot); got != want {
		t.Errorf("statusText() = %q, want %q", got, want)
	}
	custom := &http.Response{StatusCode: http.StatusTeapot, Status: "418 I am a teapot"}
	if got := statusText(custom); got != "I am a teapot" {
		t.Errorf("statusText() = %q, want the server's reason", got)
	}
}
