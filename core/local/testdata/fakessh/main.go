// SPDX-License-Identifier: Apache-2.0

// fakessh stands in for ssh in the host-key tests: it resolves aliases with
// -G, checks and records host keys in UserKnownHostsFile as ssh does
// (HostKeyAlias, StrictHostKeyChecking, HostKeyAlgorithms), and runs
// nothing remote. FAKESSH_HOSTS maps an alias to host:port; FAKESSH_KEYS
// is a folder with one file per host:port holding "type base64";
// FAKESSH_LOG, when set, gets each run's arguments.
package main

import (
	"bufio"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

func main() {
	if log := os.Getenv("FAKESSH_LOG"); log != "" {
		if f, err := os.OpenFile(log, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600); err == nil {
			fmt.Fprintln(f, strings.Join(os.Args[1:], " "))
			_ = f.Close()
		}
	}
	opts := map[string]string{}
	var g bool
	var rest []string
	args := os.Args[1:]
	for i := 0; i < len(args); i++ {
		switch a := args[i]; {
		case a == "-o" && i+1 < len(args):
			k, v, _ := strings.Cut(args[i+1], "=")
			if _, seen := opts[strings.ToLower(k)]; !seen { // ssh keeps the first value
				opts[strings.ToLower(k)] = v
			}
			i++
		case a == "-G":
			g = true
		case a == "-F" && i+1 < len(args):
			i++
		case a == "--":
		default:
			rest = append(rest, args[i:]...)
			i = len(args)
		}
	}
	if len(rest) == 0 {
		fmt.Fprintln(os.Stderr, "usage: fakessh host [command]")
		os.Exit(255)
	}
	target := rest[0]
	host := target
	if at := strings.LastIndex(host, "@"); at >= 0 {
		host = host[at+1:]
	}
	addr := host + ":22"
	for _, m := range strings.Split(os.Getenv("FAKESSH_HOSTS"), ",") {
		if k, v, ok := strings.Cut(m, "="); ok && k == host {
			addr = v
		}
	}
	hostname, port, _ := strings.Cut(addr, ":")
	if g {
		fmt.Printf("hostname %s\nport %s\n", hostname, port)
		return
	}
	keyb, err := os.ReadFile(filepath.Join(os.Getenv("FAKESSH_KEYS"), addr))
	if err != nil {
		fmt.Fprintf(os.Stderr, "ssh: connect to host %s port %s: Connection refused\n", hostname, port)
		os.Exit(255)
	}
	key := strings.TrimSpace(string(keyb))
	ktype, _, _ := strings.Cut(key, " ")
	if algs := opts["hostkeyalgorithms"]; algs != "" {
		ok := false
		for _, a := range strings.Split(algs, ",") {
			if a == ktype || (ktype == "ssh-rsa" && strings.HasPrefix(a, "rsa-sha2-")) {
				ok = true
			}
		}
		if !ok {
			fmt.Fprintf(os.Stderr, "Unable to negotiate with %s port %s: no matching host key type found. Their offer: %s\n", hostname, port, ktype)
			os.Exit(255)
		}
	}
	alias := hostname
	if port != "22" {
		alias = "[" + hostname + "]:" + port
	}
	if a := opts["hostkeyalias"]; a != "" {
		alias = a
	}
	khFile := opts["userknownhostsfile"]
	var known string
	if f, err := os.Open(khFile); err == nil {
		sc := bufio.NewScanner(f)
		for sc.Scan() {
			name, k, ok := strings.Cut(sc.Text(), " ")
			if ok && name == alias {
				known = k
			}
		}
		_ = f.Close()
	}
	switch {
	case opts["stricthostkeychecking"] == "no": // checks nothing
	case known != "" && known != key:
		fmt.Fprintln(os.Stderr, "@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@\n@    WARNING: REMOTE HOST IDENTIFICATION HAS CHANGED!     @\n@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@\nHost key verification failed.")
		os.Exit(255)
	case known == "" && opts["stricthostkeychecking"] == "accept-new":
		f, err := os.OpenFile(khFile, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
		if err == nil {
			fmt.Fprintf(f, "%s %s\n", alias, key)
			_ = f.Close()
		}
	case known == "":
		fmt.Fprintf(os.Stderr, "No host key is known for %s and you have requested strict checking.\nHost key verification failed.\n", alias)
		os.Exit(255)
	}
	if opts["preferredauthentications"] == "none" {
		fmt.Fprintf(os.Stderr, "%s: Permission denied (publickey).\n", target)
		os.Exit(255)
	}
	if len(rest) > 1 {
		fmt.Printf("ran on %s: %s\n", addr, strings.Join(rest[1:], " "))
	}
}
