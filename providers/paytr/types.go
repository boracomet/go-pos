package paytr

// BasketItem is one line of a PayTR basket. Price is the unit price in TL.
type BasketItem struct {
	Name     string `json:"name"`
	Price    string `json:"price"`
	Quantity int    `json:"quantity,omitempty"`
}

// CallbackData is the body PayTR POSTs to the notification (Bildirim) URL.
// TotalAmount and PaymentAmount are in kuruş.
type CallbackData struct {
	MerchantOid      string `json:"merchant_oid"`
	Status           string `json:"status"`
	TotalAmount      string `json:"total_amount"`
	Hash             string `json:"hash"`
	FailedReasonCode string `json:"failed_reason_code,omitempty"`
	FailedReasonMsg  string `json:"failed_reason_msg,omitempty"`
	TestMode         string `json:"test_mode,omitempty"`
	PaymentType      string `json:"payment_type,omitempty"`
	Currency         string `json:"currency,omitempty"`
	PaymentAmount    string `json:"payment_amount,omitempty"`
	InstallmentCount string `json:"installment_count,omitempty"`
	MerchantID       string `json:"merchant_id,omitempty"`
	Utoken           string `json:"utoken,omitempty"`
}

// TokenPaymentBuyer is the customer a stored-card payment is made for.
type TokenPaymentBuyer struct {
	Email     string `json:"email"`
	Name      string `json:"name"`
	Surname   string `json:"surname"`
	IP        string `json:"ip"`
	GSMNumber string `json:"gsmNumber"`
	Address   string `json:"address,omitempty"`
}

// TokenPaymentRequest pays with a stored card (Kart Saklama). The feature must
// be enabled on the PayTR account.
type TokenPaymentRequest struct {
	// Utoken is the user token returned in the first successful payment's notification.
	Utoken string `json:"utoken"`
	// Ctoken is the card token from the stored card list.
	Ctoken string `json:"ctoken"`
	// CVV is required when the stored card has require_cvv = 1.
	CVV            string            `json:"cvv,omitempty"`
	Price          string            `json:"price"`
	CallbackURL    string            `json:"callbackUrl"`
	FailURL        string            `json:"failUrl,omitempty"`
	ConversationID string            `json:"conversationId,omitempty"`
	Buyer          TokenPaymentBuyer `json:"buyer"`
	BasketItems    []BasketItem      `json:"basketItems"`
	Currency       string            `json:"currency,omitempty"`
	Installment    int               `json:"installment,omitempty"`
}

type apiIframeResponse struct {
	Status string `json:"status"`
	Reason string `json:"reason"`
	Token  string `json:"token"`
}

type apiRefundResponse struct {
	Status       string `json:"status"`
	IsTest       any    `json:"is_test"`
	MerchantOid  string `json:"merchant_oid"`
	ReturnAmount string `json:"return_amount"`
	ReferenceNo  string `json:"reference_no"`
	ErrNo        any    `json:"err_no"`
	ErrMsg       string `json:"err_msg"`
}

type apiDirectPaymentResponse struct {
	Status           string `json:"status"`
	Msg              string `json:"msg"`
	Reason           string `json:"reason"`
	MerchantOid      string `json:"merchant_oid"`
	TotalAmount      string `json:"total_amount"`
	FailedReasonCode any    `json:"failed_reason_code"`
	FailedReasonMsg  string `json:"failed_reason_msg"`
	ErrNo            any    `json:"err_no"`
	ErrMsg           string `json:"err_msg"`
	Utoken           any    `json:"utoken"`
}

type apiStatusReturn struct {
	ReturnAmount any    `json:"return_amount"`
	RefundAmount any    `json:"refund_amount"`
	ReturnDate   string `json:"return_date"`
}

type apiStatusResponse struct {
	Status        string            `json:"status"`
	PaymentAmount any               `json:"payment_amount"`
	PaymentTotal  any               `json:"payment_total"`
	PaymentDate   string            `json:"payment_date"`
	Currency      string            `json:"currency"`
	Taksit        string            `json:"taksit"`
	KartMarka     string            `json:"kart_marka"`
	MaskedPan     string            `json:"masked_pan"`
	OdemeTipi     string            `json:"odeme_tipi"`
	TestMode      string            `json:"test_mode"`
	Returns       []apiStatusReturn `json:"returns"`
	ErrNo         any               `json:"err_no"`
	ErrMsg        string            `json:"err_msg"`
}

type apiBinDetailResponse struct {
	Status       string `json:"status"`
	ErrMsg       string `json:"err_msg"`
	Bank         string `json:"bank"`
	BankCode     any    `json:"bankCode"`
	Brand        string `json:"brand"`
	CardType     string `json:"cardType"`
	Schema       string `json:"schema"`
	BusinessCard string `json:"businessCard"`
}

type apiInstallmentRatesResponse struct {
	Status        string                    `json:"status"`
	ErrMsg        string                    `json:"err_msg"`
	Oranlar       map[string]map[string]any `json:"oranlar"`
	MaxInstNonBus *int                      `json:"max_inst_non_bus"`
}
