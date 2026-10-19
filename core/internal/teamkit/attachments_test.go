// SPDX-License-Identifier: Apache-2.0

package teamkit

import (
	"strings"
	"testing"
)

// ADR 0030 point 9a: each built-in template's agents and the guide carry
// the guidance block word for word, R-0380's sentence first, so the
// templates can't drift.
func TestEveryTemplateCarriesTheAttachGuidance(t *testing.T) {
	if !strings.HasPrefix(AttachGuidance, "Attach something for the owner only when he needs to see it to understand or decide. By default, attach nothing.") {
		t.Fatalf("R-0380's sentence isn't first: %q", AttachGuidance)
	}
	for _, name := range Templates {
		files, err := Template(name)
		if err != nil {
			t.Fatal(err)
		}
		for file, b := range files {
			if !strings.Contains(string(b), AttachGuidance) {
				t.Errorf("%s/%s doesn't carry the attachment guidance word for word", name, file)
			}
		}
	}
	text := NewGuide().Text()
	for _, want := range []string{AttachGuidance, AttachDataRule, "--attach FILE", "inbox/<sender>/<time>/<name>"} {
		if !strings.Contains(text, want) {
			t.Errorf("the guide doesn't say %q", want)
		}
	}
	i, j := strings.Index(text, "Attach something for the owner only"), strings.Index(text, "--attach FILE")
	if i < 0 || j < 0 || i > j {
		t.Error("the guide doesn't put R-0380's principle before --attach's commands")
	}
	for _, p := range SecretNamePatterns {
		if !strings.Contains(text, p) {
			t.Errorf("the guide doesn't print the deny-list's %s", p)
		}
	}
	g := NewGuide()
	for k, v := range map[string]any{"attachment_bytes": MaxAttachmentBytes, "attachments_per_message": MaxAttachmentsPerMessage, "attachments_per_day": AttachmentsPerDay} {
		if g.Limits[k] != v {
			t.Errorf("limits[%s] = %v, want %v", k, g.Limits[k], v)
		}
	}
}

// Names are one check of several: the deny-list matches case-insensitively
// on the base name only.
func TestSecretNames(t *testing.T) {
	for _, n := range []string{"deploy.pem", "SERVER.KEY", "cert.p12", "a.pfx", "vault.kdbx", "login.keychain-db", "id_rsa", "id_rsa.pub", "id_ed25519", "id_ecdsa_sk",
		"aws-credentials.txt", "Credentials.md", "my_secret.txt", "SECRETS.md", "terraform.tfstate", "terraform.tfstate.backup", ".netrc", ".npmrc", ".pypirc"} {
		if !SecretName(n) {
			t.Errorf("%s isn't refused by name", n)
		}
	}
	for _, n := range []string{"login.png", "report.pdf", "keyboard.md", "notes.txt", "design.md", "pem.md"} {
		if SecretName(n) {
			t.Errorf("%s is refused by name", n)
		}
	}
	for _, p := range []string{".ssh/id", "acme/.git/config", "acme/.env", ".env.local", "a/.config/x.md"} {
		if !HiddenComponent(p) {
			t.Errorf("%s isn't hidden", p)
		}
	}
	if HiddenComponent("acme/docs/redesign.md") {
		t.Error("a plain path reads as hidden")
	}
	// The key blocks are made at run time, so no file holds one.
	block := func(kind string) string { return "-----BEGIN " + kind + "PRIVATE" + " KEY-----" }
	for _, s := range []string{block("OPENSSH ") + "\nabc", "x\n" + block("RSA "), block(""), block("EC "),
		"AWS_SECRET_ACCESS_KEY=abc", "aws_secret_access_key = abc", "export AWS_SECRET_ACCESS_KEY=x"} {
		if !SecretContent([]byte(s)) {
			t.Errorf("%q isn't seen as a secret", s)
		}
	}
	for _, s := range []string{"# Redesign\nA private key block is refused.", "-----BEGIN PUBLIC KEY-----", "aws_access_key_id = x"} {
		if SecretContent([]byte(s)) {
			t.Errorf("%q is seen as a secret", s)
		}
	}
}
