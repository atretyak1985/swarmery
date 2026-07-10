# Feature Implementation Template

## Feature Overview

**Feature Name:** [Name]

**Description:** [Brief description of what this feature does]

**Type:**
- [ ] Backend only
- [ ] Frontend only
- [ ] Full-stack
- [ ] Infrastructure
- [ ] Control Box

**Complexity:**
- [ ] Simple (< 3 files, < 2 hours)
- [ ] Medium (3-10 files, 2-8 hours)
- [ ] Complex (> 10 files, > 8 hours)

---

## Requirements

### Functional Requirements
1. [Requirement 1]
2. [Requirement 2]
3. [Requirement 3]

### Non-Functional Requirements
- **Performance:** [Target metrics]
- **Security:** [Security considerations]
- **Scalability:** [Scaling considerations]

---

## Technical Design

### Affected Components

**Backend:**
- Controllers: [List]
- Services: [List]
- Repositories: [List]
- Entities: [List]

**Frontend:**
- Components: [List]
- Contexts: [List]
- Services: [List]

**Database:**
- Tables/Collections: [List]
- Migrations needed: Yes/No

### API Changes

**New Endpoints:**
```
POST /api/[resource] - [Description]
GET /api/[resource]/{id} - [Description]
PUT /api/[resource]/{id} - [Description]
DELETE /api/[resource]/{id} - [Description]
```

**Request/Response Examples:**
```json
// Request
{
  "field1": "value",
  "field2": 123
}

// Response
{
  "id": "uuid",
  "field1": "value",
  "field2": 123,
  "createdAt": "2026-01-18T00:00:00Z"
}
```

### Database Schema Changes

```sql
-- New table or ALTER statements
CREATE TABLE [table_name] (
  id UUID PRIMARY KEY,
  field1 VARCHAR(255),
  created_at TIMESTAMP DEFAULT CURRENT_TIMESTAMP
);

-- Indexes
CREATE INDEX idx_[field] ON [table]([field]);
```

---

## Implementation Plan

### Backend Tasks
- [ ] Create/update entities
- [ ] Create/update repositories
- [ ] Create/update services
- [ ] Create/update controllers
- [ ] Add validation
- [ ] Add tests

### Frontend Tasks
- [ ] Create/update components
- [ ] Create/update contexts
- [ ] Update API client
- [ ] Add form validation
- [ ] Add error handling
- [ ] Add tests

### Infrastructure Tasks
- [ ] Update Helm values
- [ ] Update environment variables
- [ ] Update ingress rules
- [ ] Database migration

---

## Testing Plan

### Unit Tests
- [ ] Backend service tests
- [ ] Frontend component tests
- [ ] Utility function tests

### Integration Tests
- [ ] API endpoint tests
- [ ] Database integration tests
- [ ] WebSocket tests

### E2E Tests
- [ ] User flow tests
- [ ] Critical path tests

---

## Deployment Checklist

- [ ] Target environment documented (`<envAlias>`, `staging`, `production`, or local-only)
- [ ] Approval points documented
- [ ] Code merged to main branch
- [ ] Database migrations prepared
- [ ] Environment variables documented
- [ ] Docker images built and pushed
- [ ] Immutable image digest / version reference captured
- [ ] Helm charts updated
- [ ] Verification plan documented (rollout, smoke, E2E if needed)
- [ ] Promotion notes documented
- [ ] Documentation updated
- [ ] CLAUDE.md updated (if architecture changed)

---

## Rollback Plan

**Promotion rule:**
- [Describe what must pass before this change can be promoted]

**If deployment fails:**
1. [Step 1 to rollback]
2. [Step 2 to rollback]
3. [Step 3 to rollback]

**Database rollback:**
```bash
# Rollback migration or restore previous desired state
[rollback command or procedure]
```
