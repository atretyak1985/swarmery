# Bug Fix Summary Template

**Purpose**: Standardized format for documenting completed bug fixes

**When to Use**: Any bug fix (production, staging, development) regardless of severity

---

## Template

```markdown
# 🐛 [Bug Title] - Fixed

**Date**: [YYYY-MM-DD]
**Author**: [Name or "Claude + Team"]
**Bug ID**: [SM-XXX]
**Severity**: 🔴 Critical | 🟡 High | 🟠 Medium | 🟢 Low
**Environment**: Production | Staging | Development | Local
**Status**: ✅ Fixed | ⏳ In Progress | 🧪 Testing

---

## 📋 Bug Overview

### Summary
[Brief 1-2 sentence description of the bug]

### Symptoms
- [Symptom 1 - what users/developers observed]
- [Symptom 2]
- [Symptom 3]

### Bug Details
- **Reported**: [YYYY-MM-DD]
- **Discovered by**: [Name / User / Monitoring]
- **Environment**: [Production / Staging / Development]
- **Affected versions**: [Version range]
- **First occurrence**: [Date/version when it started]
- **Fixed**: [YYYY-MM-DD]
- **Time to fix**: [X hours/days]

---

## 💥 Impact

### User Impact
- **Affected users**: [X users / Y% of user base / All users]
- **Frequency**: [How often it occurred]
- **User operations affected**:
  - [Operation 1 - e.g., "Mission planning", "Video streaming"]
  - [Operation 2]
- **User experience**: [Describe the negative UX]

### Business Impact
- **Revenue impact**: [If applicable]
- **SLA breach**: [Yes/No - details]
- **Customer complaints**: [X tickets/reports]
- **Reputation damage**: [Low / Medium / High / None]

### Technical Impact
- **Services affected**: [Service names]
- **Data loss**: [Yes/No - details]
- **Downtime**: [X minutes/hours]
- **Error rate**: [X errors/minute or Y% error rate]
- **System resources**: [CPU/Memory/DB impact]

---

## 🔍 Root Cause Analysis

### What Happened
[Detailed technical description of what went wrong]

### Why It Happened
[Root cause - what was the underlying issue]

**Contributing Factors**:
- [Factor 1]
- [Factor 2]
- [Factor 3]

### When It Started
[When the bug was introduced - commit, version, date]

### How It Was Discovered
- **Discovery method**: [User report / Monitoring alert / Automated test / Code review]
- **Error logs**:
  ```
  [Relevant log excerpt]
  ```
- **Stack trace**:
  ```
  [Stack trace if applicable]
  ```

### Code Analysis

**Problematic Code** (Before):
```java
// File: src/path/to/File.java:123
[Code snippet showing the bug]
```

**Why This Was Wrong**:
[Explanation of the problem in the code]

---

## ✅ The Fix

### Solution Overview
[High-level description of how the bug was fixed]

### Technical Changes

**Fixed Code** (After):
```java
// File: src/path/to/File.java:123
[Code snippet showing the fix]
```

**What Changed**:
- [Change 1]
- [Change 2]
- [Change 3]

**Why This Fixes It**:
[Explanation of why the fix resolves the issue]

---

## 🔧 Files Modified

**Total**: [X files]

### Backend
- `src/main/java/com/[package]/[File].java:Line` - [What was fixed]
- `src/main/java/com/[package]/[File].java:Line` - [What was fixed]

### Frontend
- `src/components/[path]/[File].tsx:Line` - [What was fixed]

### Configuration
- `application.properties` - [What was changed]

### Tests
- `src/test/java/[path]/[Test].java` - [Test added/updated]

---

## 🧪 Testing

### Reproduction Steps (Before Fix)

1. [Step 1 to reproduce the bug]
2. [Step 2]
3. [Step 3]
4. **Expected**: [What should happen]
5. **Actual**: [What actually happened - the bug]

### Verification Steps (After Fix)

1. [Step 1 to verify the fix]
2. [Step 2]
3. [Step 3]
4. **Result**: ✅ [Bug is fixed]

### Regression Tests Added

**Unit Tests** ([X tests]):
- `test[Scenario]()` - [What's tested]
- `test[EdgeCase]()` - [Edge case covered]

**Integration Tests** ([Y tests]):
- `test[Integration]()` - [End-to-end scenario]

### Test Results
```bash
# All tests passing
./gradlew test
npm run test

✅ Backend: 123/123 tests passed
✅ Frontend: 45/45 tests passed
✅ Coverage: 85% → 87% (+2%)
```

---

## 📊 Metrics

### Code Changes
- **Files changed**: X files
- **Lines added**: +XX
- **Lines removed**: -YY
- **Net change**: ±ZZ lines

### Testing
- **Tests added**: X tests
- **Coverage change**: Y% → Z% (+N%)
- **Edge cases covered**: N new cases

### Performance Impact
- **Before fix**: [Metric - e.g., "500ms avg response time, 5% error rate"]
- **After fix**: [Metric - e.g., "200ms avg response time, 0% error rate"]
- **Improvement**: [Quantified improvement]

### Time Metrics
- **Time to detect**: [X hours/days from occurrence to detection]
- **Time to diagnose**: [Y hours/days from detection to root cause]
- **Time to fix**: [Z hours/days from root cause to fix deployed]
- **Total time**: [Total from occurrence to resolution]

---

## 🎯 How to Verify

### For Developers

**Local Testing**:
```bash
# Reproduce the bug (should not occur now)
[Steps to verify locally]
```

**Code Review Focus**:
- File: `[path/to/File.java:Line]`
- Key change: [What to review]

---

### For QA

**Test Cases**:

**Primary Fix Verification**:
1. [Step 1]
2. [Step 2]
3. Expected: [Bug should not occur]

**Edge Cases**:
1. [Edge case 1] - Expected: [Result]
2. [Edge case 2] - Expected: [Result]

**Regression Tests**:
- [ ] [Related feature 1] still works
- [ ] [Related feature 2] still works
- [ ] [Related feature 3] still works

---

### For Product Manager

**User Communication**:
```
Subject: [Feature/Component] Issue Resolved

We've fixed an issue where [user-friendly description].

What was affected:
- [Impact 1]
- [Impact 2]

Status: ✅ Resolved as of [Date]

Users can now [what they can do again].
```

---

## 🛡️ Prevention Measures

### Immediate Measures (Deployed)
- ✅ [Fix deployed]
- ✅ [Monitoring added for this scenario]
- ✅ [Regression tests added]

### Short-term Measures (This Sprint)
- [ ] [Code review checklist updated] - [Owner]
- [ ] [Additional tests for similar cases] - [Owner]
- [ ] [Documentation updated] - [Owner]

### Long-term Measures (Next Quarter)
- [ ] [Architectural improvement] - [Owner]
- [ ] [Process improvement] - [Owner]
- [ ] [Tool/automation improvement] - [Owner]

### Monitoring Added
- **Metric**: [What's being monitored]
- **Alert**: [When alert triggers]
- **Dashboard**: [Link to dashboard]

---

## 🔗 Related Issues

**Related Bugs**:
- [SM-XXX] - [Similar bug]
- [SM-YYY] - [Related issue]

**Caused By**:
- [SM-ZZZ] - [Original feature/change that introduced bug]

**Blocks**:
- [SM-AAA] - [Issue that was blocked by this bug]

---

## 🚀 Deployment

### Deployment Details
- **Deployed to**: [Environment]
- **Deployment date**: [YYYY-MM-DD HH:MM]
- **Deployment method**: [Hotfix / Regular release / Rollback]
- **Approval required**: [Yes / No - who approved]
- **Artifact reference**: [Digest / version / commit]
- **Downtime**: [X minutes / Zero downtime]

### Promotion Notes
- **Promotion gate**: [What verification had to pass before promotion]
- **Promotion status**: [Promoted / Not promoted / Pending]

### Rollback Plan
```bash
# If regression occurs, rollback:
helm rollback [release] [previous-revision]
```

### Post-Deployment Verification
- [ ] Bug no longer reproducible
- [ ] Error rate returned to normal
- [ ] Performance metrics normal
- [ ] No new errors in logs
- [ ] User reports confirm fix

---

## 🚀 Next Steps

### Immediate
- [ ] Monitor error logs for 24h - [DevOps] - [Date]
- [ ] Notify affected users - [PM] - [Date]
- [ ] Update release notes - [PM] - [Date]

### Short-term
- [ ] Review similar code patterns - [Dev Team] - [Date]
- [ ] Update documentation - [TechWriter] - [Date]

---

## ⚠️ Known Limitations

**Limitation 1**: [Description]
- **Workaround**: [If available]
- **Future fix**: [If planned]

**If no limitations**: No known limitations.

---

## 💡 Lessons Learned

### What Went Well
- [Success 1]
- [Success 2]

### What Could Be Improved
- [Improvement 1]
- [Improvement 2]

### Action Items
- [ ] [Process improvement] - [Owner] - [Date]
- [ ] [Tool improvement] - [Owner] - [Date]

---

## 📚 Documentation Updated

**Updated**:
- README.md - [What was updated]
- [Runbook] - [What was updated]
- [Troubleshooting guide] - [What was added]

**Created**:
- [Post-mortem doc] - [If applicable]

---

**Status**: ✅ Fixed
**Severity**: 🔴 Critical | 🟡 High | 🟠 Medium | 🟢 Low
**Date**: [YYYY-MM-DD]
**Author**: [Name]
**Bug ID**: [SM-XXX]
**Repository**: [one of the project's repos — see `project.json → repos`]
```

---

## Example: Telemetry Stream Bug

```markdown
# 🐛 Telemetry Data Not Updating During Active Missions - Fixed

**Date**: 2026-01-23
**Author**: Backend Team
**Bug ID**: SM-456
**Severity**: 🔴 Critical
**Environment**: Production
**Status**: ✅ Fixed

---

## 📋 Bug Overview

### Summary
Telemetry data (GPS, altitude, battery) stopped updating on the live dashboard during active device operations, causing operators to lose real-time situational awareness.

### Symptoms
- Telemetry dashboard freezes after 2-3 minutes of mission
- Last known position displayed indefinitely
- No error messages shown to user
- WebSocket connection shows as "connected" but no data flowing

---

## 💥 Impact

### User Impact
- **Affected users**: 100% of active mission operators
- **Frequency**: Every mission after 2-3 minutes
- **Operations affected**: Real-time mission monitoring, safety decisions
- **User experience**: Complete loss of situational awareness, forced mission aborts

### Business Impact
- **SLA breach**: Yes - 99.9% uptime violated
- **Customer complaints**: 5 critical tickets
- **Reputation damage**: High - safety-critical feature

### Technical Impact
- **Services affected**: WebSocket telemetry streaming
- **Error rate**: 15 errors/minute in backend logs
- **System resources**: WebSocket connection pool exhausted

---

## 🔍 Root Cause Analysis

### What Happened
WebSocket telemetry stream was not handling malformed CRSF protocol packets from control box. When a malformed packet arrived, the stream handler threw an uncaught exception, causing the connection to silently fail without reconnecting.

### Why It Happened
Error handling was only implemented for network failures, not for protocol-level parsing errors.

### Code Analysis

**Problematic Code** (Before):
```java
// File: src/main/java/com/example/telemetry/CRSFTelemetryHandler.java:78
public void handleTelemetryPacket(byte[] packet) {
    CRSFPacket crsfPacket = CRSFParser.parse(packet); // Throws exception on malformed packet
    publishToWebSocket(crsfPacket);
}
```

**Why This Was Wrong**:
No try-catch block around parsing - any malformed packet crashed the handler.

---

## ✅ The Fix

### Solution Overview
Added comprehensive error handling with automatic recovery and malformed packet logging.

**Fixed Code** (After):
```java
// File: src/main/java/com/example/telemetry/CRSFTelemetryHandler.java:78
public void handleTelemetryPacket(byte[] packet) {
    try {
        CRSFPacket crsfPacket = CRSFParser.parse(packet);
        publishToWebSocket(crsfPacket);
    } catch (CRSFParseException e) {
        log.warn("Malformed CRSF packet received, skipping: {}", e.getMessage());
        metrics.incrementMalformedPackets();
        // Continue processing next packet
    } catch (Exception e) {
        log.error("Fatal error processing telemetry", e);
        reconnectWebSocket();
    }
}
```

---

## 📊 Metrics

### Code Changes
- **Files changed**: 1 file
- **Lines added**: +8
- **Lines removed**: -2

### Performance Impact
- **Before fix**: 5% error rate, streams fail after 2-3 min
- **After fix**: 0% error rate, streams run continuously
- **Improvement**: 100% reliability restoration

---

**Status**: ✅ Fixed
**Severity**: 🔴 Critical
**Date**: 2026-01-23
**Bug ID**: SM-456
```

---

## Quality Checklist

Before using this template:

- [ ] Root cause identified and documented
- [ ] Fix verified in production
- [ ] Regression tests added
- [ ] Prevention measures documented
- [ ] Monitoring added
- [ ] Lessons learned captured
- [ ] Related issues linked

---

**Version**: 1.0
**Last Updated**: January 23, 2026
**Maintained by**: agentry-core
