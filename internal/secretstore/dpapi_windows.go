//go:build windows

package secretstore

import (
	"encoding/base64"
	"encoding/hex"
	"errors"
	"runtime"
	"strings"
	"unicode/utf16"
	"unicode/utf8"
	"unsafe"

	"golang.org/x/sys/windows"
)

func platformStore(app string, opts Options, backend string) (Store, error) {
	if backend == BackendSecretService {
		return nil, errors.New("secretservice: references were created on Linux and cannot be opened on Windows; re-enter the secret")
	}
	entropy := opts.Entropy
	if entropy == nil {
		entropy = []byte(LegacyEntropy)
	}
	return &dpapiStore{format: opts.DPAPIFormat, entropy: entropy}, nil
}

// dpapiStore is CurrentUser DPAPI; the sealed string is the whole secret.
type dpapiStore struct {
	format  DPAPIFormat
	entropy []byte
}

func (*dpapiStore) Kind() string        { return BackendDPAPI }
func (*dpapiStore) Delete(string) error { return nil }

func (d *dpapiStore) Seal(plain []byte, _ string) (string, error) {
	if d.format == FormatPowerShellHex {
		if len(plain) == 0 || !utf8.Valid(plain) {
			return "", errors.New("PowerShell DPAPI format needs non-empty UTF-8 text")
		}
		u := utf16.Encode([]rune(string(plain)))
		b := make([]byte, 2*len(u))
		for i, c := range u {
			b[2*i], b[2*i+1] = byte(c), byte(c>>8)
		}
		defer clear(b)
		out, err := dpapi(b, nil, true)
		if err != nil {
			return "", err
		}
		return hex.EncodeToString(out), nil
	}
	out, err := dpapi(plain, d.entropy, true)
	if err != nil {
		return "", err
	}
	return base64.StdEncoding.EncodeToString(out), nil
}

func (d *dpapiStore) Open(sealed string) ([]byte, error) {
	sealed = strings.TrimPrefix(sealed, BackendDPAPI+":")
	if d.format == FormatPowerShellHex {
		b, err := hex.DecodeString(sealed)
		if err != nil || len(b) == 0 {
			return nil, errors.New("invalid DPAPI hex encoding")
		}
		raw, err := dpapi(b, nil, false)
		if err != nil {
			return nil, err
		}
		defer clear(raw)
		if len(raw)%2 != 0 {
			return nil, errors.New("invalid UTF-16 secret")
		}
		u := make([]uint16, len(raw)/2)
		for i := range u {
			u[i] = uint16(raw[2*i]) | uint16(raw[2*i+1])<<8
		}
		defer clear(u)
		return []byte(string(utf16.Decode(u))), nil
	}
	b, err := base64.StdEncoding.DecodeString(sealed)
	if err != nil {
		return nil, errors.New("invalid DPAPI base64 encoding")
	}
	return dpapi(b, d.entropy, false)
}

var emptyByte byte

func blob(b []byte) *windows.DataBlob {
	if len(b) == 0 {
		return &windows.DataBlob{Data: &emptyByte}
	}
	return &windows.DataBlob{Size: uint32(len(b)), Data: &b[0]}
}

func dpapi(in, entropy []byte, protect bool) ([]byte, error) {
	var ent *windows.DataBlob
	if entropy != nil {
		ent = blob(entropy)
	}
	var out windows.DataBlob
	var err error
	if protect {
		err = windows.CryptProtectData(blob(in), nil, ent, 0, nil, windows.CRYPTPROTECT_UI_FORBIDDEN, &out)
	} else {
		err = windows.CryptUnprotectData(blob(in), nil, ent, 0, nil, windows.CRYPTPROTECT_UI_FORBIDDEN, &out)
	}
	runtime.KeepAlive(in)
	runtime.KeepAlive(entropy)
	if err != nil {
		if protect {
			return nil, errors.New("Windows DPAPI encryption failed")
		}
		return nil, errors.New("Windows DPAPI decryption failed; re-enter the secret under this Windows account")
	}
	defer windows.LocalFree(windows.Handle(unsafe.Pointer(out.Data)))
	raw := unsafe.Slice(out.Data, int(out.Size))
	res := append([]byte{}, raw...)
	clear(raw)
	return res, nil
}
