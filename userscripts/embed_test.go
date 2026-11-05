package userscripts

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// TestHandlerServesUserScript 验证入口返回可直接安装的脚本内容
func TestHandlerServesUserScript(t *testing.T) {
	recorder := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodGet, "/userscript.user.js", nil)
	Handler().ServeHTTP(recorder, request)
	if recorder.Code != http.StatusOK {
		t.Fatalf("状态 = %d", recorder.Code)
	}
	if contentType := recorder.Header().Get("Content-Type"); !strings.HasPrefix(contentType, "text/javascript") {
		t.Fatalf("Content-Type = %q", contentType)
	}
	if !strings.Contains(recorder.Body.String(), "==UserScript==") {
		t.Fatal("响应不是用户脚本")
	}
}

// TestHandlerRejectsOtherMethods 验证只接受 GET 与 HEAD
func TestHandlerRejectsOtherMethods(t *testing.T) {
	recorder := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodPost, "/userscript.user.js", nil)
	Handler().ServeHTTP(recorder, request)
	if recorder.Code != http.StatusMethodNotAllowed {
		t.Fatalf("状态 = %d", recorder.Code)
	}
}
