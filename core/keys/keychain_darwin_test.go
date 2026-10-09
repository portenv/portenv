// SPDX-License-Identifier: Apache-2.0

//go:build darwin && cgo

package keys

import (
	"errors"
	"os"
	"os/exec"
	"testing"
	"time"
)

// TestTheNoPromptOptionResolves: the Security framework's names for
// kSecUseAuthenticationUI and its Fail value are read at start; empty would
// silently drop the option.
func TestTheNoPromptOptionResolves(t *testing.T) {
	if useAuthUIKey == "" || useAuthUIFail == "" || useAuthUIKey == useAuthUIFail {
		t.Fatalf("no-prompt option %q = %q", useAuthUIKey, useAuthUIFail)
	}
	if Default("").(Keychain).Interactive {
		t.Fatal("the default Keychain store may prompt; only AllowPrompts allows that")
	}
}

// TestAMissingKeyIsNotFound: no item, no prompt, a plain ErrNotFound.
func TestAMissingKeyIsNotFound(t *testing.T) {
	_, err := Keychain{}.Get("portenv-test-missing-" + time.Now().Format("150405.000000"))
	if !errors.Is(err, ErrNotFound) {
		t.Fatalf("missing key: %v, want ErrNotFound", err)
	}
}

// TestNoPromptReturnsNeedsApproval: an item that only Apple's security tool
// may read (it created it) needs the person's approval to read from here.
// Without prompts, the read returns ErrNeedsApproval at once instead of
// raising a dialog. Opt-in (PORTENV_TEST_KEYCHAIN=1, make keychain-test on a
// Mac): if the no-prompt option ever broke, this would show a real prompt.
func TestNoPromptReturnsNeedsApproval(t *testing.T) {
	if os.Getenv("PORTENV_TEST_KEYCHAIN") != "1" {
		t.Skip("set PORTENV_TEST_KEYCHAIN=1 to read a real Keychain item that needs approval")
	}
	account := "portenv-test-needs-approval"
	// #nosec G204 -- fixed arguments
	_ = exec.Command("security", "delete-generic-password", "-s", keychainService, "-a", account).Run()
	// #nosec G204 -- fixed arguments
	if out, err := exec.Command("security", "add-generic-password", "-s", keychainService, "-a", account, "-w", "not-a-real-key").CombinedOutput(); err != nil {
		t.Fatalf("create the test item: %v: %s", err, out)
	}
	t.Cleanup(func() {
		// #nosec G204 -- fixed arguments
		_ = exec.Command("security", "delete-generic-password", "-s", keychainService, "-a", account).Run()
	})
	done := make(chan error, 1)
	go func() { _, err := Keychain{}.Get(account); done <- err }()
	select {
	case err := <-done:
		if !errors.Is(err, ErrNeedsApproval) {
			t.Fatalf("read without prompts: %v, want ErrNeedsApproval", err)
		}
		if err.Error() != "Keychain needs your approval. Open Portenv on this Mac to allow it." {
			t.Fatalf("the error is %q", err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("the read waited: a prompt is probably on screen (dismiss it)")
	}
}
