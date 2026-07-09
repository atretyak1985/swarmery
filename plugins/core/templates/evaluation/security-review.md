# Security Review

**Feature**: [feature name]  
**Developer**: @[username]  
**Reviewer**: @security-auditor  
**Date**: [YYYY-MM-DD]

---

## Summary

[Brief description of security-sensitive changes - 2-3 sentences]

**Scope**:
- **Authentication**: [Yes/No]
- **Authorization**: [Yes/No]
- **Data Handling**: [Yes/No]
- **External APIs**: [Yes/No]
- **User Input**: [Yes/No]

---

## Security Checklist

### Input Validation

- [ ] All user inputs validated
- [ ] SQL injection prevention (parameterized queries)
- [ ] XSS prevention (sanitized output)
- [ ] CSRF protection (tokens)
- [ ] File upload validation (type, size, content)

**Status**: ✅ PASS / ❌ FAIL

**Issues**:
- [Issue 1 if any]
- [Issue 2 if any]

---

### Authentication

- [ ] Proper authentication checks
- [ ] Secure password handling (hashed, salted)
- [ ] Token security (JWT, expiration, rotation)
- [ ] Session management (timeout, invalidation)
- [ ] Multi-factor authentication (if applicable)

**Status**: ✅ PASS / ❌ FAIL

**Issues**:
- [Issue 1 if any]
- [Issue 2 if any]

---

### Authorization

- [ ] Role-based access control (RBAC)
- [ ] Permission checks before data access
- [ ] Horizontal privilege escalation prevention
- [ ] Vertical privilege escalation prevention
- [ ] Resource ownership validation

**Status**: ✅ PASS / ❌ FAIL

**Issues**:
- [Issue 1 if any]
- [Issue 2 if any]

---

### Data Protection

- [ ] Sensitive data encrypted at rest
- [ ] Sensitive data encrypted in transit (TLS)
- [ ] PII handling compliance (GDPR, CCPA)
- [ ] Secrets not in code (use env vars)
- [ ] Database credentials secured

**Status**: ✅ PASS / ❌ FAIL

**Issues**:
- [Issue 1 if any]
- [Issue 2 if any]

---

### Error Disclosure

- [ ] Generic error messages to users
- [ ] No stack traces exposed
- [ ] No sensitive data in logs
- [ ] Proper error logging (server-side)
- [ ] Rate limiting on error endpoints

**Status**: ✅ PASS / ❌ FAIL

**Issues**:
- [Issue 1 if any]
- [Issue 2 if any]

---

## OWASP Top 10 Check

| Vulnerability | Status | Notes |
|---------------|--------|-------|
| A01: Broken Access Control | ✅/❌ | [notes] |
| A02: Cryptographic Failures | ✅/❌ | [notes] |
| A03: Injection | ✅/❌ | [notes] |
| A04: Insecure Design | ✅/❌ | [notes] |
| A05: Security Misconfiguration | ✅/❌ | [notes] |
| A06: Vulnerable Components | ✅/❌ | [notes] |
| A07: Authentication Failures | ✅/❌ | [notes] |
| A08: Software/Data Integrity | ✅/❌ | [notes] |
| A09: Logging/Monitoring Failures | ✅/❌ | [notes] |
| A10: Server-Side Request Forgery | ✅/❌ | [notes] |

---

## Critical Issues

### Issue 1: [Title]

**Severity**: 🔴 Critical / 🟠 High / 🟡 Medium / 🟢 Low

**Description**: [Detailed description]

**Location**: `[file.ts:line]`

**Impact**: [What could happen if exploited]

**Proof of Concept**:
```typescript
// Example exploit
[code showing vulnerability]
```

**Recommended Fix**:
```typescript
// Secure implementation
[code showing fix]
```

**Effort**: [XS/S/M/L/XL]

---

### Issue 2: [Title]

**Severity**: 🔴 Critical / 🟠 High / 🟡 Medium / 🟢 Low

**Description**: [Detailed description]

**Location**: `[file.ts:line]`

**Impact**: [What could happen if exploited]

**Proof of Concept**:
```typescript
// Example exploit
[code showing vulnerability]
```

**Recommended Fix**:
```typescript
// Secure implementation
[code showing fix]
```

**Effort**: [XS/S/M/L/XL]

---

## Recommendations

### Immediate Action Required (Critical/High)

1. **[Recommendation 1]**
   - **Severity**: 🔴 Critical
   - **Issue**: [Description]
   - **Fix**: [How to fix]
   - **Deadline**: [YYYY-MM-DD]

2. **[Recommendation 2]**
   - **Severity**: 🟠 High
   - **Issue**: [Description]
   - **Fix**: [How to fix]
   - **Deadline**: [YYYY-MM-DD]

### Should Fix (Medium)

3. **[Recommendation 3]**
   - **Severity**: 🟡 Medium
   - **Issue**: [Description]
   - **Fix**: [How to fix]

### Nice to Have (Low)

4. **[Recommendation 4]**
   - **Severity**: 🟢 Low
   - **Issue**: [Description]
   - **Fix**: [How to fix]

---

## Decision

- [ ] ✅ **APPROVE** - No security issues found
- [ ] ⚠️ **APPROVE WITH FIXES** - Minor issues, can be fixed post-merge
- [ ] ❌ **BLOCK** - Critical security issues must be fixed before merge

**Rationale**: [Explain decision]

---

## Next Steps

**If APPROVED**:
1. [ ] Proceed with deployment
2. [ ] Monitor security logs
3. [ ] Schedule penetration testing

**If APPROVED WITH FIXES**:
1. [ ] Create security fix tickets
2. [ ] Merge PR
3. [ ] Fix issues within [X] days

**If BLOCKED**:
1. [ ] Developer fixes critical issues
2. [ ] Re-run security review
3. [ ] Re-submit for approval

---

**Reviewed by**: @security-auditor  
**Review Date**: [YYYY-MM-DD]  
**Review Duration**: [X] minutes  
**Risk Level**: 🔴 Critical / 🟠 High / 🟡 Medium / 🟢 Low

