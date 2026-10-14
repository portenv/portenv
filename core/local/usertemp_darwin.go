// SPDX-License-Identifier: Apache-2.0

//go:build darwin && cgo

package local

/*
#include <unistd.h>
*/
import "C"

import (
	"errors"
	"path/filepath"
	"unsafe"
)

// userTempDir is the per-user temp folder, from confstr, never the TMPDIR
// variable, which a process's environment can point anywhere (ADR 0021).
func userTempDir() (string, error) {
	n := C.confstr(C._CS_DARWIN_USER_TEMP_DIR, nil, 0)
	if n == 0 {
		return "", errors.New("the per-user temp folder isn't available")
	}
	buf := make([]byte, n)
	if C.confstr(C._CS_DARWIN_USER_TEMP_DIR, (*C.char)(unsafe.Pointer(&buf[0])), n) == 0 {
		return "", errors.New("the per-user temp folder isn't available")
	}
	return string(buf[:n-1]), nil
}

// socketDirPath: pv in the per-user temp folder.
func socketDirPath() (string, error) {
	t, err := userTempDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(t, "pv"), nil
}
