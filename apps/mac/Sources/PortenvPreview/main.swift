// SPDX-License-Identifier: Apache-2.0

import Foundation
import PortenvPreviewCore

// PortenvPreview.xpc (ADR 0030 point 5): the one place an attachment's
// contents are decoded. It runs in the App Sandbox with no entitlements
// beyond it (PortenvPreview.entitlements): no network, no files outside its
// own container, no Keychain. A crash or a hang here costs the preview,
// never the app.

final class Delegate: NSObject, NSXPCListenerDelegate {
    func listener(_: NSXPCListener, shouldAcceptNewConnection c: NSXPCConnection) -> Bool {
        c.exportedInterface = NSXPCInterface(with: PreviewXPC.self)
        c.exportedObject = PreviewService()
        c.resume()
        return true
    }
}

let delegate = Delegate()
let listener = NSXPCListener.service()
listener.delegate = delegate
listener.resume()
