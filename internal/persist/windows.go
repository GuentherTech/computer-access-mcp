//go:build windows

package persist

import (
	"bytes"
	"errors"
	"os"
	"unsafe"

	"golang.org/x/sys/windows"
)

var signature = []byte("CAMCP-DPAPI-1\n")

func seal(data []byte) ([]byte, error) {
	in := windows.DataBlob{Size: uint32(len(data)), Data: &data[0]}
	var out windows.DataBlob
	if err := windows.CryptProtectData(&in, nil, nil, 0, nil, windows.CRYPTPROTECT_UI_FORBIDDEN, &out); err != nil {
		return nil, err
	}
	defer windows.LocalFree(windows.Handle(unsafe.Pointer(out.Data)))
	return append(append([]byte{}, signature...), unsafe.Slice(out.Data, out.Size)...), nil
}

func unseal(data []byte) ([]byte, error) {
	if !bytes.HasPrefix(data, signature) || len(data) <= len(signature) {
		return nil, errors.New("invalid encrypted config")
	}
	data = data[len(signature):]
	in := windows.DataBlob{Size: uint32(len(data)), Data: &data[0]}
	var out windows.DataBlob
	if err := windows.CryptUnprotectData(&in, nil, nil, 0, nil, windows.CRYPTPROTECT_UI_FORBIDDEN, &out); err != nil {
		return nil, err
	}
	defer windows.LocalFree(windows.Handle(unsafe.Pointer(out.Data)))
	return append([]byte{}, unsafe.Slice(out.Data, out.Size)...), nil
}

func lockFile(f *os.File) error {
	return windows.LockFileEx(windows.Handle(f.Fd()), windows.LOCKFILE_EXCLUSIVE_LOCK|windows.LOCKFILE_FAIL_IMMEDIATELY, 0, 1, 0, &windows.Overlapped{})
}
