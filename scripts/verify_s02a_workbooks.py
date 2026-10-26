#!/usr/bin/env python3
"""Independent filename-bound OOXML oracle for the two local JWT HTTP fixtures."""
import argparse
import hashlib
import json
from pathlib import Path
import shutil
import tempfile
import xml.etree.ElementTree as ET
import zipfile

NS = {'m': 'http://schemas.openxmlformats.org/spreadsheetml/2006/main'}
EXPECTED = {
    's02a_finance.xlsx': ('finance', 'receivables.aging@1.0.0', '9007199254740993.01',
                         {'C2': '100.25', 'C3': '-0.24', 'C4': '9007199254740893.00'}),
    's02a_shipments.xlsx': ('shipments', 'shipments.daily@1.0.0', '9007199254741000',
                           {'C2': '9007199254740993', 'C3': '7'}),
}


def verify(root):
    records = []
    for run in ['normal', 'race']:
        evidence = json.loads((root / run / 'http_checks.json').read_text())
        assert evidence['status'] == 'PASS'
        assert {p.name for p in (root / run).glob('*.xlsx')} == set(EXPECTED), 'unexpected workbook files'
        for filename, (kind, dataset, total, rows) in EXPECTED.items():
            path = root / run / filename
            data = path.read_bytes()
            sha = hashlib.sha256(data).hexdigest()
            producing_run = evidence['outputs'][kind]
            assert sha == producing_run['SHA256'] and len(data) == producing_run['Bytes'], 'producing-run digest/size mismatch'
            with zipfile.ZipFile(path) as book:
                entries = book.infolist()
                assert len(entries) <= 64 and len({e.filename for e in entries}) == len(entries), 'ZIP entry budget/duplicates'
                assert sum(e.file_size for e in entries) <= 32 << 20, 'ZIP expansion budget'
                assert book.testzip() is None, 'ZIP CRC failure'
                strings = []
                if 'xl/sharedStrings.xml' in book.namelist():
                    document = ET.fromstring(book.read('xl/sharedStrings.xml'))
                    strings = [''.join(si.itertext()) for si in document.findall('m:si', NS)]

                def cells(number):
                    document = ET.fromstring(book.read(f'xl/worksheets/sheet{number}.xml'))
                    out = {}
                    for cell in document.findall('.//m:sheetData/m:row/m:c', NS):
                        assert cell.find('m:f', NS) is None, 'unexpected formula'
                        kind = cell.attrib.get('t')
                        if kind == 's':
                            value = strings[int(cell.find('m:v', NS).text)]
                        elif kind == 'inlineStr':
                            value = ''.join(cell.find('m:is', NS).itertext())
                        else:
                            raise AssertionError('precision requires explicit string cells')
                        out[cell.attrib['r']] = value
                    return out

                table, totals, metadata = cells(1), cells(2), cells(3)
                assert metadata.get('B4') == dataset, 'independent filename/dataset mismatch'
                assert totals.get('B2') == total, 'independent exact total mismatch'
                assert all(table.get(k) == v for k, v in rows.items()), 'independent row precision mismatch'
                assert 'CONTRACT SIMULATOR' in metadata.get('B1', ''), 'missing synthetic data label'
            records.append({'file': f'{run}/{filename}', 'sha256': sha, 'bytes': len(data),
                            'dataset': dataset, 'exact_total': total, 'CRC': 'PASS',
                            'all_cells': 'explicit text', 'formulas': 0})
    return records


def main():
    parser = argparse.ArgumentParser()
    parser.add_argument('--root', type=Path, default=Path(__file__).resolve().parents[1] / 'evidence/s02a')
    parser.add_argument('--negative-controls', action='store_true')
    args = parser.parse_args()
    records = verify(args.root)
    result = {'status': 'PASS', 'reader': 'Python stdlib ZIP/XML; independent of Excelize',
              'oracle': 'literal filename/request-bound dataset, rows and totals', 'workbooks': records}
    (args.root / 'workbook_checks.json').write_text(json.dumps(result, indent=2) + '\n')
    if args.negative_controls:
        with tempfile.TemporaryDirectory(prefix='reporting-s02a-negative-') as directory:
            copied = Path(directory) / 'evidence'
            shutil.copytree(args.root, copied)
            a, b = copied / 'normal/s02a_finance.xlsx', copied / 'normal/s02a_shipments.xlsx'
            first, second = a.read_bytes(), b.read_bytes()
            a.write_bytes(second)
            b.write_bytes(first)
            # Rebind producing-run checksums deliberately: the independent
            # semantic oracle must still reject the swapped fixture contents.
            capture_path = copied / 'normal/http_checks.json'
            capture = json.loads(capture_path.read_text())
            for filename, (kind, *_) in EXPECTED.items():
                data = (copied / 'normal' / filename).read_bytes()
                capture['outputs'][kind]['SHA256'] = hashlib.sha256(data).hexdigest()
                capture['outputs'][kind]['Bytes'] = len(data)
            capture_path.write_text(json.dumps(capture))
            try:
                verify(copied)
            except AssertionError as error:
                assert str(error) == 'independent filename/dataset mismatch', str(error)
                negative = {'status': 'EXPECTED_REJECTION_CONFIRMED', 'case': 'swap finance/shipment and rebind producing-run digests',
                            'reason': str(error), 'original_files_modified': False}
            else:
                raise AssertionError('semantic oracle accepted swapped workbooks')
            (args.root / 'workbook_negative_control.json').write_text(json.dumps(negative, indent=2) + '\n')
    print(f'{len(records)} JWT HTTP workbooks: exact rows/totals, dataset binding, CRC and text cells PASS')


if __name__ == '__main__':
    main()
