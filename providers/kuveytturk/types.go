// Package kuveytturk implements the Kuveyt Türk Sanal POS (KT Pay Gate) JSON API.
package kuveytturk

import (
	"encoding/json"

	"github.com/boracomet/go-pos/core"
)

// Config holds the KT Pay Gate credentials from the Kuveyt Türk corporate panel:
// MerchantID and CustomerID identify the virtual POS and the account, Username
// and Password the API role user.
type Config struct {
	core.ProviderConfig
	MerchantID string
	CustomerID string
	Username   string
	Password   string
	// PaymentType is sent with the Payment request. The API requires it (1 or 2)
	// although the integration document does not describe it. Nil means 1.
	PaymentType *int
}

// APIResponse is the JSON envelope of every KT Pay Gate endpoint except Payment.
type APIResponse struct {
	Result          json.RawMessage `json:"Result,omitempty"`
	Success         *bool           `json:"Success,omitempty"`
	ResponseCode    string          `json:"ResponseCode,omitempty"`
	ResponseMessage string          `json:"ResponseMessage,omitempty"`
	BusinessKey     string          `json:"BusinessKey,omitempty"`
	TransactionTime json.Number     `json:"TransactionTime,omitempty"`
}

// TransactionList is the Result of GetTransactions.
type TransactionList struct {
	Transactions []Transaction `json:"Transactions,omitempty"`
}

// Transaction is one transaction of a merchant order id. OrderId is the bank's
// own order id, needed for Provision, SaleReversal and GetTransaction. Amounts
// are in minor units, like the amount sent with the request.
type Transaction struct {
	MerchantId          int64       `json:"MerchantId,omitempty"`
	PosTerminalId       string      `json:"PosTerminalId,omitempty"`
	OrderId             json.Number `json:"OrderId,omitempty"`
	MerchantOrderId     string      `json:"MerchantOrderId,omitempty"`
	CardNumber          string      `json:"CardNumber,omitempty"`
	CardType            string      `json:"CardType,omitempty"`
	TransactionTime     string      `json:"TransactionTime,omitempty"`
	IsCancellable       int         `json:"IsCancellable,omitempty"`
	IsRefundable        int         `json:"IsRefundable,omitempty"`
	IsPartialRefundable int         `json:"IsPartialRefundable,omitempty"`
	OrderStatus         string      `json:"OrderStatus,omitempty"`
	LastOrderStatus     string      `json:"LastOrderStatus,omitempty"`
	TransactionStatus   string      `json:"TransactionStatus,omitempty"`
	FirstAmount         *float64    `json:"FirstAmount,omitempty"`
	DrawbackAmount      *float64    `json:"DrawbackAmount,omitempty"`
	CancelAmount        *float64    `json:"CancelAmount,omitempty"`
	ClosedAmount        *float64    `json:"ClosedAmount,omitempty"`
	FEC                 string      `json:"FEC,omitempty"`
	InstallmentCount    int         `json:"InstallmentCount,omitempty"`
	TransactionSecurity string      `json:"TransactionSecurity,omitempty"`
	ResponseCode        string      `json:"ResponseCode,omitempty"`
	ResponseExplain     string      `json:"ResponseExplain,omitempty"`
	ProvisionNumber     string      `json:"ProvisionNumber,omitempty"`
}

// ThreeDSCallbackData holds the fields Kuveyt Türk POSTs to successUrl / failUrl
// after card verification. They are not signed: the result is confirmed with the
// Provision call. CompleteThreeDSPayment also accepts the callback as a
// map[string]string or map[string]any, which may carry further fields.
type ThreeDSCallbackData struct {
	Success         string `json:"Success,omitempty"`
	ResponseCode    string `json:"ResponseCode,omitempty"`
	ResponseMessage string `json:"ResponseMessage,omitempty"`
	MD              string `json:"MD,omitempty"`
	OrderId         string `json:"OrderId,omitempty"`
	MerchantOrderId string `json:"MerchantOrderId,omitempty"`
	BusinessKey     string `json:"BusinessKey,omitempty"`
	TransactionTime string `json:"TransactionTime,omitempty"`
}

// HashCredentials are the merchant fields every KT Pay Gate signature covers.
type HashCredentials struct {
	MerchantID string
	Username   string
	Password   string
}

// Expiry is a card expiry as two-digit month and year ("01", "25").
type Expiry struct {
	Month string `json:"expireMonth"`
	Year  string `json:"expireYear"`
}

// Phone is a Turkish GSM number split into country code and subscriber number.
type Phone struct {
	CC         string `json:"cc"`
	Subscriber string `json:"subscriber"`
}
