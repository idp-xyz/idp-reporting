# S10-B the products of the session, in the console

2026-11-09. ADR 0016 decision 4. The console shows what the server and the sources say about the session's products; it changes
nothing.

B1 · 能力与环境 has a 产品连接 panel: each product of `GET /session` and the state its catalog answered: 可用 · N 个数据集, 来源拒绝访问
(403), 来源暂时不可用 (no answer, 502/503/504, a timeout), 回答无法验证 (a catalog the console cannot believe), or 无法读取 (the
code). After sign-in the first product loads as before; the others are checked in the background, one at a time, through the same
cache the catalog uses. 重新检查 asks every source again without the cache. Sign-out clears the panel.

B2 · The data panel's product select marks a product that is not available (`shipment-contract-demo（来源暂时不可用）`); its value
is unchanged.

B3 · No product. Since S10-A the session lists only served products, and may list none. The console signs in, reads no catalog, shows
an error notice and, in the data panel, a note that says who to ask and in which order (the connection, then the binding). The
identity row says 产品连接: 无. The embed page still needs the product its address names.

Tests: DOM test S10-B (an unavailable product listed and marked, 重新检查 reads both catalogs again and clears the mark, a refusal,
an identity with no product); the console evidence script checks the panel on the demo (both products 可用). The manual's 能力与环境
figure is retaken.
