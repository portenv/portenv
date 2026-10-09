// SPDX-License-Identifier: Apache-2.0

//go:build !(darwin && cgo)

package keys

// Default is the platform's key store: a root-only directory on servers.
func Default(dir string) Store { return FileStore{Dir: dir} }

// AllowPrompts does nothing here: only the Mac's Keychain can prompt, and
// on servers keys live in a root-only directory.
func AllowPrompts() {}
