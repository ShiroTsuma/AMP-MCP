package main

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/modelcontextprotocol/go-sdk/mcp"
	"os"
	"path"
	"path/filepath"
	"strings"
	"time"
	"unicode/utf8"
)

const transferChunk = 64 * 1024

func decodeFileChunk(v any) ([]byte, error) {
	m, ok := v.(map[string]any)
	if !ok {
		return nil, errors.New("AMP returned an invalid file chunk")
	}
	text, ok := m["Result"].(string)
	if !ok {
		return nil, errors.New("AMP file chunk has no base64 Result")
	}
	b, e := base64.StdEncoding.DecodeString(text)
	if e != nil {
		return nil, errors.New("AMP returned invalid base64 file data")
	}
	return b, nil
}
func normalizeRemote(p string) (string, error) {
	p = strings.ReplaceAll(p, "\\", "/")
	if p == "" || strings.ContainsAny(p, "\x00\r\n:") {
		return "", errors.New("invalid instance-relative file path")
	}
	for _, part := range strings.Split(p, "/") {
		if part == ".." {
			return "", errors.New("file path cannot contain parent-directory traversal")
		}
	}
	p = strings.TrimPrefix(path.Clean(p), "/")
	if p == "." {
		return "", errors.New("a file path is required")
	}
	return p, nil
}
func readTextFileResult(v any, offset, chunk int) (any, error) {
	b, e := decodeFileChunk(v)
	if e != nil {
		return nil, e
	}
	if !utf8.Valid(b) {
		return nil, errors.New("chunk is not valid UTF-8; use amp_file_read operation=download for binary data or a split Unicode character")
	}
	return map[string]any{"text": string(b), "bytes": len(b), "next_offset": offset + len(b), "eof": len(b) < chunk}, nil
}
func addFileTools(s registry, a *App) {
	common := func() map[string]any {
		return map[string]any{"connection": map[string]any{"type": "string", "minLength": 1}, "instance_id": map[string]any{"type": "string", "pattern": "^[0-9a-fA-F-]{36}$"}, "filename": map[string]any{"type": "string", "minLength": 1, "maxLength": 4096}}
	}
	props := common()
	props["max_bytes"] = map[string]any{"type": "integer", "minimum": 1, "maximum": 256 * 1024 * 1024}
	addTool(s, &mcp.Tool{Name: "amp_download_file", InputSchema: map[string]any{"type": "object", "properties": props, "required": []string{"connection", "instance_id", "filename"}, "additionalProperties": false}, Annotations: &mcp.ToolAnnotations{ReadOnlyHint: true}}, func(ctx context.Context, r *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		var x struct {
			Connection string
			Scope      string `json:"instance_id"`
			Filename   string
			Max        int `json:"max_bytes"`
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
			x.Max = 16 * 1024 * 1024
		}
		dir := filepath.Join(filepath.Dir(configPath()), "artifacts")
		if e = os.MkdirAll(dir, 0700); e != nil {
			return result(nil, errors.New("cannot create artifact directory")), nil
		}
		f, e := os.CreateTemp(dir, "download-*.part")
		if e != nil {
			return result(nil, errors.New("cannot create artifact")), nil
		}
		tmp := f.Name()
		defer os.Remove(tmp)
		defer f.Close()
		offset := 0
		for {
			v, e := cl.call(ctx, x.Scope, "FileManagerPlugin/ReadFileChunk", map[string]any{"Filename": name, "Offset": offset, "ChunkSize": transferChunk})
			if e != nil {
				return result(nil, e), nil
			}
			b, e := decodeFileChunk(v)
			if e != nil {
				return result(nil, e), nil
			}
			if len(b) > transferChunk {
				return result(nil, errors.New("AMP returned an oversized file chunk")), nil
			}
			if offset+len(b) > x.Max {
				return result(nil, errors.New("file exceeds max_bytes; partial artifact removed")), nil
			}
			if _, e = f.Write(b); e != nil {
				return result(nil, errors.New("cannot write artifact")), nil
			}
			offset += len(b)
			if len(b) < transferChunk {
				break
			}
		}
		if e = f.Sync(); e != nil {
			return result(nil, errors.New("cannot flush artifact")), nil
		}
		if e = f.Close(); e != nil {
			return result(nil, errors.New("cannot close artifact")), nil
		}
		var id [16]byte
		if _, e = rand.Read(id[:]); e != nil {
			return result(nil, e), nil
		}
		base := path.Base(name)
		base = strings.Map(func(r rune) rune {
			if r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || strings.ContainsRune("._-", r) {
				return r
			}
			return '_'
		}, base)
		if len(base) > 100 {
			base = base[:100]
		}
		target := filepath.Join(dir, hex.EncodeToString(id[:])+"-"+base)
		if e = os.Rename(tmp, target); e != nil {
			return result(nil, errors.New("cannot finalize artifact")), nil
		}
		return result(map[string]any{"path": target, "bytes": offset, "remote_filename": name}, nil), nil
	})
	props = common()
	props["text"] = map[string]any{"type": "string", "maxLength": 1024 * 1024}
	props["overwrite"] = map[string]any{"type": "boolean"}
	addTool(s, &mcp.Tool{Name: "amp_write_file", InputSchema: map[string]any{"type": "object", "properties": props, "required": []string{"connection", "instance_id", "filename", "text", "overwrite"}, "additionalProperties": false}}, func(ctx context.Context, r *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		var x struct {
			Connection     string
			Scope          string `json:"instance_id"`
			Filename, Text string
			Overwrite      bool
		}
		json.Unmarshal(r.Params.Arguments, &x)
		name, e := normalizeRemote(x.Filename)
		if e != nil {
			return result(nil, e), nil
		}
		data := []byte(x.Text)
		if len(data) > 1024*1024 {
			return result(nil, errors.New("UTF-8 data exceeds 1 MiB")), nil
		}
		cl, e := a.client(x.Connection)
		if e != nil {
			return result(nil, e), nil
		}
		v, e := uploadData(ctx, cl, x.Connection, x.Scope, name, data, x.Overwrite, "amp_write_file")
		return result(v, e), nil
	})
}
func numeric(v any, fallback int) int {
	if n, ok := v.(float64); ok {
		return int(n)
	}
	return fallback
}
func fileReadBounds(body map[string]any) error {
	chunk := numeric(body["ChunkSize"], transferChunk)
	if chunk < 1 || chunk > transferChunk {
		return fmt.Errorf("ChunkSize must be 1-%d", transferChunk)
	}
	body["ChunkSize"] = chunk
	body["Offset"] = numeric(body["Offset"], 0)
	return nil
}

func uploadData(ctx context.Context, cl *Client, connection, scope, name string, data []byte, overwrite bool, tool string) (any, error) {
	caps, e := cl.call(ctx, scope, "Core/GetAPISpec", map[string]any{})
	if e != nil {
		return nil, e
	}
	if !supportsEndpoint(caps, "FileManagerPlugin/WriteFileChunk") || !supportsEndpoint(caps, "FileManagerPlugin/ReleaseFileUploadLock") {
		return nil, errors.New("file upload is not supported by this instance")
	}
	if !overwrite {
		dir := path.Dir(name)
		if dir == "." {
			dir = ""
		} else {
			dir += "/"
		}
		v, e := cl.call(ctx, scope, "FileManagerPlugin/GetDirectoryListing", map[string]any{"Dir": dir})
		if e != nil {
			return nil, e
		}
		entries, ok := v.([]any)
		if !ok {
			return nil, errors.New("cannot establish overwrite preflight")
		}
		for _, entry := range entries {
			if m, ok := entry.(map[string]any); ok && m["Filename"] == path.Base(name) {
				return nil, errors.New("file already exists; choose overwrite explicitly")
			}
		}
	}
	d := Definition{Name: tool, Endpoint: "FileManagerPlugin/WriteFileChunk"}
	j, e := beginJob(d, connection, cl.conn, scope, scope)
	if e != nil {
		return nil, e
	}
	defer func() {
		releaseCtx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		cl.call(releaseCtx, scope, "FileManagerPlugin/ReleaseFileUploadLock", map[string]any{"Filename": name})
	}()
	for offset := 0; ; {
		end := min(offset+transferChunk, len(data))
		final := end == len(data)
		_, e = cl.call(ctx, scope, "FileManagerPlugin/WriteFileChunk", map[string]any{"Filename": name, "Data": base64.StdEncoding.EncodeToString(data[offset:end]), "Offset": offset, "FinalChunk": final})
		if e != nil {
			v, err := finishSubmission(j, nil, fmt.Errorf("upload interrupted after %d confirmed bytes; a partial file may remain: %w", offset, e))
			return v, err
		}
		offset = end
		if final {
			break
		}
	}
	return finishSubmission(j, map[string]any{"bytes_written": len(data), "filename": name, "atomic": false}, nil)
}
