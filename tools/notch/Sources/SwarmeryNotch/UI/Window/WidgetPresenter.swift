// Presents the notch panel on exactly one screen: window lifecycle and
// transition choreography.
//
// Adapted from notchling's WidgetPresenter.swift (MIT license, see
// NOTICE.md) for the per-screen presenter shape and the transition
// sequencing (resize the window before the content moves, not during --
// once a state has been measured, its size is final, so the whole
// transition runs inside a window that does not change). Adapted: notchling
// wires its own session store / root view / actions; this port wires
// `AttentionState` / `NotchRootView` / this package's `WidgetActions`.
// Dropped: the debounced re-measure ("settle") and "grow if clipped while
// open" refinements notchling layers on top -- pure UI polish this phase had
// no way to visually iterate on headless, noted as a deviation in the
// phase-3 Completion Report.
import AppKit
import SwiftUI

/// The mutable state `NotchRootView` observes. Deliberately a plain
/// `ObservableObject` (Combine) rather than the newer `@Observable` macro --
/// no functional difference here, and `@Published`/`ObservableObject` is the
/// more widely portable choice across toolchains.
@MainActor
final class WidgetViewState: ObservableObject {
    @Published var presentation: WidgetPresentation = .hidden
    @Published var attention: AttentionState = AttentionState()
    @Published var metrics: WidgetMetrics
    let placement: WidgetPlacement

    init(metrics: WidgetMetrics, placement: WidgetPlacement) {
        self.metrics = metrics
        self.placement = placement
    }
}

@MainActor
public final class WidgetPresenter {
    /// Screens are identified by display id, not by `NSScreen`: those
    /// objects are replaced wholesale on reconfiguration, so a stored
    /// reference goes stale the moment someone plugs in a monitor.
    public let displayID: CGDirectDisplayID

    private let actions: WidgetActions
    private let viewState: WidgetViewState
    private var panel: WidgetPanel?
    private var geometry: WidgetWindowGeometry
    private var isHovering = false

    public init(screen: NSScreen, actions: WidgetActions, placement: WidgetPlacement = .default) {
        self.displayID = screen.displayID
        self.actions = actions
        self.viewState = WidgetViewState(metrics: WidgetMetrics(screen: screen), placement: placement)
        self.geometry = WidgetWindowGeometry(placement: placement)
    }

    public var placement: WidgetPlacement { viewState.placement }

    public var isPhysicalNotch: Bool { viewState.metrics.isPhysicalNotch }

    private var screen: NSScreen? {
        NSScreen.screens.first { $0.displayID == displayID }
    }

    // MARK: - State in

    /// Called by the app coordinator on every attention-state change.
    /// `shouldOpen` is the coordinator's `shouldExpand(now:linger:)` result,
    /// OR'd with manual hover here -- this presenter does not own the clock.
    public func update(attention: AttentionState, shouldOpen: Bool) {
        viewState.attention = attention
        apply(shouldOpen || isHovering ? .expanded : .compact)
    }

    public func setHovering(_ hovering: Bool) {
        guard hovering != isHovering else { return }
        isHovering = hovering
        apply(hovering || viewState.attention.needsAttention ? .expanded : .compact)
    }

    /// Re-measure after a display change: resolution, scale factor and menu
    /// bar height can all move under a screen that kept its id.
    public func refreshMetrics() {
        guard let screen else { return }
        let fresh = WidgetMetrics(screen: screen)
        guard fresh != viewState.metrics else { return }
        viewState.metrics = fresh
        resizeWindow(for: viewState.presentation)
    }

    public func teardown() {
        geometry.forget()
        viewState.presentation = .hidden
        isHovering = false
        panel?.orderOut(nil)
        panel?.close()
        panel = nil
    }

    // MARK: - Presentation

    private func apply(_ presentation: WidgetPresentation) {
        guard presentation != viewState.presentation else { return }

        let hadPanel = panel != nil
        ensurePanel()
        // Before the content moves, not during -- once a state has been
        // measured this is its final size, so the whole transition runs
        // inside a window that does not change.
        resizeWindow(for: presentation)

        withAnimation(WidgetTiming.curve(to: presentation, hasPanel: hadPanel)) {
            viewState.presentation = presentation
        }
    }

    // MARK: - Window

    private func ensurePanel() {
        // No screen with this id any more means the display was unplugged
        // mid-flight.
        guard panel == nil, screen != nil else { return }

        let root = NotchRootView(
            viewState: viewState,
            actions: actions,
            onHover: { [weak self] hovering in self?.setHovering(hovering) },
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

        resizeWindow(for: .compact)
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
        // Lay out synchronously, before the caller starts animating -- SwiftUI
        // centres content in the width it has been offered, and without this
        // the first frames are laid out against the *old* width.
        panel.layoutIfNeeded()
    }
}
