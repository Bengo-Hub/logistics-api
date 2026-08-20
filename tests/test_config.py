"""
Test configuration for logistics-api E2E tests.

Production domains from devops-k8s/apps/logistics-api/values.yaml
"""

import os
from dataclasses import dataclass


@dataclass
class TestConfig:
    """Configuration for logistics service E2E tests."""
    
    # Production API URLs (with /api/v1 path prefix for logistics-api)
    # Note: /healthz is at root level, not under /api/v1
    API_BASE_URL: str = "https://logisticsapi.codevertexafrica.com"
    AUTH_API_URL: str = "https://sso.codevertexafrica.com"
    
    # Frontend URL
    FRONTEND_URL: str = "https://logistics.codevertexafrica.com"
    RIDER_APP_URL: str = "https://riderapp.codevertexafrica.com"
    
    # Test tenant
    TENANT_SLUG: str = "urban-loft"
    
    # Test credentials (from auth-api seed script)
    # Demo tenant admin - safe to share, scoped to the codevertex-demo tenant only
    TEST_EMAIL: str = os.getenv("TEST_EMAIL", "admin@demo.codevertexafrica.com")
    TEST_PASSWORD: str = os.getenv("TEST_PASSWORD", "DemoAdmin2024!")
    
    # Rider credentials (specific for logistics tests)
    RIDER_EMAIL: str = os.getenv("RIDER_EMAIL", "rider@urbanloft.com")
    RIDER_PASSWORD: str = os.getenv("RIDER_PASSWORD", "DemoUser2024!")
    
    # Staff/Admin credentials for urban-loft tenant
    STAFF_EMAIL: str = os.getenv("STAFF_EMAIL", "staff@urban-loft.com")
    STAFF_PASSWORD: str = os.getenv("STAFF_PASSWORD", "Staffurban-loft2024!")
    
    TEST_PHONE: str = os.getenv("TEST_PHONE", "+254700000001")
    
    # Timeouts
    DEFAULT_TIMEOUT: int = 30
    
    # Auth endpoints
    AUTH_TOKEN_URL: str = "https://sso.codevertexafrica.com/api/v1/token"
    AUTH_ME_URL: str = "https://sso.codevertexafrica.com/api/v1/auth/me"
    
    # API Paths (to be appended to base URLs)
    API_V1_PATH: str = "/api/v1"
    HEALTH_PATH: str = "/healthz"


# Default config instance
config = TestConfig()
