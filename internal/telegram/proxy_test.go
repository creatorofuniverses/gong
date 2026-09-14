package telegram

import (
	"bufio"
	"context"
	"crypto/x509"
	"encoding/base64"
	"encoding/binary"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestHTTPProxyUsesCONNECTAndAuthentication(t *testing.T) {
	origin := telegramTLSServer(t)
	defer origin.Close()
	var connects atomic.Int32
	proxy := newConnectProxy(t, false, func(r *http.Request) {
		connects.Add(1)
		want := "Basic " + base64.StdEncoding.EncodeToString([]byte("user:pass"))
		if got := r.Header.Get("Proxy-Authorization"); got != want {
			t.Errorf("proxy authorization = %q, want %q", got, want)
		}
	})
	defer proxy.Close()

	client := proxyTestClient(t, origin.URL, "http://user:pass@"+proxy.Listener.Addr().String(), certPool(origin))
	got, err := client.Send(context.Background(), SendRequest{ChatID: "1", Text: "ok"})
	if err != nil || got.MessageID != 11 {
		t.Fatalf("result = %#v, error = %v", got, err)
	}
	if connects.Load() != 1 {
		t.Fatalf("CONNECT calls = %d, want 1", connects.Load())
	}
}

func TestHTTPSProxyValidatesTLSCertificate(t *testing.T) {
	origin := telegramTLSServer(t)
	defer origin.Close()
	var authenticated atomic.Bool
	proxy := newConnectProxy(t, true, func(r *http.Request) {
		want := "Basic " + base64.StdEncoding.EncodeToString([]byte("tls-user:tls-pass"))
		authenticated.Store(r.Header.Get("Proxy-Authorization") == want)
	})
	defer proxy.Close()

	proxyURL := "https://tls-user:tls-pass@" + proxy.Listener.Addr().String()
	client, err := New(Options{BotToken: "secret", BaseURL: origin.URL, ProxyURL: proxyURL, Timeout: time.Second})
	if err != nil {
		t.Fatal(err)
	}
	_, err = client.Send(context.Background(), SendRequest{ChatID: "1", Text: "x"})
	assertTypedError(t, err, "telegram_transport", 502, false)

	roots := certPool(origin, proxy)
	client = proxyTestClient(t, origin.URL, proxyURL, roots)
	got, err := client.Send(context.Background(), SendRequest{ChatID: "1", Text: "x"})
	if err != nil || got.MessageID != 11 {
		t.Fatalf("trusted proxy result = %#v, error = %v", got, err)
	}
	if !authenticated.Load() {
		t.Fatal("HTTPS proxy did not receive Basic authentication on CONNECT")
	}
}

func TestSOCKS5AndSOCKS5HUseAuthAndRemoteDNS(t *testing.T) {
	for _, scheme := range []string{"socks5", "socks5h"} {
		t.Run(scheme, func(t *testing.T) {
			origin := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				fmt.Fprint(w, `{"ok":true,"result":{"message_id":13}}`)
			}))
			defer origin.Close()
			originURL, err := url.Parse(origin.URL)
			if err != nil {
				t.Fatal(err)
			}
			_, port, err := net.SplitHostPort(originURL.Host)
			if err != nil {
				t.Fatal(err)
			}
			proxy, observation := newSOCKSProxy(t, "telegram.invalid", "user", "pass")
			defer proxy.Close()

			client := proxyTestClient(t, "http://telegram.invalid:"+port, scheme+"://user:pass@"+proxy.Addr().String(), nil)
			got, err := client.Send(context.Background(), SendRequest{ChatID: "1", Text: "x"})
			if err != nil || got.MessageID != 13 {
				t.Fatalf("result = %#v, error = %v", got, err)
			}
			select {
			case seen := <-observation:
				if seen != "user:pass@telegram.invalid" {
					t.Fatalf("SOCKS observation = %q", seen)
				}
			case <-time.After(time.Second):
				t.Fatal("SOCKS handshake was not observed")
			}
		})
	}
}

func TestUnavailableExplicitProxyNeverFallsBackToOrigin(t *testing.T) {
	var originCalls atomic.Int32
	origin := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		originCalls.Add(1)
		fmt.Fprint(w, `{"ok":true,"result":{"message_id":99}}`)
	}))
	defer origin.Close()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	proxyAddress := listener.Addr().String()
	listener.Close()

	client, err := New(Options{BotToken: "secret", BaseURL: origin.URL, ProxyURL: "http://proxy-user:proxy-pass@" + proxyAddress, Timeout: 100 * time.Millisecond})
	if err != nil {
		t.Fatal(err)
	}
	_, err = client.Send(context.Background(), SendRequest{ChatID: "1", Text: "x"})
	assertTypedError(t, err, "telegram_transport", 502, false)
	for _, secret := range []string{"proxy-user", "proxy-pass", "secret"} {
		if strings.Contains(err.Error(), secret) {
			t.Fatalf("safe error exposed %q: %v", secret, err)
		}
	}
	if originCalls.Load() != 0 {
		t.Fatalf("origin calls = %d, explicit proxy was bypassed", originCalls.Load())
	}
}

func TestNoProxyOptionDisablesTransportProxy(t *testing.T) {
	var originCalls atomic.Int32
	origin := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		originCalls.Add(1)
		fmt.Fprint(w, `{"ok":true,"result":{"message_id":14}}`)
	}))
	defer origin.Close()
	transport := http.DefaultTransport.(*http.Transport).Clone()
	transport.Proxy = func(*http.Request) (*url.URL, error) {
		return nil, fmt.Errorf("implicit proxy was used")
	}
	client, err := New(Options{
		BotToken: "secret", BaseURL: origin.URL, Timeout: time.Second,
		HTTPClient: &http.Client{Transport: transport},
	})
	if err != nil {
		t.Fatal(err)
	}
	got, err := client.Send(context.Background(), SendRequest{ChatID: "1", Text: "x"})
	if err != nil || got.MessageID != 14 {
		t.Fatalf("result = %#v, error = %v", got, err)
	}
	if originCalls.Load() != 1 {
		t.Fatalf("origin calls = %d, want 1", originCalls.Load())
	}
}

func TestProxyURLAcceptsConfigValidatedForms(t *testing.T) {
	for _, proxyURL := range []string{
		"HTTP://proxy.example/",
		"https://proxy.example",
		"socks5://user:pass@proxy.example/",
	} {
		if _, err := New(Options{BotToken: "secret", ProxyURL: proxyURL}); err != nil {
			t.Errorf("New(%q): %v", proxyURL, err)
		}
	}
}

func telegramTLSServer(t *testing.T) *httptest.Server {
	t.Helper()
	return httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, `{"ok":true,"result":{"message_id":11}}`)
	}))
}

func certPool(servers ...*httptest.Server) *x509.CertPool {
	pool := x509.NewCertPool()
	for _, server := range servers {
		pool.AddCert(server.Certificate())
	}
	return pool
}

func proxyTestClient(t *testing.T, baseURL, proxyURL string, roots *x509.CertPool) *Client {
	t.Helper()
	transport := http.DefaultTransport.(*http.Transport).Clone()
	transport.Proxy = nil
	if roots != nil {
		transport.TLSClientConfig.RootCAs = roots
	}
	client, err := New(Options{
		BotToken: "secret", BaseURL: baseURL, ProxyURL: proxyURL, Timeout: time.Second,
		HTTPClient: &http.Client{Transport: transport},
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(client.CloseIdleConnections)
	return client
}

func newConnectProxy(t *testing.T, tlsServer bool, observe func(*http.Request)) *httptest.Server {
	t.Helper()
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodConnect {
			http.Error(w, "CONNECT required", http.StatusMethodNotAllowed)
			return
		}
		if observe != nil {
			observe(r)
		}
		upstream, err := net.Dial("tcp", r.Host)
		if err != nil {
			http.Error(w, "upstream unavailable", http.StatusBadGateway)
			return
		}
		hijacker, ok := w.(http.Hijacker)
		if !ok {
			upstream.Close()
			t.Error("proxy response writer cannot hijack")
			return
		}
		client, buffered, err := hijacker.Hijack()
		if err != nil {
			upstream.Close()
			t.Error(err)
			return
		}
		defer client.Close()
		defer upstream.Close()
		if _, err := buffered.WriteString("HTTP/1.1 200 Connection Established\r\n\r\n"); err != nil {
			t.Error(err)
			return
		}
		if err := buffered.Flush(); err != nil {
			t.Error(err)
			return
		}
		tunnel(client, upstream)
	})
	if tlsServer {
		return httptest.NewTLSServer(handler)
	}
	return httptest.NewServer(handler)
}

func tunnel(left, right net.Conn) {
	var once sync.Once
	closeBoth := func() {
		left.Close()
		right.Close()
	}
	go func() {
		_, _ = io.Copy(left, right)
		once.Do(closeBoth)
	}()
	_, _ = io.Copy(right, left)
	once.Do(closeBoth)
}

func newSOCKSProxy(t *testing.T, expectedDomain, username, password string) (net.Listener, <-chan string) {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	observed := make(chan string, 1)
	go func() {
		conn, err := listener.Accept()
		if err != nil {
			return
		}
		defer conn.Close()
		reader := bufio.NewReader(conn)
		header := make([]byte, 2)
		if _, err := io.ReadFull(reader, header); err != nil {
			return
		}
		methods := make([]byte, int(header[1]))
		if _, err := io.ReadFull(reader, methods); err != nil {
			return
		}
		_, _ = conn.Write([]byte{5, 2})
		authHeader := make([]byte, 2)
		if _, err := io.ReadFull(reader, authHeader); err != nil {
			return
		}
		userBytes := make([]byte, int(authHeader[1]))
		if _, err := io.ReadFull(reader, userBytes); err != nil {
			return
		}
		passwordLength, err := reader.ReadByte()
		if err != nil {
			return
		}
		passwordBytes := make([]byte, int(passwordLength))
		if _, err := io.ReadFull(reader, passwordBytes); err != nil {
			return
		}
		if string(userBytes) != username || string(passwordBytes) != password {
			_, _ = conn.Write([]byte{1, 1})
			return
		}
		_, _ = conn.Write([]byte{1, 0})
		requestHeader := make([]byte, 4)
		if _, err := io.ReadFull(reader, requestHeader); err != nil || requestHeader[3] != 3 {
			return
		}
		domainLength, err := reader.ReadByte()
		if err != nil {
			return
		}
		domainBytes := make([]byte, int(domainLength))
		if _, err := io.ReadFull(reader, domainBytes); err != nil {
			return
		}
		portBytes := make([]byte, 2)
		if _, err := io.ReadFull(reader, portBytes); err != nil {
			return
		}
		domain := string(domainBytes)
		observed <- string(userBytes) + ":" + string(passwordBytes) + "@" + domain
		if domain != expectedDomain {
			return
		}
		upstream, err := net.Dial("tcp", net.JoinHostPort("127.0.0.1", fmt.Sprint(binary.BigEndian.Uint16(portBytes))))
		if err != nil {
			return
		}
		defer upstream.Close()
		_, _ = conn.Write([]byte{5, 0, 0, 1, 127, 0, 0, 1, 0, 0})
		tunnel(conn, upstream)
	}()
	return listener, observed
}
