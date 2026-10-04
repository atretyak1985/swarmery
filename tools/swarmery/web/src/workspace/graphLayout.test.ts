// Unit tests for the layered DAG layout (fusion phase 10). Pure logic, no DOM.
//
// Runs with the rest of the web suite: `npm test` (vitest, also a swarmery-ci
// step). On its own: `npx vitest run src/workspace/graphLayout.test.ts`.
// web/tsconfig.json EXCLUDES *.test.ts, and vitest transpiles without type
// checking, so NOTHING type-checks this file — treat its types as documentation.

import { describe, expect, it } from 'vitest';
import { layerCount, layerNodes, layoutGraph, type LayoutNode } from './graphLayout';

describe('graphLayout', () => {
  it('lays out a diamond 1→{2,3}→4 in 3 layers', () => {
    const nodes: LayoutNode[] = [
      { id: '1', dependsOn: [] },
      { id: '2', dependsOn: ['1'] },
      { id: '3', dependsOn: ['1'] },
      { id: '4', dependsOn: ['2', '3'] },
    ];
    const layer = layerNodes(nodes);
    expect(layer.get('1')).toBe(0);
    expect(layer.get('2')).toBe(1);
    expect(layer.get('3')).toBe(1);
    expect(layer.get('4')).toBe(2); // longest path: 1→2→4 = depth 2
    expect(layerCount(nodes)).toBe(3);
  });

  it('places nodes at x = layer*gap and stacks rows within a layer', () => {
    const nodes: LayoutNode[] = [
      { id: 'a', dependsOn: [] },
      { id: 'b', dependsOn: [] },
      { id: 'c', dependsOn: ['a'] },
    ];
    const p = layoutGraph(nodes);
    const byId = new Map(p.map((x) => [x.id, x]));
    // a and b are both roots (layer 0), different rows.
    expect(byId.get('a')?.layer).toBe(0);
    expect(byId.get('b')?.layer).toBe(0);
    expect(byId.get('a')?.row).not.toBe(byId.get('b')?.row);
    // c depends on a → layer 1, so x > a.x.
    expect(byId.get('c')?.layer).toBe(1);
    expect((byId.get('c')?.x ?? 0) > (byId.get('a')?.x ?? 0)).toBe(true);
  });

  it('ignores dangling dependency ids (edge endpoint absent)', () => {
    const nodes: LayoutNode[] = [
      { id: 'x', dependsOn: ['ghost'] }, // ghost is not a present node
      { id: 'y', dependsOn: ['x'] },
    ];
    const layer = layerNodes(nodes);
    expect(layer.get('x')).toBe(0); // dangling dep does not push x down
    expect(layer.get('y')).toBe(1);
  });

  it('does not deadlock on a dependency cycle', () => {
    const nodes: LayoutNode[] = [
      { id: 'p', dependsOn: ['q'] },
      { id: 'q', dependsOn: ['p'] },
    ];
    // Just needs to return (bounded passes) without hanging.
    const layer = layerNodes(nodes);
    expect(layer.size).toBe(2);
  });

  it('returns an empty layout for no nodes', () => {
    expect(layoutGraph([])).toEqual([]);
    expect(layerCount([])).toBe(0);
  });
});
