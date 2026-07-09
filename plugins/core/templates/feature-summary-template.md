# Feature Summary Template

**Purpose**: Standardized format for documenting completed feature implementations

**When to Use**: New features (full-stack, backend-only, frontend-only, control box, infrastructure)

---

## Template

```markdown
# ✨ [Feature Name] - Implementation Complete

**Date**: [YYYY-MM-DD]
**Author**: [Name or "Claude + Team"]
**Type**: [Full-Stack / Backend / Frontend / Control Box / Infrastructure]
**Complexity**: [Low / Medium / High / Critical]
**Duration**: [X hours/days/weeks]
**Status**: ✅ Complete | ⏳ In Progress | 🧪 Testing

---

## 📋 Feature Overview

### Summary
[Brief 2-3 sentence summary of the feature and its purpose]

### User Story
**As a** [role]
**I want** [capability]
**So that** [benefit]

### Acceptance Criteria
- ✅ [Criterion 1]
- ✅ [Criterion 2]
- ✅ [Criterion 3]

### Related Items
**JIRA**: [SM-XXX]
**Epic**: [Link to epic]
**Design Doc**: [Link]

---

## 🏗️ Architecture & Design

### System Architecture

```
┌─────────────────────────────────────┐
│ apps/<mainApp> (web framework)      │
│ - UI components                     │
│ - API route handlers                │
│ - Server actions / services         │
│ - ORM layer                         │
└──────────────┬──────────────────────┘
               │ node-postgres / SSE / WebSocket
┌──────────────▼──────────────────────┐
│ Database (PostgreSQL)               │
│ - [Table 1]                         │
│ - [Table 2]                         │
└─────────────────────────────────────┘
```

### Backend Design

**Controllers**:
- `[ControllerName]Controller` - [Purpose]
  - `GET /api/[endpoint]` - [What it does]
  - `POST /api/[endpoint]` - [What it does]
  - `PUT /api/[endpoint]/{id}` - [What it does]
  - `DELETE /api/[endpoint]/{id}` - [What it does]

**Services**:
- `[ServiceName]Service` - [Business logic description]

**Repositories**:
- `[RepositoryName]Repository` - [Data access description]

**DTOs**:
- `[DtoName]Request` - [Fields]
- `[DtoName]Response` - [Fields]

### Frontend Design

**Components**:
- `[ComponentName]` - [Purpose]
- `[ComponentName]Provider` - [State management]
- `[ComponentName]Modal` - [Dialog/modal]

**State Management**:
- Context: `[ContextName]Context`
- Hooks: `use[HookName]()`

**API Integration**:
- RestService methods: `[method1]()`, `[method2]()`

### Database Schema

**New Tables**:
```sql
CREATE TABLE [table_name] (
  id BIGSERIAL PRIMARY KEY,
  [column1] [type] NOT NULL,
  [column2] [type],
  created_at TIMESTAMP DEFAULT CURRENT_TIMESTAMP,
  updated_at TIMESTAMP DEFAULT CURRENT_TIMESTAMP
);
```

**Indexes**:
- `idx_[table]_[column]` - [Purpose]

**Migrations**:
- `V[version]__[description].sql`

---

## ✅ Files Created

**Total**: [X files] ([Y backend, Z frontend, N tests])

### Backend ([Y files])

**Controllers**:
- `src/main/java/com/[package]/controller/[Name]Controller.java` - [Purpose]

**Services**:
- `src/main/java/com/[package]/service/[Name]Service.java` - [Purpose]

**Repositories**:
- `src/main/java/com/[package]/repository/[Name]Repository.java` - [Purpose]

**DTOs**:
- `src/main/java/com/[package]/dto/[Name]Request.java` - [Purpose]
- `src/main/java/com/[package]/dto/[Name]Response.java` - [Purpose]

**Entities**:
- `src/main/java/com/[package]/entity/[Name].java` - [Purpose]

### Frontend ([Z files])

**Components**:
- `src/components/[feature]/[Component].tsx` - [Purpose]
- `src/components/[feature]/[Component]Provider.tsx` - [Purpose]

**Hooks**:
- `src/hooks/use[Name].ts` - [Purpose]

**Types**:
- `src/types/[name].ts` - [Type definitions]

**Styles**:
- `src/components/[feature]/[Component].module.css` - [Styling]

### Tests ([N files])

**Backend Tests**:
- `src/test/java/com/[package]/[Name]Test.java` - [What's tested]
- `src/test/java/com/[package]/integration/[Name]IntegrationTest.java` - [What's tested]

**Frontend Tests**:
- `src/components/[feature]/__tests__/[Component].test.tsx` - [What's tested]

### Database Migrations

**Migrations**:
- `files/backendMigration/V[version]__[description].sql` - [What it migrates]

---

## 🔧 Files Modified

**Total**: [X files]

### Backend
- `src/main/java/com/[package]/[File].java` - [What changed]

### Frontend
- `src/[path]/[File].tsx` - [What changed]

### Configuration
- `application.properties` - [What changed]
- `values.[env].yaml` - [What changed]

---

## 📊 Metrics

### Code
- **Backend**: +XXX lines (Y files)
- **Frontend**: +ZZZ lines (N files)
- **Tests**: +NNN lines (M files)
- **Total**: +XXXX lines

### Testing
- **Unit tests**: X tests (Y backend, Z frontend)
- **Integration tests**: N tests
- **E2E tests**: M tests
- **Test coverage**: X% → Y% (+Z%)

### Performance
- **API response time**: XXXms (95th percentile)
- **Page load time**: XXXms
- **Database queries**: X queries per request
- **Memory usage**: XXX MB

### Quality
- **TypeScript errors**: 0
- **ESLint warnings**: 0
- **SonarQube score**: [Score]
- **Code complexity**: [Cyclomatic complexity]

---

## 🎯 How to Use

### For Developers

**Local Setup**:
```bash
cd apps/<mainApp>
npm install
npm run dev
```

**Key Files to Review**:
- Route handler: `src/app/api/[route]/route.ts`
- Service / query: `src/lib/[feature]/...`
- UI: `src/components/[feature]/[Component].tsx`
- Tests: `src/**/__tests__/[name].test.{ts,tsx}`

**Testing Locally**:
1. [Step 1]
2. [Step 2]
3. Expected: [Result]

---

### For QA

**Test Scenarios**:

**Happy Path**:
1. [Step 1]
2. [Step 2]
3. Expected: [Result]

**Edge Cases**:
1. [Edge case 1] - Expected: [Result]
2. [Edge case 2] - Expected: [Result]

**Error Cases**:
1. [Error condition 1] - Expected: [Error message]
2. [Error condition 2] - Expected: [Error message]

**Performance Tests**:
- [ ] [Metric] under [condition] is < [threshold]
- [ ] [Metric] with [load] is < [threshold]

---

### For Product Manager

**User-Facing Changes**:
- [Change 1 from user perspective]
- [Change 2 from user perspective]

**Business Impact**:
- [Business metric affected]
- [ROI or efficiency gain]

**Release Notes**:
```
✨ New: [Feature name]

[User-friendly description]

Benefits:
- [Benefit 1]
- [Benefit 2]
```

---

## ✨ Key Features & Benefits

### User Benefits
- ✅ [Benefit 1 with quantification]
- ✅ [Benefit 2 with quantification]
- ✅ [Benefit 3 with quantification]

### Business Benefits
- ✅ [Business value 1]
- ✅ [Business value 2]

### Technical Benefits
- ✅ [Technical improvement 1]
- ✅ [Technical improvement 2]

---

## 🔒 Security Considerations

**Authentication**:
- [How authentication is handled]
- [What endpoints require auth]

**Authorization**:
- [What permission checks are in place]
- [Role-based access control]

**Input Validation**:
- [What inputs are validated]
- [Validation rules]

**Data Protection**:
- [Sensitive data handling]
- [Encryption at rest/transit]

**Vulnerabilities Addressed**:
- [Security issue 1] - [How it's mitigated]
- [Security issue 2] - [How it's mitigated]

---

## 🧪 Testing

### Unit Tests

**Backend** ([X tests, Y% coverage]):
- `[TestClass]Test` - [What's tested]
  - `test[Scenario]()` - [Test description]
  - `test[Scenario]()` - [Test description]

**Frontend** ([Z tests, N% coverage]):
- `[Component].test.tsx` - [What's tested]
  - `it('[scenario]')` - [Test description]
  - `it('[scenario]')` - [Test description]

### Integration Tests

**API Integration** ([X tests]):
- `[TestClass]IntegrationTest` - [What's tested]
  - `test[Scenario]()` - [End-to-end scenario]

### E2E Tests

**User Workflows** ([X tests]):
- `[feature].e2e.spec.ts` - [Complete user journey]

### Test Execution

```bash
# Backend tests
./gradlew test

# Frontend tests
npm run test

# E2E tests
npm run test:e2e
```

---

## 🚀 Deployment Checklist

### Pre-Deployment
- [ ] All tests passing
- [ ] Code reviewed and approved
- [ ] No security vulnerabilities
- [ ] Performance benchmarks met
- [ ] Documentation updated

### Database Migration
- [ ] Migration tested on dev
- [ ] Migration rollback tested
- [ ] Backup created
- [ ] Migration window scheduled

### Configuration
- [ ] Environment variables set
- [ ] Feature flags configured (if applicable)
- [ ] Monitoring alerts configured

### Deployment Steps
1. [ ] Deploy database migration
2. [ ] Deploy backend (rolling update)
3. [ ] Verify backend health
4. [ ] Deploy frontend (static assets)
5. [ ] Verify frontend loads
6. [ ] Smoke test critical paths
7. [ ] Monitor error rates

### Post-Deployment
- [ ] Verify metrics (response time, error rate)
- [ ] Check logs for errors
- [ ] QA smoke test
- [ ] Notify stakeholders

### Rollback Plan
```bash
# If needed, rollback:
helm rollback [release] [revision]
```

---

## 🚀 Next Steps

### Immediate
- [ ] [Action 1] - [Owner] - [Date]
- [ ] [Action 2] - [Owner] - [Date]

### Short-term (Next Sprint)
- [ ] [Action 1] - [Owner]
- [ ] [Action 2] - [Owner]

### Long-term
- [ ] [Future enhancement 1]
- [ ] [Future enhancement 2]

---

## ⚠️ Known Issues

**Issue 1**: [Description]
- **Severity**: 🔴 High | 🟡 Medium | 🟢 Low
- **Workaround**: [Workaround]
- **Fix planned**: [SM-XXX] - [Date]

**If no issues**: No known issues.

---

## 🔮 Future Improvements

### Phase 2 Enhancements
- [Enhancement 1]
- [Enhancement 2]

### Nice to Have
- [Idea 1]
- [Idea 2]

---

## 📚 Documentation

**Created**:
- [Link to user guide]
- [Link to API documentation]
- [Link to architecture diagram]

**Updated**:
- README.md - [What was updated]
- API.md - [What was updated]

**Related ADRs**:
- [ADR-XXX] - [Title]

---

**Status**: ✅ Complete
**Priority**: 🔴 High | 🟡 Medium | 🟢 Low
**Date**: [YYYY-MM-DD]
**Author**: [Name]
**JIRA**: [SM-XXX]
**Repository**: [one of the project's repos — see `project.json → repos`]
```

---

## Quality Checklist

Before using this template:

- [ ] All acceptance criteria met
- [ ] Architecture diagram included
- [ ] All files listed (created, modified)
- [ ] Metrics quantified
- [ ] Testing coverage documented
- [ ] Security considerations addressed
- [ ] Deployment checklist complete
- [ ] Known issues documented
- [ ] Future improvements identified

---

**Version**: 1.0
**Last Updated**: January 23, 2026
**Maintained by**: agentry-core
