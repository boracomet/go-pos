// Package albaraka implements the Albaraka Türk virtual POS (e-POS, Posnet JSON API).
package albaraka

import "github.com/boracomet/go-pos/core"

// Config holds the e-POS credentials from Albaraka Türk: MerchantID and
// TerminalID identify the üye işyeri and terminal, PosnetID is used by 3D Secure
// and EncKey signs every request.
type Config struct {
	core.ProviderConfig
	MerchantID string
	TerminalID string
	PosnetID   string
	EncKey     string
	// AcceptedMdStatuses are the MdStatus values charged after 3D Secure.
	// Nil means ["1"] (full authentication). 2, 3 and 4 are half-secure:
	// accepting them can leave chargeback liability with the merchant.
	AcceptedMdStatuses []string
}

// TransactionType selects a sale (the card is charged) or a pre-authorization.
type TransactionType string

const (
	// TransactionSale charges the card.
	TransactionSale TransactionType = "Sale"
	// TransactionAuth blocks the amount without charging it.
	TransactionAuth TransactionType = "Auth"
)

// CompleteRequest is the MerchantReturnURL POST plus the order from the merchant's
// own records. The bank charges the amount sent with the 3D data, so OrderID,
// Amount and Currency must come from the merchant, not from the POST.
//
// CompleteThreeDSPayment also accepts the callback as a map[string]string or
// map[string]any carrying these fields.
type CompleteRequest struct {
	MdStatus            string `json:"MdStatus,omitempty"`
	MdErrorMessage      string `json:"MdErrorMessage,omitempty"`
	SecureTransactionId string `json:"SecureTransactionId,omitempty"`
	CAVV                string `json:"CAVV,omitempty"`
	ECI                 string `json:"ECI,omitempty"`
	MD                  string `json:"MD,omitempty"`
	// OrderID is the order id (conversationId) used to start the payment.
	OrderID string `json:"orderId,omitempty"`
	// Amount is the order amount as in the payment request ("1.20").
	Amount string `json:"amount,omitempty"`
	// Currency is the order currency; empty means TRY.
	Currency string `json:"currency,omitempty"`
	// Installment is the count used to start the payment; empty means 1.
	Installment string `json:"installment,omitempty"`
	// TransactionType is Auth when the payment started with InitThreeDSAuthorize.
	TransactionType string `json:"transactionType,omitempty"`
}

// APIResponse is the answer of the JSON API.
type APIResponse struct {
	ServiceResponseData *ServiceResponseData `json:"ServiceResponseData,omitempty"`
	AuthCode            *string              `json:"AuthCode,omitempty"`
	ReferenceCode       *string              `json:"ReferenceCode,omitempty"`
	// TransactionData holds the order's transactions in a TransactionInquiry answer.
	TransactionData any `json:"TransactionData,omitempty"`
}

// ServiceResponseData carries the host response code and description.
type ServiceResponseData struct {
	ResponseCode        string `json:"ResponseCode,omitempty"`
	ResponseDescription string `json:"ResponseDescription,omitempty"`
}

// Installment is the InstallmentCount / InstallmentType pair of a request:
// a single payment is sent as 0 / "N".
type Installment struct {
	Count int    `json:"InstallmentCount"`
	Type  string `json:"InstallmentType"`
}

// CardInformationData is the card of a non-3D Sale or Auth.
type CardInformationData struct {
	CardNo         string `json:"CardNo"`
	ExpireDate     string `json:"ExpireDate"`
	Cvc2           string `json:"Cvc2"`
	CardHolderName string `json:"CardHolderName"`
}

// ThreeDSecureData is the 3D Secure result the bank returned to MerchantReturnURL.
type ThreeDSecureData struct {
	SecureTransactionId string `json:"SecureTransactionId"`
	CavvData            string `json:"CavvData"`
	Eci                 string `json:"Eci"`
	MdStatus            any    `json:"MdStatus"`
	MD                  string `json:"MD"`
}
