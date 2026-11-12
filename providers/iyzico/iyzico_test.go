package iyzico

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/boracomet/go-pos/core"
)

// stubTransport records the requests a provider sends and answers with a canned
// body, so tests never reach the network.
type stubTransport struct {
	body   string
	status int
	err    error

	requests []*http.Request
	bodies   []string
}

func (s *stubTransport) Do(req *http.Request) (*http.Response, error) {
	body := ""
	if req.Body != nil {
		raw, _ := io.ReadAll(req.Body)
		body = string(raw)
	}
	s.requests = append(s.requests, req)
	s.bodies = append(s.bodies, body)
	if s.err != nil {
		return nil, s.err
	}
	status := s.status
	if status == 0 {
		status = http.StatusOK
	}
	return &http.Response{
		StatusCode: status,
		Header:     http.Header{"Content-Type": []string{"application/json"}},
		Body:       io.NopCloser(strings.NewReader(s.body)),
	}, nil
}

func (s *stubTransport) calls() int { return len(s.requests) }

func (s *stubTransport) lastRequest() *http.Request {
	if len(s.requests) == 0 {
		return nil
	}
	return s.requests[len(s.requests)-1]
}

func (s *stubTransport) lastBody() string {
	if len(s.bodies) == 0 {
		return ""
	}
	return s.bodies[len(s.bodies)-1]
}

func (s *stubTransport) lastFields(t *testing.T) map[string]any {
	t.Helper()
	var fields map[string]any
	if err := json.Unmarshal([]byte(s.lastBody()), &fields); err != nil {
		t.Fatalf("request body is not JSON: %v", err)
	}
	return fields
}

func newTestProvider(t *testing.T, transport core.Doer) *Provider {
	t.Helper()
	p, err := New(Config{
		ProviderConfig: core.ProviderConfig{
			BaseURL: SandboxURL,
			Locale:  "tr",
			Doer:    transport,
		},
		APIKey:    "test-api-key",
		SecretKey: "test-secret-key",
	})
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	return p
}

func TestBinCheckMapsSuccessfulResponse(t *testing.T) {
	transport := &stubTransport{body: `{
		"status": "success",
		"locale": "tr",
		"systemTime": 123456789,
		"binNumber": "454360",
		"cardType": "CREDIT_CARD",
		"cardAssociation": "VISA",
		"cardFamily": "Maximum",
		"bankName": "Türkiye İş Bankası",
		"bankCode": 64,
		"commercial": 0
	}`}
	p := newTestProvider(t, transport)

	result, err := p.BinCheck(context.Background(), "454360")
	if err != nil {
		t.Fatalf("BinCheck() error = %v", err)
	}
	if result.BinNumber != "454360" || result.CardType != "CREDIT_CARD" ||
		result.CardAssociation != "VISA" || result.CardFamily != "Maximum" ||
		result.BankName != "Türkiye İş Bankası" || result.BankCode != 64 {
		t.Errorf("BinCheck() = %+v", result)
	}
	if result.Commercial {
		t.Error("commercial 0 must map to false")
	}
	raw, ok := result.RawResponse.(map[string]any)
	if !ok || raw["binNumber"] != "454360" {
		t.Errorf("RawResponse = %#v, want the parsed API body", result.RawResponse)
	}
	if got := transport.lastRequest().URL.Path; got != "/payment/bin/check" {
		t.Errorf("path = %q", got)
	}
}

func TestBinCheckReturnsErrorOnFailure(t *testing.T) {
	p := newTestProvider(t, &stubTransport{body: `{"status":"failure","errorMessage":"BIN not found"}`})

	_, err := p.BinCheck(context.Background(), "000000")
	if err == nil || err.Error() != "BIN not found" {
		t.Fatalf("BinCheck() error = %v, want \"BIN not found\"", err)
	}
}

func TestBinCheckFallsBackToDefaultMessage(t *testing.T) {
	p := newTestProvider(t, &stubTransport{body: `{"status":"failure"}`})

	_, err := p.BinCheck(context.Background(), "000000")
	if err == nil || err.Error() != "BIN check failed" {
		t.Fatalf("BinCheck() error = %v, want \"BIN check failed\"", err)
	}
}

func TestMapStatus(t *testing.T) {
	cases := map[string]core.PaymentStatus{
		"success": core.StatusSuccess,
		"failure": core.StatusFailure,
		"unknown": core.StatusPending,
		"":        core.StatusPending,
	}
	for input, want := range cases {
		if got := mapStatus(input); got != want {
			t.Errorf("mapStatus(%q) = %q, want %q", input, got, want)
		}
	}
}

const installmentBody = `{
	"status": "success",
	"locale": "tr",
	"systemTime": 123456789,
	"conversationId": "test-conversation-123",
	"installmentDetails": [
		{
			"binNumber": "552879",
			"price": 100,
			"cardType": "CREDIT_CARD",
			"cardAssociation": "MASTER_CARD",
			"cardFamilyName": "Bonus",
			"force3ds": 0,
			"bankCode": 46,
			"bankName": "Akbank",
			"forceCvc": 0,
			"commercial": 0,
			"installmentPrices": [
				{ "installmentNumber": 1, "totalPrice": 100, "installmentPrice": 100 },
				{ "installmentNumber": 2, "totalPrice": 102, "installmentPrice": 51 },
				{ "installmentNumber": 3, "totalPrice": 104.5, "installmentPrice": 34.83 }
			]
		}
	]
}`

func TestInstallmentInfoReturnsDetails(t *testing.T) {
	p := newTestProvider(t, &stubTransport{body: installmentBody})

	result, err := p.InstallmentInfo(context.Background(), &core.InstallmentInfoRequest{
		BinNumber: "552879",
		Price:     "100.00",
	})
	if err != nil {
		t.Fatalf("InstallmentInfo() error = %v", err)
	}
	if result.Status != core.StatusSuccess {
		t.Fatalf("status = %q, want success", result.Status)
	}
	if len(result.InstallmentDetails) != 1 {
		t.Fatalf("installmentDetails = %d, want 1", len(result.InstallmentDetails))
	}
	detail := result.InstallmentDetails[0]
	if detail.BinNumber != "552879" || detail.BankName != "Akbank" || detail.CardFamilyName != "Bonus" {
		t.Errorf("detail = %+v", detail)
	}
	if len(detail.InstallmentPrices) != 3 {
		t.Fatalf("installmentPrices = %d, want 3", len(detail.InstallmentPrices))
	}
	if detail.InstallmentPrices[0].InstallmentNumber != 1 || detail.InstallmentPrices[0].TotalPrice != 100 {
		t.Errorf("first installment = %+v", detail.InstallmentPrices[0])
	}
	if detail.InstallmentPrices[1].InstallmentNumber != 2 ||
		detail.InstallmentPrices[1].TotalPrice != 102 ||
		detail.InstallmentPrices[1].InstallmentPrice != 51 {
		t.Errorf("second installment = %+v", detail.InstallmentPrices[1])
	}
}

func TestInstallmentInfoHandlesMultipleBanks(t *testing.T) {
	p := newTestProvider(t, &stubTransport{body: `{
		"status": "success",
		"installmentDetails": [
			{ "binNumber": "552879", "price": 100, "cardFamilyName": "Bonus", "bankCode": 46, "bankName": "Akbank",
			  "installmentPrices": [ { "installmentNumber": 1, "totalPrice": 100, "installmentPrice": 100 },
			                         { "installmentNumber": 2, "totalPrice": 102, "installmentPrice": 51 } ] },
			{ "binNumber": "552879", "price": 100, "cardFamilyName": "Maximum", "bankCode": 64, "bankName": "İş Bankası",
			  "installmentPrices": [ { "installmentNumber": 1, "totalPrice": 100, "installmentPrice": 100 },
			                         { "installmentNumber": 3, "totalPrice": 105, "installmentPrice": 35 } ] }
		]
	}`})

	result, err := p.InstallmentInfo(context.Background(), &core.InstallmentInfoRequest{BinNumber: "552879", Price: "100.00"})
	if err != nil {
		t.Fatalf("InstallmentInfo() error = %v", err)
	}
	if result.Status != core.StatusSuccess || len(result.InstallmentDetails) != 2 {
		t.Fatalf("result = %+v", result)
	}
	if result.InstallmentDetails[0].BankName != "Akbank" || result.InstallmentDetails[1].BankName != "İş Bankası" {
		t.Errorf("bank names = %q, %q", result.InstallmentDetails[0].BankName, result.InstallmentDetails[1].BankName)
	}
}

func TestInstallmentInfoHandlesCommercialCards(t *testing.T) {
	p := newTestProvider(t, &stubTransport{body: `{
		"status": "success",
		"installmentDetails": [
			{ "binNumber": "552879", "price": 100, "cardFamilyName": "Business", "bankCode": 46,
			  "bankName": "Akbank", "commercial": 1, "force3ds": 1,
			  "installmentPrices": [ { "installmentNumber": 1, "totalPrice": 100, "installmentPrice": 100 } ] }
		]
	}`})

	result, err := p.InstallmentInfo(context.Background(), &core.InstallmentInfoRequest{BinNumber: "552879", Price: "100.00"})
	if err != nil {
		t.Fatalf("InstallmentInfo() error = %v", err)
	}
	detail := result.InstallmentDetails[0]
	if detail.Commercial != 1 {
		t.Errorf("commercial = %d, want 1", detail.Commercial)
	}
	if detail.Force3DS == nil || *detail.Force3DS != 1 {
		t.Errorf("force3ds = %v, want 1", detail.Force3DS)
	}
}

func TestInstallmentInfoHandlesQueryFailure(t *testing.T) {
	p := newTestProvider(t, &stubTransport{body: `{
		"status": "failure", "errorCode": "5001", "errorMessage": "Invalid BIN number"
	}`})

	result, err := p.InstallmentInfo(context.Background(), &core.InstallmentInfoRequest{BinNumber: "000000", Price: "100.00"})
	if err != nil {
		t.Fatalf("InstallmentInfo() error = %v", err)
	}
	if result.Status != core.StatusFailure || result.ErrorMessage != "Invalid BIN number" || result.ErrorCode != "5001" {
		t.Errorf("result = %+v", result)
	}
	if result.InstallmentDetails != nil {
		t.Errorf("installmentDetails = %#v, want nil", result.InstallmentDetails)
	}
}

func TestInstallmentInfoSendsConversationID(t *testing.T) {
	transport := &stubTransport{body: `{
		"status": "success", "conversationId": "test-conv-123", "installmentDetails": []
	}`}
	p := newTestProvider(t, transport)

	if _, err := p.InstallmentInfo(context.Background(), &core.InstallmentInfoRequest{
		BinNumber:      "552879",
		Price:          "100.00",
		ConversationID: "test-conv-123",
	}); err != nil {
		t.Fatalf("InstallmentInfo() error = %v", err)
	}

	if got := transport.lastRequest().URL.Path; got != "/payment/iyzipos/installment" {
		t.Errorf("path = %q", got)
	}
	fields := transport.lastFields(t)
	for key, want := range map[string]any{
		"conversationId": "test-conv-123",
		"binNumber":      "552879",
		"price":          "100.00",
		"locale":         "tr",
	} {
		if fields[key] != want {
			t.Errorf("body[%q] = %#v, want %#v", key, fields[key], want)
		}
	}
}

func TestInstallmentInfoNonTransportErrorIsFailure(t *testing.T) {
	p := newTestProvider(t, &stubTransport{})

	result := p.failInstallmentInfo(errors.New("Network error"))
	if result.Status != core.StatusFailure || result.ErrorMessage != "Network error" {
		t.Errorf("result = %+v", result)
	}
}

func TestInstallmentInfoTransportErrorIsPending(t *testing.T) {
	p := newTestProvider(t, &stubTransport{err: errors.New("connection reset")})

	result, err := p.InstallmentInfo(context.Background(), &core.InstallmentInfoRequest{BinNumber: "552879", Price: "100.00"})
	if err != nil {
		t.Fatalf("InstallmentInfo() error = %v", err)
	}
	if result.Status != core.StatusPending || result.ErrorCode != core.NetworkErrorCode {
		t.Errorf("result = %+v, want pending/NETWORK_ERROR", result)
	}
	if result.Code != core.CodeNetworkError {
		t.Errorf("code = %q, want %q", result.Code, core.CodeNetworkError)
	}
}
