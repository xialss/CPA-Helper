package cpahttp

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"
)

func Client(timeout time.Duration) *http.Client {
	return &http.Client{Timeout: timeout, CheckRedirect: func(req *http.Request, via []*http.Request) error {
		return http.ErrUseLastResponse
	}}
}

// PublicClient is used for user-supplied public data sources. It deliberately
// uses a direct transport and validates every DNS result at dial time so a
// hostname cannot resolve to loopback/private/link-local space (including a
// DNS-rebinding response after the initial URL validation).
func PublicClient(timeout time.Duration) *http.Client {
	transport := &http.Transport{
		DialContext:           publicDialContext,
		DisableKeepAlives:     true,
		TLSHandshakeTimeout:   10 * time.Second,
		ResponseHeaderTimeout: 30 * time.Second,
		ExpectContinueTimeout: 1 * time.Second,
	}
	return &http.Client{
		Timeout:   timeout,
		Transport: transport,
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			if len(via) >= 10 {
				return errors.New("stopped after 10 redirects")
			}
			return EnsurePublicHTTPSURL(req.URL.String())
		},
	}
}

func ManagementHeaders(key string) http.Header {
	headers := http.Header{}
	headers.Set("Authorization", "Bearer "+key)
	headers.Set("X-Management-Key", key)
	return headers
}

func MakeURL(baseURL, path string, query url.Values) string {
	base := strings.TrimRight(baseURL, "/")
	target := base + path
	if len(query) > 0 {
		target += "?" + query.Encode()
	}
	return target
}

func DoJSON(ctx context.Context, client *http.Client, method, target string, headers http.Header, body any) (*http.Response, []byte, error) {
	var reader io.Reader
	if body != nil {
		payload, err := json.Marshal(body)
		if err != nil {
			return nil, nil, err
		}
		reader = bytes.NewReader(payload)
	}
	req, err := http.NewRequestWithContext(ctx, method, target, reader)
	if err != nil {
		return nil, nil, err
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	for key, values := range headers {
		for _, value := range values {
			req.Header.Add(key, value)
		}
	}
	resp, err := client.Do(req)
	if err != nil {
		return nil, nil, err
	}
	defer resp.Body.Close()
	payload, err := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
	if err != nil {
		return resp, nil, err
	}
	return resp, payload, nil
}

func EnsureHTTPSURL(sourceURL string) error {
	parsed, err := url.Parse(sourceURL)
	if err != nil {
		return err
	}
	if parsed.Host == "" || (parsed.Scheme != "https" && parsed.Scheme != "http") {
		return errInvalidURL
	}
	return nil
}

func EnsurePublicHTTPSURL(sourceURL string) error {
	parsed, err := url.Parse(sourceURL)
	if err != nil || parsed.Scheme != "https" || parsed.Host == "" || parsed.User != nil {
		return errInvalidPublicURL
	}
	host := parsed.Hostname()
	if host == "" || strings.EqualFold(host, "localhost") || strings.HasSuffix(strings.ToLower(host), ".localhost") || strings.HasSuffix(strings.ToLower(host), ".local") {
		return errInvalidPublicURL
	}
	if ip := net.ParseIP(host); ip != nil && !isPublicIP(ip) {
		return errInvalidPublicURL
	}
	return nil
}

func publicDialContext(ctx context.Context, network, address string) (net.Conn, error) {
	host, port, err := net.SplitHostPort(address)
	if err != nil {
		return nil, err
	}
	if ip := net.ParseIP(host); ip != nil {
		if !isPublicIP(ip) {
			return nil, errInvalidPublicURL
		}
		return (&net.Dialer{}).DialContext(ctx, network, net.JoinHostPort(ip.String(), port))
	}
	if strings.EqualFold(host, "localhost") || strings.HasSuffix(strings.ToLower(host), ".localhost") || strings.HasSuffix(strings.ToLower(host), ".local") {
		return nil, errInvalidPublicURL
	}
	ips, err := net.DefaultResolver.LookupIP(ctx, "ip", host)
	if err != nil {
		return nil, err
	}
	if len(ips) == 0 {
		return nil, errors.New("public source hostname has no addresses")
	}
	for _, ip := range ips {
		if !isPublicIP(ip) {
			return nil, errInvalidPublicURL
		}
	}
	var lastErr error
	dialer := &net.Dialer{}
	for _, ip := range ips {
		conn, dialErr := dialer.DialContext(ctx, network, net.JoinHostPort(ip.String(), port))
		if dialErr == nil {
			return conn, nil
		}
		lastErr = dialErr
	}
	return nil, lastErr
}

func isPublicIP(ip net.IP) bool {
	if ip == nil || ip.IsLoopback() || ip.IsPrivate() || ip.IsLinkLocalUnicast() || ip.IsLinkLocalMulticast() || ip.IsUnspecified() || ip.IsMulticast() {
		return false
	}
	for _, cidr := range []string{"0.0.0.0/8", "100.64.0.0/10", "192.0.0.0/24", "192.0.2.0/24", "198.18.0.0/15", "198.51.100.0/24", "203.0.113.0/24", "2001:db8::/32"} {
		if _, network, err := net.ParseCIDR(cidr); err == nil && network.Contains(ip) {
			return false
		}
	}
	return true
}

type invalidURLError struct{}

func (invalidURLError) Error() string {
	return "url must be http or https"
}

var errInvalidURL error = invalidURLError{}
var errInvalidPublicURL error = errors.New("url must resolve to a public HTTPS address")
