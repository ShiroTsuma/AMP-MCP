package main

import (
	"bytes"
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strings"
	"sync"
	"time"
)

type Client struct {
	conn           Connection
	http           *http.Client
	mu             sync.Mutex
	sessions       map[string]string
	capabilities   map[string]map[string]any
	rootInstanceID string
}

type APIError struct {
	Kind       string
	HTTPStatus int
	Code       string
}

func (e *APIError) Error() string {
	switch e.Kind {
	case "session":
		return "AMP session expired or is invalid"
	case "permission":
		return "AMP denied this operation; check the account role permissions"
	case "rejected":
		if e.Code == "user_requires_disable" {
			return "AMP requires disabling the user before deletion; call amp_user operation=update with disabled=true, then delete"
		}
		return "AMP rejected the operation; inspect the panel event log"
	default:
		return fmt.Sprintf("AMP returned HTTP %d", e.HTTPStatus)
	}
}
func isSessionError(e error) bool { var a *APIError; return errors.As(e, &a) && a.Kind == "session" }

func newClient(c Connection) (*Client, error) {
	if c.Password != "" && (c.Token != "" || c.InstanceID == "") {
		return nil, errors.New("password authentication requires an instance-bound connection without a service token")
	}
	if c.InstanceID != "" && !validSetupGUID(c.InstanceID) {
		return nil, errors.New("connection instanceId must be a valid instance GUID")
	}
	u, e := url.Parse(c.URL)
	if e != nil || u.Scheme != "https" || u.Host == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" {
		return nil, errors.New("connection URL must be an HTTPS URL without credentials, query or fragment")
	}
	timeout := c.TimeoutSeconds
	if timeout == 0 {
		timeout = 30
	}
	if timeout < 1 || timeout > 300 {
		return nil, errors.New("timeoutSeconds must be between 1 and 300")
	}
	transport := http.DefaultTransport.(*http.Transport).Clone()
	if c.CACert != "" {
		pem, e := os.ReadFile(c.CACert)
		if e != nil {
			return nil, errors.New("cannot read configured CA file")
		}
		roots, e := x509.SystemCertPool()
		if e != nil {
			roots = x509.NewCertPool()
		}
		if !roots.AppendCertsFromPEM(pem) {
			return nil, errors.New("CA file contains no certificates")
		}
		transport.TLSClientConfig = &tls.Config{RootCAs: roots, MinVersion: tls.VersionTLS12}
	}
	return &Client{conn: c, http: &http.Client{Transport: transport, Timeout: time.Duration(timeout) * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}, sessions: map[string]string{}, capabilities: map[string]map[string]any{}}, nil
}
func (c *Client) request(ctx context.Context, scope, endpoint, sid string, args map[string]any) (any, error) {
	if c.conn.InstanceID != "" {
		if scope != "" && !strings.EqualFold(scope, c.conn.InstanceID) {
			return nil, errors.New("this connection is bound to a different application instance")
		}
		scope = c.conn.InstanceID
	}
	b, e := json.Marshal(args)
	if e != nil {
		return nil, e
	}
	base := strings.TrimRight(c.conn.URL, "/")
	if scope != "" {
		base += "/API/ADSModule/Servers/" + url.PathEscape(scope)
	}
	req, e := http.NewRequestWithContext(ctx, "POST", base+"/API/"+endpoint, bytes.NewReader(b))
	if e != nil {
		return nil, errors.New("invalid AMP request")
	}
	req.Header.Set("Accept", "application/json")
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("User-Agent", "AMP-MCP/"+version)
	if sid != "" {
		req.Header.Set("Authorization", "Bearer "+sid)
	}
	r, e := c.http.Do(req)
	if e != nil {
		return nil, errors.New("AMP request failed or timed out; a submitted mutation may still have executed")
	}
	defer r.Body.Close()
	if r.StatusCode != 200 {
		kind := "http"
		if r.StatusCode == http.StatusUnauthorized {
			kind = "session"
		}
		if r.StatusCode == http.StatusForbidden {
			kind = "permission"
		}
		return nil, &APIError{Kind: kind, HTTPStatus: r.StatusCode}
	}
	data, e := io.ReadAll(io.LimitReader(r.Body, 4*1024*1024+1))
	if e != nil || len(data) > 4*1024*1024 {
		return nil, errors.New("AMP response exceeds limit or could not be read")
	}
	if len(data) == 0 {
		return map[string]any{"submitted": true}, nil
	}
	var v any
	if json.Unmarshal(data, &v) != nil {
		return nil, errors.New("AMP returned invalid JSON")
	}
	if m, ok := v.(map[string]any); ok {
		if m["Title"] != nil && m["Message"] != nil {
			kind := "rejected"
			message, _ := m["Message"].(string)
			if strings.Contains(message, "Session.Exists") {
				kind = "session"
			} else if m["Title"] == "Unauthorized Access" {
				kind = "permission"
			}
			return nil, &APIError{Kind: kind}
		}
		if s, ok := m["Status"].(bool); ok && !s {
			code := ""
			if m["Reason"] == "Users must be disabled before they can be deleted." {
				code = "user_requires_disable"
			}
			return nil, &APIError{Kind: "rejected", Code: code}
		}
	}
	return v, nil
}
func (c *Client) loginLocked(ctx context.Context, scope string) (string, error) {
	var token string
	var password string
	var e error
	if scope == "" {
		if c.conn.Password != "" {
			password, e = decryptToken(c.conn.Password)
		} else {
			token, e = decryptToken(c.conn.Token)
		}
		if e != nil {
			return "", e
		}
	} else {
		// Follow ADS's own UI: a service token belongs to ADS, not its children.
		// Delegation stays internal and is never exposed as an MCP result.
		for attempt := 0; attempt < 2; attempt++ {
			sid := c.sessions[""]
			if sid == "" {
				sid, e = c.loginLocked(ctx, "")
				if e != nil {
					return "", e
				}
			}
			if c.rootInstanceID == "" {
				info, err := c.request(ctx, "", "Core/GetModuleInfo", sid, map[string]any{})
				if isSessionError(err) && attempt == 0 {
					delete(c.sessions, "")
					delete(c.capabilities, "")
					continue
				}
				if err != nil {
					return "", err
				}
				if m, ok := info.(map[string]any); ok {
					c.rootInstanceID, _ = m["InstanceId"].(string)
				}
			}
			if c.rootInstanceID != "" && strings.EqualFold(c.rootInstanceID, scope) {
				// ADS can also appear in its own instance list. It already owns
				// this session; no child delegation or credential forwarding.
				c.sessions[scope] = sid
				delete(c.capabilities, scope)
				return sid, nil
			}
			v, err := c.request(ctx, "", "ADSModule/ManageInstance", sid, map[string]any{"InstanceId": scope})
			if isSessionError(err) && attempt == 0 {
				delete(c.sessions, "")
				delete(c.capabilities, "")
				continue
			}
			if err != nil {
				return "", err
			}
			if m, ok := v.(map[string]any); ok {
				token, _ = m["Result"].(string)
			}
			if token == "" {
				return "", errors.New("ADS did not provide an instance login token")
			}
			break
		}
	}
	v, e := c.request(ctx, scope, "Core/Login", "", map[string]any{"username": c.conn.Username, "password": password, "token": token, "rememberMe": false})
	if e != nil {
		return "", e
	}
	m, ok := v.(map[string]any)
	if !ok || m["success"] != true {
		return "", errors.New("AMP login failed")
	}
	sid, _ := m["sessionID"].(string)
	if sid == "" {
		return "", errors.New("AMP login returned no session")
	}
	c.sessions[scope] = sid
	delete(c.capabilities, scope)
	return sid, nil
}
func safeRead(endpoint string) bool {
	if endpoint == "Core/CurrentSessionHasPermission" {
		return true
	}
	if moduleReadOnly(endpoint) {
		return true
	}
	if endpoint == "Core/GetUpdates" {
		return false
	}
	if endpoint == "Core/GetAPISpec" {
		return true
	}
	if endpoint == "Core/GetConfig" {
		return true
	}
	if endpoint == "Core/GetActiveAMPSessions" {
		return true
	}
	if endpoint == "FileManagerPlugin/ReadFileChunk" || endpoint == "FileManagerPlugin/GetDirectoryListing" {
		return true
	}
	var defs []Definition
	json.Unmarshal(catalog, &defs)
	for _, d := range defs {
		if d.Endpoint == endpoint {
			return d.ReadOnly
		}
	}
	return false
}
func supportsEndpoint(v any, endpoint string) bool {
	caps, ok := v.(map[string]any)
	if !ok {
		return false
	}
	p := strings.SplitN(endpoint, "/", 2)
	if len(p) != 2 {
		return false
	}
	mod, ok := caps[p[0]].(map[string]any)
	return ok && mod[p[1]] != nil
}
func (c *Client) invalidateSessionLocked(scope string) {
	sid := c.sessions[scope]
	// A root session may be cached under ADS's own instance GUID as well.
	for key, value := range c.sessions {
		if key == scope || (sid != "" && value == sid) {
			delete(c.sessions, key)
			delete(c.capabilities, key)
		}
	}
}
func (c *Client) call(ctx context.Context, scope, endpoint string, args map[string]any) (any, error) {
	if c.conn.InstanceID != "" {
		if scope != "" && !strings.EqualFold(scope, c.conn.InstanceID) {
			return nil, errors.New("this connection is bound to a different application instance")
		}
		scope = ""
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	for attempt := 0; attempt < 2; attempt++ {
		sid := c.sessions[scope]
		if sid == "" {
			var e error
			sid, e = c.loginLocked(ctx, scope)
			if e != nil {
				return nil, e
			}
		}
		if endpoint == "Core/GetAPISpec" && c.capabilities[scope] != nil {
			return c.capabilities[scope], nil
		}
		if endpoint != "Core/GetAPISpec" {
			caps := c.capabilities[scope]
			if caps == nil {
				v, e := c.request(ctx, scope, "Core/GetAPISpec", sid, map[string]any{})
				if e != nil {
					if isSessionError(e) {
						c.invalidateSessionLocked(scope)
						if safeRead(endpoint) && attempt == 0 {
							continue
						}
					}
					return nil, e
				}
				var ok bool
				caps, ok = v.(map[string]any)
				if !ok {
					return nil, errors.New("AMP returned invalid API capabilities")
				}
				c.capabilities[scope] = caps
			}
			parts := strings.SplitN(endpoint, "/", 2)
			module, ok := caps[parts[0]].(map[string]any)
			if !ok || module[parts[1]] == nil {
				return nil, errors.New("this endpoint is not supported by the selected instance or plugin")
			}
		}
		v, e := c.request(ctx, scope, endpoint, sid, args)
		if e == nil && endpoint == "Core/GetAPISpec" {
			if caps, ok := v.(map[string]any); ok {
				c.capabilities[scope] = caps
			}
		}
		if isSessionError(e) {
			c.invalidateSessionLocked(scope)
			if attempt == 0 && safeRead(endpoint) {
				continue
			}
		}
		return v, e
	}
	return nil, errors.New("AMP session refresh failed")
}
func (c *Client) close() {
	c.mu.Lock()
	defer c.mu.Unlock()
	for scope, sid := range c.sessions {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		c.request(ctx, scope, "Core/Logout", sid, map[string]any{})
		cancel()
	}
	clear(c.sessions)
}
