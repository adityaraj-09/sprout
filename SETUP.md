# Sprout VM setup (Ubuntu / Azure)

From-scratch guide that matches a working Azure Ubuntu lab: attach a data disk, optional ZFS, install Postgres **matching your upstream major** (e.g. 17 for Supabase), run `sprout-server`, connect logically, branch, and open firewall ports.

Repo: [https://github.com/adityaraj-09/sprout](https://github.com/adityaraj-09/sprout)

---

## 0. What you’re building

```text
laptop CLI  ──HTTP──►  sprout-server (VM :8080)
                              │
 upstream Postgres ──connect──►  data/replicas/<name>/   (local replica)
                                      │
                            branch create --from=<name>
                                      ▼
                               data/branches/<branch>/   (independent Postgres)
```

On Azure/Linux you typically:

1. Attach a **dedicated data disk** (do not put the pool on the OS disk).
2. Prefer **ZFS** for real CoW branches, or **`SPROUT_STORAGE=copy`** for a simple full-copy fallback.
3. Install **Postgres client/server tools** whose major version is **≥ your primary** (Supabase 17 → PG 17 tools).
4. Run with **`SPROUT_COMPUTE=local`** (Docker often mismatches `initdb` major).
5. Open **NSG** for API `8080` and branch ports `55432–55500`.

---

## 1. VM and networking

**Suggested size:** 2+ vCPU, 4+ GB RAM, Ubuntu 22.04/24.04.

**Disks**

| Disk | Role |
|------|------|
| OS (`/dev/sda` etc.) | System only — **do not** `zpool create` here |
| Extra data disk (e.g. `/dev/sdb`, 32G+) | ZFS pool / Sprout data |

**Azure NSG / security group — open:**

| Port | Purpose |
|------|---------|
| `22` | SSH (your IP) |
| `8080` | `sprout-server` API |
| `55432–55500` | Connector + branch Postgres |

From your laptop you will connect like:

```bash
psql "postgresql://sprout@<PUBLIC_IP>:<PORT>/postgres"
```

---

## 2. Install base packages + Go

```bash
sudo apt update
sudo apt install -y build-essential curl ca-certificates git

# Go 1.24+ (adjust URL if needed)
curl -fsSL https://go.dev/dl/go1.24.2.linux-amd64.tar.gz -o /tmp/go.tgz
sudo rm -rf /usr/local/go && sudo tar -C /usr/local -xzf /tmp/go.tgz
echo 'export PATH=/usr/local/go/bin:$PATH' >> ~/.bashrc
export PATH=/usr/local/go/bin:$PATH
go version
```

Clone:

```bash
git clone https://github.com/adityaraj-09/sprout.git ~/sprout
cd ~/sprout
```

---

## 3. Install Postgres tools (match upstream major)

Supabase and many cloud primaries are **PG 17**. Sprout needs `initdb`, `pg_ctl`, `psql`, `pg_dump`, `pg_basebackup` on `PATH`.

```bash
# PGDG repo (Ubuntu)
sudo apt install -y curl ca-certificates
sudo install -d /usr/share/postgresql-common/pgdg
curl -fsSL https://www.postgresql.org/media/keys/ACCC4CF8.asc \
  | sudo gpg --dearmor -o /usr/share/postgresql-common/pgdg/apt.postgresql.org.gpg
. /etc/os-release
echo "deb [signed-by=/usr/share/postgresql-common/pgdg/apt.postgresql.org.gpg] http://apt.postgresql.org/pub/repos/apt ${VERSION_CODENAME}-pgdg main" \
  | sudo tee /etc/apt/sources.list.d/pgdg.list

sudo apt update
sudo apt install -y postgresql-17 postgresql-client-17

# Always put the correct major first on PATH
export PATH="/usr/lib/postgresql/17/bin:$PATH"
echo 'export PATH=/usr/lib/postgresql/17/bin:$PATH' >> ~/.bashrc

which initdb psql pg_dump
initdb --version
pg_dump --version
```

If `pg_dump` major is lower than the primary, logical connect fails with `version_mismatch`.

---

## 4. Data disk + ZFS

### 4a. Find the empty disk

```bash
lsblk -o NAME,SIZE,TYPE,MOUNTPOINT,MODEL
```

Use the **unmounted** data disk (example: `/dev/sdb`). Never wipe the OS disk.

### 4b. Create pool + dataset

```bash
sudo apt install -y zfsutils-linux

# destroy a previous lab pool only if you intend to
# sudo zpool destroy sprout 2>/dev/null || true

sudo zpool create -f sprout /dev/sdb
sudo zfs create -o mountpoint=$HOME/sprout-data sprout/data

zpool status
df -h $HOME/sprout-data
```

### 4c. Fix ownership (critical)

`sudo zfs create` mounts as **root**. SQLite (`control.db`) will fail with “unable to open database file” until:

```bash
sudo chown -R "$USER:$USER" "$HOME/sprout-data"
touch "$HOME/sprout-data/writetest" && rm "$HOME/sprout-data/writetest"
```

### 4d. Storage mode: ZFS CoW vs copy

Sprout storage detection:

- Set **`SPROUT_ZFS_DATASET=sprout/data`** for real ZFS snapshot/clone CoW.
- Or set **`SPROUT_STORAGE=copy`** to use `cp -a` under the mount (simpler; branches are slower).  
  This is what many early Azure labs used successfully.

Recommended once the pool works:

```bash
export SPROUT_ZFS_DATASET=sprout/data
# unset SPROUT_STORAGE   # or do not force copy
```

Lab fallback:

```bash
export SPROUT_STORAGE=copy
```

---

## 5. Environment for `sprout-server`

Replace the public IP with yours:

```bash
export PATH=/usr/lib/postgresql/17/bin:$PATH
export SPROUT_DATA=$HOME/sprout-data
export SPROUT_STORAGE=copy                 # or omit and set SPROUT_ZFS_DATASET=sprout/data
export SPROUT_COMPUTE=local                # prefer local over Docker
export SPROUT_LISTEN=0.0.0.0:8080
export SPROUT_PUBLIC_HOST=20.244.18.205    # your VM public IP
export SPROUT_TOKEN='change-me-long-secret'
export SPROUT_SAFE=true
export SPROUT_DB_USER=sprout               # login role in connection strings
```

Persist in `~/.bashrc` or a systemd unit (below).

Build and run:

```bash
cd ~/sprout
make build
./bin/sprout-server
```

You should see storage/compute/meta lines and `pg_host: <PUBLIC_IP>`.

### Optional: systemd unit

```bash
sudo tee /etc/systemd/system/sprout.service >/dev/null <<EOF
[Unit]
Description=Sprout control plane
After=network.target

[Service]
User=YOUR_USER
WorkingDirectory=/home/YOUR_USER/sprout
Environment=PATH=/usr/lib/postgresql/17/bin:/usr/local/go/bin:/usr/bin
Environment=SPROUT_DATA=/home/YOUR_USER/sprout-data
Environment=SPROUT_STORAGE=copy
Environment=SPROUT_COMPUTE=local
Environment=SPROUT_LISTEN=0.0.0.0:8080
Environment=SPROUT_PUBLIC_HOST=YOUR_PUBLIC_IP
Environment=SPROUT_TOKEN=change-me-long-secret
Environment=SPROUT_SAFE=true
ExecStart=/home/YOUR_USER/sprout/bin/sprout-server
Restart=on-failure

[Install]
WantedBy=multi-user.target
EOF

sudo systemctl daemon-reload
sudo systemctl enable --now sprout
sudo systemctl status sprout
```

---

## 6. CLI from your laptop

```bash
# Go CLI (after make build) or npm:
npm install -g sproutdb-cli

sprout config set api-url http://YOUR_PUBLIC_IP:8080
sprout config set token change-me-long-secret
sprout health
sprout doctor
```

---

## 7. Connect (logical) + branch

Supabase / cloud Postgres usually needs **logical** (not physical):

```bash
# estimate first (hits prod read-only-ish for counts)
sprout connect --name=sup --mode=logical --dry-run 'postgresql://USER:PASS@HOST:5432/postgres'

# full bootstrap (creates publication + slot on prod; wipe is default)
sprout connect --name=sup --mode=logical 'postgresql://USER:PASS@HOST:5432/postgres'

sprout status sup
sprout branch create my-feature --from=sup
# prints URL + psql one-liner, e.g.
#   postgresql://sprout@YOUR_PUBLIC_IP:55434/postgres
#   psql "postgresql://sprout@YOUR_PUBLIC_IP:55434/postgres"
```

Useful flags:

| Flag | Meaning |
|------|---------|
| `--wipe` / `--no-wipe` | Rebootstrap (default) vs resume existing replica |
| `--dry-run` | Table/row estimate only |
| `--tables=a,b` | Logical allowlist |

Lifecycle:

```bash
# one branch
sprout branch suspend my-feature
sprout branch resume my-feature

# connector replica + ALL branches from it
sprout connector suspend sup
sprout connector resume sup

sprout branch diff my-feature
sprout connector delete sup   # drops local replica + remote pub/slot (destructive)
```

---

## 8. Pitfalls we hit (and fixes)

| Symptom | Cause | Fix |
|---------|--------|-----|
| `unable to open database file` / SQLite 14 | ZFS mount owned by root | `chown -R $USER:$USER $HOME/sprout-data` |
| `version_mismatch` / dump fails | Host `pg_dump` older than primary | Install matching major; put `/usr/lib/postgresql/17/bin` first |
| Docker PG14 vs initdb 16/17 | Auto Docker compute | `SPROUT_COMPUTE=local` |
| `network is unreachable` / IPv6 | Azure has no IPv6 route; DNS returns AAAA | Sprout prefers IPv4; ensure primary has an A record / pooler |
| `replication slot … already exists` | Wiped subscriber left slot on prod | Fixed in recent builds; or `SELECT pg_drop_replication_slot('sprout_sub_<name>')` on primary |
| `database "flagforge" does not exist` spam | `pg_isready` without `-d postgres` | Fixed; pull latest |
| `Address already in use` / not ready | Leftover postmaster on port | `pg_ctl -D … stop` or `fuser -k PORT/tcp`, then reconnect |
| Branch URL works remotely? | NSG missing branch ports | Open `55432–55500` |
| Publisher timeouts after sync | Egress / Supabase allowlist | Allow VM egress to primary `5432` |

Clean leftover Postgres processes:

```bash
for d in $HOME/sprout-data/replicas/* $HOME/sprout-data/branches/*; do
  [ -d "$d" ] && pg_ctl -D "$d" stop -m fast 2>/dev/null || true
done
```

---

## 9. Upgrade after `git pull`

```bash
cd ~/sprout
git pull
export PATH=/usr/lib/postgresql/17/bin:$PATH
make build
# restart systemd or:
pkill -f sprout-server || true
./bin/sprout-server
```

---

## 10. Security reminders

- Rotate any DB password pasted into chat or shell history.
- `SPROUT_TOKEN` must not stay `dev-token` on a public IP.
- Remote Postgres uses **trust** when `SPROUT_PUBLIC_HOST` is set — lock down NSG to your IPs; add SCRAM/TLS before any real multi-tenant use.
- `control.db` stores connector URLs (secrets). Keep `$SPROUT_DATA` private.

---

## Quick checklist

- [ ] Extra disk attached and identified (`lsblk`)
- [ ] ZFS pool + `sprout/data` mounted at `$HOME/sprout-data`
- [ ] `chown` so your user can write
- [ ] Postgres **17** (or matching major) first on `PATH`
- [ ] `SPROUT_COMPUTE=local`, `SPROUT_PUBLIC_HOST=<ip>`, `SPROUT_SAFE=true`
- [ ] NSG: `8080` + `55432–55500`
- [ ] `make build` + `sprout-server` running
- [ ] `sprout doctor` / `sprout health` OK from laptop
- [ ] `connect --mode=logical` then `branch create`
