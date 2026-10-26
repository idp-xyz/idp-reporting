#!/usr/bin/env python3
"""Host checks. The missing live-DB gate is reported separately, never counted."""
from pathlib import Path
import ast, json, os, subprocess, time

repo = Path(__file__).resolve().parents[1]
go = os.environ.get('GO_BIN', 'go')
evidence = repo / 'evidence'
env = os.environ.copy()
env.update({'GOTOOLCHAIN': 'local', 'GOWORK': 'off', 'GOMAXPROCS': '2'})
for name in ['REPORTING_S01_TEST_DSN', 'REPORTING_S01_REQUIRE_PG', 'REPORTING_S01_EVIDENCE_DIR']:
    env.pop(name, None)
records = []
(evidence / 'host_checks.json').write_text(json.dumps({'status': 'RUNNING', 'commands': []}) + '\n')
for name, args in [('tests', ['test', '-json', './...', '-count=1']), ('race', ['test', '-race', '-json', './...', '-count=1']), ('vet', ['vet', './...']), ('build', ['build', './...']), ('mod_verify', ['mod', 'verify'])]:
    path = evidence / ('host_' + name + ('.jsonl' if name in ['tests', 'race'] else '.log'))
    start = time.monotonic()
    with path.open('w') as out:
        result = subprocess.run([go, *args], cwd=repo, env=env, stdout=out, stderr=subprocess.STDOUT)
    record = {'name': name, 'command': [go, *args], 'exit_code': result.returncode, 'elapsed_seconds': round(time.monotonic() - start, 3), 'log': path.name}
    if name in ['tests', 'race']:
        events = [json.loads(line) for line in path.read_text().splitlines() if line.startswith('{')]
        terminal = [e for e in events if e.get('Test') and e.get('Action') in ['pass', 'fail', 'skip']]
        record['test_pass_events'] = sum(e['Action'] == 'pass' for e in terminal)
        record['passed_top_level'] = [e['Package'] + '/' + e['Test'] for e in terminal if e['Action'] == 'pass' and '/' not in e['Test']]
        record['skipped'] = [e['Package'] + '/' + e['Test'] for e in terminal if e['Action'] == 'skip']
        record['failed'] = [e['Package'] + '/' + e['Test'] for e in terminal if e['Action'] == 'fail']
    records.append(record)
    (evidence / 'host_checks.json').write_text(json.dumps({'status': 'RUNNING' if all(r['exit_code'] == 0 for r in records) else 'FAIL', 'commands': records, 'note': 'Host has no DB DSN. The explicit PostgreSQL skip is not database evidence; see vm_checks.json.'}, indent=2) + '\n')
    print(name, result.returncode, flush=True)
    if result.returncode: raise SystemExit(result.returncode)
for path in (repo / 'scripts').glob('*.py'):
    ast.parse(path.read_text(), filename=str(path))
fmt = str(Path(go).with_name('gofmt')) if '/' in go else 'gofmt'
formatted = subprocess.check_output([fmt, '-l', '.'], cwd=repo, text=True)
if formatted.strip(): raise ValueError('unformatted Go source: ' + formatted)
subprocess.run(['git', 'diff', '--check', '465d2c2', '--', '*.go', '*.py', '*.sql', '*.md', 'go.mod', 'go.sum'], cwd=repo, check=True)
(evidence / 'host_checks.json').write_text(json.dumps({'status': 'PASS', 'commands': records, 'static_checks': ['Python AST', 'Go formatting', 'source whitespace'], 'note': 'Host has no DB DSN. The explicit PostgreSQL skip is not database evidence; see vm_checks.json.'}, indent=2) + '\n')
print('Python syntax, Go formatting, source whitespace PASS')
