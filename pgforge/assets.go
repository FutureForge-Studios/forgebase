package main

import (
	"bytes"
	"fmt"
	"html/template"
	"net/http"
	"strings"
)

// Design system: "Light Editorial" (cream) - warm canvas, deep teal-green
// primary, Fraunces serif display, Inter body, JetBrains Mono micro-labels,
// floating glass sidebar, aurora wash, and the animated "Grid Assembly" brand
// mark. Ported from the profitzon-commands editorial skin (circle.health feel).

const cssDesign = `
:root{
  --bg:40 33% 96%; --fg:30 12% 12%;
  --card:40 40% 99.5%; --card-fg:30 12% 12%;
  --muted:40 24% 91%; --muted-fg:30 8% 40%;
  --primary:162 55% 30%; --primary-fg:40 40% 98%;
  --accent:40 30% 88%; --accent-fg:30 12% 16%;
  --border:38 20% 84%; --ring:162 55% 30%;
  --destructive:0 70% 45%; --warn:35 80% 42%;
  --radius:0.75rem;
  --shadow-soft:0 1px 2px rgb(0 0 0 / .04), 0 8px 24px -12px rgb(0 0 0 / .18);
  --sans:'Inter',system-ui,-apple-system,Segoe UI,Roboto,sans-serif;
  --serif:'Fraunces','Iowan Old Style',Georgia,serif;
  --mono:'JetBrains Mono',ui-monospace,'Cascadia Code',Consolas,monospace;
}
*{box-sizing:border-box}
html,body{margin:0;padding:0}
body{
  font-family:var(--sans);
  background:hsl(var(--bg)); color:hsl(var(--fg));
  -webkit-font-smoothing:antialiased; font-size:14px; line-height:1.5;
}
h1,h2,h3{font-family:var(--serif); letter-spacing:-.01em; font-weight:500; margin:0}
a{color:inherit; text-decoration:none}
code,pre,.mono{font-family:var(--mono)}
/* default size for every inline icon so a bare <svg> never balloons; the brand
   mark and charts set their own size and override this via higher specificity
   or inline styles. */
svg{width:18px; height:18px; flex-shrink:0}
.muted{color:hsl(var(--muted-fg))}
.label{font-family:var(--mono); font-size:10px; text-transform:uppercase; letter-spacing:.14em; color:hsl(var(--muted-fg))}

/* aurora canvas */
.aurora{position:fixed; inset:0; z-index:-1;
  background:
    radial-gradient(56rem 36rem at 6% -10%, hsl(162 45% 55% / .14), transparent 62%),
    radial-gradient(40rem 28rem at 104% -8%, hsl(36 60% 60% / .14), transparent 55%),
    hsl(var(--bg));
}

/* layout */
.wrap{display:flex; min-height:100vh}
.sidebar{
  position:sticky; top:0; align-self:flex-start; flex-shrink:0;
  width:250px; height:100vh; padding:.6rem;
  display:flex; flex-direction:column;
}
.sidebar-inner{
  background:color-mix(in srgb, hsl(var(--card)) 72%, transparent);
  backdrop-filter:blur(12px);
  border:1px solid hsl(var(--border)); border-radius:1.1rem;
  height:100%; display:flex; flex-direction:column; overflow:hidden;
}
.brandrow{display:flex; align-items:center; gap:.65rem; padding:1rem 1rem .8rem}
.brandtext .t{font-size:14px; font-weight:600; line-height:1.1}
.brandtext .s{font-family:var(--mono); font-size:9px; text-transform:uppercase; letter-spacing:.2em; color:hsl(var(--muted-fg))}
nav.side{display:flex; flex-direction:column; gap:2px; padding:.4rem .5rem; overflow-y:auto}
nav.side .sep{margin:.5rem .6rem .3rem; }
.navi{display:flex; align-items:center; gap:.7rem; padding:.5rem .7rem; border-radius:.6rem;
  color:hsl(var(--muted-fg)); font-weight:500; font-size:13px; transition:all .15s}
.navi:hover{background:hsl(var(--accent)); color:hsl(var(--accent-fg))}
.navi.active{background:hsl(var(--primary) / .12); color:hsl(var(--primary)); font-weight:600}
.navi svg{width:17px; height:17px; flex-shrink:0}
.sidefoot{margin-top:auto; padding:.6rem .8rem; border-top:1px solid hsl(var(--border))}

.main{flex:1; min-width:0; display:flex; flex-direction:column}
.topbar{
  position:sticky; top:0; z-index:20; height:56px;
  display:flex; align-items:center; gap:.8rem; padding:0 1.4rem;
  background:color-mix(in srgb, hsl(var(--card)) 62%, transparent);
  backdrop-filter:blur(12px); border-bottom:1px solid hsl(var(--border) / .6);
}
.crumb{display:flex; align-items:center; gap:.5rem; font-size:13px; color:hsl(var(--muted-fg))}
.crumb b{color:hsl(var(--fg)); font-weight:600}
.crumb .sl{opacity:.5}
.spacer{flex:1}
.content{padding:1.6rem 1.8rem; max-width:1180px; width:100%; margin:0 auto}
.pagehead{margin-bottom:1.4rem}
.pagehead h1{font-size:26px}
.pagehead p{margin:.3rem 0 0; color:hsl(var(--muted-fg)); font-size:13.5px}

/* cards */
.card{background:hsl(var(--card)); border:1px solid hsl(var(--border));
  border-radius:1rem; box-shadow:var(--shadow-soft); padding:1.2rem}
.card h2{font-size:17px; margin-bottom:.2rem}
.grid{display:grid; gap:1rem}
.g2{grid-template-columns:repeat(auto-fill,minmax(340px,1fr))}
.g3{grid-template-columns:repeat(auto-fill,minmax(220px,1fr))}
.stat{padding:1rem 1.15rem}
.stat .k{font-family:var(--mono); font-size:10px; text-transform:uppercase; letter-spacing:.12em; color:hsl(var(--muted-fg))}
.stat .v{font-family:var(--serif); font-size:26px; margin-top:.35rem}

/* buttons */
.btn{display:inline-flex; align-items:center; gap:.45rem; cursor:pointer;
  font-family:var(--sans); font-size:13px; font-weight:600; line-height:1;
  padding:.6rem 1rem; border-radius:999px; border:1px solid transparent; transition:all .15s}
.btn svg{width:15px; height:15px}
.btn-primary{background:hsl(var(--primary)); color:hsl(var(--primary-fg))}
.btn-primary:hover{filter:brightness(1.08)}
.btn-ghost{background:transparent; border-color:hsl(var(--border)); color:hsl(var(--fg))}
.btn-ghost:hover{background:hsl(var(--accent))}
.btn-danger{background:transparent; border-color:hsl(var(--border)); color:hsl(var(--destructive))}
.btn-danger:hover{background:hsl(var(--destructive) / .1); border-color:hsl(var(--destructive) / .4)}
.btn-sm{padding:.4rem .75rem; font-size:12px}

/* inputs */
.input,input[type=text],input[type=password],input[type=email],input[type=url],input[type=number],input[type=search],textarea,select{
  width:100%; font-family:var(--sans); font-size:13.5px; color:hsl(var(--fg));
  background:hsl(var(--bg)); border:1px solid hsl(var(--border));
  border-radius:.6rem; padding:.6rem .8rem; outline:none; transition:border .15s}
.input:focus,input[type=text]:focus,input[type=password]:focus,input[type=email]:focus,input[type=url]:focus,input[type=number]:focus,input[type=search]:focus,textarea:focus,select:focus{border-color:hsl(var(--ring)); box-shadow:0 0 0 3px hsl(var(--ring) / .12)}
textarea{resize:vertical; font-family:var(--mono); font-size:13px; line-height:1.6}
label.fld{display:block; margin-bottom:.9rem}
label.fld .lt{display:block; font-size:12px; font-weight:600; margin-bottom:.35rem}
/* native controls, themed to match */
input[type=checkbox],input[type=radio]{accent-color:hsl(var(--primary)); width:16px; height:16px; cursor:pointer; vertical-align:-2px}
input[type=file]{font-size:12.5px; color:hsl(var(--muted-fg))}
input[type=file]::file-selector-button{font-family:var(--sans); font-size:12px; font-weight:600;
  border:1px solid hsl(var(--border)); background:hsl(var(--card)); color:hsl(var(--fg));
  padding:.45rem .9rem; border-radius:999px; margin-right:.7rem; cursor:pointer; transition:background .15s}
input[type=file]::file-selector-button:hover{background:hsl(var(--accent))}

/* badges */
.badge{display:inline-flex; align-items:center; gap:.3rem; font-family:var(--mono);
  font-size:10px; text-transform:uppercase; letter-spacing:.06em; font-weight:600;
  padding:.2rem .55rem; border-radius:999px; border:1px solid transparent}
.badge.active{background:hsl(var(--primary) / .12); color:hsl(var(--primary)); border-color:hsl(var(--primary) / .25)}
.badge.paused,.badge.suspended,.badge.cloning{background:hsl(var(--warn) / .14); color:hsl(var(--warn)); border-color:hsl(var(--warn) / .3)}

/* connection string rows */
.cs{display:flex; align-items:center; gap:.6rem; background:hsl(var(--bg));
  border:1px solid hsl(var(--border)); border-radius:.6rem; padding:.5rem .7rem; margin-top:.5rem}
.cs .tag{font-family:var(--mono); font-size:9px; text-transform:uppercase; letter-spacing:.1em;
  color:hsl(var(--muted-fg)); flex-shrink:0; width:66px}
.cs code{flex:1; overflow-x:auto; white-space:nowrap; font-size:11.5px; color:hsl(var(--primary)); scrollbar-width:none}
.cs code::-webkit-scrollbar{display:none}
.copy{cursor:pointer; border:none; background:transparent; color:hsl(var(--muted-fg)); padding:.2rem; border-radius:.4rem; display:grid; place-items:center}
.copy:hover{background:hsl(var(--accent)); color:hsl(var(--fg))}
.copy svg{width:14px; height:14px}

/* tables */
.tblwrap{overflow-x:auto; border:1px solid hsl(var(--border)); border-radius:.8rem; background:hsl(var(--card))}
table.data{border-collapse:collapse; width:100%; font-size:12.5px}
table.data th{text-align:left; font-family:var(--mono); font-size:10px; text-transform:uppercase;
  letter-spacing:.06em; color:hsl(var(--muted-fg)); padding:.6rem .8rem; border-bottom:1px solid hsl(var(--border)); white-space:nowrap; background:hsl(var(--muted) / .5)}
table.data td{padding:.55rem .8rem; border-bottom:1px solid hsl(var(--border) / .6); white-space:nowrap; max-width:340px; overflow:hidden; text-overflow:ellipsis}
table.data tr:last-child td{border-bottom:none}
table.data tr:hover td{background:hsl(var(--accent) / .4)}
td .null{color:hsl(var(--muted-fg)); font-style:italic; opacity:.7}

/* SQL editor schema browser */
.sqlschema .sqltbl{border-bottom:1px solid hsl(var(--border) / .5)}
.sqlschema summary{list-style:none;cursor:pointer;display:flex;align-items:center;gap:.4rem;padding:.4rem .4rem;border-radius:.4rem;font-size:12.5px}
.sqlschema summary::-webkit-details-marker{display:none}
.sqlschema summary:hover{background:hsl(var(--accent))}
.sqlschema .tw{width:6px;height:6px;border:1.5px solid hsl(var(--muted-fg));border-radius:2px;flex-shrink:0;transition:transform .15s}
.sqlschema details[open] .tw{transform:rotate(45deg);border-color:hsl(var(--primary))}
.sqlschema .tn{flex:1;font-weight:500;overflow:hidden;text-overflow:ellipsis;white-space:nowrap}
.sqlschema .tn:hover{color:hsl(var(--primary))}
.sqlschema .tc{font-family:var(--mono);font-size:9px;color:hsl(var(--muted-fg));background:hsl(var(--muted));padding:.05rem .3rem;border-radius:99px}
.sqlschema .cols{padding:.1rem 0 .3rem .9rem}
.sqlschema .col{display:flex;justify-content:space-between;gap:.5rem;font-family:var(--mono);font-size:10.5px;color:hsl(var(--muted-fg));cursor:pointer;padding:.15rem .4rem;border-radius:.3rem}
.sqlschema .col:hover{background:hsl(var(--accent));color:hsl(var(--fg))}
.sqlschema .col .ty{opacity:.6}

/* flash */
.flash{background:hsl(var(--primary) / .1); border:1px solid hsl(var(--primary) / .3);
  color:hsl(var(--primary)); padding:.7rem 1rem; border-radius:.7rem; font-size:13px; margin-bottom:1.2rem}
.flash.err{background:hsl(var(--destructive) / .1); border-color:hsl(var(--destructive) / .3); color:hsl(var(--destructive))}

/* dropdown (details-based, no JS) */
.dd{position:relative}
.dd summary{list-style:none; cursor:pointer; display:flex; align-items:center; gap:.5rem}
.dd summary::-webkit-details-marker{display:none}
.dd[open] .ddmenu{display:block}
.ddmenu{display:none; position:absolute; right:0; top:calc(100% + .5rem); min-width:210px; z-index:40;
  background:hsl(var(--card)); border:1px solid hsl(var(--border)); border-radius:.8rem;
  box-shadow:var(--shadow-soft); padding:.4rem; overflow:hidden}
.ddmenu a,.ddmenu button{display:flex; align-items:center; gap:.6rem; width:100%; text-align:left;
  padding:.5rem .6rem; border-radius:.5rem; font-size:13px; color:hsl(var(--fg)); background:transparent; border:none; cursor:pointer}
.ddmenu a:hover,.ddmenu button:hover{background:hsl(var(--accent))}
.ddmenu .dsep{height:1px; background:hsl(var(--border)); margin:.35rem 0}
.ddmenu svg{width:15px;height:15px}
.avatar{width:30px; height:30px; border-radius:999px; background:hsl(var(--primary) / .14); color:hsl(var(--primary));
  display:grid; place-items:center; font-weight:700; font-size:12px; font-family:var(--mono)}

/* brand mark animation (Grid Assembly) */
.brand{display:inline-grid; place-items:center; width:36px; height:36px; border-radius:.7rem;
  background:hsl(var(--primary)); color:hsl(var(--primary-fg)); overflow:hidden; box-shadow:0 6px 16px -6px hsl(var(--primary) / .5)}
.brand svg{width:72%; height:72%}
.brand-cell{animation:brand-pop 4.5s ease infinite}
@keyframes brand-pop{0%,8%{transform:scale(0);opacity:0}18%,78%{transform:scale(1);opacity:1}90%,100%{transform:scale(0);opacity:0}}
*{scrollbar-width:thin; scrollbar-color:hsl(var(--muted-fg) / .3) transparent}
*::-webkit-scrollbar{width:12px;height:12px}
*::-webkit-scrollbar-thumb{background-color:hsl(var(--muted-fg) / .35); border-radius:999px; border:3px solid transparent; background-clip:padding-box}

/* auth pages */
.authwrap{min-height:100vh; display:grid; place-items:center; padding:2rem 1rem}
.authcard{width:100%; max-width:400px; background:hsl(var(--card)); border:1px solid hsl(var(--border));
  border-radius:1.4rem; box-shadow:var(--shadow-soft); padding:2.4rem 2rem}
.authcard .brand{width:60px; height:60px; border-radius:1rem; margin:0 auto}
.authcard h1{text-align:center; font-size:26px; margin-top:1.1rem}
.authcard .sub{text-align:center; color:hsl(var(--muted-fg)); font-size:13px; margin-top:.3rem}
.authcard form{margin-top:1.6rem}
.authcard .btn{width:100%; justify-content:center; margin-top:.4rem}
.authfoot{text-align:center; font-size:12.5px; color:hsl(var(--muted-fg)); margin-top:1.3rem}
.authfoot a{color:hsl(var(--primary)); font-weight:600}

/* hamburger lives in the topbar, shown only on narrow screens */
.navtoggle{display:none; align-items:center; justify-content:center;
  width:40px; height:40px; margin-left:-.35rem; border-radius:.6rem;
  color:hsl(var(--fg)); flex-shrink:0; cursor:pointer; background:transparent; border:0}
.navtoggle:hover{background:hsl(var(--accent))}
.navtoggle svg{width:22px; height:22px}
.navback{display:none}

/* ---- responsive: phones + small tablets ---- */
@media (max-width:820px){
  body{overflow-x:hidden}                 /* the page itself never scrolls sideways */
  .navtoggle{display:inline-flex}
  .sidebar{
    position:fixed; top:0; left:0; z-index:60;
    width:80vw; max-width:290px; height:100dvh;
    transform:translateX(-106%); transition:transform .25s ease;
  }
  .wrap.nav-open .sidebar{transform:translateX(0); box-shadow:0 0 40px hsl(0 0% 0% / .25)}
  .navback{display:block; position:fixed; inset:0; z-index:55;
    background:hsl(0 0% 0% / .38); opacity:0; pointer-events:none; transition:opacity .25s ease}
  .wrap.nav-open .navback{opacity:1; pointer-events:auto}
  .content{padding:1.1rem 1rem; max-width:100%}
  .topbar{padding:0 .9rem; gap:.5rem; height:54px}
  .pagehead h1{font-size:22px}
  .pagehead p{font-size:13px}
  .g2,.g3{grid-template-columns:1fr}       /* stat + content cards go single column */
  .grid{gap:.8rem}
  .card{padding:1rem; border-radius:.9rem}
  .tblwrap{overflow-x:auto; -webkit-overflow-scrolling:touch}   /* tables scroll, page doesn't */
  table.data td{max-width:none; white-space:nowrap}
  /* long monospace values (connection strings, keys) wrap instead of overflowing */
  .mono, code, pre{overflow-wrap:anywhere; word-break:break-word}
  .topbar .dd>summary{max-width:44vw; overflow:hidden; text-overflow:ellipsis; white-space:nowrap}
  .pagehead{margin-bottom:1rem}
  /* side-by-side panels (table editor, SQL editor, storage, functions) stack */
  .split{flex-direction:column}
  .split>*{width:100% !important; flex-shrink:1 !important}
  textarea{min-height:180px}
}
`

// brandBurst renders the "Grid Assembly" SVG: a symmetric 8-point burst of
// rounded cells that animate in a continuous wave. grid=true draws faint guides.
func brandBurst(grid bool) template.HTML {
	const gn = 7
	gcell := 24.0 / gn
	const inset = 0.42
	cells := [][2]int{
		{3, 3}, {3, 1}, {3, 2}, {3, 4}, {3, 5},
		{1, 3}, {2, 3}, {4, 3}, {5, 3},
		{1, 1}, {2, 2}, {5, 1}, {4, 2},
		{1, 5}, {2, 4}, {5, 5}, {4, 4},
	}
	var b strings.Builder
	b.WriteString(`<svg viewBox="0 0 24 24" fill="none">`)
	if grid {
		b.WriteString(`<g stroke="currentColor" stroke-opacity="0.16" stroke-width="0.25">`)
		for i := 0; i <= gn; i++ {
			p := float64(i) * gcell
			fmt.Fprintf(&b, `<line x1="%.3f" y1="0" x2="%.3f" y2="24"/>`, p, p)
			fmt.Fprintf(&b, `<line x1="0" y1="%.3f" x2="24" y2="%.3f"/>`, p, p)
		}
		b.WriteString(`</g>`)
	}
	b.WriteString(`<g fill="currentColor">`)
	s := gcell - inset*2
	for i, c := range cells {
		x := float64(c[0])*gcell + inset
		y := float64(c[1])*gcell + inset
		ox := float64(c[0])*gcell + gcell/2
		oy := float64(c[1])*gcell + gcell/2
		delay := float64(i) * 4.5 / float64(len(cells))
		fmt.Fprintf(&b, `<rect class="brand-cell" x="%.3f" y="%.3f" width="%.3f" height="%.3f" rx="0.7" style="transform-origin:%.3fpx %.3fpx;animation-delay:%.2fs"/>`,
			x, y, s, s, ox, oy, delay)
	}
	b.WriteString(`</g></svg>`)
	return template.HTML(b.String())
}

// faviconSVG renders a STATIC version of the Grid Assembly brand mark for the
// browser tab: white cells on a teal rounded square, matching the login logo.
// Modern browsers (Chrome/Edge/Firefox/Safari 16+) render SVG favicons directly.
func faviconSVG() string {
	const gn = 7
	gcell := 24.0 / gn
	const inset = 0.42
	cells := [][2]int{
		{3, 3}, {3, 1}, {3, 2}, {3, 4}, {3, 5},
		{1, 3}, {2, 3}, {4, 3}, {5, 3},
		{1, 1}, {2, 2}, {5, 1}, {4, 2},
		{1, 5}, {2, 4}, {5, 5}, {4, 4},
	}
	var b strings.Builder
	b.WriteString(`<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 24 24">`)
	b.WriteString(`<rect width="24" height="24" rx="5" fill="hsl(162,55%,30%)"/>`)
	b.WriteString(`<g fill="#ffffff">`)
	s := gcell - inset*2
	for _, c := range cells {
		x := float64(c[0])*gcell + inset
		y := float64(c[1])*gcell + inset
		fmt.Fprintf(&b, `<rect x="%.3f" y="%.3f" width="%.3f" height="%.3f" rx="0.7"/>`, x, y, s, s)
	}
	b.WriteString(`</g></svg>`)
	return b.String()
}

// faviconHandler serves the brand mark as an SVG favicon.
func faviconHandler(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Content-Type", "image/svg+xml")
	w.Header().Set("Cache-Control", "public, max-age=86400")
	fmt.Fprint(w, faviconSVG())
}

// icon returns an inline lucide-style SVG by name.
func icon(name string) template.HTML {
	paths := map[string]string{
		"grid":     `<rect x="3" y="3" width="7" height="7" rx="1"/><rect x="14" y="3" width="7" height="7" rx="1"/><rect x="3" y="14" width="7" height="7" rx="1"/><rect x="14" y="14" width="7" height="7" rx="1"/>`,
		"home":     `<path d="M3 9.5 12 3l9 6.5V20a1 1 0 0 1-1 1h-5v-6H9v6H4a1 1 0 0 1-1-1z"/>`,
		"table":    `<rect x="3" y="3" width="18" height="18" rx="2"/><path d="M3 9h18M3 15h18M9 3v18M15 3v18"/>`,
		"terminal": `<path d="m5 8 3 3-3 3M12 16h6"/><rect x="2" y="4" width="20" height="16" rx="2"/>`,
		"database": `<ellipse cx="12" cy="5" rx="8" ry="3"/><path d="M4 5v6c0 1.7 3.6 3 8 3s8-1.3 8-3V5M4 11v6c0 1.7 3.6 3 8 3s8-1.3 8-3v-6"/>`,
		"archive":  `<rect x="3" y="4" width="18" height="4" rx="1"/><path d="M5 8v11a1 1 0 0 0 1 1h12a1 1 0 0 0 1-1V8M10 12h4"/>`,
		"settings": `<path d="M4 21v-7M4 10V3M12 21v-9M12 8V3M20 21v-5M20 12V3M1 14h6M9 8h6M17 16h6"/>`,
		"user":     `<path d="M20 21a8 8 0 0 0-16 0"/><circle cx="12" cy="8" r="4"/>`,
		"plus":     `<path d="M12 5v14M5 12h14"/>`,
		"copy":     `<rect x="9" y="9" width="11" height="11" rx="2"/><path d="M5 15V5a2 2 0 0 1 2-2h8"/>`,
		"logout":   `<path d="M9 21H5a2 2 0 0 1-2-2V5a2 2 0 0 1 2-2h4M16 17l5-5-5-5M21 12H9"/>`,
		"play":     `<path d="M6 4v16l14-8z"/>`,
		"trash":    `<path d="M4 7h16M6 7v13a1 1 0 0 0 1 1h10a1 1 0 0 0 1-1V7M9 7V4h6v3M10 11v6M14 11v6"/>`,
		"pause":    `<rect x="6" y="4" width="4" height="16" rx="1"/><rect x="14" y="4" width="4" height="16" rx="1"/>`,
		"key":      `<circle cx="7.5" cy="15.5" r="4.5"/><path d="m10.7 12.3 8.3-8.3M17 6l2 2M14 9l2 2"/>`,
		"chart":    `<path d="M3 3v18h18M8 16v-5M13 16V8M18 16v-9"/>`,
		"branch":   `<line x1="6" y1="9" x2="6" y2="15"/><circle cx="6" cy="6" r="3"/><circle cx="6" cy="18" r="3"/><circle cx="18" cy="6" r="3"/><path d="M18 9a9 9 0 0 1-9 9"/>`,
		"list":     `<path d="M8 6h13M8 12h13M8 18h13M3 6h.01M3 12h.01M3 18h.01"/>`,
		"api":      `<path d="m8 16-4-4 4-4M16 8l4 4-4 4M13 4l-2 16"/>`,
		"shield":   `<path d="M12 3l7 3v5c0 4-3 7-7 8-4-1-7-4-7-8V6z"/>`,
		"folder":   `<path d="M3 7a2 2 0 0 1 2-2h4l2 2h8a2 2 0 0 1 2 2v8a2 2 0 0 1-2 2H5a2 2 0 0 1-2-2z"/>`,
		"upload":   `<path d="M21 15v4a2 2 0 0 1-2 2H5a2 2 0 0 1-2-2v-4M17 8l-5-5-5 5M12 3v12"/>`,
		"book":     `<path d="M4 19.5A2.5 2.5 0 0 1 6.5 17H20V3H6.5A2.5 2.5 0 0 0 4 5.5z"/><path d="M4 19.5A2.5 2.5 0 0 0 6.5 22H20"/>`,
		"bolt":     `<path d="M13 2 3 14h7l-1 8 10-12h-7z"/>`,
		"sparkle":  `<path d="M12 3l1.9 5.6L19.5 10l-5.6 1.4L12 17l-1.9-5.6L4.5 10l5.6-1.4z"/><path d="M19 15l.7 2 2 .7-2 .7-.7 2-.7-2-2-.7 2-.7z"/>`,
		"webhook":  `<path d="M18 16.98h-5.99c-1.1 0-1.95.94-2.48 1.9A4 4 0 0 1 2 17c.01-.7.2-1.4.57-2M6 17l3.13-5.78c.53-.97.1-2.18-.5-3.1a4 4 0 1 1 6.89-4.06M12 6l3.13 5.73C15.66 12.7 16.9 13 18 13a4 4 0 1 1-4 6"/>`,
		"restore":  `<path d="M3 12a9 9 0 1 0 3-6.7L3 8m0-5v5h5"/>`,
		"back":     `<path d="M19 12H5M12 19l-7-7 7-7"/>`,
		"chevron":  `<path d="m6 9 6 6 6-6"/>`,
		"menu":     `<path d="M4 6h16M4 12h16M4 18h16"/>`,
		"external": `<path d="M15 3h6v6M10 14 21 3M18 13v6a2 2 0 0 1-2 2H5a2 2 0 0 1-2-2V8a2 2 0 0 1 2-2h6"/>`,
	}
	p := paths[name]
	// default 18x18 so a bare icon never balloons; container CSS (.navi svg,
	// .btn svg, ...) overrides where a different size is wanted.
	return template.HTML(`<svg width="18" height="18" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="1.8" stroke-linecap="round" stroke-linejoin="round">` + p + `</svg>`)
}

// ----------------------------------------------------------------- shell

type navItem struct {
	Key, Label, Href, Icon string
}

func projectNav(slug string) []navItem {
	b := "/p/" + slug
	return []navItem{
		{"home", "Overview", b, "home"},
		{"tables", "Tables", b + "/tables", "table"},
		{"sql", "SQL Editor", b + "/sql", "terminal"},
		{"api", "Data API", b + "/api", "api"},
		{"storage", "Storage", b + "/storage", "folder"},
		{"authn", "Auth", b + "/auth", "shield"},
		{"realtime", "Realtime", b + "/realtime", "bolt"},
		{"webhooks", "Webhooks", b + "/webhooks", "webhook"},
		{"edge", "Edge Functions", b + "/functions", "terminal"},
		{"monitoring", "Monitoring", b + "/monitoring", "chart"},
		{"branches", "Branches", b + "/branches", "branch"},
		{"database", "Database", b + "/database", "database"},
		{"backups", "Backup & Restore", b + "/backups", "archive"},
		{"sync", "Sync / Clone", b + "/sync", "restore"},
		{"logs", "Logs", b + "/logs", "list"},
		{"docs", "Docs", b + "/docs", "book"},
		{"settings", "Settings", b + "/settings", "settings"},
	}
}

type crumb struct{ Label, Href string }

type shellData struct {
	Title    string
	Nav      string
	Slug     string
	Crumbs   []crumb
	Flash    string
	FlashErr bool
	User     string
	Initials string
	Domain   string
	Items    []navItem
	IsProj   bool
	Switch   []string
	Version  string
	Brand    template.HTML
	Content  template.HTML
}

var funcs = template.FuncMap{"icon": icon}

var shellTmpl = template.Must(template.New("shell").Funcs(funcs).Parse(`<!doctype html>
<html lang="en"><head><meta charset="utf-8"><meta name="viewport" content="width=device-width, initial-scale=1">
<title>{{.Title}} · ForgeBase</title>
<link rel="icon" type="image/svg+xml" href="/favicon.svg"><link rel="apple-touch-icon" href="/favicon.svg">
<link rel="preconnect" href="https://fonts.googleapis.com"><link rel="preconnect" href="https://fonts.gstatic.com" crossorigin>
<link href="https://fonts.googleapis.com/css2?family=Fraunces:opsz,wght@9..144,400;9..144,500;9..144,600&family=Inter:wght@400;500;600;700&family=JetBrains+Mono:wght@400;500&display=swap" rel="stylesheet">
<style>` + cssDesign + `</style></head>
<body>
<div class="aurora"></div>
<div class="wrap">
  <aside class="sidebar"><div class="sidebar-inner">
    <div class="brandrow"><span class="brand">{{.Brand}}</span>
      <div class="brandtext"><div class="t">ForgeBase</div><div class="s">by FutureForge Studios</div></div>
    </div>
    {{if .IsProj}}<a class="navi" href="/" style="margin:0 .5rem .3rem">{{icon "back"}} All projects</a>{{end}}
    <nav class="side">
      {{range .Items}}<a class="navi {{if eq .Key $.Nav}}active{{end}}" href="{{.Href}}">{{icon .Icon}} {{.Label}}</a>{{end}}
    </nav>
    <div class="sidefoot"><a class="navi" href="https://{{.Domain}}" style="padding:.3rem .2rem" target="_blank">{{icon "external"}} {{.Domain}}</a>
      <div style="font-size:10px;color:hsl(var(--muted-fg));padding:.4rem .2rem 0;line-height:1.5">© 2026 <a href="https://ffstudios.io" target="_blank" style="color:hsl(var(--muted-fg))">FutureForge Studios Private Limited</a> · <a href="/system" style="color:hsl(var(--muted-fg))">v{{.Version}}</a><br>Made with care in India ♥</div></div>
  </div></aside>
  <div class="navback" onclick="document.querySelector('.wrap').classList.remove('nav-open')"></div>
  <div class="main">
    <div class="topbar">
      <button class="navtoggle" type="button" aria-label="Open menu" onclick="document.querySelector('.wrap').classList.toggle('nav-open')">{{icon "menu"}}</button>
      {{if .IsProj}}<details class="dd"><summary style="gap:.4rem;padding:.35rem .6rem;border:1px solid hsl(var(--border));border-radius:.6rem;font-size:13px;font-weight:600">{{.Slug}} {{icon "chevron"}}</summary>
        <div class="ddmenu" style="left:0;right:auto;max-height:320px;overflow-y:auto">
          <div class="label" style="padding:.4rem .6rem">Switch project</div>
          {{range .Switch}}<a href="/p/{{.}}">{{icon "database"}} {{.}}</a>{{end}}
          <div class="dsep"></div><a href="/">{{icon "grid"}} All projects</a>
        </div></details>{{end}}
      <div class="crumb">{{range $i,$c := .Crumbs}}{{if $i}}<span class="sl">/</span>{{end}}{{if .Href}}<a href="{{.Href}}">{{.Label}}</a>{{else}}<b>{{.Label}}</b>{{end}}{{end}}</div>
      <div class="spacer"></div>
      <details class="dd"><summary><span class="avatar">{{.Initials}}</span></summary>
        <div class="ddmenu">
          <div style="padding:.5rem .6rem"><div style="font-weight:600;font-size:13px">{{.User}}</div><div class="muted" style="font-size:11px">signed in</div></div>
          <div class="dsep"></div>
          <a href="/account">{{icon "user"}} Account & password</a>
          <form method="post" action="/logout"><button type="submit">{{icon "logout"}} Sign out</button></form>
        </div>
      </details>
    </div>
    <div class="content">
      {{if .Flash}}<div class="flash {{if .FlashErr}}err{{end}}">{{.Flash}}</div>{{end}}
      {{.Content}}
    </div>
  </div>
</div>
</body></html>`))

// renderShell wraps page content in the full dashboard chrome.
func (a *app) renderShell(w http.ResponseWriter, r *http.Request, d shellData, content template.HTML) {
	d.Content = content
	d.Brand = brandBurst(false)
	d.User = currentUser(r)
	d.Initials = initials(d.User)
	d.Domain = a.cfg.domain
	d.Version = version
	if d.Slug != "" {
		d.IsProj = true
		d.Items = projectNav(d.Slug)
		if rows, err := a.db.Query(`SELECT slug FROM projects ORDER BY slug`); err == nil {
			for rows.Next() {
				var s string
				rows.Scan(&s)
				d.Switch = append(d.Switch, s)
			}
			rows.Close()
		}
	} else {
		d.Items = []navItem{{"projects", "Projects", "/", "grid"}, {"people", "Team", "/people", "user"}, {"audit", "Audit log", "/audit", "list"}, {"guide", "Guide", "/docs", "book"}, {"changelog", "What's New", "/changelog", "sparkle"}, {"system", "System", "/system", "database"}, {"account", "Account", "/account", "settings"}}
	}
	if m := r.URL.Query().Get("m"); m != "" && d.Flash == "" {
		d.Flash = m
	}
	if r.URL.Query().Get("e") == "1" {
		d.FlashErr = true
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	shellTmpl.Execute(w, d)
}

func initials(name string) string {
	name = strings.TrimSpace(name)
	if name == "" {
		return "PF"
	}
	parts := strings.Fields(name)
	if len(parts) == 1 {
		return strings.ToUpper(parts[0][:imin(2, len(parts[0]))])
	}
	return strings.ToUpper(string(parts[0][0]) + string(parts[len(parts)-1][0]))
}

func imin(a, b int) int {
	if a < b {
		return a
	}
	return b
}

// renderContent is a small helper: parse+execute a content template into HTML.
func renderContent(tmplText string, data any) template.HTML {
	t := template.Must(template.New("c").Funcs(funcs).Parse(tmplText))
	var buf bytes.Buffer
	t.Execute(&buf, data)
	return template.HTML(buf.String())
}

func redirectMsg(w http.ResponseWriter, r *http.Request, path, msg string) {
	sep := "?"
	if strings.Contains(path, "?") {
		sep = "&"
	}
	http.Redirect(w, r, path+sep+"m="+template.URLQueryEscaper(msg), http.StatusSeeOther)
}

func redirectErr(w http.ResponseWriter, r *http.Request, path, msg string) {
	sep := "?"
	if strings.Contains(path, "?") {
		sep = "&"
	}
	http.Redirect(w, r, path+sep+"e=1&m="+template.URLQueryEscaper(msg), http.StatusSeeOther)
}
