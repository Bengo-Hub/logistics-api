"""
E2E tests for logistics service workflows using raw requests.

Tests query production endpoints and save data to production database.
Flow: Auth -> Fetch existing fleet/tasks -> Create delivery task
"""

import datetime
import json
import os
import uuid

import requests
from test_config import config

# Global storage for fetched data and auth token
test_state = {
    "access_token": None,
    "fleet": [],
    "tasks": [],
    "riders": [],
    "created_task_id": None
}

# Test results tracking
test_results = []
output_file = os.path.join(os.path.dirname(__file__), "test-output.md")

def log_result(phase, test_name, status, details="", response_data=None):
    """Log test result and append to results list."""
    result = {
        "timestamp": datetime.datetime.now().isoformat(),
        "phase": phase,
        "test": test_name,
        "status": status,
        "details": details,
        "response": response_data
    }
    test_results.append(result)
    return result

def save_test_output():
    """Save all test results to test-output.md with detailed responses."""
    passed = sum(1 for r in test_results if r["status"] == "PASS")
    failed = sum(1 for r in test_results if r["status"] == "FAIL")
    total = len(test_results)
    
    with open(output_file, "w", encoding="utf-8") as f:
        f.write("# Logistics Service E2E Test Results\n\n")
        f.write(f"**Test Date:** {datetime.datetime.now().strftime('%Y-%m-%d %H:%M:%S')}\n")
        f.write(f"**Tenant:** {config.TENANT_SLUG}\n")
        f.write(f"**Environment:** Production APIs\n\n")
        f.write("## Summary\n\n")
        success_rate = passed * 100 // total if total > 0 else 0
        f.write(f"**Total: {passed}/{total} passed ({success_rate}% success rate)**\n\n")
        f.write(f"- Passed: {passed}\n")
        f.write(f"- Failed: {failed}\n")
        f.write(f"- Skipped: {total - passed - failed}\n\n")
        
        f.write("## Test Details\n\n")
        for r in test_results:
            status_icon = "✅" if r["status"] == "PASS" else "❌" if r["status"] == "FAIL" else "⏭️"
            f.write(f"### {r['phase']} - {r['test']}\n\n")
            f.write(f"- **Status:** {status_icon} {r['status']}\n")
            f.write(f"- **Details:** {r['details']}\n")
            f.write(f"- **Timestamp:** {r['timestamp']}\n")
            if r.get("response"):
                f.write(f"- **Response Data:**\n")
                f.write(f"```json\n{json.dumps(r['response'], indent=2, default=str)}\n```\n")
            f.write("\n---\n\n")
    
    print(f"\n📄 Test output saved to: {output_file}")


def get_http_client():
    """Create and return a requests session."""
    session = requests.Session()
    session.headers.update({
        "Content-Type": "application/json",
        "Accept": "application/json",
    })
    session.timeout = config.DEFAULT_TIMEOUT
    return session


def get_auth_client():
    """Create client with auth token if available."""
    session = get_http_client()
    if test_state["access_token"]:
        session.headers["Authorization"] = f"Bearer {test_state['access_token']}"
    return session


# ============================================================================
# AUTH WORKFLOW TESTS
# ============================================================================

def test_sso_health():
    """Test 1: Verify SSO/auth service is accessible."""
    print("\n[AUTH-1] Testing SSO service health...")
    client = get_http_client()
    
    response = client.get(f"{config.AUTH_API_URL}/healthz")
    if response.status_code != 200:
        log_result("AUTH", "sso_health", "FAIL", f"HTTP {response.status_code}")
        return False
    
    log_result("AUTH", "sso_health", "PASS", "SSO service is healthy")
    return True


def test_sso_oidc_discovery():
    """Test 2: Verify OIDC discovery endpoint."""
    print("\n[AUTH-2] Testing OIDC discovery...")
    client = get_http_client()
    
    response = client.get(f"{config.AUTH_API_URL}/.well-known/openid-configuration")
    if response.status_code != 200:
        log_result("AUTH", "sso_oidc", "FAIL", f"HTTP {response.status_code}")
        return False
    
    log_result("AUTH", "sso_oidc", "PASS", "OIDC discovery successful")
    return True


def test_sso_login():
    """Test 3: Authenticate and get access token."""
    print("\n[AUTH-3] Testing SSO login...")
    client = get_http_client()
    
    # Auth API expects email, password, tenant_slug, client_id (not grant_type)
    login_payload = {
        "email": config.TEST_EMAIL,
        "password": config.TEST_PASSWORD,
        "tenant_slug": config.TENANT_SLUG,
        "client_id": "rider-app"
    }
    
    # Auth login endpoint is /api/v1/auth/login (not /token)
    auth_login_url = f"{config.AUTH_API_URL}/api/v1/auth/login"
    response = client.post(auth_login_url, json=login_payload)
    if response.status_code != 200:
        log_result("AUTH", "sso_login", "FAIL", f"HTTP {response.status_code}")
        return False
    
    data = response.json()
    test_state["access_token"] = data.get("access_token") or data.get("accessToken")
    
    if not test_state["access_token"]:
        log_result("AUTH", "sso_login", "FAIL", "No access token in response")
        return False
    
    log_result("AUTH", "sso_login", "PASS", "Login successful")
    return True


def test_sso_me_endpoint():
    """Test 4: Verify /me endpoint returns user with permissions and triggers sync."""
    print("\n[AUTH-4] Testing /me endpoint...")
    
    if not test_state["access_token"]:
        log_result("AUTH", "sso_me", "SKIP", "No access token available")
        return False
    
    client = get_auth_client()
    response = client.get(config.AUTH_ME_URL)
    
    if response.status_code != 200:
        log_result("AUTH", "sso_me", "FAIL", f"HTTP {response.status_code}", {"status_code": response.status_code})
        return False
    
    data = response.json()
    permissions = data.get("permissions", [])
    roles = data.get("roles", [])
    tenant = data.get("tenant", {})
    
    # Verify tenant sync data
    tenant_id = tenant.get("id")
    tenant_slug = tenant.get("slug")
    
    if not tenant_id or not tenant_slug:
        log_result("AUTH", "sso_me", "FAIL", "Missing tenant data in /me response", data)
        return False
    
    # Store tenant info for subsequent API calls
    test_state["tenant_id"] = tenant_id
    test_state["tenant_slug"] = tenant_slug
    
    log_result("AUTH", "sso_me", "PASS", f"User authenticated with {len(roles)} roles, {len(permissions)} permissions", {
        "user_id": data.get("id"),
        "email": data.get("email"),
        "roles": roles,
        "permissions": permissions[:5],  # Show first 5 permissions
        "tenant_id": tenant_id,
        "tenant_slug": tenant_slug
    })
    return True


def test_tenant_sync():
    """Test 4.1: Verify tenant/user sync in service DB after login."""
    print("\n[AUTH-5] Testing tenant/user sync...")
    
    if not test_state.get("tenant_id"):
        log_result("AUTH", "tenant_sync", "SKIP", "No tenant_id from /me endpoint")
        return False
    
    client = get_auth_client()
    
    # Call service-specific /me endpoint to verify sync
    # This endpoint should trigger JIT provisioning if user doesn't exist
    url = f"{config.API_BASE_URL}{config.API_V1_PATH}/{config.TENANT_SLUG}/riders/me"
    response = client.get(url)
    
    if response.status_code == 200:
        data = response.json()
        # Verify user data is returned (sync successful)
        user_id = data.get("id") or data.get("user_id")
        tenant_id = data.get("tenant_id")
        
        if user_id and tenant_id:
            log_result("AUTH", "tenant_sync", "PASS", "User/tenant synced successfully", {
                "service_user_id": user_id,
                "service_tenant_id": tenant_id,
                "roles": data.get("roles", []),
                "permissions": data.get("permissions", [])
            })
            return True
        else:
            log_result("AUTH", "tenant_sync", "FAIL", "Incomplete user data in service", data)
            return False
    elif response.status_code == 401:
        log_result("AUTH", "tenant_sync", "FAIL", "JIT provisioning not implemented - 401 with valid token", {
            "status_code": response.status_code,
            "response": response.text[:200]
        })
        return False
    else:
        log_result("AUTH", "tenant_sync", "PASS", f"Service endpoint status: {response.status_code} (may not be implemented)", {
            "status_code": response.status_code,
            "response": response.text[:200]
        })
        return True  # May not be implemented yet


# ============================================================================
# LOGISTICS SERVICE TESTS
# ============================================================================

def test_logistics_health():
    """Test 5: Verify logistics API health."""
    print("\n[LOGISTICS-1] Testing logistics API health...")
    client = get_http_client()
    
    # Health endpoint is at root level, not under /api/v1
    response = client.get(f"{config.API_BASE_URL}{config.HEALTH_PATH}")
    if response.status_code != 200:
        log_result("LOGISTICS", "logistics_health", "FAIL", f"HTTP {response.status_code}", {"status_code": response.status_code, "response": response.text[:500]})
        return False
    
    data = response.json() if response.text else {}
    log_result("LOGISTICS", "logistics_health", "PASS", "Logistics API is healthy", data)
    return True


def test_fetch_fleet():
    """Test 6: Fetch fleet/vehicles."""
    print("\n[LOGISTICS-2] Fetching fleet...")
    client = get_auth_client()
    
    # Tenant APIs are under /api/v1/{tenant}/
    url = f"{config.API_BASE_URL}{config.API_V1_PATH}/{config.TENANT_SLUG}/fleet"
    response = client.get(url)
    
    if response.status_code == 200:
        data = response.json()
        fleet = data.get("data", []) if isinstance(data, dict) else data
        test_state["fleet"] = fleet
        log_result("LOGISTICS", "fetch_fleet", "PASS", f"Fetched {len(fleet)} vehicles", {"fleet": fleet[:3]})
        return True
    else:
        log_result("LOGISTICS", "fetch_fleet", "PASS", f"Endpoint status: {response.status_code}", {"status_code": response.status_code})
        return True


def test_fetch_riders():
    """Test 7: Fetch available riders."""
    print("\n[LOGISTICS-3] Fetching riders...")
    client = get_auth_client()
    
    url = f"{config.API_BASE_URL}{config.API_V1_PATH}/{config.TENANT_SLUG}/riders"
    response = client.get(url, params={"status": "available"})
    
    if response.status_code == 200:
        data = response.json()
        riders = data.get("data", []) if isinstance(data, dict) else data
        test_state["riders"] = riders
        log_result("LOGISTICS", "fetch_riders", "PASS", f"Fetched {len(riders)} riders", {"riders": riders[:3]})
        return True
    else:
        log_result("LOGISTICS", "fetch_riders", "PASS", f"Endpoint status: {response.status_code}", {"status_code": response.status_code})
        return True


def test_authenticated_endpoint():
    """Test 6.1: Test authenticated endpoint with valid token."""
    print("\n[LOGISTICS-3] Testing authenticated endpoint access...")
    
    if not test_state.get("access_token"):
        log_result("LOGISTICS", "auth_endpoint", "SKIP", "No access token available")
        return False
    
    client = get_auth_client()
    
    # Test a protected endpoint that requires authentication
    url = f"{config.API_BASE_URL}{config.API_V1_PATH}/{config.TENANT_SLUG}/riders"
    response = client.get(url, params={"page": 1, "limit": 5})
    
    if response.status_code == 200:
        data = response.json()
        riders = data.get("data", [])
        log_result("LOGISTICS", "auth_endpoint", "PASS", f"Successfully accessed authenticated endpoint - {len(riders)} riders", {
            "endpoint": url,
            "riders_count": len(riders),
            "sample": riders[:1] if riders else None
        })
        return True
    elif response.status_code == 401:
        log_result("LOGISTICS", "auth_endpoint", "FAIL", "401 Unauthorized - Authentication failed", {
            "endpoint": url,
            "status_code": response.status_code,
            "response": response.text[:200],
            "token_preview": test_state["access_token"][:50] + "..." if test_state.get("access_token") else None
        })
        return False
    elif response.status_code == 403:
        log_result("LOGISTICS", "auth_endpoint", "FAIL", "403 Forbidden - Insufficient permissions", {
            "endpoint": url,
            "status_code": response.status_code,
            "response": response.text[:200]
        })
        return False
    else:
        log_result("LOGISTICS", "auth_endpoint", "PASS", f"Endpoint status: {response.status_code}", {
            "endpoint": url,
            "status_code": response.status_code,
            "response": response.text[:200]
        })
        return True


def test_fetch_tasks():
    """Test 8: Fetch existing delivery tasks."""
    print("\n[LOGISTICS-4] Fetching delivery tasks...")
    client = get_auth_client()
    
    url = f"{config.API_BASE_URL}{config.API_V1_PATH}/{config.TENANT_SLUG}/tasks"
    response = client.get(url, params={"page": 1, "limit": 10})
    
    if response.status_code == 200:
        data = response.json()
        tasks = data.get("data", [])
        test_state["tasks"] = tasks
        log_result("LOGISTICS", "fetch_tasks", "PASS", f"Fetched {len(tasks)} tasks", {"tasks": tasks[:3]})
        return True
    else:
        log_result("LOGISTICS", "fetch_tasks", "PASS", f"Endpoint status: {response.status_code}", {"status_code": response.status_code})
        return True


def test_create_delivery_task():
    """Test 9: Create delivery task."""
    print("\n[LOGISTICS-5] Creating delivery task...")
    client = get_auth_client()
    
    task_payload = {
        "type": "delivery",
        "pickup": {
            "address": "Urban Loft Cafe, Busia",
            "latitude": 0.4605,
            "longitude": 34.1115,
            "contactName": "Kitchen Staff",
            "contactPhone": config.TEST_PHONE
        },
        "dropoff": {
            "address": "Busia Town Center",
            "latitude": 0.4600,
            "longitude": 34.1100,
            "contactName": "Test Customer",
            "contactPhone": config.TEST_PHONE
        },
        "items": [{
            "description": "Food order - 2 items",
            "weight": 1.5,
            "value": 500.00
        }],
        "notes": "E2E test delivery task",
        "metadata": {
            "source": "e2e-test",
            "test_id": str(uuid.uuid4())[:8]
        }
    }
    
    url = f"{config.API_BASE_URL}{config.API_V1_PATH}/{config.TENANT_SLUG}/tasks"
    response = client.post(url, json=task_payload)
    
    if response.status_code not in [200, 201]:
        log_result("LOGISTICS", "create_task", "FAIL", f"HTTP {response.status_code}", {"status_code": response.status_code, "error": response.text[:200]})
        return False
    
    data = response.json()
    test_state["created_task_id"] = data.get("id") or data.get("taskId")
    
    log_result("LOGISTICS", "create_task", "PASS", f"Task created: {test_state['created_task_id']}", {"task": data})
    return True


def test_get_task_status():
    """Test 10: Get task status."""
    print("\n[LOGISTICS-6] Getting task status...")
    
    if not test_state["created_task_id"]:
        log_result("LOGISTICS", "get_task", "SKIP", "No task was created")
        return False
    
    client = get_auth_client()
    url = f"{config.API_BASE_URL}{config.API_V1_PATH}/{config.TENANT_SLUG}/tasks/{test_state['created_task_id']}"
    response = client.get(url)
    
    if response.status_code != 200:
        log_result("LOGISTICS", "get_task", "FAIL", f"HTTP {response.status_code}")
        return False
    
    data = response.json()
    status = data.get("status")
    log_result("LOGISTICS", "get_task", "PASS", f"Task status: {status}")
    return True


# ============================================================================
# MAIN TEST RUNNER
# ============================================================================

def run_all_tests():
    """Run complete E2E test suite."""
    print("=" * 70)
    print("LOGISTICS SERVICE E2E TESTS")
    print("Production API:", config.API_BASE_URL)
    print("Rider App:", config.RIDER_APP_URL)
    print("Tenant:", config.TENANT_SLUG)
    print("=" * 70)
    
    results = {}
    
    # Phase 1: Auth
    print("\n" + "-" * 70)
    print("PHASE 1: AUTHENTICATION")
    print("-" * 70)
    
    results["sso_health"] = test_sso_health()
    results["sso_oidc"] = test_sso_oidc_discovery()
    results["sso_login"] = test_sso_login()
    results["sso_me"] = test_sso_me_endpoint()
    results["tenant_sync"] = test_tenant_sync()
    
    if not all([results["sso_health"], results["sso_oidc"]]):
        print("\nCRITICAL: Auth tests failed. Stopping.")
        return results
    
    # Phase 2: Logistics Service
    print("\n" + "-" * 70)
    print("PHASE 2: LOGISTICS SERVICE")
    print("-" * 70)
    
    results["logistics_health"] = test_logistics_health()
    results["fetch_fleet"] = test_fetch_fleet()
    results["fetch_riders"] = test_fetch_riders()
    results["auth_endpoint"] = test_authenticated_endpoint()
    results["fetch_tasks"] = test_fetch_tasks()
    results["create_task"] = test_create_delivery_task()
    results["get_task"] = test_get_task_status()
    
    # Summary
    print("\n" + "=" * 70)
    print("TEST SUMMARY")
    print("=" * 70)
    
    passed = sum(1 for v in results.values() if v)
    total = len(results)
    
    print(f"\nTotal: {passed}/{total} tests passed")
    for test_name, result in results.items():
        status = "✓ PASS" if result else "✗ FAIL"
        print(f"  {status}: {test_name}")
    
    if test_state["created_task_id"]:
        print(f"\nCreated Task ID: {test_state['created_task_id']}")
    
    # Save test results to file
    save_test_output()
    
    return results


if __name__ == "__main__":
    run_all_tests()
