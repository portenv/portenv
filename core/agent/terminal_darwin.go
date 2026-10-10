// SPDX-License-Identifier: Apache-2.0

package agent

// inputQueueReq asks a terminal how much input its program hasn't read
// (FIONREAD, _IOR('f', 127, int)); macOS runs ServeTerminal only in tests.
const inputQueueReq = 0x4004667f
