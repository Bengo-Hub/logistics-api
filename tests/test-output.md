# Logistics Service E2E Test Results

**Test Date:** 2026-03-09 21:51:42
**Tenant:** urban-loft
**Environment:** Production APIs

## Summary

**Total: 7/10 passed (70% success rate)**

- Passed: 7
- Failed: 2
- Skipped: 1

## Test Details

### AUTH - sso_health

- **Status:** ✅ PASS
- **Details:** SSO service is healthy
- **Timestamp:** 2026-03-09T21:51:28.975594

---

### AUTH - sso_oidc

- **Status:** ✅ PASS
- **Details:** OIDC discovery successful
- **Timestamp:** 2026-03-09T21:51:31.039443

---

### AUTH - sso_login

- **Status:** ✅ PASS
- **Details:** Login successful
- **Timestamp:** 2026-03-09T21:51:32.889972

---

### AUTH - sso_me

- **Status:** ✅ PASS
- **Details:** User authenticated with roles: ['member']
- **Timestamp:** 2026-03-09T21:51:34.266357
- **Response Data:**
```json
{
  "user_id": "46898d72-650b-4f2f-8ccc-10a18aae4df6",
  "email": "demo@bengobox.dev",
  "roles": [
    "member"
  ],
  "tenant": null
}
```

---

### LOGISTICS - logistics_health

- **Status:** ❌ FAIL
- **Details:** HTTP 503
- **Timestamp:** 2026-03-09T21:51:35.636290
- **Response Data:**
```json
{
  "status_code": 503,
  "response": "<html>\r\n<head><title>503 Service Temporarily Unavailable</title></head>\r\n<body>\r\n<center><h1>503 Service Temporarily Unavailable</h1></center>\r\n<hr><center>nginx</center>\r\n</body>\r\n</html>\r\n"
}
```

---

### LOGISTICS - fetch_fleet

- **Status:** ✅ PASS
- **Details:** Endpoint status: 503
- **Timestamp:** 2026-03-09T21:51:36.967629
- **Response Data:**
```json
{
  "status_code": 503
}
```

---

### LOGISTICS - fetch_riders

- **Status:** ✅ PASS
- **Details:** Endpoint status: 503
- **Timestamp:** 2026-03-09T21:51:38.449136
- **Response Data:**
```json
{
  "status_code": 503
}
```

---

### LOGISTICS - fetch_tasks

- **Status:** ✅ PASS
- **Details:** Endpoint status: 503
- **Timestamp:** 2026-03-09T21:51:40.223677
- **Response Data:**
```json
{
  "status_code": 503
}
```

---

### LOGISTICS - create_task

- **Status:** ❌ FAIL
- **Details:** HTTP 503
- **Timestamp:** 2026-03-09T21:51:42.241901
- **Response Data:**
```json
{
  "status_code": 503,
  "error": "<html>\r\n<head><title>503 Service Temporarily Unavailable</title></head>\r\n<body>\r\n<center><h1>503 Service Temporarily Unavailable</h1></center>\r\n<hr><center>nginx</center>\r\n</body>\r\n</html>\r\n"
}
```

---

### LOGISTICS - get_task

- **Status:** ⏭️ SKIP
- **Details:** No task was created
- **Timestamp:** 2026-03-09T21:51:42.244640

---

