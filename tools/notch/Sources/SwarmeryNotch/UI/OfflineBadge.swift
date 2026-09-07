// Shown instead of session/usage content whenever the WS connection to the
// daemon is down, so a widget with no data does not silently masquerade as
// "nothing going on".
import SwiftUI

struct OfflineBadge: View {
    var body: some View {
        Label("Daemon offline", systemImage: "wifi.slash")
            .font(.caption)
            .foregroundStyle(.secondary)
    }
}
