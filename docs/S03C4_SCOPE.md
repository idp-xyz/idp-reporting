# S03-C4 current-page bar charts

2026-10-27. Baseline 1fd0c1fba69fd767a8e1c0f1966d25954a6e951d.
Extend existing single-dataset dashboard with strict bar widget:
{type:"bar",category:<known column ID>,field:<known decimal/integer column ID>}.
Use current authorized result rows only, first 50 rows of the current page/preview,
explicitly labelled. No grouping/summing/sorting or claims of complete scope.
Duplicate categories retain separate ordered rows and row ordinals. Metadata only:
no chart values or permission/query changes in saved config. Reuse existing
save/version/publication and authorize both fields on save/get/list/publish/read.
Existing metric/table schema 1.0 remains readable; exact widget unions forbid
unrelated even-null attributes. Old binaries do not support bar variants.

Render signed horizontal bars against a common central zero line and symmetric
scale of largest absolute current plotted value; zero/all-zero and empty pages
must work. Preserve exact original value strings in text. Derive only bounded
pixel ratios with integer/BigInt arithmetic, no business-number Number/float
conversion. Strict plain decimal/integer strings up to 512 chars; invalid or
oversized numeric values must show unavailable chart, never silently omit rows,
round displayed values, coerce null or interpret exponent/NaN/infinity.
Approximate chart lengths and subresolution nonzero values clearly labelled.
Scale determined from plotted rows, not full totals. Text-safe labels and values,
accessible text values and scale information, no third-party runtime/chart library.
Cap widgets 12/config64KiB and retain current query/session invalidation behavior.
Public Go Store/HTTP tests, software DOM with actual loopback Go endpoint plus
explicit synthetic response cases for negative/zero/duplicate/extreme/injection.
No browser/visual qualification inferred. No charts beyond bar, joins/multi-dataset,
PDF/template/production/real-product integration in this slice; real integration last.
