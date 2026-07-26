package plugin

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"
)

const (
	remoteWasmRequestTimeout = 30 * time.Second
	remoteWasmDialTimeout    = 10 * time.Second
	remoteWasmMaxRedirects   = 5
)

// newRemoteWasmHTTPClient 创建只允许访问公网 HTTPS 地址的下载客户端。
func newRemoteWasmHTTPClient() *http.Client {
	dialer := &net.Dialer{Timeout: remoteWasmDialTimeout, KeepAlive: 30 * time.Second}
	transport := &http.Transport{
		Proxy:                 nil,
		ForceAttemptHTTP2:     true,
		TLSHandshakeTimeout:   remoteWasmDialTimeout,
		ResponseHeaderTimeout: remoteWasmDialTimeout,
		IdleConnTimeout:       30 * time.Second,
		DialContext: func(ctx context.Context, network, address string) (net.Conn, error) {
			host, port, err := net.SplitHostPort(address)
			if err != nil {
				return nil, fmt.Errorf("解析远程 Wasm 地址: %w", err)
			}
			addresses, err := resolvePublicRemoteAddresses(ctx, host)
			if err != nil {
				return nil, err
			}
			var dialErrors []error
			for _, address := range addresses {
				connection, dialErr := dialer.DialContext(ctx, network, net.JoinHostPort(address.String(), port))
				if dialErr == nil {
					return connection, nil
				}
				dialErrors = append(dialErrors, dialErr)
			}
			return nil, fmt.Errorf("连接远程 Wasm 地址: %w", errors.Join(dialErrors...))
		},
	}
	return &http.Client{
		Transport: transport,
		Timeout:   remoteWasmRequestTimeout,
		CheckRedirect: func(request *http.Request, via []*http.Request) error {
			if len(via) >= remoteWasmMaxRedirects {
				return fmt.Errorf("远程 Wasm 下载重定向超过 %d 次", remoteWasmMaxRedirects)
			}
			if _, err := validateRemoteWasmURL(request.URL.String()); err != nil {
				return fmt.Errorf("远程 Wasm 重定向地址无效: %w", err)
			}
			return nil
		},
	}
}

// downloadRemoteWasm 下载 URL 指向的 Wasm 二进制到受控临时文件。
func downloadRemoteWasm(ctx context.Context, client *http.Client, rawURL, directory string, maxSize int64) (string, error) {
	parsedURL, err := validateRemoteWasmURL(rawURL)
	if err != nil {
		return "", err
	}
	if client == nil {
		return "", fmt.Errorf("远程 Wasm 下载客户端不能为空")
	}

	request, err := http.NewRequestWithContext(ctx, http.MethodGet, parsedURL.String(), nil)
	if err != nil {
		return "", fmt.Errorf("创建远程 Wasm 下载请求: %w", err)
	}
	request.Header.Set("Accept", "application/wasm, application/octet-stream")
	response, err := client.Do(request)
	if err != nil {
		return "", fmt.Errorf("下载远程 Wasm: %w", err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return "", fmt.Errorf("远程 Wasm 直链返回 HTTP %d，要求直接返回二进制内容", response.StatusCode)
	}
	if response.ContentLength > maxSize {
		return "", fmt.Errorf("远程 Wasm 文件超过大小限制: %d > %d", response.ContentLength, maxSize)
	}

	temporary, err := os.CreateTemp(directory, ".remote-wasm-*.tmp")
	if err != nil {
		return "", fmt.Errorf("创建远程 Wasm 临时文件: %w", err)
	}
	temporaryPath := temporary.Name()
	keep := false
	defer func() {
		_ = temporary.Close()
		if !keep {
			_ = os.Remove(temporaryPath)
		}
	}()

	written, err := io.Copy(temporary, io.LimitReader(response.Body, maxSize+1))
	if err != nil {
		return "", fmt.Errorf("保存远程 Wasm: %w", err)
	}
	if written > maxSize {
		return "", fmt.Errorf("远程 Wasm 文件超过大小限制: %d > %d", written, maxSize)
	}
	if err := temporary.Sync(); err != nil {
		return "", fmt.Errorf("同步远程 Wasm 临时文件: %w", err)
	}
	if err := temporary.Close(); err != nil {
		return "", fmt.Errorf("关闭远程 Wasm 临时文件: %w", err)
	}
	if err := validateWasmFile(temporaryPath, maxSize); err != nil {
		return "", fmt.Errorf("URL 未直接返回有效 Wasm 二进制: %w", err)
	}
	keep = true
	return temporaryPath, nil
}

func validateRemoteWasmURL(rawURL string) (*url.URL, error) {
	if strings.TrimSpace(rawURL) != rawURL || rawURL == "" {
		return nil, fmt.Errorf("远程 Wasm URL 不能为空或包含首尾空白")
	}
	parsedURL, err := url.Parse(rawURL)
	if err != nil {
		return nil, fmt.Errorf("解析远程 Wasm URL: %w", err)
	}
	if parsedURL.Scheme != "https" {
		return nil, fmt.Errorf("远程 Wasm URL 必须使用 HTTPS")
	}
	if parsedURL.Host == "" || parsedURL.Hostname() == "" || parsedURL.Opaque != "" {
		return nil, fmt.Errorf("远程 Wasm URL 必须是绝对地址")
	}
	if parsedURL.User != nil {
		return nil, fmt.Errorf("远程 Wasm URL 不能包含用户凭据")
	}
	if parsedURL.Fragment != "" {
		return nil, fmt.Errorf("远程 Wasm URL 不能包含 fragment")
	}
	hostname := strings.TrimSuffix(strings.ToLower(parsedURL.Hostname()), ".")
	if hostname == "localhost" || strings.HasSuffix(hostname, ".localhost") {
		return nil, fmt.Errorf("远程 Wasm URL 不能指向本地主机")
	}
	if address := net.ParseIP(hostname); address != nil && !isPublicRemoteAddress(address) {
		return nil, fmt.Errorf("远程 Wasm URL 不能指向非公网地址")
	}
	return parsedURL, nil
}

func resolvePublicRemoteAddresses(ctx context.Context, host string) ([]net.IP, error) {
	if address := net.ParseIP(host); address != nil {
		if !isPublicRemoteAddress(address) {
			return nil, fmt.Errorf("远程 Wasm 地址不是公网 IP")
		}
		return []net.IP{address}, nil
	}
	resolved, err := net.DefaultResolver.LookupIP(ctx, "ip", host)
	if err != nil {
		return nil, fmt.Errorf("解析远程 Wasm 域名: %w", err)
	}
	if len(resolved) == 0 {
		return nil, fmt.Errorf("远程 Wasm 域名没有可用地址")
	}
	for _, address := range resolved {
		if !isPublicRemoteAddress(address) {
			return nil, fmt.Errorf("远程 Wasm 域名解析到非公网地址 %s", address)
		}
	}
	return resolved, nil
}

func isPublicRemoteAddress(address net.IP) bool {
	if ipv4 := address.To4(); ipv4 != nil && ipv4[0] == 100 && ipv4[1] >= 64 && ipv4[1] <= 127 {
		return false
	}
	return address.IsGlobalUnicast() &&
		!address.IsPrivate() &&
		!address.IsLoopback() &&
		!address.IsLinkLocalUnicast() &&
		!address.IsLinkLocalMulticast() &&
		!address.IsUnspecified()
}
