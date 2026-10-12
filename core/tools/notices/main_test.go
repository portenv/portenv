// SPDX-License-Identifier: Apache-2.0

package main

import (
	"slices"
	"strings"
	"testing"
)

// Downloads are always checked against the checksum database, whatever the
// shell says (R-0110).
func TestSumEnv(t *testing.T) {
	env, err := sumEnv([]string{
		"HOME=/Users/x", "GOFLAGS=-insecure", "GONOSUMDB=*", "GONOSUMCHECK=1", "GOPRIVATE=github.com/*",
		"GOINSECURE=*", "GOWORK=/x/go.work", "GOENV=/x/env", "GOSUMDB=sum.golang.org",
	})
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		"HOME=/Users/x", "GOSUMDB=sum.golang.org", "GOENV=off", "GOWORK=off", "GOFLAGS=-mod=mod",
		"GONOSUMDB=", "GONOSUMCHECK=", "GOPRIVATE=", "GOINSECURE=",
	} {
		if !slices.Contains(env, want) {
			t.Errorf("env lacks %s: %q", want, env)
		}
	}
	for _, kv := range env {
		for _, bad := range []string{"-insecure", "GONOSUMDB=*", "GOPRIVATE=github.com", "/x/"} {
			if strings.Contains(kv, bad) {
				t.Errorf("env keeps %s", kv)
			}
		}
	}
	if _, err := sumEnv([]string{"GOSUMDB=off"}); err == nil || !strings.Contains(err.Error(), "GOSUMDB=off") {
		t.Errorf("GOSUMDB=off: %v", err)
	}
}
