// SPDX-License-Identifier: Apache-2.0

//go:build !darwin

package daemon

// Away from a Mac there's no Time Machine to exclude the store from.
func excludeFromBackup(string) error { return nil }

func excludedFromBackup(string) bool { return true }
