package plugins

import (
	"context"
	"reflect"
	"strings"
	"sync"
	"testing"

	"github.com/boracomet/go-pos/core"
)

func refundApprovalTestThreshold(value float64) *float64 { return &value }

func refundApprovalTestPayment() *core.PaymentRequest {
	request := failoverTestRequest()
	request.Price = "5000.00"
	request.PaidPrice = "5000.00"
	return request
}

// refundApprovalTestRefund is a refund body with the caller-supplied extras the
// queue reads, which the typed request has no field for.
func refundApprovalTestRefund(paymentID, price string, extras map[string]any) map[string]any {
	body := map[string]any{
		"paymentId": paymentID,
		"price":     price,
		"currency":  "TRY",
		"ip":        "127.0.0.1",
	}
	for key, value := range extras {
		body[key] = value
	}
	return body
}

type refundApprovalTestMethods struct {
	approve func(id string, by any) (any, error)
	reject  func(id string, by any, reason string) (any, error)
	list    func(filter *RefundApprovalFilter) ([]RefundApprovalRequest, error)
	get     func(id string) (*RefundApprovalRequest, error)
}

func refundApprovalTestMethodsOf(t *testing.T, facade *failoverTestFacade) refundApprovalTestMethods {
	t.Helper()
	member, ok := facade.method(t, "refunds").(map[string]any)
	if !ok {
		t.Fatalf("refunds member has type %T", facade.method(t, "refunds"))
	}
	out := refundApprovalTestMethods{}
	if approve, ok := member["approve"].(func(string, any) (any, error)); ok {
		out.approve = approve
	} else {
		t.Fatalf("approve has type %T", member["approve"])
	}
	if reject, ok := member["reject"].(func(string, any, string) (any, error)); ok {
		out.reject = reject
	} else {
		t.Fatalf("reject has type %T", member["reject"])
	}
	if list, ok := member["list"].(func(*RefundApprovalFilter) ([]RefundApprovalRequest, error)); ok {
		out.list = list
	} else {
		t.Fatalf("list has type %T", member["list"])
	}
	if get, ok := member["get"].(func(string) (*RefundApprovalRequest, error)); ok {
		out.get = get
	} else {
		t.Fatalf("get has type %T", member["get"])
	}
	return out
}

func refundApprovalTestSetup(t *testing.T, options RefundApprovalOptions) (*failoverTestFacade, *failoverTestProvider) {
	t.Helper()
	plugin, err := RefundApproval(options)
	if err != nil {
		t.Fatalf("refundApproval: %v", err)
	}
	return failoverTestSingle(t, plugin, nil)
}

func refundApprovalTestPaid(t *testing.T, facade *failoverTestFacade) string {
	t.Helper()
	result := facade.createPayment(t, refundApprovalTestPayment())
	if status := failoverTestText(result, "status"); status != string(core.StatusSuccess) {
		t.Fatalf("payment status = %q, want success", status)
	}
	paymentID := failoverTestText(result, "paymentId")
	if paymentID == "" {
		t.Fatal("payment has no paymentId")
	}
	return paymentID
}

func refundApprovalTestWantCode(t *testing.T, record map[string]any, want string) {
	t.Helper()
	if got := failoverTestText(record, "code"); got != want {
		t.Errorf("code = %q, want %q", got, want)
	}
}

func refundApprovalTestWantError(t *testing.T, err error, contains string) {
	t.Helper()
	if err == nil {
		t.Fatalf("expected an error containing %q", contains)
	}
	if !strings.Contains(err.Error(), contains) {
		t.Errorf("error %q does not contain %q", err.Error(), contains)
	}
}

func refundApprovalTestRole(user any) string {
	record := failoverTestJSONMap(user)
	role, _ := record["role"].(string)
	return role
}

func refundApprovalTestFinance(user any) (bool, error) {
	return refundApprovalTestRole(user) == "finance", nil
}

func TestRefundApproval(t *testing.T) {
	t.Run("executes refunds below threshold directly without approval", func(t *testing.T) {
		facade, _ := refundApprovalTestSetup(t, RefundApprovalOptions{Threshold: refundApprovalTestThreshold(1000)})
		paymentID := refundApprovalTestPaid(t, facade)

		result := facade.call(t, core.OperationRefund, refundApprovalTestRefund(paymentID, "500.00", nil))
		if status := failoverTestText(result, "status"); status != string(core.StatusSuccess) {
			t.Errorf("refund status = %q, want success", status)
		}
		methods := refundApprovalTestMethodsOf(t, facade)
		pending, err := methods.list(nil)
		if err != nil {
			t.Fatalf("list: %v", err)
		}
		if len(pending) != 0 {
			t.Errorf("pending refunds = %d, want 0", len(pending))
		}
	})

	t.Run("holds refunds above threshold and stores the approval request", func(t *testing.T) {
		var notified []RefundApprovalRequest
		facade, _ := refundApprovalTestSetup(t, RefundApprovalOptions{
			Threshold: refundApprovalTestThreshold(1000),
			Notify: func(request RefundApprovalRequest) error {
				notified = append(notified, request)
				return nil
			},
		})
		paymentID := refundApprovalTestPaid(t, facade)

		result := facade.call(t, core.OperationRefund,
			refundApprovalTestRefund(paymentID, "2500.00", map[string]any{"requestedBy": "agent-alice"}))
		if status := failoverTestText(result, "status"); status != string(core.StatusPending) {
			t.Errorf("refund status = %q, want pending", status)
		}
		refundApprovalTestWantCode(t, result, "REFUND_APPROVAL_REQUIRED")
		approvalID := failoverTestText(result, "approvalId")
		if approvalID == "" {
			t.Fatal("no approvalId")
		}
		if len(notified) != 1 {
			t.Fatalf("notify calls = %d, want 1", len(notified))
		}
		if notified[0].Amount != 2500 {
			t.Errorf("notified amount = %v, want 2500", notified[0].Amount)
		}

		methods := refundApprovalTestMethodsOf(t, facade)
		pending, err := methods.list(&RefundApprovalFilter{Status: RefundApprovalPending})
		if err != nil {
			t.Fatalf("list: %v", err)
		}
		if len(pending) != 1 {
			t.Fatalf("pending refunds = %d, want 1", len(pending))
		}
		if pending[0].ID != approvalID {
			t.Errorf("pending id = %q, want %q", pending[0].ID, approvalID)
		}
		if pending[0].RequestedBy != "agent-alice" {
			t.Errorf("requestedBy = %v, want agent-alice", pending[0].RequestedBy)
		}
	})

	t.Run("approves and executes the refund on the provider when authorized", func(t *testing.T) {
		facade, provider := refundApprovalTestSetup(t, RefundApprovalOptions{
			Threshold: refundApprovalTestThreshold(1000),
			Approvers: refundApprovalTestFinance,
		})
		paymentID := refundApprovalTestPaid(t, facade)
		result := facade.call(t, core.OperationRefund,
			refundApprovalTestRefund(paymentID, "1500.00", map[string]any{"requestedBy": "agent-1"}))
		approvalID := failoverTestText(result, "approvalId")

		methods := refundApprovalTestMethodsOf(t, facade)
		approver := map[string]any{"id": "mgr-finance", "role": "finance"}
		approved, err := methods.approve(approvalID, approver)
		if err != nil {
			t.Fatalf("approve: %v", err)
		}
		if status := failoverTestText(failoverTestRecord(t, approved), "status"); status != string(core.StatusSuccess) {
			t.Errorf("approved status = %q, want success", status)
		}
		if calls := provider.count(core.OperationRefund); calls != 1 {
			t.Errorf("provider refunds = %d, want 1", calls)
		}
		item, err := methods.get(approvalID)
		if err != nil || item == nil {
			t.Fatalf("get: %v (%v)", item, err)
		}
		if item.Status != RefundApprovalApproved {
			t.Errorf("status = %q, want approved", item.Status)
		}
		if !reflect.DeepEqual(item.ApprovedBy, approver) {
			t.Errorf("approvedBy = %v, want %v", item.ApprovedBy, approver)
		}
	})

	t.Run("drops the refund when rejected", func(t *testing.T) {
		facade, provider := refundApprovalTestSetup(t, RefundApprovalOptions{Threshold: refundApprovalTestThreshold(500)})
		paymentID := refundApprovalTestPaid(t, facade)
		result := facade.call(t, core.OperationRefund, refundApprovalTestRefund(paymentID, "800.00", nil))
		approvalID := failoverTestText(result, "approvalId")

		methods := refundApprovalTestMethodsOf(t, facade)
		rejected, err := methods.reject(approvalID, "supervisor", "Customer did not return item")
		if err != nil {
			t.Fatalf("reject: %v", err)
		}
		item, ok := rejected.(*RefundApprovalRequest)
		if !ok {
			t.Fatalf("reject returned %T", rejected)
		}
		if item.Status != RefundApprovalRejected {
			t.Errorf("status = %q, want rejected", item.Status)
		}
		if item.RejectionReason != "Customer did not return item" {
			t.Errorf("rejectionReason = %q", item.RejectionReason)
		}
		if calls := provider.count(core.OperationRefund); calls != 0 {
			t.Errorf("provider refunds = %d, want 0", calls)
		}
	})

	t.Run("refuses self-approval by the requester", func(t *testing.T) {
		facade, _ := refundApprovalTestSetup(t, RefundApprovalOptions{Threshold: refundApprovalTestThreshold(1000)})
		paymentID := refundApprovalTestPaid(t, facade)
		result := facade.call(t, core.OperationRefund,
			refundApprovalTestRefund(paymentID, "2000.00", map[string]any{"requestedBy": "agent-bob"}))
		approvalID := failoverTestText(result, "approvalId")

		methods := refundApprovalTestMethodsOf(t, facade)
		_, err := methods.approve(approvalID, "agent-bob")
		refundApprovalTestWantError(t, err, "Requester cannot approve their own refund request")
	})

	t.Run("refuses approval by unauthorized users", func(t *testing.T) {
		facade, _ := refundApprovalTestSetup(t, RefundApprovalOptions{
			Threshold: refundApprovalTestThreshold(1000),
			Approvers: refundApprovalTestFinance,
		})
		paymentID := refundApprovalTestPaid(t, facade)
		result := facade.call(t, core.OperationRefund,
			refundApprovalTestRefund(paymentID, "3000.00", map[string]any{"requestedBy": "agent-charlie"}))
		approvalID := failoverTestText(result, "approvalId")

		methods := refundApprovalTestMethodsOf(t, facade)
		_, err := methods.approve(approvalID, map[string]any{"id": "support-1", "role": "support"})
		refundApprovalTestWantError(t, err, "User is not authorized to approve refunds")
	})

	t.Run("does not let a request body mark itself approved", func(t *testing.T) {
		facade, provider := refundApprovalTestSetup(t, RefundApprovalOptions{Threshold: refundApprovalTestThreshold(1000)})
		paymentID := refundApprovalTestPaid(t, facade)
		result := facade.call(t, core.OperationRefund,
			refundApprovalTestRefund(paymentID, "2000.00", map[string]any{"__approved": true}))
		refundApprovalTestWantCode(t, result, "REFUND_APPROVAL_REQUIRED")
		if calls := provider.count(core.OperationRefund); calls != 0 {
			t.Errorf("provider refunds = %d, want 0", calls)
		}
	})

	t.Run("holds amounts it cannot read strictly", func(t *testing.T) {
		facade, _ := refundApprovalTestSetup(t, RefundApprovalOptions{Threshold: refundApprovalTestThreshold(1000)})
		paymentID := refundApprovalTestPaid(t, facade)
		result := facade.call(t, core.OperationRefund, refundApprovalTestRefund(paymentID, "1,500.00", nil))
		refundApprovalTestWantCode(t, result, "REFUND_APPROVAL_REQUIRED")
	})

	t.Run("holds cancellations unless cancelAmount puts them under the threshold", func(t *testing.T) {
		amounts := map[string]float64{}
		facade, provider := refundApprovalTestSetup(t, RefundApprovalOptions{
			Threshold: refundApprovalTestThreshold(1000),
			CancelAmount: func(request *core.CancelRequest, _ string) (any, error) {
				if value, ok := amounts[request.PaymentID]; ok {
					return value, nil
				}
				return nil, nil
			},
		})
		small := refundApprovalTestPaid(t, facade)
		amounts[small] = 200
		direct := facade.call(t, core.OperationCancel, map[string]any{"paymentId": small, "ip": "127.0.0.1"})
		if status := failoverTestText(direct, "status"); status != string(core.StatusSuccess) {
			t.Errorf("cancel status = %q, want success", status)
		}
		if calls := provider.count(core.OperationCancel); calls != 1 {
			t.Errorf("provider cancels = %d, want 1", calls)
		}

		unknown := refundApprovalTestPaid(t, facade)
		held := facade.call(t, core.OperationCancel, map[string]any{"paymentId": unknown, "ip": "127.0.0.1"})
		refundApprovalTestWantCode(t, held, "REFUND_APPROVAL_REQUIRED")
		if calls := provider.count(core.OperationCancel); calls != 1 {
			t.Errorf("provider cancels = %d, want 1", calls)
		}
	})

	t.Run("holds every cancellation without cancelAmount", func(t *testing.T) {
		facade, _ := refundApprovalTestSetup(t, RefundApprovalOptions{Threshold: refundApprovalTestThreshold(1000)})
		paymentID := refundApprovalTestPaid(t, facade)
		held := facade.call(t, core.OperationCancel, map[string]any{
			"paymentId": paymentID, "ip": "127.0.0.1", "amount": 1,
		})
		refundApprovalTestWantCode(t, held, "REFUND_APPROVAL_REQUIRED")
	})

	t.Run("refunds once when the same request is approved twice", func(t *testing.T) {
		facade, provider := refundApprovalTestSetup(t, RefundApprovalOptions{Threshold: refundApprovalTestThreshold(1000)})
		paymentID := refundApprovalTestPaid(t, facade)
		result := facade.call(t, core.OperationRefund, refundApprovalTestRefund(paymentID, "2000.00", nil))
		approvalID := failoverTestText(result, "approvalId")

		methods := refundApprovalTestMethodsOf(t, facade)
		outcomes := make([]error, 2)
		var wg sync.WaitGroup
		for index, by := range []string{"mgr-1", "mgr-2"} {
			wg.Add(1)
			go func(index int, by string) {
				defer wg.Done()
				_, outcomes[index] = methods.approve(approvalID, by)
			}(index, by)
		}
		wg.Wait()
		fulfilled := 0
		for _, err := range outcomes {
			if err == nil {
				fulfilled++
			}
		}
		if fulfilled != 1 {
			t.Errorf("successful approvals = %d, want 1 (%v)", fulfilled, outcomes)
		}
		if calls := provider.count(core.OperationRefund); calls != 1 {
			t.Errorf("provider refunds = %d, want 1", calls)
		}
	})
}

func refundApprovalTestUsers() map[string]any {
	return map[string]any{
		"alice": map[string]any{"id": "agent-alice", "role": "finance"},
		"carol": map[string]any{"id": "mgr-carol", "role": "finance"},
		"dave":  map[string]any{"id": "support-dave", "role": "support"},
	}
}

func refundApprovalTestActor(request *core.HandlerRequest) (any, error) {
	name := ""
	for key, value := range request.Headers {
		if strings.EqualFold(key, "x-user") {
			name = value
			break
		}
	}
	return refundApprovalTestUsers()[name], nil
}

func refundApprovalTestHTTP(t *testing.T, getActor func(*core.HandlerRequest) (any, error)) (*failoverTestFacade, *failoverTestProvider) {
	t.Helper()
	plugin, err := RefundApproval(RefundApprovalOptions{
		Threshold: refundApprovalTestThreshold(1000),
		Approvers: refundApprovalTestFinance,
		GetActor:  getActor,
	})
	if err != nil {
		t.Fatalf("refundApproval: %v", err)
	}
	provider := failoverTestNewProvider("mock")
	facade := failoverTestNewFacade(t, failoverTestSetup{
		Providers: []*failoverTestProvider{provider},
		Plugins:   []core.Plugin{plugin},
		BasePath:  "/api/pay",
		Authorize: func(context.Context, core.HandlerContext) (bool, error) { return true, nil },
	})
	return facade, provider
}

// refundApprovalTestQueuePayment pays 5000 and queues a 2000 refund requested by
// agent-alice, returning the approval id.
func refundApprovalTestQueuePayment(t *testing.T, facade *failoverTestFacade) string {
	t.Helper()
	paymentID := refundApprovalTestPaid(t, facade)
	result := facade.call(t, core.OperationRefund,
		refundApprovalTestRefund(paymentID, "2000.00", map[string]any{"requestedBy": "agent-alice"}))
	approvalID := failoverTestText(result, "approvalId")
	if approvalID == "" {
		t.Fatal("no approvalId")
	}
	return approvalID
}

func refundApprovalTestPost(t *testing.T, facade *failoverTestFacade, path string, body any, user string) *core.HandlerResponse {
	t.Helper()
	headers := map[string]string{"content-type": "application/json"}
	if user != "" {
		headers["x-user"] = user
	}
	return facade.serve(t, "POST", path, body, headers)
}

func refundApprovalTestWantHTTPStatus(t *testing.T, response *core.HandlerResponse, want int) {
	t.Helper()
	if response.Status != want {
		t.Errorf("HTTP status = %d, want %d (body %v)", response.Status, want, response.Body)
	}
}

func refundApprovalTestWantRefunds(t *testing.T, provider *failoverTestProvider, want int) {
	t.Helper()
	if calls := provider.count(core.OperationRefund); calls != want {
		t.Errorf("provider refunds = %d, want %d", calls, want)
	}
}

func TestRefundApprovalHTTPEndpoints(t *testing.T) {
	t.Run("refuses to approve without a getActor option", func(t *testing.T) {
		facade, provider := refundApprovalTestHTTP(t, nil)
		id := refundApprovalTestQueuePayment(t, facade)

		response := refundApprovalTestPost(t, facade, "/refunds/approve", map[string]any{
			"id": id,
			"by": map[string]any{"id": "mgr-carol", "role": "finance"},
		}, "")
		refundApprovalTestWantHTTPStatus(t, response, 403)
		refundApprovalTestWantRefunds(t, provider, 0)
	})

	t.Run("ignores an approver named in the body", func(t *testing.T) {
		facade, provider := refundApprovalTestHTTP(t, refundApprovalTestActor)
		id := refundApprovalTestQueuePayment(t, facade)

		response := refundApprovalTestPost(t, facade, "/refunds/approve", map[string]any{
			"id": id,
			"by": map[string]any{"id": "mgr-carol", "role": "finance"},
		}, "dave")
		refundApprovalTestWantHTTPStatus(t, response, 403)
		refundApprovalTestWantRefunds(t, provider, 0)

		item, err := refundApprovalTestMethodsOf(t, facade).get(id)
		if err != nil || item == nil {
			t.Fatalf("get: %v (%v)", item, err)
		}
		if item.Status != RefundApprovalPending {
			t.Errorf("status = %q, want pending", item.Status)
		}
	})

	t.Run("refuses self-approval by the signed-in requester", func(t *testing.T) {
		facade, provider := refundApprovalTestHTTP(t, refundApprovalTestActor)
		id := refundApprovalTestQueuePayment(t, facade)

		response := refundApprovalTestPost(t, facade, "/refunds/approve",
			map[string]any{"id": id, "by": "someone-else"}, "alice")
		refundApprovalTestWantHTTPStatus(t, response, 403)
		refundApprovalTestWantRefunds(t, provider, 0)

		item, err := refundApprovalTestMethodsOf(t, facade).get(id)
		if err != nil || item == nil {
			t.Fatalf("get: %v (%v)", item, err)
		}
		if item.Status != RefundApprovalPending {
			t.Errorf("status = %q, want pending", item.Status)
		}
	})

	t.Run("approves as the signed-in approver", func(t *testing.T) {
		facade, provider := refundApprovalTestHTTP(t, refundApprovalTestActor)
		id := refundApprovalTestQueuePayment(t, facade)

		response := refundApprovalTestPost(t, facade, "/refunds/approve", map[string]any{"id": id}, "carol")
		refundApprovalTestWantHTTPStatus(t, response, 200)
		refundApprovalTestWantRefunds(t, provider, 1)

		item, err := refundApprovalTestMethodsOf(t, facade).get(id)
		if err != nil || item == nil {
			t.Fatalf("get: %v (%v)", item, err)
		}
		if !reflect.DeepEqual(item.ApprovedBy, refundApprovalTestUsers()["carol"]) {
			t.Errorf("approvedBy = %v, want %v", item.ApprovedBy, refundApprovalTestUsers()["carol"])
		}
	})
}
