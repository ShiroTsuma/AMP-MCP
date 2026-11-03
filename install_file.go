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
	"os"
	"path/filepath"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// maxTransferBytes bounds downloads and URL installs; data is staged on disk, never held in memory.
const maxTransferBytes = 5 * 1024 * 1024 * 1024

// installStallTimeout aborts a download that delivers no data for this long.
const installStallTimeout = 2 * time.Minute

func sourceURL(raw string) error {
	u, e := url.Parse(raw)
	if e != nil || u.Scheme != "https" || u.Host == "" || u.User != nil || u.Fragment != "" {
		return errors.New("source_url must be HTTPS without credentials or a fragment")
	}
	return nil
}

// stallReader extends the download deadline whenever bytes arrive.
type stallReader struct {
	r     io.Reader
	timer *time.Timer
}

func (s stallReader) Read(p []byte) (int, error) {
	n, e := s.r.Read(p)
	if n > 0 {
		s.timer.Reset(installStallTimeout)
	}
	return n, e
}

// fetchInstallFile downloads source into a staging file and verifies its SHA-512.
// The caller owns the returned file and must close and remove it.
func fetchInstallFile(ctx context.Context, source, checksum string, maxBytes int64) (*os.File, int64, error) {
	if e := sourceURL(source); e != nil {
		return nil, 0, e
	}
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	timer := time.AfterFunc(installStallTimeout, cancel)
	defer timer.Stop()
	client := &http.Client{CheckRedirect: func(r *http.Request, via []*http.Request) error {
		if len(via) >= 5 {
			return errors.New("too many redirects")
		}
		return sourceURL(r.URL.String())
	}}
	req, e := http.NewRequestWithContext(ctx, http.MethodGet, source, nil)
	if e != nil {
		return nil, 0, errors.New("invalid download request")
	}
	req.Header.Set("User-Agent", "AMP-MCP/0.1")
	resp, e := client.Do(req)
	if e != nil {
		return nil, 0, errors.New("HTTPS download failed; no instance file was written")
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		return nil, 0, errors.New("download did not return HTTP 200; no instance file was written")
	}
	if resp.ContentLength > maxBytes {
		return nil, 0, errors.New("download exceeds max_bytes")
	}
	dir := filepath.Join(filepath.Dir(configPath()), "artifacts")
	if e = os.MkdirAll(dir, 0700); e != nil {
		return nil, 0, errors.New("cannot create artifact directory")
	}
	f, e := os.CreateTemp(dir, "install-*.part")
	if e != nil {
		return nil, 0, errors.New("cannot create download staging file")
	}
	discard := func(err error) (*os.File, int64, error) {
		f.Close()
		os.Remove(f.Name())
		return nil, 0, err
	}
	h := sha512.New()
	n, e := io.Copy(io.MultiWriter(f, h), io.LimitReader(stallReader{resp.Body, timer}, maxBytes+1))
	if e != nil {
		return discard(errors.New("download interrupted or stalled; no instance file was written"))
	}
	if n > maxBytes {
		return discard(errors.New("download exceeds max_bytes"))
	}
	if hex.EncodeToString(h.Sum(nil)) != checksum {
		return discard(errors.New("SHA-512 mismatch; no instance file was written"))
	}
	return f, n, nil
}
func addInstallFileTool(s registry, a *App) {
	props := map[string]any{
		"connection":  map[string]any{"type": "string", "minLength": 1},
		"instance_id": map[string]any{"type": "string", "pattern": guidPattern},
		"filename":    map[string]any{"type": "string", "minLength": 1, "maxLength": 4096},
		"source_url":  map[string]any{"type": "string", "minLength": 1, "maxLength": 8192},
		"sha512":      map[string]any{"type": "string", "pattern": "^[0-9a-f]{128}$", "description": "Expected SHA-512 from the release publisher."},
		"overwrite":   map[string]any{"type": "boolean"},
		"max_bytes":   map[string]any{"type": "integer", "minimum": 1, "maximum": maxTransferBytes},
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
		f, size, e := fetchInstallFile(ctx, x.Source, x.SHA512, x.Max)
		if e != nil {
			return result(nil, e), nil
		}
		defer os.Remove(f.Name())
		defer f.Close()
		v, e := uploadData(ctx, cl, x.Connection, x.Scope, name, f, size, x.Overwrite, "amp_install_file_from_url")
		return result(v, e), nil
	})
}
