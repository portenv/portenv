// SPDX-License-Identifier: Apache-2.0

package main

import (
	"slices"
	"strings"
	"testing"
)

// Downloads are always checked against Go's checksum database, whatever the
// shell says (R-0110, R-0116).
func TestSumEnv(t *testing.T) {
	env := sumEnv([]string{
		"HOME=/Users/x", "GOFLAGS=-insecure", "GONOSUMDB=*", "GOPRIVATE=github.com/*",
		"GOINSECURE=*", "GOWORK=/x/go.work", "GOENV=/x/env", "GOSUMDB=sum.example.com+key",
		"GOPROXY=https://shell.example.com", "GOMODCACHE=/x/shell-cache",
	}, "https://proxy.example.com,direct", "/Users/me/go/pkg/mod")
	for _, want := range []string{
		"HOME=/Users/x", "GOSUMDB=sum.golang.org", "GOENV=off", "GOWORK=off", "GOFLAGS=-mod=mod",
		"GONOSUMDB=", "GOPRIVATE=", "GOINSECURE=",
		"GOPROXY=https://proxy.example.com,direct", "GOMODCACHE=/Users/me/go/pkg/mod",
	} {
		if !slices.Contains(env, want) {
			t.Errorf("env lacks %s: %q", want, env)
		}
	}
	for _, kv := range env {
		for _, bad := range []string{"-insecure", "GONOSUMDB=*", "GOPRIVATE=github.com", "/x/", "sum.example.com", "shell.example.com", "GONOSUMCHECK"} {
			if strings.Contains(kv, bad) {
				t.Errorf("env keeps %s", kv)
			}
		}
	}
	if off := sumEnv([]string{"GOSUMDB=off"}, "", "/m"); !slices.Contains(off, "GOSUMDB=sum.golang.org") || slices.Contains(off, "GOSUMDB=off") {
		t.Errorf("GOSUMDB=off: %q", off)
	}
}
