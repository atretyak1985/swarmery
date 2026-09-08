// The collapsed presentation for `WidgetPlacement.rightEdge`: two small tabs
// stacked on the right screen edge — sessions on top, usage below. Each is
// its own rounded surface (like Grammarly's tab), glued to the edge, and
// opens its own panel on click.
import SwiftUI

/// The widget's colours, kept in one place so the tabs, the panel surface
/// and the row dots agree. The surface is `NSColor.windowBackgroundColor` —
/// a mid-dark gray in dark mode and a light gray in light mode — rather than
/// black: a black tab against a black notch and a dark editor does not
/// register, which was the operator's complaint about the first build.
enum WidgetPalette {
    static let surface = Color(nsColor: .windowBackgroundColor)
    static let border = Color.primary.opacity(0.14)
    static let accent = Color(red: 0.13, green: 0.64, blue: 0.56)
    static let shadow = Color.black.opacity(0.28)
}

/// Which panel the expanded presentation shows (right-edge placement).
public enum PanelKind: Equatable, Sendable {
    case sessions
    case usage
}

/// The right-edge surface: rounded on the left, square on the right so it
/// reads as docked to the screen edge; hairline border and a soft shadow.
struct EdgeSurface: ViewModifier {
    func body(content: Content) -> some View {
        let shape = UnevenRoundedRectangle(
            topLeadingRadius: 14, bottomLeadingRadius: 14, bottomTrailingRadius: 0, topTrailingRadius: 0
        )
        content
            .background(shape.fill(WidgetPalette.surface))
            .overlay(shape.strokeBorder(WidgetPalette.border, lineWidth: 1))
            .shadow(color: WidgetPalette.shadow, radius: 8, x: -2, y: 2)
    }
}

extension View {
    func edgeSurface() -> some View { modifier(EdgeSurface()) }
}

extension AttentionState {
    var workingCount: Int {
        rows.filter { $0.bucket == .working || $0.bucket == .needsYou }.count
    }

    var pendingApprovalCount: Int { pendingApprovals.count }

    /// The sessions tab's icon tint, worst state first: offline is muted, a
    /// pending approval is red, a session that needs the operator is orange,
    /// and everything-fine is the accent colour.
    var tabTint: Color {
        guard isConnected else { return .secondary }
        if pendingApprovalCount > 0 { return .red }
        if rows.contains(where: { $0.bucket == .needsYou || $0.bucket == .error }) { return .orange }
        return WidgetPalette.accent
    }

    /// The highest percent used across every connected provider's windows —
    /// what the usage tab shows. Nil when the daemon reports no usable
    /// provider (e.g. `no-auth` everywhere).
    var worstUsagePercent: Double? {
        usage?.providers
            .filter { $0.status != "no-auth" }
            .flatMap { $0.windows }
            .map { $0.percentUsed }
            .max()
    }
}

/// One tab: 44 pt wide, 64 pt tall.
struct EdgeTab: View {
    nonisolated static let size = CGSize(width: 44, height: 64)

    let systemImage: String
    let tint: Color
    let caption: String?
    var badge: Int = 0
    let accessibility: String

    var body: some View {
        VStack(spacing: 5) {
            ZStack(alignment: .topTrailing) {
                Image(systemName: systemImage)
                    .font(.system(size: 20, weight: .semibold))
                    .foregroundStyle(tint)
                    .frame(width: 26, height: 26)
                if badge > 0 {
                    Text("\(badge)")
                        .font(.caption2.bold())
                        .padding(.horizontal, 4)
                        .padding(.vertical, 1)
                        .background(.red, in: Capsule())
                        .foregroundStyle(.white)
                        .offset(x: 8, y: -6)
                }
            }
            if let caption {
                Text(caption)
                    .font(.caption2.monospacedDigit())
                    .foregroundStyle(.secondary)
            }
        }
        .frame(width: Self.size.width, height: Self.size.height)
        .contentShape(Rectangle())
        .edgeSurface()
        .accessibilityLabel(accessibility)
    }
}

/// The stack of both tabs. `size` is what the window geometry reserves.
struct EdgeTabs: View {
    nonisolated static let gap: CGFloat = 8
    nonisolated static let size = CGSize(width: EdgeTab.size.width, height: EdgeTab.size.height * 2 + gap)

    let state: AttentionState
    let onTap: (PanelKind) -> Void

    var body: some View {
        VStack(spacing: Self.gap) {
            EdgeTab(
                systemImage: state.isConnected ? "circle.hexagongrid.fill" : "wifi.slash",
                tint: state.tabTint,
                caption: state.isConnected ? "\(state.workingCount)" : nil,
                badge: state.pendingApprovalCount,
                accessibility: state.isConnected
                    ? "Swarmery sessions: \(state.workingCount) live, \(state.pendingApprovalCount) pending approvals"
                    : "Swarmery: daemon offline"
            )
            .onTapGesture { onTap(.sessions) }

            EdgeTab(
                systemImage: "gauge.with.dots.needle.33percent",
                tint: usageTint,
                caption: state.worstUsagePercent.map { "\(Int($0.rounded()))%" } ?? "—",
                accessibility: "Swarmery usage: \(state.worstUsagePercent.map { "\(Int($0.rounded()))% used" } ?? "unknown")"
            )
            .onTapGesture { onTap(.usage) }
        }
    }

    private var usageTint: Color {
        guard state.isConnected, let percent = state.worstUsagePercent else { return .secondary }
        return UsagePanelViewModel.tint(forPercentUsed: percent)
    }
}
