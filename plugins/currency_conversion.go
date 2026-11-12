package plugins

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"net/http"
	"reflect"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/boracomet/go-pos/core"
)

// TcmbRateType selects which published TCMB rate a conversion uses.
type TcmbRateType string

const (
	// TcmbForexSelling is the döviz satış rate, the default.
	TcmbForexSelling TcmbRateType = "forex-selling"
	// TcmbForexBuying is the döviz alış rate.
	TcmbForexBuying TcmbRateType = "forex-buying"
	// TcmbBanknoteSelling is the efektif satış rate.
	TcmbBanknoteSelling TcmbRateType = "banknote-selling"
	// TcmbBanknoteBuying is the efektif alış rate.
	TcmbBanknoteBuying TcmbRateType = "banknote-buying"
)

// CurrencyConversionCacheOptions configures the exchange-rate cache.
type CurrencyConversionCacheOptions struct {
	// TTL is the cache lifetime in milliseconds, or a string such as "1h". Default: "1h".
	TTL any
}

// CurrencyConversionOptions configures the currency conversion plugin.
type CurrencyConversionOptions struct {
	// Source is the rate source name; "tcmb" (the default) reads the daily TCMB XML.
	Source string
	// SourceFunc supplies rates directly and wins over Source when set. A rate of
	// zero or less means "no rate" and fails the conversion.
	SourceFunc func(from, to string) (float64, error)
	// Rate selects the TCMB rate. Default: TcmbForexSelling.
	Rate TcmbRateType
	// Markup is a margin added to the rate, e.g. 0.01 for +1%.
	Markup float64
	// Target maps a provider id to the currencies it accepts. A request already in
	// one of them is left alone; otherwise it converts to the first entry.
	Target map[string][]string
	// Cache configures the rate cache.
	Cache *CurrencyConversionCacheOptions
	// Doer overrides the transport used to read the TCMB document.
	Doer core.Doer
	// TcmbURL overrides the TCMB XML endpoint.
	TcmbURL string
}

// FxQuoteRequest asks for a converted amount.
type FxQuoteRequest struct {
	Amount any    `json:"amount"`
	From   string `json:"from"`
	To     string `json:"to"`
}

// FxQuoteResponse is a converted amount and the rate that produced it.
type FxQuoteResponse struct {
	Amount          float64  `json:"amount"`
	From            string   `json:"from"`
	To              string   `json:"to"`
	ConvertedAmount float64  `json:"convertedAmount"`
	ExchangeRate    float64  `json:"exchangeRate"`
	Markup          *float64 `json:"markup,omitempty"`
}

// CurrencyConversionPaymentResponse is a payment result carrying the conversion
// that produced it. The embedded response keeps every core field and JSON name.
type CurrencyConversionPaymentResponse struct {
	core.PaymentResponse
	OriginalAmount   string  `json:"originalAmount"`
	OriginalCurrency string  `json:"originalCurrency"`
	ExchangeRate     float64 `json:"exchangeRate"`
}

// CurrencyConversionThreeDSInitResponse is a 3D Secure init result carrying its conversion.
type CurrencyConversionThreeDSInitResponse struct {
	core.ThreeDSInitResponse
	OriginalAmount   string  `json:"originalAmount"`
	OriginalCurrency string  `json:"originalCurrency"`
	ExchangeRate     float64 `json:"exchangeRate"`
}

// CurrencyConversionRefundResponse is a refund result carrying its conversion.
type CurrencyConversionRefundResponse struct {
	core.RefundResponse
	OriginalAmount   string  `json:"originalAmount"`
	OriginalCurrency string  `json:"originalCurrency"`
	ExchangeRate     float64 `json:"exchangeRate"`
}

// CurrencyConversionCancelResponse is a cancellation result carrying its conversion.
type CurrencyConversionCancelResponse struct {
	core.CancelResponse
	OriginalAmount   string  `json:"originalAmount"`
	OriginalCurrency string  `json:"originalCurrency"`
	ExchangeRate     float64 `json:"exchangeRate"`
}

type currencyConversionRecord struct {
	originalAmount    string
	originalPaidPrice string
	originalCurrency  string
	exchangeRate      float64
	targetCurrency    string
	convertedAmount   string
}

type currencyConversionTcmbCurrency struct {
	unit            float64
	forexBuying     *float64
	forexSelling    *float64
	banknoteBuying  *float64
	banknoteSelling *float64
}

var currencyConversionDurationUnits = map[string]float64{
	"s": 1000,
	"m": 60000,
	"h": 3600000,
	"d": 86400000,
}

var currencyConversionTTLPattern = regexp.MustCompile(`(?i)^(\d+(?:\.\d+)?)\s*(s|m|h|d)$`)

func currencyConversionParseTTL(ttl any) (float64, error) {
	switch value := ttl.(type) {
	case nil:
		return 3600000, nil
	case int:
		return float64(value), nil
	case int64:
		return float64(value), nil
	case float64:
		return value, nil
	case string:
		match := currencyConversionTTLPattern.FindStringSubmatch(strings.TrimSpace(value))
		if match == nil {
			return 0, core.NewConfigurationError(fmt.Sprintf(
				"currencyConversion: invalid cache ttl '%s'. Use milliseconds or '60s', '30m', '1h', '1d'.", value), "")
		}
		number, err := strconv.ParseFloat(match[1], 64)
		if err != nil {
			return 0, core.NewConfigurationError(fmt.Sprintf(
				"currencyConversion: invalid cache ttl '%s'. Use milliseconds or '60s', '30m', '1h', '1d'.", value), "")
		}
		return number * currencyConversionDurationUnits[strings.ToLower(match[2])], nil
	default:
		return 0, core.NewConfigurationError(fmt.Sprintf(
			"currencyConversion: invalid cache ttl '%v'. Use milliseconds or '60s', '30m', '1h', '1d'.", ttl), "")
	}
}

var (
	currencyConversionCurrencyPattern = regexp.MustCompile(`(?i)<Currency\s+[^>]*CurrencyCode="([A-Z]{3})"[^>]*>([\s\S]*?)</Currency>`)
	currencyConversionUnitPattern     = regexp.MustCompile(`(?i)<Unit>(\d+)</Unit>`)
	currencyConversionForexBuying     = regexp.MustCompile(`(?i)<ForexBuying>([0-9.]+)</ForexBuying>`)
	currencyConversionForexSelling    = regexp.MustCompile(`(?i)<ForexSelling>([0-9.]+)</ForexSelling>`)
	currencyConversionBanknoteBuying  = regexp.MustCompile(`(?i)<BanknoteBuying>([0-9.]+)</BanknoteBuying>`)
	currencyConversionBanknoteSelling = regexp.MustCompile(`(?i)<BanknoteSelling>([0-9.]+)</BanknoteSelling>`)
)

func currencyConversionXMLNumber(pattern *regexp.Regexp, content string) *float64 {
	match := pattern.FindStringSubmatch(content)
	if match == nil {
		return nil
	}
	value, err := strconv.ParseFloat(match[1], 64)
	if err != nil {
		return nil
	}
	return &value
}

func currencyConversionParseTcmbXML(xml string) map[string]currencyConversionTcmbCurrency {
	currencies := map[string]currencyConversionTcmbCurrency{}
	for _, match := range currencyConversionCurrencyPattern.FindAllStringSubmatch(xml, -1) {
		code := strings.ToUpper(match[1])
		content := match[2]
		unit := 1.0
		if parsed := currencyConversionXMLNumber(currencyConversionUnitPattern, content); parsed != nil {
			unit = *parsed
		}
		if unit <= 0 {
			unit = 1
		}
		currencies[code] = currencyConversionTcmbCurrency{
			unit:            unit,
			forexBuying:     currencyConversionXMLNumber(currencyConversionForexBuying, content),
			forexSelling:    currencyConversionXMLNumber(currencyConversionForexSelling, content),
			banknoteBuying:  currencyConversionXMLNumber(currencyConversionBanknoteBuying, content),
			banknoteSelling: currencyConversionXMLNumber(currencyConversionBanknoteSelling, content),
		}
	}
	return currencies
}

func currencyConversionFirstRate(values ...*float64) *float64 {
	for _, value := range values {
		if value != nil {
			return value
		}
	}
	return nil
}

func currencyConversionExtractRate(item currencyConversionTcmbCurrency, rateType TcmbRateType) (float64, bool) {
	var raw *float64
	switch rateType {
	case TcmbForexSelling:
		raw = currencyConversionFirstRate(item.forexSelling, item.banknoteSelling)
	case TcmbForexBuying:
		raw = currencyConversionFirstRate(item.forexBuying, item.banknoteBuying)
	case TcmbBanknoteSelling:
		raw = currencyConversionFirstRate(item.banknoteSelling, item.forexSelling)
	case TcmbBanknoteBuying:
		raw = currencyConversionFirstRate(item.banknoteBuying, item.forexBuying)
	}
	if raw == nil || math.IsNaN(*raw) || math.IsInf(*raw, 0) || *raw <= 0 {
		return 0, false
	}
	return *raw / item.unit, true
}

// currencyConversionMinor renders a float amount the way the decimal helpers read it.
func currencyConversionMinor(amount float64, field string) (int64, error) {
	return core.ToMinorUnits(strconv.FormatFloat(amount, 'f', -1, 64), field)
}

func currencyConversionFixed2(minor int64) string {
	return strconv.FormatFloat(float64(minor)/100, 'f', 2, 64)
}

func currencyConversionRoundMinor(minor int64, rate float64) int64 {
	return int64(math.Round(float64(minor) * rate))
}

// CurrencyConversion builds the plugin that converts prices to the currencies a
// provider accepts, using the daily TCMB rates or a custom rate source. It keeps
// kuruş accuracy across basket items and locks the checkout rate for refunds.
func CurrencyConversion(options CurrencyConversionOptions) (core.Plugin, error) {
	rateType := options.Rate
	if rateType == "" {
		rateType = TcmbForexSelling
	}
	var ttl any
	if options.Cache != nil {
		ttl = options.Cache.TTL
	}
	ttlMs, err := currencyConversionParseTTL(ttl)
	if err != nil {
		return core.Plugin{}, err
	}
	tcmbURL := options.TcmbURL
	if tcmbURL == "" {
		tcmbURL = "https://www.tcmb.gov.tr/kurlar/today.xml"
	}
	doer := options.Doer
	if doer == nil {
		doer = http.DefaultClient
	}

	var mu sync.Mutex
	var cachedRates map[string]currencyConversionTcmbCurrency
	var cacheTimestamp int64
	byConversation := map[string]currencyConversionRecord{}
	byPaymentID := map[string]currencyConversionRecord{}
	// byRequest is keyed by the request itself, so an entry is dropped as soon as
	// the after hook reads it and a finished request never keeps its record alive.
	byRequest := map[any]currencyConversionRecord{}

	fetchTcmbRates := func() (map[string]currencyConversionTcmbCurrency, error) {
		mu.Lock()
		now := time.Now().UnixMilli()
		if cachedRates != nil && float64(now-cacheTimestamp) < ttlMs {
			rates := cachedRates
			mu.Unlock()
			return rates, nil
		}
		mu.Unlock()

		request, err := http.NewRequest(http.MethodGet, tcmbURL, nil)
		if err != nil {
			return nil, err
		}
		response, err := doer.Do(request)
		if err != nil {
			return nil, err
		}
		defer response.Body.Close()
		if response.StatusCode < 200 || response.StatusCode > 299 {
			return nil, core.NewError(fmt.Sprintf(
				"Failed to fetch TCMB exchange rates: HTTP %d", response.StatusCode), string(core.CodeProviderError))
		}
		body, err := io.ReadAll(response.Body)
		if err != nil {
			return nil, err
		}
		rates := currencyConversionParseTcmbXML(string(body))
		mu.Lock()
		cachedRates, cacheTimestamp = rates, now
		mu.Unlock()
		return rates, nil
	}

	getBaseRate := func(from, to string) (float64, error) {
		f := strings.ToUpper(strings.TrimSpace(from))
		t := strings.ToUpper(strings.TrimSpace(to))
		if f == t {
			return 1, nil
		}
		if options.SourceFunc != nil {
			rate, err := options.SourceFunc(f, t)
			if err != nil {
				return 0, err
			}
			if math.IsNaN(rate) || math.IsInf(rate, 0) || rate <= 0 {
				return 0, core.NewValidationError(fmt.Sprintf(
					"currencyConversion: custom rate source returned no rate for %s -> %s", f, t), "", nil)
			}
			return rate, nil
		}

		rates, err := fetchTcmbRates()
		if err != nil {
			return 0, err
		}
		tryRate := func(currency string) (float64, error) {
			if currency == "TRY" {
				return 1, nil
			}
			data, ok := rates[currency]
			if !ok {
				return 0, core.NewValidationError(fmt.Sprintf(
					"currencyConversion: TCMB rate not found for currency '%s'", currency), "", nil)
			}
			rate, ok := currencyConversionExtractRate(data, rateType)
			if !ok {
				return 0, core.NewValidationError(fmt.Sprintf(
					"currencyConversion: TCMB %s rate not found for currency '%s'", rateType, currency), "", nil)
			}
			return rate, nil
		}

		if t == "TRY" {
			return tryRate(f)
		}
		if f == "TRY" {
			rate, err := tryRate(t)
			if err != nil {
				return 0, err
			}
			return 1 / rate, nil
		}
		rateF, err := tryRate(f)
		if err != nil {
			return 0, err
		}
		rateT, err := tryRate(t)
		if err != nil {
			return 0, err
		}
		return rateF / rateT, nil
	}

	getEffectiveRate := func(from, to string) (float64, error) {
		base, err := getBaseRate(from, to)
		if err != nil {
			return 0, err
		}
		if strings.ToUpper(from) == strings.ToUpper(to) {
			return 1, nil
		}
		return base * (1 + options.Markup), nil
	}

	resolveTargetCurrency := func(provider, requestCurrency string) string {
		rule, ok := options.Target[provider]
		if !ok || len(rule) == 0 {
			return ""
		}
		current := strings.ToUpper(requestCurrency)
		for _, candidate := range rule {
			if strings.ToUpper(candidate) == current {
				return ""
			}
		}
		return strings.ToUpper(rule[0])
	}

	convertBasketItems := func(items []core.BasketItem, rate float64, targetTotalPrice string) ([]core.BasketItem, error) {
		if len(items) == 0 {
			return []core.BasketItem{}, nil
		}
		targetMinor, err := core.ToMinorUnits(targetTotalPrice, "price")
		if err != nil {
			return nil, err
		}
		minors := make([]int64, 0, len(items))
		var sum int64
		for _, item := range items {
			itemMinor, err := core.ToMinorUnits(item.Price, "basketItems.price")
			if err != nil {
				return nil, err
			}
			converted := currencyConversionRoundMinor(itemMinor, rate)
			minors = append(minors, converted)
			sum += converted
		}
		// The last item absorbs the kuruş difference so the basket still sums to the total.
		if diff := targetMinor - sum; diff != 0 {
			minors[len(minors)-1] += diff
		}
		out := make([]core.BasketItem, 0, len(items))
		for i, item := range items {
			item.Price = currencyConversionFixed2(minors[i])
			out = append(out, item)
		}
		return out, nil
	}

	rememberRequest := func(request any, record currencyConversionRecord) {
		mu.Lock()
		byRequest[request] = record
		mu.Unlock()
	}

	quote := func(request FxQuoteRequest) (*FxQuoteResponse, error) {
		var amount float64
		switch typed := request.Amount.(type) {
		case nil:
			return nil, core.NewValidationError("Invalid amount: <nil>", "", nil)
		case string:
			parsed, err := core.ParseAmount(typed, "amount")
			if err != nil {
				return nil, err
			}
			amount = parsed
		default:
			text := fmt.Sprint(typed)
			parsed, err := core.ParseAmount(text, "amount")
			if err != nil {
				return nil, err
			}
			amount = parsed
		}
		rate, err := getEffectiveRate(request.From, request.To)
		if err != nil {
			return nil, err
		}
		minor, err := currencyConversionMinor(amount, "amount")
		if err != nil {
			return nil, err
		}
		response := &FxQuoteResponse{
			Amount:          amount,
			From:            strings.ToUpper(request.From),
			To:              strings.ToUpper(request.To),
			ConvertedAmount: float64(currencyConversionRoundMinor(minor, rate)) / 100,
			ExchangeRate:    rate,
		}
		if options.Markup != 0 {
			markup := options.Markup
			response.Markup = &markup
		}
		return response, nil
	}

	paymentMatcher := func(ctx core.OperationContext) bool {
		switch ctx.Operation {
		case core.OperationCreatePayment, core.OperationInitThreeDSPayment,
			core.OperationAuthorize, core.OperationInitThreeDSAuthorize:
			return true
		}
		return false
	}

	return core.DefinePlugin(core.Plugin{
		ID:      PluginIDCurrencyConversion,
		Version: core.Version,
		Options: options,
		Methods: func(context.Context, core.PluginContext) (map[string]any, error) {
			return map[string]any{
				"fx": map[string]any{
					"quote":   quote,
					"getRate": getEffectiveRate,
					"clearCache": func() {
						mu.Lock()
						cachedRates, cacheTimestamp = nil, 0
						mu.Unlock()
					},
				},
			}, nil
		},
		Hooks: core.PluginHooks{
			Before: []core.BeforeHook{
				{
					Matcher: paymentMatcher,
					Handler: func(ctx core.OperationContext) (*core.BeforeHookResult, error) {
						if ctx.Provider == "" {
							return nil, nil
						}
						var base *core.PaymentRequest
						isThreeDS := false
						switch typed := ctx.Request.(type) {
						case *core.PaymentRequest:
							base = typed
						case *core.ThreeDSPaymentRequest:
							if typed != nil {
								base = &typed.PaymentRequest
								isThreeDS = true
							}
						case core.PaymentRequest:
							base = &typed
						case core.ThreeDSPaymentRequest:
							base = &typed.PaymentRequest
							isThreeDS = true
						}
						if base == nil || base.Currency == "" || base.Price == "" {
							return nil, nil
						}

						targetCurrency := resolveTargetCurrency(ctx.Provider, base.Currency)
						if targetCurrency == "" {
							return nil, nil
						}

						originalCurrency := strings.ToUpper(base.Currency)
						originalAmount := base.Price
						originalPaidPrice := base.PaidPrice
						if originalPaidPrice == "" {
							originalPaidPrice = originalAmount
						}

						rate, err := getEffectiveRate(originalCurrency, targetCurrency)
						if err != nil {
							return nil, err
						}
						priceMinor, err := core.ToMinorUnits(originalAmount, "price")
						if err != nil {
							return nil, err
						}
						convertedPrice := currencyConversionFixed2(currencyConversionRoundMinor(priceMinor, rate))
						paidMinor, err := core.ToMinorUnits(originalPaidPrice, "paidPrice")
						if err != nil {
							return nil, err
						}
						convertedPaidPrice := currencyConversionFixed2(currencyConversionRoundMinor(paidMinor, rate))

						updated := *base
						updated.Price = convertedPrice
						updated.PaidPrice = convertedPaidPrice
						updated.Currency = targetCurrency
						if base.BasketItems != nil {
							items, err := convertBasketItems(base.BasketItems, rate, convertedPrice)
							if err != nil {
								return nil, err
							}
							updated.BasketItems = items
						}

						record := currencyConversionRecord{
							originalAmount:    originalAmount,
							originalPaidPrice: originalPaidPrice,
							originalCurrency:  originalCurrency,
							exchangeRate:      rate,
							targetCurrency:    targetCurrency,
							convertedAmount:   convertedPrice,
						}
						if base.ConversationID != "" {
							mu.Lock()
							byConversation[base.ConversationID] = record
							mu.Unlock()
						}

						var replacement any = &updated
						if isThreeDS {
							replacement = &core.ThreeDSPaymentRequest{PaymentRequest: updated}
						}
						rememberRequest(replacement, record)
						return &core.BeforeHookResult{Request: replacement}, nil
					},
				},
				{
					Matcher: func(ctx core.OperationContext) bool { return ctx.Operation == core.OperationRefund },
					Handler: func(ctx core.OperationContext) (*core.BeforeHookResult, error) {
						var base *core.RefundRequest
						switch typed := ctx.Request.(type) {
						case *core.RefundRequest:
							base = typed
						case core.RefundRequest:
							base = &typed
						}
						if base == nil {
							return nil, nil
						}

						mu.Lock()
						record, ok := byPaymentID[base.PaymentID]
						if !ok && base.ConversationID != "" {
							record, ok = byConversation[base.ConversationID]
						}
						mu.Unlock()
						if !ok {
							return nil, nil
						}

						// A refund in the customer's currency uses the checkout rate, not today's.
						if base.Currency == "" || strings.ToUpper(base.Currency) != record.originalCurrency {
							return nil, nil
						}
						refundAmount, err := core.ParseAmount(base.Price, "price")
						if err != nil {
							return nil, err
						}
						minor, err := currencyConversionMinor(refundAmount, "price")
						if err != nil {
							return nil, err
						}
						convertedPrice := currencyConversionFixed2(currencyConversionRoundMinor(minor, record.exchangeRate))

						updated := *base
						updated.Price = convertedPrice
						updated.Currency = record.targetCurrency

						rememberRequest(&updated, currencyConversionRecord{
							originalAmount:   base.Price,
							originalCurrency: record.originalCurrency,
							exchangeRate:     record.exchangeRate,
							targetCurrency:   record.targetCurrency,
							convertedAmount:  convertedPrice,
						})
						return &core.BeforeHookResult{Request: &updated}, nil
					},
				},
			},
			After: []core.AfterHook{{
				Handler: func(ctx core.OperationResultContext) (*core.AfterHookResult, error) {
					mu.Lock()
					var record currencyConversionRecord
					ok := false
					if ctx.Request != nil && reflect.TypeOf(ctx.Request).Comparable() {
						record, ok = byRequest[ctx.Request]
						if ok {
							delete(byRequest, ctx.Request)
						}
					}
					if !ok {
						if conversationID, isText := currencyConversionConversationID(ctx.Request); isText && conversationID != "" {
							record, ok = byConversation[conversationID]
						}
					}
					mu.Unlock()
					if !ok || ctx.Result == nil {
						return nil, nil
					}

					if paymentID := currencyConversionPaymentID(ctx.Result); paymentID != "" {
						mu.Lock()
						byPaymentID[paymentID] = record
						mu.Unlock()
					}
					return &core.AfterHookResult{Result: currencyConversionAttach(ctx.Result, record)}, nil
				},
			}},
		},
	}), nil
}

func currencyConversionConversationID(request any) (string, bool) {
	switch typed := request.(type) {
	case *core.PaymentRequest:
		if typed != nil {
			return typed.ConversationID, true
		}
	case *core.ThreeDSPaymentRequest:
		if typed != nil {
			return typed.ConversationID, true
		}
	case *core.RefundRequest:
		if typed != nil {
			return typed.ConversationID, true
		}
	case *core.CancelRequest:
		if typed != nil {
			return typed.ConversationID, true
		}
	case *core.InstallmentInfoRequest:
		if typed != nil {
			return typed.ConversationID, true
		}
	case map[string]any:
		value, ok := typed["conversationId"].(string)
		return value, ok
	}
	return "", false
}

func currencyConversionPaymentID(result any) string {
	switch typed := result.(type) {
	case *core.PaymentResponse:
		if typed != nil {
			return typed.PaymentID
		}
	case *core.ThreeDSInitResponse:
		if typed != nil {
			return typed.PaymentID
		}
	case map[string]any:
		value, _ := typed["paymentId"].(string)
		return value
	}
	return ""
}

// currencyConversionAttach adds the conversion metadata to a result, keeping its
// core type reachable through the embedded response.
func currencyConversionAttach(result any, record currencyConversionRecord) any {
	switch typed := result.(type) {
	case *core.PaymentResponse:
		if typed == nil {
			return result
		}
		return &CurrencyConversionPaymentResponse{
			PaymentResponse:  *typed,
			OriginalAmount:   record.originalAmount,
			OriginalCurrency: record.originalCurrency,
			ExchangeRate:     record.exchangeRate,
		}
	case *core.ThreeDSInitResponse:
		if typed == nil {
			return result
		}
		return &CurrencyConversionThreeDSInitResponse{
			ThreeDSInitResponse: *typed,
			OriginalAmount:      record.originalAmount,
			OriginalCurrency:    record.originalCurrency,
			ExchangeRate:        record.exchangeRate,
		}
	case *core.RefundResponse:
		if typed == nil {
			return result
		}
		return &CurrencyConversionRefundResponse{
			RefundResponse:   *typed,
			OriginalAmount:   record.originalAmount,
			OriginalCurrency: record.originalCurrency,
			ExchangeRate:     record.exchangeRate,
		}
	case *core.CancelResponse:
		if typed == nil {
			return result
		}
		return &CurrencyConversionCancelResponse{
			CancelResponse:   *typed,
			OriginalAmount:   record.originalAmount,
			OriginalCurrency: record.originalCurrency,
			ExchangeRate:     record.exchangeRate,
		}
	case map[string]any:
		typed["originalAmount"] = record.originalAmount
		typed["originalCurrency"] = record.originalCurrency
		typed["exchangeRate"] = record.exchangeRate
		return typed
	}

	data, err := json.Marshal(result)
	if err != nil {
		return result
	}
	var generic map[string]any
	if err := json.Unmarshal(data, &generic); err != nil || generic == nil {
		return result
	}
	generic["originalAmount"] = record.originalAmount
	generic["originalCurrency"] = record.originalCurrency
	generic["exchangeRate"] = record.exchangeRate
	return generic
}
