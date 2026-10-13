// SPDX-License-Identifier: Apache-2.0
//
// badge-icon.swift ICONSET LETTER: draws a dev lane's letter on every PNG in
// an .iconset folder, in place (gmake app LANE=X), so a lane's app is never
// mistaken for the release app in the Dock or Finder (R-0128): an orange
// disc with the white letter in the lower right corner.
import AppKit

let args = CommandLine.arguments
guard args.count == 3, args[2].count == 1, let letter = args[2].first, letter.isUppercase else {
    FileHandle.standardError.write(Data("usage: badge-icon.swift ICONSET LETTER\n".utf8))
    exit(2)
}
let folder = URL(fileURLWithPath: args[1])
let pngs = try FileManager.default.contentsOfDirectory(at: folder, includingPropertiesForKeys: nil)
    .filter { $0.pathExtension == "png" }
guard !pngs.isEmpty else {
    FileHandle.standardError.write(Data("badge-icon: no PNGs in \(folder.path)\n".utf8))
    exit(1)
}
for url in pngs {
    guard let source = NSBitmapImageRep(data: try Data(contentsOf: url)) else {
        FileHandle.standardError.write(Data("badge-icon: can't read \(url.lastPathComponent)\n".utf8))
        exit(1)
    }
    let w = source.pixelsWide, h = source.pixelsHigh
    guard let rep = NSBitmapImageRep(bitmapDataPlanes: nil, pixelsWide: w, pixelsHigh: h, bitsPerSample: 8,
                                     samplesPerPixel: 4, hasAlpha: true, isPlanar: false,
                                     colorSpaceName: .deviceRGB, bytesPerRow: 0, bitsPerPixel: 0),
          let context = NSGraphicsContext(bitmapImageRep: rep)
    else { exit(1) }
    rep.size = NSSize(width: w, height: h) // draw in pixels
    NSGraphicsContext.saveGraphicsState()
    NSGraphicsContext.current = context
    source.draw(in: NSRect(x: 0, y: 0, width: w, height: h))
    // The disc: about half the icon wide, so the letter still reads at 16 px.
    let side = CGFloat(w) * 0.5
    let disc = NSRect(x: CGFloat(w) - side - CGFloat(w) * 0.02, y: CGFloat(h) * 0.02, width: side, height: side)
    NSColor.white.setFill()
    NSBezierPath(ovalIn: disc).fill()
    NSColor(srgbRed: 0.93, green: 0.42, blue: 0.0, alpha: 1).setFill()
    NSBezierPath(ovalIn: disc.insetBy(dx: side * 0.07, dy: side * 0.07)).fill()
    let font = NSFont.systemFont(ofSize: side * 0.62, weight: .heavy)
    let text = NSAttributedString(string: String(letter), attributes: [.font: font, .foregroundColor: NSColor.white])
    let size = text.size()
    text.draw(at: NSPoint(x: disc.midX - size.width / 2, y: disc.midY - size.height / 2))
    NSGraphicsContext.restoreGraphicsState()
    guard let png = rep.representation(using: .png, properties: [:]) else { exit(1) }
    try png.write(to: url)
}
