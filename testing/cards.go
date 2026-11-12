package testing

// Magic card numbers for MockProvider. Any other valid card number succeeds.
// Every number passes the Luhn check; use any future expiry date and any 3-digit
// CVC.
const (
	// CardSuccess succeeds (Visa).
	CardSuccess = "4242424242424242"
	// CardSuccessMastercard succeeds (Mastercard).
	CardSuccessMastercard = "5555555555554444"
	// CardDeclined fails with CARD_DECLINED.
	CardDeclined = "4000000000000002"
	// CardInsufficientFunds fails with INSUFFICIENT_FUNDS.
	CardInsufficientFunds = "4000000000009995"
	// CardExpired fails with EXPIRED_CARD.
	CardExpired = "4000000000000069"
	// CardInvalidCVC fails with INVALID_CVC.
	CardInvalidCVC = "4000000000000127"
	// CardFraudSuspected fails with FRAUD_SUSPECTED.
	CardFraudSuspected = "4100000000000019"
	// CardLimitExceeded fails with LIMIT_EXCEEDED.
	CardLimitExceeded = "4000000000009987"
	// CardProviderError fails with PROVIDER_ERROR.
	CardProviderError = "4000000000000119"
	// CardThreeDSRequired is declined without 3D Secure (CARD_DECLINED, raw code
	// THREEDS_REQUIRED) and succeeds with 3D Secure.
	CardThreeDSRequired = "4000002760003184"
	// CardThreeDSFailed fails 3D Secure authentication (THREEDS_FAILED).
	CardThreeDSFailed = "4000008400001629"
	// CardNetworkError processes the payment but loses the response: the result is
	// PENDING with NETWORK_ERROR and GetPayment then reports success.
	CardNetworkError = "4000000000000341"
	// CardRefundFails succeeds, but refunds and cancels fail with PROVIDER_ERROR.
	CardRefundFails = "4000000000005126"
)

// MagicCards returns the magic card numbers keyed by scenario name: SUCCESS,
// SUCCESS_MASTERCARD, CARD_DECLINED, INSUFFICIENT_FUNDS, EXPIRED_CARD,
// INVALID_CVC, FRAUD_SUSPECTED, LIMIT_EXCEEDED, PROVIDER_ERROR,
// THREEDS_REQUIRED, THREEDS_FAILED, NETWORK_ERROR, REFUND_FAILS. The map is a
// copy; changing it does not affect the provider.
func MagicCards() map[string]string {
	return map[string]string{
		"SUCCESS":            CardSuccess,
		"SUCCESS_MASTERCARD": CardSuccessMastercard,
		"CARD_DECLINED":      CardDeclined,
		"INSUFFICIENT_FUNDS": CardInsufficientFunds,
		"EXPIRED_CARD":       CardExpired,
		"INVALID_CVC":        CardInvalidCVC,
		"FRAUD_SUSPECTED":    CardFraudSuspected,
		"LIMIT_EXCEEDED":     CardLimitExceeded,
		"PROVIDER_ERROR":     CardProviderError,
		"THREEDS_REQUIRED":   CardThreeDSRequired,
		"THREEDS_FAILED":     CardThreeDSFailed,
		"NETWORK_ERROR":      CardNetworkError,
		"REFUND_FAILS":       CardRefundFails,
	}
}
