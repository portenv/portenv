// SPDX-License-Identifier: Apache-2.0

//go:build !linux

package agent

import "errors"

// errNoTabs: tabs exist only inside a box.
var errNoTabs = errors.New("tabs exist only inside a box (Linux)")

func tmuxAs(Config) (tmuxFunc, error) { return nil, errNoTabs }
