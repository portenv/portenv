// SPDX-License-Identifier: Apache-2.0

import Foundation

/// Help › Acknowledgements… (R-0096): the third-party notices that gmake app
/// writes into Contents/Resources/ThirdPartyNotices.txt (every Go module in
/// the helpers, Go's own licence and every Swift package, with their
/// licences), shown as plain text.
public enum Acknowledgements {
    public static let menuItem = "Acknowledgements…"
    public static let windowTitle = "Acknowledgements"
    public static let resourceName = "ThirdPartyNotices"
    public static let resourceExtension = "txt"
    /// A build run from Xcode or `swift run` has no notices file.
    public static let missing = "This build doesn't include the acknowledgements file."

    /// The notices in a bundle's resources, or `missing`.
    public static func text(in bundle: Bundle) -> String {
        guard let url = bundle.url(forResource: resourceName, withExtension: resourceExtension),
              let text = try? String(contentsOf: url, encoding: .utf8),
              !text.isEmpty
        else { return missing }
        return text
    }
}
