// Layered DAG layout (fusion phase 10 — dependency graph): assigns every node a
// layer (x-band) by its longest-path depth from a root, and a row (y-slot)
// within that layer. Pure + framework-free so it is trivially unit-tested (the
// diamond shape 1→{2,3}→4 must lay out in 3 layers). @xyflow/react consumes the
// {x,y} this produces; it does no layout of its own.

/** A node to lay out: an id and the ids it depends ON (its in-edges). */
export interface LayoutNode {
  id: string;
  /** Ids this node depends on. Edges point dependency → dependent. */
  dependsOn: string[];
}

/** Computed placement: layer (0-based depth) + row within the layer + x/y px. */
export interface Placement {
  id: string;
  layer: number;
  row: number;
  x: number;
  y: number;
}

/** Horizontal gap between layers and vertical gap between rows (px). */
export const LAYER_GAP_X = 220;
export const ROW_GAP_Y = 96;

/**
 * layerNodes assigns each node its longest-path depth from a root. Only edges
 * whose endpoints are BOTH present contribute (a dangling dependency id is
 * ignored, not treated as a phantom node). A dependency cycle cannot deadlock:
 * nodes still on the queue after a bounded number of passes are pinned to their
 * best-known layer. Returns a map id → layer.
 */
export function layerNodes(nodes: LayoutNode[]): Map<string, number> {
  const present = new Set(nodes.map((n) => n.id));
  const layer = new Map<string, number>();
  for (const n of nodes) layer.set(n.id, 0);

  // Relax layers: a node sits one below the deepest dependency it has. Iterate
  // until stable or a cycle bound (|nodes|) is hit — longest-path in a DAG
  // converges in ≤ |nodes| passes.
  const maxPasses = nodes.length + 1;
  for (let pass = 0; pass < maxPasses; pass++) {
    let changed = false;
    for (const n of nodes) {
      let best = 0;
      for (const dep of n.dependsOn) {
        if (!present.has(dep)) continue;
        best = Math.max(best, (layer.get(dep) ?? 0) + 1);
      }
      if (best !== (layer.get(n.id) ?? 0)) {
        layer.set(n.id, best);
        changed = true;
      }
    }
    if (!changed) break;
  }
  return layer;
}

/**
 * layoutGraph places nodes left-to-right by dependency depth. Within a layer,
 * rows preserve the input order (stable, so re-renders don't jump). Node order
 * within the whole graph is otherwise the caller's responsibility.
 */
export function layoutGraph(nodes: LayoutNode[]): Placement[] {
  const layer = layerNodes(nodes);
  const rowCursor = new Map<number, number>();
  const out: Placement[] = [];
  for (const n of nodes) {
    const l = layer.get(n.id) ?? 0;
    const row = rowCursor.get(l) ?? 0;
    rowCursor.set(l, row + 1);
    out.push({ id: n.id, layer: l, row, x: l * LAYER_GAP_X, y: row * ROW_GAP_Y });
  }
  return out;
}

/** layerCount is the number of distinct layers (max layer + 1), 0 for empty. */
export function layerCount(nodes: LayoutNode[]): number {
  if (nodes.length === 0) return 0;
  let max = 0;
  for (const l of layerNodes(nodes).values()) max = Math.max(max, l);
  return max + 1;
}
