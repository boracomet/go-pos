// Package client calls the payment HTTP API from a browser-facing or
// server-side Go application. It speaks the same routes, payloads and error
// shapes as the handler in core, so a frontend can create payments and start
// 3D Secure without ever seeing merchant credentials.
package client

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"

	"github.com/boracomet/go-pos/core"
)

// PaymentErrorCode is the provider-independent failure code of a result.
type PaymentErrorCode = core.PaymentErrorCode

// PaymentStatus is the normalized outcome of a provider operation.
type PaymentStatus = core.PaymentStatus

// Config configures a Client.
type Config struct {
	// BaseURL is the URL the handler is mounted on, e.g. "/api/pay" or
	// "https://api.example.com/pay". A relative BaseURL is resolved against
	// http://localhost, since Go has no browser origin.
	BaseURL string
	// Doer overrides the transport. Defaults to http.DefaultClient.
	Doer core.Doer
	// Headers are sent with every request, e.g. an authorization header.
	Headers map[string]string
}

// HealthResponse is the answer of the health check endpoint.
type HealthResponse struct {
	Status    string `json:"status"`
	Service   string `json:"service"`
	Version   string `json:"version"`
	Timestamp string `json:"timestamp"`
}

// Client calls the payment API. Each field is a provider client for a built-in
// provider id; Use returns one for any other id configured on the server.
// A Client is safe for concurrent use.
type Client struct {
	Iyzico     *ProviderClient
	PayTR      *ProviderClient
	Akbank     *ProviderClient
	ParamPOS   *ProviderClient
	KuveytTurk *ProviderClient
	Sipay      *ProviderClient
	DenizBank  *ProviderClient
	Albaraka   *ProviderClient
	VakifBank  *ProviderClient
	YapiKredi  *ProviderClient
	QNB        *ProviderClient
	QNBPAY     *ProviderClient
	Garanti    *ProviderClient
	// Mock is the in-memory test provider from the testing package.
	Mock *ProviderClient

	config Config
}

// New builds a client. The configuration is copied, so later changes to it do
// not affect the client.
func New(config Config) *Client {
	c := &Client{config: Config{
		BaseURL: config.BaseURL,
		Doer:    config.Doer,
		Headers: make(map[string]string, len(config.Headers)),
	}}
	for name, value := range config.Headers {
		c.config.Headers[name] = value
	}
	if c.config.Doer == nil {
		c.config.Doer = http.DefaultClient
	}
	c.Iyzico = c.Use(string(core.ProviderIyzico))
	c.PayTR = c.Use(string(core.ProviderPayTR))
	c.Akbank = c.Use(string(core.ProviderAkbank))
	c.ParamPOS = c.Use(string(core.ProviderParamPOS))
	c.KuveytTurk = c.Use(string(core.ProviderKuveytTurk))
	c.Sipay = c.Use(string(core.ProviderSipay))
	c.DenizBank = c.Use(string(core.ProviderDenizBank))
	c.Albaraka = c.Use(string(core.ProviderAlbaraka))
	c.VakifBank = c.Use(string(core.ProviderVakifBank))
	c.YapiKredi = c.Use(string(core.ProviderYapiKredi))
	c.QNB = c.Use(string(core.ProviderQNB))
	c.QNBPAY = c.Use(string(core.ProviderQNBPAY))
	c.Garanti = c.Use(string(core.ProviderGaranti))
	c.Mock = c.Use(string(core.ProviderMock))
	return c
}

// Use returns a client for a provider by its id on the server (the key it was
// configured under), for custom providers or ids other than the built-in ones.
func (c *Client) Use(providerID string) *ProviderClient {
	return &ProviderClient{provider: providerID, config: &c.config}
}

// Health calls the health check endpoint.
func (c *Client) Health(ctx context.Context) (*HealthResponse, error) {
	url := absoluteURL(strings.TrimSuffix(c.config.BaseURL, "/") + "/health")
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	for name, value := range c.config.Headers {
		request.Header.Set(name, value)
	}
	response, err := c.config.Doer.Do(request)
	if err != nil {
		return nil, err
	}
	defer response.Body.Close()
	if !isOK(response.StatusCode) {
		return nil, fmt.Errorf("Health check failed: %s", statusText(response))
	}
	var health HealthResponse
	if err := json.NewDecoder(response.Body).Decode(&health); err != nil {
		return nil, err
	}
	return &health, nil
}

func isOK(status int) bool { return status >= 200 && status < 300 }

// statusText is the reason phrase of a response, as fetch exposes it.
func statusText(response *http.Response) string {
	if text := strings.TrimSpace(strings.TrimPrefix(response.Status, fmt.Sprint(response.StatusCode))); text != "" {
		return text
	}
	return http.StatusText(response.StatusCode)
}

// absoluteURL resolves a relative API URL against a local origin.
func absoluteURL(raw string) string {
	if strings.Contains(raw, "://") {
		return raw
	}
	if !strings.HasPrefix(raw, "/") {
		raw = "/" + raw
	}
	return "http://localhost" + raw
}
