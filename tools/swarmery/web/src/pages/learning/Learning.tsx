// Learning (Canvas v3 phase 6, artboards 1e/1f): one place replacing /lessons
// and /decisions, with tabs Lessons · The classifier · Forecast honesty · Proof.
// The tab lives in `?tab=` so the retired routes redirect onto a specific tab.
// Lessons, decisions and calibration take no project, so /p/:slug/learning
// shows the same fleet data; only the Inbox links follow the scope.

import { useEffect, useState } from 'react';
import { useParams } from 'react-router-dom';
import { fetchLessons } from '../../api/lessons';
import { type TabItem, Tabs, useTabParam } from '../../components/Tabs';
import { PLACES } from '../../lib/nav';
import { Lessons } from '../Lessons';
import { CalibrationPanel } from '../LessonsCalibration';
import { Classifier } from './Classifier';
import { Proof } from './Proof';

export const LEARNING_TABS = ['lessons', 'classifier', 'honesty', 'proof'] as const;
export type LearningTab = (typeof LEARNING_TABS)[number];

function inboxHref(slug: string | null): string {
  return PLACES.find((p) => p.id === 'inbox')?.href(slug) ?? '/inbox';
}

export function Learning(): JSX.Element {
  const { slug } = useParams<{ slug: string }>();
  const projectSlug = slug ?? null;
  const [tab, setTab] = useTabParam<LearningTab>('tab', LEARNING_TABS, 'lessons');
  const [candidates, setCandidates] = useState<number | null>(null);

  useEffect(() => {
    fetchLessons('candidate')
      .then((ls) => setCandidates(ls.length))
      .catch(() => setCandidates(null));
  }, []);

  const inbox = inboxHref(projectSlug);
  const tabs: TabItem<LearningTab>[] = [
    { id: 'lessons', label: 'Lessons', ...(candidates !== null && candidates > 0 ? { count: candidates } : {}) },
    { id: 'classifier', label: 'The classifier' },
    { id: 'honesty', label: 'Forecast honesty' },
    { id: 'proof', label: 'Proof' },
  ];

  return (
    <div className="px-9 pt-[30px] pb-[34px]">
      <div className="flex flex-wrap items-baseline gap-[10px]">
        <h1 className="m-0 font-display text-[30px] leading-[1.15] font-medium tracking-[-0.01em] text-ink">
          Learning
        </h1>
        <span className="font-mono text-[11px] text-ink-faint">
lessons · the classifier · forecast honesty</span>
      </div>
      <div className="mt-[14px]">
        <Tabs tabs={tabs} value={tab} onChange={setTab} ariaLabel="Learning" />
      </div>
      <div role="tabpanel" aria-label={tabs.find((t) => t.id === tab)?.label} className="mt-4">
        {tab === 'lessons' ? (
          <Lessons embedded />
        ) : tab === 'classifier' ? (
          <Classifier inboxHref={`${inbox}?tab=classifier`} />
        ) : tab === 'honesty' ? (
          <CalibrationPanel />
        ) : (
          <Proof inboxHref={inbox} />
        )}
      </div>
    </div>
  );
}
