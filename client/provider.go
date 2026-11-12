package client

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"

	"github.com/boracomet/go-pos/core"
)

// ProviderClient calls the routes of one provider on the payment API.
type ProviderClient struct {
	provider string
	config   *Config
}

// Provider returns the provider id the client calls.
func (c *ProviderClient) Provider() string { return c.provider }

// CreatePayment creates a direct (non-3D) payment: POST /:provider/payment.
func (c *ProviderClient) CreatePayment(ctx context.Context, request *core.PaymentRequest) (*core.PaymentResponse, error) {
	var result core.PaymentResponse
	if err := c.request(ctx, http.MethodPost, "payment", request, &result); err != nil {
		return nil, err
	}
	return &result, nil
}

// InitThreeDSPayment starts a 3D Secure payment: POST /:provider/payment/init-3ds.
// Render the returned ThreeDSHTMLContent to send the customer to the bank.
func (c *ProviderClient) InitThreeDSPayment(ctx context.Context, request *core.ThreeDSPaymentRequest) (*core.ThreeDSInitResponse, error) {
	var result core.ThreeDSInitResponse
	if err := c.request(ctx, http.MethodPost, "payment/init-3ds", request, &result); err != nil {
		return nil, err
	}
	return &result, nil
}

// CompleteThreeDSPayment completes a 3D Secure payment from the provider's
// callback data: POST /:provider/payment/complete-3ds.
func (c *ProviderClient) CompleteThreeDSPayment(ctx context.Context, callbackData map[string]any) (*core.PaymentResponse, error) {
	var result core.PaymentResponse
	if err := c.request(ctx, http.MethodPost, "payment/complete-3ds", callbackData, &result); err != nil {
		return nil, err
	}
	return &result, nil
}

// Authorize blocks the amount on the card: POST /:provider/authorize.
func (c *ProviderClient) Authorize(ctx context.Context, request *core.PaymentRequest) (*core.PaymentResponse, error) {
	var result core.PaymentResponse
	if err := c.request(ctx, http.MethodPost, "authorize", request, &result); err != nil {
		return nil, err
	}
	return &result, nil
}

// InitThreeDSAuthorize starts a pre-authorization with 3D Secure:
// POST /:provider/authorize/init-3ds.
func (c *ProviderClient) InitThreeDSAuthorize(ctx context.Context, request *core.ThreeDSPaymentRequest) (*core.ThreeDSInitResponse, error) {
	var result core.ThreeDSInitResponse
	if err := c.request(ctx, http.MethodPost, "authorize/init-3ds", request, &result); err != nil {
		return nil, err
	}
	return &result, nil
}

// Capture charges a pre-authorization: POST /:provider/capture. The server must
// enable the action and an authorize hook.
func (c *ProviderClient) Capture(ctx context.Context, request *core.CaptureRequest) (*core.PaymentResponse, error) {
	var result core.PaymentResponse
	if err := c.request(ctx, http.MethodPost, "capture", request, &result); err != nil {
		return nil, err
	}
	return &result, nil
}

// VoidAuthorization releases a pre-authorization: POST /:provider/void. The
// server must enable the action and an authorize hook.
func (c *ProviderClient) VoidAuthorization(ctx context.Context, request *core.VoidAuthorizationRequest) (*core.CancelResponse, error) {
	var result core.CancelResponse
	if err := c.request(ctx, http.MethodPost, "void", request, &result); err != nil {
		return nil, err
	}
	return &result, nil
}

// SaveCard stores a card: POST /:provider/cards/save. The server must enable the
// action and an authorize hook.
func (c *ProviderClient) SaveCard(ctx context.Context, request *core.SaveCardRequest) (*core.SaveCardResponse, error) {
	var result core.SaveCardResponse
	if err := c.request(ctx, http.MethodPost, "cards/save", request, &result); err != nil {
		return nil, err
	}
	return &result, nil
}

// ListCards lists a customer's saved cards: POST /:provider/cards/list. The
// server must enable the action and an authorize hook.
func (c *ProviderClient) ListCards(ctx context.Context, request *core.ListCardsRequest) (*core.ListCardsResponse, error) {
	var result core.ListCardsResponse
	if err := c.request(ctx, http.MethodPost, "cards/list", request, &result); err != nil {
		return nil, err
	}
	return &result, nil
}

// DeleteCard removes a saved card: POST /:provider/cards/delete. The server must
// enable the action and an authorize hook.
func (c *ProviderClient) DeleteCard(ctx context.Context, request *core.DeleteCardRequest) (*core.DeleteCardResponse, error) {
	var result core.DeleteCardResponse
	if err := c.request(ctx, http.MethodPost, "cards/delete", request, &result); err != nil {
		return nil, err
	}
	return &result, nil
}

// Refund refunds a payment: POST /:provider/refund. The server must enable the
// action and an authorize hook.
func (c *ProviderClient) Refund(ctx context.Context, request *core.RefundRequest) (*core.RefundResponse, error) {
	var result core.RefundResponse
	if err := c.request(ctx, http.MethodPost, "refund", request, &result); err != nil {
		return nil, err
	}
	return &result, nil
}

// Cancel cancels a payment: POST /:provider/cancel. The server must enable the
// action and an authorize hook.
func (c *ProviderClient) Cancel(ctx context.Context, request *core.CancelRequest) (*core.CancelResponse, error) {
	var result core.CancelResponse
	if err := c.request(ctx, http.MethodPost, "cancel", request, &result); err != nil {
		return nil, err
	}
	return &result, nil
}

// GetPayment reads a payment: GET /:provider/payment/:id. The server must enable
// the action and an authorize hook.
func (c *ProviderClient) GetPayment(ctx context.Context, paymentID string) (*core.PaymentResponse, error) {
	var result core.PaymentResponse
	if err := c.request(ctx, http.MethodGet, "payment/"+encodeURIComponent(paymentID), nil, &result); err != nil {
		return nil, err
	}
	return &result, nil
}

// BinCheck looks up a BIN: POST /:provider/bin-check.
func (c *ProviderClient) BinCheck(ctx context.Context, binNumber string) (*core.BinCheckResponse, error) {
	var result core.BinCheckResponse
	body := map[string]string{"binNumber": binNumber}
	if err := c.request(ctx, http.MethodPost, "bin-check", body, &result); err != nil {
		return nil, err
	}
	return &result, nil
}

// InstallmentInfo queries installment options: POST /:provider/installment.
func (c *ProviderClient) InstallmentInfo(ctx context.Context, request *core.InstallmentInfoRequest) (*core.InstallmentInfoResponse, error) {
	var result core.InstallmentInfoResponse
	if err := c.request(ctx, http.MethodPost, "installment", request, &result); err != nil {
		return nil, err
	}
	return &result, nil
}

// buildURL is the endpoint URL: the base URL, the provider id and the path.
func (c *ProviderClient) buildURL(path string) string {
	base := strings.TrimSuffix(c.config.BaseURL, "/")
	return base + "/" + c.provider + "/" + strings.TrimPrefix(path, "/")
}

// request sends one call. A 422 carrying a result is the provider rejecting the
// operation, so it is returned as a result instead of an error.
func (c *ProviderClient) request(ctx context.Context, method, path string, body any, out any) error {
	headers := make(map[string]string, len(c.config.Headers)+1)
	for name, value := range c.config.Headers {
		headers[name] = value
	}

	var payload []byte
	if body != nil && method != http.MethodGet {
		headers["Content-Type"] = "application/json"
		data, err := json.Marshal(body)
		if err != nil {
			return err
		}
		payload = data
	}

	var reader io.Reader
	if payload != nil {
		reader = bytes.NewReader(payload)
	}
	request, err := http.NewRequestWithContext(ctx, method, absoluteURL(c.buildURL(path)), reader)
	if err != nil {
		return err
	}
	for name, value := range headers {
		request.Header.Set(name, value)
	}

	response, err := c.config.Doer.Do(request)
	if err != nil {
		return err
	}
	defer response.Body.Close()

	raw, err := io.ReadAll(response.Body)
	if err != nil {
		return err
	}
	var record map[string]any
	if len(bytes.TrimSpace(raw)) > 0 {
		_ = json.Unmarshal(raw, &record)
	}
	_, hasStatus := record["status"]
	if isOK(response.StatusCode) || (response.StatusCode == http.StatusUnprocessableEntity && hasStatus) {
		if out == nil || len(bytes.TrimSpace(raw)) == 0 {
			return nil
		}
		return json.Unmarshal(raw, out)
	}

	message, _ := record["message"].(string)
	return &RequestError{
		Status:     response.StatusCode,
		StatusText: statusText(response),
		Message:    message,
	}
}

// RequestError reports an API response that is not a result: the server rejected
// the request. Message is the server's message when it sent one.
type RequestError struct {
	Status     int
	StatusText string
	Message    string
}

func (e *RequestError) Error() string {
	if e.Message != "" {
		return e.Message
	}
	return fmt.Sprintf("HTTP %d: %s", e.Status, e.StatusText)
}

// encodeURIComponent escapes a path component the way a browser does, leaving
// the unreserved marks !'()*-._~ alone.
func encodeURIComponent(value string) string {
	const unreserved = "ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789-_.!~*'()"
	var out strings.Builder
	for i := 0; i < len(value); i++ {
		char := value[i]
		if strings.IndexByte(unreserved, char) >= 0 {
			out.WriteByte(char)
			continue
		}
		fmt.Fprintf(&out, "%%%02X", char)
	}
	return out.String()
}
