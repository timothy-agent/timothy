// Document template: optional cover, optional TOC, one chapter per
// input document. Running header shows the cover/chapter title,
// footer shows page numbers. Code fences are highlighted natively by
// Typst raw blocks. Mermaid fences render via merman; each fence is
// probed with mermaid-result() first so an invalid diagram falls back
// to the original raw block instead of failing the compile.
#import "@preview/cmarker:0.1.10"
#import "@preview/merman:0.1.0"

#let render-doc(docs, cover-title: none, toc: false) = {
  set document(title: if cover-title != none and cover-title != "" {
    cover-title
  } else {
    docs.at(0).title
  })
  set page(paper: "a4", margin: (x: 2.5cm, y: 2.5cm))
  set text(font: "Libertinus Serif", size: 11pt)
  set heading(numbering: none)
  set raw(theme: none)

  let chapter-state = state("chapter-title", "")

  show raw.where(lang: "mermaid"): it => {
    let result = merman.mermaid-result(it.text)
    if result.ok {
      // Natural size, shrunk only if wider than the page — a small
      // diagram stays small instead of stretching to fill the width.
      block(breakable: false, layout(size => {
        let img = merman.mermaid(it.text)
        let w = calc.min(measure(img).width, size.width)
        box(width: w, img)
      }))
    } else {
      it
    }
  }

  show raw.where(block: true): it => block(
    breakable: false,
    fill: luma(245),
    inset: 8pt,
    radius: 2pt,
    width: 100%,
    it,
  )

  if cover-title != none and cover-title != "" {
    page(header: none, footer: none)[
      #align(center + horizon)[
        #text(size: 24pt, weight: "bold")[#cover-title]
        #v(1em)
        #text(size: 12pt)[#datetime.today().display("[month repr:long] [day], [year]")]
      ]
    ]
  }

  set page(
    header: context align(right)[#chapter-state.get()],
    footer: context align(center)[#counter(page).display()],
  )

  if toc {
    chapter-state.update("Table of Contents")
    outline(title: "Table of Contents", indent: auto)
    pagebreak()
  }

  // Injected chapter headings only make sense when there are chapters:
  // several documents, or a TOC to anchor. A lone document keeps its
  // own headings at their original levels (no duplicated title).
  let chapters = docs.len() > 1 or toc
  for doc in docs {
    chapter-state.update(doc.title)
    pagebreak(weak: true)
    if chapters {
      heading(level: 1, doc.title)
      cmarker.render(doc.content, raw-typst: false, h1-level: 2)
    } else {
      cmarker.render(doc.content, raw-typst: false)
    }
  }
}
