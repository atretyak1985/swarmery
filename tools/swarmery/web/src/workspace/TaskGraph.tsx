// Board dependency graph (fusion phase 10): a read-only @xyflow/react view of
// the board's task DAG — nodes = tasks (colored by column, with a verdict
// badge), edges = the `dependencies` (external_id) links. Layout is computed by
// the pure layoutGraph helper (layered left-to-right by dependency depth), NOT
// by xyflow. Clicking a node opens the same TaskDrawer as the board.
//
// Accessibility: xyflow's canvas is not keyboard-reachable, so a visually
// hidden but screen-reader-available <ul> mirrors every node with its column
// and dependencies and is focusable — the graph is never the ONLY way to read
// the DAG (WCAG AA).

import { useMemo } from 'react';
import {
  Background,
  Controls,
  type Edge,
  type Node,
  Position,
  ReactFlow,
} from '@xyflow/react';
import '@xyflow/react/dist/style.css';
import type { BoardColumn, BoardTask } from '../api/types';
import { COLUMN_LABELS } from './boardModel';
import { layoutGraph, type LayoutNode } from './graphLayout';

/** Per-column node accent (border + faint fill), matching the board palette. */
const COLUMN_NODE_STYLE: Record<BoardColumn, { border: string; bg: string }> = {
  triage: { border: '#3f3f46', bg: 'rgba(63,63,70,0.15)' },
  todo: { border: '#6366f1', bg: 'rgba(99,102,241,0.12)' },
  in_progress: { border: '#f59e0b', bg: 'rgba(245,158,11,0.12)' },
  in_review: { border: '#a855f7', bg: 'rgba(168,85,247,0.12)' },
  done: { border: '#22c55e', bg: 'rgba(34,197,94,0.12)' },
  archived: { border: '#3f3f46', bg: 'rgba(63,63,70,0.10)' },
};

const VERDICT_MARK: Record<string, string> = {
  pass: '✓',
  fail: '✗',
  inconclusive: '?',
};

export function TaskGraph({
  tasks,
  onOpen,
}: {
  tasks: BoardTask[];
  onOpen: (id: number) => void;
}): JSX.Element {
  // Index by external_id so dependency links resolve to node ids.
  const byExtId = useMemo(() => {
    const m = new Map<string, BoardTask>();
    for (const t of tasks) m.set(t.externalId, t);
    return m;
  }, [tasks]);

  const { nodes, edges } = useMemo(() => {
    const layoutNodes: LayoutNode[] = tasks.map((t) => ({
      id: String(t.id),
      // Keep only dependency ids that resolve to a present task node.
      dependsOn: t.dependencies.map((ext) => byExtId.get(ext)).filter((d): d is BoardTask => d !== undefined).map((d) => String(d.id)),
    }));
    const placements = new Map(layoutGraph(layoutNodes).map((p) => [p.id, p]));

    const rfNodes: Node[] = tasks.map((t) => {
      const p = placements.get(String(t.id));
      const style = COLUMN_NODE_STYLE[t.boardColumn];
      const mark = t.verifyVerdict !== null ? (VERDICT_MARK[t.verifyVerdict] ?? '') : '';
      return {
        id: String(t.id),
        position: { x: p?.x ?? 0, y: p?.y ?? 0 },
        data: { label: `${mark ? mark + ' ' : ''}${t.title}` },
        style: {
          border: `1px solid ${style.border}`,
          background: style.bg,
          borderRadius: 10,
          padding: '6px 10px',
          fontSize: 11,
          width: 180,
          color: 'var(--color-ink, #e5e5e5)',
        },
        sourcePosition: Position.Right,
        targetPosition: Position.Left,
      };
    });

    const rfEdges: Edge[] = [];
    for (const t of tasks) {
      for (const ext of t.dependencies) {
        const dep = byExtId.get(ext);
        if (!dep) continue; // dangling dependency — skip
        rfEdges.push({
          id: `${String(dep.id)}->${String(t.id)}`,
          source: String(dep.id),
          target: String(t.id),
          animated: t.boardColumn === 'in_progress',
          style: { stroke: '#52525b' },
        });
      }
    }
    return { nodes: rfNodes, edges: rfEdges };
  }, [tasks, byExtId]);

  if (tasks.length === 0) {
    return (
      <div className="flex flex-1 items-center justify-center px-1 py-2 font-mono text-[11px] text-ink-faint">
        no tasks to graph
      </div>
    );
  }

  return (
    <div className="relative flex min-h-0 flex-1 rounded-xl border border-line bg-surface/40">
      <ReactFlow
        nodes={nodes}
        edges={edges}
        fitView
        nodesDraggable={false}
        nodesConnectable={false}
        elementsSelectable
        proOptions={{ hideAttribution: true }}
        onNodeClick={(_e, node) => onOpen(Number(node.id))}
      >
        <Background gap={16} color="var(--color-line-soft, #27272a)" />
        <Controls showInteractive={false} />
      </ReactFlow>

      {/* WCAG: a keyboard/SR-reachable list mirror of the DAG. */}
      <ul className="sr-only">
        {tasks.map((t) => (
          <li key={t.id}>
            <button type="button" onClick={() => onOpen(t.id)}>
              {t.title} — column {COLUMN_LABELS[t.boardColumn]}
              {t.dependencies.length > 0
                ? `, depends on ${t.dependencies
                    .map((ext) => byExtId.get(ext)?.title ?? ext)
                    .join(', ')}`
                : ', no dependencies'}
            </button>
          </li>
        ))}
      </ul>
    </div>
  );
}
