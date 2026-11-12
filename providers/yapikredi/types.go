// Package yapikredi implements the Yapı Kredi virtual POS (Posnet XML API).
package yapikredi

import "github.com/boracomet/go-pos/core"

// Config holds the Posnet credentials from Yapı Kredi: MerchantID and TerminalID
// are the member merchant (mid) and terminal (tid) numbers, PosnetID the Posnet
// ID used by 3D Secure, EncKey the Posnet ENC key the MAC is signed with.
type Config struct {
	core.ProviderConfig
	MerchantID string
	TerminalID string
	PosnetID   string
	EncKey     string
	// AcceptedMdStatuses lists the mdStatus values that are charged after 3D
	// Secure. Nil means ["1"] (full authentication). 2, 3 and 4 are half-secure;
	// accepting them can leave chargeback liability with the merchant.
	AcceptedMdStatuses []string
}

// CallbackFields are the fields Yapı Kredi POSTs to merchantReturnURL. The
// packets are encrypted by the bank and resolved with the XML API.
type CallbackFields struct {
	BankPacket     string `json:"BankPacket,omitempty"`
	MerchantPacket string `json:"MerchantPacket,omitempty"`
	Sign           string `json:"Sign,omitempty"`
}

// CompleteRequest is the input of CompleteThreeDSPayment: the callback POST plus
// the order from the merchant's own records. The MAC covers the order id, amount
// and currency, and the callback carries them only encrypted, so they must come
// from the merchant's side.
//
// CompleteThreeDSPayment also accepts the callback as a map[string]string or
// map[string]any carrying the same fields.
type CompleteRequest struct {
	CallbackFields
	// OrderID is the order id (ConversationID) used to start the payment.
	OrderID string `json:"orderId,omitempty"`
	// Amount is the order amount, as in the payment request ("1.20").
	Amount string `json:"amount,omitempty"`
	// Currency is the order currency (TRY, USD, ...). Empty means TRY.
	Currency string `json:"currency,omitempty"`
}
