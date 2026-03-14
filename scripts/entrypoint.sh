#!/bin/sh
# Entrypoint script for Logistics-API service
# Waits for database to be ready before starting the server

set -e

echo "=========================================="
echo "Logistics-API Service Startup"
echo "=========================================="

# Create media directory if it doesn't exist (handled by Dockerfile but good for dev)
mkdir -p /media

# Sync media assets to persistent volume if mounted
if [ -d "/media" ] && [ -d "/app/media" ]; then
  echo "📁 Synchronizing media assets to persistent volume..."
  cp -rn /app/media/* /media/ 2>/dev/null || true
  echo "✅ Media synchronization complete"
fi

# Wait for database to be ready (with timeout)
echo "Waiting for database connection..."
MAX_RETRIES=60
RETRY_COUNT=0

# Use the logistics-migrate binary to check connection and run migrations
until /usr/local/bin/logistics-migrate > /dev/null 2>&1 || [ $RETRY_COUNT -eq $MAX_RETRIES ]; do
  RETRY_COUNT=$((RETRY_COUNT+1))
  echo "Database not ready yet or migrations failing... (attempt $RETRY_COUNT/$MAX_RETRIES)"
  sleep 5
done

if [ $RETRY_COUNT -eq $MAX_RETRIES ]; then
  echo "❌ Database connection timeout after $MAX_RETRIES attempts"
  exit 1
else
  echo "✅ Database connected and migrations completed"
fi

echo ""
echo "=========================================="
echo "Running seed (idempotent)"
echo "=========================================="
/usr/local/bin/logistics-seed || echo "⚠️ Seed completed with warnings (non-fatal)"

echo ""
echo "=========================================="
echo "Starting Logistics-API server"
echo "=========================================="
echo ""

exec /usr/local/bin/logistics-api
