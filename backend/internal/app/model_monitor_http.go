package app

import (
	"context"
	"errors"
	"fmt"
	"io"
	"mime"
	"net/http"
	"net/url"
	"strings"
	"time"
)

const (
	modelMonitorHTTPTimeout  = 15 * time.Second
	modelMonitorMaxRedirects = 5
	modelMonitorMaxBodyBytes = 8 << 20
)

func newModelMonitorBuiltinHTTPClient(proxyCfg ModelMonitorProxyConfig) (*http.Client, error) {
	normalized, err := normalizeModelMonitorProxyConfig(proxyCfg)
	if err != nil {
		return nil, err
	}
	defaultTransport, ok := http.DefaultTransport.(*http.Transport)
	if !ok {
		return nil, errors.New("default HTTP transport has unexpected type")
	}
	transport := defaultTransport.Clone()
	transport.Proxy = nil
	if normalized.Enabled {
		proxyURL, err := url.Parse(normalized.ProxyURL)
		if err != nil {
			return nil, validationError("代理地址必须是有效的 http://、https:// 或 socks5:// 地址")
		}
		transport.Proxy = http.ProxyURL(proxyURL)
	}
	return &http.Client{
		Timeout:   modelMonitorHTTPTimeout,
		Transport: transport,
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			if len(via) >= modelMonitorMaxRedirects {
				return errors.New("状态页重定向次数过多")
			}
			if req.URL.Scheme != "https" {
				return errors.New("状态页重定向必须使用 HTTPS")
			}
			return nil
		},
	}, nil
}

func normalizeModelMonitorProxyConfig(input ModelMonitorProxyConfig) (ModelMonitorProxyConfig, error) {
	proxyURL, err := normalizeModelMonitorProxyURL(input.ProxyURL)
	if err != nil {
		return ModelMonitorProxyConfig{}, err
	}
	if input.Enabled && proxyURL == "" {
		return ModelMonitorProxyConfig{}, validationError("启用代理时必须填写代理地址")
	}
	return ModelMonitorProxyConfig{Enabled: input.Enabled, ProxyURL: proxyURL}, nil
}

func normalizeModelMonitorProxyURL(value string) (string, error) {
	text := strings.TrimSpace(value)
	if text == "" {
		return "", nil
	}
	parsed, err := url.Parse(text)
	if err != nil || parsed.Host == "" || strings.TrimSpace(parsed.Hostname()) == "" {
		return "", validationError("代理地址必须是有效的 http://、https:// 或 socks5:// 地址")
	}
	switch strings.ToLower(parsed.Scheme) {
	case "http", "https", "socks5":
		parsed.Scheme = strings.ToLower(parsed.Scheme)
	case "sock5":
		parsed.Scheme = "socks5"
	default:
		return "", validationError("代理地址必须是有效的 http://、https:// 或 socks5:// 地址")
	}
	return parsed.String(), nil
}

func modelMonitorGet(ctx context.Context, client *http.Client, targetURL, expectedContentType string) ([]byte, error) {
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, targetURL, nil)
	if err != nil {
		return nil, err
	}
	request.Header.Set("Accept", expectedContentType)
	response, err := client.Do(request)
	if err != nil {
		return nil, fmt.Errorf("模型监控请求失败: %w", err)
	}
	defer response.Body.Close()
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return nil, fmt.Errorf("模型监控上游返回 HTTP %d", response.StatusCode)
	}
	contentType, _, err := mime.ParseMediaType(response.Header.Get("Content-Type"))
	if err != nil || !strings.EqualFold(contentType, expectedContentType) {
		return nil, fmt.Errorf("模型监控上游返回了不支持的内容类型 %q", response.Header.Get("Content-Type"))
	}
	limited := io.LimitReader(response.Body, modelMonitorMaxBodyBytes+1)
	body, err := io.ReadAll(limited)
	if err != nil {
		return nil, fmt.Errorf("读取模型监控上游响应失败: %w", err)
	}
	if len(body) > modelMonitorMaxBodyBytes {
		return nil, errors.New("模型监控上游响应超过 8 MiB 限制")
	}
	return body, nil
}
