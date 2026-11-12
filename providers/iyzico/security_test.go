package iyzico

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/boracomet/go-pos/core"
)

func newSecurityProvider(t *testing.T, transport core.Doer) *Provider {
	t.Helper()
	p, err := New(Config{
		ProviderConfig: core.ProviderConfig{BaseURL: SandboxURL, Doer: transport},
		APIKey:         "api-key",
		SecretKey:      "secret-key",
	})
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	return p
}

func TestNewRequiresCredentials(t *testing.T) {
	cases := []struct {
		name   string
		config Config
		want   string
	}{
		{"apiKey", Config{ProviderConfig: core.ProviderConfig{BaseURL: "https://x"}, SecretKey: "s"}, "apiKey"},
		{"secretKey", Config{ProviderConfig: core.ProviderConfig{BaseURL: "https://x"}, APIKey: "a"}, "secretKey"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := New(tc.config)
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("New() error = %v, want it to mention %q", err, tc.want)
			}
			var cfgErr *core.ConfigurationError
			if !errors.As(err, &cfgErr) {
				t.Errorf("error type = %T, want *core.ConfigurationError", err)
			}
		})
	}
}

func TestNewRequiresBaseURL(t *testing.T) {
	_, err := New(Config{APIKey: "a", SecretKey: "s"})
	if err == nil || !strings.Contains(err.Error(), "baseUrl") {
		t.Fatalf("New() error = %v, want it to mention baseUrl", err)
	}
}

func TestRetrieveCheckoutFormStatusReflectsThePayment(t *testing.T) {
	cases := []struct {
		name          string
		paymentStatus string
		want          core.PaymentStatus
	}{
		{"failed payment", "FAILURE", core.StatusFailure},
		{"successful payment", "SUCCESS", core.StatusSuccess},
		{"3DS in progress", "INIT_THREEDS", core.StatusPending},
		{"callback pending", "CALLBACK_THREEDS", core.StatusPending},
		{"waiting", "WAITING", core.StatusPending},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			transport := &stubTransport{body: `{"status":"success","paymentStatus":"` + tc.paymentStatus +
				`","token":"t","errorMessage":"Kart limiti yetersiz"}`}
			p := newSecurityProvider(t, transport)

			result, err := p.RetrieveCheckoutForm(context.Background(), "t", "")
			if err != nil {
				t.Fatalf("RetrieveCheckoutForm() error = %v", err)
			}
			if result.Status != tc.want {
				t.Errorf("status = %q, want %q", result.Status, tc.want)
			}
			if result.PaymentStatus != tc.paymentStatus {
				t.Errorf("paymentStatus = %q, want %q", result.PaymentStatus, tc.paymentStatus)
			}
		})
	}
}

func TestRetrieveSubscriptionUsesGetAndSignsEmptyBody(t *testing.T) {
	transport := &stubTransport{body: `{"status":"success","data":{"referenceCode":"sub-1"}}`}
	p := newSecurityProvider(t, transport)

	result, err := p.RetrieveSubscription(context.Background(), &core.SubscriptionRetrieveRequest{
		SubscriptionReferenceCode: "sub-1",
	})
	if err != nil {
		t.Fatalf("RetrieveSubscription() error = %v", err)
	}

	req := transport.lastRequest()
	if req.Method != http.MethodGet {
		t.Errorf("method = %q, want GET", req.Method)
	}
	if req.URL.Path != "/v2/subscription/subscriptions/sub-1" {
		t.Errorf("path = %q", req.URL.Path)
	}
	if transport.lastBody() != "" {
		t.Errorf("body = %q, want none", transport.lastBody())
	}
	if auth := req.Header.Get("Authorization"); !strings.HasPrefix(auth, "IYZWSv2 ") {
		t.Errorf("Authorization = %q, want an IYZWSv2 signature", auth)
	}
	if result.Status != core.StatusSuccess {
		t.Errorf("status = %q, want success", result.Status)
	}
	if result.Data == nil || result.Data.ReferenceCode != "sub-1" {
		t.Errorf("data = %+v", result.Data)
	}
}

func TestCancelSubscriptionMapsAPIStatus(t *testing.T) {
	p := newSecurityProvider(t, &stubTransport{body: `{"status":"failure","errorCode":"100001","errorMessage":"x"}`})

	result, err := p.CancelSubscription(context.Background(), &core.SubscriptionCancelRequest{
		SubscriptionReferenceCode: "sub-1",
	})
	if err != nil {
		t.Fatalf("CancelSubscription() error = %v", err)
	}
	if result.Status != core.StatusFailure || result.ErrorCode != "100001" {
		t.Errorf("result = %+v", result)
	}
}

func TestCompleteThreeDSPaymentRejectsUnauthenticatedCallback(t *testing.T) {
	transport := &stubTransport{}
	p := newSecurityProvider(t, transport)

	result, err := p.CompleteThreeDSPayment(context.Background(), map[string]any{
		"status":         "failure",
		"paymentId":      "123",
		"mdStatus":       "0",
		"conversationId": "c1",
	})
	if err != nil {
		t.Fatalf("CompleteThreeDSPayment() error = %v", err)
	}
	if result.Status != core.StatusFailure {
		t.Errorf("status = %q, want failure", result.Status)
	}
	if transport.calls() != 0 {
		t.Errorf("provider was called %d times, want 0", transport.calls())
	}
}

func TestCompleteThreeDSPaymentDoesNotTrustRejectedCallbackIDs(t *testing.T) {
	p := newSecurityProvider(t, &stubTransport{})

	result, err := p.CompleteThreeDSPayment(context.Background(), &ThreeDSCallbackData{
		Status:         "failure",
		PaymentID:      "P-OTHER",
		MDStatus:       "0",
		ConversationID: "ORDER-OTHER",
	})
	if err != nil {
		t.Fatalf("CompleteThreeDSPayment() error = %v", err)
	}
	if result.Status != core.StatusFailure {
		t.Errorf("status = %q, want failure", result.Status)
	}
	if result.PaymentID != "" || result.ConversationID != "" {
		t.Errorf("ids = %q/%q, want empty", result.PaymentID, result.ConversationID)
	}
	if result.ErrorCode != "MD_STATUS_0" {
		t.Errorf("errorCode = %q, want MD_STATUS_0", result.ErrorCode)
	}
}

func TestCompleteThreeDSPaymentDropsIDsWhenIyzicoRejects(t *testing.T) {
	p := newSecurityProvider(t, &stubTransport{
		body: `{"status":"failure","errorCode":"5005","errorMessage":"x","conversationId":"ORDER-OTHER"}`,
	})

	result, err := p.CompleteThreeDSPayment(context.Background(), map[string]any{
		"status":         "success",
		"paymentId":      "P-OTHER",
		"mdStatus":       "1",
		"conversationId": "ORDER-OTHER",
	})
	if err != nil {
		t.Fatalf("CompleteThreeDSPayment() error = %v", err)
	}
	if result.Status != core.StatusFailure {
		t.Errorf("status = %q, want failure", result.Status)
	}
	if result.PaymentID != "" || result.ConversationID != "" {
		t.Errorf("ids = %q/%q, want empty", result.PaymentID, result.ConversationID)
	}
}

func TestCompleteThreeDSPaymentAuthorizesVerifiedCallback(t *testing.T) {
	transport := &stubTransport{body: `{"status":"success","paymentId":"123","conversationId":"c1"}`}
	p := newSecurityProvider(t, transport)

	result, err := p.CompleteThreeDSPayment(context.Background(), map[string]any{
		"status":         "success",
		"paymentId":      "123",
		"mdStatus":       "1",
		"conversationId": "c1",
	})
	if err != nil {
		t.Fatalf("CompleteThreeDSPayment() error = %v", err)
	}
	if result.Status != core.StatusSuccess || result.PaymentID != "123" || result.ConversationID != "c1" {
		t.Errorf("result = %+v", result)
	}
	if got := transport.lastRequest().URL.Path; got != "/payment/3dsecure/auth" {
		t.Errorf("path = %q", got)
	}
}

func TestCompleteThreeDSPaymentAcceptsNumericMDStatus(t *testing.T) {
	transport := &stubTransport{body: `{"status":"success","paymentId":"123","conversationId":"c1"}`}
	p := newSecurityProvider(t, transport)

	result, err := p.CompleteThreeDSPayment(context.Background(), map[string]any{
		"status": "success", "paymentId": "123", "mdStatus": float64(1), "conversationId": "c1",
	})
	if err != nil {
		t.Fatalf("CompleteThreeDSPayment() error = %v", err)
	}
	if result.Status != core.StatusSuccess {
		t.Errorf("status = %q, want success", result.Status)
	}
}

func validPaymentRequest() *core.PaymentRequest {
	return &core.PaymentRequest{
		Price:     "1.00",
		PaidPrice: "1.00",
		Currency:  "TRY",
		BasketID:  "B1",
		PaymentCard: &core.PaymentCard{
			CardHolderName: "A B",
			CardNumber:     "5528790000000008",
			ExpireMonth:    "12",
			ExpireYear:     "2030",
			CVC:            "123",
		},
		Buyer: core.Buyer{
			ID: "b", Name: "A", Surname: "B", Email: "a@b.c", IdentityNumber: "11111111111",
			RegistrationAddress: "x", City: "x", Country: "x", IP: "1.1.1.1", GSMNumber: "+905350000000",
		},
		ShippingAddress: core.Address{ContactName: "A", City: "x", Country: "x", Address: "x"},
		BillingAddress:  core.Address{ContactName: "A", City: "x", Country: "x", Address: "x"},
		BasketItems: []core.BasketItem{
			{ID: "i", Name: "n", Category1: "c", ItemType: "PHYSICAL", Price: "1.00"},
		},
	}
}

func TestCreatePaymentTimeoutIsPending(t *testing.T) {
	p := newSecurityProvider(t, &stubTransport{err: context.DeadlineExceeded})

	result, err := p.CreatePayment(context.Background(), validPaymentRequest())
	if err != nil {
		t.Fatalf("CreatePayment() error = %v", err)
	}
	if result.Status != core.StatusPending {
		t.Errorf("status = %q, want pending", result.Status)
	}
	if result.ErrorCode != core.NetworkErrorCode {
		t.Errorf("errorCode = %q, want NETWORK_ERROR", result.ErrorCode)
	}
	if result.Code != core.CodeNetworkError {
		t.Errorf("code = %q, want %q", result.Code, core.CodeNetworkError)
	}
}

func TestCreatePaymentIsNeverRetried(t *testing.T) {
	transport := &stubTransport{err: errors.New("connection reset")}
	p := newSecurityProvider(t, transport)
	p.Config.Retry = &core.RetryConfig{Attempts: 3}
	p.client = p.BaseProvider.NewHTTPClient(requestTimeout, nil)

	if _, err := p.CreatePayment(context.Background(), validPaymentRequest()); err != nil {
		t.Fatalf("CreatePayment() error = %v", err)
	}
	if transport.calls() != 1 {
		t.Errorf("attempts = %d, want 1: a payment must never be retried", transport.calls())
	}
}

func TestBinCheckIsRetryable(t *testing.T) {
	transport := &stubTransport{err: errors.New("connection reset")}
	p := newSecurityProvider(t, transport)
	p.Config.Retry = &core.RetryConfig{Attempts: 2, Delay: time.Millisecond}
	p.client = p.BaseProvider.NewHTTPClient(requestTimeout, nil)

	if _, err := p.BinCheck(context.Background(), "454360"); err == nil {
		t.Fatal("BinCheck() error = nil, want a transport error")
	}
	if transport.calls() != 2 {
		t.Errorf("attempts = %d, want 2: a read-only query may be retried", transport.calls())
	}
}

func TestPaymentBodyIsSignedByteForByte(t *testing.T) {
	transport := &stubTransport{body: `{"status":"success","paymentId":"1"}`}
	p := newSecurityProvider(t, transport)

	request := validPaymentRequest()
	request.BasketItems[0].Name = "Kılıç & <b>Pro</b>"
	if _, err := p.CreatePayment(context.Background(), request); err != nil {
		t.Fatalf("CreatePayment() error = %v", err)
	}

	body := transport.lastBody()
	if !strings.Contains(body, "Kılıç & <b>Pro</b>") {
		t.Errorf("body was HTML-escaped: %q", body)
	}
	auth := transport.lastRequest().Header.Get("Authorization")
	random := transport.lastRequest().Header.Get("x-iyzi-rnd")
	want, err := GenerateAuthStringV2("api-key", "secret-key", random, "/payment/auth", body)
	if err != nil {
		t.Fatalf("GenerateAuthStringV2() error = %v", err)
	}
	if !core.SafeEqual(auth, want) {
		t.Error("Authorization does not cover the exact body sent")
	}
}
