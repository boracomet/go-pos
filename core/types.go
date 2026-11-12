// Package core holds the provider-independent payment contract: request and
// response types, normalized error codes, the provider interface and the shared
// HTTP/crypto helpers.
package core

// PaymentStatus is the normalized outcome of a provider operation.
type PaymentStatus string

const (
	StatusSuccess   PaymentStatus = "success"
	StatusFailure   PaymentStatus = "failure"
	StatusPending   PaymentStatus = "pending"
	StatusCancelled PaymentStatus = "cancelled"
)

// Currency is an ISO 4217 code. Providers also accept codes outside this set.
type Currency string

const (
	CurrencyTRY Currency = "TRY"
	CurrencyUSD Currency = "USD"
	CurrencyEUR Currency = "EUR"
	CurrencyGBP Currency = "GBP"
)

// BasketItemType distinguishes physical goods from virtual ones.
type BasketItemType string

const (
	BasketItemPhysical BasketItemType = "PHYSICAL"
	BasketItemVirtual  BasketItemType = "VIRTUAL"
)

// PaymentCard is the card to charge.
type PaymentCard struct {
	CardHolderName string `json:"cardHolderName"`
	CardNumber     string `json:"cardNumber"`
	ExpireMonth    string `json:"expireMonth"`
	ExpireYear     string `json:"expireYear"`
	CVC            string `json:"cvc"`
	RegisterCard   *bool  `json:"registerCard,omitempty"`
}

// Address is a shipping or billing address.
type Address struct {
	ContactName string `json:"contactName"`
	City        string `json:"city"`
	Country     string `json:"country"`
	Address     string `json:"address"`
	ZipCode     string `json:"zipCode,omitempty"`
}

// Buyer identifies the customer.
type Buyer struct {
	ID                  string `json:"id"`
	Name                string `json:"name"`
	Surname             string `json:"surname"`
	Email               string `json:"email"`
	IdentityNumber      string `json:"identityNumber"`
	RegistrationAddress string `json:"registrationAddress"`
	City                string `json:"city"`
	Country             string `json:"country"`
	ZipCode             string `json:"zipCode,omitempty"`
	IP                  string `json:"ip"`
	GSMNumber           string `json:"gsmNumber"`
}

// BasketItem is one line of the order.
type BasketItem struct {
	ID        string `json:"id"`
	Name      string `json:"name"`
	Category1 string `json:"category1"`
	Category2 string `json:"category2,omitempty"`
	ItemType  string `json:"itemType"`
	Price     string `json:"price"`
}

// SaveCardOption mirrors `saveCard?: boolean | { customerToken?, alias? }`.
// A non-nil value with Enabled true saves the card during the payment.
type SaveCardOption struct {
	Enabled       bool   `json:"-"`
	CustomerToken string `json:"customerToken,omitempty"`
	Alias         string `json:"alias,omitempty"`
}

// StoredCardReference pays with a card saved earlier instead of PaymentCard.
type StoredCardReference struct {
	CustomerToken string `json:"customerToken"`
	CardToken     string `json:"cardToken"`
	CVC           string `json:"cvc,omitempty"`
}

// SavedCardTokens are the tokens returned when a card is saved.
type SavedCardTokens struct {
	CustomerToken string `json:"customerToken"`
	CardToken     string `json:"cardToken,omitempty"`
}

// StoredCard is a saved card as listed by the provider; never the full number.
type StoredCard struct {
	CardToken       string `json:"cardToken"`
	Alias           string `json:"alias,omitempty"`
	BinNumber       string `json:"binNumber,omitempty"`
	LastFourDigits  string `json:"lastFourDigits,omitempty"`
	ExpireMonth     string `json:"expireMonth,omitempty"`
	ExpireYear      string `json:"expireYear,omitempty"`
	CardType        string `json:"cardType,omitempty"`
	CardAssociation string `json:"cardAssociation,omitempty"`
	CardFamily      string `json:"cardFamily,omitempty"`
	BankName        string `json:"bankName,omitempty"`
	RequiresCVC     *bool  `json:"requiresCvc,omitempty"`
}

// PaymentRequest is a direct (non-3D) payment.
type PaymentRequest struct {
	Price           string               `json:"price"`
	PaidPrice       string               `json:"paidPrice"`
	Currency        string               `json:"currency"`
	BasketID        string               `json:"basketId"`
	PaymentCard     *PaymentCard         `json:"paymentCard,omitempty"`
	StoredCard      *StoredCardReference `json:"storedCard,omitempty"`
	SaveCard        *SaveCardOption      `json:"saveCard,omitempty"`
	Buyer           Buyer                `json:"buyer"`
	ShippingAddress Address              `json:"shippingAddress"`
	BillingAddress  Address              `json:"billingAddress"`
	BasketItems     []BasketItem         `json:"basketItems"`
	CallbackURL     string               `json:"callbackUrl,omitempty"`
	ConversationID  string               `json:"conversationId,omitempty"`
	Installment     int                  `json:"installment,omitempty"`
}

// PaymentResponse is the normalized result of a payment operation.
type PaymentResponse struct {
	Status          PaymentStatus    `json:"status"`
	PaymentID       string           `json:"paymentId,omitempty"`
	ConversationID  string           `json:"conversationId,omitempty"`
	Code            PaymentErrorCode `json:"code,omitempty"`
	ErrorCode       string           `json:"errorCode,omitempty"`
	ErrorMessage    string           `json:"errorMessage,omitempty"`
	ProviderMessage string           `json:"providerMessage,omitempty"`
	ErrorGroup      string           `json:"errorGroup,omitempty"`
	StoredCard      *SavedCardTokens `json:"storedCard,omitempty"`
	RawResponse     any              `json:"rawResponse,omitempty"`
	HTTPStatus      int              `json:"httpStatus,omitempty"`
	Attempts        int              `json:"attempts,omitempty"`
}

// ThreeDSPaymentRequest is a payment that requires 3D Secure. CallbackURL is
// mandatory; it is the embedded PaymentRequest field, not a separate one.
type ThreeDSPaymentRequest struct {
	PaymentRequest
}

// ThreeDSInitResponse carries the HTML or URL that starts the 3D Secure flow.
type ThreeDSInitResponse struct {
	Status             PaymentStatus    `json:"status"`
	ThreeDSHTMLContent string           `json:"threeDSHtmlContent,omitempty"`
	RedirectURL        string           `json:"redirectUrl,omitempty"`
	PaymentID          string           `json:"paymentId,omitempty"`
	ConversationID     string           `json:"conversationId,omitempty"`
	Code               PaymentErrorCode `json:"code,omitempty"`
	ErrorCode          string           `json:"errorCode,omitempty"`
	ErrorMessage       string           `json:"errorMessage,omitempty"`
	ProviderMessage    string           `json:"providerMessage,omitempty"`
	RawResponse        any              `json:"rawResponse,omitempty"`
	HTTPStatus         int              `json:"httpStatus,omitempty"`
	Attempts           int              `json:"attempts,omitempty"`
}

// RefundRequest refunds a payment, or one basket item when PaymentTransactionID is set.
type RefundRequest struct {
	PaymentID            string `json:"paymentId"`
	PaymentTransactionID string `json:"paymentTransactionId,omitempty"`
	Price                string `json:"price"`
	Currency             string `json:"currency"`
	IP                   string `json:"ip"`
	ConversationID       string `json:"conversationId,omitempty"`
}

// RefundResponse is the normalized refund result.
type RefundResponse struct {
	Status          PaymentStatus    `json:"status"`
	RefundID        string           `json:"refundId,omitempty"`
	ConversationID  string           `json:"conversationId,omitempty"`
	Code            PaymentErrorCode `json:"code,omitempty"`
	ErrorCode       string           `json:"errorCode,omitempty"`
	ErrorMessage    string           `json:"errorMessage,omitempty"`
	ProviderMessage string           `json:"providerMessage,omitempty"`
	RawResponse     any              `json:"rawResponse,omitempty"`
	HTTPStatus      int              `json:"httpStatus,omitempty"`
	Attempts        int              `json:"attempts,omitempty"`
}

// CancelRequest cancels a payment. Price is required by some providers.
type CancelRequest struct {
	PaymentID      string `json:"paymentId"`
	IP             string `json:"ip"`
	ConversationID string `json:"conversationId,omitempty"`
	Price          string `json:"price,omitempty"`
	Currency       string `json:"currency,omitempty"`
}

// CancelResponse is the normalized cancellation result.
type CancelResponse struct {
	Status          PaymentStatus    `json:"status"`
	TransactionID   string           `json:"transactionId,omitempty"`
	VoidID          string           `json:"voidId,omitempty"`
	ConversationID  string           `json:"conversationId,omitempty"`
	Code            PaymentErrorCode `json:"code,omitempty"`
	ErrorCode       string           `json:"errorCode,omitempty"`
	ErrorMessage    string           `json:"errorMessage,omitempty"`
	ProviderMessage string           `json:"providerMessage,omitempty"`
	RawResponse     any              `json:"rawResponse,omitempty"`
	HTTPStatus      int              `json:"httpStatus,omitempty"`
	Attempts        int              `json:"attempts,omitempty"`
}

// CaptureRequest charges all or part of a pre-authorized amount.
type CaptureRequest struct {
	PaymentID      string `json:"paymentId"`
	Amount         string `json:"amount"`
	Currency       string `json:"currency,omitempty"`
	IP             string `json:"ip"`
	ConversationID string `json:"conversationId,omitempty"`
}

// VoidAuthorizationRequest releases a pre-authorization without charging it.
type VoidAuthorizationRequest struct {
	PaymentID      string `json:"paymentId"`
	IP             string `json:"ip"`
	ConversationID string `json:"conversationId,omitempty"`
}

// SaveCardRequest stores a card with the provider without charging it.
type SaveCardRequest struct {
	CustomerToken  string      `json:"customerToken,omitempty"`
	ExternalID     string      `json:"externalId,omitempty"`
	Email          string      `json:"email,omitempty"`
	Alias          string      `json:"alias,omitempty"`
	Card           PaymentCard `json:"card"`
	ConversationID string      `json:"conversationId,omitempty"`
}

// SaveCardResponse is the result of storing a card.
type SaveCardResponse struct {
	Status          PaymentStatus    `json:"status"`
	CustomerToken   string           `json:"customerToken,omitempty"`
	Card            *StoredCard      `json:"card,omitempty"`
	Code            PaymentErrorCode `json:"code,omitempty"`
	ErrorCode       string           `json:"errorCode,omitempty"`
	ErrorMessage    string           `json:"errorMessage,omitempty"`
	ProviderMessage string           `json:"providerMessage,omitempty"`
	RawResponse     any              `json:"rawResponse,omitempty"`
}

// ListCardsRequest asks for a customer's saved cards.
type ListCardsRequest struct {
	CustomerToken string `json:"customerToken"`
}

// ListCardsResponse is a customer's saved cards.
type ListCardsResponse struct {
	Status          PaymentStatus    `json:"status"`
	CustomerToken   string           `json:"customerToken,omitempty"`
	Cards           []StoredCard     `json:"cards"`
	Code            PaymentErrorCode `json:"code,omitempty"`
	ErrorCode       string           `json:"errorCode,omitempty"`
	ErrorMessage    string           `json:"errorMessage,omitempty"`
	ProviderMessage string           `json:"providerMessage,omitempty"`
	RawResponse     any              `json:"rawResponse,omitempty"`
}

// DeleteCardRequest removes a saved card.
type DeleteCardRequest struct {
	CustomerToken  string `json:"customerToken"`
	CardToken      string `json:"cardToken"`
	ConversationID string `json:"conversationId,omitempty"`
}

// DeleteCardResponse is the result of removing a saved card.
type DeleteCardResponse struct {
	Status          PaymentStatus    `json:"status"`
	Code            PaymentErrorCode `json:"code,omitempty"`
	ErrorCode       string           `json:"errorCode,omitempty"`
	ErrorMessage    string           `json:"errorMessage,omitempty"`
	ProviderMessage string           `json:"providerMessage,omitempty"`
	RawResponse     any              `json:"rawResponse,omitempty"`
}

// BinCheckResponse describes the issuer of a BIN.
type BinCheckResponse struct {
	BinNumber       string `json:"binNumber"`
	CardType        string `json:"cardType"`
	CardAssociation string `json:"cardAssociation"`
	CardFamily      string `json:"cardFamily"`
	BankName        string `json:"bankName"`
	BankCode        int    `json:"bankCode"`
	Commercial      bool   `json:"commercial"`
	RawResponse     any    `json:"rawResponse,omitempty"`
}

// InstallmentDetail is one installment option.
type InstallmentDetail struct {
	InstallmentNumber int      `json:"installmentNumber"`
	TotalPrice        float64  `json:"totalPrice"`
	InstallmentPrice  float64  `json:"installmentPrice"`
	CommissionRate    *float64 `json:"commissionRate,omitempty"`
}

// InstallmentPrice groups installment options by card BIN.
type InstallmentPrice struct {
	BinNumber         string              `json:"binNumber"`
	Price             float64             `json:"price"`
	CardType          string              `json:"cardType"`
	CardAssociation   string              `json:"cardAssociation"`
	CardFamilyName    string              `json:"cardFamilyName"`
	Force3DS          *int                `json:"force3ds,omitempty"`
	BankCode          int                 `json:"bankCode"`
	BankName          string              `json:"bankName"`
	ForceCVC          *int                `json:"forceCvc,omitempty"`
	Commercial        int                 `json:"commercial"`
	InstallmentPrices []InstallmentDetail `json:"installmentPrices"`
}

// InstallmentInfoRequest queries installment options for a BIN and amount.
type InstallmentInfoRequest struct {
	BinNumber      string `json:"binNumber"`
	Price          string `json:"price"`
	ConversationID string `json:"conversationId,omitempty"`
}

// InstallmentInfoResponse is the normalized installment result.
type InstallmentInfoResponse struct {
	Status             PaymentStatus      `json:"status"`
	InstallmentDetails []InstallmentPrice `json:"installmentDetails,omitempty"`
	ConversationID     string             `json:"conversationId,omitempty"`
	Code               PaymentErrorCode   `json:"code,omitempty"`
	ErrorCode          string             `json:"errorCode,omitempty"`
	ErrorMessage       string             `json:"errorMessage,omitempty"`
	ProviderMessage    string             `json:"providerMessage,omitempty"`
	RawResponse        any                `json:"rawResponse,omitempty"`
}

// CheckoutFormRequest starts a hosted checkout form.
type CheckoutFormRequest struct {
	Price               string       `json:"price"`
	PaidPrice           string       `json:"paidPrice"`
	Currency            string       `json:"currency"`
	BasketID            string       `json:"basketId"`
	CallbackURL         string       `json:"callbackUrl"`
	EnabledInstallments []int        `json:"enabledInstallments,omitempty"`
	Buyer               Buyer        `json:"buyer"`
	ShippingAddress     Address      `json:"shippingAddress"`
	BillingAddress      Address      `json:"billingAddress"`
	BasketItems         []BasketItem `json:"basketItems"`
	ConversationID      string       `json:"conversationId,omitempty"`
}

// CheckoutFormInitResponse carries the hosted form content or URL.
type CheckoutFormInitResponse struct {
	Status              PaymentStatus    `json:"status"`
	CheckoutFormContent string           `json:"checkoutFormContent,omitempty"`
	PaymentPageURL      string           `json:"paymentPageUrl,omitempty"`
	Token               string           `json:"token,omitempty"`
	TokenExpireTime     int64            `json:"tokenExpireTime,omitempty"`
	ConversationID      string           `json:"conversationId,omitempty"`
	Code                PaymentErrorCode `json:"code,omitempty"`
	ErrorCode           string           `json:"errorCode,omitempty"`
	ErrorMessage        string           `json:"errorMessage,omitempty"`
	ProviderMessage     string           `json:"providerMessage,omitempty"`
	RawResponse         any              `json:"rawResponse,omitempty"`
}

// CheckoutFormRetrieveResponse is the outcome of a hosted checkout form.
type CheckoutFormRetrieveResponse struct {
	Status                       PaymentStatus    `json:"status"`
	PaymentID                    string           `json:"paymentId,omitempty"`
	PaymentStatus                string           `json:"paymentStatus,omitempty"`
	Price                        *float64         `json:"price,omitempty"`
	PaidPrice                    *float64         `json:"paidPrice,omitempty"`
	Currency                     string           `json:"currency,omitempty"`
	BasketID                     string           `json:"basketId,omitempty"`
	Installment                  *int             `json:"installment,omitempty"`
	BinNumber                    string           `json:"binNumber,omitempty"`
	LastFourDigits               string           `json:"lastFourDigits,omitempty"`
	CardType                     string           `json:"cardType,omitempty"`
	CardAssociation              string           `json:"cardAssociation,omitempty"`
	CardFamily                   string           `json:"cardFamily,omitempty"`
	CardToken                    string           `json:"cardToken,omitempty"`
	CardUserKey                  string           `json:"cardUserKey,omitempty"`
	FraudStatus                  *int             `json:"fraudStatus,omitempty"`
	MerchantCommissionRate       *float64         `json:"merchantCommissionRate,omitempty"`
	MerchantCommissionRateAmount *float64         `json:"merchantCommissionRateAmount,omitempty"`
	IyziCommissionRateAmount     *float64         `json:"iyziCommissionRateAmount,omitempty"`
	IyziCommissionFee            *float64         `json:"iyziCommissionFee,omitempty"`
	PaymentTransactionID         string           `json:"paymentTransactionId,omitempty"`
	ConversationID               string           `json:"conversationId,omitempty"`
	Code                         PaymentErrorCode `json:"code,omitempty"`
	ErrorCode                    string           `json:"errorCode,omitempty"`
	ErrorMessage                 string           `json:"errorMessage,omitempty"`
	ProviderMessage              string           `json:"providerMessage,omitempty"`
	RawResponse                  any              `json:"rawResponse,omitempty"`
}

// PWIPaymentStatus is the state of a protected bank transfer.
type PWIPaymentStatus string

const (
	PWIWaiting PWIPaymentStatus = "WAITING"
	PWISuccess PWIPaymentStatus = "SUCCESS"
	PWIFailure PWIPaymentStatus = "FAILURE"
)

// PWIPaymentRequest starts a Payment With IBAN (protected bank transfer).
type PWIPaymentRequest struct {
	Price           string       `json:"price"`
	PaidPrice       string       `json:"paidPrice"`
	Currency        string       `json:"currency"`
	BasketID        string       `json:"basketId"`
	CallbackURL     string       `json:"callbackUrl"`
	Buyer           Buyer        `json:"buyer"`
	ShippingAddress Address      `json:"shippingAddress"`
	BillingAddress  Address      `json:"billingAddress"`
	BasketItems     []BasketItem `json:"basketItems"`
	ConversationID  string       `json:"conversationId,omitempty"`
}

// PWIPaymentInitResponse carries the transfer instructions page.
type PWIPaymentInitResponse struct {
	Status          PaymentStatus    `json:"status"`
	HTMLContent     string           `json:"htmlContent,omitempty"`
	Token           string           `json:"token,omitempty"`
	TokenExpireTime int64            `json:"tokenExpireTime,omitempty"`
	PaymentPageURL  string           `json:"paymentPageUrl,omitempty"`
	ConversationID  string           `json:"conversationId,omitempty"`
	Code            PaymentErrorCode `json:"code,omitempty"`
	ErrorCode       string           `json:"errorCode,omitempty"`
	ErrorMessage    string           `json:"errorMessage,omitempty"`
	ProviderMessage string           `json:"providerMessage,omitempty"`
	RawResponse     any              `json:"rawResponse,omitempty"`
}

// PWIPaymentRetrieveResponse is the state of a protected bank transfer.
type PWIPaymentRetrieveResponse struct {
	Status                       PaymentStatus    `json:"status"`
	Token                        string           `json:"token,omitempty"`
	CallbackURL                  string           `json:"callbackUrl,omitempty"`
	PaymentStatus                string           `json:"paymentStatus,omitempty"`
	PaymentID                    string           `json:"paymentId,omitempty"`
	Price                        *float64         `json:"price,omitempty"`
	PaidPrice                    *float64         `json:"paidPrice,omitempty"`
	Currency                     string           `json:"currency,omitempty"`
	BasketID                     string           `json:"basketId,omitempty"`
	MerchantCommissionRate       *float64         `json:"merchantCommissionRate,omitempty"`
	MerchantCommissionRateAmount *float64         `json:"merchantCommissionRateAmount,omitempty"`
	IyziCommissionRateAmount     *float64         `json:"iyziCommissionRateAmount,omitempty"`
	IyziCommissionFee            *float64         `json:"iyziCommissionFee,omitempty"`
	IBAN                         string           `json:"iban,omitempty"`
	BankName                     string           `json:"bankName,omitempty"`
	BuyerName                    string           `json:"buyerName,omitempty"`
	BuyerSurname                 string           `json:"buyerSurname,omitempty"`
	BuyerEmail                   string           `json:"buyerEmail,omitempty"`
	ConversationID               string           `json:"conversationId,omitempty"`
	Code                         PaymentErrorCode `json:"code,omitempty"`
	ErrorCode                    string           `json:"errorCode,omitempty"`
	ErrorMessage                 string           `json:"errorMessage,omitempty"`
	ProviderMessage              string           `json:"providerMessage,omitempty"`
	RawResponse                  any              `json:"rawResponse,omitempty"`
}
