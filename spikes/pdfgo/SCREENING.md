# Go 自研版式：底层库筛选

2026-10-27（Asia/Shanghai）。分支 spike/typst-pdf。代码在 screen/，检查脚本 screen/check.py。

## 结论

**选用 codeberg.org/go-pdf/fpdf v0.12.0**（MIT，2026-05-18 发布）。版式、中文折行、分页和承前过次都由我们自己实现，库只负责写出 PDF。

排除 maroto v2.3.3：
- 折行只有两种方式：按空格切分，或在单词中间硬断并加"-"。中文没有空格，所以要么不换行溢出，要么插入错误的连字符，也无法做避头尾。
- 页眉页脚是静态行，做不了逐页不同的承前过次。
- 底层依赖 phpdave11/gofpdf，最后一次发版是 2025-05。

排除 signintech/gopdf v0.38.3：veraPDF 报 5 条 PDF/A-2b 违规。其中 6.1.7.1 是流的 Length 与实际字节数不符，属于结构性错误。另外缺 XMP 和 OutputIntent 的接口，文件尾写的是空的 `/ID`。要达标必须 fork 修改库本身。

## 实测（两页、每页 40 行中文表格、粗体表头、旋转水印、固定时间）

| 项目 | fpdf | gopdf |
|---|---|---|
| 中文原样提取、可搜索 | 通过 | 通过 |
| 同一输入两次输出逐字节一致 | 是 | 是 |
| 文件大小（两页） | 41KB | 101KB |
| veraPDF PDF/A-2b | 只缺文件尾 `/ID` 一条（6.1.3）；用后处理插入后 **0 条违规** | 5 条违规（见上） |
| 831 页（33240 行）耗时 / 峰值内存 / 文件 | 0.25 秒 / 98MB / 2.2MB | 0.73 秒 / 137MB / 3.7MB |
| 网络 | 核心库不引入网络包，只有可选的 contrib/httpimg 用到 net/http | 未检查 |

参照：Typst 渲染 831 页单据用了 8.5–9.3 秒、2.7GB、35MB。但探针的内容更简单（单行单元格、没有折行、没有承前过次），**所以这一行还不是同条件对比**，同条件对比要等完整原型完成后用同一套检查来做。

## fpdf 的已知问题

- 文件尾没有 `/ID`，也没有对应的 API。文件尾位于交叉引用表之后，在这里插入不会移动任何对象偏移。标识取自文档内容的哈希，输出仍然确定。
- 子集字体的名字没有加 PDF 规范要求的 6 位标签前缀（如 `ABCDEF+`）。veraPDF 的 PDF/A 检查不涉及这一项，但这不符合 ISO 32000 第 9.6.4 节。
- 只接受 TrueType（glyf）字体。官方发布的 Noto Sans SC OTF 是 CFF 轮廓，不能直接用。本次的 TrueType 静态字体是从 Google Fonts 官方仓库的可变字体实例化得到的（fontTools varLib.instancer，wght=400 和 700）。Typst 评估用的是官方 OTF，所以两边用的不是同一个字体文件，这一点在对比时要说明。
- API 风格较旧，状态都挂在一个全局对象上，需要用一层自己的封装隔开。

## 复现材料

- 字体：google/fonts 仓库 ofl/notosanssc/NotoSansSC[wght].ttf，sha256 a3041811a78c361b1de50f953c805e0244951c21c5bd412f7232ef0d899af0da；用 fontTools 4.67.0 生成静态 TTF。
- ICC：由 Pillow 内置的 littleCMS 2.19 生成的 sRGB v4.4 配置文件（588 字节），sha256 c97f61cad81b5bdd7f72b6238edf5f2fc7180f040eb12c0299824e0105179326。color.org 的下载地址对脚本请求返回的是 HTML 页面，不是 ICC 文件。
- veraPDF：verapdf/cli:v1.30.3。
