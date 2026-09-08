// Owns one WidgetPanel per display and decides which presentation is on
// screen. Adapted from notchling's WidgetPresenter (MIT, see NOTICE.md); the
// right-edge state machine (dormant hot strip → tab on hover → panel on
// click / attention) is ours.
import AppKit
import SwiftUI

@MainActor
final class WidgetViewState: ObservableObject {
    @Published var presentation: WidgetPresentation = .hidden
    @Published var attention: AttentionState = AttentionState()
    @Published var metrics: WidgetMetrics
    /// Which panel `.expanded` shows for the right-edge placement.
    @Published var panel: PanelKind = .sessions
    let placement: WidgetPlacement

    init(metrics: WidgetMetrics, placement: WidgetPlacement) {
        self.metrics = metrics
        self.placement = placement
    }
}

@MainActor
public final class WidgetPresenter {
    public let displayID: CGDirectDisplayID
    private let actions: WidgetActions
    private let viewState: WidgetViewState
    private var panel: WidgetPanel?
    private var geometry: WidgetWindowGeometry

    /// Mouse is inside the widget's window (hot strip, tab or panel).
    private var isHovering = false
    /// The operator opened the panel by clicking the tab; it stays open until
    /// they click the tab again or click anywhere outside the widget (see
    /// `dismissPinned`).
    private var pinnedOpen = false
    /// Attention state's verdict from the last `update` — kept so hover and
    /// click changes can recompute the target without a fresh model pass.
    private var attentionWantsOpen = false
    private var settleTask: Task<Void, Never>?

    /// How long the tab stays visible after the mouse leaves it before the
    /// widget goes dormant again. Long enough to move from the hot strip onto
    /// the tab (the window grows under the cursor) without flicker.
    nonisolated static let hoverGrace: TimeInterval = 0.45

    public init(
        screen: NSScreen,
        actions: WidgetActions,
        placement: WidgetPlacement = .default,
        edgeAnchorFromBottom: CGFloat = WidgetWindowGeometry.defaultEdgeAnchorFromBottom
    ) {
        self.displayID = screen.displayID
        self.actions = actions
        self.viewState = WidgetViewState(metrics: WidgetMetrics(screen: screen), placement: placement)
        self.geometry = WidgetWindowGeometry(placement: placement, edgeAnchorFromBottom: edgeAnchorFromBottom)
    }

    public var placement: WidgetPlacement { viewState.placement }
    public var isPhysicalNotch: Bool { viewState.metrics.isPhysicalNotch }

    private var screen: NSScreen? {
        NSScreen.screens.first { $0.displayID == displayID }
    }

    /// True while the mouse is over this presenter's window — used by the
    /// app-wide click monitor to tell a click inside from a click outside.
    public var containsMouse: Bool {
        guard let panel else { return false }
        return panel.frame.contains(NSEvent.mouseLocation)
    }

    // MARK: - Inputs

    public func update(attention: AttentionState, shouldOpen: Bool) {
        viewState.attention = attention
        // Attention always concerns sessions/approvals: switch the panel over
        // unless the operator pinned one open themselves.
        if shouldOpen, !attentionWantsOpen, !pinnedOpen { viewState.panel = .sessions }
        attentionWantsOpen = shouldOpen
        apply(target())
    }

    public func setHovering(_ hovering: Bool) {
        guard hovering != isHovering else { return }
        isHovering = hovering
        settleTask?.cancel()
        if hovering {
            apply(target())
            return
        }
        // Leaving: give the cursor a moment — it may be crossing from the hot
        // strip onto the tab that just appeared under it.
        settleTask = Task { @MainActor [weak self] in
            try? await Task.sleep(nanoseconds: UInt64(Self.hoverGrace * 1_000_000_000))
            guard !Task.isCancelled, let self, !self.isHovering else { return }
            self.apply(self.target())
        }
    }

    /// Click on a collapsed tab: open that tab's panel and keep it open; a
    /// second click on the same tab closes it, a click on the other tab
    /// switches panels.
    public func toggleExpanded(_ panel: PanelKind = .sessions) {
        if pinnedOpen, viewState.panel == panel {
            pinnedOpen = false
        } else {
            viewState.panel = panel
            pinnedOpen = true
        }
        apply(target())
    }

    /// The panel currently selected for the expanded presentation.
    public var currentPanel: PanelKind { viewState.panel }

    /// A click anywhere outside the widget.
    public func dismissPinned() {
        guard pinnedOpen else { return }
        pinnedOpen = false
        apply(target())
    }

    public func refreshMetrics() {
        guard let screen else { return }
        let fresh = WidgetMetrics(screen: screen)
        guard fresh != viewState.metrics else { return }
        viewState.metrics = fresh
        resizeWindow(for: viewState.presentation)
    }

    public func teardown() {
        settleTask?.cancel()
        geometry.forget()
        viewState.presentation = .hidden
        isHovering = false
        pinnedOpen = false
        panel?.orderOut(nil)
        panel?.close()
        panel = nil
    }

    // MARK: - State machine

    /// Pure decision, exposed for tests via `WidgetPresentation.target(...)`.
    private func target() -> WidgetPresentation {
        WidgetPresentation.target(
            placement: placement,
            attentionWantsOpen: attentionWantsOpen,
            pinnedOpen: pinnedOpen,
            hovering: isHovering
        )
    }

    private func apply(_ presentation: WidgetPresentation) {
        guard presentation != viewState.presentation else { return }
        let hadPanel = panel != nil
        ensurePanel()
        resizeWindow(for: presentation)
        withAnimation(WidgetTiming.curve(to: presentation, hasPanel: hadPanel)) {
            viewState.presentation = presentation
        }
    }

    private func ensurePanel() {
        guard panel == nil, screen != nil else { return }
        let root = NotchRootView(
            viewState: viewState,
            actions: actions,
            onHover: { [weak self] hovering in self?.setHovering(hovering) },
            onTapTab: { [weak self] kind in self?.toggleExpanded(kind) },
            onCollapse: { [weak self] in self?.dismissPinned() },
            onContentGeometry: { [weak self] presentation, geometry in
                self?.contentGeometryChanged(presentation, geometry)
            }
        )
        let panel = WidgetPanel(
            contentRect: .zero,
            styleMask: [.borderless, .nonactivatingPanel],
            backing: .buffered,
            defer: true
        )
        panel.contentView = NSHostingView(rootView: root)
        self.panel = panel
        resizeWindow(for: WidgetPresentation.resting(for: placement))
        panel.orderFrontRegardless()
    }

    private func contentGeometryChanged(_ presentation: WidgetPresentation, _ content: WidgetContentGeometry) {
        guard geometry.record(content, for: presentation) else { return }
        resizeWindow(for: viewState.presentation)
    }

    private func resizeWindow(for presentation: WidgetPresentation) {
        guard let panel, let screen else { return }
        let frame = geometry.frame(
            for: presentation,
            metrics: viewState.metrics,
            screenFrame: screen.frame,
            visibleFrame: screen.visibleFrame
        )
        guard panel.frame != frame else { return }
        panel.setFrame(frame, display: false)
        panel.layoutIfNeeded()
    }
}
