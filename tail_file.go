package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"path"
	"strings"
	"unicode/utf8"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

func tailText(data []byte, start int64, lines int) (string, int, int64, bool, error) {
	partial := false
	if start > 0 {
		if i := bytes.IndexByte(data, '\n'); i >= 0 {
			data = data[i+1:]
			start += int64(i + 1)
		} else {
			partial = true
			for len(data) > 0 && !utf8.RuneStart(data[0]) {
				data = data[1:]
				start++
			}
		}
	}
	if !utf8.Valid(data) {
		return "", 0, start, partial, errors.New("tail is not valid UTF-8; use amp_file_read operation=download for another encoding or retry after the writer completes its current character")
	}
	text := string(data)
	logical := strings.TrimSuffix(text, "\n")
	if logical == "" {
		return text, 0, start, partial, nil
	}
	parts := strings.Split(logical, "\n")
	if len(parts) > lines {
		prefix := strings.Join(parts[:len(parts)-lines], "\n") + "\n"
		start += int64(len(prefix))
		text = text[len(prefix):]
		parts = parts[len(parts)-lines:]
		partial = false
	}
	return strings.ReplaceAll(text, "\r\n", "\n"), len(parts), start, partial, nil
}
func (a *App) tailFile(ctx context.Context, connection, scope, filename string, lines, maxBytes int) (any, error) {
	name, e := normalizeRemote(filename)
	if e != nil {
		return nil, e
	}
	cl, e := a.client(connection)
	if e != nil {
		return nil, e
	}
	dir := path.Dir(name)
	if dir == "." {
		dir = ""
	}
	v, e := cl.call(ctx, scope, "FileManagerPlugin/GetDirectoryListing", map[string]any{"Dir": dir})
	if e != nil {
		return nil, e
	}
	entries, ok := v.([]any)
	if !ok {
		return nil, errors.New("unexpected directory listing; cannot establish file size")
	}
	size := int64(-1)
	for _, entry := range entries {
		if m, ok := entry.(map[string]any); ok && m["Filename"] == path.Base(name) && m["IsDirectory"] == false {
			if n, ok := m["SizeBytes"].(float64); ok && n >= 0 && n <= 1<<53 && n == float64(int64(n)) {
				size = int64(n)
			}
		}
	}
	if size < 0 {
		return nil, errors.New("file not found in its directory listing")
	}
	start := max(int64(0), size-int64(maxBytes))
	requested := size - start
	data := []byte{}
	if requested > 0 {
		v, e = cl.call(ctx, scope, "FileManagerPlugin/ReadFileChunk", map[string]any{"Filename": name, "Offset": start, "ChunkSize": requested})
		if e != nil {
			return nil, e
		}
		data, e = decodeFileChunk(v)
		if e != nil {
			return nil, e
		}
		if int64(len(data)) > requested {
			return nil, errors.New("AMP returned more data than the bounded tail requested")
		}
	}
	next := start + int64(len(data))
	changed := int64(len(data)) < requested
	text, count, actual, partial, e := tailText(data, start, lines)
	if e != nil {
		return nil, e
	}
	return map[string]any{"text": text, "line_count": count, "observed_size": size, "start_offset": actual, "next_offset": next, "truncated": actual > 0, "partial_first_line": partial, "file_changed": changed, "note": "bounded end-of-file snapshot; offsets apply only to the same file, which may grow or rotate"}, nil
}
func addTailFileTool(s registry, a *App) {
	props := map[string]any{"connection": map[string]any{"type": "string", "minLength": 1}, "instance_id": map[string]any{"type": "string", "pattern": guidPattern}, "filename": map[string]any{"type": "string", "minLength": 1, "maxLength": 4096}, "lines": map[string]any{"type": "integer", "minimum": 1, "maximum": 500, "default": 100}, "max_bytes": map[string]any{"type": "integer", "minimum": 1, "maximum": transferChunk, "default": transferChunk}}
	addTool(s, &mcp.Tool{Name: "amp_tail_file", InputSchema: map[string]any{"type": "object", "properties": props, "required": []string{"connection", "instance_id", "filename"}, "additionalProperties": false}, Annotations: &mcp.ToolAnnotations{ReadOnlyHint: true}}, func(ctx context.Context, r *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		var x struct {
			Connection string
			Scope      string `json:"instance_id"`
			Filename   string
			Lines      int
			Max        int `json:"max_bytes"`
		}
		json.Unmarshal(r.Params.Arguments, &x)
		if x.Lines == 0 {
			x.Lines = 100
		}
		if x.Max == 0 {
			x.Max = transferChunk
		}
		v, e := a.tailFile(ctx, x.Connection, x.Scope, x.Filename, x.Lines, x.Max)
		return result(v, e), nil
	})
}
