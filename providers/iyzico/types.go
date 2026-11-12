package iyzico

import "github.com/boracomet/go-pos/core"

// ThreeDSCallbackData is the body iyzico POSTs to the 3D Secure callbackUrl.
// It is unsigned, so its values are untrusted.
type ThreeDSCallbackData struct {
	Status           string `json:"status,omitempty"`
	PaymentID        string `json:"paymentId,omitempty"`
	ConversationID   string `json:"conversationId,omitempty"`
	ConversationData string `json:"conversationData,omitempty"`
	// MDStatus is "1" only when 3D authentication succeeded. iyzico sends it as
	// a string or a number.
	MDStatus any `json:"mdStatus,omitempty"`
}

// apiResponse is the envelope every iyzico endpoint returns.
type apiResponse struct {
	Status         string `json:"status"`
	Locale         string `json:"locale"`
	SystemTime     int64  `json:"systemTime"`
	ConversationID string `json:"conversationId"`
	ErrorCode      string `json:"errorCode"`
	ErrorMessage   string `json:"errorMessage"`
	ErrorGroup     string `json:"errorGroup"`
}

type apiPaymentCard struct {
	CardHolderName string `json:"cardHolderName,omitempty"`
	CardNumber     string `json:"cardNumber,omitempty"`
	ExpireMonth    string `json:"expireMonth,omitempty"`
	ExpireYear     string `json:"expireYear,omitempty"`
	CVC            string `json:"cvc,omitempty"`
	RegisterCard   *int   `json:"registerCard,omitempty"`
	CardAlias      string `json:"cardAlias,omitempty"`
	CardUserKey    string `json:"cardUserKey,omitempty"`
	CardToken      string `json:"cardToken,omitempty"`
}

type apiStoredCardDetails struct {
	CardToken       string `json:"cardToken"`
	CardAlias       string `json:"cardAlias"`
	BinNumber       string `json:"binNumber"`
	LastFourDigits  string `json:"lastFourDigits"`
	CardType        string `json:"cardType"`
	CardAssociation string `json:"cardAssociation"`
	CardFamily      string `json:"cardFamily"`
	CardBankName    string `json:"cardBankName"`
	CardBankCode    int    `json:"cardBankCode"`
}

type apiCardResponse struct {
	apiResponse
	apiStoredCardDetails
	CardUserKey string `json:"cardUserKey"`
	ExternalID  string `json:"externalId"`
	Email       string `json:"email"`
}

type apiCardListResponse struct {
	apiResponse
	CardUserKey string                 `json:"cardUserKey"`
	CardDetails []apiStoredCardDetails `json:"cardDetails"`
}

type apiBuyer struct {
	ID                  string `json:"id"`
	Name                string `json:"name"`
	Surname             string `json:"surname"`
	GSMNumber           string `json:"gsmNumber"`
	Email               string `json:"email"`
	IdentityNumber      string `json:"identityNumber"`
	RegistrationAddress string `json:"registrationAddress"`
	IP                  string `json:"ip"`
	City                string `json:"city"`
	Country             string `json:"country"`
	ZipCode             string `json:"zipCode,omitempty"`
}

type apiAddress struct {
	ContactName string `json:"contactName"`
	City        string `json:"city"`
	Country     string `json:"country"`
	Address     string `json:"address"`
	ZipCode     string `json:"zipCode,omitempty"`
}

type apiBasketItem struct {
	ID        string `json:"id"`
	Name      string `json:"name"`
	Category1 string `json:"category1"`
	Category2 string `json:"category2,omitempty"`
	ItemType  string `json:"itemType"`
	Price     string `json:"price"`
}

type apiPaymentRequest struct {
	Locale          string          `json:"locale"`
	ConversationID  string          `json:"conversationId,omitempty"`
	Price           string          `json:"price"`
	PaidPrice       string          `json:"paidPrice"`
	Currency        string          `json:"currency"`
	Installment     int             `json:"installment"`
	BasketID        string          `json:"basketId"`
	PaymentChannel  string          `json:"paymentChannel"`
	PaymentGroup    string          `json:"paymentGroup"`
	PaymentCard     apiPaymentCard  `json:"paymentCard"`
	Buyer           apiBuyer        `json:"buyer"`
	ShippingAddress apiAddress      `json:"shippingAddress"`
	BillingAddress  apiAddress      `json:"billingAddress"`
	BasketItems     []apiBasketItem `json:"basketItems"`
	CallbackURL     string          `json:"callbackUrl,omitempty"`
}

type apiCheckoutFormRequest struct {
	Locale              string          `json:"locale"`
	ConversationID      string          `json:"conversationId,omitempty"`
	Price               string          `json:"price"`
	PaidPrice           string          `json:"paidPrice"`
	Currency            string          `json:"currency"`
	BasketID            string          `json:"basketId"`
	PaymentGroup        string          `json:"paymentGroup"`
	PaymentChannel      string          `json:"paymentChannel"`
	CallbackURL         string          `json:"callbackUrl"`
	EnabledInstallments []int           `json:"enabledInstallments,omitempty"`
	Buyer               apiBuyer        `json:"buyer"`
	ShippingAddress     apiAddress      `json:"shippingAddress"`
	BillingAddress      apiAddress      `json:"billingAddress"`
	BasketItems         []apiBasketItem `json:"basketItems"`
}

type apiPWIRequest struct {
	Locale          string          `json:"locale"`
	ConversationID  string          `json:"conversationId,omitempty"`
	Price           string          `json:"price"`
	PaidPrice       string          `json:"paidPrice"`
	Currency        string          `json:"currency"`
	BasketID        string          `json:"basketId"`
	PaymentGroup    string          `json:"paymentGroup"`
	CallbackURL     string          `json:"callbackUrl"`
	Buyer           apiBuyer        `json:"buyer"`
	ShippingAddress apiAddress      `json:"shippingAddress"`
	BillingAddress  apiAddress      `json:"billingAddress"`
	BasketItems     []apiBasketItem `json:"basketItems"`
}

type apiThreeDSAuthRequest struct {
	Locale           string `json:"locale"`
	ConversationID   string `json:"conversationId,omitempty"`
	PaymentID        string `json:"paymentId"`
	ConversationData string `json:"conversationData,omitempty"`
}

type apiRefundRequest struct {
	Locale               string `json:"locale"`
	ConversationID       string `json:"conversationId,omitempty"`
	Price                string `json:"price"`
	Currency             string `json:"currency"`
	IP                   string `json:"ip"`
	PaymentTransactionID string `json:"paymentTransactionId,omitempty"`
	PaymentID            string `json:"paymentId,omitempty"`
}

type apiCancelRequest struct {
	Locale         string `json:"locale"`
	ConversationID string `json:"conversationId,omitempty"`
	PaymentID      string `json:"paymentId"`
	IP             string `json:"ip"`
}

type apiCaptureRequest struct {
	Locale         string `json:"locale"`
	ConversationID string `json:"conversationId,omitempty"`
	PaymentID      string `json:"paymentId"`
	IP             string `json:"ip"`
	PaidPrice      string `json:"paidPrice"`
	Currency       string `json:"currency"`
}

type apiPaymentDetailRequest struct {
	Locale    string `json:"locale"`
	PaymentID string `json:"paymentId"`
}

type apiSaveCardDetails struct {
	CardAlias      string `json:"cardAlias,omitempty"`
	CardHolderName string `json:"cardHolderName,omitempty"`
	CardNumber     string `json:"cardNumber,omitempty"`
	ExpireMonth    string `json:"expireMonth,omitempty"`
	ExpireYear     string `json:"expireYear,omitempty"`
}

type apiSaveCardRequest struct {
	Locale         string             `json:"locale"`
	ConversationID string             `json:"conversationId,omitempty"`
	ExternalID     string             `json:"externalId,omitempty"`
	Email          string             `json:"email,omitempty"`
	CardUserKey    string             `json:"cardUserKey,omitempty"`
	Card           apiSaveCardDetails `json:"card"`
}

type apiListCardsRequest struct {
	Locale         string `json:"locale"`
	ConversationID string `json:"conversationId,omitempty"`
	CardUserKey    string `json:"cardUserKey"`
}

type apiDeleteCardRequest struct {
	Locale         string `json:"locale"`
	ConversationID string `json:"conversationId,omitempty"`
	CardUserKey    string `json:"cardUserKey"`
	CardToken      string `json:"cardToken"`
}

type apiBinCheckRequest struct {
	Locale         string `json:"locale,omitempty"`
	ConversationID string `json:"conversationId,omitempty"`
	BinNumber      string `json:"binNumber"`
}

type apiInstallmentInfoRequest struct {
	Locale         string `json:"locale"`
	ConversationID string `json:"conversationId,omitempty"`
	BinNumber      string `json:"binNumber"`
	Price          string `json:"price"`
}

type apiTokenDetailRequest struct {
	Locale         string `json:"locale"`
	ConversationID string `json:"conversationId,omitempty"`
	Token          string `json:"token"`
}

type apiPaymentResponse struct {
	apiResponse
	PaymentID                    string   `json:"paymentId"`
	Price                        *float64 `json:"price"`
	PaidPrice                    *float64 `json:"paidPrice"`
	Currency                     string   `json:"currency"`
	BasketID                     string   `json:"basketId"`
	BinNumber                    string   `json:"binNumber"`
	LastFourDigits               string   `json:"lastFourDigits"`
	CardType                     string   `json:"cardType"`
	CardAssociation              string   `json:"cardAssociation"`
	CardFamily                   string   `json:"cardFamily"`
	CardToken                    string   `json:"cardToken"`
	CardUserKey                  string   `json:"cardUserKey"`
	FraudStatus                  *int     `json:"fraudStatus"`
	MerchantCommissionRate       *float64 `json:"merchantCommissionRate"`
	MerchantCommissionRateAmount *float64 `json:"merchantCommissionRateAmount"`
	IyziCommissionRateAmount     *float64 `json:"iyziCommissionRateAmount"`
	IyziCommissionFee            *float64 `json:"iyziCommissionFee"`
	PaymentTransactionID         string   `json:"paymentTransactionId"`
}

type apiThreeDSInitResponse struct {
	apiResponse
	ThreeDSHTMLContent string `json:"threeDSHtmlContent"`
	PaymentID          string `json:"paymentId"`
	Token              string `json:"token"`
}

type apiRefundResponse struct {
	apiResponse
	PaymentID            string   `json:"paymentId"`
	PaymentTransactionID string   `json:"paymentTransactionId"`
	Price                *float64 `json:"price"`
	Currency             string   `json:"currency"`
}

type apiCancelResponse struct {
	apiResponse
	PaymentID string   `json:"paymentId"`
	Price     *float64 `json:"price"`
	Currency  string   `json:"currency"`
}

type apiCheckoutFormInitResponse struct {
	apiResponse
	CheckoutFormContent string `json:"checkoutFormContent"`
	PaymentPageURL      string `json:"paymentPageUrl"`
	Token               string `json:"token"`
	TokenExpireTime     int64  `json:"tokenExpireTime"`
}

type apiCheckoutFormRetrieveResponse struct {
	apiResponse
	PaymentID                    string   `json:"paymentId"`
	PaymentStatus                string   `json:"paymentStatus"`
	FraudStatus                  *int     `json:"fraudStatus"`
	Price                        *float64 `json:"price"`
	PaidPrice                    *float64 `json:"paidPrice"`
	Currency                     string   `json:"currency"`
	BasketID                     string   `json:"basketId"`
	Installment                  *int     `json:"installment"`
	BinNumber                    string   `json:"binNumber"`
	LastFourDigits               string   `json:"lastFourDigits"`
	CardType                     string   `json:"cardType"`
	CardAssociation              string   `json:"cardAssociation"`
	CardFamily                   string   `json:"cardFamily"`
	CardToken                    string   `json:"cardToken"`
	CardUserKey                  string   `json:"cardUserKey"`
	MerchantCommissionRate       *float64 `json:"merchantCommissionRate"`
	MerchantCommissionRateAmount *float64 `json:"merchantCommissionRateAmount"`
	IyziCommissionRateAmount     *float64 `json:"iyziCommissionRateAmount"`
	IyziCommissionFee            *float64 `json:"iyziCommissionFee"`
	PaymentTransactionID         string   `json:"paymentTransactionId"`
}

type apiBinCheckResponse struct {
	apiResponse
	BinNumber       string `json:"binNumber"`
	CardType        string `json:"cardType"`
	CardAssociation string `json:"cardAssociation"`
	CardFamily      string `json:"cardFamily"`
	BankName        string `json:"bankName"`
	BankCode        int    `json:"bankCode"`
	Commercial      int    `json:"commercial"`
}

type apiPWIInitResponse struct {
	apiResponse
	HTMLContent     string `json:"htmlContent"`
	Token           string `json:"token"`
	TokenExpireTime int64  `json:"tokenExpireTime"`
	PaymentPageURL  string `json:"paymentPageUrl"`
}

type apiPWIRetrieveResponse struct {
	apiResponse
	Token                        string   `json:"token"`
	CallbackURL                  string   `json:"callbackUrl"`
	PaymentStatus                string   `json:"paymentStatus"`
	PaymentID                    string   `json:"paymentId"`
	Price                        *float64 `json:"price"`
	PaidPrice                    *float64 `json:"paidPrice"`
	Currency                     string   `json:"currency"`
	BasketID                     string   `json:"basketId"`
	MerchantCommissionRate       *float64 `json:"merchantCommissionRate"`
	MerchantCommissionRateAmount *float64 `json:"merchantCommissionRateAmount"`
	IyziCommissionRateAmount     *float64 `json:"iyziCommissionRateAmount"`
	IyziCommissionFee            *float64 `json:"iyziCommissionFee"`
	IBAN                         string   `json:"iban"`
	BankName                     string   `json:"bankName"`
	BuyerName                    string   `json:"buyerName"`
	BuyerSurname                 string   `json:"buyerSurname"`
	BuyerEmail                   string   `json:"buyerEmail"`
}

type apiInstallmentInfoResponse struct {
	apiResponse
	InstallmentDetails []core.InstallmentPrice `json:"installmentDetails"`
}

type apiSubscriptionInitializeRequest struct {
	Locale                    string                  `json:"locale"`
	ConversationID            string                  `json:"conversationId,omitempty"`
	PricingPlanReferenceCode  string                  `json:"pricingPlanReferenceCode"`
	SubscriptionInitialStatus core.SubscriptionStatus `json:"subscriptionInitialStatus"`
	Customer                  apiSubscriptionCustomer `json:"customer"`
	PaymentCard               apiSubscriptionCard     `json:"paymentCard"`
}

type apiSubscriptionCustomer struct {
	Name            string      `json:"name"`
	Surname         string      `json:"surname"`
	Email           string      `json:"email"`
	GSMNumber       string      `json:"gsmNumber"`
	IdentityNumber  string      `json:"identityNumber"`
	BillingAddress  apiAddress  `json:"billingAddress"`
	ShippingAddress *apiAddress `json:"shippingAddress,omitempty"`
}

type apiSubscriptionCard struct {
	CardHolderName string `json:"cardHolderName,omitempty"`
	CardNumber     string `json:"cardNumber,omitempty"`
	ExpireMonth    string `json:"expireMonth,omitempty"`
	ExpireYear     string `json:"expireYear,omitempty"`
	CVC            string `json:"cvc,omitempty"`
}

type apiSubscriptionUpgradeRequest struct {
	NewPricingPlanReferenceCode string `json:"newPricingPlanReferenceCode"`
	UseTrial                    *bool  `json:"useTrial,omitempty"`
	ResetRecurrenceCount        *bool  `json:"resetRecurrenceCount,omitempty"`
}

type apiSubscriptionCardUpdateRequest struct {
	Locale                    string `json:"locale"`
	ConversationID            string `json:"conversationId,omitempty"`
	SubscriptionReferenceCode string `json:"subscriptionReferenceCode"`
	CallbackURL               string `json:"callbackUrl"`
}

type apiSubscriptionProductRequest struct {
	Locale         string `json:"locale"`
	ConversationID string `json:"conversationId,omitempty"`
	Name           string `json:"name"`
	Description    string `json:"description,omitempty"`
}

type apiPricingPlanRequest struct {
	Locale               string               `json:"locale"`
	ConversationID       string               `json:"conversationId,omitempty"`
	ProductReferenceCode string               `json:"productReferenceCode"`
	Name                 string               `json:"name"`
	Price                string               `json:"price"`
	Currency             string               `json:"currency"`
	PaymentInterval      core.PaymentInterval `json:"paymentInterval"`
	PaymentIntervalCount int                  `json:"paymentIntervalCount"`
	TrialPeriodDays      *int                 `json:"trialPeriodDays,omitempty"`
	RecurrenceCount      *int                 `json:"recurrenceCount,omitempty"`
}
