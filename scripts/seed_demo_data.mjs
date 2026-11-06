// Seeds a running reporting-demo with the data a person needs to walk the console's scenarios (docs/TEST_SCENARIOS.md): a preview and a
// frozen snapshot of each simulated dataset, an Excel export (and a PDF export when the server renders PDF), the identity's own document
// templates with catalogue metadata (domains, flows, favourites, collections, an archived one, published ones), copies of four samples bound
// to the finance dataset by a field mapping, two dashboards (a draft and a published one), and a saved and published report column layout.
// Everything goes through the public API with the demo's bearer token, exactly as the console does; nothing is written anywhere else.
// Safety: --base must be loopback because the token is sent to it. The script is additive: run it twice and you have two of everything, up to
// the identity's caps (64 templates, 16 layouts, 16 dashboards), which it respects and reports.
// Usage: node scripts/seed_demo_data.mjs --token-file FILE [--base http://127.0.0.1:7811] [--prefix 演示] [--only jobs,templates,samples,dashboards,layout] [--out FILE.json]
import {readFileSync, writeFileSync} from 'node:fs';
import {parseArgs} from 'node:util';

const usage = why => {
  console.error(
    why +
      '\nusage: --token-file FILE [--base http://127.0.0.1:7811] [--prefix TEXT] [--only jobs,templates,samples,dashboards,layout] [--out FILE.json]'
  );
  process.exit(2);
};
let args;
try {
  ({values: args} = parseArgs({
    options: {
      'token-file': {type: 'string'},
      base: {type: 'string'},
      prefix: {type: 'string'},
      only: {type: 'string'},
      out: {type: 'string'},
    },
    strict: true,
  }));
} catch (e) {
  usage(e.message);
}
if (!args['token-file']) usage('--token-file is required');
const base = args.base || 'http://127.0.0.1:7811',
  prefix = args.prefix || '演示';
let baseUrl;
try {
  baseUrl = new URL(base);
} catch {
  usage('--base is not a valid URL');
}
if (baseUrl.protocol !== 'http:' || !['127.0.0.1', 'localhost', '[::1]'].includes(baseUrl.hostname))
  usage('--base must be an http loopback address');
const token = readFileSync(args['token-file'], 'utf8').trim();
const only = new Set(
  (args.only || 'jobs,templates,samples,dashboards,layout')
    .split(',')
    .map(s => s.trim())
    .filter(Boolean)
);
const wants = part => only.has(part);
const sleep = ms => new Promise(r => setTimeout(r, ms));
const made = [],
  skipped = [];

async function api(method, path, body, key) {
  const headers = {Authorization: 'Bearer ' + token, Accept: 'application/json'};
  if (body !== undefined) headers['Content-Type'] = 'application/json';
  if (key) headers['Idempotency-Key'] = key;
  const r = await fetch(base + '/api/reporting/v1' + path, {
    method,
    headers,
    body: body === undefined ? undefined : JSON.stringify(body),
  });
  const text = await r.text();
  let json;
  try {
    json = text ? JSON.parse(text) : null;
  } catch {
    json = {raw: text.slice(0, 200)};
  }
  if (!r.ok) {
    const e = new Error(`${method} ${path} → ${r.status} ${json && json.error ? json.error : ''}`);
    e.status = r.status;
    e.body = json;
    throw e;
  }
  return json;
}
async function poll(id, limit = 60000) {
  const end = Date.now() + limit;
  while (Date.now() < end) {
    const j = await api('GET', '/jobs/' + id);
    if (['succeeded', 'failed', 'cancelled'].includes(j.state)) return j;
    await sleep(300);
  }
  throw new Error('job ' + id + ' did not finish in time');
}
const dateIn = zone => {
  const p = new Intl.DateTimeFormat('en', {
    timeZone: zone,
    year: 'numeric',
    month: '2-digit',
    day: '2-digit',
  }).formatToParts(new Date());
  const g = t => p.find(x => x.type === t).value;
  return `${g('year')}-${g('month')}-${g('day')}`;
};
const querySpec = (integration, d) => ({
  schema_version: '1.0',
  integration_id: integration,
  dataset: {id: d.dataset.id, version: d.dataset.version},
  company_ids: ['company_demo'],
  metrics: d.metrics,
  dimensions: d.dimensions,
  filters: [],
  sort: [],
  as_of: dateIn(d.timezone),
  timezone: d.timezone,
  page: {limit: 50},
  ...(d.currency_policy ? {currency_policy: d.currency_policy} : {}),
});
const binding = (integration, d) => ({
  integration_id: integration,
  dataset: {id: d.dataset.id, version: d.dataset.version},
});
const ok = (what, detail) => {
  made.push([what, detail]);
  console.log('made', what, detail || '');
};
const skip = (what, why) => {
  skipped.push([what, why]);
  console.log('skip', what, '—', why);
};

async function main() {
  const session = await api('GET', '/session'),
    caps = await api('GET', '/capabilities');
  console.log(
    'identity',
    session.identity.subject,
    '@',
    session.identity.tenant,
    '· integrations',
    session.integrations.join(', '),
    '· formats',
    caps.formats.join(', ')
  );
  const catalogs = {};
  for (const integration of session.integrations)
    catalogs[integration] = await api('GET', '/catalog?integration_id=' + encodeURIComponent(integration));
  const finance =
    session.integrations.find(i => catalogs[i].some(d => d.dataset.id === 'receivables.aging')) ||
    session.integrations[0];
  const fin = catalogs[finance].find(d => d.dataset.id === 'receivables.aging') || catalogs[finance][0];
  const finBinding = binding(finance, fin);

  // ---- data: a preview, a snapshot and the exports of every dataset --------------------------------------------------------------
  const snapshots = {};
  if (wants('jobs')) {
    for (const integration of session.integrations)
      for (const d of catalogs[integration]) {
        const q = querySpec(integration, d);
        const preview = await api('POST', '/queries', q);
        ok('查询预览', `${d.dataset.id} · ${preview.rows.length} 行 · 合计 ${JSON.stringify(preview.totals)}`);
        const job = await api('POST', '/snapshot-jobs', q, crypto.randomUUID());
        const done = await poll(job.job_id);
        if (done.state !== 'succeeded') {
          skip('冻结快照 ' + d.dataset.id, done.state + ' ' + (done.error_code || ''));
          continue;
        }
        snapshots[d.dataset.id] = {id: done.snapshot_id, q};
        ok('冻结快照', `${d.dataset.id} · 快照 ${done.snapshot_id} · 任务 ${done.job_id}`);
        const xlsx = await poll(
          (await api('POST', '/exports', {snapshot_id: done.snapshot_id}, crypto.randomUUID())).job_id
        );
        ok('Excel 导出', `${d.dataset.id} · ${xlsx.state} · 任务 ${xlsx.job_id}`);
        if (caps.formats.includes('pdf')) {
          const pdf = await poll(
            (await api('POST', '/exports', {snapshot_id: done.snapshot_id, format: 'pdf'}, crypto.randomUUID())).job_id
          );
          ok('PDF 导出', `${d.dataset.id} · ${pdf.state} · 任务 ${pdf.job_id}`);
        }
      }
  }

  // ---- the identity's templates with catalogue metadata ----------------------------------------------------------------------
  const existing = await api('GET', '/document-templates');
  let room = 64 - existing.length;
  const element = text => ({
    id: 'n1',
    source: 'literal',
    field: '',
    text,
    x: 10,
    y: 25,
    width: 190,
    height: 12,
    font_size: 18,
    align: 'left',
  });
  const templates = [
    ['客户往来对账单', '通用业务', '财务', {favourite: true, collections: ['客户A']}, true],
    ['销售订单', '通用业务', '销售', {}, false],
    ['出库装箱单', '仓储 WMS', '出库', {collections: ['上海仓']}, true],
    ['波次拣货单', '仓储 WMS', '出库', {}, false],
    ['海运订舱委托 · Ocean Booking', '运输 / 货代', '海运', {}, true],
    ['商业发票 · Commercial Invoice', '国际贸易', '贸易单证', {collections: ['客户A']}, true],
    ['航空运单 · Air Waybill (MAWB)', '运输 / 货代', '航空运输', {favourite: true}, true],
    ['旧版入库单（已归档）', '仓储 WMS', '入库', {archived: true}, false],
  ];
  const own = [];
  for (const [name, domain, flow, extra, publish] of wants('templates') ? templates : []) {
    if (room <= 0) {
      skip('模板 ' + name, '已到每身份 64 个模板的上限');
      continue;
    }
    const full = `${prefix} · ${name}`;
    const t = await api('POST', '/document-templates', {
      id: '',
      expected_version: 0,
      config: {schema_version: '1.0', kind: 'document', name: full, binding: finBinding, elements: [element(name)]},
    });
    room--;
    await api('PUT', `/document-templates/${t.id}/catalog`, {
      domain,
      flow,
      favourite: !!extra.favourite,
      archived: !!extra.archived,
      collections: extra.collections || [],
      expected_revision: 0,
    });
    if (publish)
      await api('POST', `/document-templates/${t.id}/release`, {action: 'publish', version: 1, expected_revision: 0});
    // A second saved version for two of them, so that the versions dialog has something to show.
    if (name === '客户往来对账单' || name === '销售订单')
      await api('POST', '/document-templates', {
        id: t.id,
        expected_version: 1,
        config: {
          schema_version: '1.0',
          kind: 'document',
          name: full,
          binding: finBinding,
          elements: [element(name), {...element('第 2 版新增的一行'), id: 'n2', y: 45, font_size: 12}],
        },
      });
    own.push(t.id);
    ok(
      '模板 1.x',
      `${full} · ${t.id.slice(0, 8)} · ${domain} / ${flow}${publish ? ' · 已发布 v1' : ''}${extra.archived ? ' · 已归档' : ''}`
    );
  }

  // ---- copies of four samples, one per domain, bound to the finance dataset by a mapping -------------------------------------------
  const columns = fin.columns;
  for (const kind of wants('samples') ? ['statement', 'wms_pick', 'booking', 'commercial'] : []) {
    if (room <= 0) {
      skip('样例副本 ' + kind, '已到模板上限');
      continue;
    }
    const sample = await api('GET', '/document-templates/samples/' + kind);
    // Row-scope slots are the detail table's columns: they take a dataset column (never a literal; an unmapped one is dropped by the server),
    // text slots alternating over the string columns, numeric slots the numeric column, date slots a string column (as the server's own test does).
    // A table may hold a column once, so each row slot takes a column not yet used (numeric slots the numeric column, the rest the string
    // columns in order); the slots left over stay unmapped and the server drops them. Document-scope slots are literals, except the party
    // slots, which show the customer column.
    const free = {
      string: columns.filter(c => c.type === 'string').map(c => c.id),
      numeric: columns.filter(c => ['decimal', 'integer'].includes(c.type)).map(c => c.id),
    };
    const mapping = {};
    for (const f of sample.field_slots) {
      if (f.scope === 'row') {
        const col = (['decimal', 'integer'].includes(f.type) ? free.numeric : free.string).shift();
        if (col) mapping[f.id] = {column: col};
      } else if (
        /customer|buyer|partner|consignee|shipper|seller/i.test(f.id) &&
        columns.some(c => c.id === 'customer.id')
      )
        mapping[f.id] = {column: 'customer.id'};
      else
        mapping[f.id] = {
          literal: f.id === 'documentNo' ? 'DEMO-2026-0001' : f.id === 'currency' ? 'CNY' : '[' + f.label + ']',
        };
    }
    try {
      const copy = await api('POST', `/document-templates/samples/${kind}/copy`, {
        dataset: {integration_id: finance, id: fin.dataset.id, version: fin.dataset.version},
        name: `${prefix} · ${sample.name}`,
        mapping,
      });
      room--;
      own.push(copy.id);
      if (kind === 'statement')
        await api('POST', `/document-templates/${copy.id}/release`, {
          action: 'publish',
          version: 1,
          expected_revision: 0,
        });
      ok('样例副本 2.x', `${sample.name} · ${copy.id.slice(0, 8)}${kind === 'statement' ? ' · 已发布 v1' : ''}`);
    } catch (e) {
      skip(
        '样例副本 ' + kind,
        e.message + (e.body && e.body.detail ? ' ' + JSON.stringify(e.body.detail).slice(0, 160) : '')
      );
    }
  }

  // ---- dashboards: a draft and a published one ----------------------------------------------------------------------------------
  const metric = fin.columns.find(c => ['decimal', 'integer'].includes(c.type)),
    category = fin.columns.find(c => c.type === 'string');
  const boards = await api('GET', '/dashboards');
  if (!wants('dashboards')) skip('看板', '未选择');
  else if (boards.length < 16 && metric && category) {
    const draft = await api('POST', '/dashboards', {
      id: '',
      expected_version: 0,
      config: {
        schema_version: '1.0',
        kind: 'dashboard',
        binding: finBinding,
        widgets: [
          {type: 'metric', field: metric.id},
          {type: 'table', columns: fin.columns.map(c => c.id)},
        ],
      },
    });
    ok('看板（草稿 v1）', draft.id.slice(0, 8));
    const pub = await api('POST', '/dashboards', {
      id: '',
      expected_version: 0,
      config: {
        schema_version: '1.0',
        kind: 'dashboard',
        binding: finBinding,
        widgets: [
          {type: 'metric', field: metric.id},
          {type: 'bar', category: category.id, field: metric.id},
          {type: 'table', columns: fin.columns.map(c => c.id)},
        ],
      },
    });
    await api('POST', `/dashboards/${pub.id}/release`, {action: 'publish', version: 1, expected_revision: 0});
    ok('看板（已发布 v1）', pub.id.slice(0, 8) + ' · 指标卡 + 柱状图 + 明细表');
  } else skip('看板', boards.length >= 16 ? '已到 16 个看板的上限' : '数据集没有数值或分类字段');

  // ---- a report column layout, saved and published --------------------------------------------------------------------------------
  if (wants('layout'))
    try {
      const layout = {
        schema_version: '1.0',
        binding: finBinding,
        columns: fin.columns
          .map(c => ({id: c.id, align: ['decimal', 'integer'].includes(c.type) ? 'right' : 'left'}))
          .reverse(),
      };
      const saved = await api('POST', '/layouts', {id: '', expected_version: 0, layout});
      await api('POST', `/layouts/${saved.id}/release`, {action: 'publish', version: 1, expected_revision: 0});
      ok('报表列布局（已发布 v1，列序倒转）', saved.id.slice(0, 8));
    } catch (e) {
      skip('报表列布局', e.message);
    }

  console.log('\n| 造出的数据 | 说明 |\n|---|---|');
  for (const [w, d] of made) console.log(`| ${w} | ${d} |`);
  if (skipped.length) {
    console.log('\n| 跳过 | 原因 |\n|---|---|');
    for (const [w, d] of skipped) console.log(`| ${w} | ${d} |`);
  }
  if (args.out)
    writeFileSync(
      args.out,
      JSON.stringify({generated: new Date().toISOString(), base, made, skipped, snapshots}, null, 2)
    );
  console.log(
    `\n本身份现有模板 ${(await api('GET', '/document-templates')).length} / 64，看板 ${(await api('GET', '/dashboards')).length} / 16。任务面板列出服务器上当前身份最近的 50 项任务（S07-C），上表的任务都在其中；更早的任务用“恢复查看”按 ID 打开。数据芯片在登录后是“未选择数据”：先查询一次，或恢复一个快照任务后“查看结果”。`
  );
}
main().catch(e => {
  console.error('ABORT', e.message, e.body ? JSON.stringify(e.body).slice(0, 300) : '');
  process.exit(1);
});
