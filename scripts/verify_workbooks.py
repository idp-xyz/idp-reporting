#!/usr/bin/env python3
"""Independent OOXML readback; does not use the XLSX writer implementation."""
from pathlib import Path
import hashlib, json, zipfile, xml.etree.ElementTree as ET

repo = Path(__file__).resolve().parents[1]
ns = {'m': 'http://schemas.openxmlformats.org/spreadsheetml/2006/main'}
records = []
(repo / 'evidence/workbook_checks.json').write_text(json.dumps({'status': 'RUNNING'}) + '\n')
paths = sorted((repo / 'evidence').glob('pg16_*_sample_*.xlsx')) + sorted((repo / 'evidence').glob('independent_sample_*.xlsx'))
if len(paths) != 6: raise ValueError('six PG/HTTP workbooks required')
for path in paths:
    with zipfile.ZipFile(path) as book:
        if book.testzip() is not None: raise ValueError('CRC failure')
        strings = []
        if 'xl/sharedStrings.xml' in book.namelist():
            root = ET.fromstring(book.read('xl/sharedStrings.xml'))
            strings = [''.join(si.itertext()) for si in root.findall('m:si', ns)]
        def cells(sheet):
            root = ET.fromstring(book.read(f'xl/worksheets/sheet{sheet}.xml'))
            out = {}
            for cell in root.findall('.//m:sheetData/m:row/m:c', ns):
                if cell.find('m:f', ns) is not None: raise ValueError('formula emitted')
                kind = cell.attrib.get('t')
                if kind == 's': value = strings[int(cell.find('m:v', ns).text)]
                elif kind == 'inlineStr': value = ''.join(cell.find('m:is', ns).itertext())
                else: raise ValueError('precision requires explicit string cells')
                out[cell.attrib['r']] = value
            return out
        data, totals, metadata = cells(1), cells(2), cells(3)
        financial = path.stem.endswith('_1')
        expected = '9007199254740993.01' if financial else '9007199254741000'
        if totals.get('B2') != expected: raise ValueError('exact total mismatch')
        if financial:
            if data.get('C2') != '100.25' or data.get('C3') != '-0.24' or data.get('C4') != '9007199254740893.00': raise ValueError('money row precision mismatch')
        elif data.get('C2') != '9007199254740993' or data.get('C3') != '7': raise ValueError('integer precision mismatch')
        if 'CONTRACT SIMULATOR' not in metadata.get('B1', ''): raise ValueError('missing synthetic data label')
        records.append({'file': path.name, 'sha256': hashlib.sha256(path.read_bytes()).hexdigest(), 'bytes': path.stat().st_size, 'exact_total': expected, 'all_cells': 'explicit text', 'formulas': 0, 'CRC': 'PASS'})
(repo / 'evidence/workbook_checks.json').write_text(json.dumps({'status': 'PASS', 'reader': 'Python standard-library OOXML/ZIP/XML independent of Excelize', 'workbooks': records}, indent=2) + '\n')
print('Six workbooks: CRC, typed text, exact money/counts and no formulas PASS')
