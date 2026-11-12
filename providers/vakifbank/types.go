// Package vakifbank implements the VakıfBank virtual POS (VPOS 7/24).
package vakifbank

import "github.com/boracomet/go-pos/core"

// Config holds the VakıfBank virtual POS credentials.
//
// MerchantID is the 15-digit üye işyeri numarası, Password the API password and
// TerminalNo the terminal number (e.g. VP000000). 3D Secure uses VakıfBank's 3DS
// product, which the bank must enable for the merchant.
type Config struct {
	core.ProviderConfig

	MerchantID string
	Password   string
	TerminalNo string

	// TestMode selects the test MPI (3D Secure) service.
	TestMode bool
	// MPIURL overrides the MPI enrollment URL.
	MPIURL string
}

// CallbackData holds the fields VakıfBank's MPI POSTs to SuccessUrl / FailureUrl.
// They are not signed: VPOS checks the 3D Secure result of the MPI transaction
// itself, so callback values cannot change the card or the amount charged.
type CallbackData map[string]string
