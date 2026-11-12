package parampos

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/boracomet/go-pos/core"
)

const testGUID = "0c13d406-873b-403b-9c09-a5766840d98c"

type stubCall struct {
	URL    string
	Path   string
	Body   string
	Header http.Header
}

func (c stubCall) soapAction() string { return c.Header.Get("SOAPAction") }

type stubResponse struct {
	status int
	body   string
	err    error
}

// stubDoer replaces the transport: it records every request and answers from a
// queue, or per SOAPAction when a test needs different answers per operation.
type stubDoer struct {
	calls    []stubCall
	queue    []stubResponse
	byAction map[string][]stubResponse
}

func (s *stubDoer) Do(req *http.Request) (*http.Response, error) {
	body := ""
	if req.Body != nil {
		raw, err := io.ReadAll(req.Body)
		if err != nil {
			return nil, err
		}
		body = string(raw)
	}
	call := stubCall{URL: req.URL.String(), Path: req.URL.Path, Body: body, Header: req.Header.Clone()}
	s.calls = append(s.calls, call)

	var next stubResponse
	action := call.soapAction()
	if queue := s.byAction[action]; len(queue) > 0 {
		next, s.byAction[action] = queue[0], queue[1:]
	} else if len(s.queue) > 0 {
		next, s.queue = s.queue[0], s.queue[1:]
	}
	if next.err != nil {
		return nil, next.err
	}
	status := next.status
	if status == 0 {
		status = http.StatusOK
	}
	return &http.Response{
		StatusCode: status,
		Body:       io.NopCloser(strings.NewReader(next.body)),
		Header:     http.Header{"Content-Type": []string{"text/xml; charset=utf-8"}},
	}, nil
}

func (s *stubDoer) respond(bodies ...string) {
	for _, body := range bodies {
		s.queue = append(s.queue, stubResponse{body: body})
	}
}

func (s *stubDoer) respondAction(action, body string) {
	if s.byAction == nil {
		s.byAction = map[string][]stubResponse{}
	}
	s.byAction[Namespace+action] = append(s.byAction[Namespace+action], stubResponse{body: body})
}

func (s *stubDoer) body(call int) string {
	if call >= len(s.calls) {
		return ""
	}
	return s.calls[call].Body
}

func timeoutError() error {
	return fmt.Errorf("request timed out: %w", context.DeadlineExceeded)
}

func newTestProvider(t *testing.T, doer core.Doer) *Provider {
	t.Helper()
	provider, err := New(Config{
		ProviderConfig: core.ProviderConfig{
			BaseURL: SandboxURL,
			Doer:    doer,
			Retry:   &core.RetryConfig{Attempts: 3, Delay: time.Millisecond},
		},
		ClientCode:     "10738",
		ClientUsername: "Test",
		ClientPassword: "Test",
		GUID:           testGUID,
	})
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	return provider
}

// soapResponse builds a response envelope in the format the service answers with.
func soapResponse(action string, fields Fields) string {
	return `<?xml version="1.0" encoding="utf-8"?>` +
		`<soap:Envelope xmlns:soap="http://schemas.xmlsoap.org/soap/envelope/"><soap:Body>` +
		`<` + action + `Response xmlns="` + Namespace + `">` +
		`<` + action + `Result>` + BuildXMLFields(fields) + `</` + action + `Result>` +
		`</` + action + `Response>` +
		`</soap:Body></soap:Envelope>`
}

func fields(pairs ...string) Fields {
	out := make(Fields, 0, len(pairs)/2)
	for i := 0; i+1 < len(pairs); i += 2 {
		out = append(out, NewField(pairs[i], pairs[i+1]))
	}
	return out
}

func mockPaymentRequest() *core.PaymentRequest {
	registerCard := true
	return &core.PaymentRequest{
		Price:     "1",
		PaidPrice: "1.2",
		Currency:  "TRY",
		BasketID:  "B67832",
		PaymentCard: &core.PaymentCard{
			CardHolderName: "John Doe",
			CardNumber:     "5528790000000008",
			ExpireMonth:    "12",
			ExpireYear:     "2030",
			CVC:            "123",
			RegisterCard:   &registerCard,
		},
		Buyer: core.Buyer{
			ID:                  "BY789",
			Name:                "John",
			Surname:             "Doe",
			GSMNumber:           "+905350000000",
			Email:               "john.doe@example.com",
			IdentityNumber:      "74300864791",
			RegistrationAddress: "Nidakule Göztepe, Merdivenköy Mah. Bora Sok. No:1",
			IP:                  "85.34.78.112",
			City:                "Istanbul",
			Country:             "Turkey",
			ZipCode:             "34732",
		},
		ShippingAddress: mockAddress("Jane Doe"),
		BillingAddress:  mockAddress("John Doe"),
		BasketItems: []core.BasketItem{
			{ID: "BI101", Name: "Binocular", Category1: "Collectibles", Category2: "Accessories", ItemType: "PHYSICAL", Price: "0.3"},
			{ID: "BI102", Name: "Game code", Category1: "Game", Category2: "Online Game Items", ItemType: "VIRTUAL", Price: "0.5"},
			{ID: "BI103", Name: "Usb", Category1: "Electronics", Category2: "Usb / Cable", ItemType: "PHYSICAL", Price: "0.2"},
		},
		ConversationID: "123456789",
	}
}

func mockAddress(contactName string) core.Address {
	return core.Address{
		ContactName: contactName,
		City:        "Istanbul",
		Country:     "Turkey",
		Address:     "Nidakule Göztepe, Merdivenköy Mah. Bora Sok. No:1",
		ZipCode:     "34732",
	}
}

func mockThreeDSPaymentRequest() *core.ThreeDSPaymentRequest {
	req := &core.ThreeDSPaymentRequest{
		PaymentRequest: *mockPaymentRequest(),
	}
	req.CallbackURL = "https://example.com/callback"
	return req
}

func referenceCallback() map[string]string {
	return map[string]string{
		"islemGUID": "35513153-9902-4a4c-a256-af6ed9cadc52",
		"md":        "581877:A65A349B0BAE27FC6567294215158DD8AE223843B5C96462F04A750CA7E8B165:3680:##500100000",
		"mdStatus":  "1",
		"orderId":   "2025011749D1",
		"islemHash": "b1D7+nI3j4k3WGJuhW5IuPOFpEE=",
	}
}
