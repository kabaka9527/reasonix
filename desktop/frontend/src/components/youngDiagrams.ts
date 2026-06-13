// Translate `\yng` (ytableau package) and `\young` (youngtab package)
// macros into KaTeX-compatible `\begin{array}...\end{array}` forms.
// KaTeX 0.17 does not include either macro package, so without this
// pass the macros fail with `Undefined control sequence` and the chat
// surfaces the raw LaTeX source as a red error block.
//
// `\yng(2,1)`               — empty (2,1) Young diagram
// `\yng(2,1,3)`             — empty Young diagram with three rows
// `\yng(2,1){a&b\\c\\d&e}`  — same shape, cells filled by row/col
//                              (rows separated by `\\`, cells by `&`)
// `\young(2 1)`             — youngtab syntax (whitespace separator)
//
// Cells are `\square` (a Unicode white-square, rendered by KaTeX as
// `mord amsrm` with a real visible glyph) by default so the diagram
// has the same width as a filled one AND is actually visible. Earlier
// versions used `\hphantom{x}` for invisible placeholder width, which
// renders to *no visible glyph* — correct typesetting but the user
// sees nothing on screen and the chat looks empty.
//
// The translator is stateful: it tracks `$…$` math delimiters so it
// only wraps *bare* `\yng`/`\young` in `$…$`. When the macros are
// already inside a math block (e.g. `$\yng(2,1)$`), it just expands
// the inner form without adding extra delimiters — the inner call in
// the math content is enough.

function parseShape(s: string, sep: "comma" | "space"): number[] {
  const re = sep === "comma" ? /,\s*/ : /\s+/;
  return s
    .trim()
    .split(re)
    .map((x) => parseInt(x, 10))
    .filter((n) => Number.isFinite(n));
}

function expandShape(rows: number[], content: string | undefined): string {
  const maxN = rows.length === 0 ? 0 : Math.max(...rows);
  // 2D array of cell content. Each cell is `\square` by default
  // (visible Unicode white-square) so the diagram has uniform width
  // AND is actually visible to the reader.
  //
  // Each cell is wrapped in `\raisebox{-0.35em}{...}` to nudge the
  // square glyph down by ~half the math-axis offset. Without this,
  // each row's square sits centred on its baseline, and the gap
  // between the bottom of one square and the top of the next is
  // roughly half the math-axis height — visible white space between
  // rows. The negative shift pulls the square down so consecutive
  // rows touch (Young diagrams are conventionally drawn as a single
  // connected shape, not as separate boxes with gaps).
  //
  // The raise argument uses `\square` directly (no `$…$` wrapper) so
  // the raise-box content stays math-mode-safe even when the whole
  // macro is itself already wrapped in `$…$` by the prose case —
  // nesting `$` inside `$…$` would break katex's parser.
  const cells: string[][] = Array.from({ length: rows.length }, () =>
    Array(maxN).fill("\\raisebox{-0.35em}{\\square}"),
  );

  if (content) {
    // Parse content: rows separated by `\\`, cells separated by `&`.
    // The content may contain nested `{...}` (e.g. `\frac{a}{b}`), so
    // we split on `\\` and `&` at brace-depth 0 only.
    const splitAtTopLevel = (s: string, sep: string): string[] => {
      const out: string[] = [];
      let depth = 0;
      let buf = "";
      for (let i = 0; i < s.length; i++) {
        const ch = s[i];
        if (ch === "{") depth++;
        else if (ch === "}") depth--;
        if (depth === 0 && s.startsWith(sep, i)) {
          out.push(buf);
          buf = "";
          i += sep.length - 1;
          continue;
        }
        buf += ch;
      }
      out.push(buf);
      return out;
    };
    const contentRows = splitAtTopLevel(content, "\\\\");
    for (let i = 0; i < contentRows.length && i < rows.length; i++) {
      const cs = splitAtTopLevel(contentRows[i], "&");
      for (let j = 0; j < cs.length && j < rows[i]; j++) {
        const c = cs[j].trim();
        // Empty cells get a raised \square (the default); filled cells
        // get the same raise so all cells in a row share the same
        // vertical position. Without the raise, `\square` centres on the
        // math axis while text sits on the baseline.
        //
        // We pass `c` directly into the raise argument — NOT wrapped in
        // `$…$`. The whole Young-diagram macro is already inside math
        // mode (either the model wrote `$…$`/`$$…$$`, or the prose
        // wrapper added it), so nesting `$` inside `$…$` would break the
        // parser. `\raisebox{...}{<math content>}` works for any math
        // token without an extra `$…$` wrapper.
        cells[i][j] = c === ""
          ? "\\raisebox{-0.35em}{\\square}"
          : `\\raisebox{-0.35em}{${c}}`;
      }
    }
  }

  // Each row is left-aligned by `\begin{array}{l}…\end{array}` plus the
  // joined cells, with rows separated by `\\`. Empty / missing trailing
  // cells in shorter rows are already absent because we slice.
  //
  // Cells are joined with `\!` (negative thin space, -0.1667em) rather
  // than `\,` (positive thin space, +0.1667em). The two cancel out
  // exactly, giving *zero* visible spacing between adjacent boxes —
  // which is how Young diagrams are conventionally drawn. `\,` would
  // leave a visible gap between every box, making the diagram look
  // like a row of spaced-out boxes rather than a connected shape.
  const arrRows = cells.map((row, ri) =>
    row.slice(0, rows[ri]).join(" \\! "),
  );
  // Use `{l}` (left) instead of `{c}` (centered): a Young diagram has
  // every row's first cell at the same horizontal position — the
  // shorter rows just have fewer cells to the right. `{c}` would
  // centre each row relative to the widest row, which doesn't look
  // like a Young diagram.
  return "\\begin{array}{l}" + arrRows.join(" \\\\ ") + "\\end{array}";
}

// Find the end of a `\yng(…)` or `\young(…)` call, including optional
// `{…}` content. Returns the index just past the entire macro call,
// or -1 if the open-paren has no matching close.
function findYoungCallEnd(src: string, startIdx: number): number {
  // src[startIdx..] begins with "\yng(" or "\young(".
  const openIdx = src.indexOf("(", startIdx);
  if (openIdx < 0) return -1;
  let depth = 1;
  let i = openIdx + 1;
  while (i < src.length && depth > 0) {
    const ch = src[i];
    if (ch === "(") depth++;
    else if (ch === ")") depth--;
    if (depth === 0) {
      // Check for optional `{…}` content block.
      const afterClose = i + 1;
      if (src[afterClose] === "{") {
        let bdepth = 1;
        let k = afterClose + 1;
        while (k < src.length && bdepth > 0) {
          if (src[k] === "{") bdepth++;
          else if (src[k] === "}") bdepth--;
          if (bdepth === 0) return k + 1;
          k++;
        }
        return -1; // unterminated `{`
      }
      return afterClose;
    }
    i++;
  }
  return -1; // unterminated `(`
}

/**
 * Walk the input, replacing `\yng(…)` / `\young(…)` with the equivalent
 * KaTeX-compatible `\begin{array}{c}…\end{array}`. If the macro is
 * outside any `$…$` math block, wrap the result in `$…$` so remark-math
 * actually parses it as math. If the macro is already inside a math
 * block (the existing common case), just substitute the expanded form.
 */
export function expandYoungDiagrams(src: string): string {
  let out = "";
  let i = 0;
  // Track whether we're inside a math block. We use a small stack-like
  // counter (depth): 0 = prose, 1 = inline math `$…$`, 2 = display
  // math `$$…$$`. We bump on `$`, decrement on `$`, and clamp at 0/2.
  // For our wrapping decision, depth>0 means "leave the macro alone,
  // it's already in math".
  let depth = 0;

  while (i < src.length) {
    const ch = src[i];

    if (ch === "$") {
      // Look ahead for `$$` (display) vs single `$` (inline).
      if (src[i + 1] === "$") {
        // Display math: toggle 0↔2.
        depth = depth === 0 ? 2 : depth === 2 ? 0 : depth;
        out += "$$";
        i += 2;
      } else {
        // Inline math: toggle 0↔1. If we're in display math (depth=2),
        // a single `$` doesn't end it — we need another `$` to do that,
        // so single `$` inside display math is left alone (depth stays 2).
        if (depth === 0) depth = 1;
        else if (depth === 1) depth = 0;
        // depth === 2: single `$` inside `$$…$$` is literal, depth unchanged.
        out += ch;
        i += 1;
      }
      continue;
    }

    // Look for \yng( or \young( and decide whether to wrap or just substitute.
    if (src.startsWith("\\yng(", i) || src.startsWith("\\young(", i)) {
      const isYng = src.startsWith("\\yng(", i);
      const callEnd = findYoungCallEnd(src, i);
      if (callEnd > 0) {
        const openIdx = src.indexOf("(", i);
        const closeIdx = src.indexOf(")", openIdx);
        const shapeText = src.slice(openIdx + 1, closeIdx);
        let content: string | undefined;
        let contentStart = closeIdx + 1;
        if (src[contentStart] === "{") {
          let bdepth = 1;
          let k = contentStart + 1;
          while (k < src.length && bdepth > 0) {
            if (src[k] === "{") bdepth++;
            else if (src[k] === "}") bdepth--;
            if (bdepth === 0) break;
            k++;
          }
          content = src.slice(contentStart + 1, k);
        }
        const rows = isYng
          ? parseShape(shapeText, "comma")
          : parseShape(shapeText, "space");
        const expanded = expandShape(rows, content);
        // Wrap in `$…$` only if we're outside math. Inside math, the
        // surrounding `$`/`$$` already supplies the math delimiters.
        if (depth === 0) {
          out += "$" + expanded + "$";
        } else {
          out += expanded;
        }
        i = callEnd;
        continue;
      }
    }

    out += ch;
    i++;
  }
  return out;
}