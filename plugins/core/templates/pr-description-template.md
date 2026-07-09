# Pull Request Description Template

**Purpose**: Standardized format for Pull Request descriptions

**When to Use**: When creating a Pull Request for code review

---

## Template

```markdown
## 📝 Description

[Brief description of what this PR does and why]

**Type**: [Feature / Bug Fix / Refactoring / Documentation / Chore]  
**Ticket**: [Link to ticket/issue]  
**Related PRs**: [Links to related PRs, if any]

---

## 🎯 Changes

### Summary

- [Change 1]
- [Change 2]
- [Change 3]

### Files Changed

**Backend:**
- `path/to/file.ts` - [What changed]
- `path/to/file2.ts` - [What changed]

**Frontend:**
- `path/to/Component.tsx` - [What changed]
- `path/to/styles.css` - [What changed]

**Tests:**
- `path/to/test.spec.ts` - [What changed]

**Total**: X files changed (+Y lines, -Z lines)

---

## 🧪 Testing

### How to Test

1. **Setup** (if needed)
   ```bash
   npm install
   npm run migrate
   ```

2. **Run tests**
   ```bash
   npm run test
   npm run test:e2e
   ```

3. **Manual testing**
   - [Step 1]
   - [Step 2]
   - Expected: [Result]

### Test Coverage

- ✅ Unit tests: X tests added/updated
- ✅ Integration tests: Y tests added/updated
- ✅ E2E tests: Z tests added/updated
- ✅ Coverage: X% (before) → Y% (after)

---

## 📸 Screenshots / Videos

**Before:**
[Screenshot or "N/A"]

**After:**
[Screenshot or "N/A"]

**Demo:**
[GIF/Video or "N/A"]

---

## ✅ Checklist

**Code Quality:**
- [ ] Code follows project coding standards
- [ ] No TypeScript errors
- [ ] No ESLint warnings
- [ ] All tests passing
- [ ] Test coverage maintained or improved

**Documentation:**
- [ ] Code is self-documenting or has comments
- [ ] README updated (if needed)
- [ ] API documentation updated (if needed)
- [ ] ADR created (if architectural change)

**Security:**
- [ ] No sensitive data exposed
- [ ] Input validation added
- [ ] Authorization checks added
- [ ] No security vulnerabilities introduced

**Performance:**
- [ ] No performance regressions
- [ ] Database queries optimized
- [ ] Bundle size checked (if frontend)

**Deployment:**
- [ ] Target environment identified (`devnext`, `staging`, `production`, or local-only)
- [ ] Approval points identified (if any)
- [ ] Database migrations included (if needed)
- [ ] Environment variables documented (if new)
- [ ] Verification requirements documented
- [ ] Backward compatible (or migration plan documented)
- [ ] Rollback plan documented (if risky change)
- [ ] Promotion notes documented (if deployed via CI/CD)

---

## 🚀 Deployment Notes

**Database Migrations:**
- [ ] No migrations needed
- [ ] Migrations included: [List migration files]

**Environment Variables:**
- [ ] No new env vars
- [ ] New env vars: [List with descriptions]

**Target Environment:**
- [ ] Local-only
- [ ] `devnext`
- [ ] `staging`
- [ ] `production`

**Approval Points:**
- [ ] No manual approvals required
- [ ] Manual approval required: [Describe who/when]

**Verification Requirements:**
- [ ] No deployment verification needed
- [ ] Required checks: [lint / tests / smoke / Playwright / rollout / other]

**Breaking Changes:**
- [ ] No breaking changes
- [ ] Breaking changes: [Describe and provide migration guide]

**Promotion Notes:**
- [ ] No promotion impact
- [ ] Promote only after verification passes
- [ ] Deploys by immutable digest / release reference

**Rollback Plan:**
[How to rollback if something goes wrong]

---

## 🔗 Related

**Related Issues:**
- Closes #[issue number]
- Fixes #[issue number]
- Related to #[issue number]

**Related PRs:**
- Depends on #[PR number]
- Blocks #[PR number]

**Documentation:**
- [Link to design doc]
- [Link to ADR]
- [Link to API docs]

---

## 💡 Notes for Reviewers

**Focus Areas:**
- [Area 1 that needs special attention]
- [Area 2 that needs special attention]

**Questions:**
- [Question 1 for reviewers]
- [Question 2 for reviewers]

**Trade-offs:**
- [Trade-off 1 and why it was chosen]
- [Trade-off 2 and why it was chosen]

---

## 📋 Post-Merge Tasks

- [ ] Deploy to `devnext` (if applicable)
- [ ] Run post-deploy verification
- [ ] Promote to staging / production only with required approvals
- [ ] Monitor metrics
- [ ] Update documentation
- [ ] Notify stakeholders

---

**Author**: @[username]  
**Reviewers**: @[username1], @[username2]  
**Estimated Review Time**: [X minutes]
```

---

## PR Types

- **Feature**: New functionality
- **Bug Fix**: Fixing a defect
- **Refactoring**: Code improvements without behavior change
- **Documentation**: Documentation updates
- **Chore**: Maintenance tasks (dependencies, configs, etc.)

---

## Quality Checklist

Before creating PR:

- [ ] All tests passing locally
- [ ] Code reviewed by yourself first
- [ ] Commit messages follow conventional commits
- [ ] Branch name follows convention (feat/, fix/, refactor/, etc.)
- [ ] PR title is clear and descriptive
- [ ] Description explains what, why, and how
- [ ] Screenshots/videos included (if UI change)
- [ ] Breaking changes documented
- [ ] Reviewers assigned

---

**Version**: 1.0  
**Last Updated**: December 3, 2025  
**Maintained by**: agentry-core

