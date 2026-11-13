package aistudio

import (
	"net/http"
	"testing"
)

// TestTransientPermissionGate 区分模型级瞬时权限门、账户权限拒绝与真正的账户限制。
func TestTransientPermissionGate(t *testing.T) {
	cases := []struct {
		name      string
		message   string
		status    int
		code      int64
		gate      bool
		rejection bool
	}{
		{
			name:    "模型级瞬时权限门",
			message: "The caller does not have permission",
			status:  http.StatusForbidden,
			code:    7,
			gate:    true,
		},
		{
			name:      "账户权限拒绝",
			message:   "Permission denied",
			status:    http.StatusForbidden,
			code:      7,
			rejection: true,
		},
		{
			name:    "账户年龄限制",
			message: "Account not supported.",
			status:  http.StatusForbidden,
			code:    7,
		},
		{
			name:    "普通上游错误",
			message: "internal error",
			status:  http.StatusInternalServerError,
			code:    13,
		},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			err := &RPCError{Method: "GenerateContent", StatusCode: testCase.status, Code: testCase.code, Message: testCase.message}
			if got := TransientPermissionGate(err); got != testCase.gate {
				t.Fatalf("TransientPermissionGate = %v, want %v", got, testCase.gate)
			}
			if _, got := AccountRejectionCooldown(err); got != testCase.rejection {
				t.Fatalf("AccountRejectionCooldown = %v, want %v", got, testCase.rejection)
			}
		})
	}
}
