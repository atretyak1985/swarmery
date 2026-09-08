// Every usage window from GET /api/usage, with its percent and reset text --
// hidden entirely once no provider has anything to show (every provider that
// reports at all is "no-auth").
import SwiftUI

/// Pure projection from `UsageReport?` to what the strip renders -- kept
/// separate from the view so it is unit-testable without SwiftUI.
public struct UsageStripViewModel: Equatable {
    public struct WindowViewModel: Identifiable, Equatable {
        public let id: String
        public let label: String
        public let percentUsed: Double
        public let resetText: String?
    }

    public let windows: [WindowViewModel]

    /// `nil` means the strip renders nothing: there is no report yet, no
    /// provider reported a window, or every provider that has windows is
    /// `no-auth`.
    public init?(usage: UsageReport?) {
        guard let usage else { return nil }
        let visibleProviders = usage.providers.filter { $0.status != "no-auth" }
        let windows = visibleProviders.flatMap { $0.windows }
        guard !windows.isEmpty else { return nil }
        self.windows = windows.map {
            WindowViewModel(id: $0.key, label: $0.label, percentUsed: $0.percentUsed, resetText: $0.resetText)
        }
    }
}

struct UsageStrip: View {
    let usage: UsageReport?

    var body: some View {
        if let viewModel = UsageStripViewModel(usage: usage) {
            HStack(spacing: 10) {
                ForEach(viewModel.windows) { window in
                    VStack(alignment: .leading, spacing: 1) {
                        Text(window.label)
                            .font(.caption2)
                            .foregroundStyle(.secondary)
                        HStack(spacing: 4) {
                            Text("\(Int(window.percentUsed.rounded()))%")
                                .font(.caption.monospacedDigit())
                            if let resetText = window.resetText {
                                Text(resetText)
                                    .font(.caption2)
                                    .foregroundStyle(.secondary)
                            }
                        }
                    }
                }
            }
        }
    }
}
