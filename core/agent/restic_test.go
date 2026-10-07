// SPDX-License-Identifier: Apache-2.0

package agent

import (
	"strings"
	"testing"
)

func TestParseResticRun(t *testing.T) {
	ok := []struct {
		args  []string
		input string
	}{
		{[]string{"--repo", "/r", "snapshots", "--json"}, `{"password":"pw"}`},
		{[]string{"-r", "s3:https://x/b", "backup", "--json", "/home"}, `{"password":"pw","env":["AWS_ACCESS_KEY_ID=a","AWS_SECRET_ACCESS_KEY=b"]}`},
		{[]string{"version"}, ``},
		{[]string{"--repo", "/r", "restore", "abc:/home", "--target", "/home"}, `{"password":"pw"}`},
	}
	for _, c := range ok {
		if _, _, err := parseResticRun(c.args, strings.NewReader(c.input)); err != nil {
			t.Errorf("%v: %v", c.args, err)
		}
	}

	bad := []struct {
		args  []string
		input string
		want  string
	}{
		{[]string{"--repo", "/r", "key", "list"}, `{"password":"pw"}`, "not allowed"},
		{[]string{"--repo", "/r", "dump", "latest", "/"}, `{"password":"pw"}`, "not allowed"},
		{[]string{"--password-command", "cat /x", "snapshots"}, `{"password":"pw"}`, "not allowed"},
		{[]string{"--password-file=/tmp/p", "snapshots"}, `{"password":"pw"}`, "not allowed"},
		{[]string{"--insecure-no-password", "init"}, `{}`, "not allowed"},
		{[]string{"--repo", "/r", "snapshots"}, `{}`, "no repository password"},
		{[]string{"--repo", "/r", "snapshots"}, `{"password":"pw","env":["LD_PRELOAD=/x.so"]}`, "LD_PRELOAD"},
		{[]string{"--repo", "/r", "snapshots"}, `{"password":"pw","env":["RESTIC_PASSWORD=x"]}`, "RESTIC_PASSWORD"},
		{[]string{"--repo", "/r", "snapshots"}, `not json`, "parse input"},
		{[]string{"--repo", "sftp:s@h:/r", "snapshots"}, `{"password":"pw","ssh_key":"k"}`, "host key"},
		{[]string{"-o", "sftp.args=-o ProxyCommand=evil", "--repo", "sftp:s@h:/r", "snapshots"}, `{"password":"pw"}`, "not allowed"},
	}
	for _, c := range bad {
		_, _, err := parseResticRun(c.args, strings.NewReader(c.input))
		if err == nil || !strings.Contains(err.Error(), c.want) {
			t.Errorf("%v %s: got %v, want an error containing %q", c.args, c.input, err, c.want)
		}
	}
}
