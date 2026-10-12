// SPDX-License-Identifier: Apache-2.0

// Package recovery is the recovery key (ADR 0019): its format, its check
// character, reading a typed key, and deriving each box's recovery password
// with HKDF-SHA256. The Swift copy in PortenvKit runs against the same
// vectors, testdata/vectors.json.
//
// portenvd never imports this package: it receives derived passwords only
// (ADR 0019, test 8). The restore test and the terminal edition use it.
package recovery

import (
	"crypto/hkdf"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"regexp"
	"strings"
)

// Alphabet is the digits 2 to 9 and every letter but I and O: 32
// characters, 5 bits each, with no look-alikes.
const Alphabet = "23456789ABCDEFGHJKLMNPQRSTUVWXYZ"

const (
	// Length is the number of characters: 23 random, then the check.
	Length = 24
	random = Length - 1

	// Salt and InfoPrefix are the HKDF inputs. The info is InfoPrefix, a
	// zero byte, then the box ID.
	Salt       = "portenv recovery key v1"
	InfoPrefix = "portenv restic password v1"
)

// BoxIDPattern is the box ID format the derivation accepts.
var BoxIDPattern = regexp.MustCompile(`^box-[0-9a-f]{12}$`)

// ValidBoxID reports whether id is "box-" and 12 lowercase hex digits.
func ValidBoxID(id string) bool { return BoxIDPattern.MatchString(id) }

// Key is a recovery key. It never prints itself (see Format) and has no
// JSON form, so a log line or a file can't carry it by accident. Grouped
// and Canonical give its text for Print, Copy and the derivation.
type Key struct {
	v     [Length]byte // symbol values, 0 to 31
	valid bool
}

// New makes a key from the system CSPRNG. If that fails, it returns the
// error and never a weaker key.
func New() (Key, error) { return Generate(rand.Reader) }

// Generate makes a key from exactly 23 bytes of r, keeping each byte's low
// 5 bits: uniform symbols, since 256 is a multiple of 32.
func Generate(r io.Reader) (Key, error) {
	var b [random]byte
	defer clear(b[:])
	if _, err := io.ReadFull(r, b[:]); err != nil {
		return Key{}, fmt.Errorf("recovery key: no random bytes: %w", err)
	}
	var k Key
	for i, x := range b {
		k.v[i] = x & 31
	}
	k.v[random] = checkSymbol(k.v[:random])
	k.valid = true
	return k, nil
}

// Canonical is the 24 characters, upper case, without hyphens.
func (k Key) Canonical() string {
	if !k.valid {
		return ""
	}
	var b [Length]byte
	for i, x := range k.v {
		b[i] = Alphabet[x]
	}
	return string(b[:])
}

// Grouped is the key as shown: six groups of four joined by hyphens.
func (k Key) Grouped() string {
	c := k.Canonical()
	if c == "" {
		return ""
	}
	var sb strings.Builder
	for i := 0; i < Length; i += 4 {
		if i > 0 {
			sb.WriteByte('-')
		}
		sb.WriteString(c[i : i+4])
	}
	return sb.String()
}

// Wipe overwrites the key's symbols. A wiped key derives nothing.
func (k *Key) Wipe() {
	clear(k.v[:])
	k.valid = false
}

// Format prints a placeholder for every verb.
func (Key) Format(f fmt.State, _ rune) { _, _ = io.WriteString(f, "[recovery key]") }

// MarshalJSON refuses: a recovery key is never written.
func (Key) MarshalJSON() ([]byte, error) {
	return nil, errors.New("a recovery key is never written")
}

// MarshalText refuses too, for encoders other than JSON.
func (Key) MarshalText() ([]byte, error) {
	return nil, errors.New("a recovery key is never written")
}

// ErrorKind says why a typed key was refused.
type ErrorKind int

const (
	UnknownCharacter ErrorKind = iota + 1
	WrongLength
	Typo
)

// ParseError is a refused typed key. Its message is the one the app shows
// (ADR 0019) and never contains the key.
type ParseError struct {
	Kind      ErrorKind
	Character string // UnknownCharacter: the first one, as typed
	Length    int    // WrongLength: letters and digits read
}

func (e *ParseError) Error() string {
	switch e.Kind {
	case UnknownCharacter:
		return fmt.Sprintf("Recovery keys don't use the character %s.", e.Character)
	case WrongLength:
		return fmt.Sprintf("A recovery key has 24 letters and digits. This one has %d.", e.Length)
	default:
		return "This recovery key has a typo. Check each group against your copy."
	}
}

// Parse reads a typed key. Case doesn't matter and spaces and hyphens are
// ignored. Any other character is refused by name (the first one, as
// typed), then the length is checked, then the check character.
func Parse(s string) (Key, error) {
	var vals []byte
	defer func() { clear(vals) }()
	for _, r := range s {
		if r == ' ' || r == '-' {
			continue
		}
		u := r
		if 'a' <= u && u <= 'z' {
			u -= 'a' - 'A'
		}
		i := strings.IndexRune(Alphabet, u)
		if i < 0 {
			return Key{}, &ParseError{Kind: UnknownCharacter, Character: string(r)}
		}
		vals = append(vals, byte(i)) // #nosec G115 -- i is an index into the 32-character Alphabet
	}
	if len(vals) != Length {
		return Key{}, &ParseError{Kind: WrongLength, Length: len(vals)}
	}
	if syndrome(vals) != 0 {
		return Key{}, &ParseError{Kind: Typo}
	}
	var k Key
	copy(k.v[:], vals)
	k.valid = true
	return k, nil
}

// Password derives the box's restic password: HKDF-SHA256 over the
// canonical key, as 64 lowercase hex characters. The box ID is checked
// against BoxIDPattern first.
func Password(k Key, boxID string) (string, error) {
	if !k.valid {
		return "", errors.New("recovery key: no key")
	}
	if !ValidBoxID(boxID) {
		return "", fmt.Errorf("recovery key: invalid box ID %q", boxID)
	}
	ikm := []byte(k.Canonical())
	defer clear(ikm)
	info := make([]byte, 0, len(InfoPrefix)+1+len(boxID))
	info = append(append(append(info, InfoPrefix...), 0), boxID...)
	out, err := hkdfSHA256(ikm, []byte(Salt), info, 32)
	if err != nil {
		return "", err
	}
	defer clear(out)
	return hex.EncodeToString(out), nil
}

func hkdfSHA256(ikm, salt, info []byte, n int) ([]byte, error) {
	return hkdf.Key(sha256.New, ikm, salt, string(info), n)
}

// GF(2^5) with x^5 + x^2 + 1; α = x = 2 is primitive (order 31).
const poly = 0x25

func gfMul(a, b byte) byte {
	var r byte
	for b != 0 {
		if b&1 != 0 {
			r ^= a
		}
		b >>= 1
		a <<= 1
		if a&0x20 != 0 {
			a ^= poly
		}
	}
	return r
}

// weight is α^(23−i) for position i.
func weight(i int) byte {
	w := byte(1)
	for range Length - 1 - i {
		w = gfMul(w, 2)
	}
	return w
}

// syndrome is Σ α^(23−i) · v_i over the values given, from position 0.
// A key is valid when it's 0 over all 24.
func syndrome(vals []byte) byte {
	var s byte
	for i, x := range vals {
		s ^= gfMul(weight(i), x)
	}
	return s
}

// checkSymbol is v23 = Σ α^(23−i) · v_i for i = 0 … 22 (α^0 = 1 weights
// the check itself, so the full sum is then 0).
func checkSymbol(vals []byte) byte { return syndrome(vals) }
