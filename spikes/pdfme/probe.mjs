// pdfme probe: renders the shared statement data with @pdfme/generator so it can be checked like
// the Typst and fpdf spikes. Usage: node probe.mjs <data.json> <out.pdf> <font file>
import { readFileSync, writeFileSync, statSync } from 'node:fs';
import { resourceUsage } from 'node:process';
import { generate } from '@pdfme/generator';
import { text, table } from '@pdfme/schemas';

const [dataPath, outPath, fontPath] = process.argv.slice(2);
const data = JSON.parse(readFileSync(dataPath, 'utf8'));
const font = { noto: { data: readFileSync(fontPath), fallback: true } };
const cell = (alignment) => ({
  fontName: 'noto', alignment, verticalAlignment: 'top', fontSize: 9, lineHeight: 1.2, characterSpacing: 0,
  fontColor: '#000000', backgroundColor: '', borderColor: '#999999',
  borderWidth: { top: 0.1, right: 0.1, bottom: 0.1, left: 0.1 }, padding: { top: 1.2, right: 1.4, bottom: 1.2, left: 1.4 },
});
const textSchema = (name, x, y, width, size, alignment, content) => ({
  name, type: 'text', position: { x, y }, width, height: 7, fontSize: size, alignment, verticalAlignment: 'top',
  lineHeight: 1, characterSpacing: 0, fontColor: '#000000', backgroundColor: '', fontName: 'noto', content, readOnly: true,
});
const template = {
  basePdf: {
    width: 210, height: 297, padding: [32, 15, 26, 15],
    staticSchema: [
      textSchema('title', 15, 10, 180, 14, 'center', data.meta.title),
      textSchema('company', 15, 20, 90, 9, 'left', '单位：' + data.meta.company),
      textSchema('customer', 105, 20, 90, 9, 'right', '客户：' + data.meta.customer),
      textSchema('period', 15, 25, 180, 9, 'left', '期间：' + data.meta.period),
      textSchema('page', 15, 280, 180, 9, 'center', '第 {currentPage} 页 / 共 {totalPages} 页'),
    ],
  },
  schemas: [[{
    name: 'rows', type: 'table', position: { x: 15, y: 32 }, width: 180, height: 20,
    showHead: true, repeatHead: true, head: ['序号', '日期', '单据号', '摘要', '金额'],
    headWidthPercentages: [7, 12, 13, 50, 18],
    tableStyles: { borderColor: '#999999', borderWidth: 0.1 },
    headStyles: cell('left'), bodyStyles: { ...cell('left'), alternateBackgroundColor: '' },
    columnStyles: { alignment: { 0: 'right', 4: 'right' } },
  }]],
};
const inputs = [{ rows: JSON.stringify(data.rows.map((r, i) => [String(i + 1), r.date, r.doc, r.desc, r.amount])) }];

const started = performance.now();
const pdf = await generate({ template, inputs, plugins: { text, table }, options: { font, title: data.meta.title } });
const seconds = (performance.now() - started) / 1000;
writeFileSync(outPath, pdf);
console.log(JSON.stringify({ engine: 'pdfme', pdf: outPath, bytes: statSync(outPath).size, rows: data.rows.length,
  render_seconds: seconds, render_max_rss_kb: resourceUsage().maxRSS }));
