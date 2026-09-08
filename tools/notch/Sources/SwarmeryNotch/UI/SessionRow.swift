// One session line in the expanded panel: a status dot for its bucket,
// title/why, and actions to focus the owning terminal (or open the
// dashboard for a headless session) and to stop it.
import SwiftUI

struct SessionRow: View {
    let row: AttentionState.Row
    let actions: WidgetActions

    private var session: Session { row.session }

    var body: some View {
        HStack(spacing: 8) {
            Circle()
                .fill(color(for: row.bucket))
                .frame(width: 8, height: 8)
            VStack(alignment: .leading, spacing: 1) {
                Text(session.title ?? session.projectName ?? "Session \(session.id)")
                    .font(.callout)
                    .lineLimit(1)
                if let why = session.why {
                    Text(why)
                        .font(.caption2)
                        .foregroundStyle(.secondary)
                        .lineLimit(1)
                }
            }
            Spacer()
            Button {
                if session.terminal != nil {
                    actions.focus(session)
                } else {
                    actions.openDashboard(session)
                }
            } label: {
                Image(systemName: session.terminal != nil ? "arrow.up.forward.app" : "safari")
            }
            .buttonStyle(.plain)
            Button {
                actions.stop(session)
            } label: {
                Image(systemName: "stop.circle")
            }
            .buttonStyle(.plain)
        }
        .padding(.vertical, 2)
    }

    private func color(for bucket: AttentionState.RowBucket) -> Color {
        return switch bucket {
        case .needsYou: .orange
        case .error: .red
        case .working: .blue
        case .idle: .secondary
        }
    }
}
