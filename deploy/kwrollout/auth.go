package kwrollout

import (
	"bytes"
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/cookiejar"
	"net/url"
	"time"
)

// ManagementClient authenticates using the existing admin Secret and trusts only
// the ingress Secret's CA. Neither credentials nor response bodies enter errors.
func ManagementClient(ctx context.Context, kubeContext, namespace, origin string) (*http.Client, error) {
	return managementClient(ctx, kubectlCommand(kubeContext, namespace), origin)
}

func managementClient(ctx context.Context, execute func(context.Context, []byte, ...string) ([]byte, error), origin string) (*http.Client, error) {
	u, err := url.Parse(origin)
	if err != nil || u.Scheme != "https" || u.Host == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" || (u.Path != "" && u.Path != "/") {
		return nil, fmt.Errorf("management authentication requires an HTTPS origin")
	}
	readSecret := func(name string) (map[string][]byte, error) {
		body, err := execute(ctx, nil, "get", "secret", name, "-o", "json")
		if err != nil {
			return nil, fmt.Errorf("read authentication Secret %s", name)
		}
		var secret struct{ Data map[string][]byte }
		if json.Unmarshal(body, &secret) != nil {
			return nil, fmt.Errorf("invalid authentication Secret %s", name)
		}
		return secret.Data, nil
	}
	ca, err := readSecret("nexora-ingress-tls")
	if err != nil {
		return nil, err
	}
	roots := x509.NewCertPool()
	if !roots.AppendCertsFromPEM(ca["ca.crt"]) {
		return nil, fmt.Errorf("ingress Secret lacks a valid CA")
	}
	admin, err := readSecret("nexora-admin")
	if err != nil {
		return nil, err
	}
	if len(admin["username"]) == 0 || len(admin["password"]) == 0 {
		return nil, fmt.Errorf("admin Secret lacks credentials")
	}
	jar, err := cookiejar.New(nil)
	if err != nil {
		return nil, err
	}
	transport := http.DefaultTransport.(*http.Transport).Clone()
	transport.TLSClientConfig = &tls.Config{RootCAs: roots, MinVersion: tls.VersionTLS12}
	client := &http.Client{Transport: transport, Jar: jar, Timeout: 5 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	body, err := json.Marshal(map[string]string{"username": string(admin["username"]), "password": string(admin["password"])})
	if err != nil {
		return nil, fmt.Errorf("encode login request")
	}
	u.Path = "/api/v1/auth/login"
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, u.String(), bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("construct login request")
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := client.Do(req)
	if err != nil {
		client.CloseIdleConnections()
		return nil, fmt.Errorf("management authentication request failed")
	}
	defer resp.Body.Close()
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 4096))
	if resp.StatusCode != http.StatusOK || len(jar.Cookies(u)) == 0 {
		client.CloseIdleConnections()
		return nil, fmt.Errorf("management authentication rejected (HTTP %d)", resp.StatusCode)
	}
	return client, nil
}
