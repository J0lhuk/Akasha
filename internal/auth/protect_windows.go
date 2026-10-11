//go:build windows

package auth

import (
	"errors"
	"unsafe"

	"golang.org/x/sys/windows"
)

// DPAPI binds the local bearer token to the Windows user. This file is not a
// portable cloud credential; transferable project keys use passphrase wrapping.
func transform(data []byte, encrypt bool) ([]byte, error) {
	if len(data) == 0 || len(data) > 65536 {
		return nil, errors.New("invalid protected token length")
	}
	in := windows.DataBlob{Size: uint32(len(data)), Data: &data[0]}
	var out windows.DataBlob
	var err error
	if encrypt {
		err = windows.CryptProtectData(&in, nil, nil, 0, nil, windows.CRYPTPROTECT_UI_FORBIDDEN, &out)
	} else {
		err = windows.CryptUnprotectData(&in, nil, nil, 0, nil, windows.CRYPTPROTECT_UI_FORBIDDEN, &out)
	}
	if err != nil {
		return nil, err
	}
	defer windows.LocalFree(windows.Handle(unsafe.Pointer(out.Data)))
	return append([]byte(nil), unsafe.Slice(out.Data, out.Size)...), nil
}
func protect(data []byte) ([]byte, error)   { return transform(data, true) }
func unprotect(data []byte) ([]byte, error) { return transform(data, false) }
