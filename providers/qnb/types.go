// Package qnb implements the QNB Finansbank virtual POS on the PayFor platform.
package qnb

import "github.com/boracomet/go-pos/core"

// Config holds the QNB virtual POS credentials.
//
// MerchantID is the üye işyeri numarası, UserCode/UserPass the API user, and
// MerchantPass the 3D Secure password: it signs the hashes and is never sent.
type Config struct {
	core.ProviderConfig

	MerchantID   string
	UserCode     string
	UserPass     string
	MerchantPass string

	// SecureType is the merchant's 3D model, "3DModel" (default) or "3DPay".
	SecureType string
}

// Credentials are the configuration values the callback hash is built from.
type Credentials struct {
	MerchantID   string
	MerchantPass string
	UserCode     string
}

// CallbackData holds the fields QNB POSTs to OkUrl / FailUrl. ResponseHash
// covers OrderId, AuthCode, ProcReturnCode, 3DStatus and ResponseRnd.
//
// A present-but-empty field is not the same as an absent one: MerchantID and
// PurchAmount are only checked when the bank actually sent them.
type CallbackData map[string]string

// HashFields are the values covered by the 3D form hash.
type HashFields struct {
	OrderID      string
	Amount       string
	OkURL        string
	FailURL      string
	TxnType      string
	Installment  string
	Rnd          string
	MerchantPass string
}
