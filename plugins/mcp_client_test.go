package plugins

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"regexp"
	"strings"
	"sync"
	"testing"

	"github.com/boracomet/go-pos/adapters"
	"github.com/boracomet/go-pos/core"
)

// The MCP plugin driven the way an agent drives it: over a real HTTP server,
// through initialize, the initialized notification, tools/list and tools/call.

const (
	mcpClientTestAdminToken = "Bearer admin_token"
	mcpClientTestReadToken  = "Bearer read_only_token"
)

var mcpClientTestRefusal = regexp.MustCompile(`refunded|refuse|Unable`)

func mcpClientTestBearer(request *core.HandlerRequest) string {
	return mcpTestHeaderOf(request, "authorization")
}

func mcpClientTestMessage(raw []byte) string {
	var body map[string]any
	if err := json.Unmarshal(raw, &body); err != nil {
		return string(raw)
	}
	if failure, ok := body["error"].(map[string]any); ok {
		if message := failoverTestText(failure, "message"); message != "" {
			return message
		}
	}
	if message := failoverTestText(body, "message"); message != "" {
		return message
	}
	return string(raw)
}

func mcpClientTestPost(t *testing.T, endpoint string, headers map[string]string, body map[string]any) (int, []byte) {
	t.Helper()
	encoded, err := json.Marshal(body)
	if err != nil {
		t.Fatalf("marshal rpc body: %v", err)
	}
	request, err := http.NewRequest(http.MethodPost, endpoint, bytes.NewReader(encoded))
	if err != nil {
		t.Fatalf("new request: %v", err)
	}
	for name, value := range headers {
		request.Header.Set(name, value)
	}
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		t.Fatalf("post %s: %v", endpoint, err)
	}
	defer response.Body.Close()
	raw, err := io.ReadAll(response.Body)
	if err != nil {
		t.Fatalf("read response: %v", err)
	}
	return response.StatusCode, raw
}

// mcpClientTestClient is the Streamable HTTP client session: the headers of one
// agent, the negotiated protocol version and the server identity.
type mcpClientTestClient struct {
	endpoint        string
	headers         map[string]string
	protocolVersion string
	serverVersion   map[string]any
	capabilities    map[string]any
	nextID          int
}

func mcpClientTestConnect(t *testing.T, endpoint, token string, extra map[string]string) (*mcpClientTestClient, error) {
	t.Helper()
	headers := map[string]string{
		"content-type": "application/json",
		"accept":       "application/json, text/event-stream",
	}
	if token != "" {
		headers["authorization"] = token
	}
	for name, value := range extra {
		headers[name] = value
	}

	status, raw := mcpClientTestPost(t, endpoint, headers, map[string]any{
		"jsonrpc": "2.0",
		"id":      1,
		"method":  "initialize",
		"params": map[string]any{
			"protocolVersion": "2025-06-18",
			"capabilities":    map[string]any{},
			"clientInfo":      map[string]any{"name": "spec-client", "version": "1.0.0"},
		},
	})
	if status != 200 {
		return nil, fmt.Errorf("initialize: HTTP %d: %s", status, mcpClientTestMessage(raw))
	}
	var envelope map[string]any
	if err := json.Unmarshal(raw, &envelope); err != nil {
		return nil, fmt.Errorf("initialize: %w", err)
	}
	if failure, ok := envelope["error"].(map[string]any); ok {
		return nil, fmt.Errorf("initialize: %s", failoverTestText(failure, "message"))
	}
	result := failoverTestJSONMap(envelope["result"])
	client := &mcpClientTestClient{
		endpoint:        endpoint,
		headers:         headers,
		protocolVersion: failoverTestText(result, "protocolVersion"),
		serverVersion:   failoverTestJSONMap(result["serverInfo"]),
		capabilities:    failoverTestJSONMap(result["capabilities"]),
		nextID:          1,
	}
	mcpClientTestPost(t, endpoint, headers, map[string]any{
		"jsonrpc": "2.0", "method": "notifications/initialized",
	})
	return client, nil
}

func (c *mcpClientTestClient) request(t *testing.T, method string, params map[string]any) (map[string]any, error) {
	t.Helper()
	c.nextID++
	status, raw := mcpClientTestPost(t, c.endpoint, c.headers, map[string]any{
		"jsonrpc": "2.0", "id": c.nextID, "method": method, "params": params,
	})
	if status != 200 {
		return nil, fmt.Errorf("%s: HTTP %d: %s", method, status, mcpClientTestMessage(raw))
	}
	var envelope map[string]any
	if err := json.Unmarshal(raw, &envelope); err != nil {
		return nil, fmt.Errorf("%s: %w", method, err)
	}
	if failure, ok := envelope["error"].(map[string]any); ok {
		return nil, fmt.Errorf("%s", failoverTestText(failure, "message"))
	}
	return failoverTestJSONMap(envelope["result"]), nil
}

func (c *mcpClientTestClient) listTools(t *testing.T) []map[string]any {
	t.Helper()
	result, err := c.request(t, "tools/list", map[string]any{})
	if err != nil {
		t.Fatalf("tools/list: %v", err)
	}
	list, _ := result["tools"].([]any)
	tools := make([]map[string]any, 0, len(list))
	for _, tool := range list {
		tools = append(tools, failoverTestJSONMap(tool))
	}
	return tools
}

func (c *mcpClientTestClient) callTool(t *testing.T, name string, args map[string]any) (map[string]any, error) {
	t.Helper()
	return c.request(t, "tools/call", map[string]any{"name": name, "arguments": args})
}

func TestMCPClientOverHTTP(t *testing.T) {
	var mu sync.Mutex
	var audit []string
	auditCount := func() int {
		mu.Lock()
		defer mu.Unlock()
		return len(audit)
	}

	plugin, err := MCP(McpOptions{
		Path: "/mcp",
		Authorize: func(request *core.HandlerRequest, tool McpToolName) (bool, error) {
			bearer := mcpClientTestBearer(request)
			return bearer == mcpClientTestAdminToken ||
				(bearer == mcpClientTestReadToken && tool != McpToolRefund && tool != McpToolCancel), nil
		},
		OnToolCall: func(info McpToolCallInfo) {
			mu.Lock()
			audit = append(audit, info.Tool)
			mu.Unlock()
		},
	})
	if err != nil {
		t.Fatalf("mcp: %v", err)
	}
	provider := failoverTestNewProvider("mock")
	facade := failoverTestNewFacade(t, failoverTestSetup{
		Providers:       []*failoverTestProvider{provider},
		DefaultProvider: "mock",
		Plugins:         []core.Plugin{plugin},
		BasePath:        "/api/pay",
		// The MCP endpoints are privileged: the handler only builds with this hook.
		Authorize: func(_ context.Context, hctx core.HandlerContext) (bool, error) {
			return strings.HasPrefix(mcpClientTestBearer(hctx.Request), "Bearer "), nil
		},
	})
	paymentID := mcpTestPaid(t, facade)
	declinedID := failoverTestText(facade.createPayment(t, failoverTestCardRequest(failoverTestCardDeclined)), "paymentId")

	server := httptest.NewServer(adapters.HTTPHandler(facade.handlerFor(t)))
	t.Cleanup(server.Close)
	endpoint := server.URL + "/api/pay/mcp"

	t.Run("negotiates a protocol version and lists the plugin tools", func(t *testing.T) {
		client, err := mcpClientTestConnect(t, endpoint, mcpClientTestAdminToken, nil)
		if err != nil {
			t.Fatalf("connect: %v", err)
		}

		if client.protocolVersion != "2025-06-18" {
			t.Errorf("protocolVersion = %q, want 2025-06-18", client.protocolVersion)
		}
		if name := failoverTestText(client.serverVersion, "name"); name != mcpDefaultServerName {
			t.Errorf("serverInfo.name = %q, want %q", name, mcpDefaultServerName)
		}
		if _, ok := client.capabilities["tools"].(map[string]any); !ok {
			t.Errorf("capabilities = %v, want a tools object", client.capabilities)
		}

		tools := client.listTools(t)
		names := make([]string, 0, len(tools))
		for _, tool := range tools {
			names = append(names, failoverTestText(tool, "name"))
		}
		want := []string{"getPayment", "binCheck", "installmentInfo", "refund", "cancel"}
		if strings.Join(names, ",") != strings.Join(want, ",") {
			t.Errorf("tools = %v, want %v", names, want)
		}
		if len(tools) == 0 {
			t.Fatal("no tools listed")
		}
		schema := failoverTestJSONMap(tools[0]["inputSchema"])
		if failoverTestText(schema, "type") != "object" {
			t.Errorf("inputSchema.type = %q, want object", failoverTestText(schema, "type"))
		}
		if required := mcpTestStrings(schema["required"]); strings.Join(required, ",") != "paymentId" {
			t.Errorf("required = %v, want [paymentId]", required)
		}
	})

	t.Run("answers the initialized notification with 202 and no body", func(t *testing.T) {
		status, raw := mcpClientTestPost(t, endpoint, map[string]string{
			"content-type":  "application/json",
			"accept":        "application/json, text/event-stream",
			"authorization": mcpClientTestAdminToken,
		}, map[string]any{"jsonrpc": "2.0", "method": "notifications/initialized"})

		if status != 202 {
			t.Errorf("status = %d, want 202", status)
		}
		if len(raw) != 0 {
			t.Errorf("body = %q, want empty", string(raw))
		}
	})

	t.Run("runs a read-only tool and keeps card data out of the result", func(t *testing.T) {
		client, err := mcpClientTestConnect(t, endpoint, mcpClientTestAdminToken, nil)
		if err != nil {
			t.Fatalf("connect: %v", err)
		}

		result, err := client.callTool(t, "getPayment", map[string]any{"paymentId": paymentID})
		if err != nil {
			t.Fatalf("tools/call: %v", err)
		}
		if result["isError"] == true {
			t.Fatalf("isError = true, result %v", result)
		}
		text := mcpTestContentText(result)
		var payment map[string]any
		if err := json.Unmarshal([]byte(text), &payment); err != nil {
			t.Fatalf("tool content: %v", err)
		}
		if failoverTestText(payment, "paymentId") != paymentID {
			t.Errorf("paymentId = %q, want %q", failoverTestText(payment, "paymentId"), paymentID)
		}
		if failoverTestText(payment, "status") != string(core.StatusSuccess) {
			t.Errorf("status = %q, want success", failoverTestText(payment, "status"))
		}
		if mcpTestPANRun.MatchString(text) {
			t.Errorf("tool result carries a PAN-shaped run: %s", text)
		}
	})

	t.Run("reports a failing tool call as isError instead of a protocol error", func(t *testing.T) {
		client, err := mcpClientTestConnect(t, endpoint, mcpClientTestAdminToken, map[string]string{
			"cf-connecting-ip": "203.0.113.9",
		})
		if err != nil {
			t.Fatalf("connect: %v", err)
		}

		result, err := client.callTool(t, "refund", map[string]any{"paymentId": declinedID, "price": "1.00"})
		if err != nil {
			t.Fatalf("a failed tool must not be a protocol error: %v", err)
		}
		if result["isError"] != true {
			t.Fatalf("isError = %v, want true", result["isError"])
		}
		if text := mcpTestContentText(result); !mcpClientTestRefusal.MatchString(text) {
			t.Errorf("content = %q, want the provider's refusal", text)
		}

		missing, err := client.callTool(t, "refund", map[string]any{"paymentId": "PAY_DOES_NOT_EXIST", "price": "1.00"})
		if err != nil {
			t.Fatalf("tools/call: %v", err)
		}
		if missing["isError"] != true {
			t.Errorf("isError = %v, want true for an unknown payment", missing["isError"])
		}
	})

	t.Run("refuses a write tool without an admin token", func(t *testing.T) {
		client, err := mcpClientTestConnect(t, endpoint, mcpClientTestReadToken, map[string]string{
			"cf-connecting-ip": "203.0.113.9",
		})
		if err != nil {
			t.Fatalf("connect: %v", err)
		}

		before := provider.count(core.OperationRefund)
		if _, err := client.callTool(t, "refund", map[string]any{"paymentId": paymentID, "price": "1.00"}); err == nil {
			t.Fatal("a read-only token refunded a payment")
		} else if !strings.Contains(err.Error(), "Unauthorized") {
			t.Errorf("error = %q, want Unauthorized", err.Error())
		}
		if provider.count(core.OperationRefund) != before {
			t.Error("the denied refund reached the provider")
		}
	})

	t.Run("keeps an unauthenticated client away from every tool", func(t *testing.T) {
		before := auditCount()

		if _, err := mcpClientTestConnect(t, endpoint, "", nil); err == nil {
			t.Fatal("an unauthenticated client connected")
		} else if !strings.Contains(err.Error(), "Forbidden") {
			t.Errorf("error = %q, want Forbidden", err.Error())
		}
		if after := auditCount(); after != before {
			t.Errorf("audit grew from %d to %d, want no tool call", before, after)
		}
	})
}
