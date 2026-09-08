// The panel's SwiftUI content: the notch/top-edge shape behind either the
// compact strip or the expanded panel, and the size/hover reporting
// `WidgetPresenter` needs to size and position the window.
import SwiftUI

struct NotchRootView: View {
    @ObservedObject var viewState: WidgetViewState
    let actions: WidgetActions
    let onHover: (Bool) -> Void
    let onTapTab: (PanelKind) -> Void
    let onCollapse: () -> Void
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
            // The window can be larger than the content for a frame or two
            // (fallback size before the first measurement, animation). Pin
            // the content so it never floats: glued to the right edge and
            // hanging from the top for the right-edge placement, top-centred
            // for the notch. Applied OUTSIDE the GeometryReader so the
            // measured size stays the content's own.
            .frame(
                maxWidth: .infinity,
                maxHeight: .infinity,
                alignment: viewState.placement == .rightEdge ? .topTrailing : .top
            )
    }

    @ViewBuilder
    private var surface: some View {
        switch (viewState.placement, viewState.presentation) {
        case (.rightEdge, .dormant), (.rightEdge, .compact):
            // Nothing to draw around the hot strip; the tabs draw their own
            // surfaces (each is its own rounded card).
            content
        default:
            placedSurface
        }
    }

    @ViewBuilder
    private var placedSurface: some View {
        switch viewState.placement {
        case .notch:
            content.background(
                WidgetShape(topRadius: viewState.metrics.isPhysicalNotch ? 0 : 8, bottomRadius: 12)
                    .fill(Color.black)
            )
        case .rightEdge:
            content
                .edgeSurface()
                .padding(.leading, Self.edgeInset)
                .padding(.vertical, Self.edgeInset)
        }
    }

    @ViewBuilder
    private var content: some View {
        switch viewState.presentation {
        case .hidden:
            EmptyView()
        case .dormant:
            // Nothing visible, but hit-testable: a fully transparent colour is
            // skipped by hit testing, so keep a hair of opacity.
            Color.black.opacity(0.001)
                .frame(width: WidgetWindowGeometry.hotStripWidth, height: EdgeTab.size.height + Self.edgeInset * 2)
        case .compact:
            switch viewState.placement {
            case .notch: CompactStrip(state: viewState.attention)
            case .rightEdge:
                EdgeTabs(state: viewState.attention, onTap: onTapTab)
                    .padding(.leading, Self.edgeInset)
                    .padding(.vertical, Self.edgeInset)
            }
        case .expanded:
            switch (viewState.placement, viewState.panel) {
            case (.rightEdge, .usage):
                UsagePanel(usage: viewState.attention.usage, onCollapse: onCollapse)
            case (.rightEdge, .sessions):
                ExpandedPanel(
                    state: viewState.attention, actions: actions,
                    showsHeader: true, onCollapse: onCollapse, showsUsage: false
                )
            case (.notch, _):
                ExpandedPanel(state: viewState.attention, actions: actions)
            }
        }
    }
}

private struct SizeKey: PreferenceKey {
    static let defaultValue: CGSize = .zero
    static func reduce(value: inout CGSize, nextValue: () -> CGSize) {
        value = nextValue()
    }
}
