-- Seed for internal/lessons/fromreview_test.go (verify-not-trust phase 8): the
-- review_findings of the reference plan 2026-10-09-phase-landing-repo-provider —
-- the 22 findings phase 7 backfilled into its nine phase docs from the
-- Completion Reports and PR #512 (workspace branch chore/vnt-7-backfill-landing-reviews,
-- 3ca1db40), with the type, file, criterion, evidence and note each block carries.
--
-- The repeats it holds (type @ area — phases):
--   correctness @ tools/swarmery/internal/repoprovider — 1, 2, 6 (credstore/ is one level down)
--   correctness @ tools/swarmery/internal/api          — 3, 4
--   correctness @ tools/swarmery/internal/wsingest     — 3, 4
--   quality     @ tools/swarmery/internal/api          — 5, 6
-- and the trap the depth bound exists for: contract @ tools/swarmery (README.md,
-- phase 2) lies two levels above contract @ tools/swarmery/internal/api (phase 4),
-- so it is NOT one group.
--
-- Load into a fully migrated store (store.Open); ids are fixed so tests can cite them.

INSERT INTO projects (id, path, slug, name, first_seen)
VALUES (90, '/repo/swarmery', 'swarmery', 'swarmery', '2026-10-09T00:00:00Z');

INSERT INTO tasks (id, project_id, title, prompt, status, created_at, started_at, source, external_id)
VALUES (900, 90, 'Phase landing via a RepoProvider', 'goal', 'done', '2026-10-09T00:00:00Z',
        '2026-10-09T00:00:00Z', 'workspace', '2026-10-09-phase-landing-repo-provider');

INSERT INTO epic_phases (id, workspace_task_id, seq, name, doc_path, depends_on) VALUES
  (901, 900, 1, 'Phase 1 — RepoProvider core',         '/ws/plan/phase-1-repoprovider-core.md',      '[]'),
  (902, 900, 2, 'Phase 2 — Board land and scrub',      '/ws/plan/phase-2-board-land-and-scrub.md',   '[1]'),
  (903, 900, 3, 'Phase 3 — Phase landing API',         '/ws/plan/phase-3-phase-landing-api.md',      '[1]'),
  (904, 900, 4, 'Phase 4 — Return to agent',           '/ws/plan/phase-4-return-to-agent.md',        '[3]'),
  (905, 900, 5, 'Phase 5 — Plans review UI',           '/ws/plan/phase-5-plans-review-ui.md',        '[3]'),
  (906, 900, 6, 'Phase 6 — GitLab provider',           '/ws/plan/phase-6-gitlab-provider.md',        '[1]'),
  (907, 900, 7, 'Phase 7 — Status poller',             '/ws/plan/phase-7-status-poller.md',          '[3]'),
  (908, 900, 8, 'Phase 8 — Device flow and PAT',       '/ws/plan/phase-8-device-flow-and-pat.md',    '[1]'),
  (909, 900, 9, 'Phase 9 — Quality gate',              '/ws/plan/phase-9-quality-gate.md',           '[1,2,3,4,5,6,7,8]');

INSERT INTO review_blocks (workspace_task_id, phase_id, doc_path, round, verdict, complete, scope_left_json, resolves_json, source, written_at) VALUES
  (900, 901, '/ws/plan/phase-1-repoprovider-core.md',    '1', 'FAIL',         1, '[]', '[]', 'backfill', '2026-10-22T17:30:00Z'),
  (900, 901, '/ws/plan/phase-1-repoprovider-core.md',    '2', 'PASS',         1, '[]', '["R1-1","R1-2","R1-3","R1-4"]', 'backfill', '2026-10-22T17:30:00Z'),
  (900, 902, '/ws/plan/phase-2-board-land-and-scrub.md', '1', 'FAIL',         1, '[]', '[]', 'backfill', '2026-10-22T17:30:00Z'),
  (900, 903, '/ws/plan/phase-3-phase-landing-api.md',    '1', 'FAIL',         1, '[]', '[]', 'backfill', '2026-10-22T17:30:00Z'),
  (900, 904, '/ws/plan/phase-4-return-to-agent.md',      '1', 'FAIL',         1, '[]', '[]', 'backfill', '2026-10-22T17:30:00Z'),
  (900, 905, '/ws/plan/phase-5-plans-review-ui.md',      '1', 'FAIL',         0, '["DiffView/TaskDiff","PhaseCard","mocks"]', '[]', 'backfill', '2026-10-22T17:30:00Z'),
  (900, 906, '/ws/plan/phase-6-gitlab-provider.md',      '1', 'FAIL',         1, '[]', '[]', 'backfill', '2026-10-22T17:30:00Z'),
  (900, 907, '/ws/plan/phase-7-status-poller.md',        '1', 'PASS',         1, '[]', '[]', 'backfill', '2026-10-22T17:30:00Z'),
  (900, 908, '/ws/plan/phase-8-device-flow-and-pat.md',  '1', 'PASS',         1, '[]', '[]', 'backfill', '2026-10-22T17:30:00Z'),
  (900, 909, '/ws/plan/phase-9-quality-gate.md',         '1', 'INCONCLUSIVE', 0, '[]', '[]', 'backfill', '2026-10-22T17:30:00Z');

INSERT INTO review_findings (id, workspace_task_id, phase_id, doc_path, round, finding_id, type, file, criterion, evidence, note, resolved_by, source, created_at) VALUES
  -- phase 1 (all four resolved by round 2)
  (1,  900, 901, '/ws/plan/phase-1-repoprovider-core.md', '1', 'R1-1', 'correctness',      'tools/swarmery/internal/repoprovider/errors.go',            'SC-3', 'b69ee1a3 (PR #512); TestClassifyRedactsBeforeTruncating, TestRedactedTailTokenAtCutBoundary', 'P1: tool output was truncated before it was redacted, so a token at the cut survived as a fragment.', '2', 'backfill', '2026-10-22T17:35:00Z'),
  (2,  900, 901, '/ws/plan/phase-1-repoprovider-core.md', '1', 'R1-2', 'plan-conformance', 'tools/swarmery/internal/repoprovider/github/github.go',     'SC-3', 'd296694b (PR #512); TestCallsUseNilEnvWithoutStore, TestCallsUseStoreEnvWhenTokenStored', 'Plan gap: gh-CLI-only operators would lose auth under the isolated GH_CONFIG_DIR when no token is stored.', '2', 'backfill', '2026-10-22T17:35:00Z'),
  (3,  900, 901, '/ws/plan/phase-1-repoprovider-core.md', '1', 'R1-3', 'correctness',      'tools/swarmery/internal/repoprovider/credstore/credstore.go', 'SC-3', '6c3f0a24 (PR #512); TestWriteRefusesOpenSecretsDir', 'Write refuses a secrets dir open to group/other instead of writing a store Load rejects.', '2', 'backfill', '2026-10-22T17:35:00Z'),
  (4,  900, 901, '/ws/plan/phase-1-repoprovider-core.md', '1', 'R1-4', 'silent-failure',   'tools/swarmery/internal/repoprovider/detect.go',            '',     'fcfd8793 (PR #512); TestParseRemoteErrorHidesCredentials', 'ParseRemote errors now strip userinfo and redact the remote URL.', '2', 'backfill', '2026-10-22T17:35:00Z'),
  -- phase 2
  (5,  900, 902, '/ws/plan/phase-2-board-land-and-scrub.md', '1', 'R1-1', 'correctness', 'tools/swarmery/internal/repoprovider/detect.go', 'SC-1', '758ff0ca (PR #512); TestLandThroughSSHAliasResolvesHost, TestLandThroughGitHubSSH443, TestDetectResolvesSSHHost', 'Blocker: board land for ssh_config Host aliases and ssh.github.com:443 origins went from 200 to 422.', NULL, 'backfill', '2026-10-22T17:35:00Z'),
  (6,  900, 902, '/ws/plan/phase-2-board-land-and-scrub.md', '1', 'R1-2', 'contract',    'tools/swarmery/README.md',                       'SC-4', '2fc5148c + 18114bb5 (PR #512)', 'Blocker: the README overstated SWARMERY_AGENT_SCRUB_VCS_TOKENS both ways.', NULL, 'backfill', '2026-10-22T17:35:00Z'),
  -- phase 3
  (7,  900, 903, '/ws/plan/phase-3-phase-landing-api.md', '1', 'R1-1', 'correctness', 'tools/swarmery/internal/wsingest/epics.go',     'SC-8',  '330791c4 (PR #512); TestApplyEpicsCarriesLandingAcrossRename', 'D2: carryAcrossRenames dropped the eight 0103 landing columns, so a renamed pr_open phase read as ready again.', NULL, 'backfill', '2026-10-22T17:35:00Z'),
  (8,  900, 903, '/ws/plan/phase-3-phase-landing-api.md', '1', 'R1-2', 'correctness', 'tools/swarmery/internal/api/phase_landing.go', 'SC-2',  '35142284 (PR #512); TestLandPhaseMultiRepoUsesProjectVcsConfig', 'D5: a multi-repo plan phase read repoprovider.Config from the sub-repo''s .claude/, ignoring the project''s vcs.provider.', NULL, 'backfill', '2026-10-22T17:35:00Z'),
  (9,  900, 903, '/ws/plan/phase-3-phase-landing-api.md', '1', 'R1-3', 'correctness', 'tools/swarmery/internal/api/phase_landing.go', 'SC-10', 'c4d0a608 (PR #512); TestLandPhasePushToCheckedOutBranchRefusedOnStartPointFallback', 'On the start-point base fallback the push-to-base refusal could never fire.', NULL, 'backfill', '2026-10-22T17:35:00Z'),
  -- phase 4
  (10, 900, 904, '/ws/plan/phase-4-return-to-agent.md', '1', 'R1-1', 'correctness', 'tools/swarmery/internal/api/phase_landing.go', 'SC-9', 'af2f52e8 (PR #512); TestLandPhaseReturnPROpenRestoredAfterRun, TestLandPhaseReturnMergedIs409', 'Blocker 1: returning a pr_open/merged phase lost its landing state.', NULL, 'backfill', '2026-10-22T17:35:00Z'),
  (11, 900, 904, '/ws/plan/phase-4-return-to-agent.md', '1', 'R1-2', 'correctness', 'tools/swarmery/internal/phaserun/service.go',  'SC-9', '694b1375 (PR #512); TestReturnedRefusedStartRetriedByPlainRun, TestLandPhaseReturnWhileSlotHeld409', 'Blocker 2: a refused return start had no retry path.', NULL, 'backfill', '2026-10-22T17:35:00Z'),
  (12, 900, 904, '/ws/plan/phase-4-return-to-agent.md', '1', 'R1-3', 'quality',     'tools/swarmery/internal/worktree/worktree.go', '',     '062b9dbb (PR #512); TestAcquireExistingChecksOutTheBranchAsItStands', 'Non-blocking: branch -D crash window when a returned run re-cut its branch.', NULL, 'backfill', '2026-10-22T17:35:00Z'),
  (13, 900, 904, '/ws/plan/phase-4-return-to-agent.md', '1', 'R1-4', 'contract',    'tools/swarmery/internal/api/phaserun.go',      '',     '2e880bf2 (PR #512); TestLandPhaseReturnStartRefUnresolvedIs409', 'Non-blocking: the return path''s refusal mapping had drifted (start-ref-unresolved answered 500, not 409).', NULL, 'backfill', '2026-10-22T17:35:00Z'),
  (14, 900, 904, '/ws/plan/phase-4-return-to-agent.md', '1', 'R1-5', 'correctness', 'tools/swarmery/internal/phaserun/service.go',  'SC-9', 'af2f52e8 (PR #512); TestReturnedRunRestoresOpenPR', 'Non-blocking: the HealStale/terminal reset blanked a returned phase to none instead of restoring pr_open.', NULL, 'backfill', '2026-10-22T17:35:00Z'),
  (15, 900, 904, '/ws/plan/phase-4-return-to-agent.md', '1', 'R1-6', 'correctness', 'tools/swarmery/internal/wsingest/feedback.go', 'SC-9', 'f79edaa1 (PR #512); TestAppendOperatorFeedbackCannotInjectStructure', 'Non-blocking: operator feedback could inject doc structure (a heading or a checkbox).', NULL, 'backfill', '2026-10-22T17:35:00Z'),
  -- phase 5
  (16, 900, 905, '/ws/plan/phase-5-plans-review-ui.md', '1', 'R1-1', 'correctness', 'tools/swarmery/web/src/pages/plans/PhaseReview.tsx',      '',      'cd9d3175 (PR #512); PhaseReview.test.tsx', 'P1: an in-flight land on phase A could write its result onto phase B after up/down navigation.', NULL, 'backfill', '2026-10-22T17:35:00Z'),
  (17, 900, 905, '/ws/plan/phase-5-plans-review-ui.md', '1', 'R1-2', 'quality',     'tools/swarmery/web/src/components/VcsAuthBanner.tsx',     'SC-12', 'fcf1ff07 (PR #512); VcsAuthBanner.test.tsx', 'Non-blocking: an unknown auth status was shown as not signed in.', NULL, 'backfill', '2026-10-22T17:35:00Z'),
  (18, 900, 905, '/ws/plan/phase-5-plans-review-ui.md', '1', 'R1-3', 'quality',     'tools/swarmery/internal/api/vcs.go',                      '',      '7d9c60be (PR #512); TestProjectVcsConcurrentColdReadsShareOneProbe', 'Non-blocking: no single-flight on the cold /vcs cache.', NULL, 'backfill', '2026-10-22T17:35:00Z'),
  (19, 900, 905, '/ws/plan/phase-5-plans-review-ui.md', '1', 'R1-4', 'correctness', 'tools/swarmery/web/src/pages/plans/landingModel.ts',      '',      'e2e4b368 (PR #512); Plans.depsUnmerged.test.tsx', 'Non-blocking: the deps-unmerged Open buttons vanished when /vcs failed.', NULL, 'backfill', '2026-10-22T17:35:00Z'),
  (20, 900, 905, '/ws/plan/phase-5-plans-review-ui.md', '1', 'R1-5', 'quality',     'tools/swarmery/web/scripts/check-no-provider-branching.sh', 'SC-11', '340db9ca (PR #512)', 'Non-blocking: ratchet gaps in the provider-branching check.', NULL, 'backfill', '2026-10-22T17:35:00Z'),
  -- phase 6
  (21, 900, 906, '/ws/plan/phase-6-gitlab-provider.md', '1', 'R1-1', 'correctness', 'tools/swarmery/internal/repoprovider/ask.go', '', 'b6fab62b (PR #512); TestPersistProviderAnswerKeepsExistingMode', 'P1: PersistProviderAnswer rewrote a 0600 settings.local.json holding env secrets as 0644.', NULL, 'backfill', '2026-10-22T17:35:00Z'),
  (22, 900, 906, '/ws/plan/phase-6-gitlab-provider.md', '1', 'R1-2', 'quality',     'tools/swarmery/internal/api/tasks_diff.go',   '', 'f43c4c30 (PR #512); TestShellQuote, TestShellQuoteRoundTripsThroughSh', 'Non-blocking: user values in copy-paste land hints were not shell-quoted, so $(…) in a title could run on paste.', NULL, 'backfill', '2026-10-22T17:35:00Z');
