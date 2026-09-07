// The collapsed strip: how many sessions are working, a red badge with the
// pending-approval count, and the worst (highest-percent-used) usage window.
import SwiftUI

struct CompactStrip: View {
    let state: AttentionState

    private var workingCount: Int {
        state.rows.filter { $0.bucket == .working || $0.bucket == .needsYou }.count
    }

    private var pendingApprovalCount: Int { state.pendingApprovals.count }

    private var worstUsagePercent: Double? {
        state.usage?.providers
            .filter { $0.status != "no-auth" }
            .flatMap { $0.windows }
            .map { $0.percentUsed }
            .max()
    }

    var body: some View {
        HStack(spacing: 6) {
            if !state.isConnected {
                OfflineBadge()
            } else {
                Text("\(workingCount)").font(.callout.monospacedDigit())
                Text(workingCount == 1 ? "session" : "sessions")
                    .font(.caption)
                    .foregroundStyle(.secondary)
                if pendingApprovalCount > 0 {
                    Text("\(pendingApprovalCount)")
                        .font(.caption2.bold())
                        .padding(.horizontal, 5)
                        .padding(.vertical, 1)
                        .background(.red, in: Capsule())
                        .foregroundStyle(.white)
                }
                if let worstUsagePercent {
                    Text("\(Int(worstUsagePercent.rounded()))%")
                        .font(.caption2)
                        .foregroundStyle(.secondary)
                }
            }
        }
        .padding(.horizontal, 10)
        .frame(height: 24)
    }
}
