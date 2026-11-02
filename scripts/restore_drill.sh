#!/usr/bin/env bash
# The restore drill of S04-F (docs/S04F_SCOPE.md F4, docs/OPERATIONS.md "Backup and restore"): a real dump and restore of the platform's
# PostgreSQL store, a copy of its bucket, the combinations of a database and a bucket of two different moments, the verification command, and a
# kill -9 of the server in the middle of an issue job. Local evidence of one run on one machine; it is not a CI job and not a backup tool.
#
# It needs docker (pg_dump and pg_restore run through `docker exec` in the throwaway PostgreSQL container), the Go toolchain to build the two
# binaries it uses, and a throwaway S3-compatible server. It creates and drops its own databases (reporting_restore_s04f_*), its own roles
# (reporting_restore_s04f_mig, _app) and uses its own bucket, reporting-restore-s04f, whose objects it removes. It never opens the fixture databases
# (reporting_test_s01, reporting_source_test_s01 and their _race pair) and refuses a server whose port is 55432. Every process it starts is
# stopped by pid when it ends, however it ends.
#
#   DRILL_DIR                       where its files go (default: a new temporary directory)
#   DRILL_PG_CONTAINER              the throwaway PostgreSQL container (default reporting-s03g-pg)
#   GO                              the go command (default go)
#   REPORTING_OBJECT_STORE_TEST_ENDPOINT, _ACCESS_KEY, _SECRET_KEY   the throwaway S3-compatible server (http on loopback)
#
#   scripts/restore_drill.sh 2>&1 | tee transcript.txt
set -euo pipefail

root="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "$root"
C="${DRILL_PG_CONTAINER:-reporting-s03g-pg}"
GO="${GO:-go}"
DIR="${DRILL_DIR:-$(mktemp -d)}"
mkdir -p "$DIR/bin"
: "${REPORTING_OBJECT_STORE_TEST_ENDPOINT:?set REPORTING_OBJECT_STORE_TEST_ENDPOINT}"
: "${REPORTING_OBJECT_STORE_TEST_ACCESS_KEY:?set REPORTING_OBJECT_STORE_TEST_ACCESS_KEY}"
: "${REPORTING_OBJECT_STORE_TEST_SECRET_KEY:?set REPORTING_OBJECT_STORE_TEST_SECRET_KEY}"

PORT="$(docker port "$C" 5432/tcp | head -n1 | sed 's/.*://')"
case "$PORT" in
  ''|*[!0-9]*) echo "cannot find the port of $C"; exit 2 ;;
  55432) echo "refusing port 55432"; exit 2 ;;
esac
case "$REPORTING_OBJECT_STORE_TEST_ENDPOINT" in http://127.0.0.1:*|http://localhost:*) ;; *) echo "the object store must be a loopback http endpoint"; exit 2 ;; esac

STAMP="$(date +%Y%m%d_%H%M%S)"
PLAT="reporting_restore_s04f_${STAMP}_plat"   # the platform store the demo session runs on
SRC="reporting_restore_s04f_${STAMP}_src"     # the product simulator: it outlives the platform, as a product does
REST="reporting_restore_s04f_${STAMP}_rest"   # the restore target
MIG=reporting_restore_s04f_mig
APP=reporting_restore_s04f_app
BUCKET=reporting-restore-s04f
LISTEN=127.0.0.1:7811
for n in "$PLAT" "$SRC" "$REST" "$MIG" "$APP"; do case "$n" in reporting_restore_s04f_*) ;; *) echo "bad scratch name $n"; exit 2 ;; esac; done

say() { printf '\n== %s\n' "$*"; }
pg() { docker exec -i "$C" psql -U postgres -v ON_ERROR_STOP=1 "$@"; }
admin_dsn() { echo "postgresql://postgres@127.0.0.1:$PORT/$1?sslmode=disable"; }
app_dsn() { echo "postgresql://$APP@127.0.0.1:$PORT/$1?sslmode=disable"; }

export REPORTING_OBJECT_STORE_ACCESS_KEY="$REPORTING_OBJECT_STORE_TEST_ACCESS_KEY"
export REPORTING_OBJECT_STORE_SECRET_KEY="$REPORTING_OBJECT_STORE_TEST_SECRET_KEY"
export DRILL_TOKEN="restore-drill-token-restore-drill-token"
export DRILL_URL="http://$LISTEN"
export DRILL_SOURCE_DSN="$(admin_dsn "$SRC")"
export DRILL_STALL_FILE="$DIR/stall"
OBJ=(--object-store-endpoint="$REPORTING_OBJECT_STORE_TEST_ENDPOINT" --object-store-bucket="$BUCKET" --object-store-loopback-http)
DRILL="$DIR/bin/restoredrill"
MIGRATE="$DIR/bin/reporting-migrate"
SERVER_PID=""

stop_server() {
  if [ -n "$SERVER_PID" ] && kill -0 "$SERVER_PID" 2>/dev/null; then
    kill -TERM "$SERVER_PID" 2>/dev/null || true
    wait "$SERVER_PID" 2>/dev/null || true
  fi
  SERVER_PID=""
}
cleanup() {
  status=$?
  set +e
  stop_server
  say "cleanup: the scratch objects, databases and roles are removed"
  "$DRILL" bucket purge --dir "$DIR/bucketcopy" "${OBJ[@]}" 2>&1
  for db in "$PLAT" "$SRC" "$REST"; do pg -d postgres -c "DROP DATABASE IF EXISTS $db WITH (FORCE)" >/dev/null 2>&1; done
  for role in "$MIG" "$APP"; do pg -d postgres -c "DROP ROLE IF EXISTS $role" >/dev/null 2>&1; done
  echo "scratch databases left: $(pg -d postgres -At -c "SELECT count(*) FROM pg_database WHERE datname LIKE 'reporting_restore_s04f_%'")"
  echo "scratch roles left: $(pg -d postgres -At -c "SELECT count(*) FROM pg_roles WHERE rolname LIKE 'reporting_restore_s04f_%'")"
  echo "the drill ended with status $status; its files are in $DIR"
  exit $status
}
trap cleanup EXIT

start_server() { # name database
  "$DRILL" serve --listen "$LISTEN" "${OBJ[@]}" >"$DIR/$1.log" 2>&1 &
  SERVER_PID=$!
  for _ in $(seq 1 100); do
    grep -q 'listening at' "$DIR/$1.log" 2>/dev/null && break
    kill -0 "$SERVER_PID" 2>/dev/null || { cat "$DIR/$1.log"; echo "the server did not start"; exit 1; }
    sleep 0.1
  done
  sed 's/^/  server: /' "$DIR/$1.log"
}
serve_on() { # name database
  say "start the server '$1' on $2 (the application role; bucket $BUCKET)"
  DRILL_PLATFORM_DSN="$(app_dsn "$2")" start_server "$1" "$2"
}
# Runs a command that may fail, says its status, and does not stop the drill.
expect_status() { # expected-status command...
  want=$1; shift
  set +e; "$@"; got=$?; set -e
  echo "exit status $got (expected $want)"
  [ "$got" = "$want" ] || { echo "UNEXPECTED STATUS"; exit 1; }
}
verify() { REPORTING_STORE_DSN="$(admin_dsn "$1")" "$MIGRATE" verify-objects "${OBJ[@]}" "${@:2}"; }

say "environment: $(date -u +%FT%TZ), container $C on port $PORT, scratch directory $DIR"
echo "scratch databases before: $(pg -d postgres -At -c "SELECT count(*) FROM pg_database WHERE datname LIKE 'reporting_restore_s04f_%'")"

say "build the binaries the drill uses"
"$GO" build -o "$MIGRATE" ./cmd/reporting-migrate
"$GO" build -o "$DRILL" ./internal/restoredrill
echo built

say "create the scratch databases, the two roles and the bucket"
for db in "$PLAT" "$SRC"; do pg -d postgres -c "CREATE DATABASE $db" >/dev/null; done
for role in "$MIG" "$APP"; do pg -d postgres -c "DROP ROLE IF EXISTS $role" -c "CREATE ROLE $role LOGIN" >/dev/null; done
REPORTING_RESTORE_DRILL_CREATE_BUCKET=1 "$GO" test -count=1 ./internal/restoredrill -run TestTheDrillsBucketIsMadeByATestAlone
"$DRILL" bucket purge --dir "$DIR/bucketcopy" "${OBJ[@]}"

say "the platform store: migrations, production profile, roles"
export REPORTING_STORE_DSN="$(admin_dsn "$PLAT")"
"$MIGRATE" up
"$MIGRATE" init-profile production
"$MIGRATE" roles --migrator-role "$MIG" --app-role "$APP"
"$MIGRATE" status
"$DRILL" init-source

serve_on first "$PLAT"
say "a demo session: snapshot, export, issue and the downloads, through the object store"
"$DRILL" session --state "$DIR/state1.json" --label first
"$DRILL" bucket count "${OBJ[@]}"
echo "-- the rows that name those objects"
pg -d "$PLAT" -c "SELECT kind, data->>'Object' AS object, data->>'Size' AS size FROM reporting_s01.assets WHERE kind='artifact' ORDER BY created_at"

say "the backup: pg_dump -Fc of the platform store, and a copy of the bucket's objects through the client"
docker exec "$C" pg_dump -U postgres -Fc "$PLAT" >"$DIR/platform.dump"
ls -l "$DIR/platform.dump"
DRILL_ADMIN_DSN="$(admin_dsn "$PLAT")" "$DRILL" bucket copy --dir "$DIR/bucketcopy" "${OBJ[@]}"

say "more work after the backup (the lost work of the restore)"
"$DRILL" session --state "$DIR/state2.json" --label later
"$DRILL" bucket count "${OBJ[@]}"
stop_server

say "restore the dump into a fresh database, then re-run roles (the dump is restored without owners or grants)"
pg -d postgres -c "CREATE DATABASE $REST" >/dev/null
docker exec -i "$C" pg_restore -U postgres -d "$REST" --no-owner --no-acl <"$DIR/platform.dump"
export REPORTING_STORE_DSN="$(admin_dsn "$REST")"
"$MIGRATE" roles --migrator-role "$MIG" --app-role "$APP"
"$MIGRATE" status
echo "-- row-level security and its policies are in the restored store"
pg -d "$REST" -At -c "SELECT relname || ' rls=' || relrowsecurity FROM pg_class WHERE relnamespace='reporting_s01'::regnamespace AND relname IN ('jobs','assets') ORDER BY 1" -c "SELECT tablename || ' policy ' || policyname FROM pg_policies WHERE schemaname='reporting_s01' ORDER BY tablename, policyname"
echo "-- the restored store holds $(pg -d "$REST" -At -c "SELECT count(*) FROM reporting_s01.assets WHERE kind='artifact'") artifact rows; the original holds $(pg -d "$PLAT" -At -c "SELECT count(*) FROM reporting_s01.assets WHERE kind='artifact'")"

say "the issue jobs the restored store holds, and the product's copy (a restore older than the product's confirmations)"
pg -d "$REST" -c "SELECT id, state, generation, created_at FROM reporting_s01.jobs WHERE kind = 'issue' ORDER BY created_at, id"
for job in $(awk '/"kind": "issue"/{getline; gsub(/[",]/,""); print $2}' "$DIR/state1.json"); do echo "-- an issue job the restore holds:"; "$DRILL" product --id "$job"; done
for job in $(awk '/"kind": "issue"/{getline; gsub(/[",]/,""); print $2}' "$DIR/state2.json"); do
  echo "-- an issue job made after the backup, which the restored platform does not have (it is not in the list above):"
  "$DRILL" product --id "$job"
  echo "   the product holds a confirmed number the restored platform never recorded: the operator reconciles it with the product by this job id"
done

say "combination 1: the bucket is NEWER than the database (the bucket was not restored)"
"$DRILL" bucket count "${OBJ[@]}"
expect_status 0 verify "$REST" --deep
serve_on bucket-newer "$REST"
"$DRILL" fetch --state "$DIR/state1.json"
"$DRILL" fetch --state "$DIR/state2.json"
stop_server

say "combination 2: the bucket is restored to the moment of the dump (purged, then the copy put back)"
"$DRILL" bucket purge --dir "$DIR/bucketcopy" "${OBJ[@]}"
"$DRILL" bucket restore --dir "$DIR/bucketcopy" "${OBJ[@]}"
"$DRILL" bucket count "${OBJ[@]}"
expect_status 0 verify "$REST" --deep
serve_on bucket-restored "$REST"
"$DRILL" fetch --state "$DIR/state1.json"
"$DRILL" fetch --state "$DIR/state2.json"
stop_server

say "combination 3 (the case the runbook warns of): the DATABASE is NEWER than the bucket (the original database, the restored bucket)"
expect_status 1 verify "$PLAT" --deep
echo "-- with --ids, which prints identifiers, and only for the rows that fail"
expect_status 1 verify "$PLAT" --ids
serve_on database-newer "$PLAT"
"$DRILL" fetch --state "$DIR/state1.json"
"$DRILL" fetch --state "$DIR/state2.json"
stop_server

say "kill -9 in the middle of an issue job on the restored store, and the restart"
serve_on before-kill "$REST"
touch "$DRILL_STALL_FILE"
"$DRILL" issue --label killdemo | tee "$DIR/issue.out"
JOB="$(sed -n 's/^issue job \([0-9a-f]*\) admitted.*/\1/p' "$DIR/issue.out")"
[ -n "$JOB" ] || { echo "no job id"; exit 1; }
for _ in $(seq 1 100); do [ -e "$DRILL_STALL_FILE.hit" ] && break; sleep 0.1; done
[ -e "$DRILL_STALL_FILE.hit" ] || { echo "the worker never reached the render"; exit 1; }
echo "-- the worker is inside the render of job $JOB, after the product reserved its number and before it confirmed the file"
"$DRILL" product --id "$JOB"
pg -d "$REST" -c "SELECT id, state, generation, lease_until > clock_timestamp() AS lease_holds FROM reporting_s01.jobs WHERE id='$JOB'"
echo "-- kill -9 of the server (pid $SERVER_PID)"
kill -9 "$SERVER_PID"; wait "$SERVER_PID" 2>/dev/null || true; SERVER_PID=""
rm -f "$DRILL_STALL_FILE" "$DRILL_STALL_FILE.hit"
pg -d "$REST" -c "SELECT id, state, generation, lease_until > clock_timestamp() AS lease_holds FROM reporting_s01.jobs WHERE id='$JOB'"
echo "-- restart; the job is claimed again once its lease (30 s, by the database's clock) has run out"
serve_on after-kill "$REST"
"$DRILL" wait-job --id "$JOB" --timeout 120s
pg -d "$REST" -c "SELECT id, state, generation FROM reporting_s01.jobs WHERE id='$JOB'" -c "SELECT count(*) AS issue_records FROM reporting_s01.assets WHERE kind='issue' AND id='$JOB'"
"$DRILL" product --id "$JOB"
expect_status 0 verify "$REST" --deep
stop_server
