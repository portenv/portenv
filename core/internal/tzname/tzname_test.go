// SPDX-License-Identifier: Apache-2.0

package tzname

import (
	"errors"
	"strings"
	"testing"
	"time"
)

func TestValid(t *testing.T) {
	for _, good := range []string{
		"UTC", "Europe/Paris", "America/Argentina/Buenos_Aires", "Etc/GMT+2", "Etc/GMT-14",
		"America/Port-au-Prince", "EST5EDT",
	} {
		if !Valid(good) {
			t.Errorf("Valid(%q) = false, want true", good)
		}
	}
	for _, bad := range []string{
		"", "Local", "local", "/Europe/Paris", "Europe/Paris/", "Europe//Paris",
		"..", "../etc/passwd", "Europe/../../etc/shadow", "Europe/..", "./UTC", "Europe/.",
		"Europe/Paris\n", "Europe/Paris;rm -rf /", "$(id)", "Europe Paris", "Europe\\Paris",
		"Europe/Paris\x00", "Éurope/Paris", ":Europe/Paris", "UTC+2:00",
		strings.Repeat("A", 200),
	} {
		if Valid(bad) {
			t.Errorf("Valid(%q) = true, want false", bad)
		}
	}
}

func TestFromLink(t *testing.T) {
	for target, want := range map[string]string{
		"/var/db/timezone/zoneinfo/Europe/Paris":           "Europe/Paris",
		"/usr/share/zoneinfo/America/New_York":             "America/New_York",
		"../usr/share/zoneinfo/UTC":                        "UTC",
		"/var/db/timezone/tz/2024a.1.0/zoneinfo/Etc/GMT-2": "Etc/GMT-2",
		"/usr/share/zoneinfo/../../etc/passwd":             "",
		"/etc/passwd":                                      "",
		"/usr/share/zoneinfo/":                             "",
		"/usr/share/zoneinfo/Local":                        "",
	} {
		if got := FromLink(target); got != want {
			t.Errorf("FromLink(%q) = %q, want %q", target, got, want)
		}
	}
}

func TestMac(t *testing.T) {
	loads := func(names ...string) func(string) error {
		return func(n string) error {
			for _, ok := range names {
				if n == ok {
					return nil
				}
			}
			return errors.New("unknown time zone " + n)
		}
	}
	cases := []struct {
		name  string
		link  string
		lerr  error
		local string
		load  func(string) error
		want  string
	}{
		{"the localtime link", "/var/db/timezone/zoneinfo/Europe/Paris", nil, "Local", loads("Europe/Paris"), "Europe/Paris"},
		{"no link: time.Local's IANA name", "", errors.New("no link"), "Asia/Tokyo", loads("Asia/Tokyo"), "Asia/Tokyo"},
		{"no link and Local: UTC", "", errors.New("no link"), "Local", loads("UTC"), "UTC"},
		{"a link outside zoneinfo, Local: UTC", "/etc/passwd", nil, "Local", loads(), "UTC"},
		{"a name the tz database doesn't know: UTC", "/usr/share/zoneinfo/Mars/Olympus", nil, "Local", loads("Europe/Paris"), "UTC"},
		{"a traversal in the link: UTC", "/usr/share/zoneinfo/../../../etc/shadow", nil, "Local", loads(), "UTC"},
		{"an unknown link falls to a good Local", "/usr/share/zoneinfo/Mars/Olympus", nil, "Europe/Berlin", loads("Europe/Berlin"), "Europe/Berlin"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := mac(func(string) (string, error) { return c.link, c.lerr }, c.local, c.load)
			if got != c.want {
				t.Fatalf("mac = %q, want %q", got, c.want)
			}
		})
	}
}

// Go's own tz database knows the names Mac() returns.
func TestLoadKnown(t *testing.T) {
	if err := load("Europe/Paris"); err != nil {
		t.Fatalf("load(Europe/Paris): %v", err)
	}
	if err := load("Mars/Olympus"); err == nil {
		t.Fatal("load(Mars/Olympus) succeeded")
	}
	if _, err := time.LoadLocation(Mac()); err != nil {
		t.Fatalf("Mac() = %q, which Go can't load: %v", Mac(), err)
	}
}
