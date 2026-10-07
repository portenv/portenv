// SPDX-License-Identifier: Apache-2.0

package main

import "testing"

func TestSFTPAddr(t *testing.T) {
	for in, want := range map[string]string{
		"sftp:portenv-storage@52.47.201.32:/storage/boxes/b":            "52.47.201.32:22",
		"sftp:portenv-storage@127.0.0.1:/storage/boxes/b":               "127.0.0.1:22",
		"sftp://portenv-storage@127.0.0.1:2222//storage/boxes/b":        "127.0.0.1:2222",
		"sftp://portenv-storage@host.portenv.internal//storage/boxes/b": "host.portenv.internal:22",
	} {
		if got := sftpAddr(in); got != want {
			t.Errorf("sftpAddr(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestHostRepoRewritesTheBoxHostAddress(t *testing.T) {
	s := &session{cfg: boxConfig{ID: "b", Storage: "sftp://portenv-storage@host.portenv.internal:2222//storage"},
		repo: "sftp://portenv-storage@host.portenv.internal:2222//storage/boxes/b"}
	if got, want := s.hostRepo(), "sftp://portenv-storage@127.0.0.1:2222//storage/boxes/b"; got != want {
		t.Fatalf("hostRepo = %q, want %q", got, want)
	}
}

func TestFromRegistry(t *testing.T) {
	for ref, want := range map[string]bool{
		"ghcr.io/portenv/toolbox-node:main":           true,
		"ghcr.io/portenv/toolbox-node@sha256:abcd":    true,
		"localhost:5000/toolbox:dev":                  true,
		"portenv/toolbox-node:dev":                    false,
		"portenv/toolbox-node:dev@sha256:89cb7465493": false,
		"toolbox": false,
	} {
		if got := fromRegistry(ref); got != want {
			t.Errorf("fromRegistry(%q) = %v, want %v", ref, got, want)
		}
	}
}
