// SPDX-License-Identifier: Apache-2.0

//go:build !(darwin && cgo)

package keys

// Default is the platform's key store: a root-only directory on servers.
func Default(dir string) Store { return FileStore{Dir: dir} }
