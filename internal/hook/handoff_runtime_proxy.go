package hook

import (
	"bufio"
	"context"
	"crypto/tls"
	"encoding/base64"
	"errors"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"time"
)

func handoffRuntimeProxyDial(direct *net.Dialer) func(context.Context, string, string) (net.Conn, error) {
	proxyTLS := &tls.Config{MinVersion: tls.VersionTLS12}
	return func(ctx context.Context, network, addr string) (net.Conn, error) {
		proxy, err := http.ProxyFromEnvironment(&http.Request{URL: &url.URL{Scheme: "https", Host: addr}})
		if err != nil {
			return nil, err
		}
		if proxy == nil {
			return direct.DialContext(ctx, network, addr)
		}
		return dialHandoffRuntimeProxy(ctx, network, addr, proxy, proxyTLS)
	}
}

func dialHandoffRuntimeProxy(ctx context.Context, network, addr string, proxy *url.URL, proxyTLS *tls.Config) (net.Conn, error) {
	return dialHandoffRuntimeProxyResolved(ctx, network, addr, proxy, proxyTLS, net.DefaultResolver.LookupNetIP)
}

func dialHandoffRuntimeProxyResolved(ctx context.Context, network, addr string, proxy *url.URL, proxyTLS *tls.Config, lookup func(context.Context, string, string) ([]netip.Addr, error)) (net.Conn, error) {
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	targets, err := pinHandoffRuntimeTargets(ctx, addr, lookup)
	if err != nil {
		return nil, err
	}
	port := proxy.Port()
	var dial func(context.Context, string, string) (net.Conn, error)
	switch proxy.Scheme {
	case "http":
		if port == "" {
			port = "80"
		}
		dial = (&net.Dialer{}).DialContext
	case "https":
		if port == "" {
			port = "443"
		}
		dial = (&tls.Dialer{Config: proxyTLS}).DialContext
	default:
		return nil, errors.New("runtime asset proxy must use HTTP or HTTPS")
	}
	// Only the operator's proxy endpoint may be local. CONNECT uses the
	// checked IP, and the outer transport authenticates the asset hostname.
	endpoint := net.JoinHostPort(proxy.Hostname(), port)
	deadline, _ := ctx.Deadline()
	for i, target := range targets {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		attempt, stop := context.WithTimeout(ctx, time.Until(deadline)/time.Duration(len(targets)-i))
		var conn net.Conn
		conn, err = openHandoffRuntimeTunnel(attempt, dial, network, endpoint, target, proxy)
		stop()
		if err == nil {
			return conn, nil
		}
	}
	return nil, err
}

func openHandoffRuntimeTunnel(ctx context.Context, dial func(context.Context, string, string) (net.Conn, error), network, endpoint, target string, proxy *url.URL) (net.Conn, error) {
	conn, err := dial(ctx, network, endpoint)
	if err != nil {
		return nil, err
	}
	stop := context.AfterFunc(ctx, func() { _ = conn.Close() })
	if err := connectHandoffRuntimeProxy(conn, target, proxy); err != nil {
		stop()
		_ = conn.Close()
		return nil, err
	}
	if !stop() {
		_ = conn.Close()
		return nil, ctx.Err()
	}
	return conn, nil
}

func pinHandoffRuntimeTargets(ctx context.Context, addr string, lookup func(context.Context, string, string) ([]netip.Addr, error)) ([]string, error) {
	host, port, err := net.SplitHostPort(addr)
	if err != nil {
		return nil, err
	}
	ips, err := lookup(ctx, "ip", host)
	if err != nil {
		return nil, err
	}
	if len(ips) == 0 {
		return nil, errors.New("runtime asset address did not resolve")
	}
	targets := make([]string, 0, len(ips))
	for _, ip := range ips {
		if !publicHandoffRuntimeIP(ip) {
			return nil, errors.New("runtime asset address is not public")
		}
		targets = append(targets, net.JoinHostPort(ip.Unmap().String(), port))
	}
	return targets, nil
}

func connectHandoffRuntimeProxy(conn net.Conn, target string, proxy *url.URL) error {
	req := &http.Request{
		Method: http.MethodConnect,
		URL:    &url.URL{Opaque: target},
		Host:   target,
		Header: make(http.Header),
	}
	if proxy.User != nil {
		password, _ := proxy.User.Password()
		credentials := proxy.User.Username() + ":" + password
		req.Header.Set("Proxy-Authorization", "Basic "+base64.StdEncoding.EncodeToString([]byte(credentials)))
	}
	if err := req.Write(conn); err != nil {
		return err
	}
	reader := bufio.NewReader(conn)
	resp, err := http.ReadResponse(reader, req)
	if err != nil {
		return err
	}
	if resp.StatusCode != http.StatusOK || reader.Buffered() != 0 {
		return errors.New("runtime asset proxy refused the CONNECT tunnel")
	}
	return nil
}
