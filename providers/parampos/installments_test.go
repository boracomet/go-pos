package parampos

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"

	"github.com/boracomet/go-pos/core"
)

func ratesEnvelope(body string) string {
	return `<?xml version="1.0" encoding="utf-8"?>` +
		`<soap:Envelope xmlns:soap="http://schemas.xmlsoap.org/soap/envelope/"><soap:Body>` +
		body + `</soap:Body></soap:Envelope>`
}

func rateRow(id, posID, bank string, rates []string) string {
	var b strings.Builder
	b.WriteString(`<DT_Ozel_Oranlar_SK diffgr:id="DT_Ozel_Oranlar_SK` + id + `" msdata:rowOrder="`)
	b.WriteString(id)
	b.WriteString(`"><Ozel_Oran_SK_ID>` + id + `</Ozel_Oran_SK_ID>`)
	b.WriteString(`<GUID>` + testGUID + `</GUID>`)
	b.WriteString(`<SanalPOS_ID>` + posID + `</SanalPOS_ID><Kredi_Karti_Banka>` + bank + `</Kredi_Karti_Banka>`)
	for i, rate := range rates {
		tag := "MO_" + pad2(i+1)
		b.WriteString("<" + tag + ">" + rate + "</" + tag + ">")
	}
	b.WriteString(`</DT_Ozel_Oranlar_SK>`)
	return b.String()
}

// ratesResponse is a TP_Ozel_Oran_SK_Liste answer: a negative rate means the
// installment count is not available for that card program.
var ratesResponse = ratesEnvelope(
	`<TP_Ozel_Oran_SK_ListeResponse xmlns="` + Namespace + `"><TP_Ozel_Oran_SK_ListeResult>` +
		`<Sonuc>1</Sonuc><Sonuc_Str>Başarılı</Sonuc_Str>` +
		`<DT_Bilgi><xs:schema id="NewDataSet"></xs:schema><diffgr:diffgram><NewDataSet>` +
		rateRow("1", "1029", "Axess", []string{
			"1.7500", "3.2000", "4.1000", "-2.0000", "-2.0000", "7.9000",
			"-1.0000", "-2.0000", "-2.0000", "-2.0000", "-2.0000", "-2.0000",
		}) +
		rateRow("2", "1009", "Diğer Banka Kartları", []string{
			"1.9900", "-2.0000", "-2.0000", "-2.0000", "-2.0000", "-2.0000",
			"-2.0000", "-2.0000", "-2.0000", "-2.0000", "-2.0000", "-2.0000",
		}) +
		`</NewDataSet></diffgr:diffgram></DT_Bilgi>` +
		`</TP_Ozel_Oran_SK_ListeResult></TP_Ozel_Oran_SK_ListeResponse>`)

func binResponse(posID, dkk string) string {
	return ratesEnvelope(
		`<BIN_SanalPosResponse xmlns="` + Namespace + `"><BIN_SanalPosResult>` +
			`<Sonuc>1</Sonuc><Sonuc_Str>Başarılı</Sonuc_Str><DT_Bilgi><diffgr:diffgram><NewDataSet><Temp>` +
			`<BIN>435508</BIN><SanalPOS_ID>` + posID + `</SanalPOS_ID><Kart_Banka>AKBANK T.A.Ş.</Kart_Banka>` +
			`<DKK>` + dkk + `</DKK><Kart_Tip>Credit Kart</Kart_Tip><Kart_Org>VISA</Kart_Org>` +
			`</Temp></NewDataSet></diffgr:diffgram></DT_Bilgi></BIN_SanalPosResult></BIN_SanalPosResponse>`)
}

// respondInstallments answers both read-only queries of an installment lookup.
func respondInstallments(doer *stubDoer, bin string, times int) {
	for i := 0; i < times; i++ {
		doer.respondAction("BIN_SanalPos", bin)
		doer.respondAction("TP_Ozel_Oran_SK_Liste", ratesResponse)
	}
}

func commission(rate float64) *float64 { return &rate }

func TestParseDataSetRowsReadsEveryRow(t *testing.T) {
	rows := ParseDataSetRows(ratesResponse, "DT_Ozel_Oranlar_SK")
	if len(rows) != 2 {
		t.Fatalf("rows = %d, want 2", len(rows))
	}
	if rows[0]["SanalPOS_ID"] != "1029" || rows[0]["Kredi_Karti_Banka"] != "Axess" || rows[0]["MO_01"] != "1.7500" {
		t.Errorf("row 0 = %v", rows[0])
	}
	if rows[1]["Kredi_Karti_Banka"] != "Diğer Banka Kartları" {
		t.Errorf("row 1 bank = %q", rows[1]["Kredi_Karti_Banka"])
	}
}

func TestRatesOfDropsUnavailableRates(t *testing.T) {
	rows := ParseDataSetRows(ratesResponse, "DT_Ozel_Oranlar_SK")
	want := map[int]float64{1: 1.75, 2: 3.2, 3: 4.1, 6: 7.9}
	if got := RatesOf(rows[0]); !reflect.DeepEqual(got, want) {
		t.Errorf("RatesOf() = %v, want %v", got, want)
	}
}

func TestCalculateTotalMinorRoundsHalfUp(t *testing.T) {
	cases := []struct {
		priceMinor int64
		rate       float64
		want       int64
	}{
		{10000, 1.75, 10175},
		{3333, 3.2, 3440},
		{50, 1, 51},
		{12345, 0, 12345},
		{99999999999, 7.9, 107899999999},
	}
	for _, c := range cases {
		if got := CalculateTotalMinor(c.priceMinor, c.rate); got != c.want {
			t.Errorf("CalculateTotalMinor(%d, %v) = %d, want %d", c.priceMinor, c.rate, got, c.want)
		}
	}
}

func TestInstallmentInfoReturnsProgramTotals(t *testing.T) {
	doer := &stubDoer{}
	respondInstallments(doer, binResponse("1029", "0"), 1)
	provider := newTestProvider(t, doer)

	result, err := provider.InstallmentInfo(context.Background(), &core.InstallmentInfoRequest{BinNumber: "435508", Price: "100.00"})
	if err != nil {
		t.Fatalf("InstallmentInfo() error = %v", err)
	}
	if result.Status != core.StatusSuccess {
		t.Fatalf("status = %q, want %q", result.Status, core.StatusSuccess)
	}
	if len(result.InstallmentDetails) != 1 {
		t.Fatalf("installmentDetails = %d, want 1", len(result.InstallmentDetails))
	}
	detail := result.InstallmentDetails[0]
	if detail.BinNumber != "435508" || detail.BankName != "AKBANK T.A.Ş." || detail.CardFamilyName != "Axess" {
		t.Errorf("detail = %+v", detail)
	}
	want := []core.InstallmentDetail{
		{InstallmentNumber: 1, TotalPrice: 101.75, InstallmentPrice: 101.75, CommissionRate: commission(1.75)},
		{InstallmentNumber: 2, TotalPrice: 103.2, InstallmentPrice: 51.6, CommissionRate: commission(3.2)},
		{InstallmentNumber: 3, TotalPrice: 104.1, InstallmentPrice: 34.7, CommissionRate: commission(4.1)},
		{InstallmentNumber: 6, TotalPrice: 107.9, InstallmentPrice: 17.98, CommissionRate: commission(7.9)},
	}
	if !reflect.DeepEqual(detail.InstallmentPrices, want) {
		t.Errorf("installmentPrices = %+v, want %+v", detail.InstallmentPrices, want)
	}
}

func TestInstallmentInfoSendsBothReadOnlyQueries(t *testing.T) {
	doer := &stubDoer{}
	respondInstallments(doer, binResponse("1029", "0"), 1)
	provider := newTestProvider(t, doer)

	if _, err := provider.InstallmentInfo(context.Background(), &core.InstallmentInfoRequest{BinNumber: "435508", Price: "100.00"}); err != nil {
		t.Fatalf("InstallmentInfo() error = %v", err)
	}
	actions := map[string]bool{}
	for _, call := range doer.calls {
		actions[call.soapAction()] = true
	}
	for _, action := range []string{Namespace + "BIN_SanalPos", Namespace + "TP_Ozel_Oran_SK_Liste"} {
		if !actions[action] {
			t.Errorf("missing SOAPAction %q, got %v", action, actions)
		}
	}
	for i, call := range doer.calls {
		if strings.HasSuffix(call.soapAction(), "Liste") && !strings.Contains(call.Body, "<GUID>"+testGUID+"</GUID>") {
			t.Errorf("call %d: Liste request missing <GUID>%s</GUID>", i, testGUID)
		}
	}
}

// Both queries are read-only, so a transport failure is retried instead of
// failing the lookup.
func TestInstallmentInfoRetriesBothQueries(t *testing.T) {
	for _, retried := range []string{"BIN_SanalPos", "TP_Ozel_Oran_SK_Liste"} {
		t.Run(retried, func(t *testing.T) {
			doer := &stubDoer{}
			doer.respondAction("BIN_SanalPos", binResponse("1029", "0"))
			doer.respondAction("TP_Ozel_Oran_SK_Liste", ratesResponse)
			if retried == "BIN_SanalPos" {
				doer.byAction[Namespace+"BIN_SanalPos"] = append(
					[]stubResponse{{err: timeoutError()}}, doer.byAction[Namespace+"BIN_SanalPos"]...)
			} else {
				doer.byAction[Namespace+"TP_Ozel_Oran_SK_Liste"] = append(
					[]stubResponse{{err: timeoutError()}}, doer.byAction[Namespace+"TP_Ozel_Oran_SK_Liste"]...)
			}
			provider := newTestProvider(t, doer)

			result, err := provider.InstallmentInfo(context.Background(), &core.InstallmentInfoRequest{BinNumber: "435508", Price: "100.00"})
			if err != nil {
				t.Fatalf("InstallmentInfo() error = %v", err)
			}
			if len(doer.calls) != 3 {
				t.Errorf("calls = %d, want 3 (the failing query is retried)", len(doer.calls))
			}
			if result.Status != core.StatusSuccess {
				t.Errorf("status = %q, want %q", result.Status, core.StatusSuccess)
			}
		})
	}
}

func TestInstallmentInfoUsesOtherBankRatesWhenDKK(t *testing.T) {
	doer := &stubDoer{}
	respondInstallments(doer, binResponse("9999", "1"), 1)
	provider := newTestProvider(t, doer)

	result, err := provider.InstallmentInfo(context.Background(), &core.InstallmentInfoRequest{BinNumber: "435508", Price: "100.00"})
	if err != nil {
		t.Fatalf("InstallmentInfo() error = %v", err)
	}
	want := []core.InstallmentDetail{
		{InstallmentNumber: 1, TotalPrice: 101.99, InstallmentPrice: 101.99, CommissionRate: commission(1.99)},
	}
	if got := result.InstallmentDetails[0].InstallmentPrices; !reflect.DeepEqual(got, want) {
		t.Errorf("installmentPrices = %+v, want %+v", got, want)
	}
}

func TestInstallmentInfoWithoutMatchingProgram(t *testing.T) {
	doer := &stubDoer{}
	respondInstallments(doer, binResponse("9999", "0"), 1)
	provider := newTestProvider(t, doer)

	result, err := provider.InstallmentInfo(context.Background(), &core.InstallmentInfoRequest{BinNumber: "435508", Price: "100.00"})
	if err != nil {
		t.Fatalf("InstallmentInfo() error = %v", err)
	}
	if result.Status != core.StatusSuccess {
		t.Errorf("status = %q, want %q", result.Status, core.StatusSuccess)
	}
	if got := result.InstallmentDetails[0].InstallmentPrices; len(got) != 0 {
		t.Errorf("installmentPrices = %+v, want none", got)
	}
}

func TestInstallmentInfoRejectsInvalidPriceWithoutCalling(t *testing.T) {
	doer := &stubDoer{}
	provider := newTestProvider(t, doer)

	result, err := provider.InstallmentInfo(context.Background(), &core.InstallmentInfoRequest{BinNumber: "435508", Price: "abc"})
	if err != nil {
		t.Fatalf("InstallmentInfo() error = %v", err)
	}
	if len(doer.calls) != 0 {
		t.Errorf("calls = %d, want 0", len(doer.calls))
	}
	if result.Code != core.CodeInvalidRequest {
		t.Errorf("code = %q, want %q", result.Code, core.CodeInvalidRequest)
	}
}

func TestCalculatePaidPrice(t *testing.T) {
	doer := &stubDoer{}
	respondInstallments(doer, binResponse("1029", "0"), 2)
	provider := newTestProvider(t, doer)

	got, err := provider.CalculatePaidPrice(context.Background(), PaidPriceRequest{BinNumber: "435508", Price: "250.00", Installment: 3})
	if err != nil {
		t.Fatalf("CalculatePaidPrice() error = %v", err)
	}
	if got != "260.25" {
		t.Errorf("paidPrice = %q, want 260.25", got)
	}

	got, err = provider.CalculatePaidPrice(context.Background(), PaidPriceRequest{BinNumber: "435508", Price: "250.00"})
	if err != nil {
		t.Fatalf("CalculatePaidPrice() error = %v", err)
	}
	if got != "254.38" {
		t.Errorf("paidPrice = %q, want 254.38", got)
	}
}

func TestCalculatePaidPriceRejectsUnavailableInstallment(t *testing.T) {
	doer := &stubDoer{}
	respondInstallments(doer, binResponse("1029", "0"), 1)
	provider := newTestProvider(t, doer)

	_, err := provider.CalculatePaidPrice(context.Background(), PaidPriceRequest{BinNumber: "435508", Price: "250.00", Installment: 4})
	if err == nil {
		t.Fatal("CalculatePaidPrice() error = nil, want ValidationError")
	}
	var validation *core.ValidationError
	if !errors.As(err, &validation) {
		t.Fatalf("error = %T (%v), want *core.ValidationError", err, err)
	}
}

func TestGetInstallmentRates(t *testing.T) {
	doer := &stubDoer{}
	doer.respond(ratesResponse)
	provider := newTestProvider(t, doer)

	programs, err := provider.GetInstallmentRates(context.Background())
	if err != nil {
		t.Fatalf("GetInstallmentRates() error = %v", err)
	}
	if len(programs) != 2 {
		t.Fatalf("programs = %d, want 2", len(programs))
	}
	if programs[0].PosID != "1029" || programs[0].BankName != "Axess" {
		t.Errorf("program 0 = %+v", programs[0])
	}
	if !reflect.DeepEqual(programs[1].Rates, map[int]float64{1: 1.99}) {
		t.Errorf("program 1 rates = %v", programs[1].Rates)
	}
	if !strings.Contains(doer.body(0), "<GUID>"+testGUID+"</GUID>") {
		t.Error("Liste request missing GUID")
	}
}
