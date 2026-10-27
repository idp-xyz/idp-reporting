#!/usr/bin/env python3
"""S01-B ordinary/race contract tests in an isolated Linux guest and real PG16.
Inputs are provisioned tools, never source-product repos. No downloads, KVM,
ptrace, host UID remapping or changes to PostgreSQL root safeguards.
"""
from pathlib import Path
import base64, fcntl, gzip, hashlib, json, os, re, shutil, stat, subprocess, tempfile, time

repo = Path(__file__).resolve().parents[1]
evidence = repo / 'evidence'
evidence.mkdir(exist_ok=True)
lock = open(Path(tempfile.gettempdir()) / ('reporting-s01-' + hashlib.sha256(str(repo).encode()).hexdigest()[:20] + '.lock'), 'a')
fcntl.flock(lock, fcntl.LOCK_EX | fcntl.LOCK_NB)
work = Path(tempfile.mkdtemp(prefix='reporting-s01-vm-'))
status = evidence / 'vm_checks.json'
status.write_text(json.dumps({'status': 'RUNNING', 'production_approved': False}) + '\n')

def required(name):
    if not os.environ.get(name):
        raise ValueError('missing tool input: ' + name)
    return Path(os.environ[name]).resolve(strict=True)

try:
    go, native = required('GO_BIN'), required('REPORTING_PG_BUNDLE')
    qemu, kernel = required('QEMU_BIN'), required('REPORTING_VM_KERNEL')
    busybox, qdata, bios = required('REPORTING_VM_BUSYBOX'), required('REPORTING_QEMU_DATA'), required('REPORTING_VM_BIOS')
    goversion = subprocess.check_output([str(go), 'version'], text=True).strip()
    pgversion = subprocess.check_output([str(native / 'bin/postgres'), '--version'], text=True).strip()
    pinned = re.search(r'(?m)^toolchain (go\S+)$', (repo / 'go.mod').read_text()).group(1)
    if pinned + ' ' not in goversion or not pgversion.startswith('postgres (PostgreSQL) 16.'):
        raise ValueError('pinned ' + pinned + ' and a PostgreSQL 16 bundle required')
    source_hashes={str(p.relative_to(repo)):hashlib.sha256(p.read_bytes()).hexdigest() for p in sorted(repo.rglob('*.go'))}
    expected_by_parent={}
    for parent,filename in [('TestPostgresContract','contracts_test.go'),('TestDurablePostgresContract','durable_test.go')]:
        cases=set(re.findall(r't\.Run\("([^"]+)"\s*,',(repo/'integration'/filename).read_text()))
        if parent=='TestPostgresContract': cases|={'completed_currency_change','completed_timezone_change'}
        expected_by_parent[parent]={parent+'/'+n for n in cases}
    env = os.environ.copy()
    env.update({'GOTOOLCHAIN': 'local', 'CGO_ENABLED': '0'})
    for name, args, cwd in [
        ('normal.test', ['test', '-c', '-o', str(work / 'normal.test')], repo / 'integration'),
        ('test2json', ['build', '-o', str(work / 'test2json'), 'cmd/test2json'], repo),
    ]:
        subprocess.run([str(go), *args], cwd=cwd, env=env, check=True)
    env['CGO_ENABLED'] = '1'
    subprocess.run([str(go), 'test', '-race', '-c', '-o', str(work / 'race.test')], cwd=repo / 'integration', env=env, check=True)
    env['CGO_ENABLED']='0'
    for name,package in [('reporting-demo','./cmd/reporting-demo'),('recovery-probe','./cmd/reporting-recovery-probe')]:
        subprocess.run([str(go),'build','-o',str(work/name),package],cwd=repo,env=env,check=True)
    root = work / 'rootfs'
    root.mkdir()
    shutil.copytree(native, root / 'pg', symlinks=True)
    for binary in [native / 'bin/initdb', native / 'bin/postgres', native / 'bin/pg_ctl', work / 'race.test']:
        output = subprocess.check_output(['ldd', str(binary)], text=True)
        for path in re.findall(r'(?:=>\s+)?(/[^\s()]+)', output):
            src = Path(path)
            if not str(src).startswith(str(native)):
                dest = root / str(src).lstrip('/')
                dest.parent.mkdir(parents=True, exist_ok=True)
                shutil.copy2(src, dest)
    for name in ['normal.test', 'race.test', 'test2json','reporting-demo','recovery-probe']:
        shutil.copy2(work / name, root / name)
    for name in ['bin', 'etc', 'proc', 'sys', 'dev', 'tmp', 'evidence/normal', 'evidence/race', 'usr/share/zoneinfo/Asia']:
        (root / name).mkdir(parents=True, exist_ok=True)
    shutil.copy2(busybox, root / 'bin/busybox')
    for name in ['sh', 'mount', 'chown', 'su', 'cat', 'echo', 'ip', 'poweroff', 'base64', 'uname', 'id', 'sync','kill','sleep','grep']:
        (root / 'bin' / name).symlink_to('busybox')
    for name, body in {
        'passwd': 'root:x:0:0:root:/:/bin/sh\nnobody:x:65534:65534:nobody:/tmp:/bin/sh\n',
        'group': 'root:x:0:\nnobody:x:65534:\n',
        'nsswitch.conf': 'passwd: files\ngroup: files\nhosts: files\n',
        'hosts': '127.0.0.1 localhost\n',
    }.items():
        (root / 'etc' / name).write_text(body)
    shutil.copy2('/usr/share/zoneinfo/Asia/Shanghai', root / 'usr/share/zoneinfo/Asia/Shanghai')
    init = '''#!/bin/sh
export PATH=/bin:/pg/bin
export LD_LIBRARY_PATH=/pg/lib
mount -t proc proc /proc
mount -t sysfs sysfs /sys
mount -t devtmpfs devtmpfs /dev
mount -t tmpfs -o mode=1777 tmpfs /tmp
ip link set lo up
echo REPORTING_VM_BOOT
uname -a
su -s /bin/sh nobody -c 'id'
/pg/bin/postgres --version
chown -R nobody:nobody /evidence
fail() { echo REPORTING_INFRA_FAIL; cat /evidence/initdb.log /evidence/create.log /evidence/postgres.log /evidence/server_*.log 2>/dev/null; poweroff -f; }
su -s /bin/sh nobody -c 'LD_LIBRARY_PATH=/pg/lib /pg/bin/initdb -D /tmp/pgdata -L /pg/share/postgresql -U postgres --no-locale --encoding=UTF8 --auth=trust' > /evidence/initdb.log 2>&1 || fail
for name in reporting_test_s01 reporting_test_s01_race reporting_source_test_s01 reporting_source_test_s01_race; do
 echo "CREATE DATABASE $name;" | su -s /bin/sh nobody -c 'LD_LIBRARY_PATH=/pg/lib /pg/bin/postgres --single -D /tmp/pgdata postgres' >> /evidence/create.log 2>&1 || fail
done
start_pg() { su -s /bin/sh nobody -c 'LD_LIBRARY_PATH=/pg/lib /pg/bin/pg_ctl -D /tmp/pgdata -l /evidence/postgres.log -w -t 40 -o "-h 127.0.0.1 -p 55432 -k /tmp -c shared_buffers=16MB -c max_connections=40" start'; }
start_pg || fail
export REPORTING_S01_REQUIRE_PG=1
for phase in normal race; do
 export REPORTING_S01_TEST_DSN=postgresql://postgres@127.0.0.1:55432/reporting_test_s01?sslmode=disable
 export REPORTING_S01_SOURCE_DSN=postgresql://postgres@127.0.0.1:55432/reporting_source_test_s01?sslmode=disable
 if test "$phase" = race; then
  export REPORTING_S01_TEST_DSN=postgresql://postgres@127.0.0.1:55432/reporting_test_s01_race?sslmode=disable
  export REPORTING_S01_SOURCE_DSN=postgresql://postgres@127.0.0.1:55432/reporting_source_test_s01_race?sslmode=disable
 fi
 export REPORTING_S01_EVIDENCE_DIR=/evidence/$phase
 echo REPORTING_JSON_${phase}_BEGIN
 /test2json -t -p idp.local/reporting/integration /$phase.test -test.v=test2json -test.run '^(TestPostgresContract|TestDurablePostgresContract)$' -test.count=1 -test.timeout=180s
 echo REPORTING_EXIT_${phase}=$?
 echo REPORTING_JSON_${phase}_END
done
# Actual server-process and PostgreSQL restart, in the normal fixture pair.
export REPORTING_S01_TEST_DSN=postgresql://postgres@127.0.0.1:55432/reporting_test_s01?sslmode=disable
export REPORTING_S01_SOURCE_DSN=postgresql://postgres@127.0.0.1:55432/reporting_source_test_s01?sslmode=disable
export REPORTING_PROBE_URL=http://127.0.0.1:7810
export REPORTING_DEMO_TOKEN=$(/bin/busybox dd if=/dev/urandom bs=32 count=1 2>/dev/null | /bin/busybox base64)
server_round=0
start_server() {
 server_round=$((server_round+1))
 /reporting-demo -profile contract-simulator -listen 127.0.0.1:7810 "$@" > /evidence/server_${server_round}.log 2>&1 &
 REPORTING_PROBE_SERVER_PID=$!
 export REPORTING_PROBE_SERVER_PID
 attempt=0
 while test "$attempt" -lt 200; do
  if grep -q 'listening at' /evidence/server_${server_round}.log; then echo REPORTING_SERVER_STARTED=$REPORTING_PROBE_SERVER_PID; return; fi
  kill -0 "$REPORTING_PROBE_SERVER_PID" || fail
  sleep 0.05
  attempt=$((attempt+1))
 done
 fail
}
stop_server() {
 kill -TERM "$REPORTING_PROBE_SERVER_PID" || fail
 wait "$REPORTING_PROBE_SERVER_PID" || fail
 echo REPORTING_SERVER_STOPPED=$REPORTING_PROBE_SERVER_PID
}
probe() { /recovery-probe -phase "$1" -state /evidence/recovery.json || fail; }
probe bootstrap
start_server -initialize-fixture
probe existing
stop_server
start_server -pause-workers
probe queued
su -s /bin/sh nobody -c 'LD_LIBRARY_PATH=/pg/lib /pg/bin/pg_ctl -D /tmp/pgdata -m fast -w stop' || fail
probe offline
stop_server
start_pg || fail
start_server
probe verify
probe revoke
stop_server
start_server -initialize-fixture
probe verify-revoked
stop_server
echo REPORTING_RECOVERY_JSON_BEGIN
cat /evidence/recovery.json
echo REPORTING_RECOVERY_JSON_END

su -s /bin/sh nobody -c 'LD_LIBRARY_PATH=/pg/lib /pg/bin/pg_ctl -D /tmp/pgdata -m fast -w stop' || fail
for file in /evidence/normal/*.xlsx /evidence/race/*.xlsx /evidence/recovery_*.xlsx; do
 if test -f "$file"; then
  echo REPORTING_FILE=$file
  /bin/busybox sha256sum "$file"
  echo REPORTING_XLSX_BEGIN
  base64 "$file" || fail
  echo REPORTING_XLSX_END
 fi
done
echo REPORTING_PG_LOG_BEGIN
cat /evidence/postgres.log
echo REPORTING_PG_LOG_END
sync
echo REPORTING_VM_COMPLETED
poweroff -f
'''
    (root / 'init').write_text(init)
    (root / 'init').chmod(0o755)
    raw, ino = bytearray(), 0
    def entry(name, mode, data=b'', major=0, minor=0):
        global ino
        ino += 1
        encoded = name.encode() + b'\0'
        fields = [ino, mode, 0, 0, 1, 0, len(data), 0, 0, major, minor, len(encoded), 0]
        raw.extend(('070701' + ''.join(f'{v:08x}' for v in fields)).encode())
        raw.extend(encoded)
        raw.extend(b'\0' * ((-len(raw)) % 4))
        raw.extend(data)
        raw.extend(b'\0' * ((-len(raw)) % 4))
    for path in sorted(root.rglob('*')):
        name, mode = path.relative_to(root).as_posix(), path.lstat().st_mode
        if path.is_symlink(): entry(name, mode, os.readlink(path).encode())
        elif path.is_dir(): entry(name, mode)
        elif path.is_file(): entry(name, mode, path.read_bytes())
    for name, major, minor in [('console', 5, 1), ('null', 1, 3), ('random', 1, 8), ('urandom', 1, 9)]:
        entry('dev/' + name, stat.S_IFCHR | 0o666, b'', major, minor)
    entry('TRAILER!!!', 0)
    initrd = work / 'initrd.gz'
    initrd.write_bytes(gzip.compress(bytes(raw), compresslevel=1, mtime=0))
    prefix = []
    if os.environ.get('REPORTING_QEMU_LOADER'):
        prefix = [os.environ['REPORTING_QEMU_LOADER'], '--library-path', os.environ['REPORTING_QEMU_LIBRARY_PATH']]
    cmd = prefix + [str(qemu), '-machine', 'pc', '-accel', 'tcg,thread=multi', '-cpu', 'max', '-smp', '2', '-m', '1024', '-nodefaults', '-display', 'none', '-serial', 'stdio', '-monitor', 'none', '-nic', 'none', '-no-reboot', '-L', str(qdata), '-bios', str(bios), '-kernel', str(kernel), '-initrd', str(initrd), '-append', 'console=ttyS0 rdinit=/init panic=1 random.trust_cpu=on quiet']
    launch = {'command': cmd, 'go': goversion, 'postgres': pgversion, 'guest_tests': {n: hashlib.sha256((work / n).read_bytes()).hexdigest() for n in ['normal.test', 'race.test','reporting-demo','recovery-probe']}, 'host_uid_map': Path('/proc/self/uid_map').read_text(), 'inputs': {str(p): hashlib.sha256(p.read_bytes()).hexdigest() for p in [go, native / 'bin/postgres', kernel, busybox, qemu]}, 'source_go_hashes':source_hashes,'qualification': 'contract simulators, ordinary/race real PG, graceful PG/server restart; no production/crash/capacity qualification'}
    (evidence / 'vm_launch.json').write_text(json.dumps(launch, indent=2) + '\n')
    vm_env = os.environ.copy()
    if os.environ.get('REPORTING_QEMU_MODULE_DIR'): vm_env['QEMU_MODULE_DIR'] = os.environ['REPORTING_QEMU_MODULE_DIR']
    print('Launching isolated guest for S01-B real PostgreSQL ordinary/race contracts', flush=True)
    start = time.monotonic()
    with (evidence / 'qemu_serial.log').open('w') as out:
        result = subprocess.run(cmd, env=vm_env, stdout=out, stderr=subprocess.STDOUT, timeout=300)
    serial = (evidence / 'qemu_serial.log').read_text(errors='replace').replace('\r', '')
    if result.returncode or 'REPORTING_VM_COMPLETED' not in serial or 'REPORTING_INFRA_FAIL' in serial: raise ValueError('guest infrastructure failed')
    summary = {'status': 'PASS', 'production_approved': False, 'go': goversion, 'postgres': pgversion, 'elapsed_seconds': round(time.monotonic() - start, 2), 'phases': {}}
    for phase in ['normal', 'race']:
        match = re.search(rf'REPORTING_JSON_{phase}_BEGIN\n(.*?)REPORTING_EXIT_{phase}=(\d+)', serial, re.S)
        if not match or match.group(2) != '0': raise ValueError(phase + ' tests failed')
        lines = [line for line in match.group(1).splitlines() if line.startswith('{')]
        (evidence / ('pg16_' + phase + '.jsonl')).write_text('\n'.join(lines) + '\n')
        events = [json.loads(line) for line in lines]
        terminal = [e for e in events if e.get('Test') and e.get('Action') in ['pass', 'fail', 'skip']]
        passed = [e['Test'] for e in terminal if e['Action'] == 'pass']
        if any(e['Action'] != 'pass' for e in terminal) or not {'TestPostgresContract','TestDurablePostgresContract'}.issubset(set(passed)): raise ValueError('incomplete ' + phase + ' gate')
        expected=set().union(*expected_by_parent.values())
        if not expected.issubset(set(passed)): raise ValueError('missing tests: ' + repr(expected - set(passed)))
        summary['phases'][phase] = {'passed': passed, 'test_pass_events': len(passed), 'skips': 0, 'failures': 0}
    recovery_match=re.search(r'REPORTING_RECOVERY_JSON_BEGIN\n(.*?)REPORTING_RECOVERY_JSON_END',serial,re.S)
    if not recovery_match: raise ValueError('missing actual-process recovery proof')
    recovery=json.loads(recovery_match.group(1))
    if recovery['Status']!='PASS' or len(recovery['Processes'])!=4 or len({(p['PID'],p['StartTicks']) for p in recovery['Processes']})!=4: raise ValueError('HTTP processes did not restart')
    if recovery['PGStartBefore']==recovery['PGStartAfter'] or recovery['OfflineHTTPStatus']<400 or recovery['RecoveredGeneration']!=recovery['Abandoned']['Job']['Generation']+1: raise ValueError('incomplete restart/outage/fencing proof')
    (evidence/'recovery_checks.json').write_text(json.dumps(recovery,indent=2)+'\n')
    summary['recovery']={'status':'PASS','processes':recovery['Processes'],'postgres_start_before':recovery['PGStartBefore'],'postgres_start_after':recovery['PGStartAfter'],'offline_http_status':recovery['OfflineHTTPStatus'],'reclaimed_generation':recovery['RecoveredGeneration'],'checks':recovery['Checks']}
    exports=[]
    for match in re.finditer(r'REPORTING_FILE=/evidence/([a-zA-Z0-9_/]+\.xlsx)\n([0-9a-f]{64})[^\n]*\nREPORTING_XLSX_BEGIN\n(.*?)REPORTING_XLSX_END',serial,re.S):
        b=base64.b64decode(''.join(match.group(3).split()),validate=True)
        if hashlib.sha256(b).hexdigest()!=match.group(2): raise ValueError('export transfer hash mismatch')
        path=evidence/('pg16_'+match.group(1).replace('/','_'))
        path.write_bytes(b)
        exports.append({'file':path.name,'sha256':match.group(2),'bytes':len(b)})
    if len(exports)!=12: raise ValueError('twelve ordinary/race/recovery XLSX artifacts required')
    summary['exports'] = exports
    if source_hashes!={str(p.relative_to(repo)):hashlib.sha256(p.read_bytes()).hexdigest() for p in sorted(repo.rglob('*.go'))}: raise ValueError('Go source changed during qualification')
    status.write_text(json.dumps(summary, indent=2) + '\n')
    print(json.dumps(summary, indent=2), flush=True)
except BaseException as exc:
    status.write_text(json.dumps({'status': 'FAIL', 'error': str(exc), 'production_approved': False}, indent=2) + '\n')
    raise
finally:
    shutil.rmtree(work, ignore_errors=True)
