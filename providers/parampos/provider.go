// Package parampos implements the Param (TurkPOS) SOAP virtual POS.
//
// Flow: TP_WMD_UCD charges a card (Islem_Guvenlik_Tip NS) or starts 3D Secure
// (3D, returning UCD_HTML); the bank then POSTs md/mdStatus/orderId/islemGUID/
// islemHash to the callback URL and TP_WMD_Pay finalizes the payment. Refunds and
// cancellations use TP_Islem_Iptal_Iade_Kismi2, status queries TP_Islem_Sorgulama4
// and BIN queries BIN_SanalPos.
package parampos

import (
	"context"
	"fmt"
	"math"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/boracomet/go-pos/core"
)

const (
	providerName = "parampos"
	displayName  = "Parampos"
)

var cardRules = core.PaymentValidationRules{Card: true, Required: []string{"buyer.ip"}}

// Config holds the Param merchant credentials.
type Config struct {
	core.ProviderConfig
	// ClientCode is CLIENT_CODE, the terminal number.
	ClientCode string
	// ClientUsername is CLIENT_USERNAME.
	ClientUsername string
	// ClientPassword is CLIENT_PASSWORD.
	ClientPassword string
	// GUID is the merchant key (anahtar). It is used for hashes and never sent
	// to the browser.
	GUID string
}

// Provider is the Param (TurkPOS) payment provider.
type Provider struct {
	core.BaseProvider
	cfg    Config
	client *core.HTTPClient
}

// New builds the provider, requiring every credential and a base URL. Use
// SandboxURL or ProductionURL as the base URL.
func New(cfg Config) (*Provider, error) {
	missing := make([]string, 0, 4)
	if cfg.ClientCode == "" {
		missing = append(missing, "clientCode")
	}
	if cfg.ClientUsername == "" {
		missing = append(missing, "clientUsername")
	}
	if cfg.ClientPassword == "" {
		missing = append(missing, "clientPassword")
	}
	if cfg.GUID == "" {
		missing = append(missing, "guid")
	}
	if len(missing) > 0 {
		return nil, core.NewConfigurationError("Parampos configuration is missing: "+strings.Join(missing, ", "), providerName)
	}
	if cfg.BaseURL == "" {
		return nil, core.NewConfigurationError("Parampos baseUrl is required", providerName)
	}

	base := core.NewBaseProvider(providerName, cfg.ProviderConfig)
	base.ErrorCodes = ErrorCodes
	return &Provider{
		BaseProvider: base,
		cfg:          cfg,
		client: base.NewHTTPClient(60*time.Second, map[string]string{
			"Content-Type": "text/xml; charset=utf-8",
		}),
	}, nil
}

func (p *Provider) credentials() Fields {
	return Fields{
		NewGroup("G", Fields{
			NewField("CLIENT_CODE", p.cfg.ClientCode),
			NewField("CLIENT_USERNAME", p.cfg.ClientUsername),
			NewField("CLIENT_PASSWORD", p.cfg.ClientPassword),
		}),
		NewField("GUID", p.cfg.GUID),
	}
}

func (p *Provider) sendSOAPRaw(ctx context.Context, soapAction string, fields Fields, retryable bool) (string, error) {
	envelope := BuildSOAPEnvelope(soapAction, fields)
	response, err := p.client.Post(ctx, "", envelope, core.RequestConfig{
		Headers:      map[string]string{"SOAPAction": Namespace + soapAction},
		Retryable:    retryable,
		ResponseType: "text",
	})
	if err != nil {
		return "", err
	}
	return response.Text, nil
}

func (p *Provider) sendSOAP(ctx context.Context, soapAction string, fields Fields, retryable bool) (Result, error) {
	xml, err := p.sendSOAPRaw(ctx, soapAction, fields, retryable)
	if err != nil {
		return nil, err
	}
	return ParseSOAPResponse(xml, soapAction+"Result")
}

// codeFor resolves the normalized code of a result. The bank's ISO 8583 code
// (Banka_Sonuc_Kod) is the most specific reason for a decline; Param's Sonuc is
// used when it is missing or not mapped.
func (p *Provider) codeFor(status core.PaymentStatus, errorCode string, raw any) core.PaymentErrorCode {
	if status != core.StatusFailure && errorCode != core.NetworkErrorCode {
		return ""
	}
	if bank := bankResultCode(raw); bank != "" {
		if code, ok := core.ISO8583ErrorCodes[bank]; ok {
			return code
		}
	}
	return p.ErrorCodeFor(status, errorCode)
}

// bankResultCode reads Banka_Sonuc_Kod from a SOAP result, or from payment after
// 3D completion.
func bankResultCode(raw any) string {
	var value any
	switch v := raw.(type) {
	case Result:
		value = v["Banka_Sonuc_Kod"]
	case map[string]any:
		value = v["Banka_Sonuc_Kod"]
		if value == nil {
			switch payment := v["payment"].(type) {
			case Result:
				value = payment["Banka_Sonuc_Kod"]
			case map[string]any:
				value = payment["Banka_Sonuc_Kod"]
			}
		}
	default:
		return ""
	}
	text, ok := value.(string)
	if !ok {
		return ""
	}
	text = strings.TrimSpace(text)
	if !bankCodeRe.MatchString(text) {
		return ""
	}
	if len(text) < 2 {
		return "0" + text
	}
	return text
}

func (p *Provider) buildPaymentFields(req *core.PaymentRequest, orderID, securityType string, installment int, callbackURL string) (Fields, error) {
	card, err := p.CardOf(req)
	if err != nil {
		return nil, err
	}
	transactionAmount, err := FormatAmount(req.Price)
	if err != nil {
		return nil, err
	}
	totalAmount, err := FormatAmount(paidPriceOf(req))
	if err != nil {
		return nil, err
	}
	month, err := FormatExpiryMonth(card.ExpireMonth)
	if err != nil {
		return nil, err
	}
	year, err := FormatExpiryYear(card.ExpireYear)
	if err != nil {
		return nil, err
	}
	hash, err := GeneratePaymentHash(p.cfg.ClientCode, p.cfg.GUID, installment, transactionAmount, totalAmount, orderID)
	if err != nil {
		return nil, err
	}

	fields := p.credentials()
	return append(fields,
		NewField("KK_Sahibi", card.CardHolderName),
		NewField("KK_No", card.CardNumber),
		NewField("KK_SK_Ay", month),
		NewField("KK_SK_Yil", year),
		NewField("KK_CVC", card.CVC),
		// Optional per the documentation, but the service rejects requests
		// without the element.
		NewField("KK_Sahibi_GSM", FormatGSM(req.Buyer.GSMNumber)),
		NewField("Hata_URL", callbackURL),
		NewField("Basarili_URL", callbackURL),
		NewField("Siparis_ID", orderID),
		NewField("Siparis_Aciklama", req.BasketID),
		NewField("Taksit", fmt.Sprint(installment)),
		NewField("Islem_Tutar", transactionAmount),
		NewField("Toplam_Tutar", totalAmount),
		NewField("Islem_Hash", hash),
		NewField("Islem_Guvenlik_Tip", securityType),
		NewField("Islem_ID", orderID),
		NewField("IPAdr", req.Buyer.IP),
		NewField("Ref_URL", ""),
		NewField("Data1", ""),
		NewField("Data2", ""),
		NewField("Data3", ""),
		NewField("Data4", ""),
		NewField("Data5", ""),
	), nil
}

// buildPreAuthFields builds the TP_Islem_Odeme_OnProv_WMD fields as Param
// expects them: no order description or Data fields, and the URLs only for 3D
// Secure.
func (p *Provider) buildPreAuthFields(req *core.PaymentRequest, orderID, securityType string, installment int, callbackURL string) (Fields, error) {
	card, err := p.CardOf(req)
	if err != nil {
		return nil, err
	}
	transactionAmount, err := FormatAmount(req.Price)
	if err != nil {
		return nil, err
	}
	totalAmount, err := FormatAmount(paidPriceOf(req))
	if err != nil {
		return nil, err
	}
	month, err := FormatExpiryMonth(card.ExpireMonth)
	if err != nil {
		return nil, err
	}
	year, err := FormatExpiryYear(card.ExpireYear)
	if err != nil {
		return nil, err
	}
	successURL, failURL := "", ""
	if securityType == "3D" {
		successURL, failURL = callbackURL, callbackURL
	}
	hash, err := GeneratePreAuthHash(p.cfg.ClientCode, p.cfg.GUID, transactionAmount, totalAmount, orderID, failURL, successURL)
	if err != nil {
		return nil, err
	}

	fields := p.credentials()
	fields = append(fields,
		NewField("Islem_Guvenlik_Tip", securityType),
		NewField("Islem_ID", orderID),
		NewField("IPAdr", req.Buyer.IP),
		NewField("Siparis_ID", orderID),
		NewField("Islem_Tutar", transactionAmount),
		NewField("Toplam_Tutar", totalAmount),
	)
	if securityType == "3D" {
		fields = append(fields,
			NewField("Basarili_URL", successURL),
			NewField("Hata_URL", failURL),
		)
	}
	return append(fields,
		NewField("Taksit", fmt.Sprint(installment)),
		NewField("KK_Sahibi", card.CardHolderName),
		NewField("KK_No", card.CardNumber),
		NewField("KK_SK_Ay", month),
		NewField("KK_SK_Yil", year),
		NewField("KK_CVC", card.CVC),
		NewField("KK_Sahibi_GSM", FormatGSM(req.Buyer.GSMNumber)),
		NewField("Islem_Hash", hash),
	), nil
}

// assertTRY rejects foreign currencies: TP_WMD_UCD only supports TRY. Foreign
// currency payments need TP_Islem_Odeme_WD, which is not implemented.
func assertTRY(currency string) error {
	value := strings.ToUpper(currency)
	if value == "" {
		value = "TRY"
	}
	if value != "TRY" && value != "TL" {
		return core.NewValidationError(
			fmt.Sprintf("Parampos provider currently supports only TRY payments (received %s)", currency), "", nil)
	}
	return nil
}

// CreatePayment charges a card without 3D Secure. Price is Islem_Tutar and
// PaidPrice is Toplam_Tutar, the amount charged to the card including any
// installment commission; calculate PaidPrice from the rates configured on the
// Param account (see CalculatePaidPrice).
func (p *Provider) CreatePayment(ctx context.Context, req *core.PaymentRequest) (*core.PaymentResponse, error) {
	return p.sale(ctx, req, "TP_WMD_UCD", false, "Payment failed")
}

// Authorize blocks the amount without 3D Secure (TP_Islem_Odeme_OnProv_WMD, NS).
// Charge it with Capture or release it with VoidAuthorization.
func (p *Provider) Authorize(ctx context.Context, req *core.PaymentRequest) (*core.PaymentResponse, error) {
	return p.sale(ctx, req, "TP_Islem_Odeme_OnProv_WMD", true, "Pre-authorization failed")
}

func (p *Provider) sale(ctx context.Context, req *core.PaymentRequest, action string, preAuth bool, fallback string) (*core.PaymentResponse, error) {
	orderID := ""
	if req != nil {
		orderID = req.ConversationID
	}
	if orderID == "" {
		orderID = core.GenerateOrderID("")
	}
	fail := func(err error) (*core.PaymentResponse, error) {
		resp := core.ApplyFailure(displayName, err, fallback, &core.PaymentResponse{})
		resp.PaymentID, resp.ConversationID = orderID, orderID
		resp.Code = p.codeFor(resp.Status, resp.ErrorCode, resp.RawResponse)
		return resp, nil
	}

	if err := p.ValidatePayment(req, cardRules); err != nil {
		return fail(err)
	}
	if err := assertTRY(req.Currency); err != nil {
		return fail(err)
	}
	installment := max(1, req.Installment)

	var fields Fields
	var err error
	if preAuth {
		fields, err = p.buildPreAuthFields(req, orderID, "NS", installment, "")
	} else {
		fields, err = p.buildPaymentFields(req, orderID, "NS", installment, "")
	}
	if err != nil {
		return fail(err)
	}
	result, err := p.sendSOAP(ctx, action, fields, false)
	if err != nil {
		return fail(err)
	}

	approved := IsSuccess(result["Sonuc"]) && positive(result["Islem_ID"])
	resp := &core.PaymentResponse{
		Status:         statusOf(approved),
		PaymentID:      orderID,
		ConversationID: orderID,
		RawResponse:    result,
	}
	if !approved {
		resp.ErrorCode = result["Sonuc"]
		resp.ErrorMessage = firstNonEmpty(result["Sonuc_Str"], result["Bank_HostMsg"])
	}
	resp.Code = p.codeFor(resp.Status, resp.ErrorCode, resp.RawResponse)
	return resp, nil
}

// InitThreeDSPayment starts a 3D Secure payment (TP_WMD_UCD, Islem_Guvenlik_Tip
// 3D). Render ThreeDSHTMLContent in the browser; Param POSTs the result to
// CallbackURL, and that POST body goes to CompleteThreeDSPayment.
func (p *Provider) InitThreeDSPayment(ctx context.Context, req *core.ThreeDSPaymentRequest) (*core.ThreeDSInitResponse, error) {
	return p.initThreeDS(ctx, req, "TP_WMD_UCD", false, "3D Secure initialization failed")
}

// InitThreeDSAuthorize starts a 3D Secure pre-authorization
// (TP_Islem_Odeme_OnProv_WMD, 3D). The bank posts to CallbackURL;
// CompleteThreeDSPayment verifies it and finalizes with TP_WMD_Pay.
func (p *Provider) InitThreeDSAuthorize(ctx context.Context, req *core.ThreeDSPaymentRequest) (*core.ThreeDSInitResponse, error) {
	return p.initThreeDS(ctx, req, "TP_Islem_Odeme_OnProv_WMD", true, "3D Secure pre-authorization failed")
}

func (p *Provider) initThreeDS(ctx context.Context, req *core.ThreeDSPaymentRequest, action string, preAuth bool, fallback string) (*core.ThreeDSInitResponse, error) {
	orderID, callbackURL := "", ""
	if req != nil {
		orderID = req.ConversationID
		callbackURL = req.CallbackURL
		if callbackURL == "" {
			callbackURL = req.PaymentRequest.CallbackURL
		}
	}
	if orderID == "" {
		orderID = core.GenerateOrderID("")
	}
	fail := func(err error) (*core.ThreeDSInitResponse, error) {
		resp := core.ApplyFailure(displayName, err, fallback, &core.ThreeDSInitResponse{})
		resp.PaymentID, resp.ConversationID = orderID, orderID
		resp.Code = p.codeFor(resp.Status, resp.ErrorCode, resp.RawResponse)
		return resp, nil
	}

	if req == nil {
		return fail(requestRequired())
	}
	if err := p.ValidatePayment(&req.PaymentRequest, cardRules); err != nil {
		return fail(err)
	}
	if err := assertTRY(req.Currency); err != nil {
		return fail(err)
	}
	if callbackURL == "" {
		return fail(core.NewValidationError("callbackUrl is required for 3D Secure payments", "", nil))
	}
	installment := max(1, req.Installment)

	var fields Fields
	var err error
	if preAuth {
		fields, err = p.buildPreAuthFields(&req.PaymentRequest, orderID, "3D", installment, callbackURL)
	} else {
		fields, err = p.buildPaymentFields(&req.PaymentRequest, orderID, "3D", installment, callbackURL)
	}
	if err != nil {
		return fail(err)
	}
	result, err := p.sendSOAP(ctx, action, fields, false)
	if err != nil {
		return fail(err)
	}

	html := result["UCD_HTML"]
	if !IsSuccess(result["Sonuc"]) || html == "" || html == "NONSECURE" {
		resp := &core.ThreeDSInitResponse{
			Status:         core.StatusFailure,
			PaymentID:      orderID,
			ConversationID: orderID,
			ErrorCode:      result["Sonuc"],
			ErrorMessage:   firstNonEmpty(result["Sonuc_Str"], "3D Secure form could not be created"),
			RawResponse:    result,
		}
		resp.Code = p.codeFor(resp.Status, resp.ErrorCode, resp.RawResponse)
		return resp, nil
	}

	resp := &core.ThreeDSInitResponse{
		Status:             core.StatusPending,
		ThreeDSHTMLContent: html,
		PaymentID:          orderID,
		ConversationID:     orderID,
		RawResponse:        result,
	}
	resp.Code = p.codeFor(resp.Status, resp.ErrorCode, resp.RawResponse)
	return resp, nil
}

// Capture charges all or part of a pre-authorization
// (TP_Islem_Odeme_OnProv_Kapa). Amount may be lower than the authorized amount.
func (p *Provider) Capture(ctx context.Context, req *core.CaptureRequest) (*core.PaymentResponse, error) {
	paymentID, conversationID := "", ""
	if req != nil {
		paymentID = req.PaymentID
		conversationID = firstNonEmpty(req.ConversationID, req.PaymentID)
	}
	fail := func(err error) (*core.PaymentResponse, error) {
		resp := core.ApplyFailure(displayName, err, "Capture failed", &core.PaymentResponse{})
		resp.PaymentID, resp.ConversationID = paymentID, conversationID
		resp.Code = p.codeFor(resp.Status, resp.ErrorCode, resp.RawResponse)
		return resp, nil
	}

	if req == nil {
		return fail(requestRequired())
	}
	if err := p.ValidateCapture(req); err != nil {
		return fail(err)
	}
	if err := assertTRY(req.Currency); err != nil {
		return fail(err)
	}
	amount, err := FormatAmount(req.Amount)
	if err != nil {
		return fail(err)
	}
	result, err := p.sendSOAP(ctx, "TP_Islem_Odeme_OnProv_Kapa", append(p.credentials(),
		NewField("Prov_ID", ""),
		NewField("Prov_Tutar", amount),
		NewField("Siparis_ID", req.PaymentID),
	), false)
	if err != nil {
		return fail(err)
	}

	approved := IsSuccess(result["Sonuc"])
	resp := &core.PaymentResponse{
		Status:         statusOf(approved),
		PaymentID:      req.PaymentID,
		ConversationID: conversationID,
		RawResponse:    result,
	}
	if !approved {
		resp.ErrorCode = result["Sonuc"]
		resp.ErrorMessage = firstNonEmpty(result["Sonuc_Str"], result["Bank_HostMsg"])
	}
	resp.Code = p.codeFor(resp.Status, resp.ErrorCode, resp.RawResponse)
	return resp, nil
}

// VoidAuthorization releases a pre-authorization (TP_Islem_Iptal_OnProv);
// nothing is charged.
func (p *Provider) VoidAuthorization(ctx context.Context, req *core.VoidAuthorizationRequest) (*core.CancelResponse, error) {
	paymentID, conversationID := "", ""
	if req != nil {
		paymentID = req.PaymentID
		conversationID = firstNonEmpty(req.ConversationID, req.PaymentID)
	}
	fail := func(err error) (*core.CancelResponse, error) {
		resp := core.ApplyFailure(displayName, err, "Void failed", &core.CancelResponse{})
		resp.ConversationID = conversationID
		resp.Code = p.codeFor(resp.Status, resp.ErrorCode, resp.RawResponse)
		return resp, nil
	}

	if req == nil {
		return fail(requestRequired())
	}
	result, err := p.sendSOAP(ctx, "TP_Islem_Iptal_OnProv", append(p.credentials(),
		NewField("Prov_ID", ""),
		NewField("Siparis_ID", paymentID),
	), false)
	if err != nil {
		return fail(err)
	}

	approved := IsSuccess(result["Sonuc"])
	resp := &core.CancelResponse{
		Status:         statusOf(approved),
		ConversationID: conversationID,
		RawResponse:    result,
	}
	if !approved {
		resp.ErrorCode = result["Sonuc"]
		resp.ErrorMessage = result["Sonuc_Str"]
	}
	resp.Code = p.codeFor(resp.Status, resp.ErrorCode, resp.RawResponse)
	return resp, nil
}

// CompleteThreeDSPayment finishes a 3D Secure payment. It verifies islemHash
// with the configured GUID (callback values are untrusted), requires mdStatus 1
// (full 3D authentication) and finalizes with TP_WMD_Pay. The payment is only
// successful when TP_WMD_Pay returns Sonuc > 0 and a Dekont_ID.
func (p *Provider) CompleteThreeDSPayment(ctx context.Context, callbackData any) (*core.PaymentResponse, error) {
	cb, _ := normalizeCallback(callbackData)
	orderID := cb.data.OrderID
	fail := func(err error) (*core.PaymentResponse, error) {
		resp := core.ApplyFailure(displayName, err, "3D Secure completion failed", &core.PaymentResponse{})
		resp.PaymentID, resp.ConversationID = orderID, orderID
		resp.Code = p.codeFor(resp.Status, resp.ErrorCode, resp.RawResponse)
		return resp, nil
	}
	rejected := func(errorCode, errorMessage string) (*core.PaymentResponse, error) {
		resp := &core.PaymentResponse{
			Status:         core.StatusFailure,
			PaymentID:      orderID,
			ConversationID: orderID,
			ErrorCode:      errorCode,
			ErrorMessage:   errorMessage,
			RawResponse:    callbackData,
		}
		resp.Code = p.codeFor(resp.Status, resp.ErrorCode, resp.RawResponse)
		return resp, nil
	}

	if !Verify3DSCallback(callbackData, p.cfg.GUID) {
		return rejected("INVALID_HASH", "Invalid 3D Secure callback signature")
	}
	if cb.data.MDStatus != "1" {
		return rejected("MD_STATUS_"+cb.data.MDStatus,
			firstNonEmpty(cb.data.BankResult, "3D Secure authentication failed"))
	}

	result, err := p.sendSOAP(ctx, "TP_WMD_Pay", append(p.credentials(),
		NewField("UCD_MD", cb.data.MD),
		NewField("Islem_GUID", cb.data.IslemGUID),
		NewField("Siparis_ID", cb.data.OrderID),
	), false)
	if err != nil {
		return fail(err)
	}

	approved := IsSuccess(result["Sonuc"]) && positive(result["Dekont_ID"])
	resp := &core.PaymentResponse{
		Status:         statusOf(approved),
		PaymentID:      orderID,
		ConversationID: orderID,
		RawResponse:    map[string]any{"callback": callbackData, "payment": result},
	}
	if !approved {
		resp.ErrorCode = result["Sonuc"]
		resp.ErrorMessage = firstNonEmpty(result["Sonuc_Ack"], result["Sonuc_Str"], result["Bank_HostMsg"])
	}
	resp.Code = p.codeFor(resp.Status, resp.ErrorCode, resp.RawResponse)
	return resp, nil
}

func (p *Provider) cancelOrRefund(ctx context.Context, action, orderID, amount string) (Result, error) {
	tutar, err := FormatRefundAmount(amount)
	if err != nil {
		return nil, err
	}
	return p.sendSOAP(ctx, "TP_Islem_Iptal_Iade_Kismi2", append(p.credentials(),
		NewField("Durum", action),
		NewField("Siparis_ID", orderID),
		NewField("Tutar", tutar),
	), false)
}

// Refund refunds a payment in full or in part. PaymentID is the order id (Siparis_ID).
func (p *Provider) Refund(ctx context.Context, req *core.RefundRequest) (*core.RefundResponse, error) {
	conversationID := ""
	if req != nil {
		conversationID = req.ConversationID
	}
	fail := func(err error) (*core.RefundResponse, error) {
		resp := core.ApplyFailure(displayName, err, "Refund failed", &core.RefundResponse{})
		resp.ConversationID = conversationID
		resp.Code = p.codeFor(resp.Status, resp.ErrorCode, resp.RawResponse)
		return resp, nil
	}

	if req == nil {
		return fail(requestRequired())
	}
	if err := p.ValidateRefund(req); err != nil {
		return fail(err)
	}
	result, err := p.cancelOrRefund(ctx, "IADE", req.PaymentID, req.Price)
	if err != nil {
		return fail(err)
	}

	approved := IsSuccess(result["Sonuc"])
	resp := &core.RefundResponse{
		Status:         statusOf(approved),
		ConversationID: req.ConversationID,
		RawResponse:    result,
	}
	if approved {
		resp.RefundID = firstNonEmpty(result["Bank_Trans_ID"], req.PaymentID)
	} else {
		resp.ErrorCode = result["Sonuc"]
		resp.ErrorMessage = result["Sonuc_Str"]
	}
	resp.Code = p.codeFor(resp.Status, resp.ErrorCode, resp.RawResponse)
	return resp, nil
}

// Cancel voids a same-day payment. PaymentID is the order id (Siparis_ID). Param
// requires the full amount; when Price is not given it is read with a status
// query first.
func (p *Provider) Cancel(ctx context.Context, req *core.CancelRequest) (*core.CancelResponse, error) {
	conversationID := ""
	if req != nil {
		conversationID = req.ConversationID
	}
	fail := func(err error) (*core.CancelResponse, error) {
		resp := core.ApplyFailure(displayName, err, "Cancellation failed", &core.CancelResponse{})
		resp.ConversationID = conversationID
		resp.Code = p.codeFor(resp.Status, resp.ErrorCode, resp.RawResponse)
		return resp, nil
	}

	if req == nil {
		return fail(requestRequired())
	}
	amount := req.Price
	if amount == "" {
		status, err := p.queryOrder(ctx, req.PaymentID)
		if err != nil {
			return fail(err)
		}
		total, ok := ParseAmount(status["Toplam_Tutar"])
		if !IsSuccess(status["Sonuc"]) || !ok {
			resp := &core.CancelResponse{
				Status:         core.StatusFailure,
				ConversationID: req.ConversationID,
				ErrorMessage: firstNonEmpty(status["Sonuc_Str"],
					"Could not determine payment amount for cancellation; pass price"),
				RawResponse: status,
			}
			resp.Code = p.codeFor(resp.Status, resp.ErrorCode, resp.RawResponse)
			return resp, nil
		}
		amount = fixed2(total)
	}

	result, err := p.cancelOrRefund(ctx, "IPTAL", req.PaymentID, amount)
	if err != nil {
		return fail(err)
	}

	approved := IsSuccess(result["Sonuc"])
	resp := &core.CancelResponse{
		Status:         statusOf(approved),
		ConversationID: req.ConversationID,
		RawResponse:    result,
	}
	if approved {
		resp.TransactionID = result["Bank_Trans_ID"]
	} else {
		resp.ErrorCode = result["Sonuc"]
		resp.ErrorMessage = result["Sonuc_Str"]
	}
	resp.Code = p.codeFor(resp.Status, resp.ErrorCode, resp.RawResponse)
	return resp, nil
}

func (p *Provider) queryOrder(ctx context.Context, orderID string) (Result, error) {
	return p.sendSOAP(ctx, "TP_Islem_Sorgulama4", append(p.credentials(),
		NewField("Dekont_ID", ""),
		NewField("Siparis_ID", orderID),
		NewField("Islem_ID", ""),
	), true)
}

// GetPayment queries a payment by order id (Siparis_ID).
func (p *Provider) GetPayment(ctx context.Context, paymentID string) (*core.PaymentResponse, error) {
	fail := func(err error) (*core.PaymentResponse, error) {
		resp := core.ApplyFailure(displayName, err, "Payment inquiry failed", &core.PaymentResponse{})
		resp.PaymentID = paymentID
		resp.Code = p.codeFor(resp.Status, resp.ErrorCode, resp.RawResponse)
		return resp, nil
	}

	result, err := p.queryOrder(ctx, paymentID)
	if err != nil {
		return fail(err)
	}

	if !IsSuccess(result["Sonuc"]) {
		resp := &core.PaymentResponse{
			Status:         core.StatusFailure,
			PaymentID:      paymentID,
			ConversationID: paymentID,
			ErrorCode:      result["Sonuc"],
			ErrorMessage:   result["Sonuc_Str"],
			RawResponse:    result,
		}
		resp.Code = p.codeFor(resp.Status, resp.ErrorCode, resp.RawResponse)
		return resp, nil
	}

	orderID := firstNonEmpty(result["Siparis_ID"], paymentID)
	resp := &core.PaymentResponse{
		Status:         MapOrderStatus(result["Durum"]),
		PaymentID:      orderID,
		ConversationID: orderID,
		RawResponse:    result,
	}
	if result["Durum"] != "SUCCESS" {
		resp.ErrorMessage = result["Odeme_Sonuc_Aciklama"]
	}
	resp.Code = p.codeFor(resp.Status, resp.ErrorCode, resp.RawResponse)
	return resp, nil
}

// BinCheck looks up a BIN (BIN_SanalPos).
func (p *Provider) BinCheck(ctx context.Context, binNumber string) (*core.BinCheckResponse, error) {
	result, err := p.lookupBin(ctx, binNumber)
	if err != nil {
		return nil, err
	}
	return &core.BinCheckResponse{
		BinNumber:       firstNonEmpty(result["BIN"], binNumber),
		CardType:        result["Kart_Tip"],
		CardAssociation: result["Kart_Org"],
		CardFamily:      result["Kart_Tip"],
		BankName:        result["Kart_Banka"],
		BankCode:        int(numberOrZero(result["Banka_Kodu"])),
		Commercial:      result["Ticari_Kart"] == "1" || ticariRe.MatchString(result["Kart_Tip"]),
		RawResponse:     result,
	}, nil
}

func (p *Provider) lookupBin(ctx context.Context, binNumber string) (Result, error) {
	result, err := p.sendSOAP(ctx, "BIN_SanalPos", append(p.credentials(),
		NewField("BIN", binNumber),
	), true)
	if err != nil {
		return nil, err
	}
	if !IsSuccess(result["Sonuc"]) || result["BIN"] == "" {
		return nil, newError(firstNonEmpty(result["Sonuc_Str"], "BIN "+binNumber+" not found"))
	}
	return result, nil
}

// GetInstallmentRates lists the merchant's customer-facing commission rates per
// card program (TP_Ozel_Oran_SK_Liste). Negative rates mean "not available" and
// are left out.
func (p *Provider) GetInstallmentRates(ctx context.Context) ([]InstallmentRates, error) {
	xml, err := p.sendSOAPRaw(ctx, "TP_Ozel_Oran_SK_Liste", p.credentials(), true)
	if err != nil {
		return nil, err
	}
	result, err := ParseSOAPResponse(xml, "TP_Ozel_Oran_SK_ListeResult")
	if err != nil {
		return nil, err
	}
	if sonuc, present := result["Sonuc"]; present && !IsSuccess(sonuc) {
		return nil, newError(firstNonEmpty(result["Sonuc_Str"], "Could not list installment rates"))
	}
	rows := ParseDataSetRows(xml, "DT_Ozel_Oranlar_SK")
	programs := make([]InstallmentRates, 0, len(rows))
	for _, row := range rows {
		programs = append(programs, InstallmentRates{
			PosID:    row["SanalPOS_ID"],
			BankName: row["Kredi_Karti_Banka"],
			Rates:    RatesOf(row),
		})
	}
	return programs, nil
}

// ratesForBin finds the rates that apply to a card: the row of the card's
// virtual POS, or the "other bank cards" row when Param routes the BIN there
// (DKK = 1).
func (p *Provider) ratesForBin(ctx context.Context, binNumber string) (Result, *InstallmentRates, error) {
	bin, err := p.lookupBin(ctx, binNumber)
	if err != nil {
		return nil, nil, err
	}
	programs, err := p.GetInstallmentRates(ctx)
	if err != nil {
		return nil, nil, err
	}
	var program *InstallmentRates
	for i := range programs {
		if programs[i].PosID == bin["SanalPOS_ID"] {
			program = &programs[i]
			break
		}
	}
	if program == nil && bin["DKK"] == "1" {
		for i := range programs {
			if otherBankRe.MatchString(programs[i].BankName) {
				program = &programs[i]
				break
			}
		}
	}
	return bin, program, nil
}

// InstallmentInfo lists the installment options of a card with Param's totals
// (Toplam_Tutar = price + price x rate / 100).
func (p *Provider) InstallmentInfo(ctx context.Context, req *core.InstallmentInfoRequest) (*core.InstallmentInfoResponse, error) {
	conversationID := ""
	if req != nil {
		conversationID = req.ConversationID
	}
	fail := func(err error) (*core.InstallmentInfoResponse, error) {
		resp := core.ApplyFailure(displayName, err, "Installment info failed", &core.InstallmentInfoResponse{})
		resp.ConversationID = conversationID
		resp.Code = p.codeFor(resp.Status, resp.ErrorCode, resp.RawResponse)
		return resp, nil
	}

	if req == nil {
		return fail(requestRequired())
	}
	priceMinor, err := core.ToMinorUnits(req.Price, "amount")
	if err != nil {
		return fail(err)
	}
	if priceMinor <= 0 {
		return fail(core.NewValidationError("price must be greater than 0", providerName, nil))
	}
	bin, program, err := p.ratesForBin(ctx, req.BinNumber)
	if err != nil {
		return fail(err)
	}

	prices := make([]core.InstallmentDetail, 0, len(programRates(program)))
	for count, rate := range programRates(program) {
		totalMinor := CalculateTotalMinor(priceMinor, rate)
		commission := rate
		prices = append(prices, core.InstallmentDetail{
			InstallmentNumber: count,
			TotalPrice:        float64(totalMinor) / 100,
			InstallmentPrice:  math.Round(float64(totalMinor)/float64(count)) / 100,
			CommissionRate:    &commission,
		})
	}
	sort.Slice(prices, func(i, j int) bool { return prices[i].InstallmentNumber < prices[j].InstallmentNumber })

	raw := map[string]any{"bin": bin}
	familyName := ""
	if program != nil {
		familyName = program.BankName
		raw["program"] = program
	}
	resp := &core.InstallmentInfoResponse{
		Status: core.StatusSuccess,
		InstallmentDetails: []core.InstallmentPrice{{
			BinNumber:         firstNonEmpty(bin["BIN"], req.BinNumber),
			Price:             float64(priceMinor) / 100,
			CardType:          bin["Kart_Tip"],
			CardAssociation:   bin["Kart_Org"],
			CardFamilyName:    firstNonEmpty(familyName, bin["Kart_Banka"]),
			BankCode:          int(numberOrZero(bin["Banka_Kodu"])),
			BankName:          bin["Kart_Banka"],
			Commercial:        boolToInt(bin["Ticari_Kart"] == "1"),
			InstallmentPrices: prices,
		}},
		ConversationID: conversationID,
		RawResponse:    raw,
	}
	resp.Code = p.codeFor(resp.Status, resp.ErrorCode, resp.RawResponse)
	return resp, nil
}

// CalculatePaidPrice returns the amount to charge (PaidPrice, Param's
// Toplam_Tutar) for a card and installment count, using the merchant's rates. It
// returns a ValidationError when the installment count is not available.
func (p *Provider) CalculatePaidPrice(ctx context.Context, req PaidPriceRequest) (string, error) {
	installment := req.Installment
	if installment == 0 {
		installment = 1
	}
	priceMinor, err := core.ToMinorUnits(req.Price, "amount")
	if err != nil {
		return "", err
	}
	_, program, err := p.ratesForBin(ctx, req.BinNumber)
	if err != nil {
		return "", err
	}
	rate, available := programRates(program)[installment]
	if !available {
		return "", core.NewValidationError(
			fmt.Sprintf("installment %d is not available for BIN %s", installment, req.BinNumber),
			providerName,
			[]core.ValidationIssue{{Path: "installment", Message: "is not available for this card"}},
		)
	}
	return fixed2(float64(CalculateTotalMinor(priceMinor, rate)) / 100), nil
}

func programRates(program *InstallmentRates) map[int]float64 {
	if program == nil {
		return nil
	}
	return program.Rates
}

var (
	ticariRe    = regexp.MustCompile(`(?i)ticari`)
	otherBankRe = regexp.MustCompile(`(?i)diğer|diger|other`)
)

func statusOf(approved bool) core.PaymentStatus {
	if approved {
		return core.StatusSuccess
	}
	return core.StatusFailure
}

func boolToInt(value bool) int {
	if value {
		return 1
	}
	return 0
}

func firstNonEmpty(values ...string) string {
	for _, v := range values {
		if v != "" {
			return v
		}
	}
	return ""
}

func paidPriceOf(req *core.PaymentRequest) string {
	if req.PaidPrice != "" {
		return req.PaidPrice
	}
	return req.Price
}

func positive(value string) bool {
	n, ok := numberOf(value)
	return ok && n > 0
}

func numberOrZero(value string) float64 {
	n, _ := numberOf(value)
	return n
}

func requestRequired() error {
	return core.NewValidationError("request is required", providerName, []core.ValidationIssue{
		{Path: "request", Message: "is required"},
	})
}
