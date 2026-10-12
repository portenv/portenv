// SPDX-License-Identifier: Apache-2.0

package recovery

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strings"
	"testing"
)

// vectors is testdata/vectors.json, which the Swift tests read too.
type vectors struct {
	Alphabet     string `json:"alphabet"`
	Salt         string `json:"salt"`
	InfoPrefix   string `json:"info_prefix"`
	BoxIDPattern string `json:"box_id_pattern"`
	RFC5869      struct {
		IKM    string `json:"ikm_hex"`
		Salt   string `json:"salt_hex"`
		Info   string `json:"info_hex"`
		Length int    `json:"length"`
		OKM    string `json:"okm_hex"`
	} `json:"rfc5869_case1"`
	Generate []struct {
		Bytes   string `json:"bytes_hex"`
		Grouped string `json:"grouped"`
	} `json:"generate"`
	Parse []struct {
		Input     string `json:"input"`
		Canonical string `json:"canonical"`
		Error     string `json:"error"`
		Character string `json:"character"`
		Length    int    `json:"length"`
		Message   string `json:"message"`
	} `json:"parse"`
	Derive []struct {
		Recovery string `json:"recovery"`
		BoxID    string `json:"box_id"`
		Derived  string `json:"derived"`
	} `json:"derive"`
	InvalidBoxIDs []string `json:"invalid_box_ids"`
}

func loadVectors(t *testing.T) vectors {
	t.Helper()
	b, err := os.ReadFile("testdata/vectors.json")
	if err != nil {
		t.Fatal(err)
	}
	var v vectors
	if err := json.Unmarshal(b, &v); err != nil {
		t.Fatal(err)
	}
	return v
}

const adrVector = "K7QF-M2XA-9TRD-HV4P-W8LC-3NZ5"

func mustParse(t *testing.T, s string) Key {
	t.Helper()
	k, err := Parse(s)
	if err != nil {
		t.Fatalf("Parse(%q): %v", s, err)
	}
	return k
}

// randomKey is a fresh key from the system CSPRNG.
func randomKey(t *testing.T) Key {
	t.Helper()
	k, err := New()
	if err != nil {
		t.Fatal(err)
	}
	return k
}

func TestAlphabet(t *testing.T) {
	v := loadVectors(t)
	if Alphabet != v.Alphabet {
		t.Fatalf("Alphabet = %q, vectors say %q", Alphabet, v.Alphabet)
	}
	seen := map[rune]bool{}
	for _, c := range Alphabet {
		if seen[c] {
			t.Fatalf("%q twice", c)
		}
		seen[c] = true
	}
	if len(seen) != 32 {
		t.Fatalf("%d characters, want 32", len(seen))
	}
	for _, c := range "01IO" {
		if seen[c] {
			t.Fatalf("the alphabet has the look-alike %q", c)
		}
	}
}

func TestGeneratedKeysAreGroupedAndPassTheirCheck(t *testing.T) {
	for range 1000 {
		k := randomKey(t)
		g := k.Grouped()
		if len(g) != 29 {
			t.Fatalf("grouped length %d", len(g))
		}
		for i, c := range g {
			if (i%5 == 4) != (c == '-') {
				t.Fatalf("not six groups of four: %q", g)
			}
		}
		if got := mustParse(t, g); got.Canonical() != k.Canonical() {
			t.Fatalf("round trip %q -> %q", g, got.Canonical())
		}
	}
}

func TestFormattingAndReadingRoundTrip(t *testing.T) {
	k := mustParse(t, adrVector)
	if k.Grouped() != adrVector || k.Canonical() != strings.ReplaceAll(adrVector, "-", "") {
		t.Fatalf("%q %q", k.Grouped(), k.Canonical())
	}
	for _, s := range []string{
		strings.ToLower(adrVector),
		k.Canonical(),
		strings.ReplaceAll(strings.ToLower(adrVector), "-", " "),
		" " + strings.ReplaceAll(adrVector, "-", "  - ") + " ",
	} {
		if got := mustParse(t, s); got.Grouped() != adrVector {
			t.Fatalf("Parse(%q) = %q", s, got.Grouped())
		}
	}
}

// Generation uses each byte's low 5 bits: a fake byte source gives a known key.
func TestGenerateFromKnownBytes(t *testing.T) {
	v := loadVectors(t)
	for _, g := range v.Generate {
		b, _ := hex.DecodeString(g.Bytes)
		k, err := Generate(bytes.NewReader(b))
		if err != nil {
			t.Fatal(err)
		}
		if k.Grouped() != g.Grouped {
			t.Fatalf("Generate(%s) = %q, want %q", g.Bytes, k.Grouped(), g.Grouped)
		}
	}
	// Exactly 23 bytes are read.
	r := bytes.NewReader(make([]byte, 40))
	if _, err := Generate(r); err != nil || r.Len() != 40-23 {
		t.Fatalf("read %d bytes, err %v", 40-r.Len(), err)
	}
}

// A failing byte source is an error, never a weaker key.
func TestGenerateFailsWithoutRandomBytes(t *testing.T) {
	if _, err := Generate(bytes.NewReader(make([]byte, 22))); err == nil {
		t.Fatal("made a key from 22 bytes")
	}
}

func TestParseVectors(t *testing.T) {
	v := loadVectors(t)
	for _, p := range v.Parse {
		k, err := Parse(p.Input)
		if p.Error == "" {
			if err != nil || k.Canonical() != p.Canonical {
				t.Errorf("Parse(%q) = %q, %v; want %q", p.Input, k.Canonical(), err, p.Canonical)
			}
			continue
		}
		var pe *ParseError
		if !errors.As(err, &pe) {
			t.Errorf("Parse(%q): err %v, want a ParseError", p.Input, err)
			continue
		}
		if err.Error() != p.Message {
			t.Errorf("Parse(%q): %q, want %q", p.Input, err.Error(), p.Message)
		}
		switch p.Error {
		case "character":
			if pe.Kind != UnknownCharacter || pe.Character != p.Character {
				t.Errorf("Parse(%q): %+v", p.Input, pe)
			}
		case "length":
			if pe.Kind != WrongLength || pe.Length != p.Length {
				t.Errorf("Parse(%q): %+v", p.Input, pe)
			}
		case "typo":
			if pe.Kind != Typo {
				t.Errorf("Parse(%q): %+v", p.Input, pe)
			}
		default:
			t.Fatalf("unknown error kind %q in vectors", p.Error)
		}
	}
}

// keysToCheck is the vector key and 1,000 random ones.
func keysToCheck(t *testing.T) []Key {
	ks := []Key{mustParse(t, adrVector)}
	for range 1000 {
		ks = append(ks, randomKey(t))
	}
	return ks
}

func isTypo(err error) bool {
	var pe *ParseError
	return errors.As(err, &pe) && pe.Kind == Typo
}

// Every single wrong character, the check character included, is caught.
func TestCheckCatchesEverySingleChange(t *testing.T) {
	for _, k := range keysToCheck(t) {
		c := []byte(k.Canonical())
		for i := range c {
			orig := c[i]
			for j := range len(Alphabet) {
				if Alphabet[j] == orig {
					continue
				}
				c[i] = Alphabet[j]
				if _, err := Parse(string(c)); !isTypo(err) {
					t.Fatalf("%s: missed %q at %d (err %v)", k.Grouped(), Alphabet[j], i, err)
				}
			}
			c[i] = orig
		}
	}
}

// Every swap of two different characters, adjacent or not, is caught.
func TestCheckCatchesEverySwap(t *testing.T) {
	for _, k := range keysToCheck(t) {
		c := []byte(k.Canonical())
		for i := range c {
			for j := i + 1; j < len(c); j++ {
				if c[i] == c[j] {
					continue
				}
				c[i], c[j] = c[j], c[i]
				if _, err := Parse(string(c)); !isTypo(err) {
					t.Fatalf("%s: missed the swap of %d and %d (err %v)", k.Grouped(), i, j, err)
				}
				c[i], c[j] = c[j], c[i]
			}
		}
	}
}

// Two wrong characters slip through at about 1 in 31 (ADR 0019); measured
// over the vector key's every pair of positions and every pair of changes.
func TestTwoCharacterErrorRate(t *testing.T) {
	c := []byte(mustParse(t, adrVector).Canonical())
	var tried, missed int
	for i := range c {
		for j := i + 1; j < len(c); j++ {
			oi, oj := c[i], c[j]
			for a := range len(Alphabet) {
				if Alphabet[a] == oi {
					continue
				}
				for b := range len(Alphabet) {
					if Alphabet[b] == oj {
						continue
					}
					c[i], c[j] = Alphabet[a], Alphabet[b]
					tried++
					if _, err := Parse(string(c)); err == nil {
						missed++
					} else if !isTypo(err) {
						t.Fatal(err)
					}
				}
			}
			c[i], c[j] = oi, oj
		}
	}
	rate := float64(missed) / float64(tried)
	t.Logf("two-character errors: %d of %d missed (1 in %.1f)", missed, tried, 1/rate)
	if rate < 1.0/33 || rate > 1.0/29 {
		t.Fatalf("missed 1 in %.1f, want about 1 in 31", 1/rate)
	}
}

func TestDeriveVectors(t *testing.T) {
	v := loadVectors(t)
	if Salt != v.Salt || InfoPrefix != v.InfoPrefix || BoxIDPattern.String() != v.BoxIDPattern {
		t.Fatalf("constants differ from the vectors")
	}
	for _, d := range v.Derive {
		got, err := Password(mustParse(t, d.Recovery), d.BoxID)
		if err != nil || got != d.Derived {
			t.Errorf("Password(%s, %s) = %q, %v; want %q", d.Recovery, d.BoxID, got, err, d.Derived)
		}
	}
}

// The ADR's own two vectors are in the file.
func TestADRVectorsAreInTheFile(t *testing.T) {
	v := loadVectors(t)
	want := map[string]string{
		"box-0123456789ab": "b4eef5e9da4b5563a65819e2bd99ec3291f9da036e6688e6d6be20cd120d66af",
		"box-0123456789ac": "32e479fe2dcc4a2fa0b28f9c62f85f88a130de773bf72434019522fa769465e9",
	}
	for _, d := range v.Derive {
		if d.Recovery == adrVector && want[d.BoxID] == d.Derived {
			delete(want, d.BoxID)
		}
	}
	if len(want) != 0 {
		t.Fatalf("missing from the vectors: %v", want)
	}
}

func TestHKDFMatchesRFC5869Case1(t *testing.T) {
	v := loadVectors(t).RFC5869
	ikm, _ := hex.DecodeString(v.IKM)
	salt, _ := hex.DecodeString(v.Salt)
	info, _ := hex.DecodeString(v.Info)
	got, err := hkdfSHA256(ikm, salt, info, v.Length)
	if err != nil || hex.EncodeToString(got) != v.OKM {
		t.Fatalf("got %x, %v", got, err)
	}
}

// Changing the salt, the info's separator or the order of the fields fails
// the vectors.
func TestDerivationMutationsFailTheVectors(t *testing.T) {
	v := loadVectors(t)
	d := v.Derive[0]
	ikm := []byte(strings.ReplaceAll(d.Recovery, "-", ""))
	box := []byte(d.BoxID)
	cat := func(parts ...[]byte) []byte { return bytes.Join(parts, nil) }
	mutants := map[string]func() ([]byte, error){
		"salt": func() ([]byte, error) {
			return hkdfSHA256(ikm, []byte("portenv recovery key v2"), cat([]byte(InfoPrefix), []byte{0}, box), 32)
		},
		"no salt":      func() ([]byte, error) { return hkdfSHA256(ikm, nil, cat([]byte(InfoPrefix), []byte{0}, box), 32) },
		"no separator": func() ([]byte, error) { return hkdfSHA256(ikm, []byte(Salt), cat([]byte(InfoPrefix), box), 32) },
		"space separator": func() ([]byte, error) {
			return hkdfSHA256(ikm, []byte(Salt), cat([]byte(InfoPrefix), []byte(" "), box), 32)
		},
		"box ID first": func() ([]byte, error) {
			return hkdfSHA256(ikm, []byte(Salt), cat(box, []byte{0}, []byte(InfoPrefix)), 32)
		},
		"salt and info": func() ([]byte, error) {
			return hkdfSHA256(ikm, cat([]byte(InfoPrefix), []byte{0}, box), []byte(Salt), 32)
		},
		"hyphens in IKM": func() ([]byte, error) {
			return hkdfSHA256([]byte(d.Recovery), []byte(Salt), cat([]byte(InfoPrefix), []byte{0}, box), 32)
		},
		"no check in IKM": func() ([]byte, error) {
			return hkdfSHA256(ikm[:23], []byte(Salt), cat([]byte(InfoPrefix), []byte{0}, box), 32)
		},
		"lower-case IKM": func() ([]byte, error) {
			return hkdfSHA256(bytes.ToLower(ikm), []byte(Salt), cat([]byte(InfoPrefix), []byte{0}, box), 32)
		},
		"plain SHA-256": func() ([]byte, error) { s := sha256.Sum256(cat(ikm, box)); return s[:], nil },
	}
	right, err := hkdfSHA256(ikm, []byte(Salt), cat([]byte(InfoPrefix), []byte{0}, box), 32)
	if err != nil || hex.EncodeToString(right) != d.Derived {
		t.Fatalf("the unmutated derivation doesn't match: %x %v", right, err)
	}
	for name, f := range mutants {
		got, err := f()
		if err != nil {
			t.Fatal(name, err)
		}
		if hex.EncodeToString(got) == d.Derived {
			t.Errorf("mutation %q still matches the vector", name)
		}
	}
}

func TestDerivationProperties(t *testing.T) {
	k := mustParse(t, adrVector)
	a, _ := Password(k, "box-0123456789ab")
	b, _ := Password(k, "box-0123456789ab")
	if a != b {
		t.Fatal("not deterministic")
	}
	if len(a) != 64 || strings.ToLower(a) != a {
		t.Fatalf("not 64 lowercase hex characters: %q", a)
	}
	if _, err := hex.DecodeString(a); err != nil {
		t.Fatal(err)
	}
	// Box IDs that differ by one character.
	seen := map[string]bool{a: true}
	base := []byte("box-0123456789ab")
	for i := 4; i < len(base); i++ {
		orig := base[i]
		for _, c := range []byte("0123456789abcdef") {
			if c == orig {
				continue
			}
			base[i] = c
			p, err := Password(k, string(base))
			if err != nil {
				t.Fatal(err)
			}
			if seen[p] {
				t.Fatalf("%s repeats a password", base)
			}
			seen[p] = true
		}
		base[i] = orig
	}
	// Different keys.
	for range 100 {
		p, _ := Password(randomKey(t), "box-0123456789ab")
		if p == a {
			t.Fatal("two keys gave one password")
		}
	}
}

func TestInvalidBoxIDsAreRefused(t *testing.T) {
	v := loadVectors(t)
	k := mustParse(t, adrVector)
	for _, id := range v.InvalidBoxIDs {
		if ValidBoxID(id) {
			t.Errorf("ValidBoxID(%q) = true", id)
		}
		if p, err := Password(k, id); err == nil {
			t.Errorf("Password(%q) = %q, want an error", id, p)
		}
	}
	for _, d := range v.Derive {
		if !ValidBoxID(d.BoxID) {
			t.Errorf("ValidBoxID(%q) = false", d.BoxID)
		}
	}
}

// A Key never prints itself: fmt, %v, %+v, %#v and %s show a placeholder,
// so a log line can't carry it by accident.
func TestKeyNeverPrintsItself(t *testing.T) {
	k := mustParse(t, adrVector)
	for _, verb := range []string{"%v", "%+v", "%#v", "%s", "%q", "%x"} {
		out := fmt.Sprintf(verb, k)
		for _, frag := range []string{"K7QF", "k7qf", "3NZ5", "4b37"} {
			if strings.Contains(strings.ToLower(out), strings.ToLower(frag)) {
				t.Fatalf("%s printed the key: %q", verb, out)
			}
		}
	}
	if _, err := json.Marshal(k); err == nil {
		t.Fatal("a Key marshals to JSON")
	}
}

func TestWipe(t *testing.T) {
	k := mustParse(t, adrVector)
	k.Wipe()
	if k.Canonical() == mustParse(t, adrVector).Canonical() {
		t.Fatal("Wipe left the key")
	}
	if _, err := Password(k, "box-0123456789ab"); err == nil {
		t.Fatal("derived from a wiped key")
	}
}
