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

    /// The list of components at the top of the notices, one per line
    /// ("name version · licence"): the lines after the first blank line, up
    /// to the next one. The window gives them a hanging indent, so a long
    /// line that wraps still reads as one component (R-0119).
    public static func listRange(in text: String) -> NSRange? {
        let ns = text as NSString
        let first = ns.range(of: "\n\n")
        guard first.location != NSNotFound else { return nil }
        let start = first.location + first.length
        let rest = NSRange(location: start, length: ns.length - start)
        let next = ns.range(of: "\n\n", range: rest)
        let end = next.location == NSNotFound ? ns.length : next.location + 1 // with the last line's newline
        return end > start ? NSRange(location: start, length: end - start) : nil
    }

    /// The notices in a bundle's resources, or `missing`.
    public static func text(in bundle: Bundle) -> String {
        guard let url = bundle.url(forResource: resourceName, withExtension: resourceExtension),
              let text = try? String(contentsOf: url, encoding: .utf8),
              !text.isEmpty
        else { return missing }
        return text
    }
}
