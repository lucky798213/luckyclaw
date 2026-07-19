package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"mime"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"
	"unicode/utf8"

	"golang.org/x/net/html"

	"github.com/lucky798213/luckyclaw/internal/provider"
)

const (
	defaultHTTPFetchMaxChars = 10_000
	maximumHTTPFetchMaxChars = 20_000
	maximumHTTPFetchBodySize = 1 << 20
	maximumHTTPRedirects     = 5
)

type httpFetchTool struct {
	client *http.Client
}

type httpFetchArguments struct {
	URL      string `json:"url"`
	MaxChars int    `json:"max_chars,omitempty"`
}

func newHTTPFetchTool(client *http.Client) Tool {
	if client == nil {
		client = newSafeHTTPClient()
	}
	return &httpFetchTool{client: client}
}

func (t *httpFetchTool) Definition() provider.Tool {
	return functionDefinition(
		"http_fetch",
		"Fetch a known public HTTP or HTTPS URL with SSRF protection and return readable text.",
		map[string]any{
			"type": "object",
			"properties": map[string]any{
				"url": map[string]any{
					"type":        "string",
					"description": "Exact public HTTP or HTTPS URL to fetch.",
				},
				"max_chars": map[string]any{
					"type":        "integer",
					"minimum":     1,
					"maximum":     maximumHTTPFetchMaxChars,
					"description": "Maximum returned characters, default 10000 and maximum 20000.",
				},
			},
			"required":             []string{"url"},
			"additionalProperties": false,
		},
	)
}

func (t *httpFetchTool) Execute(ctx context.Context, raw json.RawMessage) (string, error) {
	var arguments httpFetchArguments
	if err := decodeArguments(raw, &arguments); err != nil {
		return "", err
	}
	parsedURL, err := validateFetchURL(arguments.URL)
	if err != nil {
		return "", err
	}
	maxChars := arguments.MaxChars
	if maxChars == 0 {
		maxChars = defaultHTTPFetchMaxChars
	}
	if maxChars < 1 || maxChars > maximumHTTPFetchMaxChars {
		return "", fmt.Errorf("max_chars must be between 1 and %d", maximumHTTPFetchMaxChars)
	}

	request, err := http.NewRequestWithContext(ctx, http.MethodGet, parsedURL.String(), nil)
	if err != nil {
		return "", fmt.Errorf("create request: %w", err)
	}
	request.Header.Set("User-Agent", "LuckyClaw/1.0 (Safe HTTP Fetch)")
	request.Header.Set("Accept", "text/html,text/plain,application/json,application/xml;q=0.9,*/*;q=0.1")
	response, err := t.client.Do(request)
	if err != nil {
		return "", fmt.Errorf("fetch URL: %w", err)
	}
	defer response.Body.Close()
	if response.StatusCode < http.StatusOK || response.StatusCode >= http.StatusMultipleChoices {
		return "", fmt.Errorf("HTTP %d: %s", response.StatusCode, response.Status)
	}

	body, err := io.ReadAll(io.LimitReader(response.Body, maximumHTTPFetchBodySize+1))
	if err != nil {
		return "", fmt.Errorf("read response: %w", err)
	}
	if len(body) > maximumHTTPFetchBodySize {
		return "", fmt.Errorf("response body exceeds %d bytes", maximumHTTPFetchBodySize)
	}
	mediaType, err := readableMediaType(response.Header.Get("Content-Type"), body)
	if err != nil {
		return "", err
	}
	text := strings.ToValidUTF8(string(body), "�")
	if mediaType == "text/html" || mediaType == "application/xhtml+xml" {
		text = htmlToText(text)
	} else {
		text = strings.TrimSpace(text)
	}
	return truncateCharacters(text, maxChars), nil
}

func newSafeHTTPClient() *http.Client {
	transport := &http.Transport{
		DialContext:           safeDialContext,
		ForceAttemptHTTP2:     true,
		TLSHandshakeTimeout:   10 * time.Second,
		ResponseHeaderTimeout: 20 * time.Second,
		IdleConnTimeout:       60 * time.Second,
	}
	return &http.Client{
		Timeout:   30 * time.Second,
		Transport: transport,
		CheckRedirect: func(request *http.Request, previous []*http.Request) error {
			if len(previous) >= maximumHTTPRedirects {
				return fmt.Errorf("too many redirects")
			}
			_, err := validateFetchURL(request.URL.String())
			return err
		},
	}
}

func validateFetchURL(raw string) (*url.URL, error) {
	parsed, err := url.Parse(strings.TrimSpace(raw))
	if err != nil {
		return nil, fmt.Errorf("parse URL: %w", err)
	}
	parsed.Scheme = strings.ToLower(parsed.Scheme)
	if parsed.Scheme != "http" && parsed.Scheme != "https" {
		return nil, fmt.Errorf("scheme %q is not allowed; use http or https", parsed.Scheme)
	}
	if parsed.Hostname() == "" {
		return nil, fmt.Errorf("URL host is required")
	}
	if parsed.User != nil {
		return nil, fmt.Errorf("URL credentials are not allowed")
	}
	hostname := strings.ToLower(strings.TrimSuffix(parsed.Hostname(), "."))
	if hostname == "localhost" || strings.HasSuffix(hostname, ".localhost") {
		return nil, fmt.Errorf("localhost addresses are not allowed")
	}
	if literal := net.ParseIP(hostname); literal != nil && isBlockedAddress(literal) {
		return nil, fmt.Errorf("address %s is not allowed", literal)
	}
	return parsed, nil
}

func safeDialContext(ctx context.Context, network, address string) (net.Conn, error) {
	// 阶段一：在真正拨号前自行解析 DNS，不能只检查用户最初提交的域名字符串。
	host, port, err := net.SplitHostPort(address)
	if err != nil {
		return nil, err
	}
	addresses, err := net.DefaultResolver.LookupIPAddr(ctx, host)
	if err != nil {
		return nil, err
	}
	if len(addresses) == 0 {
		return nil, fmt.Errorf("no addresses found for %s", host)
	}
	// 阶段二：只要任一解析结果落入私网、本机或特殊网段，就整体拒绝，防止 DNS 重绑定绕过。
	for _, candidate := range addresses {
		if isBlockedAddress(candidate.IP) {
			return nil, fmt.Errorf("address %s for host %s is not allowed", candidate.IP, host)
		}
	}
	// 阶段三：仅使用已经校验过的 IP 地址拨号，避免连接阶段再次解析到不同目标。
	dialer := &net.Dialer{Timeout: 10 * time.Second, KeepAlive: 30 * time.Second}
	var lastErr error
	for _, candidate := range addresses {
		connection, dialErr := dialer.DialContext(ctx, network, net.JoinHostPort(candidate.IP.String(), port))
		if dialErr == nil {
			return connection, nil
		}
		lastErr = dialErr
	}
	return nil, lastErr
}

func isBlockedAddress(ip net.IP) bool {
	if ip == nil || !ip.IsGlobalUnicast() || ip.IsLoopback() || ip.IsPrivate() ||
		ip.IsLinkLocalUnicast() || ip.IsLinkLocalMulticast() || ip.IsInterfaceLocalMulticast() ||
		ip.IsMulticast() || ip.IsUnspecified() {
		return true
	}
	if ipv4 := ip.To4(); ipv4 != nil {
		// 100.64.0.0/10 是运营商级 NAT 网段，不能作为公网抓取目标。
		if ipv4[0] == 100 && ipv4[1]&0xc0 == 0x40 {
			return true
		}
		// 云厂商元数据地址位于链路本地网段，这里额外显式保护。
		if ipv4[0] == 169 && ipv4[1] == 254 {
			return true
		}
	}
	return false
}

func readableMediaType(contentType string, body []byte) (string, error) {
	if strings.TrimSpace(contentType) == "" {
		contentType = http.DetectContentType(body)
	}
	mediaType, _, err := mime.ParseMediaType(contentType)
	if err != nil {
		return "", fmt.Errorf("invalid Content-Type %q: %w", contentType, err)
	}
	mediaType = strings.ToLower(mediaType)
	if strings.HasPrefix(mediaType, "text/") {
		return mediaType, nil
	}
	switch mediaType {
	case "application/json", "application/xml", "application/xhtml+xml", "application/javascript":
		return mediaType, nil
	default:
		return "", fmt.Errorf("Content-Type %q is not readable text", mediaType)
	}
}

func htmlToText(input string) string {
	tokenizer := html.NewTokenizer(strings.NewReader(input))
	var output strings.Builder
	skipDepth := 0
	for {
		tokenType := tokenizer.Next()
		switch tokenType {
		case html.ErrorToken:
			return strings.Join(strings.Fields(output.String()), " ")
		case html.StartTagToken:
			token := tokenizer.Token()
			tag := strings.ToLower(token.Data)
			if skipDepth > 0 || tag == "script" || tag == "style" || tag == "noscript" || tag == "svg" {
				skipDepth++
				continue
			}
			if isHTMLSeparator(tag) {
				output.WriteByte(' ')
			}
		case html.EndTagToken:
			token := tokenizer.Token()
			if skipDepth > 0 {
				skipDepth--
				continue
			}
			if isHTMLSeparator(strings.ToLower(token.Data)) {
				output.WriteByte(' ')
			}
		case html.TextToken:
			if skipDepth == 0 {
				output.Write(tokenizer.Text())
				output.WriteByte(' ')
			}
		}
	}
}

func isHTMLSeparator(tag string) bool {
	switch tag {
	case "br", "p", "div", "li", "ul", "ol", "section", "article", "header", "footer",
		"h1", "h2", "h3", "h4", "h5", "h6", "tr", "td", "th", "blockquote", "pre":
		return true
	default:
		return false
	}
}

func truncateCharacters(input string, maximum int) string {
	if !utf8.ValidString(input) {
		input = strings.ToValidUTF8(input, "�")
	}
	characters := []rune(input)
	if len(characters) <= maximum {
		return input
	}
	return string(characters[:maximum]) + "\n[...truncated]"
}
