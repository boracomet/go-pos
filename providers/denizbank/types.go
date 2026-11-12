// Package denizbank implements the Denizbank virtual POS (InterVPos), 3D Model.
package denizbank

import "github.com/boracomet/go-pos/core"

// Config holds the Denizbank virtual POS credentials.
//
// ShopCode is the mağaza kodu, UserCode/UserPass the API user, and MerchantPass
// the 3D Secure store key: it signs the hashes and is never sent.
type Config struct {
	core.ProviderConfig

	ShopCode     string
	UserCode     string
	UserPass     string
	MerchantPass string

	// SecureType may only be "3DModel" (the default). 3D Pay is rejected: its
	// callback would be the only proof of payment, and the HASH joins the values
	// without separators, so it cannot bind the order.
	SecureType string
}

// CallbackData holds the fields Denizbank POSTs to OkUrl / FailUrl. HASH covers
// the fields named in HASHPARAMS.
//
// A present-but-empty field is not the same as an absent one: ShopCode and
// 3DStatus are only checked when the bank actually sent them.
type CallbackData map[string]string

// HashFields are the values covered by the 3D form hash.
type HashFields struct {
	ShopCode     string
	OrderID      string
	Amount       string
	OkURL        string
	FailURL      string
	TxnType      string
	Installment  string
	Rnd          string
	MerchantPass string
}
