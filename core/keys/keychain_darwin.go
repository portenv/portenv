// SPDX-License-Identifier: Apache-2.0

//go:build darwin && cgo

package keys

/*
#cgo LDFLAGS: -framework CoreFoundation -framework Security
#include <CoreFoundation/CoreFoundation.h>
#include <Security/Security.h>

static int portenv_cfstr(CFStringRef s, char *buf, int n) {
	return CFStringGetCString(s, buf, n, kCFStringEncodingUTF8) ? 1 : 0;
}

// Items in the file-based login keychain are guarded by their access
// lists, not by kSecUseAuthenticationUI: turning user interaction off for
// the process is what makes those reads fail with
// errSecInteractionNotAllowed instead of showing a prompt.
#pragma clang diagnostic push
#pragma clang diagnostic ignored "-Wdeprecated-declarations"
static OSStatus portenv_no_prompts(void) {
	return SecKeychainSetUserInteractionAllowed(false);
}
#pragma clang diagnostic pop
*/
import "C"

import (
	"errors"
	"sync"
	"unsafe"

	"github.com/keybase/go-keychain"
)

const keychainService = "dev.portenv.repository"

// Keychain is the macOS Keychain. Only a person at a terminal (or the app,
// in Swift) may be asked to approve a read: Interactive is false for
// portenvd, the runner and the CLI when the app or a script runs it, and
// then a read that would need approval fails at once with ErrNeedsApproval
// instead of raising a prompt nobody may see (PLAN.md, Phase 1 item 1).
type Keychain struct {
	Interactive bool
}

// query is the read for one key. Without Interactive it carries
// kSecUseAuthenticationUI = Fail: no prompt, errSecInteractionNotAllowed.
func (k Keychain) query(boxID string) keychain.Item {
	q := keychain.NewItem()
	q.SetSecClass(keychain.SecClassGenericPassword)
	q.SetService(keychainService)
	q.SetAccount(boxID)
	q.SetMatchLimit(keychain.MatchLimitOne)
	q.SetReturnData(true)
	if !k.Interactive {
		q.SetString(useAuthUIKey, useAuthUIFail)
	}
	return q
}

func (k Keychain) Get(boxID string) ([]byte, error) {
	if !k.Interactive {
		noPrompts()
	}
	res, err := keychain.QueryItem(k.query(boxID))
	if needsApproval(k.Interactive, err) {
		return nil, ErrNeedsApproval
	}
	if err != nil {
		return nil, err
	}
	if len(res) == 0 || res[0].Data == nil {
		return nil, ErrNotFound
	}
	return res[0].Data, nil
}

func (k Keychain) Put(boxID string, key []byte) error {
	if !k.Interactive {
		noPrompts()
	}
	item := keychain.NewGenericPassword(keychainService, boxID, "Portenv repository key ("+boxID+")", key, "")
	item.SetSynchronizable(keychain.SynchronizableNo)
	item.SetAccessible(keychain.AccessibleWhenUnlockedThisDeviceOnly)
	if !k.Interactive {
		item.SetString(useAuthUIKey, useAuthUIFail)
	}
	err := keychain.AddItem(item)
	if errors.Is(err, keychain.ErrorDuplicateItem) {
		return errors.New("a key for box " + boxID + " already exists in the Keychain")
	}
	if needsApproval(k.Interactive, err) {
		return ErrNeedsApproval
	}
	return err
}

// needsApproval: with interaction off, the legacy keychain refuses a read
// its access list doesn't allow with errSecAuthFailed, and data-protection
// items with errSecInteractionNotAllowed; either way the person must
// approve it in the app.
func needsApproval(interactive bool, err error) bool {
	if errors.Is(err, keychain.ErrorInteractionNotAllowed) {
		return true
	}
	return !interactive && errors.Is(err, keychain.ErrorAuthFailed)
}

// Whether this process may raise a Keychain prompt is decided once per
// process: only the CLI in front of a person (AllowPrompts) may, never
// portenvd, the runner, the app's helper calls or a script. Those turn
// Keychain interaction off for the whole process before their first call
// (SecKeychainSetUserInteractionAllowed, process-wide and never turned back
// on), so the two exclude each other: a process that may prompt never
// turns interaction off, and one that turned it off can't start prompting.
// The app (Swift) never links this package and never calls the switch.
var (
	promptsMu   sync.Mutex
	prompts     bool
	switchedOff bool
)

// AllowPrompts lets this process ask the person to approve Keychain reads
// (the CLI, when a terminal is attached). It panics if Keychain interaction
// was already turned off in this process: that order is a bug.
func AllowPrompts() {
	promptsMu.Lock()
	defer promptsMu.Unlock()
	if switchedOff {
		panic("keys: AllowPrompts after Keychain interaction was turned off for this process")
	}
	prompts = true
}

// mayTurnOffInteraction reports whether a non-interactive call may turn
// Keychain interaction off for this process, and records it: never in a
// process that may prompt.
func mayTurnOffInteraction() bool {
	promptsMu.Lock()
	defer promptsMu.Unlock()
	if prompts {
		return false
	}
	switchedOff = true
	return true
}

func promptsAllowed() bool {
	promptsMu.Lock()
	defer promptsMu.Unlock()
	return prompts
}

// Default is the Keychain, without prompts unless AllowPrompts was called.
func Default(string) Store { return Keychain{Interactive: promptsAllowed()} }

// The Security framework's names for the no-prompt option.
var (
	useAuthUIKey  = cfString(C.kSecUseAuthenticationUI)
	useAuthUIFail = cfString(C.kSecUseAuthenticationUIFail)
)

// noPrompts turns Keychain user interaction off for this process, unless
// the process may prompt (AllowPrompts): see keys.go. Deprecated API: it goes
// once the app is signed with a stable identity and keys move to the
// data-protection keychain, where kSecUseAuthenticationUI alone is enough
// (ADR 0013).
func noPrompts() {
	if mayTurnOffInteraction() {
		turnOffInteraction()
	}
}

// turnOffInteraction is the process-wide switch (a variable for tests).
var turnOffInteraction = func() { C.portenv_no_prompts() }

func cfString(s C.CFStringRef) string {
	buf := make([]byte, 256)
	if C.portenv_cfstr(s, (*C.char)(unsafe.Pointer(&buf[0])), C.int(len(buf))) == 0 {
		return ""
	}
	return C.GoString((*C.char)(unsafe.Pointer(&buf[0])))
}
