// The panel's SwiftUI content: the notch/top-edge shape behind either the
// compact strip or the expanded panel, and the size/hover reporting
// `WidgetPresenter` needs to size and position the window.
import SwiftUI

struct NotchRootView: View {
    @ObservedObject var viewState: WidgetViewState
    let actions: WidgetActions
    let onHover: (Bool) -> Void
    let onContentGeometry: (WidgetPresentation, WidgetContentGeometry) -> Void

    var body: some View {
        content
            .background(
                GeometryReader { proxy in
                    Color.clear.preference(key: SizeKey.self, value: proxy.size)
                }
            )
            .background(
                WidgetShape(topRadius: viewState.metrics.isPhysicalNotch ? 0 : 8, bottomRadius: 12)
                    .fill(Color.black)
            )
            .onPreferenceChange(SizeKey.self) { size in
                onContentGeometry(viewState.presentation, WidgetContentGeometry(size: size))
            }
            .onHover(perform: onHover)
    }

    @ViewBuilder
    private var content: some View {
        switch viewState.presentation {
        case .hidden:
            EmptyView()
        case .compact:
            CompactStrip(state: viewState.attention)
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
