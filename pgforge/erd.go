package main

// Auto-generated schema diagram (ERD): tables laid out as cards in an SVG,
// foreign keys drawn as curved edges. Layout is a simple masonry - boxes go
// into the currently shortest column - which keeps related tables readable
// without a heavyweight graph engine.

import (
	"fmt"
	"net/http"
)

type erdRow struct {
	Name, Type string
	PK         bool
}

type erdBox struct {
	Name       string
	X, Y, W, H int
	HeadH      int
	Rows       []erdRow
	More       int // columns not shown
}

type erdEdge struct {
	Path  string
	Title string // src.col -> dst.col, for the hover tooltip
}

const (
	erdBoxW    = 230
	erdRowH    = 19
	erdHeadH   = 30
	erdGapX    = 80
	erdGapY    = 26
	erdMaxRows = 14
)

func (a *app) erdPage(w http.ResponseWriter, r *http.Request) {
	slug := r.PathValue("slug")
	if !a.projectExists(slug) {
		http.NotFound(w, r)
		return
	}
	db, err := a.dbFor(slug)
	if err != nil {
		http.Error(w, err.Error(), 500)
		return
	}
	sc := a.edSchema(db, r.URL.Query().Get("sc"))

	var names []string
	for _, rel := range a.listRelations(db, sc) {
		if rel.Kind == "table" {
			names = append(names, rel.Name)
		}
	}

	type fkRef struct{ srcCol, dstTable, dstCol string }
	boxes := make([]*erdBox, 0, len(names))
	byName := map[string]*erdBox{}
	fks := map[string][]fkRef{}
	rowIdx := map[string]map[string]int{} // table -> shown column -> row index

	for _, t := range names {
		cols := a.tableCols(db, sc, t)
		pk := map[string]bool{}
		for _, k := range a.tablePK(db, sc, t) {
			pk[k] = true
		}
		b := &erdBox{Name: t, W: erdBoxW, HeadH: erdHeadH}
		rowIdx[t] = map[string]int{}
		for i, c := range cols {
			if i >= erdMaxRows {
				b.More = len(cols) - erdMaxRows
				break
			}
			ty := c.Type
			if len(ty) > 16 {
				ty = ty[:15] + "…"
			}
			b.Rows = append(b.Rows, erdRow{Name: c.Name, Type: ty, PK: pk[c.Name]})
			rowIdx[t][c.Name] = i
			if c.FKTable != "" && c.FKSchema == sc {
				fks[t] = append(fks[t], fkRef{c.Name, c.FKTable, c.FKCol})
			}
		}
		b.H = erdHeadH + len(b.Rows)*erdRowH + 10
		if b.More > 0 {
			b.H += erdRowH
		}
		boxes = append(boxes, b)
		byName[t] = b
	}

	// masonry layout: each new box lands in the currently shortest column
	nCols := 1
	for nCols*nCols < len(boxes) {
		nCols++
	}
	if nCols > 5 {
		nCols = 5
	}
	colY := make([]int, nCols)
	for _, b := range boxes {
		best := 0
		for i := 1; i < nCols; i++ {
			if colY[i] < colY[best] {
				best = i
			}
		}
		b.X = 30 + best*(erdBoxW+erdGapX)
		b.Y = 30 + colY[best]
		colY[best] += b.H + erdGapY
	}

	var edges []erdEdge
	for src, refs := range fks {
		sb := byName[src]
		for _, ref := range refs {
			db2, ok := byName[ref.dstTable]
			if !ok {
				continue
			}
			ri, shown := rowIdx[src][ref.srcCol]
			y1 := sb.Y + erdHeadH/2
			if shown {
				y1 = sb.Y + erdHeadH + ri*erdRowH + erdRowH/2
			}
			y2 := db2.Y + erdHeadH/2
			var x1, x2, dx int
			if db2.X >= sb.X+erdBoxW || db2.X == sb.X {
				x1, x2, dx = sb.X+erdBoxW, db2.X, 45
				if db2.X == sb.X { // same column: bow out to the right
					x2 = db2.X + erdBoxW
					dx = 60
					edges = append(edges, erdEdge{
						Path: fmt.Sprintf("M %d %d C %d %d, %d %d, %d %d",
							x1, y1, x1+dx, y1, x2+dx, y2, x2, y2),
						Title: fmt.Sprintf("%s.%s -> %s.%s", src, ref.srcCol, ref.dstTable, ref.dstCol),
					})
					continue
				}
			} else {
				x1, x2, dx = sb.X, db2.X+erdBoxW, -45
			}
			edges = append(edges, erdEdge{
				Path: fmt.Sprintf("M %d %d C %d %d, %d %d, %d %d",
					x1, y1, x1+dx, y1, x2-dx, y2, x2, y2),
				Title: fmt.Sprintf("%s.%s -> %s.%s", src, ref.srcCol, ref.dstTable, ref.dstCol),
			})
		}
	}

	width, height := 60, 60
	for _, b := range boxes {
		if b.X+b.W+60 > width {
			width = b.X + b.W + 60
		}
		if b.Y+b.H+60 > height {
			height = b.Y + b.H + 60
		}
	}

	content := renderContent(erdBody, map[string]any{
		"Slug": slug, "Schema": sc, "Schemas": a.listSchemas(db),
		"Boxes": boxes, "Edges": edges, "Width": width, "Height": height,
		"Empty": len(boxes) == 0,
	})
	a.renderShell(w, r, shellData{Title: slug + " · Diagram", Nav: "tables", Slug: slug,
		Crumbs: []crumb{{Label: "Projects", Href: "/"}, {Label: slug, Href: "/p/" + slug},
			{Label: "Table Editor", Href: "/p/" + slug + "/tables"}, {Label: "Diagram"}}}, content)
}
