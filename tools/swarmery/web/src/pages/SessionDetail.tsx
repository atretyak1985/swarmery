// /sessions/:id renders the same place as /sessions: the same component, so React
// keeps the list and its scroll across a selection. The route table still imports
// this name; the detail itself is pages/detail/SessionPane.tsx.

export { Sessions as SessionDetailPage } from './Sessions';
