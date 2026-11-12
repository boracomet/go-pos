package plugins

import (
	"context"
	"encoding/json"
	"errors"
	"regexp"
	"strings"
	"testing"

	"github.com/boracomet/go-pos/core"
)

const mcpTestToken = "Bearer agent_secret"

// mcpTestPANRun is any 16-19 digit run, card-style separators included: a
// possible PAN wherever it appears in a tool result.
var mcpTestPANRun = regexp.MustCompile(`\d(?:[ -]?\d){15,18}`)

func mcpTestHeaderOf(request *core.HandlerRequest, name string) string {
	if request == nil {
		return ""
	}
	for key, value := range request.Headers {
		if strings.EqualFold(key, name) {
			return value
		}
	}
	return ""
}

func mcpTestBearer(request *core.HandlerRequest) bool {
	return mcpTestHeaderOf(request, "authorization") == mcpTestToken
}

func mcpTestPlugin(t *testing.T, options McpOptions) core.Plugin {
	t.Helper()
	if options.Path == "" {
		options.Path = "/mcp"
	}
	plugin, err := MCP(options)
	if err != nil {
		t.Fatalf("mcp: %v", err)
	}
	return plugin
}

// mcpTestNew builds a facade with one mock provider and the mcp plugin. A nil
// authorize lets every request through the handler hook.
func mcpTestNew(t *testing.T, options McpOptions, authorize func(*core.HandlerRequest) bool) (*failoverTestFacade, *failoverTestProvider) {
	t.Helper()
	provider := failoverTestNewProvider("mock")
	hook := func(context.Context, core.HandlerContext) (bool, error) { return true, nil }
	if authorize != nil {
		hook = func(_ context.Context, hctx core.HandlerContext) (bool, error) {
			return authorize(hctx.Request), nil
		}
	}
	facade := failoverTestNewFacade(t, failoverTestSetup{
		Providers:       []*failoverTestProvider{provider},
		DefaultProvider: "mock",
		Plugins:         []core.Plugin{mcpTestPlugin(t, options)},
		BasePath:        "/api/pay",
		Authorize:       hook,
	})
	return facade, provider
}

func mcpTestPost(t *testing.T, facade *failoverTestFacade, body map[string]any, headers map[string]string) *core.HandlerResponse {
	t.Helper()
	encoded, err := json.Marshal(body)
	if err != nil {
		t.Fatalf("marshal rpc body: %v", err)
	}
	requestHeaders := map[string]string{"content-type": "application/json"}
	for key, value := range headers {
		requestHeaders[key] = value
	}
	return facade.serve(t, "POST", "/mcp", string(encoded), requestHeaders)
}

func mcpTestCallTool(t *testing.T, facade *failoverTestFacade, name string, args map[string]any, id any, headers map[string]string) *core.HandlerResponse {
	t.Helper()
	return mcpTestPost(t, facade, map[string]any{
		"jsonrpc": "2.0",
		"id":      id,
		"method":  "tools/call",
		"params":  map[string]any{"name": name, "arguments": args},
	}, headers)
}

func mcpTestEnvelope(response *core.HandlerResponse) map[string]any {
	return failoverTestJSONMap(response.Body)
}

func mcpTestResult(response *core.HandlerResponse) map[string]any {
	return failoverTestJSONMap(mcpTestEnvelope(response)["result"])
}

func mcpTestRPCError(response *core.HandlerResponse) map[string]any {
	value, ok := mcpTestEnvelope(response)["error"]
	if !ok || value == nil {
		return nil
	}
	return failoverTestJSONMap(value)
}

func mcpTestContentText(result map[string]any) string {
	parts, _ := result["content"].([]any)
	texts := make([]string, 0, len(parts))
	for _, part := range parts {
		texts = append(texts, failoverTestText(failoverTestJSONMap(part), "text"))
	}
	return strings.Join(texts, "\n")
}

// mcpTestStrings reads a list the plugin built either as []string or as []any.
func mcpTestStrings(value any) []string {
	switch list := value.(type) {
	case []string:
		return append([]string{}, list...)
	case []any:
		out := make([]string, 0, len(list))
		for _, item := range list {
			if text, ok := item.(string); ok {
				out = append(out, text)
			}
		}
		return out
	}
	return nil
}

func mcpTestToolNames(result map[string]any) []string {
	tools, _ := result["tools"].([]any)
	names := make([]string, 0, len(tools))
	for _, tool := range tools {
		names = append(names, failoverTestText(failoverTestJSONMap(tool), "name"))
	}
	return names
}

func mcpTestWantStatus(t *testing.T, response *core.HandlerResponse, want int) {
	t.Helper()
	if response.Status != want {
		t.Errorf("HTTP status = %d, want %d (body %v)", response.Status, want, response.Body)
	}
}

func mcpTestWantRPCCode(t *testing.T, response *core.HandlerResponse, want float64) {
	t.Helper()
	failure := mcpTestRPCError(response)
	if failure == nil {
		t.Fatalf("no JSON-RPC error in %v", response.Body)
	}
	if code := failoverTestNumber(failure, "code"); code != want {
		t.Errorf("JSON-RPC code = %v, want %v", code, want)
	}
}

func mcpTestWantNoRPCError(t *testing.T, response *core.HandlerResponse) {
	t.Helper()
	if failure := mcpTestRPCError(response); failure != nil {
		t.Errorf("unexpected JSON-RPC error %v", failure)
	}
}

func mcpTestWantIsError(t *testing.T, response *core.HandlerResponse) map[string]any {
	t.Helper()
	mcpTestWantStatus(t, response, 200)
	mcpTestWantNoRPCError(t, response)
	result := mcpTestResult(response)
	if result["isError"] != true {
		t.Fatalf("isError = %v, want true (result %v)", result["isError"], result)
	}
	return result
}

// mcpTestPaid creates a succeeded 10.00 payment and returns its id.
func mcpTestPaid(t *testing.T, facade *failoverTestFacade) string {
	t.Helper()
	request := failoverTestRequest()
	request.Price = "10.00"
	request.PaidPrice = "10.00"
	paymentID := failoverTestText(facade.createPayment(t, request), "paymentId")
	if paymentID == "" {
		t.Fatal("createPayment returned no paymentId")
	}
	return paymentID
}

func mcpTestRefundRequests(t *testing.T, provider *failoverTestProvider) []*core.RefundRequest {
	t.Helper()
	calls := provider.callsOf(core.OperationRefund)
	requests := make([]*core.RefundRequest, 0, len(calls))
	for _, call := range calls {
		request, ok := call.Request.(*core.RefundRequest)
		if !ok {
			t.Fatalf("refund request type = %T", call.Request)
		}
		requests = append(requests, request)
	}
	return requests
}

func TestMCPAuthorization(t *testing.T) {
	t.Run("refuses to build a handler for the privileged endpoints without an authorize hook", func(t *testing.T) {
		provider := failoverTestNewProvider("mock")
		facade := failoverTestNewFacade(t, failoverTestSetup{
			Providers: []*failoverTestProvider{provider},
			Plugins:   []core.Plugin{mcpTestPlugin(t, McpOptions{})},
			BasePath:  "/api/pay",
		})

		_, err := core.NewHandler(facade, core.HandlerOptions{BasePath: "/api/pay"})
		if err == nil {
			t.Fatal("handler built without an authorize hook")
		}
		if !strings.Contains(strings.ToLower(err.Error()), "authorize") {
			t.Errorf("error = %q, want it to name the authorize hook", err.Error())
		}
	})

	t.Run("keeps a request without a valid token away from every tool", func(t *testing.T) {
		calls := 0
		facade, _ := mcpTestNew(t, McpOptions{
			Authorize: func(request *core.HandlerRequest, _ McpToolName) (bool, error) {
				return mcpTestBearer(request), nil
			},
			OnToolCall: func(McpToolCallInfo) { calls++ },
		}, mcpTestBearer)

		bodies := []map[string]any{
			{"jsonrpc": "2.0", "id": 1, "method": "initialize"},
			{"jsonrpc": "2.0", "id": 2, "method": "tools/list"},
			{"jsonrpc": "2.0", "id": 3, "method": "tools/call", "params": map[string]any{
				"name": "refund", "arguments": map[string]any{"paymentId": "pay_1", "price": "1.00"},
			}},
			{"jsonrpc": "2.0", "id": 4, "method": "tools/call", "params": map[string]any{
				"name": "getPayment", "arguments": map[string]any{"paymentId": "pay_1"},
			}},
		}
		for _, body := range bodies {
			mcpTestWantStatus(t, mcpTestPost(t, facade, body, nil), 403)
		}
		mcpTestWantStatus(t, facade.serve(t, "GET", "/mcp/info", nil, nil), 403)
		if calls != 0 {
			t.Errorf("onToolCall ran %d times, want 0", calls)
		}
	})

	t.Run("answers 401 with a JSON-RPC error when the tool authorizer denies the call", func(t *testing.T) {
		var audit []McpToolCallInfo
		facade, provider := mcpTestNew(t, McpOptions{
			Authorize: func(_ *core.HandlerRequest, tool McpToolName) (bool, error) {
				return tool != McpToolRefund && tool != McpToolCancel, nil
			},
			OnToolCall: func(info McpToolCallInfo) { audit = append(audit, info) },
		}, mcpTestBearer)

		response := mcpTestCallTool(t, facade, "refund", map[string]any{
			"paymentId": "pay_1", "price": "1.00",
		}, 7, map[string]string{"authorization": mcpTestToken})

		mcpTestWantStatus(t, response, 401)
		mcpTestWantRPCCode(t, response, -32001)
		if id := failoverTestNumber(mcpTestEnvelope(response), "id"); id != 7 {
			t.Errorf("envelope id = %v, want 7", id)
		}
		if len(audit) != 1 {
			t.Fatalf("onToolCall ran %d times, want 1", len(audit))
		}
		if audit[0].Tool != "refund" || !audit[0].IsError {
			t.Errorf("audit = %+v, want a failed refund call", audit[0])
		}
		if !strings.Contains(audit[0].Error, "denied") {
			t.Errorf("audit error = %q, want it to mention the denial", audit[0].Error)
		}
		if provider.count(core.OperationRefund) != 0 {
			t.Error("a denied tool reached the provider")
		}
	})

	t.Run("requires an authorize option for write tools", func(t *testing.T) {
		facade, _ := mcpTestNew(t, McpOptions{}, nil)

		response := mcpTestCallTool(t, facade, "cancel", map[string]any{"paymentId": "pay_1"}, 8, map[string]string{
			"authorization":    mcpTestToken,
			"cf-connecting-ip": "203.0.113.9",
		})

		mcpTestWantStatus(t, response, 401)
		failure := mcpTestRPCError(response)
		if failure == nil {
			t.Fatalf("no JSON-RPC error in %v", response.Body)
		}
		if message := failoverTestText(failure, "message"); !strings.Contains(message, "write operation") {
			t.Errorf("message = %q, want it to name the write operation", message)
		}
	})

	t.Run("hides write tools in readOnly mode and refuses to run them", func(t *testing.T) {
		facade, _ := mcpTestNew(t, McpOptions{ReadOnly: true}, nil)

		list := mcpTestPost(t, facade, map[string]any{"jsonrpc": "2.0", "id": 6, "method": "tools/list"}, nil)
		names := mcpTestToolNames(mcpTestResult(list))
		if !mcpTestHasTool(names, "getPayment") {
			t.Errorf("tools = %v, want getPayment exposed", names)
		}
		if mcpTestHasTool(names, "refund") {
			t.Errorf("tools = %v, want refund hidden", names)
		}

		call := mcpTestCallTool(t, facade, "refund", map[string]any{"paymentId": "pay_1", "price": "1.00"}, 7, nil)
		mcpTestWantStatus(t, call, 200)
		mcpTestWantRPCCode(t, call, -32601)
	})

	t.Run("exposes only the configured tools", func(t *testing.T) {
		facade, _ := mcpTestNew(t, McpOptions{Tools: []McpToolName{McpToolBinCheck}}, nil)

		list := mcpTestPost(t, facade, map[string]any{"jsonrpc": "2.0", "id": 1, "method": "tools/list"}, nil)
		names := mcpTestToolNames(mcpTestResult(list))
		if len(names) != 1 || names[0] != "binCheck" {
			t.Errorf("tools = %v, want [binCheck]", names)
		}
	})
}

func mcpTestHasTool(names []string, want string) bool {
	for _, name := range names {
		if name == want {
			return true
		}
	}
	return false
}

func TestMCPProtocol(t *testing.T) {
	t.Run("echoes a protocol version the client asks for", func(t *testing.T) {
		facade, _ := mcpTestNew(t, McpOptions{}, nil)

		for index, version := range McpProtocolVersions {
			response := mcpTestPost(t, facade, map[string]any{
				"jsonrpc": "2.0",
				"id":      index,
				"method":  "initialize",
				"params": map[string]any{
					"protocolVersion": version,
					"capabilities":    map[string]any{},
					"clientInfo":      map[string]any{"name": "cursor"},
				},
			}, nil)
			if got := failoverTestText(mcpTestResult(response), "protocolVersion"); got != version {
				t.Errorf("protocolVersion = %q, want %q", got, version)
			}
		}
	})

	t.Run("falls back to the server version for a version it does not speak", func(t *testing.T) {
		facade, _ := mcpTestNew(t, McpOptions{}, nil)

		response := mcpTestPost(t, facade, map[string]any{
			"jsonrpc": "2.0",
			"id":      1,
			"method":  "initialize",
			"params":  map[string]any{"protocolVersion": "1999-01-01"},
		}, nil)

		result := mcpTestResult(response)
		if got := failoverTestText(result, "protocolVersion"); got != "2025-06-18" {
			t.Errorf("protocolVersion = %q, want 2025-06-18", got)
		}
		serverInfo := failoverTestJSONMap(result["serverInfo"])
		if got := failoverTestText(serverInfo, "name"); got != mcpDefaultServerName {
			t.Errorf("serverInfo.name = %q, want %q", got, mcpDefaultServerName)
		}
		capabilities := failoverTestJSONMap(result["capabilities"])
		if _, ok := capabilities["tools"].(map[string]any); !ok {
			t.Errorf("capabilities = %v, want a tools object", capabilities)
		}
	})

	t.Run("answers a notification with 202 and no body", func(t *testing.T) {
		facade, _ := mcpTestNew(t, McpOptions{}, nil)

		for _, method := range []string{"notifications/initialized", "notifications/cancelled", "nope"} {
			response := mcpTestPost(t, facade, map[string]any{"jsonrpc": "2.0", "method": method}, nil)
			mcpTestWantStatus(t, response, 202)
			if response.Body != nil {
				t.Errorf("%s body = %v, want none", method, response.Body)
			}
		}
	})

	t.Run("answers JSON-RPC errors with HTTP 200", func(t *testing.T) {
		facade, _ := mcpTestNew(t, McpOptions{}, nil)

		unknownMethod := mcpTestPost(t, facade, map[string]any{
			"jsonrpc": "2.0", "id": 3, "method": "resources/list",
		}, nil)
		mcpTestWantStatus(t, unknownMethod, 200)
		mcpTestWantRPCCode(t, unknownMethod, -32601)
		if message := failoverTestText(mcpTestRPCError(unknownMethod), "message"); !strings.Contains(message, "not found") {
			t.Errorf("message = %q, want it to say the method was not found", message)
		}

		invalidRequest := mcpTestPost(t, facade, map[string]any{"id": 4, "method": "initialize"}, nil)
		mcpTestWantStatus(t, invalidRequest, 200)
		mcpTestWantRPCCode(t, invalidRequest, -32600)
		if id := failoverTestNumber(mcpTestEnvelope(invalidRequest), "id"); id != 4 {
			t.Errorf("envelope id = %v, want 4", id)
		}
	})

	t.Run("reports a tool failure as a result with isError", func(t *testing.T) {
		facade, _ := mcpTestNew(t, McpOptions{
			Authorize: func(*core.HandlerRequest, McpToolName) (bool, error) { return true, nil },
		}, nil)
		declined := facade.createPayment(t, failoverTestCardRequest(failoverTestCardDeclined))
		declinedID := failoverTestText(declined, "paymentId")

		refused := mcpTestCallTool(t, facade, "refund", map[string]any{
			"paymentId": declinedID, "price": "1.00",
		}, 5, map[string]string{"cf-connecting-ip": "203.0.113.9"})
		result := mcpTestWantIsError(t, refused)
		if text := mcpTestContentText(result); !strings.Contains(text, "Only a succeeded payment can be refunded") {
			t.Errorf("content = %q, want the provider's refusal", text)
		}

		throwing, scripted := mcpTestNew(t, McpOptions{}, nil)
		scripted.respond = func(operation core.PaymentOperation, _ any) (any, error) {
			if operation == core.OperationGetPayment {
				return nil, errors.New("provider unreachable")
			}
			return nil, nil
		}
		failed := mcpTestCallTool(t, throwing, "getPayment", map[string]any{"paymentId": "PAY1"}, 6, nil)
		if text := mcpTestContentText(mcpTestWantIsError(t, failed)); text != "provider unreachable" {
			t.Errorf("content = %q, want %q", text, "provider unreachable")
		}

		incomplete := mcpTestCallTool(t, facade, "installmentInfo", map[string]any{"binNumber": "540667"}, 7, nil)
		if text := mcpTestContentText(mcpTestWantIsError(t, incomplete)); !strings.Contains(text, "price") {
			t.Errorf("content = %q, want it to name the missing price argument", text)
		}
	})
}

func TestMCPClientIdentity(t *testing.T) {
	t.Run("passes the request IP to write operations instead of a fixed address", func(t *testing.T) {
		facade, provider := mcpTestNew(t, McpOptions{
			Authorize: func(*core.HandlerRequest, McpToolName) (bool, error) { return true, nil },
		}, nil)
		paymentID := mcpTestPaid(t, facade)
		args := map[string]any{"paymentId": paymentID, "price": "5.00"}

		mcpTestCallTool(t, facade, "refund", args, 1, map[string]string{"cf-connecting-ip": "203.0.113.9"})
		mcpTestCallTool(t, facade, "refund", args, 2, map[string]string{"x-real-ip": "198.51.100.7"})

		requests := mcpTestRefundRequests(t, provider)
		if len(requests) != 2 {
			t.Fatalf("provider refunds = %d, want 2", len(requests))
		}
		if requests[0].IP != "203.0.113.9" {
			t.Errorf("first refund ip = %q, want 203.0.113.9", requests[0].IP)
		}
		if requests[1].IP != "198.51.100.7" {
			t.Errorf("second refund ip = %q, want 198.51.100.7", requests[1].IP)
		}

		// x-forwarded-for is client-appendable: it is never trusted by default.
		spoofed := mcpTestCallTool(t, facade, "refund", args, 3, map[string]string{
			"x-forwarded-for": "203.0.113.9, 10.0.0.1",
		})
		if text := mcpTestContentText(mcpTestWantIsError(t, spoofed)); !strings.Contains(text, "getClientIp") {
			t.Errorf("content = %q, want it to name the getClientIp option", text)
		}
		if got := provider.count(core.OperationRefund); got != 2 {
			t.Errorf("provider refunds = %d, want 2", got)
		}

		refused := mcpTestCallTool(t, facade, "refund", args, 4, nil)
		if text := mcpTestContentText(mcpTestWantIsError(t, refused)); !strings.Contains(text, "getClientIp") {
			t.Errorf("content = %q, want it to name the getClientIp option", text)
		}
		if got := provider.count(core.OperationRefund); got != 2 {
			t.Errorf("provider refunds = %d, want 2", got)
		}
		if strings.Contains(failoverTestJSON(t, requests), "127.0.0.1") {
			t.Error("a refund was attributed to a fixed loopback address")
		}
	})

	t.Run("lets the host app resolve the IP itself", func(t *testing.T) {
		facade, provider := mcpTestNew(t, McpOptions{
			Authorize: func(*core.HandlerRequest, McpToolName) (bool, error) { return true, nil },
			GetClientIP: func(request *core.HandlerRequest) string {
				return mcpTestHeaderOf(request, "x-agent-ip")
			},
		}, nil)
		paymentID := mcpTestPaid(t, facade)

		response := mcpTestCallTool(t, facade, "cancel", map[string]any{"paymentId": paymentID}, 1, map[string]string{
			"cf-connecting-ip": "203.0.113.9",
			"x-agent-ip":       "192.0.2.10",
		})

		mcpTestWantStatus(t, response, 200)
		if _, failed := mcpTestResult(response)["isError"]; failed {
			t.Errorf("isError = %v, want absent", mcpTestResult(response)["isError"])
		}
		if got := failoverTestText(facade.getPayment(t, paymentID), "status"); got != string(core.StatusCancelled) {
			t.Errorf("payment status = %q, want cancelled", got)
		}
		request, ok := provider.lastRequest(core.OperationCancel).(*core.CancelRequest)
		if !ok {
			t.Fatalf("cancel request type = %T", provider.lastRequest(core.OperationCancel))
		}
		if request.IP != "192.0.2.10" {
			t.Errorf("cancel ip = %q, want 192.0.2.10", request.IP)
		}
	})

	t.Run("records the resolved IP in the audit hook", func(t *testing.T) {
		var audit []McpToolCallInfo
		facade, _ := mcpTestNew(t, McpOptions{
			OnToolCall: func(info McpToolCallInfo) { audit = append(audit, info) },
		}, nil)

		mcpTestCallTool(t, facade, "binCheck", map[string]any{"binNumber": "540667"}, 1, map[string]string{
			"x-real-ip": "203.0.113.42",
		})

		if len(audit) != 1 {
			t.Fatalf("onToolCall ran %d times, want 1", len(audit))
		}
		if audit[0].Tool != "binCheck" || audit[0].IP != "203.0.113.42" {
			t.Errorf("audit = %+v, want binCheck from 203.0.113.42", audit[0])
		}
	})
}

func TestMCPCardDataSafety(t *testing.T) {
	const pan = "4111111111111111"

	t.Run("never returns a full PAN in a tool result", func(t *testing.T) {
		facade, leaky := mcpTestNew(t, McpOptions{}, nil)
		leaky.respond = func(operation core.PaymentOperation, _ any) (any, error) {
			if operation != core.OperationGetPayment {
				return nil, nil
			}
			return &core.PaymentResponse{
				Status:       core.StatusSuccess,
				PaymentID:    "MOCK1",
				ErrorMessage: "card " + pan + " declined by the issuer",
				RawResponse: map[string]any{
					"cardNumber":       pan,
					"maskedCardNumber": "4111 1111 1111 1111",
					"lastFourDigits":   "1111",
					"cvv":              "123",
					"cards":            []any{map[string]any{"number": "4111-1111-1111-1111"}},
				},
			}, nil
		}

		response := mcpTestCallTool(t, facade, "getPayment", map[string]any{"paymentId": "MOCK1"}, 1, nil)
		text := mcpTestContentText(mcpTestResult(response))

		if mcpTestPANRun.MatchString(text) {
			t.Errorf("tool result carries a PAN-shaped run: %s", text)
		}
		if strings.Contains(text, pan) {
			t.Error("tool result carries the full PAN")
		}
		if strings.Contains(text, "4111") {
			t.Error("tool result carries the first four digits of the PAN")
		}
		if !strings.Contains(text, "*******1111") {
			t.Errorf("tool result = %s, want the masked form keeping the last four digits", text)
		}
		if strings.Contains(text, `"cardNumber"`) {
			t.Error("tool result carries the cardNumber key")
		}
		if strings.Contains(text, `"cvv"`) {
			t.Error("tool result carries the cvv key")
		}
	})

	t.Run("sanitizes the arguments handed to the audit hook", func(t *testing.T) {
		var audit []McpToolCallInfo
		facade, _ := mcpTestNew(t, McpOptions{
			OnToolCall: func(info McpToolCallInfo) { audit = append(audit, info) },
		}, nil)

		mcpTestCallTool(t, facade, "binCheck", map[string]any{"binNumber": "540667", "cardNumber": pan}, 1, nil)

		if len(audit) != 1 {
			t.Fatalf("onToolCall ran %d times, want 1", len(audit))
		}
		encoded := failoverTestJSON(t, audit[0])
		if mcpTestPANRun.MatchString(encoded) {
			t.Errorf("audit record carries a PAN-shaped run: %s", encoded)
		}
		if strings.Contains(encoded, "cardNumber") {
			t.Error("audit record carries the cardNumber key")
		}
	})
}

func TestMCPAuditTrail(t *testing.T) {
	t.Run("names every tool call in the library logger, without bodies", func(t *testing.T) {
		logger := &failoverTestLogger{}
		provider := failoverTestNewProvider("mock")
		facade := failoverTestNewFacade(t, failoverTestSetup{
			Providers:       []*failoverTestProvider{provider},
			DefaultProvider: "mock",
			Plugins:         []core.Plugin{mcpTestPlugin(t, McpOptions{})},
			Logger:          logger,
			BasePath:        "/api/pay",
			Authorize:       func(context.Context, core.HandlerContext) (bool, error) { return true, nil },
		})

		mcpTestCallTool(t, facade, "binCheck", map[string]any{"binNumber": "540667"}, 1, nil)

		var found *failoverTestLogEntry
		for _, entry := range logger.all() {
			if entry.Message == "mcp: tool 'binCheck' called" {
				captured := entry
				found = &captured
			}
		}
		if found == nil {
			t.Fatalf("logger has no tool call line: %v", logger.all())
		}
		if found.Level != "info" {
			t.Errorf("level = %q, want info", found.Level)
		}
		if failoverTestText(found.Meta, "tool") != "binCheck" || failoverTestText(found.Meta, "provider") != "mock" {
			t.Errorf("meta = %v, want tool binCheck on provider mock", found.Meta)
		}
		if strings.Contains(failoverTestJSON(t, logger.all()), "540667") {
			t.Error("the audit log carries the request body")
		}
	})

	t.Run("pushes write tools through the normal payment event pipeline", func(t *testing.T) {
		facade, provider := mcpTestNew(t, McpOptions{
			Authorize: func(*core.HandlerRequest, McpToolName) (bool, error) { return true, nil },
		}, nil)
		var seen []string
		facade.on(core.EventRefundSucceeded, func(_ context.Context, event *core.PaymentEvent) error {
			seen = append(seen, event.PaymentID)
			return nil
		})
		paymentID := mcpTestPaid(t, facade)

		mcpTestCallTool(t, facade, "refund", map[string]any{"paymentId": paymentID, "price": "4.00"}, 1, map[string]string{
			"cf-connecting-ip": "203.0.113.9",
		})

		if len(seen) != 1 || seen[0] != paymentID {
			t.Errorf("refund.succeeded events = %v, want [%s]", seen, paymentID)
		}
		requests := mcpTestRefundRequests(t, provider)
		if len(requests) != 1 {
			t.Fatalf("provider refunds = %d, want 1", len(requests))
		}
		minor, err := core.ToMinorUnits(requests[0].Price, "price")
		if err != nil {
			t.Fatalf("refund price: %v", err)
		}
		if minor != 400 {
			t.Errorf("refunded amount = %d minor units, want 400", minor)
		}
	})

	t.Run("keeps the onToolCall hook for both outcomes", func(t *testing.T) {
		var audit []McpToolCallInfo
		facade, _ := mcpTestNew(t, McpOptions{
			OnToolCall: func(info McpToolCallInfo) { audit = append(audit, info) },
			Authorize:  func(*core.HandlerRequest, McpToolName) (bool, error) { return true, nil },
		}, nil)
		paymentID := mcpTestPaid(t, facade)

		mcpTestCallTool(t, facade, "getPayment", map[string]any{"paymentId": paymentID}, 1, nil)
		mcpTestCallTool(t, facade, "getPayment", map[string]any{"paymentId": "PAY_MISSING"}, 2, nil)

		if len(audit) != 2 {
			t.Fatalf("onToolCall ran %d times, want 2", len(audit))
		}
		if audit[0].Tool != "getPayment" || audit[0].IsError || audit[0].Provider != "mock" {
			t.Errorf("first audit = %+v, want a successful getPayment on mock", audit[0])
		}
		if got := failoverTestText(failoverTestJSONMap(audit[0].Result), "paymentId"); got != paymentID {
			t.Errorf("audit result paymentId = %q, want %q", got, paymentID)
		}
		if !audit[1].IsError {
			t.Errorf("second audit = %+v, want a failed call", audit[1])
		}
		if audit[1].Error == "" {
			t.Error("second audit carries no error message")
		}
	})
}

func TestMCPTools(t *testing.T) {
	t.Run("lists the exposed tools with their input schemas", func(t *testing.T) {
		facade, _ := mcpTestNew(t, McpOptions{}, nil)

		response := mcpTestPost(t, facade, map[string]any{"jsonrpc": "2.0", "id": 2, "method": "tools/list"}, nil)
		result := mcpTestResult(response)
		want := []string{"getPayment", "binCheck", "installmentInfo", "refund", "cancel"}
		names := mcpTestToolNames(result)
		if strings.Join(names, ",") != strings.Join(want, ",") {
			t.Errorf("tools = %v, want %v", names, want)
		}

		tools, _ := result["tools"].([]any)
		if len(tools) == 0 {
			t.Fatal("no tools listed")
		}
		first := failoverTestJSONMap(tools[0])
		if description := failoverTestText(first, "description"); !strings.Contains(strings.ToLower(description), "payment") {
			t.Errorf("description = %q, want it to mention payment", description)
		}
		schema := failoverTestJSONMap(first["inputSchema"])
		required := mcpTestStrings(schema["required"])
		if len(required) != 1 || required[0] != "paymentId" {
			t.Errorf("required = %v, want [paymentId]", required)
		}
	})

	t.Run("runs the read-only query tools", func(t *testing.T) {
		facade, _ := mcpTestNew(t, McpOptions{}, nil)
		paymentID := mcpTestPaid(t, facade)

		getPayment := mcpTestCallTool(t, facade, "getPayment", map[string]any{"paymentId": paymentID}, 3, nil)
		mcpTestWantStatus(t, getPayment, 200)
		var payment map[string]any
		if err := json.Unmarshal([]byte(mcpTestContentText(mcpTestResult(getPayment))), &payment); err != nil {
			t.Fatalf("getPayment content: %v", err)
		}
		if failoverTestText(payment, "paymentId") != paymentID {
			t.Errorf("paymentId = %q, want %q", failoverTestText(payment, "paymentId"), paymentID)
		}
		if failoverTestText(payment, "status") != string(core.StatusSuccess) {
			t.Errorf("status = %q, want success", failoverTestText(payment, "status"))
		}

		binCheck := mcpTestCallTool(t, facade, "binCheck", map[string]any{"binNumber": "540667"}, 4, nil)
		var bin map[string]any
		if err := json.Unmarshal([]byte(mcpTestContentText(mcpTestResult(binCheck))), &bin); err != nil {
			t.Fatalf("binCheck content: %v", err)
		}
		if failoverTestText(bin, "bankName") != "Mock Bank" {
			t.Errorf("bankName = %q, want Mock Bank", failoverTestText(bin, "bankName"))
		}

		installments := mcpTestCallTool(t, facade, "installmentInfo", map[string]any{
			"binNumber": "540667", "price": "1000.00",
		}, 5, nil)
		mcpTestWantStatus(t, installments, 200)
		if _, failed := mcpTestResult(installments)["isError"]; failed {
			t.Errorf("isError = %v, want absent", mcpTestResult(installments)["isError"])
		}
	})

	t.Run("answers ping and serves the info endpoint", func(t *testing.T) {
		facade, _ := mcpTestNew(t, McpOptions{ServerName: "shop-mcp", ServerVersion: "3.1.0"}, nil)

		ping := mcpTestPost(t, facade, map[string]any{"jsonrpc": "2.0", "id": 9, "method": "ping"}, nil)
		mcpTestWantStatus(t, ping, 200)
		if result := mcpTestResult(ping); len(result) != 0 {
			t.Errorf("ping result = %v, want an empty object", result)
		}

		info := facade.serve(t, "GET", "/mcp/info", nil, nil)
		mcpTestWantStatus(t, info, 200)
		body := failoverTestJSONMap(info.Body)
		if failoverTestText(body, "name") != "shop-mcp" {
			t.Errorf("name = %q, want shop-mcp", failoverTestText(body, "name"))
		}
		if failoverTestText(body, "version") != "3.1.0" {
			t.Errorf("version = %q, want 3.1.0", failoverTestText(body, "version"))
		}
		if failoverTestText(body, "protocolVersion") != "2025-06-18" {
			t.Errorf("protocolVersion = %q, want 2025-06-18", failoverTestText(body, "protocolVersion"))
		}
		names := mcpTestStrings(body["tools"])
		want := []string{"getPayment", "binCheck", "installmentInfo", "refund", "cancel"}
		if strings.Join(names, ",") != strings.Join(want, ",") {
			t.Errorf("tools = %v, want %v", names, want)
		}
	})
}
