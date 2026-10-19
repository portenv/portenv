// SPDX-License-Identifier: Apache-2.0

package daemon

import (
	"archive/zip"
	"bytes"
	"fmt"
	"image"
	"image/gif"
	"image/jpeg"
	"image/png"
	"io"
	"path"
	"strconv"
	"strings"
	"unicode"
	"unicode/utf8"

	"golang.org/x/image/webp"
	"golang.org/x/text/unicode/norm"

	"github.com/portenv/portenv/core/internal/teamkit"
)

// The type check (ADR 0030 point 4): from the content, never the name.
// Images by their signature and header only (image.DecodeConfig reads no
// pixels); a PDF by its first and last bytes (the real parse is the
// preview service's); text as valid UTF-8 with no control characters; a
// .docx (from the owner only) by its ZIP central directory, never
// unpacked. Anything else is refused.

const docxMediaType = "application/vnd.openxmlformats-officedocument.wordprocessingml.document"

// attachType is what the content is.
type attachType struct {
	mediaType string
	ext       string
	width     int32
	height    int32
}

// typeLine is the refusal for a type that isn't allowed.
func typeLine(name string, inbound bool) string {
	if inbound {
		return name + " isn't a PDF, PNG, JPEG, GIF, WebP, Markdown, text or Word file. Attach one of those."
	}
	return name + " isn't a PDF, PNG, JPEG, GIF, WebP, Markdown or text file. Attach one of those."
}

// thousands writes n with commas: 40,000.
func thousands(n int) string {
	s := strconv.Itoa(n)
	for i := len(s) - 3; i > 0; i -= 3 {
		s = s[:i] + "," + s[i:]
	}
	return s
}

// sniffAttachment finds the type of size bytes in r; name is the file's
// name as it was given, for the lines. It returns the type, or the plain
// line refusing it.
func sniffAttachment(r io.ReaderAt, size int64, name string, inbound bool) (attachType, string) {
	head := make([]byte, min(size, 1024))
	if _, err := r.ReadAt(head, 0); err != nil && err != io.EOF {
		return attachType{}, typeLine(name, inbound)
	}
	sr := io.NewSectionReader(r, 0, size)
	img := func(mt, ext string, decode func(io.Reader) (image.Config, error)) (attachType, string) {
		c, err := decode(sr)
		if err != nil || c.Width <= 0 || c.Height <= 0 {
			return attachType{}, typeLine(name, inbound)
		}
		switch {
		case c.Width > teamkit.MaxImageSide:
			return attachType{}, fmt.Sprintf("%s is %s px wide; images can be at most %s px a side.", name, thousands(c.Width), thousands(teamkit.MaxImageSide))
		case c.Height > teamkit.MaxImageSide:
			return attachType{}, fmt.Sprintf("%s is %s px tall; images can be at most %s px a side.", name, thousands(c.Height), thousands(teamkit.MaxImageSide))
		case int64(c.Width)*int64(c.Height) > teamkit.MaxImagePixels:
			return attachType{}, fmt.Sprintf("%s is %d megapixels; images can be at most %d megapixels.", name, int64(c.Width)*int64(c.Height)/1_000_000, teamkit.MaxImagePixels/1_000_000)
		}
		return attachType{mediaType: mt, ext: ext, width: int32(c.Width), height: int32(c.Height)}, "" // #nosec G115 -- at most 16,384
	}
	switch {
	case bytes.HasPrefix(head, []byte("\x89PNG\r\n\x1a\n")):
		return img("image/png", ".png", png.DecodeConfig)
	case bytes.HasPrefix(head, []byte("\xff\xd8\xff")):
		return img("image/jpeg", ".jpg", jpeg.DecodeConfig)
	case bytes.HasPrefix(head, []byte("GIF87a")), bytes.HasPrefix(head, []byte("GIF89a")):
		return img("image/gif", ".gif", gif.DecodeConfig)
	case len(head) >= 12 && string(head[:4]) == "RIFF" && string(head[8:12]) == "WEBP":
		return img("image/webp", ".webp", webp.DecodeConfig)
	case bytes.Contains(head, []byte("%PDF-1.")) || bytes.Contains(head, []byte("%PDF-2.")):
		tail := make([]byte, min(size, 1024))
		if _, err := r.ReadAt(tail, size-int64(len(tail))); err != nil && err != io.EOF {
			return attachType{}, typeLine(name, inbound)
		}
		if !bytes.Contains(tail, []byte("%%EOF")) {
			return attachType{}, typeLine(name, inbound)
		}
		return attachType{mediaType: "application/pdf", ext: ".pdf"}, ""
	case bytes.HasPrefix(head, []byte("PK\x03\x04")):
		if !inbound {
			return attachType{}, typeLine(name, inbound)
		}
		return sniffDocx(r, size, name)
	}
	b := make([]byte, size)
	if _, err := r.ReadAt(b, 0); err != nil && err != io.EOF {
		return attachType{}, typeLine(name, inbound)
	}
	if !plainFileText(b) {
		return attachType{}, typeLine(name, inbound)
	}
	switch strings.ToLower(path.Ext(name)) {
	case ".md", ".markdown":
		return attachType{mediaType: "text/markdown", ext: ".md"}, ""
	case ".txt":
		return attachType{mediaType: "text/plain", ext: ".txt"}, ""
	}
	return attachType{}, typeLine(name, inbound)
}

// plainFileText is valid UTF-8 with no NUL and no control characters but
// tab, newline and carriage return.
func plainFileText(b []byte) bool {
	if !utf8.Valid(b) {
		return false
	}
	for _, r := range string(b) {
		if r != '\t' && r != '\n' && r != '\r' && unicode.IsControl(r) {
			return false
		}
	}
	return true
}

// sniffDocx checks a Word document from its ZIP central directory alone:
// at most 10,000 entries declaring at most 200 MB unpacked, no entry named
// with "..", an absolute path or a backslash, [Content_Types].xml naming
// word/document.xml as a Word document, and no macros. Nothing but the
// content types (at most 1 MB) is read out of it.
func sniffDocx(r io.ReaderAt, size int64, name string) (attachType, string) {
	unsafe := func(why string) (attachType, string) {
		return attachType{}, name + " isn't a Word document Portenv can pass on safely (" + why + ")."
	}
	z, err := zip.NewReader(r, size)
	if err != nil && z == nil {
		return attachType{}, typeLine(name, true)
	}
	if len(z.File) > teamkit.MaxDocxEntries {
		return unsafe(fmt.Sprintf("it has more than %s parts", thousands(teamkit.MaxDocxEntries)))
	}
	var total uint64
	var types *zip.File
	for _, f := range z.File {
		n := f.Name
		if strings.HasPrefix(n, "/") || strings.Contains(n, `\`) || strings.Contains(n, ":") || slicesContains(strings.Split(n, "/"), "..") {
			return unsafe("a part is named outside the document")
		}
		total += f.UncompressedSize64
		if total > teamkit.MaxDocxUnpackedBytes {
			return unsafe(fmt.Sprintf("it unpacks to more than %d MB", teamkit.MaxDocxUnpackedBytes>>20))
		}
		if strings.HasSuffix(strings.ToLower(n), "vbaproject.bin") {
			return unsafe("it has macros")
		}
		if n == "[Content_Types].xml" {
			types = f
		}
	}
	if types == nil || types.UncompressedSize64 > 1<<20 {
		return attachType{}, typeLine(name, true)
	}
	rc, err := types.Open()
	if err != nil {
		return attachType{}, typeLine(name, true)
	}
	ct, err := io.ReadAll(io.LimitReader(rc, 1<<20))
	_ = rc.Close()
	if err != nil {
		return attachType{}, typeLine(name, true)
	}
	if bytes.Contains(bytes.ToLower(ct), []byte("macroenabled")) {
		return unsafe("it has macros")
	}
	if !bytes.Contains(ct, []byte(`PartName="/word/document.xml"`)) || !bytes.Contains(ct, []byte("wordprocessingml.document.main+xml")) {
		return attachType{}, typeLine(name, true)
	}
	return attachType{mediaType: docxMediaType, ext: ".docx"}, ""
}

func slicesContains(s []string, v string) bool {
	for _, x := range s {
		if x == v {
			return true
		}
	}
	return false
}

// cleanAttachName is ADR 0030 point 3's name: the base name, NFC, with
// control, bidi and zero-width characters removed; \, : and NUL made "-";
// no leading "." or "-", no trailing spaces or dots; at most 120 bytes,
// cut before the extension; the extension the type's. An empty name is
// attachment.<ext>.
func cleanAttachName(name, ext string) string {
	if i := strings.LastIndexByte(name, '/'); i >= 0 {
		name = name[i+1:]
	}
	name = strings.ToValidUTF8(name, "")
	name = norm.NFC.String(name)
	var b strings.Builder
	for _, r := range name {
		switch {
		case r == '\\' || r == ':' || r == 0:
			b.WriteByte('-')
		case unicode.IsControl(r), r >= 0x202A && r <= 0x202E, r >= 0x2066 && r <= 0x2069, r == 0x200E, r == 0x200F, r == 0x061C,
			r >= 0x200B && r <= 0x200D, r == 0x2060, r == 0xFEFF:
		default:
			b.WriteRune(r)
		}
	}
	trim := func(s string) string {
		s = strings.TrimLeft(s, ".-")
		return strings.TrimRight(s, " .")
	}
	stem := trim(b.String())
	stem = trim(strings.TrimSuffix(stem, path.Ext(stem)))
	limit := teamkit.MaxAttachmentNameBytes - len(ext)
	for len(stem) > limit {
		_, n := utf8.DecodeLastRuneInString(stem)
		stem = stem[:len(stem)-n]
	}
	stem = strings.TrimRight(stem, " .")
	if stem == "" {
		stem = "attachment"
	}
	return stem + ext
}

// numbered is name with " (n)" before its extension.
func numbered(name string, n int) string {
	ext := path.Ext(name)
	stem := strings.TrimSuffix(name, ext)
	suffix := " (" + strconv.Itoa(n) + ")"
	for len(stem)+len(suffix)+len(ext) > teamkit.MaxAttachmentNameBytes {
		_, k := utf8.DecodeLastRuneInString(stem)
		stem = stem[:len(stem)-k]
	}
	return stem + suffix + ext
}
