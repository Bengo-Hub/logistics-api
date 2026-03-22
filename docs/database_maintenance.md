# Database Maintenance Procedures

This document records operational procedures for maintaining the Logistics Service database within the Kubernetes cluster.

## Database Reset Procedures

### 1. Identify Resources
- **PostgreSQL Pod:** `postgresql-0` (Namespace: `infra`)
- **Logistics API Deployment:** `logistics-api` (Namespace: `logistics`)
- **Database Name:** `logistics`
- **Database User:** `logistics_user`

### 2. Preparation: Scale Down Logistics API
```powershell
kubectl scale deployment logistics-api -n logistics --replicas=0
```

### 3. Terminate Active Sessions
```powershell
kubectl exec postgresql-0 -n infra -- env PGPASSWORD='Vertex2020!' psql -h 127.0.0.1 -U admin_user -d postgres -c "SELECT pg_terminate_backend(pid) FROM pg_stat_activity WHERE datname='logistics' AND pid<>pg_backend_pid();"
```

### 4. Drop and Recreate Database
```powershell
kubectl exec postgresql-0 -n infra -- /bin/bash -c "export PGPASSWORD='Vertex2020!'; dropdb -h 127.0.0.1 -U admin_user logistics --if-exists; createdb -h 127.0.0.1 -U admin_user logistics"
```

### 5. Fix Database Ownership
```powershell
kubectl exec postgresql-0 -n infra -- /bin/bash -c "export PGPASSWORD='Vertex2020!'; psql -h 127.0.0.1 -U admin_user -d postgres -c 'ALTER DATABASE logistics OWNER TO logistics_user;'"
```

### 6. Restore Logistics API Deployment
```powershell
kubectl rollout restart deployment logistics-api -n logistics
```

### 7. Run Seed (after deployment)
```powershell
kubectl exec <logistics-api-pod> -n logistics -- /app/seed
```

### 8. Verification
```powershell
kubectl get pods -n logistics
kubectl exec postgresql-0 -n infra -- env PGPASSWORD='Vertex2020!' psql -h 127.0.0.1 -U admin_user -d logistics -c "SELECT COUNT(*) FROM fleet_members; SELECT COUNT(*) FROM fleets; SELECT COUNT(*) FROM users;"
```

## Common Issues

### 401 Unauthorized on Fleet Endpoints
1. Verify JWT token is valid and not expired
2. Check auth middleware JWKS connectivity: `kubectl logs <pod> -n logistics --tail=50`
3. GET requests skip subscription check but still require valid JWT
4. Verify CORS allows the requesting origin

### Pod Restart Loop
1. Check database connectivity
2. Check NATS connectivity
3. Review pod logs for errors
