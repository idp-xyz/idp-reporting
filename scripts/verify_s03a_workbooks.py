#!/usr/bin/env python3
"""Independent OOXML readback of four API regressions and one console DOM file."""
from pathlib import Path
import hashlib
import importlib.util
import json
import xml.etree.ElementTree as ET
import zipfile

ROOT = Path(__file__).resolve().parents[1]
OUT = ROOT / 'evidence/s03a'
NS = {'m': 'http://schemas.openxmlformats.org/spreadsheetml/2006/main'}


def main():
    spec = importlib.util.spec_from_file_location('previous_literal_oracle', ROOT / 'scripts/verify_s02a_workbooks.py')
    oracle = importlib.util.module_from_spec(spec)
    spec.loader.exec_module(oracle)
    records = oracle.verify(OUT)
    path = OUT / 'dom/console_finance.xlsx'
    capture = json.loads((OUT / 'dom/console_download.json').read_text())
    data = path.read_bytes()
    sha = hashlib.sha256(data).hexdigest()
    assert capture['status'] == 'PASS' and capture['sha256'] == sha and capture['bytes'] == len(data)
    with zipfile.ZipFile(path) as book:
        infos = book.infolist()
        assert len(infos) <= 64 and len({i.filename for i in infos}) == len(infos)
        assert sum(i.file_size for i in infos) <= 32 << 20 and book.testzip() is None
        strings = []
        if 'xl/sharedStrings.xml' in book.namelist():
            tree = ET.fromstring(book.read('xl/sharedStrings.xml'))
            strings = [''.join(s.itertext()) for s in tree.findall('m:si', NS)]

        def cells(number):
            tree = ET.fromstring(book.read(f'xl/worksheets/sheet{number}.xml'))
            out = {}
            for cell in tree.findall('.//m:sheetData/m:row/m:c', NS):
                assert cell.find('m:f', NS) is None
                kind = cell.attrib.get('t')
                if kind == 's':
                    value = strings[int(cell.find('m:v', NS).text)]
                elif kind == 'inlineStr':
                    value = ''.join(cell.find('m:is', NS).itertext())
                else:
                    raise AssertionError('precision requires string cells')
                out[cell.attrib['r']] = value
            return out
        rows, totals, metadata = cells(1), cells(2), cells(3)
        assert metadata['B4'] == 'receivables.aging@1.0.0'
        assert totals['B2'] == '9007199254740993.01'
        assert [rows[k] for k in ['C2', 'C3', 'C4']] == ['100.25', '-0.24', '9007199254740893.00']
        assert 'CONTRACT SIMULATOR' in metadata['B1']
    records.append({'file': 'dom/console_finance.xlsx', 'source': 'software DOM + Go HTTP',
                    'sha256': sha, 'bytes': len(data), 'exact_total': '9007199254740993.01',
                    'dataset': 'receivables.aging@1.0.0', 'CRC': 'PASS', 'formulas': 0, 'all_cells': 'explicit text'})
    (OUT / 'workbook_checks.json').write_text(json.dumps({'status': 'PASS', 'workbooks': records, 'browser_download_verified': False}, indent=2) + '\n')
    print('5 workbooks PASS: 4 authenticated HTTP regressions + 1 software DOM console export; browser download not qualified.')


if __name__ == '__main__':
    main()
