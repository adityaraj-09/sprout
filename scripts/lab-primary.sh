#!/usr/bin/env bash
# Spin up a local "production" Postgres for Phase 3 lab (physical replication source).
set -euo pipefail
export PATH="/opt/homebrew/bin:/usr/local/bin:$PATH"

ROOT="$(cd "$(dirname "$0")/.." && pwd)"
DATA="${ARDENT_DATA:-$ROOT/data}/lab-primary"
PORT="${LAB_PRIMARY_PORT:-55431}"
LOG="$ROOT/data/logs/lab-primary.log"

mkdir -p "$(dirname "$LOG")" "$ROOT/data/logs"

if [[ "${1:-}" == "stop" ]]; then
  pg_ctl -D "$DATA" -m fast stop || true
  exit 0
fi

if [[ ! -f "$DATA/PG_VERSION" ]]; then
  echo "→ initdb lab primary at $DATA"
  rm -rf "$DATA"
  initdb -D "$DATA" --auth=trust --no-sync >/dev/null
  cat >> "$DATA/postgresql.conf" <<EOF
port = $PORT
listen_addresses = '127.0.0.1'
unix_socket_directories = '$DATA'
wal_level = logical
max_wal_senders = 10
max_replication_slots = 10
hot_standby = on
fsync = off
synchronous_commit = off
full_page_writes = off
EOF
  cat >> "$DATA/pg_hba.conf" <<EOF
host replication all 127.0.0.1/32 trust
host all all 127.0.0.1/32 trust
local replication all trust
EOF
fi

if ! pg_isready -h 127.0.0.1 -p "$PORT" >/dev/null 2>&1; then
  echo "→ starting lab primary on :$PORT"
  mkdir -p "$(dirname "$LOG")"
  pg_ctl -D "$DATA" -l "$LOG" start -o "-p $PORT"
  for i in $(seq 1 50); do
    pg_isready -h 127.0.0.1 -p "$PORT" >/dev/null 2>&1 && break
    sleep 0.1
  done
fi

psql -h 127.0.0.1 -p "$PORT" -d postgres -v ON_ERROR_STOP=1 <<'SQL'
CREATE TABLE IF NOT EXISTS products (
  id bigserial PRIMARY KEY,
  name text NOT NULL,
  price_cents int NOT NULL DEFAULT 0
);
INSERT INTO products (name, price_cents)
SELECT 'sku-' || g, (g * 100)
FROM generate_series(1, 1000) g
WHERE NOT EXISTS (SELECT 1 FROM products LIMIT 1);
SQL

echo "✓ lab primary ready"
echo "  postgresql://$(whoami)@127.0.0.1:${PORT}/postgres"
echo "  tables: products (1000 rows)"
