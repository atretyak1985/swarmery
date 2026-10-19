// Vitest setup (vitest.config.ts → setupFiles): activate the i18n singleton in
// the source locale before any test module loads. Models that build messages
// with the core macros (t``, plural()) call i18n._() at run time, and Lingui
// throws when no locale is active — this keeps their plain unit tests working
// without each one importing the i18n module itself (plan D4).
import '../i18n';
import { configure } from '@testing-library/react';

// Testing Library's 1 s default for findBy*/waitFor is not a contract here. The
// first mount of Plans.tsx in a test file is CPU-bound (cold render plus the
// stubbed-fetch chain): under scripts/tests/under-load.sh (16 burners) it
// measured min 1001 / median 1277 / max 2909 ms over 10 runs, all past the
// 1 s default, and every mount completed (70 mounts: min 49, median 317,
// max 2909 ms) — late, not lost. 5 s covers that with headroom; vitest.config.ts raises
// testTimeout to 15 s so a 5 s wait cannot run into vitest's own 5 s default.
configure({ asyncUtilTimeout: 5000 });
