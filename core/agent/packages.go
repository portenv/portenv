// SPDX-License-Identifier: Apache-2.0

package agent

import (
	"bufio"
	"fmt"
	"io"
	"regexp"
	"strings"
)

// packageRE matches a Debian package name with an optional architecture
// qualifier and version pin: name[:arch][=version]. It never matches
// anything starting with "-", so a line cannot smuggle an option into
// apt-get.
var packageRE = regexp.MustCompile(`^[a-z0-9][a-z0-9+.-]+(:[a-z0-9]+)?(=[A-Za-z0-9.+~:-]+)?$`)

// ParsePackages reads an apt-packages.txt file: one package per line, blank
// lines and "#" comments ignored. Every entry must be a valid package name;
// the first invalid one is an error naming its line.
func ParsePackages(r io.Reader) ([]string, error) {
	var pkgs []string
	seen := map[string]bool{}
	sc := bufio.NewScanner(r)
	for n := 1; sc.Scan(); n++ {
		line := sc.Text()
		if i := strings.IndexByte(line, '#'); i >= 0 {
			line = line[:i]
		}
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		if !packageRE.MatchString(line) {
			return nil, fmt.Errorf("apt-packages.txt line %d: %q is not a package name", n, line)
		}
		if !seen[line] {
			seen[line] = true
			pkgs = append(pkgs, line)
		}
	}
	if err := sc.Err(); err != nil {
		return nil, fmt.Errorf("read apt-packages.txt: %w", err)
	}
	return pkgs, nil
}

// packageName strips an architecture qualifier and version pin.
func packageName(pkg string) string {
	if i := strings.IndexAny(pkg, ":="); i >= 0 {
		return pkg[:i]
	}
	return pkg
}
