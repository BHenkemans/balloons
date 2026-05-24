#import "@preview/zebra:0.1.0": qrcode

// --- Inputs ---
// `theme` is "color" (default — sized for a regular printer via IPP) or
// "thermal" (sized + restyled for a black-and-white ESC/POS receipt printer).
// `page_width_mm` is only meaningful for the thermal theme; the color theme
// uses a fixed 80mm regardless.
#let theme-name = sys.inputs.at("theme", default: "color")
#let date-time = sys.inputs.at("datetime", default: "14-02-2026 10:00")
#let ticket-id = sys.inputs.at("ticket_id", default: "41")
#let problem-letter = sys.inputs.at("problem", default: "C")
#let problem-color = rgb(sys.inputs.at("color", default: "#cc2222"))
#let uni-name = sys.inputs.at("team_name", default: "Eindhoven University of Technology")
#let team-number = sys.inputs.at("team_id", default: "41")
#let balloons-input = sys.inputs.at("balloons", default: "A,B,C,D,E,F,G,H,I,J,K,L")
#let delivered = sys.inputs.at("delivered", default: "A,B").split(",").map(s => s.trim()).filter(s => s != "")
#let in-delivery = sys.inputs.at("in_delivery", default: "C").split(",").map(s => s.trim()).filter(s => s != "")
#let is-first-solve = sys.inputs.at("first_solve", default: "false") == "true"
#let scan-url = sys.inputs.at("scan_url", default: "")
#let page-width-mm = float(sys.inputs.at("page_width_mm", default: "80"))

// --- Theme palette ---
#let is-thermal = theme-name == "thermal"
#let palette = if is-thermal { (
  page-width: page-width-mm * 1mm,
  page-margin-x: 2mm,
  page-margin-y: 6mm,
  text-size: 11pt,
  text-weight: "bold",
  text-fill: black,
  rule-stroke: 1.2pt + black,
  banner-fill: black,
  banner-text-size: 16pt,
  banner-tracking: 2pt,
  banner-pad-y: 2mm,
  letter-size: 110pt,
  letter-fill: black,
  team-name-size: 13pt,
  team-name-fill: black,
  team-location-size: 11pt,
  team-location-weight: "regular",
  glyph-box-w: 20pt,
  glyph-box-h: 38pt,
  glyph-radius: 10pt,
  glyph-label-size: 9pt,
  glyph-delivered-fill: black,
  glyph-delivered-text: white,
  glyph-delivered-stroke: 0.8pt + black,
  glyph-in-delivery-fill: white,
  glyph-in-delivery-text: black,
  glyph-in-delivery-stroke: 1.5pt + black,
  chevron-half: 4pt,
  chevron-drop: 5pt,
  qr-width: 50mm,
  qr-caption-size: 8pt,
  qr-caption-fill: black,
  qr-caption-weight: "regular",
  header-fill: black,
  tight-spacing: true,
) } else { (
  page-width: 80mm,
  page-margin-x: 5mm,
  page-margin-y: 8mm,
  text-size: 10pt,
  text-weight: "regular",
  text-fill: black,
  rule-stroke: 0.7pt + gray,
  banner-fill: problem-color,
  banner-text-size: 13pt,
  banner-tracking: 1pt,
  banner-pad-y: 1.5mm,
  letter-size: 32pt,
  letter-fill: problem-color,
  team-name-size: 9pt,
  team-name-fill: rgb("333333"),
  team-location-size: 7pt,
  team-location-weight: "regular",
  glyph-box-w: 16pt,
  glyph-box-h: 34pt,
  glyph-radius: 8pt,
  glyph-label-size: 7pt,
  glyph-delivered-fill: rgb("333333"),
  glyph-delivered-text: white,
  glyph-delivered-stroke: 0.5pt + black,
  glyph-in-delivery-fill: rgb("999999"),
  glyph-in-delivery-text: white,
  glyph-in-delivery-stroke: 0.5pt + black,
  chevron-half: 3pt,
  chevron-drop: 4pt,
  qr-width: 60mm,
  qr-caption-size: 8pt,
  qr-caption-fill: gray,
  qr-caption-weight: "regular",
  header-fill: gray,
  tight-spacing: false,
) }

// --- Page setup ---
#set page(
  width: palette.page-width,
  height: auto,
  margin: (x: palette.page-margin-x, y: palette.page-margin-y),
)
#set text(
  font: ("Arial", "Liberation Sans", "Helvetica", "DejaVu Sans"),
  size: palette.text-size,
  weight: palette.text-weight,
  fill: palette.text-fill,
)

// Thermal output is unforgiving — Typst's default paragraph and block
// spacing stacks up to several centimetres of invisible gaps over a long
// receipt. Zero everything out for thermal and use explicit `#v(...)` where
// we want breathing room. The color theme keeps Typst defaults. Done via a
// document-level show rule because `set` inside a top-level `#if` block is
// scoped to that block and would not affect the document body.
#show: body => if palette.tight-spacing {
  set par(spacing: 0pt, leading: 0.5em)
  set block(above: 0pt, below: 0pt)
  body
} else { body }

// --- Helpers ---
#let h-line() = line(length: 100%, stroke: palette.rule-stroke)

#let draw-balloon(lbl, status: "unsolved", is-current: false) = {
  let fill-color = if status == "delivered" {
    palette.glyph-delivered-fill
  } else if status == "in-delivery" {
    palette.glyph-in-delivery-fill
  } else {
    none
  }
  let text-color = if status == "delivered" {
    palette.glyph-delivered-text
  } else if status == "in-delivery" {
    palette.glyph-in-delivery-text
  } else {
    black
  }
  let border = if status == "delivered" {
    palette.glyph-delivered-stroke
  } else if status == "in-delivery" {
    palette.glyph-in-delivery-stroke
  } else {
    0.5pt + black
  }

  box(width: palette.glyph-box-w, height: palette.glyph-box-h)[
    #align(center)[
      #circle(radius: palette.glyph-radius, fill: fill-color, stroke: border)[
        #set align(center + horizon)
        #text(fill: text-color, size: palette.glyph-label-size, weight: "bold")[#lbl]
      ]
      #v(-3pt)
      #if is-current [
        #v(2pt)
        #polygon(
          fill: black,
          (0pt, 0pt),
          (-palette.chevron-half, palette.chevron-drop),
          (palette.chevron-half, palette.chevron-drop),
        )
      ]
    ]
  ]
}

#let bw = palette.glyph-box-w
#let gap = 4pt
#let avail = (palette.page-width - 2 * palette.page-margin-x).pt()
#let per-row = calc.max(1, calc.floor((avail + gap.pt()) / (bw.pt() + gap.pt())))

#let solved = (
  balloons-input.split(",").map(b => b.trim()).filter(b => b != "" and (b in delivered or b in in-delivery))
)

#let balloon-rows = (
  range(0, solved.len(), step: per-row).map(i => solved.slice(i, calc.min(i + per-row, solved.len())))
)

// --- Layout ---
#align(center)[
  #grid(
    columns: (1fr, 1fr),
    align(left)[#text(size: 8pt, weight: "regular", fill: palette.header-fill)[#date-time]],
    align(right)[#text(size: 8pt, weight: "regular", fill: palette.header-fill)[id: #ticket-id]],
  )

  #v(1mm)
  #h-line()
  #v(2mm)

  #if is-first-solve [
    #block(
      width: 100%,
      inset: (x: 3mm, y: palette.banner-pad-y),
      fill: palette.banner-fill,
      radius: 2pt,
      text(
        size: palette.banner-text-size,
        weight: "bold",
        fill: white,
        tracking: palette.banner-tracking,
      )[★ FIRST SOLVE ★],
    )
    #v(1mm)
  ]

  #text(
    size: palette.letter-size,
    weight: "bold",
    fill: palette.letter-fill,
    top-edge: "cap-height",
    bottom-edge: "baseline",
  )[#problem-letter]

  #v(2mm)
  #h-line()
  #v(2mm)

  #text(size: palette.team-name-size, weight: "bold", fill: palette.team-name-fill)[#uni-name]
  #v(2mm)
  #text(size: palette.team-location-size, weight: palette.team-location-weight)[Location #team-number]

  #v(2mm)
  #image("map-image.png")

  #v(2mm)
  #h-line()
  #v(2mm)

  #stack(
    dir: ttb,
    spacing: 3pt,
    ..balloon-rows.map(row => align(center, box(stack(
      dir: ltr,
      spacing: gap,
      ..row.map(b => {
        let status = if b in delivered { "delivered" } else { "in-delivery" }
        draw-balloon(b, status: status, is-current: b == problem-letter)
      }),
    )))),
  )

  #v(2mm)
  #h-line()

  #if scan-url != "" [
    #v(2mm)
    #qrcode(
      scan-url,
      width: palette.qr-width,
      quiet-zone: true,
      options: (ec-level: "h"),
    )
    #v(1mm)
    #text(
      size: palette.qr-caption-size,
      weight: palette.qr-caption-weight,
      fill: palette.qr-caption-fill,
    )[Scan to mark delivered]
  ]
]
