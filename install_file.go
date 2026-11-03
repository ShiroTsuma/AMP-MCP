package main

import (
	"context"
	"crypto/sha512"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/url"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

func sourceURL(raw string) error {
	u, e := url.Parse(raw)
	if e != nil || u.Scheme != "https" || u.Host == "" || u.User != nil || u.Fragment != "" {
		return errors.New("source_url must be HTTPS without credentials or a fragment")
	}
	return nil
}
func fetchInstallFile(ctx context.Context, source, checksum string, maxBytes int64) ([]byte, error) {
	if e := sourceURL(source); e != nil {
		return nil, e
	}
	client := &http.Client{Timeout: 2 * time.Minute, CheckRedirect: func(r *http.Request, via []*http.Request) error {
		if len(via) >= 5 {
			return errors.New("too many redirects")
		}
		return sourceURL(r.URL.String())
	}}
	req, e := http.NewRequestWithContext(ctx, http.MethodGet, source, nil)
	if e != nil {
		return nil, errors.New("invalid download request")
	}
	req.Header.Set("User-Agent", "AMP-MCP/0.1")
	resp, e := client.Do(req)
	if e != nil {
		return nil, errors.New("HTTPS download failed; no instance file was written")
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		return nil, errors.New("download did not return HTTP 200; no instance file was written")
	}
	if resp.ContentLength > maxBytes {
		return nil, errors.New("download exceeds max_bytes")
	}
	b, e := io.ReadAll(io.LimitReader(resp.Body, maxBytes+1))
	if e != nil {
		return nil, errors.New("download interrupted; no instance file was written")
	}
	if int64(len(b)) > maxBytes {
		return nil, errors.New("download exceeds max_bytes")
	}
	sum := sha512.Sum512(b)
	if hex.EncodeToString(sum[:]) != checksum {
		return nil, errors.New("SHA-512 mismatch; no instance file was written")
	}
	return b, nil
}
func addInstallFileTool(s registry, a *App) {
	props := map[string]any{
		"connection":  map[string]any{"type": "string", "minLength": 1},
		"instance_id": map[string]any{"type": "string", "pattern": guidPattern},
		"filename":    map[string]any{"type": "string", "minLength": 1, "maxLength": 4096},
		"source_url":  map[string]any{"type": "string", "minLength": 1, "maxLength": 8192},
		"sha512":      map[string]any{"type": "string", "pattern": "^[0-9a-f]{128}$", "description": "Expected SHA-512 from the release publisher."},
		"overwrite":   map[string]any{"type": "boolean"},
		"max_bytes":   map[string]any{"type": "integer", "minimum": 1, "maximum": 256 * 1024 * 1024},
	}
	addTool(s, &mcp.Tool{Name: "amp_install_file_from_url", InputSchema: map[string]any{"type": "object", "properties": props, "required": []string{"connection", "instance_id", "filename", "source_url", "sha512", "overwrite"}, "additionalProperties": false}}, func(ctx context.Context, r *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		var x struct {
			Connection string
			Scope      string `json:"instance_id"`
			Filename   string
			Source     string `json:"source_url"`
			SHA512     string `json:"sha512"`
			Overwrite  bool
			Max        int64 `json:"max_bytes"`
		}
		json.Unmarshal(r.Params.Arguments, &x)
		name, e := normalizeRemote(x.Filename)
		if e != nil {
			return result(nil, e), nil
		}
		cl, e := a.client(x.Connection)
		if e != nil {
			return result(nil, e), nil
		}
		if x.Max == 0 {
			x.Max = 64 * 1024 * 1024
		}
		b, e := fetchInstallFile(ctx, x.Source, x.SHA512, x.Max)
		if e != nil {
			return result(nil, e), nil
		}
		v, e := uploadData(ctx, cl, x.Connection, x.Scope, name, b, x.Overwrite, "amp_install_file_from_url")
		return result(v, e), nil
	})
}
