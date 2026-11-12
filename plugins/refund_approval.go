package plugins

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"reflect"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/boracomet/go-pos/core"
)

// RefundApprovalStatus is the review state of a queued refund or cancellation.
type RefundApprovalStatus string

const (
	// RefundApprovalPending waits for a reviewer.
	RefundApprovalPending RefundApprovalStatus = "pending"
	// RefundApprovalApproved was approved and replayed on the provider.
	RefundApprovalApproved RefundApprovalStatus = "approved"
	// RefundApprovalRejected was dropped by a reviewer.
	RefundApprovalRejected RefundApprovalStatus = "rejected"
)

// RefundApprovalRequest is one queued refund or cancellation. RequestedBy,
// ApprovedBy and RejectedBy hold a user id string or the user object itself.
type RefundApprovalRequest struct {
	ID              string               `json:"id"`
	Operation       string               `json:"operation"`
	Provider        string               `json:"provider"`
	PaymentID       string               `json:"paymentId"`
	Amount          float64              `json:"amount"`
	Currency        string               `json:"currency"`
	RequestedBy     any                  `json:"requestedBy,omitempty"`
	Status          RefundApprovalStatus `json:"status"`
	Request         any                  `json:"request"`
	CreatedAt       int64                `json:"createdAt"`
	ApprovedAt      int64                `json:"approvedAt,omitempty"`
	ApprovedBy      any                  `json:"approvedBy,omitempty"`
	RejectedAt      int64                `json:"rejectedAt,omitempty"`
	RejectedBy      any                  `json:"rejectedBy,omitempty"`
	RejectionReason string               `json:"rejectionReason,omitempty"`
}

// RefundApprovalFilter narrows a store listing to one status.
type RefundApprovalFilter struct {
	Status RefundApprovalStatus
}

// RefundApprovalStore persists approval requests. Get returns nil when the id is
// unknown; List returns the requests in insertion order.
type RefundApprovalStore interface {
	Save(request RefundApprovalRequest) error
	Get(id string) (*RefundApprovalRequest, error)
	List(filter *RefundApprovalFilter) ([]RefundApprovalRequest, error)
	Delete(id string) error
}

// MemoryRefundApprovalStore keeps approval requests in one process.
type MemoryRefundApprovalStore struct {
	mu    sync.Mutex
	order []string
	items map[string]RefundApprovalRequest
}

// NewMemoryRefundApprovalStore builds an empty in-memory approval store.
func NewMemoryRefundApprovalStore() *MemoryRefundApprovalStore {
	return &MemoryRefundApprovalStore{items: map[string]RefundApprovalRequest{}}
}

// Save stores a copy of the request.
func (s *MemoryRefundApprovalStore) Save(request RefundApprovalRequest) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.items == nil {
		s.items = map[string]RefundApprovalRequest{}
	}
	if _, seen := s.items[request.ID]; !seen {
		s.order = append(s.order, request.ID)
	}
	s.items[request.ID] = request
	return nil
}

// Get returns a copy of the stored request, or nil when the id is unknown.
func (s *MemoryRefundApprovalStore) Get(id string) (*RefundApprovalRequest, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	item, ok := s.items[id]
	if !ok {
		return nil, nil
	}
	return &item, nil
}

// List returns copies of the stored requests, optionally filtered by status.
func (s *MemoryRefundApprovalStore) List(filter *RefundApprovalFilter) ([]RefundApprovalRequest, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]RefundApprovalRequest, 0, len(s.order))
	for _, id := range s.order {
		item, ok := s.items[id]
		if !ok {
			continue
		}
		if filter != nil && filter.Status != "" && item.Status != filter.Status {
			continue
		}
		out = append(out, item)
	}
	return out, nil
}

// Delete drops one request.
func (s *MemoryRefundApprovalStore) Delete(id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.items, id)
	for i, candidate := range s.order {
		if candidate == id {
			s.order = append(s.order[:i], s.order[i+1:]...)
			break
		}
	}
	return nil
}

// RefundApprovalOptions configures the refund approval queue.
type RefundApprovalOptions struct {
	// Threshold is the amount above which refunds require approval, in primary
	// currency units. Required: nil is a configuration error.
	Threshold *float64
	// Store persists approval requests. Default: NewMemoryRefundApprovalStore().
	Store RefundApprovalStore
	// Approvers reports whether a user may approve or reject refunds.
	Approvers func(user any) (bool, error)
	// GetActor resolves the signed-in user of an HTTP request. The approve and
	// reject endpoints take the approver from here, never from the request body,
	// and answer 403 without it.
	GetActor func(request *core.HandlerRequest) (any, error)
	// CancelAmount looks up what a cancellation voids. Without it, or when it
	// returns nil, every cancellation waits for approval.
	CancelAmount func(request *core.CancelRequest, provider string) (any, error)
	// Notify is called when a new approval request is created. An error is logged.
	Notify func(request RefundApprovalRequest) error
}

// refundApprovalErrorMessages are the plugin's error codes and default messages.
var refundApprovalErrorMessages = map[string]string{
	"REFUND_APPROVAL_REQUIRED":       "Refund requires approval from an authorized manager",
	"REFUND_SELF_APPROVAL_FORBIDDEN": "Requester cannot approve their own refund request",
	"REFUND_NOT_AUTHORIZED":          "User is not authorized to approve refunds",
}

// refundApprovalParseAmount reads an amount strictly: "1,000.00", "" and "abc"
// report no amount rather than 1 or 0.
func refundApprovalParseAmount(value any) (float64, bool) {
	var amount float64
	switch typed := value.(type) {
	case nil:
		return 0, false
	case float64:
		amount = typed
	case float32:
		amount = float64(typed)
	case int:
		amount = float64(typed)
	case int64:
		amount = float64(typed)
	case json.Number:
		parsed, err := typed.Float64()
		if err != nil {
			return 0, false
		}
		amount = parsed
	case string:
		trimmed := strings.TrimSpace(typed)
		if trimmed == "" {
			return 0, false
		}
		parsed, err := strconv.ParseFloat(trimmed, 64)
		if err != nil {
			return 0, false
		}
		amount = parsed
	default:
		return 0, false
	}
	if math.IsNaN(amount) || math.IsInf(amount, 0) || amount < 0 {
		return 0, false
	}
	return amount, true
}

// refundApprovalActorRecord reads a user object as a record, whatever its Go type.
func refundApprovalActorRecord(user any) map[string]any {
	switch typed := user.(type) {
	case nil:
		return nil
	case map[string]any:
		return typed
	}
	data, err := json.Marshal(user)
	if err != nil {
		return nil
	}
	var record map[string]any
	if err := json.Unmarshal(data, &record); err != nil {
		return nil
	}
	return record
}

func refundApprovalUserID(user any) (string, bool) {
	switch typed := user.(type) {
	case nil:
		return "", false
	case string:
		if typed == "" {
			return "", false
		}
		return typed, true
	}
	record := refundApprovalActorRecord(user)
	if record == nil {
		return "", false
	}
	for _, key := range []string{"id", "userId", "username", "email"} {
		if value, ok := record[key]; ok && value != nil {
			if text, ok := value.(string); ok {
				return text, true
			}
			return fmt.Sprint(value), true
		}
	}
	return "", false
}

// refundApprovalRequestView is how the before hook reads a refund or cancel
// request: the shared fields plus the caller-supplied extras this queue needs.
type refundApprovalRequestView struct {
	paymentID      string
	price          any
	hasPrice       bool
	amount         any
	currency       string
	conversationID string
	requestedBy    any
}

func refundApprovalViewOf(request any) refundApprovalRequestView {
	view := refundApprovalRequestView{}
	switch typed := request.(type) {
	case *core.RefundRequest:
		if typed != nil {
			view.paymentID, view.currency, view.conversationID = typed.PaymentID, typed.Currency, typed.ConversationID
			view.price, view.hasPrice = typed.Price, true
		}
		return view
	case core.RefundRequest:
		view.paymentID, view.currency, view.conversationID = typed.PaymentID, typed.Currency, typed.ConversationID
		view.price, view.hasPrice = typed.Price, true
		return view
	case *core.CancelRequest:
		if typed != nil {
			view.paymentID, view.currency, view.conversationID = typed.PaymentID, typed.Currency, typed.ConversationID
		}
		return view
	case core.CancelRequest:
		view.paymentID, view.currency, view.conversationID = typed.PaymentID, typed.Currency, typed.ConversationID
		return view
	case map[string]any:
		view.paymentID, _ = typed["paymentId"].(string)
		view.currency, _ = typed["currency"].(string)
		view.conversationID, _ = typed["conversationId"].(string)
		if value, ok := typed["price"]; ok && value != nil {
			view.price, view.hasPrice = value, true
		}
		view.amount = typed["amount"]
		for _, key := range []string{"requestedBy", "by", "userId"} {
			if value, ok := typed[key]; ok && value != nil {
				view.requestedBy = value
				break
			}
		}
		return view
	}
	record := refundApprovalActorRecord(request)
	if record == nil {
		return view
	}
	return refundApprovalViewOf(record)
}

// refundApprovalTypedRequest rebuilds the stored request as the provider's type.
func refundApprovalTypedRequest(operation string, request any) (any, error) {
	if operation == "cancel" {
		switch typed := request.(type) {
		case *core.CancelRequest:
			if typed == nil {
				return nil, core.NewError("Approval request carries no request", string(core.CodeInvalidRequest))
			}
			copyOf := *typed
			return &copyOf, nil
		case core.CancelRequest:
			return &typed, nil
		}
		var out core.CancelRequest
		if err := refundApprovalReencode(request, &out); err != nil {
			return nil, err
		}
		return &out, nil
	}
	switch typed := request.(type) {
	case *core.RefundRequest:
		if typed == nil {
			return nil, core.NewError("Approval request carries no request", string(core.CodeInvalidRequest))
		}
		copyOf := *typed
		return &copyOf, nil
	case core.RefundRequest:
		return &typed, nil
	}
	var out core.RefundRequest
	if err := refundApprovalReencode(request, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

func refundApprovalReencode(request any, out any) error {
	data, err := json.Marshal(request)
	if err != nil {
		return err
	}
	return json.Unmarshal(data, out)
}

func refundApprovalBaseCode(err error) (string, bool) {
	var base *core.BaseError
	if errors.As(err, &base) {
		return base.Code, true
	}
	return "", false
}

// RefundApproval builds the plugin that holds refunds and cancellations above a
// threshold in a pending queue until an authorized reviewer approves them.
// Requesters cannot approve their own request, and the HTTP endpoints act only as
// the user GetActor resolves.
func RefundApproval(options RefundApprovalOptions) (core.Plugin, error) {
	if options.Threshold == nil || math.IsNaN(*options.Threshold) || math.IsInf(*options.Threshold, 0) || *options.Threshold < 0 {
		value := any("undefined")
		if options.Threshold != nil {
			value = *options.Threshold
		}
		return core.Plugin{}, core.NewConfigurationError(fmt.Sprintf(
			"refundApproval: threshold must be a non-negative number, got %v", value), "")
	}
	threshold := *options.Threshold

	store := options.Store
	if store == nil {
		store = NewMemoryRefundApprovalStore()
	}

	var mu sync.Mutex
	var pluginCtx core.PluginContext
	// inFlight holds the ids being approved or rejected in this process: two
	// approvals of one request both read it as pending, so without this both refund.
	inFlight := map[string]bool{}
	// approved marks the request copies this plugin replays after approval. The
	// marker is the copy's identity, so it can never arrive in a JSON body.
	approved := map[any]bool{}

	contextOf := func() core.PluginContext {
		mu.Lock()
		defer mu.Unlock()
		return pluginCtx
	}
	setContext := func(ctx core.PluginContext) {
		mu.Lock()
		pluginCtx = ctx
		mu.Unlock()
	}

	exclusive := func(id string, run func() (any, error)) (any, error) {
		mu.Lock()
		if inFlight[id] {
			mu.Unlock()
			return nil, core.NewError(fmt.Sprintf("Approval request '%s' is already being processed", id), string(core.CodeInvalidRequest))
		}
		inFlight[id] = true
		mu.Unlock()
		defer func() {
			mu.Lock()
			delete(inFlight, id)
			mu.Unlock()
		}()
		return run()
	}

	// Only a hashable request can carry the marker: the replayed copies are always
	// pointers, while an incoming request may be a map.
	markApproved := func(request any) func() {
		mu.Lock()
		approved[request] = true
		mu.Unlock()
		return func() {
			mu.Lock()
			delete(approved, request)
			mu.Unlock()
		}
	}
	isApproved := func(request any) bool {
		if request == nil || !reflect.TypeOf(request).Comparable() {
			return false
		}
		mu.Lock()
		defer mu.Unlock()
		return approved[request]
	}

	runApprove := func(id string, by any) (any, error) {
		item, err := store.Get(id)
		if err != nil {
			return nil, err
		}
		if item == nil {
			return nil, core.NewError(fmt.Sprintf("Approval request '%s' not found", id), string(core.CodeInvalidRequest))
		}
		if item.Status != RefundApprovalPending {
			return nil, core.NewError(fmt.Sprintf("Approval request '%s' is already %s", id, item.Status), string(core.CodeInvalidRequest))
		}

		requesterID, hasRequester := refundApprovalUserID(item.RequestedBy)
		approverID, hasApprover := refundApprovalUserID(by)
		if hasRequester && hasApprover && requesterID != "" && approverID != "" && requesterID == approverID {
			return nil, core.NewError("Requester cannot approve their own refund request", "REFUND_SELF_APPROVAL_FORBIDDEN")
		}

		if options.Approvers != nil {
			allowed, err := options.Approvers(by)
			if err != nil {
				return nil, err
			}
			if !allowed {
				return nil, core.NewError("User is not authorized to approve refunds", "REFUND_NOT_AUTHORIZED")
			}
		}

		// Saved before the provider call, so a second approval of the same request
		// finds it already approved instead of refunding twice.
		item.Status = RefundApprovalApproved
		item.ApprovedAt = time.Now().UnixMilli()
		item.ApprovedBy = by
		if err := store.Save(*item); err != nil {
			return nil, err
		}

		ctx := contextOf()
		if ctx == nil {
			return nil, core.NewConfigurationError("refundApproval: plugin is not initialized", "")
		}
		provider, err := ctx.Use(item.Provider)
		if err != nil {
			return nil, err
		}
		request, err := refundApprovalTypedRequest(item.Operation, item.Request)
		if err != nil {
			return nil, err
		}
		unmark := markApproved(request)
		defer unmark()
		if item.Operation == "cancel" {
			return provider.Cancel(context.Background(), request.(*core.CancelRequest))
		}
		return provider.Refund(context.Background(), request.(*core.RefundRequest))
	}

	runReject := func(id string, by any, reason string) (any, error) {
		item, err := store.Get(id)
		if err != nil {
			return nil, err
		}
		if item == nil {
			return nil, core.NewError(fmt.Sprintf("Approval request '%s' not found", id), string(core.CodeInvalidRequest))
		}
		if item.Status != RefundApprovalPending {
			return nil, core.NewError(fmt.Sprintf("Approval request '%s' is already %s", id, item.Status), string(core.CodeInvalidRequest))
		}

		if options.Approvers != nil {
			allowed, err := options.Approvers(by)
			if err != nil {
				return nil, err
			}
			if !allowed {
				return nil, core.NewError("User is not authorized to reject refunds", "REFUND_NOT_AUTHORIZED")
			}
		}

		item.Status = RefundApprovalRejected
		item.RejectedAt = time.Now().UnixMilli()
		item.RejectedBy = by
		item.RejectionReason = reason
		if err := store.Save(*item); err != nil {
			return nil, err
		}
		return item, nil
	}

	approve := func(id string, by any) (any, error) {
		return exclusive(id, func() (any, error) { return runApprove(id, by) })
	}
	reject := func(id string, by any, reason string) (any, error) {
		return exclusive(id, func() (any, error) { return runReject(id, by, reason) })
	}

	actorOf := func(request *core.HandlerRequest) (any, error) {
		if options.GetActor == nil {
			return nil, nil
		}
		return options.GetActor(request)
	}

	forbidden := func(ectx core.EndpointContext) *core.HandlerResponse {
		message := "refundApproval: set the 'getActor' option to use the HTTP endpoints"
		if options.GetActor != nil {
			message = "No signed-in approver for this request"
		}
		return ectx.JSON(map[string]any{"status": "error", "errorMessage": message}, 403)
	}

	// A refused approval is the caller's error, not the server's.
	respond := func(ectx core.EndpointContext, run func() (any, error)) (*core.HandlerResponse, error) {
		value, err := run()
		if err == nil {
			return ectx.JSON(value), nil
		}
		code, ok := refundApprovalBaseCode(err)
		if !ok {
			return nil, err
		}
		status := 403
		if code == string(core.CodeInvalidRequest) {
			status = 400
		}
		return ectx.JSON(map[string]any{"status": "error", "errorCode": code, "errorMessage": err.Error()}, status), nil
	}

	missingID := func(ectx core.EndpointContext) *core.HandlerResponse {
		return ectx.JSON(map[string]any{"status": "error", "errorMessage": "Missing required field: id"}, 400)
	}

	return core.DefinePlugin(core.Plugin{
		ID:         PluginIDRefundApproval,
		Options:    options,
		ErrorCodes: refundApprovalErrorMessages,
		Init: func(_ context.Context, ctx core.PluginContext) error {
			setContext(ctx)
			return nil
		},
		Hooks: core.PluginHooks{
			Before: []core.BeforeHook{{
				Matcher: func(ctx core.OperationContext) bool {
					return ctx.Operation == core.OperationRefund || ctx.Operation == core.OperationCancel
				},
				Handler: func(ctx core.OperationContext) (*core.BeforeHookResult, error) {
					if isApproved(ctx.Request) {
						return nil, nil
					}
					view := refundApprovalViewOf(ctx.Request)

					provider := ctx.Provider
					if provider == "" {
						provider = "default"
					}

					// A cancellation carries no amount: only the merchant's own records
					// know what it voids. An amount that cannot be read waits for approval.
					var rawAmount any
					if ctx.Operation == core.OperationCancel {
						if options.CancelAmount != nil {
							request, _ := ctx.Request.(*core.CancelRequest)
							value, err := options.CancelAmount(request, provider)
							if err != nil {
								return nil, err
							}
							rawAmount = value
						}
					} else if view.hasPrice {
						rawAmount = view.price
					} else {
						rawAmount = view.amount
					}
					amount, hasAmount := refundApprovalParseAmount(rawAmount)
					if hasAmount && amount <= threshold {
						return nil, nil
					}

					suffix, err := core.RandomHex(12)
					if err != nil {
						return nil, err
					}
					id := "apr_" + suffix

					record := RefundApprovalRequest{
						ID:          id,
						Operation:   string(ctx.Operation),
						Provider:    provider,
						PaymentID:   view.paymentID,
						Currency:    view.currency,
						RequestedBy: view.requestedBy,
						Status:      RefundApprovalPending,
						Request:     ctx.Request,
						CreatedAt:   time.Now().UnixMilli(),
					}
					if hasAmount {
						record.Amount = amount
					}
					if record.Currency == "" {
						record.Currency = "TRY"
					}

					if err := store.Save(record); err != nil {
						return nil, err
					}

					if options.Notify != nil {
						if err := options.Notify(record); err != nil {
							if ctx := contextOf(); ctx != nil {
								if logger := ctx.Logger(); logger != nil {
									logger.Error("refundApproval: notify callback failed", err, nil)
								}
							}
						}
					}

					return &core.BeforeHookResult{Result: map[string]any{
						"status":         string(core.StatusPending),
						"code":           "REFUND_APPROVAL_REQUIRED",
						"errorCode":      "REFUND_APPROVAL_REQUIRED",
						"errorMessage":   refundApprovalErrorMessages["REFUND_APPROVAL_REQUIRED"],
						"approvalId":     id,
						"paymentId":      view.paymentID,
						"conversationId": view.conversationID,
					}}, nil
				},
			}},
		},
		Methods: func(_ context.Context, ctx core.PluginContext) (map[string]any, error) {
			setContext(ctx)
			return map[string]any{
				"refunds": map[string]any{
					"approve": func(id string, by any) (any, error) { return approve(id, by) },
					"reject":  func(id string, by any, reason string) (any, error) { return reject(id, by, reason) },
					"list":    func(filter *RefundApprovalFilter) ([]RefundApprovalRequest, error) { return store.List(filter) },
					"get":     func(id string) (*RefundApprovalRequest, error) { return store.Get(id) },
					"store":   store,
				},
			}, nil
		},
		Endpoints: map[string]core.PaymentEndpoint{
			"pendingRefunds": {
				Method:     "GET",
				Path:       "/refunds/pending",
				Privileged: true,
				Handler: func(_ context.Context, ectx core.EndpointContext) (any, error) {
					setContext(ectx)
					return store.List(&RefundApprovalFilter{Status: RefundApprovalPending})
				},
			},
			"approveRefund": {
				Method:     "POST",
				Path:       "/refunds/approve",
				Privileged: true,
				Handler: func(_ context.Context, ectx core.EndpointContext) (any, error) {
					setContext(ectx)
					body := ectx.Body()
					id, _ := body["id"].(string)
					if id == "" {
						return missingID(ectx), nil
					}
					by, err := actorOf(ectx.Request())
					if err != nil {
						return nil, err
					}
					if by == nil {
						return forbidden(ectx), nil
					}
					return respond(ectx, func() (any, error) { return approve(id, by) })
				},
			},
			"rejectRefund": {
				Method:     "POST",
				Path:       "/refunds/reject",
				Privileged: true,
				Handler: func(_ context.Context, ectx core.EndpointContext) (any, error) {
					setContext(ectx)
					body := ectx.Body()
					id, _ := body["id"].(string)
					if id == "" {
						return missingID(ectx), nil
					}
					by, err := actorOf(ectx.Request())
					if err != nil {
						return nil, err
					}
					if by == nil {
						return forbidden(ectx), nil
					}
					reason, _ := body["reason"].(string)
					return respond(ectx, func() (any, error) { return reject(id, by, reason) })
				},
			},
		},
	}), nil
}
