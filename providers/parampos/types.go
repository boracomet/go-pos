package parampos

import (
	"errors"
	"strconv"
)

// Namespace is the XML namespace of every Param (TurkPOS) service operation.
const Namespace = "https://turkpos.com.tr/"

// SandboxURL is Param's test service.
const SandboxURL = "https://test-dmz.param.com.tr/turkpos.ws/service_turkpos_test.asmx"

// ProductionURL is Param's live service.
const ProductionURL = "https://posws.param.com.tr/turkpos.ws/service_turkpos_prod.asmx"

// Result is a parsed SOAP result. All values are strings as returned by the service.
type Result map[string]string

// CallbackData is the data Param POSTs to Basarili_URL / Hata_URL after 3D
// authentication. Only islemHash is signed; every other value is untrusted.
type CallbackData struct {
	MD                string `json:"md"`
	MDStatus          string `json:"mdStatus"`
	OrderID           string `json:"orderId"`
	IslemGUID         string `json:"islemGUID"`
	IslemHash         string `json:"islemHash"`
	TransactionAmount string `json:"transactionAmount,omitempty"`
	BankResult        string `json:"bankResult,omitempty"`
}

// InstallmentRates are the commission rates of one card program (virtual POS).
type InstallmentRates struct {
	// PosID is the Param SanalPOS_ID.
	PosID string `json:"posId"`
	// BankName is the card bank / program name, e.g. "Diğer Banka Kartları".
	BankName string `json:"bankName"`
	// Rates holds the commission rate in percent by installment count;
	// unavailable counts are absent.
	Rates map[int]float64 `json:"rates"`
}

// PaidPriceRequest asks for the amount to charge for a card and installment count.
type PaidPriceRequest struct {
	BinNumber string
	Price     string
	// Installment defaults to 1 when zero.
	Installment int
}

// callback is a normalized 3D callback. mdStatusPresent distinguishes an absent
// mdStatus from an empty one: only an absent value skips verification.
type callback struct {
	data            CallbackData
	mdStatusPresent bool
}

func normalizeCallback(value any) (callback, bool) {
	switch v := value.(type) {
	case nil:
		return callback{}, false
	case CallbackData:
		return callback{data: v, mdStatusPresent: true}, true
	case *CallbackData:
		if v == nil {
			return callback{}, false
		}
		return callback{data: *v, mdStatusPresent: true}, true
	case map[string]string:
		return callback{
			data: CallbackData{
				MD:                v["md"],
				MDStatus:          v["mdStatus"],
				OrderID:           v["orderId"],
				IslemGUID:         v["islemGUID"],
				IslemHash:         v["islemHash"],
				TransactionAmount: v["transactionAmount"],
				BankResult:        v["bankResult"],
			},
			mdStatusPresent: hasKey(v, "mdStatus"),
		}, true
	case map[string]any:
		return callback{
			data: CallbackData{
				MD:                stringValue(v["md"]),
				MDStatus:          stringValue(v["mdStatus"]),
				OrderID:           stringValue(v["orderId"]),
				IslemGUID:         stringValue(v["islemGUID"]),
				IslemHash:         stringValue(v["islemHash"]),
				TransactionAmount: stringValue(v["transactionAmount"]),
				BankResult:        stringValue(v["bankResult"]),
			},
			mdStatusPresent: v["mdStatus"] != nil,
		}, true
	default:
		return callback{}, false
	}
}

func hasKey(m map[string]string, key string) bool {
	_, ok := m[key]
	return ok
}

func stringValue(value any) string {
	switch v := value.(type) {
	case nil:
		return ""
	case string:
		return v
	case bool:
		return strconv.FormatBool(v)
	case float64:
		return strconv.FormatFloat(v, 'f', -1, 64)
	case int:
		return strconv.Itoa(v)
	default:
		return ""
	}
}

func newError(message string) error { return errors.New(message) }
