// SPDX-License-Identifier: Apache-2.0

import AppKit
import PortenvKit
import SwiftUI

/// "Move Portenv to Applications?" as a sheet on the window (R-0121): the
/// same words and buttons as the alert it replaces. Return moves, Escape is
/// Not Now.
struct MoveOfferView: View {
    let answer: (MoveOffer.Answer) -> Void

    var body: some View {
        VStack(alignment: .leading, spacing: 14) {
            HStack(alignment: .top, spacing: 14) {
                Image(nsImage: PortenvIcon.image)
                    .resizable()
                    .frame(width: 56, height: 56)
                    .accessibilityHidden(true)
                VStack(alignment: .leading, spacing: 8) {
                    Text(MoveText.question).font(.headline)
                        .accessibilityAddTraits(.isHeader)
                    Text(MoveText.detail)
                        .foregroundStyle(.secondary)
                        .fixedSize(horizontal: false, vertical: true)
                }
            }
            HStack {
                Spacer()
                ForEach(MoveOffer.buttons, id: \.title) { button in
                    switch button.key {
                    case .default:
                        Button(button.title) { answer(button.answer) }
                            .buttonStyle(.borderedProminent)
                            .keyboardShortcut(.defaultAction)
                    case .cancel:
                        Button(button.title) { answer(button.answer) }
                            .keyboardShortcut(.cancelAction)
                    }
                }
            }
        }
        .padding(20)
        .frame(width: 440)
    }
}
