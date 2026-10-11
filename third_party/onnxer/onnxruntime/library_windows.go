//go:build windows

package onnxruntime

import "golang.org/x/sys/windows"

func openSharedLibrary(path string) (uintptr, error) {
	handle, err := windows.LoadLibraryEx(path, 0, windows.LOAD_LIBRARY_SEARCH_DLL_LOAD_DIR|windows.LOAD_LIBRARY_SEARCH_DEFAULT_DIRS)
	return uintptr(handle), err
}
