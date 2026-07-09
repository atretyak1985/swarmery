---
name: functional-design
description: "EXECUTES refactors of TypeScript business logic in the main app toward pure functions, immutability, and data-flow pipelines -- edits files directly via Edit. NOT for planning-only refactors with no code changes (use refactor-plan). NOT for cloud service config, I/O-heavy route handlers, or Python/firmware paths in the device/edge repo (project.json → device)."
version: "1.0.0"
owner: "agentry-core"
allowed-tools: Read, Edit, Grep
color: teal
---

# Purpose

Refactor business logic to follow functional programming principles -- immutability, pure functions, composition, and data-flow pipelines. Targets TypeScript code in the main app's `src/lib/` services where pure transformations live (see `.claude/project.json` → `mainApp`). This skill writes to files via Edit; every change produces a before/after diff shown to the caller. This skill targets TypeScript only; it does not apply to Python code in the device/edge repo.

Success criteria: every refactored function is a pure function (no side effects, same input produces same output), all interface properties use `readonly` where immutability was applied, and `npm run typecheck` passes after changes.

# When to use

- Refactoring a service function in `src/lib/` that mutates its input parameters
- Replacing imperative loops with `.filter()/.map()/.reduce()` chains in business logic
- Extracting pure calculation functions from route handlers (Read the handler, Edit only in `src/lib/`)
- Converting a class with mutable state into functions and data interfaces

# When NOT to use

- Route handlers and server actions themselves -- keep them thin, put pure logic in `src/lib/` (use `code-standards` instead)
- Any Python code in the device/edge repo (use `api-integration` for hardware code or `code-standards` for Python refactoring -- this skill targets TypeScript only)
- Prisma query builders -- they use a fluent mutable API by design
- React component state management with `useState`/`useReducer` -- React already provides its own immutability contract
- Cloud service config templates or YAML configuration files
- Planning a refactor without executing changes (use `refactor-plan`)

# Required environment

- Runtime: `.claude/skills/functional-design/SKILL.md`
- Tools: Read (inspect code), Edit (apply changes), Grep (find patterns)
- Target: TypeScript files in the main app's `src/lib/` (e.g., `apps/<mainApp>/src/lib/`)

# Inputs

- `file_path: string` -- path to the file to refactor
- `function_name: string` (optional) -- specific function to target; if omitted, scan the entire file

# Outputs

- Format: refactored file via Edit tool, plus a summary listing each change with before/after snippets and the principle applied
- Length budget: max 3 Edit operations per invocation; each refactored function under 40 lines; summary under 30 lines
- Template: function name | principle applied | before snippet (3-5 lines) | after snippet (3-5 lines)

# Procedure

1. **Read the target file** -- Identify functions that mutate parameters, use imperative loops, or mix I/O with calculations.
   **Checkpoint:** List candidate functions with line numbers.

2. **Classify each candidate** -- Determine which functional principle applies: immutability, pure function extraction, composition, or data-flow pipeline. Skip functions that fall in the "When NOT to use" list.
   **Checkpoint:** Confirm no candidates are in route handlers, server actions, Prisma queries, or performance-critical paths.

3. **Verify the route handler boundary** -- If the task involves "extracting pure calculation from a route handler," Read the route handler to identify the calculation logic, but apply Edit only to files in `src/lib/`. The route handler file itself receives at most one Edit to add an import and call to the extracted function. DO NOT refactor the route handler's I/O logic.
   **Checkpoint:** All Edit targets are in `src/lib/` (or a single import-only Edit in the route handler).

4. **Verify types** -- Use Grep/Read to confirm all referenced types, interfaces, and imports. Do not guess parameter shapes.
   **Checkpoint:** All types verified.

5. **Apply refactoring** -- Use Edit for each change. One Edit per function to keep diffs reviewable. Use `readonly` on interface properties and `ReadonlyArray<T>` for array parameters.
   **Checkpoint:** Each Edit applied; diff shown to caller.

6. **Show diff** -- For each Edit, state: function name, principle applied, before snippet (3-5 lines), after snippet (3-5 lines).
   **Checkpoint:** Summary complete.

7. **Verify** -- Read the file after all edits to confirm it parses correctly. Flag any `[VERIFY]` items that need manual testing.
   **Checkpoint:** File parses; `[VERIFY]` items flagged.

# Self-check

<self-check>
- [ ] Every refactored function is a pure function (no side effects, same input produces same output)
- [ ] No refactoring applied to route handlers, server actions, or Prisma query call sites (except a single import-only Edit)
- [ ] All Edit targets are in the main app's `src/lib/` (the route handler boundary is respected)
- [ ] All interface properties use `readonly` where immutability was applied
- [ ] No new dependencies introduced (no immer, ramda, lodash-fp unless already in package.json)
- [ ] Each change has a before/after diff in the summary
- [ ] Functions that perform I/O (database, fetch, WebSocket) are not wrapped in synchronous pipelines
- [ ] No `pipe()` utility used -- TypeScript has no built-in `pipe`; use method chaining or sequential `const` assignments
- [ ] This skill was not applied to Python code (TypeScript only)
</self-check>

# Common mistakes

- DO NOT apply immutability to high-frequency telemetry data structures in the device/edge repo -- the allocation cost of spread copies on a hot per-device telemetry tick is prohibitive
- DO NOT wrap async I/O operations in synchronous function composition chains -- use `async/await` for I/O, then pipe the result through pure transformations
- DO NOT introduce `immer` or `ramda` as new dependencies without confirming they are already in `package.json`
- DO NOT refactor inside React component render functions -- `useState` and `useReducer` already enforce immutable update patterns
- DO NOT use an undeclared `pipe()` utility -- TypeScript has no built-in `pipe`; use method chaining (`.filter().map()`) or sequential `const` assignments instead
- DO NOT Edit route handler files beyond adding an import and function call to the extracted logic -- route handler structure belongs to `code-standards`
- DO NOT apply this skill to any Python file -- this skill is TypeScript-only

# Escalation

- Stop and ask when: the target function performs both calculation and I/O (e.g., calculates a value then writes to DB) -- the caller must decide where to split the boundary
- Stop and ask when: the file has no unit tests and the refactoring changes return types -- the caller should add tests first
- Stop and ask when: the function is called from more than 3 call sites -- changing its signature has a blast radius that needs review

# What to surface

- File paths and line numbers for every changed function
- The functional principle applied to each change (immutability, pure function, composition, data flow)
- Any functions skipped and why (I/O, performance-critical, framework constraint)
- Any `[VERIFY]` items that need manual testing after refactoring

# Examples

<example name="order-pricing">
**Worked example: refactoring an order pricing calculation in the main app**

Before (`src/lib/orders/pricing.ts`):
```typescript
function calculateOrderCost(order: Order): void {
  order.baseCost = order.items.length * 2.5;
  order.shippingCost = calculateTotalWeight(order.items) * 0.1;
  order.totalCost = order.baseCost + order.shippingCost;
  order.status = 'PRICED';
}
```

Principle: replace mutation with transformation + immutability.

After:
```typescript
interface OrderCostResult {
  readonly baseCost: number;
  readonly shippingCost: number;
  readonly totalCost: number;
}

const calculateOrderCost = (
  items: readonly OrderItem[]
): OrderCostResult => {
  const baseCost = items.length * 2.5;
  const shippingCost = calculateTotalWeight(items) * 0.1;
  return {
    baseCost,
    shippingCost,
    totalCost: baseCost + shippingCost,
  };
};
```

Changes:
1. Function no longer mutates the `Order` object -- returns a new `OrderCostResult` instead.
2. Takes `readonly OrderItem[]` instead of the full `Order` -- depends only on what it needs.
3. Status change (`'PRICED'`) removed -- status transitions are a side effect that belongs in the calling route handler, not in a pricing calculation.
</example>

<example name="device-filtering">
**Worked example: device-fleet filtering with composition**

Before (`src/lib/devices/filters.ts`):
```typescript
function getAvailableDevices(devices: Device[], taskType: string): Device[] {
  const result: Device[] = [];
  for (const device of devices) {
    if (device.active && device.batteryLevel > 20) {
      if (taskType === 'TRACKING' && device.hasGPS) {
        result.push(device);
      } else if (taskType === 'MONITORING' && device.hasSensors) {
        result.push(device);
      }
    }
  }
  return result;
}
```

Principle: replace loop with higher-order functions + extract predicates.

After:
```typescript
const isOperational = (device: Device): boolean =>
  device.active && device.batteryLevel > 20;

const supportsTracking = (device: Device): boolean => device.hasGPS;
const supportsMonitoring = (device: Device): boolean => device.hasSensors;

const taskCapability: Record<string, (device: Device) => boolean> = {
  TRACKING: supportsTracking,
  MONITORING: supportsMonitoring,
};

const getAvailableDevices = (
  devices: readonly Device[],
  taskType: string
): readonly Device[] =>
  devices
    .filter(isOperational)
    .filter(taskCapability[taskType] ?? (() => false));
```
</example>

# Failure modes

| Mode | Symptom | Detection | Fix |
|------|---------|-----------|-----|
| Over-refactoring I/O code | Async operations wrapped in synchronous pipe chain, runtime errors | Presence of `await` inside a composed function | Split I/O from pure logic, use async/await for I/O then pass result to pure pipeline |
| Performance regression on telemetry path | Increased GC pressure, dropped frames at 5Hz | Spread operator in a function called per telemetry tick | Revert to mutable update for hot-path code, document why |
| Broken call sites | TypeScript errors in files that call the refactored function | `npm run typecheck` failures | Update all call sites to match new signature, or revert if blast radius is too large |
| Edit applied to route handler | Route handler logic modified beyond import addition | Edit target file is not in `src/lib/` | Undo the Edit; move logic to `src/lib/` and add only an import in the handler |

# Related skills

- `code-standards` -- defer for general code quality, naming conventions, and Next.js patterns; also for Python refactoring
- `code-quality` -- defer for broader code review beyond functional patterns
- `refactor-plan` -- defer when the user wants a structured plan before executing changes
