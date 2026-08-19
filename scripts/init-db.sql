-- Runs once on first container start (docker-entrypoint-initdb.d).
-- The primary database (ledgercore) is created by POSTGRES_DB; here we add the
-- separate test database used by `go test`.
CREATE DATABASE ledgercore_test;
