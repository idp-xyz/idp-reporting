#!/usr/bin/env python3
"""Build/start only a fresh copy of this standalone project, then exercise HTTP."""
from pathlib import Path
import hashlib, json, os, secrets, shutil, socket, subprocess, tempfile, time, urllib.error, urllib.request, zipfile, io

repo = Path(__file__).resolve().parents[1]
go = os.environ.get('GO_BIN', 'go')
work = Path(tempfile.mkdtemp(prefix='reporting-independent-'))
env = os.environ.copy()
env.update({'GOWORK': 'off', 'GOTOOLCHAIN': 'local', 'GOMAXPROCS': '2'})
process = None
(repo / 'evidence/independence_checks.json').write_text(json.dumps({'status': 'RUNNING', 'production_approved': False}) + '\n')
try:
    source = work / 'standalone'
    shutil.copytree(repo, source, ignore=shutil.ignore_patterns('.git', 'bin', '__pycache__', 'evidence'))
    if 'replace ' in (source / 'go.mod').read_text(): raise ValueError('local replacement dependency')
    imports = json.loads(subprocess.check_output([go, 'list', '-json', './reporting'], cwd=source, env=env, text=True))['Imports']
    if any('.' in p.split('/')[0] for p in imports): raise ValueError('core has non-standard-library imports')
    deps = subprocess.check_output([go, 'list', '-deps', './cmd/reporting-demo'], cwd=source, env=env, text=True).splitlines()
    if any('idp-brant' in d or 'idp-parcel' in d for d in deps): raise ValueError('product-private dependency')
    forbidden = ['customer.id', 'aging.bucket', 'ar.open_balance', 'receivables.aging', 'shipment.count', 'shipments.daily']
    for path in (source / 'reporting').glob('*.go'):
        if not path.name.endswith('_test.go') and any(word in path.read_text() for word in forbidden): raise ValueError('domain field in core')
    binary = work / 'reporting-demo'
    subprocess.run([go, 'build', '-trimpath', '-o', str(binary), './cmd/reporting-demo'], cwd=source, env=env, check=True)
    for args in [[], ['-profile', 'contract-simulator', '-memory', '-listen', '0.0.0.0:7810']]:
        rejected = subprocess.run([str(binary), *args], cwd=source, env=env, stdout=subprocess.PIPE, stderr=subprocess.STDOUT, timeout=5)
        if rejected.returncode == 0: raise ValueError('unsafe demo startup accepted')
    token = secrets.token_urlsafe(32)
    env['REPORTING_DEMO_TOKEN'] = token
    # The listener is allocated by the OS. Reading its bound address from startup
    # output avoids a reserve/release port race.
    log = work / 'server.log'
    with log.open('w') as out:
        process = subprocess.Popen([str(binary), '-profile', 'contract-simulator', '-memory', '-listen', '127.0.0.1:0'], cwd=source, env=env, stdout=out, stderr=subprocess.STDOUT)
    import re
    base = None
    for _ in range(200):
        if process.poll() is not None: raise ValueError('standalone demo exited')
        match = re.search(r'listening at (http://127\.0\.0\.1:\d+)', log.read_text())
        if match: base = match.group(1) + '/api/reporting/v1'; break
        time.sleep(0.02)
    if base is None: raise ValueError('standalone listener did not start')
    def request(path, payload=None, key=None, authorized=True):
        body = None if payload is None else json.dumps(payload).encode()
        req = urllib.request.Request(base + path, data=body)
        if authorized: req.add_header('Authorization', 'Bearer ' + token)
        if body is not None: req.add_header('Content-Type', 'application/json')
        if key is not None: req.add_header('Idempotency-Key', key)
        try:
            with urllib.request.urlopen(req, timeout=25) as response:
                return response.status, dict(response.headers), response.read()
        except urllib.error.HTTPError as response:
            return response.code, dict(response.headers), response.read()
    if request('/capabilities', authorized=False)[0] != 401: raise ValueError('anonymous access allowed')
    code, _, b = request('/capabilities')
    if code != 200 or json.loads(b)['production_approved'] is not False: raise ValueError('wrong capability profile')
    def wait(job):
        deadline = time.monotonic() + 20
        while time.monotonic() < deadline:
            code, _, body = request('/jobs/' + job['job_id'])
            if code != 200: raise ValueError('job status denied')
            result = json.loads(body)
            if result['state'] == 'succeeded': return result
            if result['state'] in ['failed', 'cancelled']: raise ValueError('task did not succeed')
            time.sleep(0.02)
        raise ValueError('task timeout')
    examples = json.loads((source / 'docs/s00/contracts/query-examples.json').read_text())['examples']
    proofs = []
    for index, example in enumerate(examples):
        query = example['query']
        code, _, _ = request('/catalog?integration_id=' + query['integration_id'])
        if code != 200: raise ValueError('catalog unavailable')
        code, _, b = request('/queries', query)
        if code != 200: raise ValueError('query failed')
        expected = '9007199254740993.01' if index == 0 else '9007199254741000'
        if list(json.loads(b)['totals'].values()) != [expected]: raise ValueError('query total lost precision')
        code, _, b = request('/snapshot-jobs', query, 'same-key')
        if code != 202 or json.loads(b)['state'] != 'queued': raise ValueError('admission is not asynchronous')
        snap = wait(json.loads(b))
        code, _, b = request('/snapshots/' + snap['snapshot_id'] + '/rows?limit=1')
        if code != 200 or len(json.loads(b)['rows']) != 1: raise ValueError('snapshot paging failed')
        code, _, b = request('/exports', {'snapshot_id': snap['snapshot_id']}, 'same-key')
        if code != 202: raise ValueError('export admission failed')
        export = wait(json.loads(b))
        code, headers, content = request('/artifacts/' + export['artifact_id'] + '/content')
        digest = hashlib.sha256(content).hexdigest()
        if code != 200 or headers.get('X-Content-Sha256') != digest: raise ValueError('private artifact digest mismatch')
        with zipfile.ZipFile(io.BytesIO(content)) as workbook:
            if workbook.testzip() is not None: raise ValueError('XLSX CRC failure')
        target = repo / 'evidence' / f'independent_sample_{index+1}.xlsx'
        target.write_bytes(content)
        proofs.append({'integration': query['integration_id'], 'expected_total': expected, 'sha256': digest, 'bytes': len(content), 'file': target.name})
    result = {'status': 'PASS', 'profile': 'S01-A contract simulators with ephemeral repository', 'build': 'fresh project-only copy, GOWORK=off, no Brant source or local replace', 'core_imports': imports, 'product_private_dependencies': [], 'http_chains': proofs, 'production_approved': False}
    (repo / 'evidence/independence_checks.json').write_text(json.dumps(result, indent=2) + '\n')
    print(json.dumps(result, indent=2))
except BaseException as exc:
    (repo / 'evidence/independence_checks.json').write_text(json.dumps({'status': 'FAIL', 'error': str(exc), 'production_approved': False}) + '\n')
    raise
finally:
    if process is not None:
        process.terminate()
        try: process.wait(timeout=8)
        except subprocess.TimeoutExpired: process.kill(); process.wait()
    shutil.rmtree(work, ignore_errors=True)
