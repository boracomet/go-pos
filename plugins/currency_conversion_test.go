package plugins

import (
	"context"
	"io"
	"math"
	"net/http"
	"strings"
	"sync"
	"testing"

	"github.com/boracomet/go-pos/core"
)

const currencyConversionTestXML = `<?xml version="1.0" encoding="UTF-8"?>
<Tarih_Date Tarih="18.10.2026" Date="10/18/2026">
  <Currency CrossOrder="0" Kod="USD" CurrencyCode="USD">
    <Unit>1</Unit>
    <Isim>ABD DOLARI</Isim>
    <CurrencyName>US DOLLAR</CurrencyName>
    <ForexBuying>34.0000</ForexBuying>
    <ForexSelling>34.5000</ForexSelling>
    <BanknoteBuying>33.8000</BanknoteBuying>
    <BanknoteSelling>34.7000</BanknoteSelling>
  </Currency>
  <Currency CrossOrder="1" Kod="EUR" CurrencyCode="EUR">
    <Unit>1</Unit>
    <Isim>EURO</Isim>
    <CurrencyName>EURO</CurrencyName>
    <ForexBuying>37.0000</ForexBuying>
    <ForexSelling>37.5000</ForexSelling>
    <BanknoteBuying>36.8000</BanknoteBuying>
    <BanknoteSelling>37.8000</BanknoteSelling>
  </Currency>
</Tarih_Date>`

// currencyConversionTestDoer answers every request with the current XML document
// and counts the calls, so a test can prove the rate cache was used.
type currencyConversionTestDoer struct {
	mu    sync.Mutex
	xml   string
	calls int
}

func currencyConversionTestFetch() *currencyConversionTestDoer {
	return &currencyConversionTestDoer{xml: currencyConversionTestXML}
}

func (d *currencyConversionTestDoer) Do(_ *http.Request) (*http.Response, error) {
	d.mu.Lock()
	d.calls++
	document := d.xml
	d.mu.Unlock()
	return &http.Response{
		StatusCode: http.StatusOK,
		Header:     http.Header{},
		Body:       io.NopCloser(strings.NewReader(document)),
	}, nil
}

func (d *currencyConversionTestDoer) count() int {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.calls
}

func (d *currencyConversionTestDoer) setXML(document string) {
	d.mu.Lock()
	d.xml = document
	d.mu.Unlock()
}

type currencyConversionTestFx struct {
	quote      func(FxQuoteRequest) (*FxQuoteResponse, error)
	getRate    func(from, to string) (float64, error)
	clearCache func()
}

func currencyConversionTestFxOf(t *testing.T, facade *failoverTestFacade) currencyConversionTestFx {
	t.Helper()
	member, ok := facade.method(t, "fx").(map[string]any)
	if !ok {
		t.Fatalf("fx member has type %T", facade.method(t, "fx"))
	}
	out := currencyConversionTestFx{}
	if quote, ok := member["quote"].(func(FxQuoteRequest) (*FxQuoteResponse, error)); ok {
		out.quote = quote
	} else {
		t.Fatalf("quote has type %T", member["quote"])
	}
	if getRate, ok := member["getRate"].(func(string, string) (float64, error)); ok {
		out.getRate = getRate
	} else {
		t.Fatalf("getRate has type %T", member["getRate"])
	}
	if clearCache, ok := member["clearCache"].(func()); ok {
		out.clearCache = clearCache
	} else {
		t.Fatalf("clearCache has type %T", member["clearCache"])
	}
	return out
}

func currencyConversionTestSetup(t *testing.T, options CurrencyConversionOptions) (*failoverTestFacade, *failoverTestProvider) {
	t.Helper()
	plugin, err := CurrencyConversion(options)
	if err != nil {
		t.Fatalf("currencyConversion: %v", err)
	}
	return failoverTestSingle(t, plugin, nil)
}

// currencyConversionTestDirect runs a non-routable provider call, the way
// payment.<provider>.<operation> does, and keeps the plugin's result metadata.
func currencyConversionTestDirect(t *testing.T, facade *failoverTestFacade, operation core.PaymentOperation, request any) map[string]any {
	t.Helper()
	result, err := facade.Run(context.Background(), operation, "mock", request, core.RunOptions{Routable: false})
	if err != nil {
		t.Fatalf("%s: %v", operation, err)
	}
	return failoverTestRecord(t, result)
}

func currencyConversionTestWantNumber(t *testing.T, label string, got, want float64) {
	t.Helper()
	if got != want {
		t.Errorf("%s = %v, want %v", label, got, want)
	}
}

func currencyConversionTestWantClose(t *testing.T, label string, got, want, precision float64) {
	t.Helper()
	if math.Abs(got-want) >= math.Pow(10, -precision)/2 {
		t.Errorf("%s = %v, want %v (within %v)", label, got, want, precision)
	}
}

func currencyConversionTestQuote(t *testing.T, fx currencyConversionTestFx, amount any, from, to string) *FxQuoteResponse {
	t.Helper()
	quote, err := fx.quote(FxQuoteRequest{Amount: amount, From: from, To: to})
	if err != nil {
		t.Fatalf("quote: %v", err)
	}
	return quote
}

func currencyConversionTestItem(id, name, price, category string) core.BasketItem {
	return core.BasketItem{
		ID: id, Name: name, Price: price, Category1: category,
		ItemType: string(core.BasketItemPhysical),
	}
}

func TestCurrencyConversion(t *testing.T) {
	t.Run("parses TCMB XML and calculates exchange rates and fx quotes", func(t *testing.T) {
		fetch := currencyConversionTestFetch()
		facade, _ := currencyConversionTestSetup(t, CurrencyConversionOptions{
			Doer: fetch, Rate: TcmbForexSelling,
		})
		fx := currencyConversionTestFxOf(t, facade)

		quote := currencyConversionTestQuote(t, fx, 100, "USD", "TRY")
		currencyConversionTestWantNumber(t, "amount", quote.Amount, 100)
		currencyConversionTestWantNumber(t, "convertedAmount", quote.ConvertedAmount, 3450)
		currencyConversionTestWantNumber(t, "exchangeRate", quote.ExchangeRate, 34.5)
		if quote.From != "USD" || quote.To != "TRY" {
			t.Errorf("quote = %s -> %s, want USD -> TRY", quote.From, quote.To)
		}
		if fetch.count() != 1 {
			t.Errorf("fetch calls = %d, want 1", fetch.count())
		}

		rate, err := fx.getRate("USD", "TRY")
		if err != nil {
			t.Fatalf("getRate: %v", err)
		}
		currencyConversionTestWantNumber(t, "cached rate", rate, 34.5)
		if fetch.count() != 1 {
			t.Errorf("fetch calls = %d, want 1", fetch.count())
		}
	})

	t.Run("supports markup percentage margin", func(t *testing.T) {
		fetch := currencyConversionTestFetch()
		facade, _ := currencyConversionTestSetup(t, CurrencyConversionOptions{
			Doer: fetch, Rate: TcmbForexSelling, Markup: 0.01,
		})
		quote := currencyConversionTestQuote(t, currencyConversionTestFxOf(t, facade), 100, "USD", "TRY")
		currencyConversionTestWantClose(t, "exchangeRate", quote.ExchangeRate, 34.845, 4)
		currencyConversionTestWantNumber(t, "convertedAmount", quote.ConvertedAmount, 3484.5)
	})

	t.Run("converts a payment request when the provider does not accept its currency", func(t *testing.T) {
		fetch := currencyConversionTestFetch()
		facade, _ := currencyConversionTestSetup(t, CurrencyConversionOptions{
			Doer: fetch, Target: map[string][]string{"mock": {"TRY"}},
		})
		request := failoverTestRequest()
		request.Price = "50.00"
		request.PaidPrice = "50.00"
		request.Currency = string(core.CurrencyUSD)
		request.BasketItems = []core.BasketItem{
			currencyConversionTestItem("1", "Item 1", "20.00", "C1"),
			currencyConversionTestItem("2", "Item 2", "30.00", "C2"),
		}
		request.ConversationID = "order-conv-123"

		result := currencyConversionTestDirect(t, facade, core.OperationCreatePayment, request)
		if status := failoverTestText(result, "status"); status != string(core.StatusSuccess) {
			t.Errorf("status = %q, want success", status)
		}
		if got := failoverTestText(result, "originalAmount"); got != "50.00" {
			t.Errorf("originalAmount = %q, want 50.00", got)
		}
		if got := failoverTestText(result, "originalCurrency"); got != "USD" {
			t.Errorf("originalCurrency = %q, want USD", got)
		}
		currencyConversionTestWantNumber(t, "exchangeRate", failoverTestNumber(result, "exchangeRate"), 34.5)
	})

	t.Run("skips conversion when the provider already supports the currency", func(t *testing.T) {
		fetch := currencyConversionTestFetch()
		facade, _ := currencyConversionTestSetup(t, CurrencyConversionOptions{
			Doer: fetch, Target: map[string][]string{"mock": {"USD", "TRY"}},
		})
		request := failoverTestRequest()
		request.Price = "50.00"
		request.PaidPrice = "50.00"
		request.Currency = string(core.CurrencyUSD)

		result := currencyConversionTestDirect(t, facade, core.OperationCreatePayment, request)
		if status := failoverTestText(result, "status"); status != string(core.StatusSuccess) {
			t.Errorf("status = %q, want success", status)
		}
		if value, present := result["originalCurrency"]; present && value != nil {
			t.Errorf("originalCurrency = %v, want absent", value)
		}
		if fetch.count() != 0 {
			t.Errorf("fetch calls = %d, want 0", fetch.count())
		}
	})

	t.Run("preserves the original exchange rate during refunds", func(t *testing.T) {
		fetch := currencyConversionTestFetch()
		facade, _ := currencyConversionTestSetup(t, CurrencyConversionOptions{
			Doer: fetch, Target: map[string][]string{"mock": {"TRY"}},
		})
		fx := currencyConversionTestFxOf(t, facade)

		request := failoverTestRequest()
		request.Price = "100.00"
		request.PaidPrice = "100.00"
		request.Currency = string(core.CurrencyUSD)
		request.ConversationID = "order-refund-test"
		paid := currencyConversionTestDirect(t, facade, core.OperationCreatePayment, request)
		if status := failoverTestText(paid, "status"); status != string(core.StatusSuccess) {
			t.Fatalf("payment status = %q, want success", status)
		}

		fx.clearCache()
		fetch.setXML(strings.Replace(currencyConversionTestXML,
			"<ForexSelling>34.5000</ForexSelling>", "<ForexSelling>40.0000</ForexSelling>", 1))

		refund := failoverTestRefundRequest()
		refund.PaymentID = failoverTestText(paid, "paymentId")
		refund.Price = "50.00"
		refund.Currency = string(core.CurrencyUSD)
		refund.ConversationID = "order-refund-test"
		result := currencyConversionTestDirect(t, facade, core.OperationRefund, refund)
		if status := failoverTestText(result, "status"); status != string(core.StatusSuccess) {
			t.Fatalf("refund status = %q, want success", status)
		}
		currencyConversionTestWantNumber(t, "exchangeRate", failoverTestNumber(result, "exchangeRate"), 34.5)
		if got := failoverTestText(result, "originalAmount"); got != "50.00" {
			t.Errorf("originalAmount = %q, want 50.00", got)
		}
		if got := failoverTestText(result, "originalCurrency"); got != "USD" {
			t.Errorf("originalCurrency = %q, want USD", got)
		}
	})

	t.Run("supports a custom rate source function", func(t *testing.T) {
		facade, _ := currencyConversionTestSetup(t, CurrencyConversionOptions{
			SourceFunc: func(from, to string) (float64, error) {
				if from == "EUR" && to == "TRY" {
					return 36.8, nil
				}
				return 1, nil
			},
			Target: map[string][]string{"mock": {"TRY"}},
		})
		quote := currencyConversionTestQuote(t, currencyConversionTestFxOf(t, facade), 10, "EUR", "TRY")
		currencyConversionTestWantNumber(t, "convertedAmount", quote.ConvertedAmount, 368)
		currencyConversionTestWantNumber(t, "exchangeRate", quote.ExchangeRate, 36.8)
	})

	t.Run("calculates cross rates between foreign currencies", func(t *testing.T) {
		fetch := currencyConversionTestFetch()
		facade, _ := currencyConversionTestSetup(t, CurrencyConversionOptions{
			Doer: fetch, Rate: TcmbForexSelling,
		})
		quote := currencyConversionTestQuote(t, currencyConversionTestFxOf(t, facade), 100, "EUR", "USD")
		currencyConversionTestWantNumber(t, "convertedAmount", quote.ConvertedAmount, 108.7)
		currencyConversionTestWantClose(t, "exchangeRate", quote.ExchangeRate, 37.5/34.5, 4)
	})

	t.Run("preserves kurus sum equality across converted basket items", func(t *testing.T) {
		fetch := currencyConversionTestFetch()
		facade, provider := currencyConversionTestSetup(t, CurrencyConversionOptions{
			Doer: fetch, Target: map[string][]string{"mock": {"TRY"}},
		})
		request := failoverTestRequest()
		request.Price = "10.00"
		request.PaidPrice = "10.00"
		request.Currency = string(core.CurrencyUSD)
		request.BasketItems = []core.BasketItem{
			currencyConversionTestItem("1", "Item 1", "3.33", "C1"),
			currencyConversionTestItem("2", "Item 2", "3.33", "C2"),
			currencyConversionTestItem("3", "Item 3", "3.34", "C3"),
		}
		currencyConversionTestDirect(t, facade, core.OperationCreatePayment, request)

		called, ok := provider.lastRequest(core.OperationCreatePayment).(*core.PaymentRequest)
		if !ok || called == nil {
			t.Fatalf("provider received %T", provider.lastRequest(core.OperationCreatePayment))
		}
		if called.Currency != "TRY" {
			t.Errorf("currency = %q, want TRY", called.Currency)
		}
		if called.Price != "345.00" {
			t.Errorf("price = %q, want 345.00", called.Price)
		}
		sum := 0.0
		for _, item := range called.BasketItems {
			amount, err := failoverTestParseAmount(item.Price)
			if err != nil {
				t.Fatalf("basket price %q: %v", item.Price, err)
			}
			sum += amount
		}
		currencyConversionTestWantClose(t, "basket sum", sum, 345.0, 2)
	})

	t.Run("supports banknote rate types", func(t *testing.T) {
		fetch := currencyConversionTestFetch()
		facade, _ := currencyConversionTestSetup(t, CurrencyConversionOptions{
			Doer: fetch, Rate: TcmbBanknoteSelling,
		})
		quote := currencyConversionTestQuote(t, currencyConversionTestFxOf(t, facade), 100, "USD", "TRY")
		currencyConversionTestWantNumber(t, "exchangeRate", quote.ExchangeRate, 34.7)
		currencyConversionTestWantNumber(t, "convertedAmount", quote.ConvertedAmount, 3470)
	})
}
