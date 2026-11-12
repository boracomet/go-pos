package paytr

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"strconv"
	"strings"
	"testing"

	"github.com/boracomet/go-pos/core"
)

// Characters that must survive both URL encoding and HTML attributes, including
// a literal entity that must not be decoded twice.
const (
	basketProductName = `Türkçe "çay" & <ürün> 'özel' &quot;`
	basketProductJSON = `Türkçe \"çay\" & <ürün> 'özel' &quot;`
)

const (
	basketMerchantKey  = "test-key"
	basketMerchantSalt = "test-salt"
)

// core.BasketItem carries no quantity, so order lines coming from a
// core.PaymentRequest are always a single unit. Only the provider-local
// TokenPaymentRequest can express quantity > 1.
func expectedBasketJSON(firstQuantity int) string {
	return `[["` + basketProductJSON + `","0.30",` + strconv.Itoa(firstQuantity) +
		`],["Game code","0.50",1],["Usb","0.20",1]]`
}

func expectedBasketRows(firstQuantity int) [][]any {
	return [][]any{
		{basketProductName, "0.30", float64(firstQuantity)},
		{"Game code", "0.50", float64(1)},
		{"Usb", "0.20", float64(1)},
	}
}

func basketRequest() core.ThreeDSPaymentRequest {
	req := mockThreeDSRequest()
	req.ConversationID = "BASKET1"
	req.BasketItems = mockBasket()
	req.BasketItems[0].Name = basketProductName
	return req
}

func basketTokenRequest(quantity int) *TokenPaymentRequest {
	req := basketRequest()
	return &TokenPaymentRequest{
		Utoken:         "U1",
		Ctoken:         "C1",
		Price:          req.PaidPrice,
		CallbackURL:    req.CallbackURL,
		ConversationID: req.ConversationID,
		Buyer: TokenPaymentBuyer{
			Email:     req.Buyer.Email,
			Name:      req.Buyer.Name,
			Surname:   req.Buyer.Surname,
			IP:        req.Buyer.IP,
			GSMNumber: req.Buyer.GSMNumber,
		},
		BasketItems: []BasketItem{
			{Name: basketProductName, Price: "0.3", Quantity: quantity},
			{Name: "Game code", Price: "0.5", Quantity: 1},
			{Name: "Usb", Price: "0.2", Quantity: 1},
		},
	}
}

func newBasketProvider(t *testing.T, transport core.Doer, mode string) *Provider {
	t.Helper()
	testMode := true
	return newProvider(t, Config{
		ProviderConfig: core.ProviderConfig{BaseURL: DefaultBaseURL, Doer: transport},
		MerchantID:     "123456",
		MerchantKey:    basketMerchantKey,
		MerchantSalt:   basketMerchantSalt,
		TestMode:       &testMode,
		ThreeDSMode:    mode,
	})
}

func expectJSONBasket(t *testing.T, value string, firstQuantity int) {
	t.Helper()
	if want := expectedBasketJSON(firstQuantity); value != want {
		t.Fatalf("user_basket = %s, want %s", value, want)
	}
	var rows [][]any
	if err := json.Unmarshal([]byte(value), &rows); err != nil {
		t.Fatalf("user_basket is not JSON: %v", err)
	}
	want := expectedBasketRows(firstQuantity)
	if len(rows) != len(want) {
		t.Fatalf("basket rows = %v, want %v", rows, want)
	}
	for i := range want {
		for j := range want[i] {
			if rows[i][j] != want[i][j] {
				t.Fatalf("basket rows = %v, want %v", rows, want)
			}
		}
	}
}

func TestDirectThreeDSFormPostsJSONBasket(t *testing.T) {
	transport := &stubTransport{queue: []stubResponse{jsonBody(map[string]any{"status": "success", "token": "IFRAME1"})}}
	p := newBasketProvider(t, transport, ModeDirect)

	req := basketRequest()
	result, err := p.InitThreeDSPayment(context.Background(), &req)
	if err != nil {
		t.Fatalf("InitThreeDSPayment: %v", err)
	}
	if result.Status != core.StatusPending {
		t.Fatalf("status = %q, want pending", result.Status)
	}
	html := result.ThreeDSHTMLContent
	if !strings.Contains(html, `action="https://www.paytr.com/odeme"`) {
		t.Fatalf("form action missing: %s", html)
	}
	if !strings.Contains(html, "&lt;ürün&gt;") {
		t.Fatal("product name is not HTML-escaped in the form")
	}
	expectJSONBasket(t, formFields(t, html)["user_basket"], 1)
	if transport.calls() != 0 {
		t.Fatalf("calls = %d, want 0", transport.calls())
	}
}

func TestTokenPaymentFormKeepsQuantity(t *testing.T) {
	transport := &stubTransport{queue: []stubResponse{jsonBody(map[string]any{"status": "success", "token": "IFRAME1"})}}
	p := newBasketProvider(t, transport, "")

	result, err := p.CreatePaymentWithToken(context.Background(), basketTokenRequest(2))
	if err != nil {
		t.Fatalf("CreatePaymentWithToken: %v", err)
	}
	if result.Status != core.StatusPending {
		t.Fatalf("status = %q, want pending", result.Status)
	}
	expectJSONBasket(t, formFields(t, result.ThreeDSHTMLContent)["user_basket"], 2)
	if transport.calls() != 0 {
		t.Fatalf("calls = %d, want 0", transport.calls())
	}
}

func TestUnifiedThreeDSWithStoredCardUsesDirectJSON(t *testing.T) {
	transport := &stubTransport{queue: []stubResponse{jsonBody(map[string]any{"status": "success", "token": "IFRAME1"})}}
	p := newBasketProvider(t, transport, "")

	req := basketRequest()
	req.PaymentCard = nil
	req.StoredCard = &core.StoredCardReference{CustomerToken: "U1", CardToken: "C1"}
	result, err := p.InitThreeDSPayment(context.Background(), &req)
	if err != nil {
		t.Fatalf("InitThreeDSPayment: %v", err)
	}
	if result.Status != core.StatusPending {
		t.Fatalf("status = %q, want pending", result.Status)
	}
	expectJSONBasket(t, formFields(t, result.ThreeDSHTMLContent)["user_basket"], 1)
	if transport.calls() != 0 {
		t.Fatalf("calls = %d, want 0", transport.calls())
	}
}

func TestNon3DPaymentsSendJSONBasket(t *testing.T) {
	for _, card := range []string{"new", "stored"} {
		t.Run(card, func(t *testing.T) {
			transport := &stubTransport{queue: []stubResponse{jsonBody(map[string]any{"status": "success", "merchant_oid": "BASKET1"})}}
			p := newBasketProvider(t, transport, "")

			req := basketRequest()
			if card == "stored" {
				req.PaymentCard = nil
				req.StoredCard = &core.StoredCardReference{CustomerToken: "U1", CardToken: "C1"}
			}
			result, err := p.CreatePayment(context.Background(), &req.PaymentRequest)
			if err != nil {
				t.Fatalf("CreatePayment: %v", err)
			}
			if result.Status != core.StatusSuccess {
				t.Fatalf("status = %q, want success", result.Status)
			}
			if transport.calls() != 1 {
				t.Fatalf("calls = %d, want 1", transport.calls())
			}
			if path := transport.path(0); path != "/odeme" {
				t.Fatalf("path = %q", path)
			}
			expectJSONBasket(t, sentForm(t, transport, 0)["user_basket"], 1)
		})
	}
}

func TestIframeModePreservesBase64BasketAndSignature(t *testing.T) {
	for _, mode := range []string{"", ModeIframe} {
		t.Run("mode="+mode, func(t *testing.T) {
			transport := &stubTransport{queue: []stubResponse{jsonBody(map[string]any{"status": "success", "token": "IFRAME1"})}}
			p := newBasketProvider(t, transport, mode)

			req := basketRequest()
			result, err := p.InitThreeDSPayment(context.Background(), &req)
			if err != nil {
				t.Fatalf("InitThreeDSPayment: %v", err)
			}
			if result.Status != core.StatusPending {
				t.Fatalf("status = %q, want pending", result.Status)
			}
			if transport.calls() != 1 {
				t.Fatalf("calls = %d, want 1", transport.calls())
			}
			if path := transport.path(0); path != "/odeme/api/get-token" {
				t.Fatalf("path = %q", path)
			}
			form := sentForm(t, transport, 0)
			expectedBase64 := base64.StdEncoding.EncodeToString([]byte(expectedBasketJSON(1)))
			if form["user_basket"] != expectedBase64 {
				t.Fatalf("user_basket = %q, want %q", form["user_basket"], expectedBase64)
			}
			raw, err := base64.StdEncoding.DecodeString(form["user_basket"])
			if err != nil {
				t.Fatalf("user_basket is not base64: %v", err)
			}
			expectJSONBasket(t, string(raw), 1)

			// Independent reference calculation: sign the same base64 string sent
			// to PayTR, with the merchant key as the HMAC key.
			signatureInput := "123456" + req.Buyer.IP + req.ConversationID + req.Buyer.Email +
				"120" + expectedBase64 + "0" + "0" + "TL" + "1" + basketMerchantSalt
			mac := hmac.New(sha256.New, []byte(basketMerchantKey))
			mac.Write([]byte(signatureInput))
			want := base64.StdEncoding.EncodeToString(mac.Sum(nil))
			if !core.SafeEqual(form["paytr_token"], want) {
				t.Fatalf("paytr_token = %q, want %q", form["paytr_token"], want)
			}
		})
	}
}
