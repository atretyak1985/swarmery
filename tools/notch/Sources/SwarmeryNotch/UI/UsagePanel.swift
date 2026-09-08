// The usage panel opened from the usage tab: the dashboard's Usage modal,
// re-expressed natively. One card per provider (per account when the daemon
// reports more than one), each window with a progress bar, percent used /
// left, the reset text and time, and the pace line the daemon computes.
import SwiftUI

/// Pure projection of `UsageReport` for the panel — what to show, grouped
/// by account, with the bar tint decided once so views and tests agree.
public struct UsagePanelViewModel: Equatable {
    public struct Window: Identifiable, Equatable {
        public let id: String
        public let label: String
        public let percentUsed: Double
        public let percentLeft: Double
        public let resetText: String?
        public let resetClock: String?
        public let pace: String?
        public let paceStatus: String?

        /// Green up to 70%, orange up to 90%, red above — the same thresholds
        /// the dashboard's bar uses.
        public var tint: Color { UsagePanelViewModel.tint(forPercentUsed: percentUsed) }
    }

    public struct Provider: Identifiable, Equatable {
        public let id: String
        public let name: String
        public let plan: String?
        public let status: String
        public let error: String?
        public let windows: [Window]
    }

    public struct Account: Identifiable, Equatable {
        public let id: String
        public let providers: [Provider]
    }

    public let accounts: [Account]
    public let generatedClock: String?

    public init?(usage: UsageReport?) {
        guard let usage else { return nil }
        let grouped: [UsageAccount] = usage.accounts.isEmpty
            ? [UsageAccount(account: "default", providers: usage.providers)]
            : usage.accounts
        let accounts = grouped.map { account in
            Account(
                id: account.account,
                providers: account.providers
                    .filter { $0.status != "no-auth" }
                    .map { provider in
                        Provider(
                            id: "\(account.account)/\(provider.name)",
                            name: provider.name,
                            plan: provider.plan,
                            status: provider.status,
                            error: provider.error,
                            windows: provider.windows.map { window in
                                Window(
                                    id: "\(account.account)/\(provider.name)/\(window.key)",
                                    label: window.label,
                                    percentUsed: window.percentUsed,
                                    percentLeft: window.percentLeft,
                                    resetText: window.resetText,
                                    resetClock: Self.clock(iso: window.resetAt),
                                    pace: window.pace?.message,
                                    paceStatus: window.pace?.status
                                )
                            }
                        )
                    }
            )
        }
        .filter { !$0.providers.isEmpty }
        guard !accounts.isEmpty else { return nil }
        self.accounts = accounts
        self.generatedClock = Self.clock(iso: usage.generatedAt, withSeconds: true)
    }

    public static func tint(forPercentUsed percent: Double) -> Color {
        if percent >= 90 { return .red }
        if percent >= 70 { return .orange }
        return .green
    }

    /// "4:19 PM" / "Sat 4:59 PM" style — a day prefix only when the reset is
    /// not today, mirroring the dashboard.
    static func clock(iso: String?, withSeconds: Bool = false, now: Date = Date()) -> String? {
        guard let iso, let date = Self.parse(iso) else { return nil }
        let formatter = DateFormatter()
        formatter.locale = Locale(identifier: "en_US_POSIX")
        let sameDay = Calendar.current.isDate(date, inSameDayAs: now)
        formatter.dateFormat = (sameDay ? "" : "EEE ") + (withSeconds ? "h:mm:ss a" : "h:mm a")
        return formatter.string(from: date)
    }

    private static func parse(_ iso: String) -> Date? {
        let fractional = ISO8601DateFormatter()
        fractional.formatOptions = [.withInternetDateTime, .withFractionalSeconds]
        if let date = fractional.date(from: iso) { return date }
        return ISO8601DateFormatter().date(from: iso)
    }
}

struct UsagePanel: View {
    let usage: UsageReport?
    let onCollapse: () -> Void
    @State private var selectedAccount: String?

    var body: some View {
        VStack(alignment: .leading, spacing: 10) {
            HStack(spacing: 6) {
                Image(systemName: "gauge.with.dots.needle.33percent")
                    .foregroundStyle(WidgetPalette.accent)
                Text("Usage").font(.callout.bold())
                Spacer()
                Button(action: onCollapse) { Image(systemName: "chevron.right") }
                    .buttonStyle(.plain)
                    .foregroundStyle(.secondary)
                    .accessibilityLabel("Collapse")
            }
            if let model = UsagePanelViewModel(usage: usage) {
                if model.accounts.count > 1 {
                    Picker("Account", selection: Binding(
                        get: { selectedAccount ?? model.accounts[0].id },
                        set: { selectedAccount = $0 }
                    )) {
                        ForEach(model.accounts) { account in
                            Text(account.id).tag(account.id)
                        }
                    }
                    .pickerStyle(.segmented)
                    .labelsHidden()
                }
                let account = model.accounts.first { $0.id == selectedAccount } ?? model.accounts[0]
                ForEach(account.providers) { provider in
                    providerCard(provider)
                }
                if let generatedClock = model.generatedClock {
                    Text("Updated \(generatedClock)")
                        .font(.caption2)
                        .foregroundStyle(.secondary)
                }
            } else {
                Text(usage == nil ? "No usage data from the daemon yet." : "No connected provider.")
                    .font(.caption)
                    .foregroundStyle(.secondary)
            }
        }
        .padding(12)
        .frame(width: ExpandedPanel.panelWidth)
    }

    private func providerCard(_ provider: UsagePanelViewModel.Provider) -> some View {
        VStack(alignment: .leading, spacing: 8) {
            HStack(spacing: 6) {
                Image(systemName: "diamond.fill")
                    .font(.caption2)
                    .foregroundStyle(.orange)
                Text(provider.name).font(.callout.bold())
                if let plan = provider.plan {
                    Text(plan)
                        .font(.caption2)
                        .padding(.horizontal, 6)
                        .padding(.vertical, 1)
                        .background(Color.primary.opacity(0.08), in: Capsule())
                }
                Spacer()
                if provider.status != "ok" {
                    Text(provider.error ?? provider.status)
                        .font(.caption2)
                        .foregroundStyle(.red)
                        .lineLimit(1)
                }
            }
            ForEach(provider.windows) { window in
                windowRow(window)
            }
        }
        .padding(10)
        .background(Color.primary.opacity(0.05), in: RoundedRectangle(cornerRadius: 10))
    }

    private func windowRow(_ window: UsagePanelViewModel.Window) -> some View {
        VStack(alignment: .leading, spacing: 4) {
            HStack {
                Text(window.label).font(.caption.bold())
                Spacer()
                Text("\(Int(window.percentUsed.rounded()))% used")
                    .font(.caption.monospacedDigit())
            }
            GeometryReader { proxy in
                ZStack(alignment: .leading) {
                    Capsule().fill(Color.primary.opacity(0.08))
                    Capsule()
                        .fill(window.tint)
                        .frame(width: max(4, proxy.size.width * min(max(window.percentUsed, 0), 100) / 100))
                }
            }
            .frame(height: 6)
            HStack {
                Text("\(Int(window.percentLeft.rounded()))% left")
                    .font(.caption2.monospacedDigit())
                    .foregroundStyle(.secondary)
                Spacer()
                if let resetText = window.resetText {
                    Text(resetText).font(.caption2.italic()).foregroundStyle(.secondary)
                }
                if let resetClock = window.resetClock {
                    Text(resetClock).font(.caption2.monospacedDigit())
                }
            }
            if let pace = window.pace {
                Label(pace, systemImage: window.paceStatus == "ahead" ? "arrow.up.right" : "arrow.down.right")
                    .font(.caption2)
                    .foregroundStyle(window.paceStatus == "ahead" ? .orange : .green)
            }
        }
        .padding(8)
        .background(Color.primary.opacity(0.04), in: RoundedRectangle(cornerRadius: 8))
    }
}
