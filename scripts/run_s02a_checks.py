#!/usr/bin/env python3
"""Qualify the S02-A identity seam; preserve S01-B PG evidence as historical."""
import argparse
import hashlib
import json
import os
from pathlib import Path
import shutil
import subprocess
import time

BASELINE = 'f425c0d69e0b687565b04a48ca347f9781aa9bea'


def digest(path):
    return hashlib.sha256(path.read_bytes()).hexdigest()


def main():
    parser = argparse.ArgumentParser()
    parser.add_argument('--go', default=os.environ.get('REPORTING_GO') or shutil.which('go'))
    args = parser.parse_args()
    if not args.go:
        parser.error('provide --go or install the pinned Go toolchain')
    root = Path(__file__).resolve().parents[1]
    out = root / 'evidence' / 's02a'
    out.mkdir(parents=True, exist_ok=True)
    env = os.environ.copy()
    for name in ['REPORTING_S01_REQUIRE_PG', 'REPORTING_S01_TEST_DSN', 'REPORTING_S01_SOURCE_DSN']:
        env.pop(name, None)
    env['GOTOOLCHAIN'] = 'local'
    env['CGO_ENABLED'] = '1'
    hashes = {str(p.relative_to(root)): digest(p) for p in sorted(root.rglob('*.go'))}
    results = []
    for name, options in [('normal', ['test', '-json', './...', '-count=1']),
                          ('race', ['test', '-race', '-json', './...', '-count=1']),
                          ('vet', ['vet', './...']), ('build', ['build', './...']),
                          ('mod_verify', ['mod', 'verify'])]:
        command = [args.go, *options]
        env['REPORTING_S02A_EVIDENCE_DIR'] = str(out / name)
        filename = name + ('.jsonl' if name in ['normal', 'race'] else '.log')
        start = time.monotonic()
        with (out / filename).open('w') as log:
            completed = subprocess.run(command, cwd=root, env=env, stdout=log, stderr=subprocess.STDOUT, timeout=180)
        record = {'name': name, 'command': command, 'exit_code': completed.returncode,
                  'elapsed_seconds': round(time.monotonic() - start, 3), 'log': filename}
        if name in ['normal', 'race']:
            events = []
            for line in (out / filename).read_text().splitlines():
                try:
                    events.append(json.loads(line))
                except json.JSONDecodeError:
                    pass
            record['pass_events'] = sum(e.get('Action') == 'pass' and 'Test' in e for e in events)
            record['top_level_passes'] = sum(e.get('Action') == 'pass' and 'Test' in e and '/' not in e['Test'] for e in events)
            record['failures'] = sum(e.get('Action') == 'fail' for e in events)
            record['skips'] = [e['Test'] for e in events if e.get('Action') == 'skip' and 'Test' in e]
            record['packages_without_tests'] = [e['Package'] for e in events if e.get('Action') == 'skip' and 'Test' not in e]
        results.append(record)
        print(name + ': ' + ('PASS' if completed.returncode == 0 else 'FAIL'), flush=True)
        if completed.returncode != 0:
            raise SystemExit('check failed; inspect ' + str(out / filename))
    assert hashes == {str(p.relative_to(root)): digest(p) for p in sorted(root.rglob('*.go'))}, 'source changed during checks'
    unchanged = {}
    for relative in ['reporting/types.go', 'reporting/validation.go', 'reporting/service.go',
                     'reporting/repository_contract.go', 'go.mod', 'go.sum']:
        original = subprocess.check_output(['git', 'show', BASELINE + ':' + relative], cwd=root)
        assert original == (root / relative).read_bytes(), 'baseline changed: ' + relative
        unchanged[relative] = hashlib.sha256(original).hexdigest()
    summary = {'status': 'PASS', 'phase': 'S02-A', 'go': subprocess.check_output([args.go, 'version'], text=True).strip(),
               'baseline': BASELINE, 'checks': results, 'go_source_hashes': hashes,
               'unchanged_core_and_modules': unchanged, 'real_pg_rerun': False,
               'historical_pg_evidence': 'evidence/s01b_baseline', 'production_approved': False,
               'real_identity_provider_qualified': False, 'real_product_adapters_qualified': False}
    (out / 'host_checks.json').write_text(json.dumps(summary, ensure_ascii=False, indent=2) + '\n')


if __name__ == '__main__':
    main()
