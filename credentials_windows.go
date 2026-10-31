//go:build windows

package main

import (
	"encoding/hex"
	"errors"
	"golang.org/x/sys/windows"
	"runtime"
	"unicode/utf16"
	"unsafe"
)

func encryptCredential(s string) (string, error) {
	u := utf16.Encode([]rune(s))
	defer clear(u)
	b := make([]byte, len(u)*2)
	defer clear(b)
	if len(b) == 0 {
		return "", errors.New("empty credential")
	}
	for i, v := range u {
		b[2*i] = byte(v)
		b[2*i+1] = byte(v >> 8)
	}
	in := windows.DataBlob{Size: uint32(len(b)), Data: &b[0]}
	var out windows.DataBlob
	if windows.CryptProtectData(&in, nil, nil, 0, nil, windows.CRYPTPROTECT_UI_FORBIDDEN, &out) != nil {
		return "", errors.New("DPAPI encryption failed")
	}
	defer windows.LocalFree(windows.Handle(unsafe.Pointer(out.Data)))
	runtime.KeepAlive(b)
	raw := unsafe.Slice(out.Data, int(out.Size))
	defer clear(raw)
	return hex.EncodeToString(raw), nil
}

// PowerShell ConvertFrom-SecureString uses CurrentUser DPAPI and UTF-16LE.
func decryptToken(s string) (string, error) {
	b, e := hex.DecodeString(s)
	if e != nil || len(b) == 0 {
		return "", errors.New("invalid encrypted token")
	}
	in := windows.DataBlob{Size: uint32(len(b)), Data: &b[0]}
	var out windows.DataBlob
	if e = windows.CryptUnprotectData(&in, nil, nil, 0, nil, windows.CRYPTPROTECT_UI_FORBIDDEN, &out); e != nil {
		return "", errors.New("DPAPI decryption failed; re-enter token under this Windows account")
	}
	defer windows.LocalFree(windows.Handle(unsafe.Pointer(out.Data)))
	runtime.KeepAlive(b)
	raw := unsafe.Slice(out.Data, int(out.Size))
	defer clear(raw)
	if len(raw)%2 != 0 {
		return "", errors.New("invalid token encoding")
	}
	u := make([]uint16, len(raw)/2)
	for i := range u {
		u[i] = uint16(raw[2*i]) | uint16(raw[2*i+1])<<8
	}
	defer clear(u)
	return string(utf16.Decode(u)), nil
}
