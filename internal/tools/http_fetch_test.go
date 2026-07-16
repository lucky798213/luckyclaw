package tools

import (
	"context"
	"io"
	"net"
	"net/http"
	"strings"
	"testing"
)

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(request *http.Request) (*http.Response, error) {
	return f(request)
}

func TestHTTPFetchReturnsReadableHTMLAndSetsHeaders(t *testing.T) {
	client := &http.Client{Transport: roundTripFunc(func(request *http.Request) (*http.Response, error) {
		if !strings.Contains(request.Header.Get("User-Agent"), "LuckyClaw") {
			t.Errorf("User-Agent = %q", request.Header.Get("User-Agent"))
		}
		return response(http.StatusOK, "text/html; charset=utf-8", `<html><head><title>标题</title><style>hidden</style></head><body><h1>Hello</h1><p>Lucky <b>Claw</b></p><script>secret()</script></body></html>`), nil
	})}
	tool := newHTTPFetchTool(client)
	result, err := tool.Execute(context.Background(), []byte(`{"url":"https://example.com/page"}`))
	if err != nil {
		t.Fatal(err)
	}
	if result != "标题 Hello Lucky Claw" {
		t.Fatalf("result = %q", result)
	}
}

func TestHTTPFetchValidatesURLsBeforeRequest(t *testing.T) {
	called := false
	client := &http.Client{Transport: roundTripFunc(func(request *http.Request) (*http.Response, error) {
		called = true
		return response(http.StatusOK, "text/plain", "unexpected"), nil
	})}
	tool := newHTTPFetchTool(client)
	for _, rawURL := range []string{
		"",
		"file:///etc/passwd",
		"ftp://example.com/file",
		"https://user:password@example.com",
		"http://localhost/admin",
		"http://service.localhost/admin",
		"http://127.0.0.1/admin",
		"http://[::1]/admin",
		"http://10.0.0.1/admin",
		"http://169.254.169.254/latest/meta-data",
		"http://100.64.0.1/internal",
	} {
		t.Run(rawURL, func(t *testing.T) {
			if _, err := tool.Execute(context.Background(), []byte(`{"url":`+quoteJSON(rawURL)+`}`)); err == nil {
				t.Fatalf("Execute(%q) error = nil", rawURL)
			}
		})
	}
	if called {
		t.Fatal("invalid URL reached HTTP transport")
	}
}

func TestHTTPFetchRejectsStatusBinaryAndOversizedBodies(t *testing.T) {
	tests := []struct {
		name        string
		status      int
		contentType string
		body        string
	}{
		{name: "错误状态", status: http.StatusNotFound, contentType: "text/plain", body: "missing"},
		{name: "二进制", status: http.StatusOK, contentType: "application/octet-stream", body: "binary"},
		{name: "正文过大", status: http.StatusOK, contentType: "text/plain", body: strings.Repeat("a", maximumHTTPFetchBodySize+1)},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			tool := newHTTPFetchTool(&http.Client{Transport: roundTripFunc(func(request *http.Request) (*http.Response, error) {
				return response(test.status, test.contentType, test.body), nil
			})})
			if _, err := tool.Execute(context.Background(), []byte(`{"url":"https://example.com"}`)); err == nil {
				t.Fatal("Execute() error = nil")
			}
		})
	}
}

func TestHTTPFetchTruncatesByUnicodeCharacters(t *testing.T) {
	tool := newHTTPFetchTool(&http.Client{Transport: roundTripFunc(func(request *http.Request) (*http.Response, error) {
		return response(http.StatusOK, "text/plain; charset=utf-8", "你好世界 LuckyClaw"), nil
	})})
	result, err := tool.Execute(context.Background(), []byte(`{"url":"https://example.com","max_chars":4}`))
	if err != nil {
		t.Fatal(err)
	}
	if result != "你好世界\n[...truncated]" {
		t.Fatalf("result = %q", result)
	}
	for _, maxChars := range []int{-1, maximumHTTPFetchMaxChars + 1} {
		if _, err := tool.Execute(context.Background(), []byte(`{"url":"https://example.com","max_chars":`+integerString(maxChars)+`}`)); err == nil {
			t.Fatalf("max_chars %d error = nil", maxChars)
		}
	}
}

func TestSafeHTTPClientLimitsAndValidatesRedirects(t *testing.T) {
	client := newSafeHTTPClient()
	client.Transport = roundTripFunc(func(request *http.Request) (*http.Response, error) {
		path := strings.TrimPrefix(request.URL.Path, "/")
		if path == "private" {
			return response(http.StatusFound, "text/plain", ""), nil
		}
		next := "/1"
		if path != "" {
			next = "/" + integerString(parseSmallInt(path)+1)
		}
		current := response(http.StatusFound, "text/plain", "")
		current.Header.Set("Location", next)
		return current, nil
	})
	tool := newHTTPFetchTool(client)
	if _, err := tool.Execute(context.Background(), []byte(`{"url":"https://example.com/0"}`)); err == nil || !strings.Contains(err.Error(), "too many redirects") {
		t.Fatalf("redirect error = %v", err)
	}

	privateClient := newSafeHTTPClient()
	privateClient.Transport = roundTripFunc(func(request *http.Request) (*http.Response, error) {
		current := response(http.StatusFound, "text/plain", "")
		current.Header.Set("Location", "http://127.0.0.1/private")
		return current, nil
	})
	privateTool := newHTTPFetchTool(privateClient)
	if _, err := privateTool.Execute(context.Background(), []byte(`{"url":"https://example.com"}`)); err == nil || !strings.Contains(err.Error(), "not allowed") {
		t.Fatalf("private redirect error = %v", err)
	}
}

func TestBlockedAddressClassification(t *testing.T) {
	tests := []struct {
		address string
		blocked bool
	}{
		{address: "8.8.8.8", blocked: false},
		{address: "2606:4700:4700::1111", blocked: false},
		{address: "127.0.0.1", blocked: true},
		{address: "10.0.0.1", blocked: true},
		{address: "169.254.169.254", blocked: true},
		{address: "100.127.255.254", blocked: true},
		{address: "::1", blocked: true},
		{address: "fc00::1", blocked: true},
	}
	for _, test := range tests {
		if got := isBlockedAddress(net.ParseIP(test.address)); got != test.blocked {
			t.Errorf("isBlockedAddress(%s) = %v, want %v", test.address, got, test.blocked)
		}
	}
}

func response(status int, contentType, body string) *http.Response {
	header := make(http.Header)
	if contentType != "" {
		header.Set("Content-Type", contentType)
	}
	return &http.Response{
		StatusCode: status,
		Status:     http.StatusText(status),
		Header:     header,
		Body:       io.NopCloser(strings.NewReader(body)),
	}
}

func quoteJSON(value string) string {
	value = strings.ReplaceAll(value, `\`, `\\`)
	value = strings.ReplaceAll(value, `"`, `\"`)
	return `"` + value + `"`
}

func integerString(value int) string {
	if value == 0 {
		return "0"
	}
	negative := value < 0
	if negative {
		value = -value
	}
	var digits [32]byte
	index := len(digits)
	for value > 0 {
		index--
		digits[index] = byte('0' + value%10)
		value /= 10
	}
	if negative {
		index--
		digits[index] = '-'
	}
	return string(digits[index:])
}

func parseSmallInt(value string) int {
	result := 0
	for _, digit := range value {
		if digit < '0' || digit > '9' {
			return result
		}
		result = result*10 + int(digit-'0')
	}
	return result
}
