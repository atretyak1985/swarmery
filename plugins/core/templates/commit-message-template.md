# Commit Message Quick Reference

**Purpose**: Quick reference for conventional commit messages with emojis

**When to Use**: When writing commit messages manually or using `@commit-message` agent

**Full details**: See `.claude/skills/git-commit/SKILL.md`

---

## Format

```
<type>(<scope>): <emoji> <subject>

<body>

<footer>
```

> **Important:** Type must be the first token (no emoji prefix) for semantic-release compatibility.

---

## Common Types & Emojis

| Type | Emoji | When to Use | Example |
|------|-------|-------------|---------|
| `feat` | ✨ | New feature | `feat(api): ✨ add vendor registration` |
| `fix` | 🐛 | Bug fix | `fix(mobile): 🐛 resolve crash on startup` |
| `docs` | 📝 | Documentation | `docs(readme): 📝 update setup instructions` |
| `style` | 💄 | UI/styling | `style(client): 💄 update button colors` |
| `refactor` | ♻️ | Code refactoring | `refactor(api): ♻️ extract validation logic` |
| `perf` | ⚡ | Performance | `perf(api): ⚡ add Redis caching` |
| `test` | ✅ | Tests | `test(api): ✅ add vendor service tests` |
| `build` | 📦 | Build system | `build: 📦 update dependencies` |
| `ci` | 👷 | CI/CD | `ci: 👷 add mobile build workflow` |
| `chore` | 🔧 | Maintenance | `chore: 🔧 update ESLint config` |

---

## Common Scopes

The canonical scope list for the current project lives in `project.json → commitScopes`.
Typical scopes look like:

| Scope | Description | Example |
|-------|-------------|---------|
| `api` | REST route handlers (main app) | `feat(api): ✨ add telemetry endpoint` |
| `ui` | UI components | `feat(ui): ✨ add setup wizard step` |
| `db` | Database / ORM schema & queries | `refactor(db): ♻️ optimize list queries` |
| `auth` | Authentication / identity provider | `feat(auth): 🔒 add role-based route guard` |
| `edge` | Device/edge repo (`project.json → device`) | `fix(edge): 🐛 resolve UART reconnect` |
| `helm` | Helm charts / K8s deployment | `feat(helm): ✨ add resource limits` |
| `telemetry` | Telemetry pipeline / WebSocket | `perf(telemetry): ⚡ batch SSE events` |
| `ci` | CI/CD pipelines | `fix(ci): 🐛 correct deploy stage rules` |
| `infra` | Infrastructure repo | `feat(infra): ✨ add cert-manager config` |

---

## Subject Rules

✅ **DO:**
- Use imperative mood: "add" not "added" or "adds"
- Keep it lowercase: "add feature" not "Add feature"
- Keep it short: max 50 characters
- Be specific: "add vendor registration" not "add feature"

❌ **DON'T:**
- Don't add period at end
- Don't use past tense
- Don't be vague

**Examples:**

✅ Good:
```
feat(api): ✨ add vendor registration with validation
```

❌ Bad:
```
feat(api): ✨ Added vendor registration feature.
```

---

## Body Guidelines

**When to add body:**
- Multiple changes in one commit
- Need to explain **why** (not how)
- Breaking changes
- Complex logic

**Format:**
- Separate from subject with blank line
- Use bullet points for multiple changes
- Wrap at 72 characters
- Explain what and why, not how

**Example:**
```
feat(api): ✨ add vendor registration with validation

- Implement createVendor mutation
- Add input validation for required fields
- Add duplicate vendor check by email
- Return created vendor with ID and timestamps

This allows vendors to self-register instead of manual admin creation.
```

---

## Footer Guidelines

**Reference issues:**
```
Closes #123
Fixes #456
Related to #789
```

**Breaking changes:**
```
BREAKING CHANGE: Rating scale changed from 1-10 to 1-5.
Clients must update UI to reflect new scale.
```

**Co-authors (only for real human co-authors):**
```
Co-authored-by: John Doe <john@example.com>
```

> **NOTE**: Do NOT add AI attribution (e.g., `Co-Authored-By: Claude...`). Only use this for actual human collaborators.

---

## Quick Examples

### New Feature
```
feat(api): ✨ add vendor registration with validation

- Implement createVendor mutation
- Add CreateVendorDto with validation
- Add duplicate vendor check by email

Closes #234
```

### Bug Fix
```
fix(mobile): 🐛 resolve order status update issue

Check for existing pending orders before updating.
Add transaction to ensure atomic updates.

Fixes #456
```

### Refactoring
```
refactor(api): ♻️ extract validation to separate class

Move validation logic from resolver to VendorValidator.
Improves testability and allows reuse in other modules.
```

### Documentation
```
docs(readme): 📝 update vendor registration setup

Add instructions for:
- Environment variables
- Database migrations
- Testing vendor registration
```

### Performance
```
perf(api): ⚡ add Redis caching for vendor list

Cache vendor list with 5-minute TTL.
Reduces database queries by ~80% for popular endpoints.
```

### Tests
```
test(api): ✅ add vendor service unit tests

- Test createVendor with valid input
- Test duplicate vendor detection
- Test validation errors
- Test edge cases

Coverage increased from 60% to 85%.
```

---

## Multi-Scope Commits

**When changes affect multiple areas:**

**Option 1: Use primary scope**
```
feat(api): ✨ add vendor registration (full-stack)

Backend:
- Implement createVendor mutation
- Add validation

Frontend:
- Create VendorRegistrationForm
- Add form validation
```

**Option 2: Split into multiple commits (recommended)**
```
# Commit 1
feat(api): ✨ add vendor registration endpoint

# Commit 2
feat(client): ✨ add vendor registration form
```

---

## Before Committing Checklist

- [ ] Code compiles/runs without errors
- [ ] All tests pass (`npm run test`)
- [ ] Code is formatted (`npm run code:fix`)
- [ ] No linting errors
- [ ] Commit message follows format
- [ ] Subject is descriptive and < 50 chars
- [ ] Body explains what and why (if needed)
- [ ] Issues referenced in footer (if applicable)

---

## Using @commit-message Agent

**Instead of writing manually, use the agent:**

```
@commit-message
```

**The agent will:**
1. Analyze your staged changes
2. Determine type and scope
3. Generate commit message
4. Explain reasoning
5. Offer to commit

**Example:**
```
User: @commit-message

Agent: I analyzed your staged changes:

📁 Files changed:
- src/modules/vendors/vendor.service.ts (modified)

🎯 Type: feat (new feature)
📍 Scope: api (backend API)

📝 Suggested commit message:
feat(api): ✨ add vendor registration with validation

Would you like me to commit with this message? (yes/no)
```

---

## Common Mistakes

### ❌ Mistake 1: Vague subject
```
chore: 🔧 updates
```
✅ **Fix:**
```
chore(api): 🔧 update next to v16.1.0
```

---

### ❌ Mistake 2: Multiple unrelated changes
```
feat: ✨ add vendor registration and fix order bug
```
✅ **Fix:** Split into two commits
```
feat(api): ✨ add vendor registration
fix(api): 🐛 prevent duplicate order creation
```

---

### ❌ Mistake 3: Wrong mood
```
feat(api): ✨ added vendor registration
```
✅ **Fix:**
```
feat(api): ✨ add vendor registration
```

---

### ❌ Mistake 4: Missing scope
```
feat: ✨ add registration
```
✅ **Fix:**
```
feat(api): ✨ add vendor registration
```

---

## Resources

- **Full skill**: `.claude/skills/git-commit/SKILL.md`
- **Agent**: `.claude/agents/commit-message.md`
- **Conventional Commits**: https://www.conventionalcommits.org/

---

**Version**: 1.0  
**Last Updated**: December 3, 2025  
**Maintained by**: agentry-core

