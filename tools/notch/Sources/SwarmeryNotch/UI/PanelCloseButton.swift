// The close control shared by both right-edge panels: a round, clearly
// visible button with the conventional × glyph. Plain `›` glyphs in secondary
// colour were easy to miss.
import SwiftUI

struct PanelCloseButton: View {
    let action: () -> Void
    @State private var hovering = false

    var body: some View {
        Button(action: action) {
            Image(systemName: "xmark")
                .font(.system(size: 12, weight: .bold))
                .foregroundStyle(hovering ? Color.primary : Color.secondary)
                .frame(width: 26, height: 26)
                .background(
                    Circle().fill(Color.primary.opacity(hovering ? 0.16 : 0.08))
                )
                .overlay(Circle().strokeBorder(WidgetPalette.border, lineWidth: 1))
        }
        .buttonStyle(.plain)
        .onHover { hovering = $0 }
        .help("Close")
        .accessibilityLabel("Close panel")
    }
}
