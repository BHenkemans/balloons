#import "@preview/zebra:0.1.0": qrcode

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

// --- PAGE SETUP ---
#let page-width = 80mm
#let page-margin-x = 5mm
#set page(width: page-width, height: auto, margin: (x: page-margin-x, y: 8mm))
#set text(font: ("Arial", "Liberation Sans", "Helvetica", "DejaVu Sans"), size: 10pt)

// --- HELPER FUNCTIONS ---
#let h-line() = line(length: 100%, stroke: 0.7pt + gray)

#let draw-balloon(lbl, status: "unsolved", is-current: false) = {
  let c-bg = if status == "delivered" {
    rgb("333333")
  } else if status == "in-delivery" {
    rgb("999999")
  } else {
    none
  }

  let c-fg = if status == "unsolved" { black } else { white }

  box(width: 16pt, height: 34pt)[
    #align(center)[
      #circle(radius: 8pt, fill: c-bg, stroke: 0.5pt + black)[
        #set align(center + horizon)
        #text(fill: c-fg, size: 7pt, weight: "bold")[#lbl]
      ]
      #v(-3pt)
      #if is-current [
        #v(2pt)
        #polygon(fill: black, (0pt, 0pt), (-3pt, 4pt), (3pt, 4pt))
      ]
    ]
  ]
}

// --- TICKET LAYOUT ---
#align(center)[
  #grid(
    columns: (1fr, 1fr),
    align(left)[#text(size: 7pt, fill: gray)[#date-time]], align(right)[#text(size: 7pt, fill: gray)[id: #ticket-id]],
  )

  #h-line()

  #if is-first-solve [
    #block(
      width: 100%,
      inset: (x: 3mm, y: 1.5mm),
      fill: problem-color,
      radius: 2pt,
      text(size: 13pt, weight: "bold", fill: white, tracking: 1pt)[★ FIRST SOLVE ★],
    )
    #v(1mm)
  ]

  #block(
    text(
      size: 32pt,
      weight: "bold",
      fill: problem-color,
      top-edge: "cap-height",
      bottom-edge: "baseline",
    )[#problem-letter],
  )

  #h-line()

  // Team Info
  #text(size: 9pt, weight: "bold", fill: rgb("333333"))[#uni-name]
  #v(2mm)
  #text(size: 7pt)[Location #team-number]

  #v(2mm)
  #image("map-image.png")

  #h-line()

  #let bw = 16pt          // balloon box width (matches draw-balloon)
  #let gap = 4pt          // horizontal gap between balloons
  #let avail = (page-width - 2 * page-margin-x).pt()
  #let per-row = calc.max(1, calc.floor((avail + gap.pt()) / (bw.pt() + gap.pt())))

  #let solved = (
    balloons-input.split(",").map(b => b.trim()).filter(b => b != "" and (b in delivered or b in in-delivery))
  )

  #let rows = (
    range(0, solved.len(), step: per-row).map(i => solved.slice(i, calc.min(i + per-row, solved.len())))
  )

  #stack(
    dir: ttb,
    spacing: 2pt,
    ..rows.map(row => align(center, box(stack(
      dir: ltr,
      spacing: gap,
      ..row.map(b => {
        let status = if b in delivered { "delivered" } else { "in-delivery" }
        draw-balloon(b, status: status, is-current: b == problem-letter)
      }),
    )))),
  )

  #h-line()

  #qrcode(
    "Trek een bak! LOLOLOLOL",
    width: 60mm,
    quiet-zone: true,
    options: (ec-level: "h"),
  )
]
