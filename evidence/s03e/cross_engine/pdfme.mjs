// Renders the boxes of a document expectation (export/pdf's <name>.json) with the archived
// pdfme renderer of the S03-D1..D4 delivery, for compare.py. The delivery is not part of this
// repository: node pdfme.mjs ARCHIVE/render/pdfme/render.mjs EXPECTATION.json OUT.pdf
import {readFile, writeFile} from 'node:fs/promises';
import {pathToFileURL} from 'node:url';
const [renderer, expectation, out] = process.argv.slice(2);
const {render} = await import(pathToFileURL(renderer));
const exp = JSON.parse(await readFile(expectation, 'utf8'));
const elements = exp.elements.map((e, i) => ({id: 'n' + (i + 1), source: 'literal', field: '', text: '', x: e.x, y: e.y, width: e.width, height: e.height, font_size: e.size, align: e.align}));
const values = Object.fromEntries(exp.elements.map((e, i) => ['n' + (i + 1), e.text]));
const config = {schema_version: '1.1', kind: 'document', binding: {integration_id: 'i', dataset: {id: 'd', version: '1'}}, elements, print: {mode: 'full', offset_x: exp.offset_x, offset_y: exp.offset_y}};
await writeFile(out, await render(config, values));
console.log(out, elements.length, 'boxes');
