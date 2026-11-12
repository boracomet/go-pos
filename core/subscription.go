package core

// SubscriptionStatus is the state of a recurring subscription.
type SubscriptionStatus string

const (
	SubscriptionActive   SubscriptionStatus = "ACTIVE"
	SubscriptionPending  SubscriptionStatus = "PENDING"
	SubscriptionCanceled SubscriptionStatus = "CANCELED"
	SubscriptionExpired  SubscriptionStatus = "EXPIRED"
	SubscriptionUnpaid   SubscriptionStatus = "UNPAID"
)

// PaymentInterval is the billing period of a pricing plan.
type PaymentInterval string

const (
	IntervalDaily   PaymentInterval = "DAILY"
	IntervalWeekly  PaymentInterval = "WEEKLY"
	IntervalMonthly PaymentInterval = "MONTHLY"
	IntervalYearly  PaymentInterval = "YEARLY"
)

// SubscriptionCustomer is the subscriber a subscription is created for.
type SubscriptionCustomer struct {
	Name            string   `json:"name"`
	Surname         string   `json:"surname"`
	Email           string   `json:"email"`
	GSMNumber       string   `json:"gsmNumber"`
	IdentityNumber  string   `json:"identityNumber"`
	BillingAddress  Address  `json:"billingAddress"`
	ShippingAddress *Address `json:"shippingAddress,omitempty"`
}

// SubscriptionInitializeRequest starts a subscription on a pricing plan.
type SubscriptionInitializeRequest struct {
	Locale                    string               `json:"locale,omitempty"`
	ConversationID            string               `json:"conversationId,omitempty"`
	PricingPlanReferenceCode  string               `json:"pricingPlanReferenceCode"`
	SubscriptionInitialStatus SubscriptionStatus   `json:"subscriptionInitialStatus"`
	Customer                  SubscriptionCustomer `json:"customer"`
	PaymentCard               PaymentCard          `json:"paymentCard"`
}

// SubscriptionInitializeData is the subscription created by InitializeSubscription.
type SubscriptionInitializeData struct {
	ReferenceCode            string             `json:"referenceCode"`
	ParentReferenceCode      string             `json:"parentReferenceCode,omitempty"`
	PricingPlanReferenceCode string             `json:"pricingPlanReferenceCode"`
	CustomerReferenceCode    string             `json:"customerReferenceCode"`
	SubscriptionStatus       SubscriptionStatus `json:"subscriptionStatus"`
	TrialDays                *int               `json:"trialDays,omitempty"`
	TrialStartDate           *int64             `json:"trialStartDate,omitempty"`
	TrialEndDate             *int64             `json:"trialEndDate,omitempty"`
	CreatedDate              int64              `json:"createdDate"`
	StartDate                int64              `json:"startDate"`
	EndDate                  *int64             `json:"endDate,omitempty"`
}

// SubscriptionInitializeResponse is the result of starting a subscription.
type SubscriptionInitializeResponse struct {
	Status          PaymentStatus               `json:"status"`
	SystemTime      *int64                      `json:"systemTime,omitempty"`
	Code            PaymentErrorCode            `json:"code,omitempty"`
	ErrorCode       string                      `json:"errorCode,omitempty"`
	ErrorMessage    string                      `json:"errorMessage,omitempty"`
	ProviderMessage string                      `json:"providerMessage,omitempty"`
	Data            *SubscriptionInitializeData `json:"data,omitempty"`
}

// SubscriptionCancelRequest cancels a subscription.
type SubscriptionCancelRequest struct {
	SubscriptionReferenceCode string `json:"subscriptionReferenceCode"`
}

// SubscriptionCancelData is the subscription after cancellation.
type SubscriptionCancelData struct {
	ReferenceCode      string             `json:"referenceCode"`
	SubscriptionStatus SubscriptionStatus `json:"subscriptionStatus"`
}

// SubscriptionCancelResponse is the result of cancelling a subscription.
type SubscriptionCancelResponse struct {
	Status          PaymentStatus           `json:"status"`
	SystemTime      *int64                  `json:"systemTime,omitempty"`
	Code            PaymentErrorCode        `json:"code,omitempty"`
	ErrorCode       string                  `json:"errorCode,omitempty"`
	ErrorMessage    string                  `json:"errorMessage,omitempty"`
	ProviderMessage string                  `json:"providerMessage,omitempty"`
	Data            *SubscriptionCancelData `json:"data,omitempty"`
}

// SubscriptionUpgradeRequest moves a subscription to another pricing plan.
type SubscriptionUpgradeRequest struct {
	SubscriptionReferenceCode   string `json:"subscriptionReferenceCode"`
	NewPricingPlanReferenceCode string `json:"newPricingPlanReferenceCode"`
	UseTrial                    *bool  `json:"useTrial,omitempty"`
	ResetRecurrenceCount        *bool  `json:"resetRecurrenceCount,omitempty"`
}

// SubscriptionUpgradeData is the subscription after an upgrade.
type SubscriptionUpgradeData struct {
	ReferenceCode            string             `json:"referenceCode"`
	PricingPlanReferenceCode string             `json:"pricingPlanReferenceCode"`
	SubscriptionStatus       SubscriptionStatus `json:"subscriptionStatus"`
}

// SubscriptionUpgradeResponse is the result of upgrading a subscription.
type SubscriptionUpgradeResponse struct {
	Status          PaymentStatus            `json:"status"`
	SystemTime      *int64                   `json:"systemTime,omitempty"`
	Code            PaymentErrorCode         `json:"code,omitempty"`
	ErrorCode       string                   `json:"errorCode,omitempty"`
	ErrorMessage    string                   `json:"errorMessage,omitempty"`
	ProviderMessage string                   `json:"providerMessage,omitempty"`
	Data            *SubscriptionUpgradeData `json:"data,omitempty"`
}

// SubscriptionRetrieveRequest asks for a subscription's details.
type SubscriptionRetrieveRequest struct {
	SubscriptionReferenceCode string `json:"subscriptionReferenceCode"`
}

// SubscriptionRetrieveData is a subscription's details.
type SubscriptionRetrieveData struct {
	ReferenceCode            string             `json:"referenceCode"`
	ParentReferenceCode      string             `json:"parentReferenceCode,omitempty"`
	PricingPlanReferenceCode string             `json:"pricingPlanReferenceCode"`
	PricingPlanName          string             `json:"pricingPlanName"`
	CustomerReferenceCode    string             `json:"customerReferenceCode"`
	SubscriptionStatus       SubscriptionStatus `json:"subscriptionStatus"`
	TrialDays                *int               `json:"trialDays,omitempty"`
	TrialStartDate           *int64             `json:"trialStartDate,omitempty"`
	TrialEndDate             *int64             `json:"trialEndDate,omitempty"`
	CreatedDate              int64              `json:"createdDate"`
	StartDate                int64              `json:"startDate"`
	EndDate                  *int64             `json:"endDate,omitempty"`
}

// SubscriptionRetrieveResponse is a subscription's details.
type SubscriptionRetrieveResponse struct {
	Status          PaymentStatus             `json:"status"`
	SystemTime      *int64                    `json:"systemTime,omitempty"`
	Code            PaymentErrorCode          `json:"code,omitempty"`
	ErrorCode       string                    `json:"errorCode,omitempty"`
	ErrorMessage    string                    `json:"errorMessage,omitempty"`
	ProviderMessage string                    `json:"providerMessage,omitempty"`
	Data            *SubscriptionRetrieveData `json:"data,omitempty"`
}

// SubscriptionCardUpdateRequest starts a hosted form that replaces the card of a
// subscription.
type SubscriptionCardUpdateRequest struct {
	Locale                    string `json:"locale,omitempty"`
	ConversationID            string `json:"conversationId,omitempty"`
	SubscriptionReferenceCode string `json:"subscriptionReferenceCode"`
	CallbackURL               string `json:"callbackUrl"`
}

// SubscriptionCardUpdateResponse carries the hosted card-update form.
type SubscriptionCardUpdateResponse struct {
	Status              PaymentStatus    `json:"status"`
	SystemTime          *int64           `json:"systemTime,omitempty"`
	Code                PaymentErrorCode `json:"code,omitempty"`
	ErrorCode           string           `json:"errorCode,omitempty"`
	ErrorMessage        string           `json:"errorMessage,omitempty"`
	ProviderMessage     string           `json:"providerMessage,omitempty"`
	Token               string           `json:"token,omitempty"`
	CheckoutFormContent string           `json:"checkoutFormContent,omitempty"`
	TokenExpireTime     *int64           `json:"tokenExpireTime,omitempty"`
	PaymentPageURL      string           `json:"paymentPageUrl,omitempty"`
}

// SubscriptionProductCreateRequest creates a subscription product.
type SubscriptionProductCreateRequest struct {
	Locale         string `json:"locale,omitempty"`
	ConversationID string `json:"conversationId,omitempty"`
	Name           string `json:"name"`
	Description    string `json:"description,omitempty"`
}

// SubscriptionProductData is a created subscription product.
type SubscriptionProductData struct {
	ReferenceCode string `json:"referenceCode"`
	Name          string `json:"name"`
	Description   string `json:"description,omitempty"`
	CreatedDate   int64  `json:"createdDate"`
}

// SubscriptionProductResponse is the result of creating a subscription product.
type SubscriptionProductResponse struct {
	Status          PaymentStatus            `json:"status"`
	SystemTime      *int64                   `json:"systemTime,omitempty"`
	Code            PaymentErrorCode         `json:"code,omitempty"`
	ErrorCode       string                   `json:"errorCode,omitempty"`
	ErrorMessage    string                   `json:"errorMessage,omitempty"`
	ProviderMessage string                   `json:"providerMessage,omitempty"`
	Data            *SubscriptionProductData `json:"data,omitempty"`
}

// PricingPlanCreateRequest creates a pricing plan for a subscription product.
type PricingPlanCreateRequest struct {
	Locale               string          `json:"locale,omitempty"`
	ConversationID       string          `json:"conversationId,omitempty"`
	ProductReferenceCode string          `json:"productReferenceCode"`
	Name                 string          `json:"name"`
	Price                string          `json:"price"`
	Currency             string          `json:"currency,omitempty"`
	PaymentInterval      PaymentInterval `json:"paymentInterval"`
	PaymentIntervalCount int             `json:"paymentIntervalCount"`
	TrialPeriodDays      *int            `json:"trialPeriodDays,omitempty"`
	RecurrenceCount      *int            `json:"recurrenceCount,omitempty"`
}

// PricingPlanData is a created pricing plan.
type PricingPlanData struct {
	ReferenceCode        string          `json:"referenceCode"`
	ProductReferenceCode string          `json:"productReferenceCode"`
	Name                 string          `json:"name"`
	Price                string          `json:"price"`
	Currency             string          `json:"currency"`
	PaymentInterval      PaymentInterval `json:"paymentInterval"`
	PaymentIntervalCount int             `json:"paymentIntervalCount"`
	TrialPeriodDays      *int            `json:"trialPeriodDays,omitempty"`
	RecurrenceCount      *int            `json:"recurrenceCount,omitempty"`
	CreatedDate          int64           `json:"createdDate"`
}

// PricingPlanResponse is the result of creating a pricing plan.
type PricingPlanResponse struct {
	Status          PaymentStatus    `json:"status"`
	SystemTime      *int64           `json:"systemTime,omitempty"`
	Code            PaymentErrorCode `json:"code,omitempty"`
	ErrorCode       string           `json:"errorCode,omitempty"`
	ErrorMessage    string           `json:"errorMessage,omitempty"`
	ProviderMessage string           `json:"providerMessage,omitempty"`
	Data            *PricingPlanData `json:"data,omitempty"`
}

// SetFailure records a failure on the response.
func (r *SubscriptionInitializeResponse) SetFailure(f Failure) {
	r.Status, r.Code, r.ErrorCode, r.ErrorMessage = f.Status, f.Code, f.ErrorCode, f.ErrorMessage
	r.ProviderMessage = f.ProviderMessage
}

// SetFailure records a failure on the response.
func (r *SubscriptionCancelResponse) SetFailure(f Failure) {
	r.Status, r.Code, r.ErrorCode, r.ErrorMessage = f.Status, f.Code, f.ErrorCode, f.ErrorMessage
	r.ProviderMessage = f.ProviderMessage
}

// SetFailure records a failure on the response.
func (r *SubscriptionUpgradeResponse) SetFailure(f Failure) {
	r.Status, r.Code, r.ErrorCode, r.ErrorMessage = f.Status, f.Code, f.ErrorCode, f.ErrorMessage
	r.ProviderMessage = f.ProviderMessage
}

// SetFailure records a failure on the response.
func (r *SubscriptionRetrieveResponse) SetFailure(f Failure) {
	r.Status, r.Code, r.ErrorCode, r.ErrorMessage = f.Status, f.Code, f.ErrorCode, f.ErrorMessage
	r.ProviderMessage = f.ProviderMessage
}

// SetFailure records a failure on the response.
func (r *SubscriptionCardUpdateResponse) SetFailure(f Failure) {
	r.Status, r.Code, r.ErrorCode, r.ErrorMessage = f.Status, f.Code, f.ErrorCode, f.ErrorMessage
	r.ProviderMessage = f.ProviderMessage
}

// SetFailure records a failure on the response.
func (r *SubscriptionProductResponse) SetFailure(f Failure) {
	r.Status, r.Code, r.ErrorCode, r.ErrorMessage = f.Status, f.Code, f.ErrorCode, f.ErrorMessage
	r.ProviderMessage = f.ProviderMessage
}

// SetFailure records a failure on the response.
func (r *PricingPlanResponse) SetFailure(f Failure) {
	r.Status, r.Code, r.ErrorCode, r.ErrorMessage = f.Status, f.Code, f.ErrorCode, f.ErrorMessage
	r.ProviderMessage = f.ProviderMessage
}
