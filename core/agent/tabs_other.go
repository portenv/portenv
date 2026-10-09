// SPDX-License-Identifier: Apache-2.0

//go:build !linux

package agent

func tmuxAs(Config) (tmuxFunc, error) { return nil, errNoTabs }
