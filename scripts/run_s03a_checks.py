#!/usr/bin/env python3
"""Current console qualification. Real browser/visual QA is a separate gate."""
import argparse
import hashlib
import json
import os
from pathlib import Path
import re
import shutil
import subprocess
import time

BASELINE = '6f782bd7b4b7b2b4b554cd934e86b41609d0fb6b'


def digest(path):
    return hashlib.sha256(path.read_bytes()).hexdigest()


def main():
    parser = argparse.ArgumentParser()
    parser.add_argument('--go', default=os.environ.get('REPORTING_GO') or shutil.which('go'))
    args = parser.parse_args()
    if not args.go:
        parser.error('provide --go for the toolchain pinned in go.mod')
    root = Path(__file__).resolve().parents[1]
    out = root / 'evidence/s03a'
    out.mkdir(parents=True, exist_ok=True)
    (out / 'host_checks.json').write_text(json.dumps({'phase': 'S03-A', 'status': 'RUNNING',
        'baseline': BASELINE, 'production_approved': False}, indent=2) + '\n')
    env = os.environ.copy()
    for name in ['REPORTING_S01_REQUIRE_PG', 'REPORTING_S01_TEST_DSN', 'REPORTING_S01_SOURCE_DSN']:
        env.pop(name, None)
    env['GOTOOLCHAIN'] = 'local'
    env['REPORTING_GO'] = args.go
    env['REPORTING_S03A_EVIDENCE_DIR'] = str(out / 'dom')
    sources = [*sorted(root.rglob('*.go')), *sorted((root / 'console/assets').glob('*')),
               root / 'console/dom.test.mjs', root / 'package.json', root / 'package-lock.json']
    hashes = {str(p.relative_to(root)): digest(p) for p in sources}
    results = []
    commands = [('normal', [args.go, 'test', '-json', './...', '-count=1']),
                ('race', [args.go, 'test', '-race', '-json', './...', '-count=1']),
                ('vet', [args.go, 'vet', './...']), ('build', [args.go, 'build', './...']),
                ('mod_verify', [args.go, 'mod', 'verify']),
                ('javascript_syntax', ['node', '--check', 'console/assets/app.js']),
                ('dom', ['node', '--test', '--test-reporter=tap', 'console/dom.test.mjs'])]
    for name, command in commands:
        env['REPORTING_S02A_EVIDENCE_DIR'] = str(out / name)
        filename = name + ('.jsonl' if name in ['normal', 'race'] else '.log')
        start = time.monotonic()
        with (out / filename).open('w') as log:
            result = subprocess.run(command, cwd=root, env=env, stdout=log, stderr=subprocess.STDOUT, timeout=180)
        item = {'name': name, 'exit_code': result.returncode, 'elapsed_seconds': round(time.monotonic() - start, 3), 'log': filename}
        if name in ['normal', 'race']:
            events = [json.loads(line) for line in (out / filename).read_text().splitlines() if line.startswith('{')]
            item['pass_events'] = sum(e.get('Action') == 'pass' and 'Test' in e for e in events)
            item['top_level_passes'] = sum(e.get('Action') == 'pass' and 'Test' in e and '/' not in e['Test'] for e in events)
            item['failures'] = sum(e.get('Action') == 'fail' for e in events)
            item['skips'] = [e['Test'] for e in events if e.get('Action') == 'skip' and 'Test' in e]
            item['packages_without_tests'] = [e['Package'] for e in events if e.get('Action') == 'skip' and 'Test' not in e]
        if name == 'dom':
            lines = (out / filename).read_text().splitlines()
            for count in ['tests', 'pass', 'fail', 'cancelled', 'skipped']:
                item[count] = int(next(line.split()[-1] for line in lines if line.startswith('# ' + count + ' ')))
            item['runner'] = 'jsdom 30.1.3 + actual Go loopback HTTP; not a browser'
        results.append(item)
        print(name + ': ' + ('PASS' if result.returncode == 0 else 'FAIL'), flush=True)
        if result.returncode:
            raise SystemExit('failed; inspect ' + str(out / filename))
    assert hashes == {str(p.relative_to(root)): digest(p) for p in sources}, 'source changed during qualification'
    old_paths = subprocess.check_output(['git', 'ls-tree', '-r', '--name-only', BASELINE, 'reporting', 'adapters', 'storage', 'go.mod', 'go.sum'], cwd=root, text=True).splitlines()
    unchanged = {}
    for relative in old_paths:
        original = subprocess.check_output(['git', 'show', BASELINE + ':' + relative], cwd=root)
        current = (root / relative).read_bytes()
        if relative == 'go.mod':  # a toolchain upgrade is not a dependency change
            original, current = (re.sub(rb'(?m)^toolchain .*\n', b'', b) for b in (original, current))
        assert original == current, relative
        unchanged[relative] = hashlib.sha256(original).hexdigest()
    summary = {'phase': 'S03-A', 'status': 'PASS_API_AND_SOFTWARE_DOM_BROWSER_QA_PENDING',
               'baseline': BASELINE, 'go': subprocess.check_output([args.go, 'version'], text=True).strip(),
               'node': subprocess.check_output(['node', '--version'], text=True).strip(),
               'checks': results, 'source_hashes': hashes, 'unchanged_core_adapters_storage_modules': unchanged,
               'browser_qa': 'NOT_VERIFIED_ENVIRONMENT_ROUTE_AND_URL_POLICY_BLOCK',
               'real_pg_rerun': False, 'real_product_integration': 'DEFERRED_BY_USER',
               'production_approved': False}
    (out / 'host_checks.json').write_text(json.dumps(summary, ensure_ascii=False, indent=2) + '\n')


if __name__ == '__main__':
    main()
