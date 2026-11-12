package core

import "testing"

func TestResolveErrorCodeWithoutARawCode(t *testing.T) {
	for _, raw := range []string{"", "   ", "\t\n"} {
		if got := ResolveErrorCode(raw, nil); got != CodeUnknown {
			t.Errorf("ResolveErrorCode(%q) = %q, want %q", raw, got, CodeUnknown)
		}
	}
}

func TestResolveErrorCodeInternalCodes(t *testing.T) {
	cases := []struct {
		raw  string
		want PaymentErrorCode
	}{
		{"NETWORK_ERROR", CodeNetworkError},
		{"INVALID_HASH", CodeInvalidHash},
		{"INVALID_CALLBACK", CodeThreeDSFailed},
		{"VALIDATION_ERROR", CodeInvalidRequest},
	}
	for _, tc := range cases {
		t.Run(tc.raw, func(t *testing.T) {
			if got := ResolveErrorCode(tc.raw, nil); got != tc.want {
				t.Errorf("ResolveErrorCode(%q) = %q, want %q", tc.raw, got, tc.want)
			}
		})
	}
}

func TestResolveErrorCodeThreeDSStatusPrefix(t *testing.T) {
	for _, raw := range []string{"MD_STATUS_0", "MD_STATUS_1", "MD_STATUS_4", "MD_STATUS_", "MD_STATUS_9"} {
		if got := ResolveErrorCode(raw, nil); got != CodeThreeDSFailed {
			t.Errorf("ResolveErrorCode(%q) = %q, want %q", raw, got, CodeThreeDSFailed)
		}
	}
	// The prefix is matched at the start only.
	if got := ResolveErrorCode("X_MD_STATUS_1", nil); got != CodeUnknown {
		t.Errorf("ResolveErrorCode(%q) = %q, want %q", "X_MD_STATUS_1", got, CodeUnknown)
	}
}

func TestResolveErrorCodeISO8583HostCodes(t *testing.T) {
	cases := []struct {
		raw  string
		want PaymentErrorCode
	}{
		{"01", CodeCardDeclined},
		{"02", CodeCardDeclined},
		{"03", CodeProviderError},
		{"04", CodeFraudSuspected},
		{"05", CodeCardDeclined},
		{"07", CodeFraudSuspected},
		{"12", CodeCardDeclined},
		{"13", CodeInvalidRequest},
		{"14", CodeInvalidCard},
		{"15", CodeInvalidCard},
		{"33", CodeExpiredCard},
		{"41", CodeFraudSuspected},
		{"43", CodeFraudSuspected},
		{"51", CodeInsufficientFunds},
		{"54", CodeExpiredCard},
		{"57", CodeCardDeclined},
		{"58", CodeProviderError},
		{"59", CodeFraudSuspected},
		{"61", CodeLimitExceeded},
		{"62", CodeCardDeclined},
		{"65", CodeLimitExceeded},
		{"82", CodeInvalidCVC},
		{"91", CodeProviderError},
		{"94", CodeDuplicateOrder},
		{"96", CodeProviderError},
	}
	for _, tc := range cases {
		t.Run(tc.raw, func(t *testing.T) {
			if got := ResolveErrorCode(tc.raw, ISO8583ErrorCodes); got != tc.want {
				t.Errorf("ResolveErrorCode(%q) = %q, want %q", tc.raw, got, tc.want)
			}
		})
	}
}

func TestResolveErrorCodeProviderTable(t *testing.T) {
	// A provider table of the shape the built-in providers declare.
	codes := map[string]PaymentErrorCode{
		"10051": CodeInsufficientFunds,
		"2":     CodeThreeDSFailed,
		"6":     CodeCancelledByCustomer,
	}
	cases := []struct {
		raw  string
		want PaymentErrorCode
	}{
		{"10051", CodeInsufficientFunds},
		{"2", CodeThreeDSFailed},
		{"6", CodeCancelledByCustomer},
		{"99999", CodeUnknown},
		{"5001", CodeUnknown},
	}
	for _, tc := range cases {
		t.Run(tc.raw, func(t *testing.T) {
			if got := ResolveErrorCode(tc.raw, codes); got != tc.want {
				t.Errorf("ResolveErrorCode(%q) = %q, want %q", tc.raw, got, tc.want)
			}
		})
	}
}

func TestResolveErrorCodeInternalCodesWinOverTheProviderTable(t *testing.T) {
	codes := map[string]PaymentErrorCode{
		"NETWORK_ERROR":    CodeCardDeclined,
		"VALIDATION_ERROR": CodeFraudSuspected,
		"MD_STATUS_1":      CodeCardDeclined,
	}
	if got := ResolveErrorCode("NETWORK_ERROR", codes); got != CodeNetworkError {
		t.Errorf("NETWORK_ERROR = %q, want %q", got, CodeNetworkError)
	}
	if got := ResolveErrorCode("VALIDATION_ERROR", codes); got != CodeInvalidRequest {
		t.Errorf("VALIDATION_ERROR = %q, want %q", got, CodeInvalidRequest)
	}
	if got := ResolveErrorCode("MD_STATUS_1", codes); got != CodeThreeDSFailed {
		t.Errorf("MD_STATUS_1 = %q, want %q", got, CodeThreeDSFailed)
	}
}

func TestResolveErrorCodeTrimsWhitespace(t *testing.T) {
	if got := ResolveErrorCode("  51  ", ISO8583ErrorCodes); got != CodeInsufficientFunds {
		t.Errorf("ResolveErrorCode(%q) = %q, want %q", "  51  ", got, CodeInsufficientFunds)
	}
}

func TestResolveErrorCodeWithoutATableFallsBackToUnknown(t *testing.T) {
	if got := ResolveErrorCode("51", nil); got != CodeUnknown {
		t.Errorf("ResolveErrorCode(\"51\", nil) = %q, want %q", got, CodeUnknown)
	}
}

func TestErrorCodeTablesOnlyUseKnownCodes(t *testing.T) {
	known := map[PaymentErrorCode]bool{
		CodeInsufficientFunds:   true,
		CodeCardDeclined:        true,
		CodeInvalidCard:         true,
		CodeExpiredCard:         true,
		CodeInvalidCVC:          true,
		CodeThreeDSFailed:       true,
		CodeFraudSuspected:      true,
		CodeLimitExceeded:       true,
		CodeDuplicateOrder:      true,
		CodeCancelledByCustomer: true,
		CodeInvalidRequest:      true,
		CodeNetworkError:        true,
		CodeInvalidHash:         true,
		CodeProviderError:       true,
		CodeUnknown:             true,
	}
	tables := map[string]map[string]PaymentErrorCode{
		"ISO8583":  ISO8583ErrorCodes,
		"internal": internalCodes,
	}
	for name, table := range tables {
		for raw, code := range table {
			if !known[code] {
				t.Errorf("%s[%q] = %q, which is not a PaymentErrorCode", name, raw, code)
			}
		}
	}
}

func TestPaymentErrorCodeValues(t *testing.T) {
	cases := []struct {
		code PaymentErrorCode
		want string
	}{
		{CodeInsufficientFunds, "INSUFFICIENT_FUNDS"},
		{CodeCardDeclined, "CARD_DECLINED"},
		{CodeInvalidCard, "INVALID_CARD"},
		{CodeExpiredCard, "EXPIRED_CARD"},
		{CodeInvalidCVC, "INVALID_CVC"},
		{CodeThreeDSFailed, "THREEDS_FAILED"},
		{CodeFraudSuspected, "FRAUD_SUSPECTED"},
		{CodeLimitExceeded, "LIMIT_EXCEEDED"},
		{CodeDuplicateOrder, "DUPLICATE_ORDER"},
		{CodeCancelledByCustomer, "CANCELLED_BY_CUSTOMER"},
		{CodeInvalidRequest, "INVALID_REQUEST"},
		{CodeNetworkError, "NETWORK_ERROR"},
		{CodeInvalidHash, "INVALID_HASH"},
		{CodeProviderError, "PROVIDER_ERROR"},
		{CodeUnknown, "UNKNOWN"},
	}
	for _, tc := range cases {
		if string(tc.code) != tc.want {
			t.Errorf("code = %q, want %q", tc.code, tc.want)
		}
	}
}

func TestBaseProviderResolveErrorCode(t *testing.T) {
	base := NewBaseProvider("test-pos", ProviderConfig{})
	base.ErrorCodes = map[string]PaymentErrorCode{"51": CodeInsufficientFunds}

	if got := base.ResolveErrorCode("51"); got != CodeInsufficientFunds {
		t.Errorf("ResolveErrorCode(51) = %q, want %q", got, CodeInsufficientFunds)
	}
	if got := base.ResolveErrorCode("nope"); got != CodeUnknown {
		t.Errorf("ResolveErrorCode(nope) = %q, want %q", got, CodeUnknown)
	}
	if got := base.ResolveErrorCode("NETWORK_ERROR"); got != CodeNetworkError {
		t.Errorf("ResolveErrorCode(NETWORK_ERROR) = %q, want %q", got, CodeNetworkError)
	}
}

func TestBaseProviderErrorCodeFor(t *testing.T) {
	base := NewBaseProvider("test-pos", ProviderConfig{})
	base.ErrorCodes = map[string]PaymentErrorCode{"51": CodeInsufficientFunds}

	cases := []struct {
		name   string
		status PaymentStatus
		raw    string
		want   PaymentErrorCode
	}{
		{"failure maps the raw code", StatusFailure, "51", CodeInsufficientFunds},
		{"failure of an unknown code", StatusFailure, "??", CodeUnknown},
		{"success has no code", StatusSuccess, "51", ""},
		{"success without a raw code", StatusSuccess, "", ""},
		{"pending has no code", StatusPending, "51", ""},
		{"a network error is always mapped", StatusPending, NetworkErrorCode, CodeNetworkError},
		{"a cancelled result has no code", StatusCancelled, "51", ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := base.ErrorCodeFor(tc.status, tc.raw); got != tc.want {
				t.Errorf("ErrorCodeFor(%q, %q) = %q, want %q", tc.status, tc.raw, got, tc.want)
			}
		})
	}
}
