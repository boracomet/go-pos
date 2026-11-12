package plugins

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"regexp"
	"strings"
	"time"

	"github.com/boracomet/go-pos/core"
)

// McpToolName is a tool the MCP server can expose.
type McpToolName string

const (
	// McpToolGetPayment looks up a payment by id.
	McpToolGetPayment McpToolName = "getPayment"
	// McpToolBinCheck queries a card BIN.
	McpToolBinCheck McpToolName = "binCheck"
	// McpToolInstallmentInfo queries installment options.
	McpToolInstallmentInfo McpToolName = "installmentInfo"
	// McpToolRefund refunds a captured payment.
	McpToolRefund McpToolName = "refund"
	// McpToolCancel voids an unsettled payment.
	McpToolCancel McpToolName = "cancel"
)

// AllMcpTools lists every tool, in the order tools/list reports them.
var AllMcpTools = []McpToolName{
	McpToolGetPayment,
	McpToolBinCheck,
	McpToolInstallmentInfo,
	McpToolRefund,
	McpToolCancel,
}

// WriteMcpTools are the tools that move money and need an Authorize option.
var WriteMcpTools = []McpToolName{McpToolRefund, McpToolCancel}

// McpProtocolVersions are the protocol versions initialize answers with, newest
// first. A client's version is echoed when it is in this list.
var McpProtocolVersions = []string{"2025-06-18", "2025-03-26", "2024-11-05"}

const (
	mcpDefaultProtocolVersion = "2025-06-18"
	mcpDefaultServerName      = "go-pos-mcp"
)

// JSON-RPC 2.0 error codes, plus the MCP-specific one this server adds.
const (
	mcpInvalidRequest = -32600
	mcpMethodNotFound = -32601
	mcpUnauthorized   = -32001
)

// McpToolCallInfo is the audit record of one tool call. Args and Result are
// sanitized: card-shaped keys are removed and PAN-shaped runs are masked.
type McpToolCallInfo struct {
	Tool       string         `json:"tool"`
	Args       map[string]any `json:"args"`
	Result     any            `json:"result,omitempty"`
	Error      string         `json:"error,omitempty"`
	IsError    bool           `json:"isError"`
	DurationMs int64          `json:"durationMs"`
	Provider   string         `json:"provider"`
	IP         string         `json:"ip,omitempty"`
	Timestamp  int64          `json:"timestamp"`
}

// McpOptions configures the MCP server plugin.
type McpOptions struct {
	// Path is the endpoint path relative to the handler basePath. Default: "/mcp".
	Path string
	// Tools lists the exposed tools. Nil exposes all of them.
	Tools []McpToolName
	// Authorize runs before every tool call. Write tools need it: without it they
	// are refused. The endpoints are privileged, so the handler's own authorize
	// hook is required as well and runs first.
	Authorize func(request *core.HandlerRequest, tool McpToolName) (bool, error)
	// ReadOnly exposes only the read-only tools.
	ReadOnly bool
	// ServerName is reported in serverInfo. Default: "go-pos-mcp".
	ServerName string
	// ServerVersion is reported in serverInfo. Default: the library version.
	ServerVersion string
	// GetClientIP resolves the address handed to write operations and recorded in
	// the audit trail. The default reads cf-connecting-ip, then x-real-ip: headers
	// an edge overwrites rather than appends to. x-forwarded-for is not read,
	// because a client can put whatever it likes in it.
	GetClientIP func(request *core.HandlerRequest) string
	// OnToolCall is invoked on every tool call, denied and failed ones included.
	OnToolCall func(info McpToolCallInfo)
}

type mcpToolDefinition struct {
	description string
	properties  []mcpToolProperty
	required    []string
}

type mcpToolProperty struct {
	name        string
	kind        string
	description string
}

func (d mcpToolDefinition) inputSchema() map[string]any {
	properties := make(map[string]any, len(d.properties))
	for _, property := range d.properties {
		properties[property.name] = map[string]any{
			"type":        property.kind,
			"description": property.description,
		}
	}
	return map[string]any{
		"type":       "object",
		"properties": properties,
		"required":   d.required,
	}
}

var mcpToolDefinitions = map[McpToolName]mcpToolDefinition{
	McpToolGetPayment: {
		description: "Look up payment details and status by payment ID",
		properties: []mcpToolProperty{
			{"paymentId", "string", "The provider payment ID returned during checkout"},
			{"provider", "string", "Payment provider ID (e.g. iyzico, paytr). Defaults to default provider"},
		},
		required: []string{"paymentId"},
	},
	McpToolBinCheck: {
		description: "Query card issuer bank, card family, card type and brand from first 6-8 digits of a card",
		properties: []mcpToolProperty{
			{"binNumber", "string", "The first 6 to 8 digits of the payment card"},
			{"provider", "string", "Payment provider ID"},
		},
		required: []string{"binNumber"},
	},
	McpToolInstallmentInfo: {
		description: "Query available installment counts and bank campaign commission rates for card BIN and amount",
		properties: []mcpToolProperty{
			{"binNumber", "string", "Card BIN number (first 6-8 digits)"},
			{"price", "string", `Transaction amount (e.g. "100.00")`},
			{"currency", "string", "Currency code (e.g. TRY, USD)"},
			{"provider", "string", "Payment provider ID"},
		},
		required: []string{"binNumber", "price"},
	},
	McpToolRefund: {
		description: "Issue a partial or full refund for a captured payment transaction",
		properties: []mcpToolProperty{
			{"paymentId", "string", "Payment ID to refund"},
			{"price", "string", "Amount to refund"},
			{"currency", "string", "Currency code (default: TRY)"},
			{"conversationId", "string", "Merchant reference or ticket ID"},
			{"provider", "string", "Payment provider ID"},
		},
		required: []string{"paymentId", "price"},
	},
	McpToolCancel: {
		description: "Void / cancel an unsettled transaction on the same day before batch settlement",
		properties: []mcpToolProperty{
			{"paymentId", "string", "Payment ID to void/cancel"},
			{"conversationId", "string", "Merchant reference or ticket ID"},
			{"provider", "string", "Payment provider ID"},
		},
		required: []string{"paymentId"},
	},
}

var mcpSensitiveCardKeys = map[string]bool{
	"card":           true,
	"cardNumber":     true,
	"cvv":            true,
	"securityCode":   true,
	"expireMonth":    true,
	"expireYear":     true,
	"expiryMonth":    true,
	"expiryYear":     true,
	"cardHolderName": true,
	"pan":            true,
}

// mcpDigitRun matches a maximal digit run, card-style separators included.
var mcpDigitRun = regexp.MustCompile(`\d[\d -]*\d`)

var mcpNonDigit = regexp.MustCompile(`\D`)

// mcpMaskPANRuns masks every 16-19 digit run, keeping the last four digits.
// Length only, no Luhn check: a long numeric id is masked too, which is the safe
// direction for a response a model reads.
func mcpMaskPANRuns(value string) string {
	return mcpDigitRun.ReplaceAllStringFunc(value, func(run string) string {
		digits := mcpNonDigit.ReplaceAllString(run, "")
		if len(digits) < 16 || len(digits) > 19 {
			return run
		}
		return strings.Repeat("*", len(digits)-4) + digits[len(digits)-4:]
	})
}

func mcpSanitize(value any) any {
	switch typed := value.(type) {
	case string:
		return mcpMaskPANRuns(typed)
	case map[string]any:
		clean := make(map[string]any, len(typed))
		for key, item := range typed {
			if mcpSensitiveCardKeys[key] {
				continue
			}
			clean[key] = mcpSanitize(item)
		}
		return clean
	case []any:
		out := make([]any, len(typed))
		for i, item := range typed {
			out[i] = mcpSanitize(item)
		}
		return out
	case nil, bool, float64, float32, int, int8, int16, int32, int64,
		uint, uint8, uint16, uint32, uint64, json.Number:
		return value
	}

	data, err := json.Marshal(value)
	if err != nil {
		return value
	}
	var generic any
	if err := json.Unmarshal(data, &generic); err != nil {
		return value
	}
	switch typed := generic.(type) {
	case map[string]any, []any:
		return mcpSanitize(typed)
	case string:
		return mcpMaskPANRuns(typed)
	default:
		return generic
	}
}

// mcpPrettyJSON renders a value the way JSON.stringify(value, null, 2) does.
func mcpPrettyJSON(value any) (string, error) {
	var buffer bytes.Buffer
	encoder := json.NewEncoder(&buffer)
	encoder.SetEscapeHTML(false)
	encoder.SetIndent("", "  ")
	if err := encoder.Encode(value); err != nil {
		return "", err
	}
	return strings.TrimSuffix(buffer.String(), "\n"), nil
}

func mcpTrimSlashes(path string) string {
	start, end := 0, len(path)
	for start < end && path[start] == '/' {
		start++
	}
	for end > start && path[end-1] == '/' {
		end--
	}
	return path[start:end]
}

func mcpHeader(request *core.HandlerRequest, name string) (string, bool) {
	if request == nil {
		return "", false
	}
	for key, value := range request.Headers {
		if strings.ToLower(key) == name {
			return value, true
		}
	}
	return "", false
}

var mcpClientIPHeaders = []string{"cf-connecting-ip", "x-real-ip"}

func mcpDefaultClientIP(request *core.HandlerRequest) string {
	for _, header := range mcpClientIPHeaders {
		value, ok := mcpHeader(request, header)
		if !ok {
			continue
		}
		first := strings.TrimSpace(strings.Split(value, ",")[0])
		if first != "" {
			return first
		}
	}
	return ""
}

func mcpResult(id any, value any) map[string]any {
	return map[string]any{"jsonrpc": "2.0", "id": id, "result": value}
}

func mcpRPCError(id any, code int, message string) map[string]any {
	return map[string]any{"jsonrpc": "2.0", "id": id, "error": map[string]any{"code": code, "message": message}}
}

// mcpIsRequestID reports whether a JSON-RPC id makes the message a request rather
// than a notification.
func mcpIsRequestID(id any) bool {
	switch id.(type) {
	case string, float64, float32, int, int8, int16, int32, int64,
		uint, uint8, uint16, uint32, uint64, json.Number:
		return true
	}
	return false
}

func mcpIsFailureResult(value any) bool {
	switch typed := value.(type) {
	case *core.PaymentResponse:
		return typed != nil && typed.Status == core.StatusFailure
	case *core.RefundResponse:
		return typed != nil && typed.Status == core.StatusFailure
	case *core.CancelResponse:
		return typed != nil && typed.Status == core.StatusFailure
	case *core.BinCheckResponse:
		return false
	case *core.InstallmentInfoResponse:
		return typed != nil && typed.Status == core.StatusFailure
	case map[string]any:
		return typed["status"] == string(core.StatusFailure)
	}
	return false
}

func mcpFailureMessage(value any) string {
	var message string
	switch typed := value.(type) {
	case *core.PaymentResponse:
		if typed != nil {
			message = typed.ErrorMessage
		}
	case *core.RefundResponse:
		if typed != nil {
			message = typed.ErrorMessage
		}
	case *core.CancelResponse:
		if typed != nil {
			message = typed.ErrorMessage
		}
	case *core.InstallmentInfoResponse:
		if typed != nil {
			message = typed.ErrorMessage
		}
	case map[string]any:
		message, _ = typed["errorMessage"].(string)
	}
	if message != "" {
		return message
	}
	return "The provider rejected the operation"
}

func mcpArgString(args map[string]any, key string) string {
	value, ok := args[key]
	if !ok || value == nil {
		return ""
	}
	if text, ok := value.(string); ok {
		return text
	}
	return fmt.Sprint(value)
}

func mcpArgMissing(args map[string]any, key string) bool {
	value, ok := args[key]
	return !ok || value == nil || value == ""
}

// MCP builds the plugin that exposes payment queries and write operations as MCP
// tools over JSON-RPC 2.0 (Streamable HTTP). Both endpoints are privileged: the
// handler only serves them with an authorize hook, and the plugin's own Authorize
// option runs before every tool call on top of that.
func MCP(options McpOptions) (core.Plugin, error) {
	rawPath := options.Path
	if rawPath == "" {
		rawPath = "/mcp"
	}
	mcpPath := "/" + mcpTrimSlashes(rawPath)
	serverName := options.ServerName
	if serverName == "" {
		serverName = mcpDefaultServerName
	}
	serverVersion := options.ServerVersion
	if serverVersion == "" {
		serverVersion = core.Version
	}

	requested := options.Tools
	if requested == nil {
		requested = AllMcpTools
	}
	writes := map[McpToolName]bool{}
	for _, tool := range WriteMcpTools {
		writes[tool] = true
	}
	known := map[McpToolName]bool{}
	for _, tool := range AllMcpTools {
		known[tool] = true
	}
	// A set keeps the declaration order and drops duplicates.
	seen := map[McpToolName]bool{}
	allowedTools := make([]McpToolName, 0, len(requested))
	for _, tool := range requested {
		if options.ReadOnly && writes[tool] {
			continue
		}
		if !seen[tool] {
			seen[tool] = true
			allowedTools = append(allowedTools, tool)
		}
	}

	serverInfo := func() map[string]any {
		tools := make([]string, 0, len(allowedTools))
		for _, tool := range allowedTools {
			tools = append(tools, string(tool))
		}
		return map[string]any{
			"name":            serverName,
			"version":         serverVersion,
			"protocolVersion": mcpDefaultProtocolVersion,
			"status":          "running",
			"tools":           tools,
		}
	}

	callTool := func(ectx core.EndpointContext, id any, params map[string]any) (*core.HandlerResponse, error) {
		toolName, _ := params["name"].(string)
		args, _ := params["arguments"].(map[string]any)
		if args == nil {
			args = map[string]any{}
		}
		safeArgs, _ := mcpSanitize(args).(map[string]any)
		if safeArgs == nil {
			safeArgs = map[string]any{}
		}
		ip := mcpDefaultClientIP(ectx.Request())
		if options.GetClientIP != nil {
			ip = options.GetClientIP(ectx.Request())
		}
		started := time.Now().UnixMilli()

		if toolName == "" || !known[McpToolName(toolName)] {
			return ectx.JSON(mcpRPCError(id, mcpMethodNotFound, fmt.Sprintf("Tool '%v' is not defined", params["name"]))), nil
		}
		if !seen[McpToolName(toolName)] {
			return ectx.JSON(mcpRPCError(id, mcpMethodNotFound, fmt.Sprintf("Tool '%s' is not exposed by this server", toolName))), nil
		}

		providerID := mcpArgString(args, "provider")
		if providerID == "" {
			providerID = ectx.DefaultProvider()
		}
		if providerID == "" {
			if ids := ectx.ProviderIDs(); len(ids) > 0 {
				providerID = ids[0]
			}
		}
		if providerID == "" {
			providerID = "default"
		}

		// Denied and failed calls are audit events too: the hook and the log line
		// name the attempt without ever carrying the request or the result.
		record := func(outcome, message string) {
			if options.OnToolCall != nil {
				options.OnToolCall(McpToolCallInfo{
					Tool:       toolName,
					Args:       safeArgs,
					Error:      message,
					IsError:    true,
					DurationMs: time.Now().UnixMilli() - started,
					Provider:   providerID,
					IP:         ip,
					Timestamp:  time.Now().UnixMilli(),
				})
			}
			if logger := ectx.Logger(); logger != nil {
				logger.Info(fmt.Sprintf("mcp: tool '%s' %s", toolName, outcome), map[string]any{
					"tool":       toolName,
					"provider":   providerID,
					"durationMs": time.Now().UnixMilli() - started,
				})
			}
		}

		// A failing tool answers with isError, not with a JSON-RPC error: that is how
		// MCP clients show the failure to the model instead of dropping the call.
		fail := func(message string, data any) (*core.HandlerResponse, error) {
			record("failed", message)
			text := message
			if data != nil {
				encoded, err := mcpPrettyJSON(data)
				if err != nil {
					return nil, err
				}
				text = message + "\n" + encoded
			}
			return ectx.JSON(mcpResult(id, map[string]any{
				"content": []any{map[string]any{"type": "text", "text": text}},
				"isError": true,
			})), nil
		}

		deny := func(message string) *core.HandlerResponse {
			record("denied", message)
			return ectx.JSON(mcpRPCError(id, mcpUnauthorized, message), 401)
		}

		isWrite := writes[McpToolName(toolName)]
		if isWrite && options.Authorize == nil {
			return deny(fmt.Sprintf(
				"Tool '%s' is a write operation: the mcp plugin needs an 'authorize' option", toolName)), nil
		}
		if options.Authorize != nil {
			allowed, err := options.Authorize(ectx.Request(), McpToolName(toolName))
			if err != nil {
				return nil, err
			}
			if !allowed {
				return deny(fmt.Sprintf("Unauthorized: access to tool '%s' was denied", toolName)), nil
			}
		}

		var missing []string
		for _, key := range mcpToolDefinitions[McpToolName(toolName)].required {
			if mcpArgMissing(args, key) {
				missing = append(missing, key)
			}
		}
		if len(missing) > 0 {
			return fail("Missing required argument(s): "+strings.Join(missing, ", "), nil)
		}
		if isWrite && ip == "" {
			return fail("No client IP for this request: set the mcp plugin's getClientIp option or a forwarding header your edge controls", nil)
		}

		// The proxied provider runs the plugin hooks and emits the payment events of
		// write operations, so refunds and cancellations reach the event listeners.
		provider, err := ectx.Use(providerID)
		if err != nil {
			return fail(err.Error(), nil)
		}
		var executionResult any
		switch McpToolName(toolName) {
		case McpToolGetPayment:
			executionResult, err = provider.GetPayment(context.Background(), mcpArgString(args, "paymentId"))
		case McpToolBinCheck:
			executionResult, err = provider.BinCheck(context.Background(), mcpArgString(args, "binNumber"))
		case McpToolInstallmentInfo:
			executionResult, err = provider.InstallmentInfo(context.Background(), &core.InstallmentInfoRequest{
				BinNumber:      mcpArgString(args, "binNumber"),
				Price:          mcpArgString(args, "price"),
				ConversationID: mcpArgString(args, "conversationId"),
			})
		case McpToolRefund:
			currency := "TRY"
			if value, ok := args["currency"]; ok && value != nil {
				currency = mcpArgString(args, "currency")
			}
			executionResult, err = provider.Refund(context.Background(), &core.RefundRequest{
				PaymentID:      mcpArgString(args, "paymentId"),
				Price:          mcpArgString(args, "price"),
				Currency:       currency,
				ConversationID: mcpArgString(args, "conversationId"),
				IP:             ip,
			})
		case McpToolCancel:
			executionResult, err = provider.Cancel(context.Background(), &core.CancelRequest{
				PaymentID:      mcpArgString(args, "paymentId"),
				ConversationID: mcpArgString(args, "conversationId"),
				IP:             ip,
			})
		}
		if err != nil {
			return fail(err.Error(), nil)
		}

		safeResult := mcpSanitize(executionResult)
		// A provider answer that says failure is a failed call, not a successful one
		// that happens to carry a reason.
		if mcpIsFailureResult(executionResult) {
			return fail(mcpFailureMessage(executionResult), safeResult)
		}
		if options.OnToolCall != nil {
			options.OnToolCall(McpToolCallInfo{
				Tool:       toolName,
				Args:       safeArgs,
				Result:     safeResult,
				IsError:    false,
				DurationMs: time.Now().UnixMilli() - started,
				Provider:   providerID,
				IP:         ip,
				Timestamp:  time.Now().UnixMilli(),
			})
		}
		// Never the request or the result: an audit line names the call only.
		if logger := ectx.Logger(); logger != nil {
			logger.Info(fmt.Sprintf("mcp: tool '%s' called", toolName), map[string]any{
				"tool":       toolName,
				"provider":   providerID,
				"durationMs": time.Now().UnixMilli() - started,
			})
		}

		text, err := mcpPrettyJSON(safeResult)
		if err != nil {
			return nil, err
		}
		return ectx.JSON(mcpResult(id, map[string]any{
			"content": []any{map[string]any{"type": "text", "text": text}},
		})), nil
	}

	rpc := func(ectx core.EndpointContext) (*core.HandlerResponse, error) {
		body := ectx.Body()
		if body == nil {
			return ectx.JSON(mcpRPCError(nil, mcpInvalidRequest, "Expected a single JSON-RPC 2.0 object")), nil
		}

		id := body["id"]
		method, _ := body["method"].(string)

		// Notifications carry no id: answer 202 with an empty body, as the Streamable
		// HTTP transport of the MCP spec requires.
		if !mcpIsRequestID(id) {
			return ectx.JSON(nil, 202), nil
		}
		if body["jsonrpc"] != "2.0" || method == "" {
			return ectx.JSON(mcpRPCError(id, mcpInvalidRequest, "Invalid JSON-RPC 2.0 request")), nil
		}

		params, _ := body["params"].(map[string]any)
		if params == nil {
			params = map[string]any{}
		}

		switch method {
		case "initialize":
			protocolVersion := mcpDefaultProtocolVersion
			if requested, ok := params["protocolVersion"].(string); ok {
				for _, version := range McpProtocolVersions {
					if version == requested {
						protocolVersion = requested
						break
					}
				}
			}
			return ectx.JSON(mcpResult(id, map[string]any{
				"protocolVersion": protocolVersion,
				"serverInfo":      map[string]any{"name": serverName, "version": serverVersion},
				"capabilities":    map[string]any{"tools": map[string]any{"listChanged": false}},
			})), nil
		case "ping":
			return ectx.JSON(mcpResult(id, map[string]any{})), nil
		case "tools/list":
			tools := make([]any, 0, len(allowedTools))
			for _, name := range allowedTools {
				definition := mcpToolDefinitions[name]
				tools = append(tools, map[string]any{
					"name":        string(name),
					"description": definition.description,
					"inputSchema": definition.inputSchema(),
				})
			}
			return ectx.JSON(mcpResult(id, map[string]any{"tools": tools})), nil
		case "tools/call":
			return callTool(ectx, id, params)
		default:
			return ectx.JSON(mcpRPCError(id, mcpMethodNotFound, fmt.Sprintf("Method '%s' not found", method))), nil
		}
	}

	return core.DefinePlugin(core.Plugin{
		ID:      PluginIDMCP,
		Options: options,
		Endpoints: map[string]core.PaymentEndpoint{
			"mcpInfo": {
				Method:     "GET",
				Path:       mcpPath + "/info",
				Privileged: true,
				Handler: func(context.Context, core.EndpointContext) (any, error) {
					return serverInfo(), nil
				},
			},
			"mcpRpc": {
				Method:     "POST",
				Path:       mcpPath,
				Privileged: true,
				Handler: func(_ context.Context, ectx core.EndpointContext) (any, error) {
					return rpc(ectx)
				},
			},
		},
	}), nil
}
