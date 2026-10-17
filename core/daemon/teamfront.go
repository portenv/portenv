// SPDX-License-Identifier: Apache-2.0

package daemon

import (
	"errors"
	"fmt"
	"regexp"
	"strings"
)

// An agent file's frontmatter is a small, strict part of YAML (ADR 0022
// §3): flat "key: value" lines, one nested map (grant) on indented lines,
// lists written inline as [a, b], # comments, and plain, 'single' or
// "double" quoted values. Anything else is a problem line, never a guess:
// the file decides what runs in the owner's workspace.

type fmKind int

const (
	fmEmpty  fmKind = iota // "key:" with nothing after it
	fmScalar               // a value
	fmList                 // [a, b]
	fmMap                  // grant's indented lines
	fmBad                  // a value that didn't parse; its problem is already said
)

type fmValue struct {
	kind   fmKind
	quoted bool
	s      string
	list   []string
	sub    *fmKeys
}

// fmKeys keeps keys in the order the file gives them, so problems come in
// the file's order.
type fmKeys struct {
	order []string
	vals  map[string]*fmValue
}

func (k *fmKeys) add(key string, v *fmValue) bool {
	if k.vals == nil {
		k.vals = map[string]*fmValue{}
	}
	if _, dup := k.vals[key]; dup {
		return false
	}
	k.order = append(k.order, key)
	k.vals[key] = v
	return true
}

type frontmatter struct {
	keys fmKeys
	body string
}

var fmKeyRE = regexp.MustCompile(`^[a-z_]+$`)

// parseFrontmatter reads content, which is valid UTF-8. ok is false when
// there is no frontmatter to read at all. Each problem is the text of a
// plain line, without the agent's name.
func parseFrontmatter(content string) (fm frontmatter, problems []string, ok bool) {
	addf := func(format string, a ...any) { problems = append(problems, fmt.Sprintf(format, a...)) }
	lines := strings.SplitAfter(content, "\n")
	if trimEOL(lines[0]) != "---" {
		addf("the file must start with a --- line.")
		return fm, problems, false
	}
	end := -1
	for i := 1; i < len(lines); i++ {
		if trimEOL(lines[i]) == "---" {
			end = i
			break
		}
	}
	if end < 0 {
		addf("the frontmatter has no closing --- line.")
		return fm, problems, false
	}
	fm.body = strings.Join(lines[end+1:], "")

	var grant *fmKeys // set while grant's indented lines may follow
	subIndent := 0
	for i := 1; i < end; i++ {
		n := i + 1
		raw := trimEOL(lines[i])
		if strings.TrimSpace(raw) == "" {
			continue
		}
		if strings.Contains(raw, "\t") {
			addf("line %d has a tab; use spaces.", n)
			continue
		}
		text := strings.TrimLeft(raw, " ")
		indent := len(raw) - len(text)
		if text[0] == '#' {
			continue
		}
		if indent > 0 && grant == nil {
			addf("line %d is indented, but only grant takes indented settings.", n)
			continue
		}
		key, rest, found := strings.Cut(text, ":")
		if !found || !fmKeyRE.MatchString(key) || (rest != "" && rest[0] != ' ') {
			addf("line %d isn't in the form \"key: value\".", n)
			if indent == 0 {
				grant = nil
			}
			continue
		}
		v, problem := parseFMValue(rest)
		if problem != "" {
			addf("line %d %s", n, problem)
			v = &fmValue{kind: fmBad}
		}
		if indent == 0 {
			grant = nil
			if key == "grant" && v.kind == fmEmpty {
				v = &fmValue{kind: fmMap, sub: &fmKeys{}}
				grant, subIndent = v.sub, 0
			}
			if !fm.keys.add(key, v) {
				addf("%s is set twice.", key)
			}
			continue
		}
		if subIndent == 0 {
			subIndent = indent
		} else if indent != subIndent {
			addf("line %d's indentation doesn't match grant's other settings.", n)
			continue
		}
		if !grant.add(key, v) {
			addf("grant.%s is set twice.", key)
		}
	}
	return fm, problems, true
}

func trimEOL(s string) string {
	return strings.TrimSuffix(strings.TrimSuffix(s, "\n"), "\r")
}

const fmUnsupported = `uses YAML an agent file doesn't take; use "key: value", or a list in [ ].`

// parseFMValue reads what follows a key's colon. A problem is the rest of
// a sentence that starts "line N".
func parseFMValue(s string) (*fmValue, string) {
	s = strings.Trim(s, " ")
	if s == "" || s[0] == '#' {
		return &fmValue{kind: fmEmpty}, ""
	}
	switch c := s[0]; {
	case c == '[':
		return parseFMList(s)
	case c == '"' || c == '\'':
		v, n, problem := readFMQuoted(s)
		if problem != "" {
			return nil, problem
		}
		if !onlyComment(s[n:]) {
			return nil, "has text after a quoted value."
		}
		return &fmValue{kind: fmScalar, quoted: true, s: v}, ""
	case strings.ContainsRune("{}]&*!|>%@`?,", rune(c)) || s == "-" || strings.HasPrefix(s, "- "):
		return nil, fmUnsupported
	}
	if i := strings.Index(s, " #"); i >= 0 {
		s = strings.TrimRight(s[:i], " ")
	}
	if strings.Contains(s, ": ") || strings.HasSuffix(s, ":") {
		return nil, `has ": " in a value; put the value in quotes.`
	}
	return &fmValue{kind: fmScalar, s: s}, ""
}

// onlyComment: nothing after a value but spaces and a comment.
func onlyComment(rest string) bool {
	if rest == "" {
		return true
	}
	t := strings.TrimLeft(rest, " ")
	return t == "" || (t[0] == '#' && len(t) < len(rest))
}

// readFMQuoted reads the quoted value at the start of s and how many bytes
// it took. Double quotes take only \" and \\ as escapes.
func readFMQuoted(s string) (string, int, string) {
	q := s[0]
	var b strings.Builder
	for i := 1; i < len(s); i++ {
		c := s[i]
		switch {
		case q == '\'' && c == '\'':
			if i+1 < len(s) && s[i+1] == '\'' {
				b.WriteByte('\'')
				i++
				continue
			}
			return b.String(), i + 1, ""
		case q == '"' && c == '"':
			return b.String(), i + 1, ""
		case q == '"' && c == '\\':
			if i+1 < len(s) && (s[i+1] == '"' || s[i+1] == '\\') {
				b.WriteByte(s[i+1])
				i++
				continue
			}
			return "", 0, `has an escape an agent file doesn't take; only \" and \\ are.`
		default:
			b.WriteByte(c)
		}
	}
	return "", 0, "has an unclosed quote."
}

func parseFMList(s string) (*fmValue, string) {
	v := &fmValue{kind: fmList}
	i := 1
	skip := func() {
		for i < len(s) && s[i] == ' ' {
			i++
		}
	}
	for {
		skip()
		if i >= len(s) {
			return nil, "has a list with no closing ]."
		}
		if s[i] == ']' {
			if len(v.list) > 0 {
				return nil, "has an empty item in a list."
			}
			i++
			break
		}
		if s[i] == ',' {
			return nil, "has an empty item in a list."
		}
		var item string
		if s[i] == '"' || s[i] == '\'' {
			it, n, problem := readFMQuoted(s[i:])
			if problem != "" {
				return nil, problem
			}
			item, i = it, i+n
		} else {
			j := strings.IndexAny(s[i:], ",]")
			if j < 0 {
				return nil, "has a list with no closing ]."
			}
			item = strings.TrimRight(s[i:i+j], " ")
			if strings.ContainsAny(item, "[{}#\"'") || strings.Contains(item, ": ") || strings.ContainsRune("&*!|>%@`?-", rune(item[0])) {
				return nil, fmUnsupported
			}
			i += j
		}
		v.list = append(v.list, item)
		skip()
		if i >= len(s) {
			return nil, "has a list with no closing ]."
		}
		if s[i] == ',' {
			i++
			continue
		}
		if s[i] == ']' {
			i++
			break
		}
		return nil, fmUnsupported
	}
	if !onlyComment(s[i:]) {
		return nil, "has text after a list."
	}
	return v, ""
}

var (
	errArgvEmpty      = errors.New("empty")
	errArgvQuote      = errors.New("unbalanced quote")
	errArgvBackslash  = errors.New("lone backslash")
	argvDoubleEscapes = "\"\\$`\n"
)

// splitArgv splits a runner line into words the way a POSIX shell would
// for quotes and backslashes, and does nothing else: no variables, globs,
// pipes or redirections. The words are an argv, never run through a
// shell, so "a; b" is three words.
func splitArgv(s string) ([]string, error) {
	var args []string
	var cur strings.Builder
	word := false
	for i := 0; i < len(s); {
		switch s[i] {
		case ' ', '\t', '\n':
			if word {
				args = append(args, cur.String())
				cur.Reset()
				word = false
			}
			i++
		case '\'':
			j := strings.IndexByte(s[i+1:], '\'')
			if j < 0 {
				return nil, errArgvQuote
			}
			cur.WriteString(s[i+1 : i+1+j])
			word = true
			i += j + 2
		case '"':
			word = true
			i++
			for {
				if i >= len(s) {
					return nil, errArgvQuote
				}
				if s[i] == '"' {
					i++
					break
				}
				if s[i] == '\\' && i+1 < len(s) && strings.IndexByte(argvDoubleEscapes, s[i+1]) >= 0 {
					cur.WriteByte(s[i+1])
					i += 2
					continue
				}
				cur.WriteByte(s[i])
				i++
			}
		case '\\':
			if i+1 >= len(s) {
				return nil, errArgvBackslash
			}
			cur.WriteByte(s[i+1])
			word = true
			i += 2
		default:
			cur.WriteByte(s[i])
			word = true
			i++
		}
	}
	if word {
		args = append(args, cur.String())
	}
	if len(args) == 0 {
		return nil, errArgvEmpty
	}
	return args, nil
}
