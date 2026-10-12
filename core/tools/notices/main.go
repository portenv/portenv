// SPDX-License-Identifier: Apache-2.0

// Command notices builds and checks Portenv.app's third-party notices
// (R-0096; core/internal/notices). It is a build tool, run by `gmake app` and
// scripts/release-local.sh; it never ships in the app.
//
//	notices generate --app APP --resolved Package.resolved --checkouts DIR [--allowlist FILE] BINARY...
//	notices check --app APP --resolved Package.resolved
//
// generate reads the Go modules from each BINARY, finds each module's source
// in the module cache (`go mod download`, always checked against the checksum
// database, at the exact version) and each Swift pin's checkout, copies their
// licence files into the app and writes ThirdPartyNotices.txt. check confirms that an app's notices
// cover every Go binary in Contents/Helpers and every Swift pin.
package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"

	"github.com/portenv/portenv/core/internal/notices"
)

func main() {
	if err := run(os.Args[1:]); err != nil {
		fmt.Fprintln(os.Stderr, "notices:", err)
		os.Exit(1)
	}
}

func run(args []string) error {
	if len(args) == 0 {
		return errors.New("usage: notices generate|check --app APP --resolved Package.resolved")
	}
	fs := flag.NewFlagSet(args[0], flag.ContinueOnError)
	app := fs.String("app", "", "the Portenv.app bundle")
	resolved := fs.String("resolved", "", "apps/mac/Package.resolved")
	checkouts := fs.String("checkouts", "", "SwiftPM's checkouts folder for this build (generate)")
	allowlist := fs.String("allowlist", "", "the reviewed allowlist (generate)")
	if err := fs.Parse(args[1:]); err != nil {
		return err
	}
	if *app == "" || *resolved == "" {
		return errors.New("--app and --resolved are required")
	}
	pins, err := notices.SwiftPins(*resolved)
	if err != nil {
		return err
	}
	switch args[0] {
	case "generate":
		if *checkouts == "" || fs.NArg() == 0 {
			return errors.New("generate needs --checkouts and the Go binaries")
		}
		return generate(*app, pins, *checkouts, *allowlist, fs.Args())
	case "check":
		return check(*app, pins)
	}
	return fmt.Errorf("unknown command %q", args[0])
}

func generate(app string, pins []notices.SwiftPin, checkouts, allowFile string, binaries []string) error {
	goComps, err := notices.GoComponents(binaries)
	if err != nil {
		return err
	}
	allow, err := notices.LoadAllowlist(allowFile)
	if err != nil {
		return err
	}
	modDirs, err := downloadModules(goComps)
	if err != nil {
		return err
	}
	goroot, err := goStd(goComps)
	if err != nil {
		return err
	}
	byPin := map[notices.Component]notices.SwiftPin{}
	comps := goComps
	for _, p := range pins {
		byPin[p.Component()] = p
		comps = append(comps, p.Component())
	}
	src := func(c notices.Component) (string, error) {
		switch c.Kind {
		case notices.KindGoStd:
			return goroot, nil
		case notices.KindGo:
			if d := modDirs[c.Name+"@"+c.Version]; d != "" {
				return d, nil
			}
			return "", errors.New("not in the module cache")
		default:
			return swiftCheckout(checkouts, byPin[c])
		}
	}
	origin := func(c notices.Component) string {
		if c.Kind == notices.KindSwift {
			return byPin[c].Origin()
		}
		return notices.GoOrigin(c)
	}
	entries, err := notices.Collect(comps, src, origin, allow)
	if err != nil {
		return err
	}
	if err := notices.Write(app, entries); err != nil {
		return err
	}
	fmt.Printf("third-party notices: %d components (%d Go modules and the standard library, %d Swift packages)\n",
		len(entries), len(goComps)-countKind(goComps, notices.KindGoStd), len(pins))
	return nil
}

func check(app string, pins []notices.SwiftPin) error {
	helpers := filepath.Join(app, "Contents", "Helpers")
	entries, err := os.ReadDir(helpers)
	if err != nil {
		return err
	}
	var binaries []string
	for _, e := range entries {
		if e.Type().IsRegular() {
			binaries = append(binaries, filepath.Join(helpers, e.Name()))
		}
	}
	if len(binaries) == 0 {
		return fmt.Errorf("%s has no helpers", helpers)
	}
	comps, err := notices.GoComponents(binaries)
	if err != nil {
		return err
	}
	for _, p := range pins {
		comps = append(comps, p.Component())
	}
	if err := notices.Check(app, comps); err != nil {
		return err
	}
	fmt.Printf("third-party notices cover %d components\n", len(comps))
	return nil
}

func countKind(cs []notices.Component, kind string) int {
	n := 0
	for _, c := range cs {
		if c.Kind == kind {
			n++
		}
	}
	return n
}

// downloadModules runs one `go mod download -json` for every module, outside
// any workspace, and returns each module's folder in the module cache. The go
// command checks every download against the checksum database, whatever this
// shell or the go env file says (sumEnv).
func downloadModules(comps []notices.Component) (map[string]string, error) {
	env, err := sumEnv(os.Environ())
	if err != nil {
		return nil, err
	}
	var mods []string
	for _, c := range comps {
		if c.Kind == notices.KindGo {
			mods = append(mods, c.Name+"@"+c.Version)
		}
	}
	sort.Strings(mods)
	tmp, err := os.MkdirTemp("", "notices")
	if err != nil {
		return nil, err
	}
	defer func() { _ = os.RemoveAll(tmp) }()
	cmd := exec.Command("go", append([]string{"mod", "download", "-json"}, mods...)...) // #nosec G204 -- module paths and versions read from the binaries' build information
	cmd.Dir = tmp
	cmd.Env = env
	var out, errOut bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &errOut
	runErr := cmd.Run()
	dirs := map[string]string{}
	var failed []string
	dec := json.NewDecoder(&out)
	for {
		var m struct{ Path, Version, Dir, Error string }
		if err := dec.Decode(&m); errors.Is(err, io.EOF) {
			break
		} else if err != nil {
			return nil, fmt.Errorf("go mod download: %w", err)
		}
		if m.Error != "" {
			failed = append(failed, m.Error)
			continue
		}
		dirs[m.Path+"@"+m.Version] = m.Dir
	}
	if len(failed) > 0 {
		return nil, fmt.Errorf("go mod download: %s", strings.Join(failed, "; "))
	}
	if runErr != nil {
		return nil, fmt.Errorf("go mod download: %w: %s", runErr, strings.TrimSpace(errOut.String()))
	}
	return dirs, nil
}

// sumEnv is the go command's environment for downloads that are always
// checked against the checksum database: no workspace, the go env file left
// out (it could exempt modules), and no variable that skips or exempts the
// check. It refuses GOSUMDB=off rather than quietly turning it back on.
func sumEnv(environ []string) ([]string, error) {
	override := map[string]string{
		"GOENV":        "off",
		"GOWORK":       "off",
		"GOFLAGS":      "-mod=mod",
		"GONOSUMDB":    "",
		"GONOSUMCHECK": "",
		"GOPRIVATE":    "",
		"GOINSECURE":   "",
	}
	var out []string
	for _, kv := range environ {
		k, v, _ := strings.Cut(kv, "=")
		if k == "GOSUMDB" && strings.TrimSpace(v) == "off" {
			return nil, errors.New("GOSUMDB=off: the notices need modules checked against the checksum database; unset it")
		}
		if _, ok := override[k]; !ok {
			out = append(out, kv)
		}
	}
	keys := make([]string, 0, len(override))
	for k := range override {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		out = append(out, k+"="+override[k])
	}
	return out, nil
}

// goStd is the folder with Go's own licence: this machine's GOROOT. Go's
// licence is the same text in every release, so a helper built with another
// Go 1.x release is covered by it; the warning says so, since that helper
// was built with an older toolchain than the others.
func goStd(comps []notices.Component) (string, error) {
	out, err := exec.Command("go", "env", "GOROOT", "GOVERSION").Output()
	if err != nil {
		return "", fmt.Errorf("go env: %w", err)
	}
	f := strings.Fields(string(out))
	if len(f) != 2 {
		return "", fmt.Errorf("go env: unexpected output %q", out)
	}
	for _, c := range comps {
		if c.Kind == notices.KindGoStd && c.Version != f[1] {
			fmt.Fprintf(os.Stderr, "notices: warning: a helper was built with %s, this machine's go is %s (Go's licence is taken from %s)\n", c.Version, f[1], f[1])
		}
	}
	return f[0], nil
}

// swiftCheckout is the pin's checkout, which must be at the pinned revision.
func swiftCheckout(checkouts string, p notices.SwiftPin) (string, error) {
	dir := filepath.Join(checkouts, p.CheckoutName())
	out, err := exec.Command("git", "-C", dir, "rev-parse", "HEAD").Output() // #nosec G204 G702 -- fixed git command; the folder is a checkout under this build's checkouts
	if err != nil {
		return "", fmt.Errorf("no checkout at %s: build the app first", dir)
	}
	if got := strings.TrimSpace(string(out)); p.Revision != "" && got != p.Revision {
		return "", fmt.Errorf("checkout at %s, but Package.resolved pins %s", got, p.Revision)
	}
	return dir, nil
}
