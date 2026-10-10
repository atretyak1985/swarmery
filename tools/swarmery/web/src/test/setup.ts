// Vitest setup (vitest.config.ts → setupFiles): activate the i18n singleton in
// the source locale before any test module loads. Models that build messages
// with the core macros (t``, plural()) call i18n._() at run time, and Lingui
// throws when no locale is active — this keeps their plain unit tests working
// without each one importing the i18n module itself (plan D4).
import '../i18n';
