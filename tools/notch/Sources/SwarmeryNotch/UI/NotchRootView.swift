// The panel's SwiftUI content: the notch/top-edge shape behind either the
// compact strip or the expanded panel, and the size/hover reporting
// `WidgetPresenter` needs to size and position the window.
import SwiftUI

struct NotchRootView: View {
    @ObservedObject var viewState: WidgetViewState
    let actions: WidgetActions
    let onHover: (Bool) -> Void
    let onContentGeometry: (WidgetPresentation, WidgetContentGeometry) -> Void

    /// Transparent margin around the right-edge surface so its shadow has
    /// room inside the window; the window frame is the measured size, so
    /// without this the shadow would be clipped.
    nonisolated static let edgeInset: CGFloat = 10

    var body: some View {
        surface
            .background(
                GeometryReader { proxy in
                    Color.clear.preference(key: SizeKey.self, value: proxy.size)
                }
            )
            .onPreferenceChange(SizeKey.self) { size in
                onContentGeometry(viewState.presentation, WidgetContentGeometry(size: size))
            }
            .onHover(perform: onHover)
    }

    @ViewBuilder
    private var surface: some View {
        switch viewState.placement {
        case .notch:
            content.background(
                WidgetShape(topRadius: viewState.metrics.isPhysicalNotch ? 0 : 8, bottomRadius: 12)
                    .fill(Color.black)
            )
        case .rightEdge:
            let shape = UnevenRoundedRectangle(
                topLeadingRadius: 14, bottomLeadingRadius: 14, bottomTrailingRadius: 0, topTrailingRadius: 0
            )
            content
                .background(shape.fill(WidgetPalette.surface))
                .overlay(shape.strokeBorder(WidgetPalette.border, lineWidth: 1))
                .shadow(color: WidgetPalette.shadow, radius: 8, x: -2, y: 2)
                .padding(.leading, Self.edgeInset)
                .padding(.vertical, Self.edgeInset)
        }
    }

    @ViewBuilder
    private var content: some View {
        switch viewState.presentation {
        case .hidden:
            EmptyView()
        case .compact:
            switch viewState.placement {
            case .notch: CompactStrip(state: viewState.attention)
            case .rightEdge: EdgeTab(state: viewState.attention)
            }
        case .expanded:
            ExpandedPanel(state: viewState.attention, actions: actions)
        }
    }
}

private struct SizeKey: PreferenceKey {
    static let defaultValue: CGSize = .zero
    static func reduce(value: inout CGSize, nextValue: () -> CGSize) {
        value = nextValue()
    }
}
