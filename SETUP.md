# Sprout VM setup (Ubuntu / Azure)

From-scratch guide that matches a working Azure Ubuntu lab: attach a data disk, optional ZFS, install Postgres **matching your upstream major** (e.g. 17 for Supabase), run `sprout-server`, connect logically, branch, and open firewall ports.

Repo: [https://github.com/adityaraj-09/sprout](https://github.com/adityaraj-09/sprout) · Architecture: [`ARCHITECTURE.md`](ARCHITECTURE.md)

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
5. Open **NSG** for API `8080`, Postgres `5432`, and Mongo `27017` (SNI proxies when using a domain).

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
| `5432` | Postgres SNI proxy (domain URLs). Unique branch ports stay on the VM loopback. |
| `27017` | Mongo SNI passthrough (domain URLs). `mongod` unique ports stay on loopback. |

From your laptop you will connect like:

```bash
psql "postgresql://sprout:<pass>@testdb-lab.strido.fit:5432/postgres"
# or with a public IP (no subdomain / no SNI proxy):
psql "postgresql://sprout:<pass>@<PUBLIC_IP>:<PORT>/postgres"
```

Wildcard DNS: `*.strido.fit` A record → VM IP. The **hostname** selects the branch; clients use port **5432**.

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

## 3b. Optional: MongoDB tools (dump-restore connectors)

MongoDB connectors need `mongod`, `mongodump`, `mongorestore`, and `mongosh` on `PATH`. Compute is **local only** (`SPROUT_COMPUTE=local`). There is no Docker Mongo. With a DNS `SPROUT_PUBLIC_HOST`, clients use `:27017` (`tls=true`); hostname SNI selects the instance. `SPROUT_MONGO_PROXY=false` keeps unique ports.

Ubuntu **24.04 (noble)** has MongoDB **8.0** packages. Ubuntu **22.04 (jammy)** has **7.0**. There is no `noble/mongodb-org/7.0` repo.

```bash
sudo rm -f /etc/apt/sources.list.d/mongodb-org-7.0.list

. /etc/os-release
MONGO_VER=8.0
case "$VERSION_CODENAME" in
  jammy|focal) MONGO_VER=7.0 ;;
esac

curl -fsSL "https://www.mongodb.org/static/pgp/server-${MONGO_VER}.asc" \
  | sudo gpg -o "/usr/share/keyrings/mongodb-server-${MONGO_VER}.gpg" --dearmor
echo "deb [ arch=amd64,arm64 signed-by=/usr/share/keyrings/mongodb-server-${MONGO_VER}.gpg ] https://repo.mongodb.org/apt/ubuntu ${VERSION_CODENAME}/mongodb-org/${MONGO_VER} multiverse" \
  | sudo tee "/etc/apt/sources.list.d/mongodb-org-${MONGO_VER}.list"

sudo apt update
sudo apt install -y mongodb-org mongodb-database-tools mongodb-mongosh

# Sprout starts its own mongod; do not keep the distro service on :27017
sudo systemctl disable --now mongod 2>/dev/null || true

which mongod mongodump mongorestore mongosh
mongod --version | head -1
```

Skip this if you only use Postgres connectors. `sprout doctor` reports these binaries as optional. Restart `sprout-server` after install so it picks up `PATH`.

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

- Set **`SPROUT_ZFS_DATASET=sprout/data`** for real ZFS snapshot/clone CoW. Sprout creates **child datasets** for `main`, each replica, and each branch (so branching from a connector does not snapshot `main`).
- Or set **`SPROUT_STORAGE=copy`** to use `cp -a` under the mount (simpler; branches are slower).  
  This is what many early Azure labs used successfully.

Recommended once the pool works:

```bash
export SPROUT_STORAGE=zfs
export SPROUT_ZFS_DATASET=sprout/data
export SPROUT_ZFS_SUDO=true
```

On Linux, delegated `zfs allow mount` cannot bypass the kernel's root-only
mount requirement. Run ZFS mutations through non-interactive sudo:

```bash
ZFS_BIN="$(command -v zfs)"
CHOWN_BIN="$(command -v chown)"
OWNER="$(id -u)\\:$(id -g)"
DATA_ROOT="$HOME/sprout-data"
echo "$USER ALL=(root) NOPASSWD: $ZFS_BIN, $CHOWN_BIN -- $OWNER $DATA_ROOT/main, $CHOWN_BIN -- $OWNER $DATA_ROOT/replicas/*, $CHOWN_BIN -- $OWNER $DATA_ROOT/branches/*" |
  sudo tee /etc/sudoers.d/sprout-zfs
sudo chmod 0440 /etc/sudoers.d/sprout-zfs
sudo visudo -cf /etc/sudoers.d/sprout-zfs
export SPROUT_ZFS_SUDO=true
```

Keep the sudo rule restricted to a dedicated Sprout VM. Sprout invokes only
its configured dataset hierarchy, but permission to the `zfs` binary itself is
powerful.

Lab fallback:

```bash
export SPROUT_STORAGE=copy
```

---

## 5. Environment for `sprout-server`

Replace the public IP with yours:

```bash
export PATH=/usr/lib/postgresql/17/bin:$PATH
export SPROUT_DATA=/home/flagforge/sprout-data   # or $HOME/sprout-data
export SPROUT_STORAGE=zfs
export SPROUT_ZFS_DATASET=sprout/data            # required with SPROUT_STORAGE=zfs
export SPROUT_ZFS_SUDO=true                      # Linux root mounts; see §4d sudoers
export SPROUT_COMPUTE=local
export SPROUT_LISTEN=0.0.0.0:8080
export SPROUT_PUBLIC_HOST=server_domain          # e.g. strido.fit or the VM public IP
export SPROUT_TOKEN='change-me-long-secret'
export SPROUT_SAFE=true
export SPROUT_GITHUB_CLIENT_ID=Iv1.xxxxxxxx      # GitHub OAuth App; enable Device Flow
# optional: export SPROUT_GITHUB_USERS=alice,bob # omit = any GitHub user can login
export SPROUT_DB_USER=sprout                     # login role in connection strings
# optional:
# export SPROUT_STORAGE=copy                     # lab fallback if ZFS is not configured
# export SPROUT_BRANCH_SUBDOMAIN=false           # keep host as-is (default auto-on for DNS names)
# export SPROUT_PG_PROXY=false                   # advertise unique ports; skip the :5432 SNI proxy
# export SPROUT_MONGO_PROXY=false                # advertise unique Mongo ports; skip the :27017 SNI passthrough
# export SPROUT_TRUST_REMOTE=true                # lab-only: remote trust instead of SCRAM
# export SPROUT_AUTO_RESUME=true                 # restart crashed connectors/branches
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
AmbientCapabilities=CAP_NET_BIND_SERVICE
CapabilityBoundingSet=CAP_NET_BIND_SERVICE
WorkingDirectory=/home/YOUR_USER/sprout
Environment=PATH=/usr/lib/postgresql/17/bin:/usr/local/go/bin:/usr/bin
Environment=SPROUT_DATA=/home/YOUR_USER/sprout-data
Environment=SPROUT_STORAGE=zfs
Environment=SPROUT_ZFS_DATASET=sprout/data
Environment=SPROUT_ZFS_SUDO=true
# Environment=SPROUT_STORAGE=copy   # lab fallback if ZFS is not configured
Environment=SPROUT_COMPUTE=local
Environment=SPROUT_LISTEN=0.0.0.0:8080
Environment=SPROUT_PUBLIC_HOST=server_domain
Environment=SPROUT_TOKEN=change-me-long-secret
Environment=SPROUT_GITHUB_CLIENT_ID=Iv1.xxxxxxxx
# Environment=SPROUT_GITHUB_USERS=alice,bob   # omit for public GitHub login
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

sprout config set api-url http://strido.fit:8080   # or YOUR_PUBLIC_IP:8080
sprout login     # GitHub device flow; opens a browser
sprout whoami
sprout health
sprout doctor
```

Every teammate uses the **same** API URL and **GitHub login** (`sprout login`). Each person runs `sprout connect --name=supabase` for **their own** replica, then `sprout branch create testdb --from=supabase`. Lists and URLs are per GitHub user (`testdb-alice-supabase.strido.fit`). Shared `main` is machine-token only. See README “Team use” and [`SKILL.md`](SKILL.md) for the CLI agent skill.

Create a GitHub OAuth App (any callback URL is fine), enable **Device Flow**, and put the client ID in `SPROUT_GITHUB_CLIENT_ID`. Any GitHub user can then `sprout login`. Optionally set `SPROUT_GITHUB_USERS` or `SPROUT_GITHUB_ORGS` to restrict.

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

MongoDB uses the **same ZFS CoW path** (snapshot the replica dataset, clone a branch dataset, start a new `mongod`). Storage must be `SPROUT_STORAGE=zfs` with `SPROUT_ZFS_DATASET` (or `copy` for a full-file clone):

```bash
sprout connect --name=mongo 'mongodb+srv://USER:PASS@cluster.mongodb.net/test'
sprout branch create feat --from=mongo
# mongodb://sprout:<pass>@feat-<github>-mongo.strido.fit:27017/?tls=true&tlsAllowInvalidCertificates=true&authSource=admin
# mongosh "<that url>"
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
sprout connector delete sup              # blocked if branches still exist
sprout connector delete sup --force      # also destroys branches from this connector
```

---

## 8. Pitfalls we hit (and fixes)

| Symptom | Cause | Fix |
|---------|--------|-----|
| `unable to open database file` / SQLite 14 | ZFS mount owned by root | `chown -R $USER:$USER $HOME/sprout-data` |
| `initdb: could not change permissions` | Child ZFS dataset mounted as root | Latest code + `SPROUT_ZFS_SUDO=true` + the `chown` sudoers rule in §4d |
| `version_mismatch` / dump fails | Host `pg_dump` older than primary | Install matching major; put `/usr/lib/postgresql/17/bin` first |
| Docker PG14 vs initdb 16/17 | Auto Docker compute | `SPROUT_COMPUTE=local` |
| `network is unreachable` / IPv6 | Azure has no IPv6 route; DNS returns AAAA | Sprout prefers IPv4; ensure primary has an A record / pooler |
| `replication slot … already exists` | Wiped subscriber left slot on prod | Fixed in recent builds; or `SELECT pg_drop_replication_slot('sprout_sub_<name>')` on primary |
| `database "flagforge" does not exist` spam | `pg_isready` without `-d postgres` | Fixed; pull latest |
| `Address already in use` / not ready | Leftover postmaster on port | `pg_ctl -D … stop` or `fuser -k PORT/tcp`, then reconnect |
| `port 55434 already in use` while adding a second connector | Failed mongo/postgres connect left `mongod`/`postgres` listening; allocator used to hand out the busy port | Latest code skips busy ports and stops leftover compute on connect failure. Or `ss -lptn 'sport = :55434'` / `fuser -k 55434/tcp`, then retry |
| Mongo `branch create` fails / URL hangs | Source `mongod` must be stopped for a consistent ZFS snapshot; leftover error branches blocked retries; SNI hostname is `<branch>-<github>-<connector>.host` | Pull latest: create cold-stops mongod, clones the ZFS dataset, then restarts. Retry the same branch name. Use the printed `mongodb://` URL (tls=true). |
| `cannot unmount ... pool or dataset is busy` on `connect --wipe` | Leftover `mongod` still has the replica dataset open (often an old `~/sprout/data` mount after `SPROUT_DATA` moved to `~/sprout-data`) | Pull latest — wipe stops mongod, then `zfs unmount -f` / destroy. Or `sudo zfs umount -f sprout/data/replica-<name>` after `mongosh`/`kill` the leftover mongod. |
| `filesystem has dependent clones` on wipe | A CoW branch (e.g. `mango`) is a ZFS clone of the replica | Latest code `zfs promote`s the branch then destroys only the replica. Or `sprout branch delete mango --from=mongo` first. Never `zfs destroy -R` (that deletes branches). |
| Branch URL works remotely? | NSG missing 5432/27017, or proxy not bound | Open `5432` and `27017`; `setcap cap_net_bind_service=+ep ./bin/sprout-server` |
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
- Remote Postgres uses **SCRAM-SHA-256** when `SPROUT_PUBLIC_HOST` is set (loopback stays trust). Connection strings include a generated password. `SPROUT_TRUST_REMOTE=true` is lab-only; `sprout doctor` errors if remote trust is on without `SPROUT_SAFE=true`.
- `control.db` stores connector URLs (secrets). Keep `$SPROUT_DATA` private.

---

## Quick checklist

- [ ] Extra disk attached and identified (`lsblk`)
- [ ] ZFS pool + `sprout/data` mounted at `$HOME/sprout-data`
- [ ] `chown` so your user can write
- [ ] Postgres **17** (or matching major) first on `PATH`
- [ ] `SPROUT_COMPUTE=local`, `SPROUT_STORAGE=zfs`, `SPROUT_ZFS_DATASET=sprout/data`, `SPROUT_ZFS_SUDO=true`, `SPROUT_PUBLIC_HOST=server_domain`, `SPROUT_SAFE=true`
- [ ] Wildcard DNS `*.strido.fit` → VM if using a domain (optional)
- [ ] NSG: `8080` + `5432` + `27017` (domain) or unique branch ports (raw IP)
- [ ] `make build` + `sprout-server` running
- [ ] `sprout doctor` / `sprout health` OK from laptop
- [ ] `connect --mode=logical` then `branch create`
