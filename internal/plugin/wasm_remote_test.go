package plugin

import (
	"bytes"
	"context"
	"io"
	"net"
	"net/http"
	"os"
	"strings"
	"testing"
)

type roundTripFunc func(*http.Request) (*http.Response, error)

func (function roundTripFunc) RoundTrip(request *http.Request) (*http.Response, error) {
	return function(request)
}

func TestDownloadRemoteWasm(t *testing.T) {
	wasm := append([]byte("\x00asm\x01\x00\x00\x00"), 0x00, 0x00)
	client := &http.Client{Transport: roundTripFunc(func(request *http.Request) (*http.Response, error) {
		if request.URL.String() != "https://plugins.example/signin.wasm" {
			t.Fatalf("request URL = %q", request.URL)
		}
		if request.Header.Get("Accept") == "" {
			t.Fatal("request does not advertise Wasm response types")
		}
		return &http.Response{
			StatusCode:    http.StatusOK,
			Body:          io.NopCloser(bytes.NewReader(wasm)),
			ContentLength: int64(len(wasm)),
			Header:        make(http.Header),
			Request:       request,
		}, nil
	})}

	path, err := downloadRemoteWasm(context.Background(), client, "https://plugins.example/signin.wasm", t.TempDir(), 1024)
	if err != nil {
		t.Fatalf("download remote Wasm: %v", err)
	}
	defer os.Remove(path)
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read downloaded Wasm: %v", err)
	}
	if !bytes.Equal(got, wasm) {
		t.Fatalf("downloaded bytes = %x, want %x", got, wasm)
	}
}

func TestDownloadRemoteWasmRejectsNonBinaryResponse(t *testing.T) {
	client := &http.Client{Transport: roundTripFunc(func(request *http.Request) (*http.Response, error) {
		body := "<html>GitHub login</html>"
		return &http.Response{
			StatusCode:    http.StatusOK,
			Body:          io.NopCloser(strings.NewReader(body)),
			ContentLength: int64(len(body)),
			Header:        make(http.Header),
			Request:       request,
		}, nil
	})}

	_, err := downloadRemoteWasm(context.Background(), client, "https://github.com/example/plugin", t.TempDir(), 1024)
	if err == nil || !strings.Contains(err.Error(), "未直接返回有效 Wasm 二进制") {
		t.Fatalf("error = %v, want direct Wasm binary rejection", err)
	}
}

func TestDownloadRemoteWasmEnforcesStreamingSizeLimit(t *testing.T) {
	body := append([]byte("\x00asm\x01\x00\x00\x00"), bytes.Repeat([]byte{0}, 32)...)
	client := &http.Client{Transport: roundTripFunc(func(request *http.Request) (*http.Response, error) {
		return &http.Response{
			StatusCode:    http.StatusOK,
			Body:          io.NopCloser(bytes.NewReader(body)),
			ContentLength: -1,
			Header:        make(http.Header),
			Request:       request,
		}, nil
	})}

	_, err := downloadRemoteWasm(context.Background(), client, "https://plugins.example/large.wasm", t.TempDir(), 16)
	if err == nil || !strings.Contains(err.Error(), "超过大小限制") {
		t.Fatalf("error = %v, want size limit rejection", err)
	}
}

func TestDownloadRemoteWasmRejectsHTTPRedirect(t *testing.T) {
	client := newRemoteWasmHTTPClient()
	client.Transport = roundTripFunc(func(request *http.Request) (*http.Response, error) {
		header := make(http.Header)
		header.Set("Location", "http://169.254.169.254/plugin.wasm")
		return &http.Response{
			StatusCode: http.StatusFound,
			Body:       http.NoBody,
			Header:     header,
			Request:    request,
		}, nil
	})

	_, err := downloadRemoteWasm(context.Background(), client, "https://plugins.example/plugin.wasm", t.TempDir(), 1024)
	if err == nil || !strings.Contains(err.Error(), "必须使用 HTTPS") {
		t.Fatalf("error = %v, want insecure redirect rejection", err)
	}
}

func TestValidateRemoteWasmURL(t *testing.T) {
	tests := []struct {
		name    string
		rawURL  string
		wantErr bool
	}{
		{name: "GitHub release", rawURL: "https://github.com/example/project/releases/download/v1/plugin.wasm"},
		{name: "GitHub raw", rawURL: "https://raw.githubusercontent.com/example/project/main/plugin.wasm"},
		{name: "HTTP", rawURL: "http://example.com/plugin.wasm", wantErr: true},
		{name: "relative", rawURL: "plugin.wasm", wantErr: true},
		{name: "credentials", rawURL: "https://token@example.com/plugin.wasm", wantErr: true},
		{name: "localhost", rawURL: "https://localhost/plugin.wasm", wantErr: true},
		{name: "loopback IPv4", rawURL: "https://127.0.0.1/plugin.wasm", wantErr: true},
		{name: "private IPv4", rawURL: "https://192.168.1.10/plugin.wasm", wantErr: true},
		{name: "link local IPv6", rawURL: "https://[fe80::1]/plugin.wasm", wantErr: true},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			_, err := validateRemoteWasmURL(test.rawURL)
			if (err != nil) != test.wantErr {
				t.Fatalf("validateRemoteWasmURL(%q) error = %v, wantErr = %t", test.rawURL, err, test.wantErr)
			}
		})
	}
}

func TestIsPublicRemoteAddress(t *testing.T) {
	tests := []struct {
		address string
		want    bool
	}{
		{address: "1.1.1.1", want: true},
		{address: "2606:4700:4700::1111", want: true},
		{address: "127.0.0.1"},
		{address: "10.0.0.1"},
		{address: "100.64.0.1"},
		{address: "169.254.169.254"},
		{address: "::1"},
		{address: "fd00::1"},
	}
	for _, test := range tests {
		t.Run(test.address, func(t *testing.T) {
			if got := isPublicRemoteAddress(net.ParseIP(test.address)); got != test.want {
				t.Fatalf("isPublicRemoteAddress(%s) = %t, want %t", test.address, got, test.want)
			}
		})
	}
}
