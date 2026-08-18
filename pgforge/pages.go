package main

// Content templates for each dashboard page. They render inside the shell
// (assets.go). Kept as strings so the whole UI ships in one Go binary.

const dashboardBody = `
<div class="pagehead" style="display:flex;align-items:flex-end;gap:1rem;flex-wrap:wrap">
  <div><h1>Projects</h1><p>Each project is an isolated Postgres database with its own credentials.</p></div>
  <div class="spacer"></div>
</div>

<div class="grid g3" style="margin-bottom:1.5rem">
  <div class="card stat"><div class="k">Projects</div><div class="v">{{.Stats.NProjects}}</div></div>
  <div class="card stat"><div class="k">Memory</div><div class="v">{{.Stats.RAMUsed}}</div><div class="muted" style="font-size:11px;margin-top:.2rem">of {{.Stats.RAMTotal}}</div></div>
  <div class="card stat"><div class="k">Disk free</div><div class="v">{{.Stats.DiskFree}}</div></div>
  <div class="card stat"><div class="k">Postgres</div><div class="v" style="font-size:18px">{{.Stats.PGVersion}}</div></div>
</div>

<form class="card" method="post" action="/create" style="display:flex;gap:.7rem;align-items:center;margin-bottom:.7rem">
  <input type="text" name="slug" placeholder="new project name  ·  letters, numbers, - and _" pattern="[A-Za-z][A-Za-z0-9_-]{1,38}[A-Za-z0-9]" required style="flex:1">
  <button class="btn btn-primary" type="submit">{{icon "plus"}} New Project</button>
  <button class="btn btn-ghost" type="button" onclick="document.getElementById('impdb').style.display='block'">{{icon "restore"}} Import from Postgres</button>
</form>
<form id="impdb" class="card" method="post" action="/clone" style="display:none;margin-bottom:1.5rem">
  <h2 style="font-size:16px;margin-bottom:.2rem">Clone an existing database</h2>
  <p class="muted" style="font-size:12.5px;margin:0 0 .8rem">Paste any Postgres connection string (Neon, Supabase, RDS, …). ForgeBase creates a new project and copies the whole database into it.</p>
  <div style="display:flex;gap:.6rem;flex-wrap:wrap">
    <input type="text" name="slug" placeholder="new project name" pattern="[A-Za-z][A-Za-z0-9_-]{1,38}[A-Za-z0-9]" required style="width:220px">
    <input type="text" name="source" placeholder="postgresql://user:pass@host/db?sslmode=require" required style="flex:1;min-width:280px;font-family:var(--mono);font-size:12px">
    <button class="btn btn-primary" type="submit">{{icon "restore"}} Clone</button>
    <button class="btn btn-ghost" type="button" onclick="document.getElementById('impdb').style.display='none'">Cancel</button>
  </div>
</form>

{{if not .Projects}}
<div class="card" style="text-align:center;padding:3rem 1rem;color:hsl(var(--muted-fg))">
  No projects yet. Create your first one above - it takes under a second.
</div>
{{else}}
<div style="display:flex;align-items:center;gap:.6rem;margin-bottom:1rem">
  <span class="label">{{len .Projects}} project(s)</span>
  <div class="spacer"></div>
  <input type="text" id="projsearch" placeholder="Search projects…" onkeyup="filterProj()" style="width:240px">
</div>
{{end}}

<div class="grid g2" id="projgrid">
{{range .Projects}}
  <div class="card proj-card" data-slug="{{.Slug}}">
    <div style="display:flex;align-items:center;gap:.6rem">
      <a href="/p/{{.Slug}}" style="font-family:var(--serif);font-size:18px;font-weight:600">{{.Slug}}</a>
      <span class="badge {{.Status}}">{{.Status}}</span>
      <div class="spacer"></div>
      <a class="btn btn-ghost btn-sm" href="/p/{{.Slug}}">Open</a>
    </div>
    <div class="muted" style="font-size:12px;margin:.5rem 0 .2rem">created {{.Created}} · {{.Size}} · {{.Conns}} connection(s)</div>
    <div class="cs"><span class="tag">Direct TLS</span><code id="d-{{.Slug}}">{{.DirectURL}}</code><button class="copy" onclick="cp('d-{{.Slug}}')">{{icon "copy"}}</button></div>
    <div class="cs"><span class="tag">Pooled</span><code id="p-{{.Slug}}">{{.PooledURL}}</code><button class="copy" onclick="cp('p-{{.Slug}}')">{{icon "copy"}}</button></div>
    <div style="display:flex;gap:.5rem;margin-top:.9rem">
      {{if eq .Status "active"}}
      <form method="post" action="/pause"><input type="hidden" name="slug" value="{{.Slug}}"><button class="btn btn-ghost btn-sm">{{icon "pause"}} Pause</button></form>
      {{else}}
      <form method="post" action="/resume"><input type="hidden" name="slug" value="{{.Slug}}"><button class="btn btn-ghost btn-sm">{{icon "play"}} Resume</button></form>
      {{end}}
      <button class="btn btn-danger btn-sm" onclick="askDel('{{.Slug}}')">{{icon "trash"}} Delete</button>
    </div>
  </div>
{{end}}
</div>
<script>
function filterProj(){var q=document.getElementById('projsearch').value.toLowerCase();
  document.querySelectorAll('.proj-card').forEach(function(c){
    c.style.display=c.getAttribute('data-slug').toLowerCase().indexOf(q)<0?'none':'';});}
</script>
` + copyJS + delDialog

const projectHomeBody = `
<div class="pagehead"><h1>{{.Slug}}</h1><p>Connection strings and a quick health snapshot.</p></div>
<div class="grid" style="grid-template-columns:repeat(auto-fill,minmax(150px,1fr));margin-bottom:1rem">
  <div class="card stat"><div class="k">Status</div><div class="v" style="font-size:20px;text-transform:capitalize">{{.Status}}</div></div>
  <div class="card stat"><div class="k">Size</div><div class="v">{{.Size}}</div></div>
  <div class="card stat"><div class="k">Tables</div><div class="v">{{.Tables}}</div></div>
  <div class="card stat"><div class="k">Branches</div><div class="v">{{.Branches}}</div></div>
  <div class="card stat"><div class="k">Connections</div><div class="v">{{.Conns}}</div></div>
</div>
<div class="card" style="margin-bottom:1rem">
  <div style="display:flex;align-items:center"><h2>Project settings</h2><div class="spacer"></div><a class="btn btn-ghost btn-sm" href="/p/{{.Slug}}/settings">Manage</a></div>
  <div class="grid" style="grid-template-columns:repeat(auto-fill,minmax(200px,1fr));margin-top:.9rem;gap:.9rem">
    <div><div class="label">Project ID</div><code style="font-size:13px">{{.Slug}}</code></div>
    <div><div class="label">Postgres version</div><div style="font-size:14px">{{.Version}}</div></div>
    <div><div class="label">Host</div><div style="font-size:14px">Hetzner Cloud · {{.Domain}}</div></div>
    <div><div class="label">Created</div><div style="font-size:14px">{{.Created}}</div></div>
    <div><div class="label">Backup retention</div><div style="font-size:14px">{{.Retention}} days + WAL archive</div></div>
    <div><div class="label">Data API</div><div style="font-size:14px">{{if .APIEnabled}}<span class="badge active">enabled</span>{{else}}<a href="/p/{{.Slug}}/api" style="color:hsl(var(--primary))">enable</a>{{end}}</div></div>
  </div>
</div>
<div class="card">
  <div style="display:flex;align-items:center;gap:.6rem;margin-bottom:.3rem">
    <h2>Connect to {{.Slug}}</h2><div class="spacer"></div>
    <div id="cxtabs" class="label"></div>
  </div>
  <p class="muted" style="font-size:12.5px;margin:.1rem 0 .7rem">Pick your stack. <b style="color:hsl(var(--fg))">Direct TLS</b> for Prisma/session, <b style="color:hsl(var(--fg))">Pooled</b> for serverless.</p>
  <div style="display:flex;gap:.4rem;flex-wrap:wrap;margin-bottom:.7rem" id="cxbar"></div>
  <div class="cs"><span class="tag" id="cxtag">psql</span><code id="cxcode">{{.Direct}}</code><button class="copy" onclick="cp('cxcode')">{{icon "copy"}}</button></div>
  <div class="cs"><span class="tag">Pooled</span><code id="hp">{{.Pooled}}</code><button class="copy" onclick="cp('hp')">{{icon "copy"}}</button></div>
</div>
<script>
(function(){
  var direct={{.Direct}}, pooled={{.Pooled}};
  var snips={
    'psql': 'psql "'+direct+'"',
    '.env': 'DATABASE_URL="'+direct+'"',
    'Prisma': 'datasource db {\n  provider = "postgresql"\n  url      = "'+direct+'"\n}',
    'Node': "import pg from 'pg';\nconst client = new pg.Client('"+direct+"');",
    'Django': "DATABASES = {'default': dj_database_url.parse('"+direct+"')}",
    'Go': 'db, _ := sql.Open("postgres", "'+direct+'")'
  };
  var bar=document.getElementById('cxbar'), code=document.getElementById('cxcode'), tag=document.getElementById('cxtag');
  Object.keys(snips).forEach(function(k,i){
    var b=document.createElement('button'); b.textContent=k;
    b.className='btn btn-ghost btn-sm'; b.style.padding='.3rem .7rem';
    b.onclick=function(){code.textContent=snips[k];tag.textContent=k;
      [].forEach.call(bar.children,function(c){c.classList.remove('btn-primary');c.classList.add('btn-ghost');});
      b.classList.remove('btn-ghost');b.classList.add('btn-primary');};
    if(i===0){b.classList.remove('btn-ghost');b.classList.add('btn-primary');}
    bar.appendChild(b);
  });
})();
</script>
<div class="grid g2" style="margin-top:1rem">
  <a class="card" href="/p/{{.Slug}}/tables" style="display:flex;align-items:center;gap:.8rem">
    <span class="brand" style="background:hsl(var(--primary)/.12);color:hsl(var(--primary));box-shadow:none">{{icon "table"}}</span>
    <div><div style="font-weight:600">Table Editor</div><div class="muted" style="font-size:12px">Browse and edit data</div></div>
  </a>
  <a class="card" href="/p/{{.Slug}}/sql" style="display:flex;align-items:center;gap:.8rem">
    <span class="brand" style="background:hsl(var(--primary)/.12);color:hsl(var(--primary));box-shadow:none">{{icon "terminal"}}</span>
    <div><div style="font-weight:600">SQL Editor</div><div class="muted" style="font-size:12px">Run queries</div></div>
  </a>
</div>
` + copyJS

const tablesBody = `
<div class="pagehead"><h1>Table Editor</h1><p>Browse, insert, edit and delete rows.</p></div>
{{if not .Tables}}
<div class="card" style="text-align:center;padding:2.5rem">
  <p style="color:hsl(var(--muted-fg));margin:0 0 1rem">No tables yet in <b>public</b>. Create one in the <a href="/p/{{.Slug}}/sql" style="color:hsl(var(--primary))">SQL Editor</a>, or import a CSV:</p>
  <form method="post" action="/p/{{.Slug}}/import" enctype="multipart/form-data" style="display:flex;gap:.6rem;align-items:center;justify-content:center;flex-wrap:wrap">
    <input type="text" name="table" placeholder="table name" required style="width:180px">
    <input type="file" name="file" accept=".csv" required style="width:auto">
    <button class="btn btn-primary btn-sm" type="submit">{{icon "upload"}} Import CSV</button>
  </form>
</div>
{{else}}
<div style="display:flex;gap:.7rem;align-items:center;margin-bottom:1rem;flex-wrap:wrap">
  <label class="label" style="display:flex;align-items:center;gap:.4rem">Database
    <select onchange="location.href='/p/'+this.value+'/tables'" style="width:auto;padding:.4rem .6rem">
      {{range .DBs}}<option value="{{.}}" {{if eq . $.Slug}}selected{{end}}>{{.}}</option>{{end}}
    </select></label>
  <span class="label" style="display:flex;align-items:center;gap:.4rem">Schema
    <span class="badge active" style="text-transform:none">public</span></span>
  <div class="spacer"></div>
  <button class="btn btn-ghost btn-sm" onclick="document.getElementById('nt').style.display='flex'">{{icon "plus"}} New table</button>
  <button class="btn btn-ghost btn-sm" onclick="document.getElementById('imp').style.display='flex'">{{icon "upload"}} Import CSV</button>
  <input type="text" id="tsearch" placeholder="Search tables…" onkeyup="filterTables()" style="width:200px">
</div>
<form id="nt" class="card" method="post" action="/p/{{.Slug}}/table-create" style="display:none;gap:.6rem;align-items:center;margin-bottom:1rem">
  <span class="label">New empty table</span>
  <input type="text" name="name" placeholder="table name" required style="width:200px">
  <span class="muted" style="font-size:11.5px">gets an <code>id bigserial primary key</code>; add columns after</span>
  <button class="btn btn-primary btn-sm" type="submit">Create</button>
  <button class="btn btn-ghost btn-sm" type="button" onclick="document.getElementById('nt').style.display='none'">Cancel</button>
</form>
<form id="imp" class="card" method="post" action="/p/{{.Slug}}/import" enctype="multipart/form-data" style="display:none;gap:.6rem;align-items:center;margin-bottom:1rem">
  <span class="label">New table from CSV</span>
  <input type="text" name="table" placeholder="table name" required style="width:180px">
  <input type="file" name="file" accept=".csv" required style="flex:1;padding:.4rem">
  <button class="btn btn-primary btn-sm" type="submit">Import</button>
  <button class="btn btn-ghost btn-sm" type="button" onclick="document.getElementById('imp').style.display='none'">Cancel</button>
</form>
<div class="split" style="display:flex;gap:1rem;align-items:flex-start">
  <div class="card" style="width:200px;flex-shrink:0;padding:.6rem">
    <div class="label" style="padding:.3rem .5rem">Tables ({{len .Tables}})</div>
    <div id="tablist">{{range .Tables}}<a class="navi tbl-item {{if eq . $.Sel}}active{{end}}" href="/p/{{$.Slug}}/tables?t={{.}}" style="font-size:12.5px" data-name="{{.}}">{{icon "table"}} {{.}}</a>{{end}}</div>
  </div>
  <div style="flex:1;min-width:0">
    <div style="display:flex;align-items:center;gap:.6rem;margin-bottom:.8rem">
      <h2 style="font-size:18px">{{.Sel}}</h2>
      <span class="muted" style="font-size:12px">{{if .EstUnknown}}~ rows{{else}}≈{{.Est}} rows{{end}}{{if not .HasPK}} · no primary key (read-only rows){{end}}</span>
      <div class="spacer"></div>
      <a class="btn btn-ghost btn-sm" href="/p/{{.Slug}}/export?t={{.Sel}}">{{icon "archive"}} Export CSV</a>
      {{if .Meta}}<button class="btn btn-ghost btn-sm" onclick="document.getElementById('ins').style.display='block'">{{icon "plus"}} Insert row</button>{{end}}
    </div>
    {{if .Error}}<div class="flash err">{{.Error}}</div>{{end}}

    <div id="ins" class="card" style="display:none;margin-bottom:.9rem">
      <form method="post" action="/p/{{.Slug}}/row-insert">
        <input type="hidden" name="__table" value="{{.Sel}}">
        <div class="grid g2">
        {{range .Meta}}<label class="fld" style="margin-bottom:.4rem"><span class="lt" style="font-size:11px">{{.Name}} <span class="muted">{{.Type}}</span></span><input type="text" name="c_{{.Name}}" placeholder="{{if .Default}}default{{else if eq .Nullable "YES"}}null{{end}}"></label>{{end}}
        </div>
        <button class="btn btn-primary btn-sm" type="submit">Insert</button>
        <button class="btn btn-ghost btn-sm" type="button" onclick="document.getElementById('ins').style.display='none'">Cancel</button>
      </form>
    </div>

    <div class="tblwrap">
      <table class="data">
        <thead><tr>{{range .Cols}}<th>{{.}}</th>{{end}}{{if .HasPK}}<th></th>{{end}}</tr></thead>
        <tbody>
        {{$s := .Slug}}{{$sel := .Sel}}{{$cols := .Cols}}{{$pk := .PK}}{{$haspk := .HasPK}}{{$types := .Types}}
        {{range $ri, $row := .Rows}}
          <tr>
            {{range $ci, $c := $row}}
              <td {{if and $haspk (ne (index $types (index $cols $ci)) "bytea")}}data-c="{{index $cols $ci}}" ondblclick="editCell(this)"{{end}}>{{if $c.Null}}<span class="null">null</span>{{else}}{{$c.Val}}{{end}}</td>
            {{end}}
            {{if $haspk}}<td style="text-align:right">
              <form method="post" action="/p/{{$s}}/row-delete" onsubmit="return confirm('Delete this row?')" style="display:inline">
                <input type="hidden" name="__table" value="{{$sel}}">
                {{range $k := $pk}}<input type="hidden" name="pk_{{$k}}" value="{{range $ci,$cn := $cols}}{{if eq $cn $k}}{{$cell := index $row $ci}}{{$cell.Val}}{{end}}{{end}}">{{end}}
                <button class="copy" title="Delete row" style="color:hsl(var(--destructive))">{{icon "trash"}}</button>
              </form>
            </td>{{end}}
          </tr>
        {{end}}
        </tbody>
      </table>
    </div>
    <div style="display:flex;align-items:center;gap:.6rem;margin-top:.7rem">
      {{if .HasPK}}<span class="muted" style="font-size:11.5px">Double-click a cell to edit.</span>{{end}}
      <div class="spacer"></div>
      {{if .Rows}}<span class="muted" style="font-size:12px">rows {{.FirstRow}}-{{.LastRow}}</span>{{end}}
      {{if .HasPrev}}<a class="btn btn-ghost btn-sm" href="/p/{{.Slug}}/tables?t={{.Sel}}&p={{.PrevPage}}">{{icon "back"}} Prev</a>{{else}}<button class="btn btn-ghost btn-sm" disabled style="opacity:.4">{{icon "back"}} Prev</button>{{end}}
      <span class="muted" style="font-size:12px">page {{.Page}}</span>
      {{if .HasNext}}<a class="btn btn-ghost btn-sm" href="/p/{{.Slug}}/tables?t={{.Sel}}&p={{.NextPage}}">Next {{icon "chevron"}}</a>{{else}}<button class="btn btn-ghost btn-sm" disabled style="opacity:.4">Next</button>{{end}}
    </div>
    {{if .Meta}}
    <div class="card" style="margin-top:1rem">
      <div style="display:flex;align-items:center;gap:.6rem"><h2 style="font-size:16px">Columns</h2><div class="spacer"></div>
        <form method="post" action="/p/{{.Slug}}/table-drop" onsubmit="return confirm('Drop table {{.Sel}} and ALL its data?')" style="display:inline"><input type="hidden" name="table" value="{{.Sel}}"><button class="btn btn-ghost btn-sm" style="color:hsl(var(--destructive))">{{icon "trash"}} Drop table</button></form>
      </div>
      <div class="tblwrap" style="margin-top:.6rem"><table class="data">
        <thead><tr><th>Column</th><th>Type</th><th>Nullable</th><th>Default</th><th></th></tr></thead>
        <tbody>{{range .Meta}}<tr><td><code>{{.Name}}</code></td><td class="muted">{{.Type}}</td><td class="muted">{{.Nullable}}</td><td class="muted" style="font-size:11px;max-width:200px">{{.Default}}</td>
          <td style="text-align:right"><form method="post" action="/p/{{$.Slug}}/column-drop" onsubmit="return confirm('Drop column {{.Name}}? Its data is lost.')" style="display:inline"><input type="hidden" name="table" value="{{$.Sel}}"><input type="hidden" name="column" value="{{.Name}}"><button class="copy" style="color:hsl(var(--destructive))" title="Drop column">×</button></form></td>
        </tr>{{end}}</tbody>
      </table></div>
      <form method="post" action="/p/{{.Slug}}/column-add" style="display:flex;gap:.5rem;flex-wrap:wrap;align-items:flex-end;margin-top:.8rem;padding-top:.8rem;border-top:1px solid hsl(var(--border))">
        <input type="hidden" name="table" value="{{.Sel}}">
        <label class="fld" style="margin:0"><span class="lt">New column</span><input type="text" name="name" placeholder="column_name" required style="width:150px"></label>
        <label class="fld" style="margin:0"><span class="lt">Type</span><select name="type"><option>text</option><option>integer</option><option>bigint</option><option>boolean</option><option>numeric</option><option>timestamptz</option><option>date</option><option>uuid</option><option>jsonb</option><option>bytea</option></select></label>
        <label class="fld" style="margin:0"><span class="lt">Default</span><input type="text" name="default" placeholder="optional" style="width:110px"></label>
        <label class="muted" style="font-size:12px;display:flex;align-items:center;gap:.3rem"><input type="checkbox" name="notnull"> not null</label>
        <button class="btn btn-primary btn-sm" type="submit">Add column</button>
      </form>
    </div>
    {{end}}
  </div>
</div>
{{end}}
<script>
function filterTables(){var q=document.getElementById('tsearch').value.toLowerCase();
  document.querySelectorAll('.tbl-item').forEach(function(a){
    a.style.display=a.getAttribute('data-name').toLowerCase().indexOf(q)<0?'none':'';});}
</script>
` + editCellJS

const sqlBody = `
<div class="pagehead"><h1>SQL Editor</h1><p>Run queries against <b>{{.Slug}}</b> as the database owner. Full Postgres - DDL, DML, functions, everything.</p></div>
<div class="split" style="display:flex;gap:1rem;align-items:flex-start">
  <div class="card sqlschema" style="width:220px;flex-shrink:0;padding:.6rem;max-height:520px;overflow-y:auto">
    <div class="label" style="padding:.2rem .4rem .5rem">Schema · public</div>
    {{if not .Schema}}<div class="muted" style="font-size:12px;padding:.3rem .4rem">No tables yet.</div>{{end}}
    {{range .Schema}}
    <details class="sqltbl">
      <summary>
        <span class="tw"></span>
        <span class="tn" onclick="event.preventDefault();ins('{{.Name}}')">{{.Name}}</span>
        <span class="tc">{{len .Cols}}</span>
      </summary>
      <div class="cols">
        {{range .Cols}}<div class="col" onclick="ins('{{.Name}}')"><span>{{.Name}}</span><span class="ty">{{.Type}}</span></div>{{end}}
      </div>
    </details>{{end}}
  </div>
  <div style="flex:1;min-width:0">
    <div style="display:flex;gap:.4rem;flex-wrap:wrap;margin-bottom:.6rem" id="snips">
      <button type="button" class="btn btn-ghost btn-sm" onclick="setq('select * from TABLE limit 100;')">SELECT</button>
      <button type="button" class="btn btn-ghost btn-sm" onclick="setq('insert into TABLE (col) values (val);')">INSERT</button>
      <button type="button" class="btn btn-ghost btn-sm" onclick="setq('update TABLE set col=val where id=1;')">UPDATE</button>
      <button type="button" class="btn btn-ghost btn-sm" onclick="setq('create table NAME (\n  id bigserial primary key,\n  name text not null,\n  created_at timestamptz default now()\n);')">CREATE TABLE</button>
      <button type="button" class="btn btn-ghost btn-sm" onclick="setq('explain analyze select * from TABLE;')">EXPLAIN</button>
      <button type="button" class="btn btn-ghost btn-sm" onclick="setq(&#34;select table_name from information_schema.tables where table_schema=&#39;public&#39;;&#34;)">List tables</button>
      <div class="spacer"></div>
      <button type="button" class="btn btn-ghost btn-sm" onclick="saveq()">{{icon "plus"}} Save query</button>
    </div>
    {{if .Saved}}
    <div style="display:flex;gap:.4rem;flex-wrap:wrap;align-items:center;margin-bottom:.6rem">
      <span class="label">Saved:</span>
      {{range .Saved}}<span style="display:inline-flex;align-items:center;gap:.2rem;background:hsl(var(--bg));border:1px solid hsl(var(--border));border-radius:99px;padding:.15rem .3rem .15rem .6rem;font-size:12px">
        <a href="/p/{{$.Slug}}/sql?load={{.ID}}">{{.Name}}</a>
        <form method="post" action="/p/{{$.Slug}}/sql/delete" style="margin:0;display:flex" onsubmit="return confirm('Delete saved query?')"><input type="hidden" name="id" value="{{.ID}}"><button class="copy" style="color:hsl(var(--destructive));padding:.1rem">{{icon "trash"}}</button></form>
      </span>{{end}}
    </div>{{end}}
    <form id="saveform" method="post" action="/p/{{.Slug}}/sql/save" style="display:none"><input type="hidden" name="name" id="savename"><input type="hidden" name="query" id="savequery"></form>
    <form method="post" action="/p/{{.Slug}}/sql" id="sqlform">
      <textarea name="query" id="q" rows="9" placeholder="select * from ..." autofocus>{{.Query}}</textarea>
      <div style="display:flex;gap:.6rem;margin-top:.7rem;align-items:center;flex-wrap:wrap">
        <button class="btn btn-primary" type="submit">{{icon "play"}} Run</button>
        <label class="muted" style="font-size:12px;display:flex;align-items:center;gap:.3rem">as
          <select name="role" style="padding:.25rem .4rem;font-size:12px">
            <option value="">owner</option>
            <option value="anon" {{if eq .RunAs "anon"}}selected{{end}}>anon</option>
            <option value="authenticated" {{if eq .RunAs "authenticated"}}selected{{end}}>authenticated</option>
            <option value="service_role" {{if eq .RunAs "service_role"}}selected{{end}}>service_role</option>
          </select></label>
        <select id="histsel" onchange="if(this.value){setq(this.value)}" style="padding:.25rem .4rem;font-size:12px;max-width:190px"><option value="">Recent...</option></select>
        <span class="muted" style="font-size:12px">Ctrl/Cmd + Enter</span>
        {{if .Took}}<div class="spacer"></div><span class="muted" style="font-size:12px">{{if .RunAs}}as {{.RunAs}} · {{end}}{{if .Ok}}{{.Affected}} row(s) affected · {{else}}{{.Count}} row(s){{if .Capped}} (first {{.Count}} shown){{end}} · {{end}}{{.Took}}</span>{{end}}
      </div>
    </form>
    {{if .Error}}<div class="flash err" style="margin-top:1rem">{{.Error}}</div>{{end}}
    {{if .Ok}}<div class="flash" style="margin-top:1rem">Success - {{.Affected}} row(s) affected in {{.Took}}.</div>{{end}}
    {{if .Cols}}
    <div style="display:flex;align-items:center;margin-top:1rem;gap:.6rem"><span class="label">Result{{if .RunAs}} (as {{.RunAs}}){{end}}</span><div class="spacer"></div><button class="btn btn-ghost btn-sm" type="button" onclick="exportResults()">{{icon "archive"}} Export CSV</button></div>
    <div class="tblwrap" style="margin-top:.5rem">
      <table class="data" id="resulttable">
        <thead><tr>{{range .Cols}}<th>{{.}}</th>{{end}}</tr></thead>
        <tbody>{{range .Rows}}<tr>{{range .}}<td>{{if .Null}}<span class="null">null</span>{{else}}{{.Val}}{{end}}</td>{{end}}</tr>{{end}}</tbody>
      </table>
    </div>
    {{if not .Rows}}<p class="muted" style="margin-top:.6rem">0 rows.</p>{{end}}
    {{end}}
  </div>
</div>
<script>
var q=document.getElementById('q');
q.addEventListener('keydown',function(e){if((e.ctrlKey||e.metaKey)&&e.key==='Enter'){this.form.submit();}});
function ins(t){var s=q.selectionStart,v=q.value;q.value=v.slice(0,s)+t+v.slice(q.selectionEnd);q.focus();q.selectionStart=q.selectionEnd=s+t.length;}
function setq(t){q.value=t;q.focus();}
function saveq(){var n=prompt('Save this query as:');if(!n)return;document.getElementById('savename').value=n;document.getElementById('savequery').value=q.value;document.getElementById('saveform').submit();}
var HKEY='fb_sqlhist_'+"{{.Slug}}";
function loadHist(){try{var h=JSON.parse(localStorage.getItem(HKEY)||'[]');var s=document.getElementById('histsel');h.forEach(function(x){var o=document.createElement('option');o.value=x;o.textContent=(x.length>55?x.slice(0,55)+'...':x).replace(/\s+/g,' ');s.appendChild(o);});}catch(e){}}
function pushHist(x){if(!x||!x.trim())return;try{var h=JSON.parse(localStorage.getItem(HKEY)||'[]');h=h.filter(function(v){return v!==x});h.unshift(x);h=h.slice(0,25);localStorage.setItem(HKEY,JSON.stringify(h));}catch(e){}}
document.getElementById('sqlform').addEventListener('submit',function(){pushHist(q.value);});
loadHist();
function exportResults(){var t=document.getElementById('resulttable');if(!t)return;var lines=[];for(var r=0;r<t.rows.length;r++){var row=[];for(var c=0;c<t.rows[r].cells.length;c++){row.push('"'+t.rows[r].cells[c].innerText.replace(/"/g,'""')+'"');}lines.push(row.join(','));}var a=document.createElement('a');a.href=URL.createObjectURL(new Blob([lines.join('\n')],{type:'text/csv'}));a.download='query_result.csv';a.click();}
</script>`

const databaseBody = `
<div class="pagehead"><h1>Database</h1><p>Credentials, extensions and vitals for <b>{{.Slug}}</b>.</p></div>
<div class="grid g2">
  <div class="card">
    <h2>Rotate password</h2>
    <p class="muted" style="font-size:12.5px;margin:.3rem 0 .8rem">Sets a new password for role <code>{{.Slug}}</code>. Leave blank to auto-generate a strong one. Update your app's connection string afterward.</p>
    <form method="post" action="/p/{{.Slug}}/db-password" onsubmit="return confirm('Rotate the database password now?')">
      <label class="fld"><span class="lt">New password (optional)</span><input type="text" name="password" placeholder="leave blank to auto-generate"></label>
      <button class="btn btn-primary" type="submit">{{icon "key"}} Rotate password</button>
    </form>
  </div>
  <div class="card">
    <h2>Vitals</h2>
    <div style="display:flex;gap:2rem;margin-top:.8rem;flex-wrap:wrap">
      <div><div class="label">Size</div><div style="font-family:var(--serif);font-size:22px">{{.Size}}</div></div>
      <div><div class="label">Connections</div><div style="font-family:var(--serif);font-size:22px">{{.Conns}}</div></div>
      <div><div class="label">Conn limit</div><div style="font-family:var(--serif);font-size:22px">{{if lt .ConnLimit 0}}&infin;{{else}}{{.ConnLimit}}{{end}}</div></div>
      <div><div class="label">Postgres</div><div style="font-family:var(--serif);font-size:22px">{{.Version}}</div></div>
    </div>
  </div>
</div>
<div class="card" style="margin-top:1rem">
  <h2>Connection &amp; pooling</h2>
  <p class="muted" style="font-size:12.5px;margin:.3rem 0 .8rem">Two entry points to this database. Cluster max connections: <b style="color:hsl(var(--fg))">{{.MaxConns}}</b>.</p>
  <div class="grid" style="grid-template-columns:repeat(auto-fill,minmax(220px,1fr));gap:.9rem">
    <div><div class="label">Direct (session, TLS)</div><div style="font-size:14px">{{.Domain}}<b>:5432</b></div><div class="muted" style="font-size:11px">Prisma, migrations, long sessions</div></div>
    <div><div class="label">Pooled (transaction)</div><div style="font-size:14px">{{.Domain}}<b>:6543</b></div><div class="muted" style="font-size:11px">serverless, high concurrency</div></div>
  </div>
  <div style="display:flex;gap:2.5rem;flex-wrap:wrap;margin-top:1.1rem;padding-top:1rem;border-top:1px solid hsl(var(--border))">
    <form method="post" action="/p/{{.Slug}}/conn-limit" style="display:flex;gap:.6rem;align-items:flex-end">
      <label class="fld" style="margin:0"><span class="lt">Project connection limit</span>
        <input type="number" name="limit" value="{{.ConnLimit}}" min="-1" max="10000" step="1" style="width:130px"></label>
      <button class="btn btn-primary btn-sm" type="submit">Save</button>
      <span class="muted" style="font-size:11.5px;max-width:230px">Concurrent connections role <code>{{.Slug}}</code> may hold. -1 = unlimited. Applies instantly to new connections.</span>
    </form>
    {{if .CanAdmin}}
    <form method="post" action="/p/{{.Slug}}/max-conns" style="display:flex;gap:.6rem;align-items:flex-end"
      onsubmit="return confirm('Change cluster max connections and restart Postgres now? Every project loses its connections for a few seconds.')">
      <label class="fld" style="margin:0"><span class="lt">Cluster max connections</span>
        <input type="number" name="max" value="{{.MaxConns}}" min="10" max="2000" step="1" style="width:130px"></label>
      <button class="btn btn-sm" type="submit">Save &amp; restart</button>
      <span class="muted" style="font-size:11.5px;max-width:250px">Platform-wide ceiling across all projects. Restarts Postgres (~10s). Each connection costs RAM; keep it modest on this box.</span>
    </form>
    {{end}}
  </div>
</div>
<div class="card" style="margin-top:1rem">
  <h2>Roles</h2>
  <p class="muted" style="font-size:12.5px;margin:.3rem 0 .8rem">Postgres roles in this database. The owner role matches the project name; anon/authenticated/service_role appear when the Data API or Auth are enabled.</p>
  <div class="tblwrap"><table class="data">
    <thead><tr><th>Role</th><th>Attributes</th></tr></thead>
    <tbody>{{range .Roles}}<tr><td><code>{{.Name}}</code></td><td class="muted" style="font-size:11.5px">{{.Attrs}}</td></tr>{{end}}</tbody>
  </table></div>
  <div style="display:flex;gap:.8rem;align-items:center;flex-wrap:wrap;margin-top:1rem;padding-top:1rem;border-top:1px solid hsl(var(--border))">
    <form method="post" action="/p/{{.Slug}}/createdb" style="display:inline">
      {{if .CanCreateDB}}<input type="hidden" name="action" value="revoke">
      <button class="btn btn-sm" type="submit">Revoke database creation</button>
      {{else}}<input type="hidden" name="action" value="allow">
      <button class="btn btn-sm" type="submit">Allow database creation</button>{{end}}
    </form>
    <span class="muted" style="font-size:11.5px;max-width:520px">{{if .CanCreateDB}}Role <code>{{.Slug}}</code> can create scratch databases (needed by <code>prisma migrate dev</code>'s shadow DB). Databases it creates are not ForgeBase projects: no panel, metrics or API, though nightly dumps still cover them.{{else}}Off by default. Enable if a tool needs to create scratch databases, e.g. the shadow database of <code>prisma migrate dev</code>.{{end}}</span>
  </div>
</div>
<div class="card" style="margin-top:1rem">
  <div style="display:flex;align-items:center;gap:.6rem;flex-wrap:wrap">
    <h2>Extensions</h2>
    <span class="badge active" style="text-transform:none">{{.NInstalled}} enabled</span>
    <span class="muted" style="font-size:12px">· {{.NAvail}} available</span>
    <div class="spacer"></div>
    <input type="text" id="extsearch" placeholder="Search extensions…" onkeyup="filterExt()" style="width:240px">
  </div>
  <p class="muted" style="font-size:12.5px;margin:.5rem 0 .8rem">Add capabilities to this database - vectors, cron, crypto, geospatial, full-text search and more. Enabled per project.</p>
  <div class="tblwrap">
    <table class="data"><thead><tr><th>Extension</th><th>Description</th><th>Version</th><th></th></tr></thead>
    <tbody id="extrows">
    {{range .Exts}}
      <tr class="ext-row" data-name="{{.Name}} {{.Comment}}">
        <td><code style="font-size:12px">{{.Name}}</code></td>
        <td style="max-width:460px;white-space:normal;color:hsl(var(--muted-fg))">{{.Comment}}</td>
        <td class="muted">{{.Version}}</td>
        <td style="text-align:right">
          <form method="post" action="/p/{{$.Slug}}/extension" style="display:inline">
            <input type="hidden" name="ext" value="{{.Name}}">
            {{if .Installed}}<input type="hidden" name="action" value="disable"><button class="btn btn-ghost btn-sm" style="color:hsl(var(--destructive))" onclick="return confirm('Disable {{.Name}}?')">Disable</button>
            {{else}}<input type="hidden" name="action" value="enable"><button class="btn btn-ghost btn-sm" type="submit">Enable</button>{{end}}
          </form>
        </td>
      </tr>
    {{end}}
    </tbody></table>
  </div>
</div>
<script>
function filterExt(){var q=document.getElementById('extsearch').value.toLowerCase();
  document.querySelectorAll('.ext-row').forEach(function(tr){
    tr.style.display=tr.getAttribute('data-name').toLowerCase().indexOf(q)<0?'none':'';});}
</script>`

const backupsBody = `
<div class="pagehead"><h1>Backup &amp; Restore</h1><p>Automatic nightly dumps, continuous WAL archiving and off-box copies for <b>{{.Slug}}</b>.</p></div>
<div class="grid g2" style="margin-bottom:1rem">
  <div class="card">
    <h2>Retention</h2>
    <p class="muted" style="font-size:12.5px;margin:.3rem 0 .7rem">Automatic nightly dumps at 03:30 UTC, kept as a rolling window then pruned. Applies to every project.</p>
    <form method="post" action="/p/{{.Slug}}/retention" style="display:flex;gap:.5rem;align-items:center">
      <input type="number" name="days" min="1" max="365" value="{{.Retention}}" style="width:90px">
      <span class="muted" style="font-size:13px">days</span>
      <button class="btn btn-ghost btn-sm" type="submit">Save</button>
    </form>
  </div>
  <div class="card">
    <h2>Off-box copy</h2>
    {{if .Remote}}<p class="muted" style="font-size:12.5px;margin:.3rem 0">Synced to <code>{{.Remote}}</code> after every nightly run.</p>
    {{else}}<p class="muted" style="font-size:12.5px;margin:.3rem 0">Not configured.</p>{{end}}
    <form method="post" action="/p/{{.Slug}}/backup-now" style="margin-top:.6rem"><button class="btn btn-primary btn-sm" type="submit">{{icon "archive"}} Back up now</button></form>
  </div>
</div>
<div class="card" style="margin-bottom:1rem">
  <h2>Restore to a point in time</h2>
  <p class="muted" style="font-size:12.5px;margin:.3rem 0 .8rem">Recover <b>{{.Slug}}</b> to any instant, down to the second, into a <b>new</b> project - the source is never touched. This replays the continuous WAL archive forward from a basebackup to the moment you pick. Available for any time within the physical-backup window (basebackups kept 7 days).</p>
  <form method="post" action="/p/{{.Slug}}/pitr" style="display:flex;gap:.6rem;flex-wrap:wrap;align-items:flex-end"
        onsubmit="return confirm('Recover '+{{.Slug}}+' to the chosen time into a new project?')">
    <div><div class="label">Point in time (UTC)</div><input type="datetime-local" name="target" step="1" required style="width:220px"></div>
    <div><div class="label">New project name</div><input type="text" name="new_slug" placeholder="{{.Slug}}-restore" required style="width:200px"></div>
    <button class="btn btn-primary btn-sm" type="submit">{{icon "restore"}} Recover to new project</button>
  </form>
</div>
<div class="card">
  <h2>Restore points</h2>
  <p class="muted" style="font-size:12.5px;margin:.3rem 0">Restore replaces the current data in <b>{{.Slug}}</b> with the selected daily snapshot (drops and recreates objects). For recovery to an exact moment, use "Restore to a point in time" above.</p>
  {{if not .Files}}<p class="muted" style="margin-top:.6rem">No dumps yet for this project. Use "Back up now" or wait for tonight's run.</p>
  {{else}}
  <div class="tblwrap" style="margin-top:.8rem">
    <table class="data"><thead><tr><th>Snapshot</th><th>Size</th><th>Created</th><th></th></tr></thead>
    <tbody>{{range .Files}}<tr><td>{{.Name}}</td><td>{{.Size}}</td><td>{{.Age}}</td>
      <td style="text-align:right"><form method="post" action="/p/{{$.Slug}}/restore" onsubmit="return confirm('Restore this snapshot? It replaces all current data in {{$.Slug}}.')" style="display:inline">
        <input type="hidden" name="file" value="{{.Name}}">
        <button class="btn btn-ghost btn-sm" type="submit">{{icon "restore"}} Restore</button>
      </form></td></tr>{{end}}</tbody></table>
  </div>{{end}}
</div>`

const settingsBody = `
<div class="pagehead"><h1>Settings</h1><p>Manage the <b>{{.Slug}}</b> project.</p></div>
<div class="card" style="margin-bottom:1rem">
  <h2>Project details</h2>
  <div class="grid" style="grid-template-columns:repeat(auto-fill,minmax(160px,1fr));margin-top:.8rem;gap:1rem">
    <div><div class="label">Project ID</div><code style="font-size:13px">{{.Slug}}</code></div>
    <div><div class="label">Status</div><div style="font-size:15px;text-transform:capitalize">{{.Status}}</div></div>
    <div><div class="label">Size</div><div style="font-size:15px">{{.Size}}</div></div>
    <div><div class="label">Postgres</div><div style="font-size:15px">{{.Version}}</div></div>
    <div><div class="label">Created</div><div style="font-size:15px">{{.Created}}</div></div>
    <div><div class="label">Last active</div><div style="font-size:15px">{{.LastActive}}</div></div>
    <div><div class="label">Host</div><div style="font-size:15px">Hetzner Cloud</div></div>
    <div><div class="label">API host</div><code style="font-size:11px">{{.Slug}}.{{.Domain}}</code></div>
  </div>
</div>
<div class="card" style="margin-bottom:1rem">
  <h2>Features</h2>
  <p class="muted" style="font-size:12.5px;margin:.3rem 0 .8rem">What's turned on for this project. Toggle each from its own page.</p>
  <div class="grid" style="grid-template-columns:repeat(auto-fill,minmax(150px,1fr));gap:.7rem">
    <a class="card" href="/p/{{.Slug}}/api" style="padding:.7rem .9rem;display:flex;align-items:center;gap:.5rem">{{icon "api"}} <span style="flex:1;font-size:13px">Data API</span>{{if .API}}<span class="badge active">on</span>{{else}}<span class="badge paused">off</span>{{end}}</a>
    <a class="card" href="/p/{{.Slug}}/auth" style="padding:.7rem .9rem;display:flex;align-items:center;gap:.5rem">{{icon "shield"}} <span style="flex:1;font-size:13px">Auth</span>{{if .Auth}}<span class="badge active">on</span>{{else}}<span class="badge paused">off</span>{{end}}</a>
    <a class="card" href="/p/{{.Slug}}/realtime" style="padding:.7rem .9rem;display:flex;align-items:center;gap:.5rem">{{icon "bolt"}} <span style="flex:1;font-size:13px">Realtime</span>{{if .Realtime}}<span class="badge active">on</span>{{else}}<span class="badge paused">off</span>{{end}}</a>
    <a class="card" href="/p/{{.Slug}}/storage" style="padding:.7rem .9rem;display:flex;align-items:center;gap:.5rem">{{icon "folder"}} <span style="flex:1;font-size:13px">Storage</span></a>
  </div>
</div>
<div class="card" style="margin-bottom:1rem">
  <h2>Availability</h2>
  <p class="muted" style="font-size:12.5px;margin:.3rem 0 .8rem">Pause blocks all connections and stops the Data API but keeps every byte of data. Resume anytime.</p>
  {{if eq .Status "active"}}
  <form method="post" action="/pause"><input type="hidden" name="slug" value="{{.Slug}}"><button class="btn btn-ghost">{{icon "pause"}} Pause project</button></form>
  {{else}}
  <form method="post" action="/resume"><input type="hidden" name="slug" value="{{.Slug}}"><button class="btn btn-primary">{{icon "play"}} Resume project</button></form>
  {{end}}
</div>
<div class="card" style="margin-bottom:1rem">
  <h2>Compute &amp; sleep</h2>
  <p class="muted" style="font-size:12.5px;margin:.4rem 0 0">To save resources, a project with no activity for <b style="color:hsl(var(--fg))">14 days</b> is automatically suspended (connections blocked, Data API stopped). It <b style="color:hsl(var(--fg))">wakes instantly on the next request</b> - opening it here, a database connection, or an API call. Idle Data API processes stop after 15 minutes and restart on demand.</p>
</div>
<div class="card" style="border-color:hsl(var(--destructive)/.4)">
  <h2 style="color:hsl(var(--destructive))">Danger zone</h2>
  <p class="muted" style="font-size:12.5px;margin:.3rem 0 .8rem">Deleting drops the database and all its data permanently. This cannot be undone.</p>
  <button class="btn btn-danger" onclick="askDel('{{.Slug}}')">{{icon "trash"}} Delete this project</button>
</div>
` + delDialog

const monitoringBody = `
<div class="pagehead"><h1>Monitoring</h1><p>Live health and usage for <b>{{.Slug}}</b>. Counters are cumulative since the last stats reset.</p></div>
<div class="grid g3" style="margin-bottom:1rem">
  <div class="card stat"><div class="k">Storage</div><div class="v">{{.Size}}</div></div>
  <div class="card stat"><div class="k">Cache hit ratio</div><div class="v">{{.Hit}}%</div>
    <div style="height:6px;background:hsl(var(--muted));border-radius:99px;margin-top:.6rem;overflow:hidden"><div style="height:100%;width:{{.HitPct}}%;background:hsl(var(--primary))"></div></div></div>
  <div class="card stat"><div class="k">Connections</div><div class="v">{{.Conns}} <span style="font-size:13px;color:hsl(var(--muted-fg))">/ {{.MaxConns}}</span></div>
    <div style="height:6px;background:hsl(var(--muted));border-radius:99px;margin-top:.6rem;overflow:hidden"><div style="height:100%;width:{{.ConnPct}}%;background:hsl(var(--primary))"></div></div></div>
</div>
<div class="grid" style="grid-template-columns:repeat(auto-fill,minmax(150px,1fr));margin-bottom:1rem">
  <div class="card stat"><div class="k">Host RAM</div><div class="v" style="font-size:19px">{{.RAMUsed}}</div><div class="muted" style="font-size:11px">of {{.RAMTotal}}</div></div>
  <div class="card stat"><div class="k">CPU load (1m)</div><div class="v" style="font-size:19px">{{.Load}}</div><div class="muted" style="font-size:11px">{{.Cores}} cores</div></div>
  <div class="card stat"><div class="k">Deadlocks</div><div class="v" style="font-size:19px">{{.Deadlocks}}</div></div>
  <div class="card stat"><div class="k">Commits</div><div class="v" style="font-size:19px">{{.Commits}}</div></div>
  <div class="card stat"><div class="k">Rollbacks</div><div class="v" style="font-size:19px">{{.Rollbacks}}</div></div>
  <div class="card stat"><div class="k">Rows inserted</div><div class="v" style="font-size:19px">{{.TupIns}}</div></div>
  <div class="card stat"><div class="k">Rows updated</div><div class="v" style="font-size:19px">{{.TupUpd}}</div></div>
  <div class="card stat"><div class="k">Rows deleted</div><div class="v" style="font-size:19px">{{.TupDel}}</div></div>
  <div class="card stat"><div class="k">Temp files</div><div class="v" style="font-size:19px">{{.TempFiles}}</div></div>
</div>
<div class="card" style="margin-bottom:1rem">
  <div style="display:flex;align-items:center"><h2>Resource usage</h2><div class="spacer"></div><span class="label">last 7 days</span></div>
  <div class="grid g2" style="margin-top:.9rem">
    <div><div class="label" style="margin-bottom:.3rem">Database size</div>{{.ChartSize}}</div>
    <div><div class="label" style="margin-bottom:.3rem">Connections</div>{{.ChartConn}}</div>
    <div><div class="label" style="margin-bottom:.3rem">Host RAM (MB)</div>{{.ChartRAM}}</div>
    <div><div class="label" style="margin-bottom:.3rem">CPU load</div>{{.ChartCPU}}</div>
  </div>
  <p class="muted" style="font-size:11px;margin-top:.7rem">Sampled every 5 minutes. Charts fill in over time.</p>
</div>
<div class="card" style="margin-bottom:1rem">
  <h2>Table sizes</h2>
  {{if not .Tables}}<p class="muted" style="margin-top:.5rem">No tables yet.</p>{{else}}
  <div style="margin-top:.9rem;display:flex;flex-direction:column;gap:.6rem">
  {{range .Tables}}
    <div style="display:flex;align-items:center;gap:.8rem">
      <code style="width:150px;font-size:12px;flex-shrink:0;overflow:hidden;text-overflow:ellipsis;white-space:nowrap">{{.Label}}</code>
      <div style="flex:1;height:10px;background:hsl(var(--muted));border-radius:99px;overflow:hidden"><div style="height:100%;width:{{.Pct}}%;background:hsl(var(--primary))"></div></div>
      <span class="muted" style="width:70px;text-align:right;font-size:12px">{{.Disp}}</span>
    </div>
  {{end}}
  </div>{{end}}
</div>
<div class="card">
  <h2>Top queries by total time</h2>
  {{if .PSS}}{{if .Top}}
  <div class="tblwrap" style="margin-top:.8rem">
    <table class="data"><thead><tr><th>Query</th><th>Calls</th><th>Mean ms</th><th>Total ms</th></tr></thead>
    <tbody>{{range .Top}}<tr><td style="max-width:520px"><code style="font-size:11.5px">{{.Query}}</code></td><td>{{.Calls}}</td><td>{{.MeanMs}}</td><td>{{.TotalMs}}</td></tr>{{end}}</tbody></table>
  </div>{{else}}<p class="muted" style="margin-top:.5rem">No query statistics recorded yet.</p>{{end}}
  {{else}}<p class="muted" style="margin-top:.5rem">Enable the <b>pg_stat_statements</b> extension in <a href="/p/{{.Slug}}/database" style="color:hsl(var(--primary))">Database</a> to see query analytics.</p>{{end}}
</div>`

const auditBody = `
<div class="pagehead"><h1>Audit log</h1><p>Platform-wide - every login, registration and privileged action across all projects, with source IPs.</p></div>
<div class="grid g3" style="margin-bottom:1rem">
  <div class="card stat"><div class="k">Events shown</div><div class="v">{{len .Events}}</div></div>
  <div class="card stat"><div class="k">Logins (7d)</div><div class="v">{{.Logins}}</div></div>
  <div class="card stat"><div class="k">Failed logins (7d)</div><div class="v">{{.Failed}}</div></div>
</div>
<div class="card">
  <div style="display:flex;align-items:center;gap:.6rem"><h2>All activity</h2><div class="spacer"></div>
    <input type="text" id="logsearch" placeholder="Filter by actor, IP, action…" onkeyup="filterLogs()" style="width:260px"></div>
  {{if not .Events}}<p class="muted" style="margin-top:.5rem">No events yet.</p>{{else}}
  <div class="tblwrap" style="margin-top:.8rem"><table class="data">
    <thead><tr><th>When</th><th>Actor</th><th>Source IP</th><th>Action</th><th>Target</th></tr></thead>
    <tbody id="logrows">{{range .Events}}<tr class="log-row" data-s="{{.Actor}} {{.IP}} {{.Action}} {{.Target}}">
      <td class="muted" style="white-space:nowrap">{{.At}}</td><td><b>{{.Actor}}</b></td>
      <td class="mono" style="font-size:11px">{{.IP}}</td>
      <td>{{if eq .Action "login-failed"}}<span class="badge paused">{{.Action}}</span>{{else if eq .Action "denied"}}<span class="badge paused">{{.Action}}</span>{{else}}<span class="badge active">{{.Action}}</span>{{end}}</td>
      <td class="muted">{{.Target}}</td></tr>{{end}}</tbody>
  </table></div>{{end}}
</div>
<script>
function filterLogs(){var q=document.getElementById('logsearch').value.toLowerCase();
  document.querySelectorAll('.log-row').forEach(function(tr){
    tr.style.display=tr.getAttribute('data-s').toLowerCase().indexOf(q)<0?'none':'';});}
</script>`

const logsBody = `
<div class="pagehead"><h1>Logs</h1><p>Activity for <b>{{.Slug}}</b> only - its actions and live database sessions. Platform-wide logins are in the <a href="/audit" style="color:hsl(var(--primary))">Audit log</a>.</p></div>
<div class="card" style="margin-bottom:1rem">
  <div style="display:flex;align-items:center"><h2>Live database activity</h2><div class="spacer"></div><span class="label">{{len .Acts}} session(s)</span></div>
  {{if not .Acts}}<p class="muted" style="margin-top:.5rem">No active sessions right now.</p>{{else}}
  <div class="tblwrap" style="margin-top:.8rem"><table class="data">
    <thead><tr><th>PID</th><th>State</th><th>Client</th><th>Statement</th><th>Started</th><th>Elapsed</th></tr></thead>
    <tbody>{{range .Acts}}<tr><td class="muted">{{.PID}}</td><td>{{if eq .State "active"}}<span class="badge active">active</span>{{else}}<span class="muted">{{.State}}</span>{{end}}</td><td class="mono" style="font-size:11px">{{.Client}}</td><td><code style="font-size:11px">{{.Query}}</code></td><td class="muted">{{.Started}}</td><td class="muted">{{.Dur}}</td></tr>{{end}}</tbody>
  </table></div>{{end}}
</div>
<div class="card">
  <div style="display:flex;align-items:center;gap:.6rem"><h2>Audit trail</h2><div class="spacer"></div>
    <input type="text" id="logsearch" placeholder="Filter…" onkeyup="filterLogs()" style="width:200px"></div>
  <p class="muted" style="font-size:12px;margin:.3rem 0 0">Every privileged action and every login - actor, source IP, action and target.</p>
  {{if not .Events}}<p class="muted" style="margin-top:.5rem">No events yet.</p>{{else}}
  <div class="tblwrap" style="margin-top:.8rem"><table class="data">
    <thead><tr><th>When</th><th>Actor</th><th>Source IP</th><th>Action</th><th>Target</th></tr></thead>
    <tbody id="logrows">{{range .Events}}<tr class="log-row" data-s="{{.Actor}} {{.IP}} {{.Action}} {{.Target}}">
      <td class="muted" style="white-space:nowrap">{{.At}}</td>
      <td><b>{{.Actor}}</b></td>
      <td class="mono" style="font-size:11px">{{.IP}}</td>
      <td>{{if eq .Action "login-failed"}}<span class="badge paused">{{.Action}}</span>{{else}}<span class="badge active">{{.Action}}</span>{{end}}</td>
      <td class="muted">{{.Target}}</td></tr>{{end}}</tbody>
  </table></div>{{end}}
</div>
<script>
function filterLogs(){var q=document.getElementById('logsearch').value.toLowerCase();
  document.querySelectorAll('.log-row').forEach(function(tr){
    tr.style.display=tr.getAttribute('data-s').toLowerCase().indexOf(q)<0?'none':'';});}
</script>`

const branchesBody = `
<div class="pagehead"><h1>Branches</h1><p>Instant copies of <b>{{.Slug}}</b> - spin up an isolated database for staging, a feature, or a migration test.</p></div>
<form class="card" method="post" action="/p/{{.Slug}}/branch-create" style="display:flex;gap:.6rem;align-items:center;margin-bottom:1.2rem">
  <span class="mono muted" style="font-size:13px">{{.Slug}}-</span>
  <input type="text" name="name" placeholder="branch name  ·  e.g. staging" pattern="[A-Za-z0-9][A-Za-z0-9_-]{0,30}" required style="flex:1">
  <button class="btn btn-primary" type="submit">{{icon "branch"}} Create branch</button>
</form>
{{if not .Branches}}
<div class="card" style="text-align:center;padding:2.5rem;color:hsl(var(--muted-fg))">No branches yet. A branch is a full copy of this database with its own connection string.</div>
{{else}}
<div class="grid g2">
{{range .Branches}}
  <div class="card">
    <div style="display:flex;align-items:center;gap:.5rem">
      <span class="brand" style="width:30px;height:30px;background:hsl(var(--primary)/.12);color:hsl(var(--primary));box-shadow:none">{{icon "branch"}}</span>
      <a href="/p/{{.Slug}}" style="font-family:var(--serif);font-size:16px;font-weight:600;flex:1">{{.Slug}}</a>
      <a class="btn btn-ghost btn-sm" href="/p/{{.Slug}}">Open</a>
    </div>
    <div class="muted" style="font-size:12px;margin:.5rem 0 .2rem">created {{.Created}} · {{.Size}} · branch of {{$.Slug}}</div>
    <div class="cs"><span class="tag">Direct TLS</span><code id="b-{{.Slug}}">{{.Conn}}</code><button class="copy" onclick="cp('b-{{.Slug}}')">{{icon "copy"}}</button></div>
    <div style="margin-top:.7rem"><button class="btn btn-danger btn-sm" onclick="askDel('{{.Slug}}')">{{icon "trash"}} Delete branch</button></div>
  </div>
{{end}}
</div>{{end}}
` + delDialog + copyJS

const apiBody = `
<div class="pagehead"><h1>Data API</h1><p>An instant REST API for <b>{{.Slug}}</b>, generated from your schema and served over HTTPS.</p></div>
{{if not .Enabled}}
<div class="card" style="text-align:center;padding:2.4rem">
  <h2>Turn on the Data API</h2>
  <p class="muted" style="font-size:13px;margin:.5rem auto .9rem;max-width:460px">This creates <code>anon</code> and <code>service_role</code> roles, mints API keys, and serves a REST endpoint at <code>{{.Base}}</code>. Reads use the anon key; writes use the service key.</p>
  <form method="post" action="/p/{{.Slug}}/api-enable"><button class="btn btn-primary" type="submit">{{icon "api"}} Enable Data API</button></form>
</div>
{{else}}
<div class="card" style="margin-bottom:1rem">
  <div style="display:flex;align-items:center;gap:.6rem"><h2>Endpoint</h2><span class="badge active">live</span><div class="spacer"></div>
    <form method="post" action="/p/{{.Slug}}/api-disable"><button class="btn btn-ghost btn-sm" type="submit">Disable</button></form></div>
  <div class="cs"><span class="tag">Base URL</span><code id="apibase">{{.Base}}</code><button class="copy" onclick="cp('apibase')">{{icon "copy"}}</button></div>
  <div class="cs"><span class="tag">anon key</span><code id="anon">{{.Anon}}</code><button class="copy" onclick="cp('anon')">{{icon "copy"}}</button></div>
  <div class="cs"><span class="tag">service key</span><code id="svc">{{.Service}}</code><button class="copy" onclick="cp('svc')">{{icon "copy"}}</button></div>
  <p class="muted" style="font-size:11.5px;margin-top:.7rem">Keep the <b>service key</b> secret - it bypasses row-level security. Ship only the anon key to browsers.</p>
</div>
<div class="card" style="margin-bottom:1rem">
  <div style="display:flex;align-items:center"><h2>Endpoints</h2><div class="spacer"></div><span class="label">{{len .Tables}} table(s)</span></div>
  <p class="muted" style="font-size:12.5px;margin:.3rem 0 .6rem">One REST resource is generated per table. Click to copy its URL.</p>
  {{if not .Tables}}<p class="muted" style="font-size:12.5px">No tables yet - create one in the Table or SQL editor and it appears here automatically.</p>
  {{else}}
  <div class="tblwrap"><table class="data">
    <thead><tr><th>Table</th><th>Endpoint</th><th>Methods</th><th></th></tr></thead>
    <tbody>{{range .Tables}}<tr>
      <td><code>{{.}}</code></td>
      <td><code id="ep-{{.}}" style="font-size:11px;color:hsl(var(--primary))">{{$.Base}}/{{.}}</code></td>
      <td class="muted" style="font-size:11px">GET · POST · PATCH · DELETE</td>
      <td style="text-align:right"><button class="copy" onclick="cp('ep-{{.}}')">{{icon "copy"}}</button></td>
    </tr>{{end}}</tbody>
  </table></div>{{end}}
</div>
<div class="card" style="margin-bottom:1rem">
  <div style="display:flex;align-items:center;gap:.6rem"><h2>Row Level Security</h2>{{icon "shield"}}</div>
  <p class="muted" style="font-size:12.5px;margin:.3rem 0 .8rem">Restrict rows to the signed-in user. Enable RLS on a table (it then denies all access until a policy allows it), then add a policy. Policies use <code>auth.uid()</code> - the user id from the JWT. The <b>service key bypasses RLS</b>; the anon and authenticated keys obey it.{{if not $.CanAdmin}} Admin role required to change policies.{{end}}</p>
  {{if not .RLS}}<p class="muted" style="font-size:12.5px">No tables yet.</p>{{else}}
  <div class="tblwrap"><table class="data">
    <thead><tr><th>Table</th><th>RLS</th><th>Policies</th>{{if $.CanAdmin}}<th></th>{{end}}</tr></thead>
    <tbody>{{range .RLS}}{{$t := .}}<tr>
      <td><code>{{.Name}}</code></td>
      <td>{{if .Enabled}}<span class="badge active">on</span>{{else}}<span class="badge" style="color:hsl(var(--destructive))">off</span>{{end}}</td>
      <td style="white-space:normal">{{if .Policies}}{{range .Policies}}<span class="badge" style="text-transform:none;margin:1px;font-weight:500">{{.Name}} <span class="muted">{{.Cmd}}</span>{{if $.CanAdmin}} <form method="post" action="/p/{{$.Slug}}/rls/policy-drop" style="display:inline" onsubmit="return confirm('Drop policy {{.Name}}?')"><input type="hidden" name="table" value="{{$t.Name}}"><input type="hidden" name="policy" value="{{.Name}}"><button class="copy" style="color:hsl(var(--destructive));padding:0 3px" title="Drop">×</button></form>{{end}}</span>{{end}}{{else}}<span class="muted" style="font-size:11px">none</span>{{end}}</td>
      {{if $.CanAdmin}}<td style="text-align:right;white-space:nowrap">
        <form method="post" action="/p/{{$.Slug}}/rls/toggle" style="display:inline"><input type="hidden" name="table" value="{{.Name}}"><input type="hidden" name="action" value="{{if .Enabled}}disable{{else}}enable{{end}}"><button class="btn btn-ghost btn-sm">{{if .Enabled}}Disable{{else}}Enable{{end}}</button></form>
      </td>{{end}}
    </tr>{{end}}</tbody>
  </table></div>
  {{if $.CanAdmin}}
  <div style="margin-top:1rem;padding-top:.9rem;border-top:1px solid hsl(var(--border))">
    <div class="label" style="margin-bottom:.5rem">Add a policy from a template</div>
    <form method="post" action="/p/{{$.Slug}}/rls/policy" style="display:flex;gap:.5rem;flex-wrap:wrap;align-items:flex-end">
      <label class="fld" style="margin:0"><span class="lt">Table</span><select name="table">{{range .RLS}}<option value="{{.Name}}">{{.Name}}</option>{{end}}</select></label>
      <label class="fld" style="margin:0"><span class="lt">Template</span><select name="template">
        <option value="public-read">Public read (anyone)</option>
        <option value="auth-read">Authenticated read</option>
        <option value="auth-write">Authenticated read + write</option>
        <option value="owner">Owner only (auth.uid() = column)</option>
      </select></label>
      <label class="fld" style="margin:0"><span class="lt">Owner column</span><input type="text" name="column" placeholder="user_id" style="width:120px"></label>
      <button class="btn btn-primary btn-sm" type="submit">Add policy</button>
    </form>
    <p class="muted" style="font-size:11px;margin-top:.5rem">"Owner only" needs a uuid column holding the user's id; adding any policy also turns RLS on for that table.</p>
  </div>{{end}}
  {{end}}
</div>
<div class="card" style="margin-bottom:1rem">
  <h2>Quick start</h2>
  <p class="muted" style="font-size:12.5px;margin:.3rem 0 .6rem">Read rows from a table (replace <code>your_table</code>):</p>
  <div class="cs"><span class="tag">GET</span><code id="ex1">curl '{{.Base}}/your_table?select=*' -H "apikey: {{.Anon}}"</code><button class="copy" onclick="cp('ex1')">{{icon "copy"}}</button></div>
  <p class="muted" style="font-size:12.5px;margin:.7rem 0 .6rem">Insert a row (service key):</p>
  <div class="cs"><span class="tag">POST</span><code id="ex2">curl -X POST '{{.Base}}/your_table' -H "apikey: {{.Service}}" -H "Content-Type: application/json" -d '{"col":"val"}'</code><button class="copy" onclick="cp('ex2')">{{icon "copy"}}</button></div>
  <p class="muted" style="font-size:11.5px;margin-top:.7rem">Full method reference with example responses is on the project <a href="/p/{{.Slug}}/docs" style="color:hsl(var(--primary))">Docs</a> page.</p>
</div>
<div class="card">
  <div style="display:flex;align-items:center;gap:.6rem"><h2>GraphQL</h2><span class="badge active">pg_graphql</span></div>
  <p class="muted" style="font-size:12.5px;margin:.3rem 0 .6rem">A GraphQL API reflecting your whole schema - same anon/service keys. Endpoint: <code>{{.GraphQL}}</code></p>
  <div class="cs"><span class="tag">POST</span><code id="gq">curl -X POST '{{.GraphQL}}' -H "apikey: {{.Anon}}" -H "Content-Type: application/json" -d '{"query":"{ customersCollection { edges { node { name mrr } } } }"}'</code><button class="copy" onclick="cp('gq')">{{icon "copy"}}</button></div>
  <p class="muted" style="font-size:11.5px;margin-top:.6rem">pg_graphql names collections <code>&lt;table&gt;Collection</code> and exposes filters, ordering, pagination and mutations.</p>
</div>
{{end}}
` + copyJS

const peopleBody = `
<div class="pagehead"><h1>Team</h1><p>Invite people, assign roles, or remove them from your ForgeBase organization.</p></div>
<div class="grid" style="grid-template-columns:repeat(auto-fill,minmax(150px,1fr));margin-bottom:1rem">
  <div class="card stat"><div class="k">Members</div><div class="v">{{.Total}}</div></div>
  <div class="card stat"><div class="k">Owners</div><div class="v">{{.Owners}}</div></div>
  <div class="card stat"><div class="k">Admins</div><div class="v">{{.Admins}}</div></div>
  <div class="card stat"><div class="k">Members</div><div class="v">{{.Mem}}</div></div>
</div>
{{if .Invite}}<div class="flash" style="margin-bottom:1rem;display:flex;align-items:center;gap:.6rem;flex-wrap:wrap">
  <span>Invite link (valid 7 days) - send it to your new member:</span>
  <code id="invlink" style="color:hsl(var(--primary));flex:1;min-width:200px;overflow-x:auto;white-space:nowrap">{{.Invite}}</code>
  <button class="btn btn-primary btn-sm" onclick="navigator.clipboard.writeText(document.getElementById('invlink').textContent);this.textContent='Copied!'">Copy link</button>
</div>{{end}}
<form class="card" method="post" action="/people/add" style="display:flex;gap:.6rem;align-items:center;margin-bottom:1.2rem;flex-wrap:wrap">
  <input type="text" name="name" placeholder="Full name" required style="flex:1;min-width:140px">
  <input type="email" name="email" placeholder="email@company.com" required style="flex:1;min-width:180px">
  <select name="role" style="width:auto">{{range .Roles}}<option value="{{.}}">{{.}}</option>{{end}}</select>
  <button class="btn btn-primary" type="submit">{{icon "plus"}} Add member</button>
</form>
<div class="card">
  <h2>Members</h2>
  <div class="tblwrap" style="margin-top:.8rem">
    <table class="data"><thead><tr><th>Name</th><th>Email</th><th>Role</th><th>Joined</th><th></th></tr></thead>
    <tbody>
    {{range .Members}}
      <tr>
        <td><b>{{.Name}}</b></td><td>{{.Email}}</td>
        <td><form method="post" action="/people/role" style="display:inline"><input type="hidden" name="id" value="{{.ID}}">
          <select name="role" onchange="this.form.submit()" style="width:auto;padding:.3rem .5rem;font-size:12px">
            {{$r := .Role}}{{range $.Roles}}<option value="{{.}}" {{if eq . $r}}selected{{end}}>{{.}}</option>{{end}}
          </select></form></td>
        <td class="muted">{{.Created}}</td>
        <td style="text-align:right"><form method="post" action="/people/remove" onsubmit="return confirm('Remove {{.Email}}?')" style="display:inline">
          <input type="hidden" name="id" value="{{.ID}}"><button class="copy" style="color:hsl(var(--destructive))" title="remove">{{icon "trash"}}</button></form></td>
      </tr>
    {{end}}
    </tbody></table>
  </div>
</div>`

const storageBody = `
<div class="pagehead"><h1>Storage</h1><p>File buckets for <b>{{.Slug}}</b> with public and signed URLs, served over HTTPS.</p></div>
<div class="grid g3" style="margin-bottom:1rem">
  <div class="card stat"><div class="k">Buckets</div><div class="v">{{.NBuckets}}</div></div>
  <div class="card stat"><div class="k">Objects</div><div class="v">{{.TotObjects}}</div></div>
  <div class="card stat"><div class="k">Total size</div><div class="v">{{.TotSize}}</div></div>
</div>
<div class="split" style="display:flex;gap:1rem;align-items:flex-start">
  <div style="width:240px;flex-shrink:0">
    <div class="card" style="padding:.7rem;margin-bottom:.8rem">
      <div class="label" style="padding:.2rem .4rem .4rem">Buckets</div>
      {{if not .Buckets}}<div class="muted" style="font-size:12px;padding:.3rem .4rem">No buckets yet.</div>{{end}}
      {{range .Buckets}}<a class="navi {{if eq .Name $.Sel}}active{{end}}" href="/p/{{$.Slug}}/storage?b={{.Name}}" style="font-size:12.5px">{{icon "folder"}} <span style="flex:1">{{.Name}}</span>{{if .Public}}<span class="badge active" style="font-size:8px">public</span>{{end}}</a>{{end}}
    </div>
    <form class="card" method="post" action="/p/{{.Slug}}/storage/bucket" style="padding:.8rem">
      <div class="label" style="margin-bottom:.5rem">New bucket</div>
      <input type="text" name="name" placeholder="avatars" required style="margin-bottom:.5rem">
      <label style="display:flex;align-items:center;gap:.4rem;font-size:12.5px;margin-bottom:.5rem;cursor:pointer"><input type="checkbox" name="public" style="width:auto"> Public bucket</label>
      <input type="number" name="max_size_mb" min="0" placeholder="max MB per file (0 = no limit)" style="margin-bottom:.5rem;width:100%">
      <input type="text" name="allowed_mime" placeholder="allowed types, e.g. image/,application/pdf" style="margin-bottom:.6rem;width:100%">
      <button class="btn btn-primary btn-sm" type="submit" style="width:100%">Create bucket</button>
    </form>
  </div>
  <div style="flex:1;min-width:0">
    {{if not .Sel}}
    <div class="card" style="text-align:center;padding:3rem;color:hsl(var(--muted-fg))">Select or create a bucket to upload files.</div>
    {{else}}
    <div class="card" style="margin-bottom:1rem">
      <div style="display:flex;align-items:center;gap:.6rem;flex-wrap:wrap"><h2>{{.Sel}}</h2>{{if .SelPublic}}<span class="badge active">public</span>{{else}}<span class="badge paused">private</span>{{end}}{{if .SelMaxMB}}<span class="badge" style="text-transform:none">max {{.SelMaxMB}} MB/file</span>{{end}}{{if .SelMime}}<span class="badge" style="text-transform:none">types: {{.SelMime}}</span>{{end}}
        <div class="spacer"></div>
        <form method="post" action="/p/{{.Slug}}/storage/bucket-delete" onsubmit="return confirm('Delete bucket {{.Sel}} and all its files?')"><input type="hidden" name="bucket" value="{{.Sel}}"><button class="btn btn-danger btn-sm">Delete bucket</button></form>
      </div>
      <form method="post" action="/p/{{.Slug}}/storage/upload" enctype="multipart/form-data" style="display:flex;gap:.5rem;margin-top:.9rem;align-items:center">
        <input type="hidden" name="bucket" value="{{.Sel}}">
        <input type="file" name="file" required style="flex:1;padding:.45rem">
        <button class="btn btn-primary btn-sm" type="submit">{{icon "upload"}} Upload</button>
      </form>
    </div>
    <div class="card">
      <h2>Objects</h2>
      {{if not .Objects}}<p class="muted" style="margin-top:.5rem">Empty. Upload a file above.</p>
      {{else}}
      <div class="tblwrap" style="margin-top:.8rem"><table class="data">
        <thead><tr><th>Path</th><th>Size</th><th>Type</th><th>Uploaded</th><th></th></tr></thead>
        <tbody>{{range .Objects}}<tr>
          <td><a href="{{.URL}}" target="_blank" style="color:hsl(var(--primary))">{{.Path}}</a></td>
          <td class="muted">{{.Size}}</td><td class="muted">{{.Mime}}</td><td class="muted">{{.Created}}</td>
          <td style="text-align:right;white-space:nowrap">
            <button class="copy" title="copy URL" onclick="navigator.clipboard.writeText('{{.URL}}');this.style.color='hsl(var(--primary))'">{{icon "copy"}}</button>
            <form method="post" action="/p/{{$.Slug}}/storage/delete" style="display:inline" onsubmit="return confirm('Delete this file?')"><input type="hidden" name="bucket" value="{{$.Sel}}"><input type="hidden" name="path" value="{{.Path}}"><button class="copy" style="color:hsl(var(--destructive))">{{icon "trash"}}</button></form>
          </td></tr>{{end}}</tbody>
      </table></div>
      <p class="muted" style="font-size:11px;margin-top:.6rem">{{if .SelPublic}}Public URLs are permanent.{{else}}Signed URLs expire in 24h - regenerate by reloading this page.{{end}}</p>
      {{end}}
    </div>
    {{end}}
  </div>
</div>`

const authPageBody = `
<div class="pagehead"><h1>Auth</h1><p>Email + password authentication for <b>{{.Slug}}</b>'s end users, with JWTs your Data API trusts.</p></div>
{{if not .Enabled}}
<div class="card" style="text-align:center;padding:2.4rem">
  <h2>Enable Auth</h2>
  <p class="muted" style="font-size:13px;margin:.5rem auto .9rem;max-width:480px">Creates an <code>auth.users</code> table and an <code>authenticated</code> role, and exposes signup/login endpoints at <code>{{.Base}}</code>. Tokens are signed with this project's JWT secret, so PostgREST enforces them automatically.</p>
  <form method="post" action="/p/{{.Slug}}/auth-enable"><button class="btn btn-primary" type="submit">{{icon "shield"}} Enable Auth</button></form>
</div>
{{else}}
<div class="card" style="margin-bottom:1rem">
  <div style="display:flex;align-items:center;gap:.6rem"><h2>Endpoints</h2><span class="badge active">live</span><div class="spacer"></div>
    <form method="post" action="/p/{{.Slug}}/auth-disable"><button class="btn btn-ghost btn-sm">Disable</button></form></div>
  <div class="cs"><span class="tag">Sign up</span><code id="a1">curl -X POST '{{.Base}}/signup' -H "Content-Type: application/json" -d '{"email":"user@example.com","password":"secret123"}'</code><button class="copy" onclick="cp('a1')">{{icon "copy"}}</button></div>
  <div class="cs"><span class="tag">Log in</span><code id="a2">curl -X POST '{{.Base}}/token?grant_type=password' -H "Content-Type: application/json" -d '{"email":"user@example.com","password":"secret123"}'</code><button class="copy" onclick="cp('a2')">{{icon "copy"}}</button></div>
  <div class="cs"><span class="tag">Get user</span><code id="a3">curl '{{.Base}}/user' -H "Authorization: Bearer &lt;access_token&gt;"</code><button class="copy" onclick="cp('a3')">{{icon "copy"}}</button></div>
  <p class="muted" style="font-size:11.5px;margin-top:.7rem">Login returns an <code>access_token</code> (role <code>authenticated</code>). Send it to the Data API as <code>Authorization: Bearer</code> to act as that user under your RLS policies.</p>
</div>
<div class="card" style="margin-bottom:1rem">
  <h2>Social sign-in</h2>
  <p class="muted" style="font-size:12.5px;margin:.3rem 0 .6rem">Let users sign in with Google, GitHub, GitLab or Discord. Register this callback URL with the provider, then start the flow at <code>{{.Base}}/authorize?provider=google&redirect_to=YOUR_APP</code>.</p>
  <div class="cs" style="margin-bottom:.9rem"><span class="tag">Callback</span><code id="cb">{{.Callback}}</code><button class="copy" onclick="cp('cb')">{{icon "copy"}}</button></div>
  <div class="grid g2">
  {{range .Providers}}
    <form method="post" action="/p/{{$.Slug}}/oauth-save" style="border:1px solid hsl(var(--border));border-radius:.7rem;padding:.9rem">
      <input type="hidden" name="provider" value="{{.Name}}">
      <div style="display:flex;align-items:center;gap:.5rem;margin-bottom:.7rem"><b style="text-transform:capitalize;flex:1">{{.Name}}</b>
        {{if .Enabled}}<span class="badge active">on</span>{{end}}
        <label style="display:flex;align-items:center;gap:.4rem;font-size:12px;cursor:pointer"><input type="checkbox" name="enabled" {{if .Enabled}}checked{{end}}> Enabled</label></div>
      <label class="fld" style="margin-bottom:.5rem"><span class="lt" style="font-size:11px">Client ID</span><input type="text" name="client_id" value="{{.ClientID}}" placeholder="client id"></label>
      <label class="fld" style="margin-bottom:.6rem"><span class="lt" style="font-size:11px">Client secret</span><input type="password" name="client_secret" placeholder="{{if .ClientID}}leave blank to keep{{else}}client secret{{end}}"></label>
      <button class="btn btn-ghost btn-sm" type="submit">Save {{.Name}}</button>
    </form>
  {{end}}
  </div>
</div>
<div class="card">
  <div style="display:flex;align-items:center;gap:.6rem"><h2>Users <span class="muted" style="font-family:var(--serif);font-size:15px">· {{.Count}}</span></h2>
    <div class="spacer"></div>
    <button class="btn btn-ghost btn-sm" onclick="document.getElementById('adduser').style.display='flex'">{{icon "plus"}} Add user</button></div>
  <form id="adduser" method="post" action="/p/{{.Slug}}/auth-user-add" style="display:none;gap:.5rem;align-items:center;margin-top:.8rem">
    <input type="email" name="email" placeholder="user@example.com" required style="flex:1">
    <input type="text" name="password" placeholder="temp password (6+)" required style="width:180px">
    <button class="btn btn-primary btn-sm" type="submit">Create</button>
    <button class="btn btn-ghost btn-sm" type="button" onclick="document.getElementById('adduser').style.display='none'">Cancel</button>
  </form>
  {{if not .Users}}<p class="muted" style="margin-top:.5rem">No users yet. They appear here after signing up, or add one above.</p>
  {{else}}
  <div class="tblwrap" style="margin-top:.8rem"><table class="data">
    <thead><tr><th>Email</th><th>Signed up</th><th>Last sign-in</th><th></th></tr></thead>
    <tbody>{{range .Users}}<tr>
      <td><b>{{.Email}}</b><br><code class="muted" style="font-size:10px">{{.ID}}</code></td>
      <td class="muted">{{.Created}}</td><td class="muted">{{.LastSeen}}</td>
      <td>
        <div style="display:flex;gap:.5rem;justify-content:flex-end;align-items:center">
          <form method="post" action="/p/{{$.Slug}}/auth-user-password" style="margin:0" onsubmit="this.password.value=prompt('New password for {{.Email}} (6+ chars):')||'';return this.password.value.length>=6">
            <input type="hidden" name="id" value="{{.ID}}"><input type="hidden" name="password">
            <button class="btn btn-ghost btn-sm">Reset password</button></form>
          <form method="post" action="/p/{{$.Slug}}/auth-user-delete" style="margin:0;display:flex" onsubmit="return confirm('Delete {{.Email}}?')">
            <input type="hidden" name="id" value="{{.ID}}"><button class="copy" style="color:hsl(var(--destructive))" title="delete">{{icon "trash"}}</button></form>
        </div>
      </td></tr>{{end}}</tbody>
  </table></div>{{end}}
</div>
{{end}}
` + copyJS

const docsBody = `
<div class="pagehead"><h1>Docs</h1><p>Everything you need to build against <b>{{.Slug}}</b> - real URLs, what to send, and example responses.</p></div>

<div class="card" style="margin-bottom:1rem">
  <h2>Connect directly (Postgres)</h2>
  <p class="muted" style="font-size:12.5px;margin:.3rem 0 .6rem">Any Postgres client, ORM, or driver. <b>Direct</b> for Prisma/sessions, <b>Pooled</b> for serverless.</p>
  <div class="cs"><span class="tag">Direct TLS</span><code id="d1">{{.Direct}}</code><button class="copy" onclick="cp('d1')">{{icon "copy"}}</button></div>
  <div class="cs"><span class="tag">Pooled</span><code id="d2">{{.Pooled}}</code><button class="copy" onclick="cp('d2')">{{icon "copy"}}</button></div>
</div>

<div class="card" style="margin-bottom:1rem">
  <div style="display:flex;align-items:center;gap:.6rem"><h2>Data API (REST)</h2>{{if .APIOn}}<span class="badge active">enabled</span>{{else}}<span class="badge paused">disabled</span> <a href="/p/{{.Slug}}/api" style="font-size:12px;color:hsl(var(--primary))">enable</a>{{end}}</div>
  <p class="muted" style="font-size:12.5px;margin:.3rem 0 .8rem">Auto-generated from your tables. Base URL <code>{{.Rest}}</code>. Every request needs an <code>apikey</code> header (anon key for reads, service key for writes).</p>

  <div class="doc-op"><div class="doc-h"><span class="badge active" style="text-transform:none">GET</span> Read rows - filter, order, paginate</div>
    <div class="label">Request</div>
    <pre class="doc-code" id="r1">curl '{{.Rest}}/products?select=name,price&price=gt.10&order=price.desc&limit=20' \
  -H "apikey: {{if .Anon}}{{.Anon}}{{else}}YOUR_ANON_KEY{{end}}"</pre>
    <div class="label">Response  <span class="muted">200 OK</span></div>
    <pre class="doc-resp">[
  { "name": "Gadget", "price": 49.90 },
  { "name": "Widget", "price": 19.99 }
]</pre>
  </div>

  <div class="doc-op"><div class="doc-h"><span class="badge active" style="text-transform:none">POST</span> Insert a row</div>
    <div class="label">Request - send JSON, ask for the created row back</div>
    <pre class="doc-code" id="r2">curl -X POST '{{.Rest}}/products' \
  -H "apikey: YOUR_SERVICE_KEY" \
  -H "Content-Type: application/json" \
  -H "Prefer: return=representation" \
  -d '{"name":"New","price":9.99}'</pre>
    <div class="label">Response  <span class="muted">201 Created</span></div>
    <pre class="doc-resp">[
  { "id": 3, "name": "New", "price": 9.99 }
]</pre>
  </div>

  <div class="doc-op"><div class="doc-h"><span class="badge active" style="text-transform:none">PATCH</span> Update rows matched by a filter</div>
    <div class="label">Request</div>
    <pre class="doc-code" id="r3">curl -X PATCH '{{.Rest}}/products?id=eq.3' \
  -H "apikey: YOUR_SERVICE_KEY" \
  -H "Content-Type: application/json" \
  -H "Prefer: return=representation" \
  -d '{"price":12.50}'</pre>
    <div class="label">Response  <span class="muted">200 OK</span></div>
    <pre class="doc-resp">[
  { "id": 3, "name": "New", "price": 12.50 }
]</pre>
  </div>

  <div class="doc-op"><div class="doc-h"><span class="badge paused" style="text-transform:none">DELETE</span> Delete rows matched by a filter</div>
    <div class="label">Request</div>
    <pre class="doc-code" id="r4">curl -X DELETE '{{.Rest}}/products?id=eq.3' \
  -H "apikey: YOUR_SERVICE_KEY"</pre>
    <div class="label">Response  <span class="muted">204 No Content</span></div>
    <pre class="doc-resp">(empty body)</pre>
  </div>

  <p class="muted" style="font-size:11.5px;margin-top:.4rem">Filter operators: <code>eq neq gt gte lt lte like ilike in</code> - e.g. <code>?status=in.(active,trial)</code>. Pagination: <code>?limit=20&offset=40</code>. Your real keys are on the <a href="/p/{{.Slug}}/api" style="color:hsl(var(--primary))">Data API</a> page.</p>
</div>

<div class="card" style="margin-bottom:1rem">
  <div style="display:flex;align-items:center;gap:.6rem"><h2>Auth</h2>{{if .AuthOn}}<span class="badge active">enabled</span>{{else}}<span class="badge paused">disabled</span> <a href="/p/{{.Slug}}/auth" style="font-size:12px;color:hsl(var(--primary))">enable</a>{{end}}</div>
  <p class="muted" style="font-size:12.5px;margin:.3rem 0 .8rem">Email + password for your app's end users. Base <code>{{.AuthURL}}</code>.</p>

  <div class="doc-op"><div class="doc-h"><span class="badge active" style="text-transform:none">POST</span> Sign up</div>
    <div class="label">Request - send email + password (JSON)</div>
    <pre class="doc-code" id="au1">curl -X POST '{{.AuthURL}}/signup' \
  -H "Content-Type: application/json" \
  -d '{"email":"user@example.com","password":"secret123"}'</pre>
    <div class="label">Response  <span class="muted">200 OK</span></div>
    <pre class="doc-resp">{
  "access_token": "eyJhbGciOiJIUzI1Ni...",
  "token_type": "bearer",
  "user": { "id": "7e00480e-...", "email": "user@example.com" }
}</pre>
  </div>

  <div class="doc-op"><div class="doc-h"><span class="badge active" style="text-transform:none">POST</span> Log in</div>
    <div class="label">Request</div>
    <pre class="doc-code" id="au2">curl -X POST '{{.AuthURL}}/token?grant_type=password' \
  -H "Content-Type: application/json" \
  -d '{"email":"user@example.com","password":"secret123"}'</pre>
    <div class="label">Response  <span class="muted">200 OK - same shape as signup</span></div>
    <pre class="doc-resp">{ "access_token": "eyJ...", "token_type": "bearer", "user": { ... } }</pre>
  </div>

  <div class="doc-op"><div class="doc-h"><span class="badge active" style="text-transform:none">GET</span> Current user</div>
    <div class="label">Request - send the token from login</div>
    <pre class="doc-code" id="au3">curl '{{.AuthURL}}/user' \
  -H "Authorization: Bearer YOUR_ACCESS_TOKEN"</pre>
    <div class="label">Response  <span class="muted">200 OK</span></div>
    <pre class="doc-resp">{ "id": "7e00480e-...", "email": "user@example.com", "role": "authenticated" }</pre>
  </div>
  <p class="muted" style="font-size:11.5px;margin-top:.4rem">Send that <code>access_token</code> to the Data API as <code>apikey</code> (or <code>Authorization: Bearer</code>) to read/write as that signed-in user under your RLS policies.</p>

  <div class="doc-op" style="margin-top:.6rem"><div class="doc-h"><span class="badge active" style="text-transform:none">GET</span> Social sign-in (Google / GitHub)</div>
    <div class="label">Send the user here; on success they return to your app with a token in the URL fragment</div>
    <pre class="doc-code" id="oa">{{.AuthURL}}/authorize?provider=google&redirect_to=https://your-app.com/callback</pre>
    <div class="label">Return</div><pre class="doc-resp">https://your-app.com/callback#access_token=eyJ...&token_type=bearer</pre>
  </div>
</div>

<div class="card" style="margin-bottom:1rem">
  <div style="display:flex;align-items:center;gap:.6rem"><h2>GraphQL</h2>{{if .APIOn}}<span class="badge active">enabled</span>{{else}}<span class="badge paused">disabled</span>{{end}}</div>
  <p class="muted" style="font-size:12.5px;margin:.3rem 0 .8rem">A GraphQL API over your whole schema (pg_graphql), same anon/service keys. Endpoint <code>{{.GraphQL}}</code>.</p>
  <div class="doc-op"><div class="doc-h"><span class="badge active" style="text-transform:none">POST</span> Query</div>
    <div class="label">Request</div>
    <pre class="doc-code" id="gq1">curl -X POST '{{.GraphQL}}' \
  -H "apikey: {{if .Anon}}{{.Anon}}{{else}}YOUR_ANON_KEY{{end}}" \
  -H "Content-Type: application/json" \
  -d '{"query":"{ customersCollection(first: 5) { edges { node { name mrr } } } }"}'</pre>
    <div class="label">Response  <span class="muted">200 OK</span></div>
    <pre class="doc-resp">{ "data": { "customersCollection": { "edges": [
  { "node": { "name": "Acme", "mrr": 480 } }
] } } }</pre>
  </div>
  <p class="muted" style="font-size:11.5px;margin-top:.4rem">Collections are named <code>&lt;table&gt;Collection</code>; filtering, ordering, pagination and mutations are all supported.</p>
</div>

<div class="card" style="margin-bottom:1rem">
  <div style="display:flex;align-items:center;gap:.6rem"><h2>Realtime</h2>{{if .RTOn}}<span class="badge active">enabled</span>{{else}}<span class="badge paused">disabled</span> <a href="/p/{{.Slug}}/realtime" style="font-size:12px;color:hsl(var(--primary))">enable</a>{{end}}</div>
  <p class="muted" style="font-size:12.5px;margin:.3rem 0 .8rem">Subscribe to row changes over a WebSocket. Endpoint <code>{{.Realtime}}</code>.</p>
  <div class="doc-op"><div class="doc-h"><span class="badge active" style="text-transform:none">WS</span> Subscribe (browser)</div>
    <pre class="doc-code" id="rt1">const ws = new WebSocket('{{.Realtime}}');
ws.onmessage = (e) => console.log(JSON.parse(e.data));</pre>
    <div class="label">Messages</div>
    <pre class="doc-resp">{ "type": "INSERT", "table": "customers", "record": { "id": 8, "name": "New" } }</pre>
  </div>
</div>

<div class="card" style="margin-bottom:1rem">
  <div style="display:flex;align-items:center"><h2>Edge Functions</h2><div class="spacer"></div><span class="label">{{len .Fns}} deployed</span></div>
  <p class="muted" style="font-size:12.5px;margin:.3rem 0 .8rem">Deno functions invoked over HTTPS at <code>{{.FuncBase}}/&lt;name&gt;</code>. Deploy on the <a href="/p/{{.Slug}}/functions" style="color:hsl(var(--primary))">Edge Functions</a> page.</p>
  <div class="doc-op"><div class="doc-h"><span class="badge active" style="text-transform:none">ANY</span> Invoke</div>
    <pre class="doc-code" id="ef1">curl '{{.FuncBase}}/{{if .Fns}}{{index .Fns 0}}{{else}}hello{{end}}?name=Saurabh'</pre>
    <div class="label">Response  <span class="muted">whatever your function returns</span></div>
    <pre class="doc-resp">{ "hello": "Saurabh" }</pre>
  </div>
  {{if .Fns}}<div class="label" style="margin-top:.5rem">Your functions</div>
  <div style="display:flex;gap:.4rem;flex-wrap:wrap;margin-top:.3rem">{{range .Fns}}<code style="background:hsl(var(--bg));border:1px solid hsl(var(--border));border-radius:.4rem;padding:.2rem .5rem;font-size:11px">{{$.FuncBase}}/{{.}}</code>{{end}}</div>{{end}}
</div>

<div class="card" style="margin-bottom:1rem">
  <h2>Webhooks</h2>
  <p class="muted" style="font-size:12.5px;margin:.3rem 0">Configure on the <a href="/p/{{.Slug}}/webhooks" style="color:hsl(var(--primary))">Webhooks</a> page - ForgeBase POSTs a JSON payload to your URL on every insert/update/delete: <code>{"type":"INSERT","table":"...","record":{...}}</code>.</p>
</div>

<div class="card">
  <h2>Storage</h2>
  <p class="muted" style="font-size:12.5px;margin:.3rem 0 .8rem">Create buckets and upload on the <a href="/p/{{.Slug}}/storage" style="color:hsl(var(--primary))">Storage</a> page. Files serve over HTTPS.</p>
  <div class="doc-op"><div class="doc-h"><span class="badge active" style="text-transform:none">GET</span> Public file</div>
    <pre class="doc-code" id="s1">curl '{{.StoreURL}}/public/&lt;bucket&gt;/&lt;path&gt;'</pre>
    <div class="label">Response</div><pre class="doc-resp">(the file bytes, with its Content-Type)</pre>
  </div>
  <div class="doc-op"><div class="doc-h"><span class="badge paused" style="text-transform:none">GET</span> Private file (signed URL)</div>
    <pre class="doc-code" id="s2">curl '{{.StoreURL}}/sign/&lt;bucket&gt;/&lt;path&gt;?token=&lt;exp&gt;.&lt;sig&gt;'</pre>
    <div class="label">Response</div><pre class="doc-resp">200 with file bytes  ·  403 if the token is expired or tampered</pre>
  </div>
</div>
` + docCSS + copyJS

const docCSS = `<style>
.doc-op{border:1px solid hsl(var(--border));border-radius:.7rem;padding:.8rem .9rem;margin-bottom:.8rem}
.doc-h{font-weight:600;font-size:13px;margin-bottom:.5rem;display:flex;align-items:center;gap:.5rem}
.doc-code{background:hsl(var(--fg)/.05);border:1px solid hsl(var(--border));border-radius:.5rem;padding:.6rem .7rem;font-family:var(--mono);font-size:11.5px;white-space:pre-wrap;word-break:break-all;margin:.2rem 0 .6rem;overflow-x:auto}
.doc-resp{background:hsl(var(--primary)/.06);border:1px solid hsl(var(--primary)/.18);border-radius:.5rem;padding:.6rem .7rem;font-family:var(--mono);font-size:11.5px;white-space:pre-wrap;margin:.2rem 0 0}
</style>`

const guideBody = `
<div class="pagehead"><h1>Guide</h1><p>How to run your platform - creating projects, managing your account, and your team.</p></div>
<div class="grid g2">
  <div class="card">
    <h2>Create a project</h2>
    <ol class="muted" style="font-size:13px;line-height:1.8;padding-left:1.1rem;margin:.5rem 0 0">
      <li>Go to <a href="/" style="color:hsl(var(--primary))">Projects</a> and type a name (a-z, 0-9, _).</li>
      <li>Click <b>New Project</b> - a dedicated, isolated Postgres database is ready in under a second.</li>
      <li>Open it and copy a connection string from <b>Overview</b>.</li>
    </ol>
  </div>
  <div class="card">
    <h2>Everything a project can do</h2>
    <ul class="muted" style="font-size:13px;line-height:1.75;padding-left:1.1rem;margin:.5rem 0 0">
      <li><b>Tables / SQL Editor</b> - schema, edit data, CSV import, saved queries.</li>
      <li><b>Data API</b> - instant REST (PostgREST) with anon/service keys.</li>
      <li><b>GraphQL</b> - a GraphQL API over your schema (pg_graphql).</li>
      <li><b>Auth</b> - email+password + Google/GitHub OAuth for your app's users.</li>
      <li><b>Storage</b> - file buckets with public and signed URLs.</li>
      <li><b>Realtime</b> - WebSocket streams of row changes.</li>
      <li><b>Webhooks</b> - POST to a URL on every insert/update/delete.</li>
      <li><b>Edge Functions</b> - deploy Deno functions, invoked over HTTPS.</li>
      <li><b>Branches</b> - instant database copies for staging or tests.</li>
      <li><b>Monitoring</b> - 7-day charts, cache hit, top queries.</li>
      <li><b>Backup &amp; Restore</b> - nightly dumps + WAL archive, off-box S3, one-click restore.</li>
      <li><b>Logs</b> - per-project audit trail + live sessions; platform-wide Audit log.</li>
      <li><b>Docs</b> (inside each project) - copy-paste examples for every endpoint.</li>
    </ul>
  </div>
  <div class="card">
    <h2>Your account</h2>
    <ul class="muted" style="font-size:13px;line-height:1.8;padding-left:1.1rem;margin:.5rem 0 0">
      <li><a href="/account" style="color:hsl(var(--primary))">Account</a> - set your name, change email and password.</li>
      <li>Create <b>personal API keys</b> for programmatic access.</li>
    </ul>
  </div>
  <div class="card">
    <h2>Your team</h2>
    <ul class="muted" style="font-size:13px;line-height:1.8;padding-left:1.1rem;margin:.5rem 0 0">
      <li>Registration is <b>invite-only</b> - only the first account self-registers (owner).</li>
      <li>Add teammates from <a href="/people" style="color:hsl(var(--primary))">Team</a>; they get a temporary password and the <b>member</b> role by default.</li>
      <li>Roles: <b>owner</b> (full control), <b>admin</b>, <b>member</b>.</li>
    </ul>
  </div>
</div>`

const realtimeBody = `
<div class="pagehead"><h1>Realtime</h1><p>Stream <b>{{.Slug}}</b>'s row changes (insert/update/delete) to browsers over WebSockets.</p></div>
{{if not .Enabled}}
<div class="card" style="text-align:center;padding:2.4rem">
  <h2>Enable Realtime</h2>
  <p class="muted" style="font-size:13px;margin:.5rem auto .9rem;max-width:500px">Adds a change-capture trigger to every current table and opens a WebSocket at <code>{{.WS}}</code>. Each insert/update/delete is pushed to connected clients as JSON.</p>
  <form method="post" action="/p/{{.Slug}}/realtime-enable"><button class="btn btn-primary" type="submit">{{icon "bolt"}} Enable Realtime</button></form>
</div>
{{else}}
<div class="grid g3" style="margin-bottom:1rem">
  <div class="card stat"><div class="k">Status</div><div class="v" style="font-size:20px">Live</div></div>
  <div class="card stat"><div class="k">Tables watched</div><div class="v">{{.Tables}}</div></div>
  <div class="card stat"><div class="k">Connected clients</div><div class="v">{{.Clients}}</div></div>
</div>
<div class="card" style="margin-bottom:1rem">
  <div style="display:flex;align-items:center;gap:.6rem"><h2>WebSocket endpoint</h2><span class="badge active">live</span><div class="spacer"></div>
    <form method="post" action="/p/{{.Slug}}/realtime-enable"><button class="btn btn-ghost btn-sm" title="re-attach triggers to any new tables">Re-scan tables</button></form>
    <form method="post" action="/p/{{.Slug}}/realtime-disable"><button class="btn btn-ghost btn-sm">Disable</button></form></div>
  <div class="cs"><span class="tag">WS URL</span><code id="ws">{{.WS}}</code><button class="copy" onclick="cp('ws')">{{icon "copy"}}</button></div>
  <div class="cs"><span class="tag">Browser</span><code id="js">const ws = new WebSocket('{{.WS}}'); ws.onmessage = e => console.log(JSON.parse(e.data));</code><button class="copy" onclick="cp('js')">{{icon "copy"}}</button></div>
  <p class="muted" style="font-size:11.5px;margin-top:.6rem">Messages look like <code>{"type":"INSERT","table":"customers","record":{...}}</code>. New tables created later need a "Re-scan tables". Filter with <code>?table=customers&amp;event=INSERT&amp;filter=id=eq.5</code>.</p>
</div>
<div class="card" style="margin-bottom:1rem">
  <h2>Access</h2>
  <form method="post" action="/p/{{.Slug}}/realtime-auth" style="margin-top:.5rem">
    <label style="display:flex;align-items:center;gap:.45rem;font-size:12.5px;cursor:pointer"><input type="checkbox" name="require_auth" {{if .RequireAuth}}checked{{end}} onchange="this.form.submit()"> Require an authenticated key (block the public <code>anon</code> key)</label>
  </form>
  <p class="muted" style="font-size:11.5px;margin-top:.5rem">On (recommended): only <code>authenticated</code> and <code>service_role</code> keys can subscribe - the stream is not per-row RLS filtered, so the public <code>anon</code> key would otherwise see every change. Turn off only if your app subscribes with the anon key and the data is not sensitive.</p>
</div>
<div class="card">
  <div style="display:flex;align-items:center"><h2>Live tester</h2><div class="spacer"></div><span id="rtstatus" class="badge paused">connecting…</span></div>
  <p class="muted" style="font-size:12.5px;margin:.3rem 0 .6rem">This page is subscribed below. Insert a row (SQL editor or Data API) and watch it arrive.</p>
  <div id="rtlog" class="mono" style="background:hsl(var(--bg));border:1px solid hsl(var(--border));border-radius:.6rem;padding:.7rem;height:200px;overflow-y:auto;font-size:11px"></div>
</div>
<script>
(function(){
  var log=document.getElementById('rtlog'), st=document.getElementById('rtstatus');
  try{
    var ws=new WebSocket('{{.WS}}');
    ws.onopen=function(){st.textContent='connected';st.className='badge active';};
    ws.onclose=function(){st.textContent='disconnected';st.className='badge paused';};
    ws.onmessage=function(e){var p=document.createElement('div');p.textContent=new Date().toLocaleTimeString()+'  '+e.data;log.prepend(p);};
  }catch(err){st.textContent='error';}
})();
</script>
{{end}}
` + copyJS

const webhooksBody = `
<div class="pagehead"><h1>Webhooks</h1><p>POST a JSON payload to any URL when a row changes in <b>{{.Slug}}</b> - trigger workflows, sync systems, send alerts.</p></div>
<form class="card" method="post" action="/p/{{.Slug}}/webhook-create" style="margin-bottom:1.2rem">
  <h2 style="margin-bottom:.8rem">New webhook</h2>
  <div class="grid g2">
    <label class="fld"><span class="lt">Name</span><input type="text" name="name" placeholder="notify-slack" required></label>
    <label class="fld"><span class="lt">Table</span><select name="table"><option value="">All tables</option>{{range .Tables}}<option value="{{.}}">{{.}}</option>{{end}}</select></label>
  </div>
  <label class="fld"><span class="lt">URL</span><input type="url" name="url" placeholder="https://example.com/webhook" required></label>
  <div class="grid g2">
    <label class="fld"><span class="lt">Method</span><select name="method"><option>POST</option><option>PUT</option><option>PATCH</option></select></label>
    <label class="fld"><span class="lt">Custom header (optional)</span><input type="text" name="headers" placeholder="Authorization: Bearer xyz"></label>
  </div>
  <div style="display:flex;gap:1rem;align-items:center;margin-bottom:.9rem">
    <span class="label">Events:</span>
    <label style="display:flex;align-items:center;gap:.3rem;font-size:13px;cursor:pointer"><input type="checkbox" name="ev_INSERT" checked style="width:auto"> Insert</label>
    <label style="display:flex;align-items:center;gap:.3rem;font-size:13px;cursor:pointer"><input type="checkbox" name="ev_UPDATE" checked style="width:auto"> Update</label>
    <label style="display:flex;align-items:center;gap:.3rem;font-size:13px;cursor:pointer"><input type="checkbox" name="ev_DELETE" checked style="width:auto"> Delete</label>
  </div>
  <button class="btn btn-primary" type="submit">{{icon "webhook"}} Create webhook</button>
</form>
<div class="card">
  <h2>Webhooks</h2>
  <p class="muted" style="font-size:12.5px;margin:.3rem 0 0">Payload: <code>{"type":"UPDATE","table":"...","record":{...},"old_record":{...}}</code>, sent as JSON. Each delivery is signed - verify <code>X-ForgeBase-Signature: sha256=HMAC(secret, body)</code>. Failed deliveries retry up to 5 times over ~7 minutes. New tables are covered automatically.</p>
  {{if not .Hooks}}<p class="muted" style="margin-top:.6rem">No webhooks yet.</p>{{else}}
  <div class="tblwrap" style="margin-top:.8rem"><table class="data">
    <thead><tr><th>Name</th><th>URL</th><th>Table</th><th>Events</th><th>Signing secret</th><th></th></tr></thead>
    <tbody>{{range .Hooks}}<tr>
      <td><b>{{.Name}}</b></td><td><code style="font-size:11px">{{.URL}}</code></td>
      <td class="muted">{{.Table}}</td><td class="mono" style="font-size:10.5px">{{.Events}}</td>
      <td><code style="font-size:10.5px">{{.Secret}}</code></td>
      <td style="text-align:right"><form method="post" action="/p/{{$.Slug}}/webhook-delete" onsubmit="return confirm('Delete webhook?')"><input type="hidden" name="id" value="{{.ID}}"><button class="copy" style="color:hsl(var(--destructive))">{{icon "trash"}}</button></form></td>
    </tr>{{end}}</tbody>
  </table></div>{{end}}
</div>
<div class="card" style="margin-top:1rem">
  <h2 style="font-size:16px">Recent deliveries</h2>
  {{if not .Deliveries}}<p class="muted" style="font-size:12px;margin-top:.4rem">No deliveries yet.</p>{{else}}
  <div class="tblwrap" style="margin-top:.5rem"><table class="data"><thead><tr><th>When</th><th>Webhook</th><th>Result</th></tr></thead>
    <tbody>{{range .Deliveries}}<tr><td class="muted" style="font-size:11px;white-space:nowrap">{{.At}}</td><td><code>{{.Name}}</code></td><td>{{if .OK}}<span class="badge active">{{.Status}}</span>{{else}}<span class="badge" style="color:hsl(var(--destructive))">{{.Status}}</span>{{end}}</td></tr>{{end}}</tbody>
  </table></div>{{end}}
</div>`

const edgeBody = `
<div class="pagehead"><h1>Edge Functions</h1><p>Deploy TypeScript/JavaScript functions that run on-demand in a Deno sandbox, invoked over HTTPS.</p></div>
<div class="split" style="display:flex;gap:1rem;align-items:flex-start">
  <div style="width:220px;flex-shrink:0">
    <div class="card" style="padding:.7rem;margin-bottom:.8rem">
      <div class="label" style="padding:.2rem .4rem .4rem">Functions</div>
      {{if not .Fns}}<div class="muted" style="font-size:12px;padding:.3rem .4rem">None yet.</div>{{end}}
      {{range .Fns}}<a class="navi {{if eq .Name $.Edit}}active{{end}}" href="/p/{{$.Slug}}/functions?fn={{.Name}}" style="font-size:12.5px">{{icon "terminal"}} {{.Name}}</a>{{end}}
      <a class="navi" href="/p/{{.Slug}}/functions" style="font-size:12.5px;color:hsl(var(--primary))">{{icon "plus"}} New function</a>
    </div>
  </div>
  <div style="flex:1;min-width:0">
    {{if .Edit}}
    <div class="card" style="margin-bottom:1rem">
      <div style="display:flex;align-items:center;gap:.6rem"><h2>{{.Edit}}</h2><span class="badge active">deployed</span>{{if .VerifyJWT}}<span class="badge active">JWT required</span>{{else}}<span class="badge paused">public</span>{{end}}<div class="spacer"></div>
        <form method="post" action="/p/{{.Slug}}/function-delete" onsubmit="return confirm('Delete {{.Edit}}?')"><input type="hidden" name="name" value="{{.Edit}}"><button class="btn btn-danger btn-sm">Delete</button></form></div>
      <div class="cs" style="margin-top:.7rem"><span class="tag">Invoke</span><code id="iu">{{.Base}}/{{.Edit}}</code><button class="copy" onclick="cp('iu')">{{icon "copy"}}</button></div>
      <div class="cs"><span class="tag">curl</span><code id="ic">curl '{{.Base}}/{{.Edit}}?name=Saurabh'</code><button class="copy" onclick="cp('ic')">{{icon "copy"}}</button></div>
    </div>
    {{end}}
    <form class="card" method="post" action="/p/{{.Slug}}/function-save">
      <div style="display:flex;gap:.6rem;align-items:center;margin-bottom:.7rem">
        <span class="label">Name</span>
        <input type="text" name="name" value="{{.Edit}}" placeholder="hello" pattern="[a-z][a-z0-9_-]{1,40}" {{if .Edit}}readonly{{end}} required style="width:200px">
        <div class="spacer"></div>
        <span class="muted" style="font-size:11.5px">Deno · export default (req: Request) =&gt; Response</span>
      </div>
      <textarea name="code" rows="16" spellcheck="false" style="font-family:var(--mono);font-size:12.5px">{{.Code}}</textarea>
      <label style="display:flex;align-items:center;gap:.45rem;margin:.7rem 0 0;font-size:12.5px">
        <input type="checkbox" name="verify_jwt" {{if .VerifyJWT}}checked{{end}}>
        Require a valid JWT (apikey or Bearer) to invoke <span class="muted">- recommended; unchecked makes the function public to anyone</span>
      </label>
      <button class="btn btn-primary" type="submit" style="margin-top:.7rem">{{icon "bolt"}} Deploy</button>
    </form>
    <div class="card" style="margin-top:1rem">
      <h2 style="font-size:16px">Secrets</h2>
      <p class="muted" style="font-size:12px;margin:.3rem 0 .6rem">Exposed to your functions via <code>Deno.env.get(...)</code>. <code>FORGEBASE_URL</code>, <code>FORGEBASE_ANON_KEY</code>, <code>FORGEBASE_SERVICE_KEY</code> and <code>FORGEBASE_PROJECT</code> are injected automatically - the host environment is never shared.</p>
      {{if .Secrets}}<div style="display:flex;gap:.4rem;flex-wrap:wrap;margin-bottom:.7rem">{{range .Secrets}}<span class="badge" style="text-transform:none">{{.}} <form method="post" action="/p/{{$.Slug}}/edge-secret-delete" style="display:inline" onsubmit="return confirm('Delete {{.}}?')"><input type="hidden" name="name" value="{{.}}"><button class="copy" style="color:hsl(var(--destructive));padding:0 3px">×</button></form></span>{{end}}</div>{{end}}
      <form method="post" action="/p/{{.Slug}}/edge-secret" style="display:flex;gap:.5rem;align-items:flex-end;flex-wrap:wrap">
        <label class="fld" style="margin:0"><span class="lt">Name</span><input type="text" name="name" placeholder="STRIPE_KEY" style="width:150px" required></label>
        <label class="fld" style="margin:0"><span class="lt">Value</span><input type="text" name="value" placeholder="secret value" style="width:200px" required></label>
        <button class="btn btn-ghost btn-sm" type="submit">Add secret</button>
      </form>
    </div>
    <div class="card" style="margin-top:1rem">
      <h2 style="font-size:16px">Recent errors</h2>
      {{if not .Logs}}<p class="muted" style="font-size:12px;margin-top:.4rem">No errors logged.</p>{{else}}
      <div class="tblwrap" style="margin-top:.5rem"><table class="data"><thead><tr><th>When</th><th>Function</th><th>Error</th></tr></thead>
        <tbody>{{range .Logs}}<tr><td class="muted" style="font-size:11px;white-space:nowrap">{{.At}}</td><td><code>{{.Name}}</code></td><td class="muted" style="font-size:11px;white-space:normal;max-width:380px">{{.Error}}</td></tr>{{end}}</tbody>
      </table></div>{{end}}
    </div>
  </div>
</div>
` + copyJS

const systemBody = `
<div class="pagehead"><h1>System</h1><p>Running version, service health, and the resilience model for this ForgeBase deployment.</p></div>
<div class="grid g2" style="margin-bottom:1rem">
  <div class="card">
    <h2>Version</h2>
    <div style="display:flex;gap:2rem;margin-top:.8rem;flex-wrap:wrap">
      <div><div class="label">Release</div><div style="font-family:var(--serif);font-size:18px">v{{.AppVersion}}</div></div>
      <div><div class="label">Commit</div><a href="{{.Commit}}" target="_blank" style="font-family:var(--mono);font-size:16px;color:hsl(var(--primary))">{{.Version}}</a></div>
      <div><div class="label">Built</div><div style="font-size:14px">{{.BuildTime}}</div></div>
    </div>
    <p class="muted" style="font-size:11.5px;margin-top:.7rem">Click the commit to view this exact version on GitHub. Rolling back to an older commit is safe - the schema only ever adds tables/columns, so older binaries run against the current database unchanged.</p>
  </div>
  <div class="card">
    <h2>Database</h2>
    <div style="display:flex;gap:2rem;margin-top:.8rem;flex-wrap:wrap">
      <div><div class="label">Health</div><div style="font-size:16px">{{if .DBOK}}<span class="badge active">healthy</span>{{else}}<span class="badge paused">unreachable</span>{{end}}</div></div>
      <div><div class="label">Postgres</div><div style="font-size:14px">{{.PGVer}}</div></div>
      <div><div class="label">Total size</div><div style="font-size:14px">{{.DBSize}}</div></div>
      <div><div class="label">Active Data APIs</div><div style="font-size:14px">{{.ActiveAPIs}}</div></div>
    </div>
  </div>
</div>
<div class="card" style="margin-bottom:1rem">
  <h2>Software updates</h2>
  {{if not .Checked}}
    <p class="muted" style="font-size:12.5px;margin:.4rem 0 .8rem">Check GitHub for a newer ForgeBase build. Your databases keep serving throughout; only the control plane restarts.</p>
    <a class="btn btn-primary btn-sm" href="/system?check=1">{{icon "restore"}} Check for updates</a>
    <a class="btn btn-ghost btn-sm" href="/changelog">{{icon "sparkle"}} What's New</a>
  {{else if .Upd.Err}}
    <p style="font-size:13px;margin:.4rem 0 .8rem;color:hsl(var(--destructive))">Could not check for updates: {{.Upd.Err}}</p>
    <a class="btn btn-ghost btn-sm" href="/system?check=1">Try again</a>
  {{else if .Upd.Behind}}
    <div style="display:flex;align-items:center;gap:.6rem;flex-wrap:wrap;margin:.4rem 0 .6rem">
      <span class="badge cloning">update available</span>
      <span style="font-size:13px">you're on <b>v{{.Upd.Current}}</b>, latest is <b>v{{.Upd.Latest}}</b></span>
    </div>
    {{if .Upd.Changelog}}
    <div class="label" style="margin:.6rem 0 .2rem">What's new</div>
    {{range .Upd.Changelog}}
      <div style="font-size:13px;font-weight:600;margin:.5rem 0 .2rem">v{{.Version}}</div>
      <ul class="muted" style="font-size:12.5px;line-height:1.6;padding-left:1.1rem;margin:0 0 .5rem">
        {{range .Items}}<li>{{.}}</li>{{end}}
      </ul>
    {{end}}
    {{end}}
    {{if .IsOwner}}
    <form method="post" action="/system/update" onsubmit="return confirm('Update ForgeBase to {{.Upd.Latest}}? The panel will rebuild and restart. It rolls back automatically if the new build is unhealthy.')">
      <button class="btn btn-primary btn-sm">{{icon "bolt"}} Update now</button>
    </form>
    <p class="muted" style="font-size:11.5px;margin:.6rem 0 0">The updater keeps the previous binary and rolls back if the health check fails.</p>
    {{else}}
    <p class="muted" style="font-size:12px;margin:.2rem 0 0">Ask an owner to install this update.</p>
    {{end}}
  {{else}}
    <div style="display:flex;align-items:center;gap:.6rem;margin:.4rem 0 .2rem">
      <span class="badge active">up to date</span>
      <span class="muted" style="font-size:13px">running the latest release (v{{.Upd.Current}}).</span>
    </div>
    <a class="btn btn-ghost btn-sm" href="/changelog" style="margin-top:.6rem">{{icon "sparkle"}} What's New</a>
  {{end}}
  {{if .UpdateLog}}<div class="label" style="margin:.9rem 0 .2rem">Last update log</div><pre style="background:hsl(var(--primary) / .04);border:1px solid hsl(var(--border));border-radius:.5rem;padding:.7rem;font-size:11px;line-height:1.5;overflow:auto;max-height:220px;margin:0">{{.UpdateLog}}</pre>{{end}}
</div>
<div class="card" style="margin-bottom:1rem">
  <h2>Services</h2>
  <div class="tblwrap" style="margin-top:.8rem"><table class="data">
    <thead><tr><th>Service</th><th>State</th></tr></thead>
    <tbody>{{range .Svcs}}<tr><td>{{.Name}}</td><td>{{if .OK}}<span class="badge active">{{.State}}</span>{{else}}<span class="badge paused">{{.State}}</span>{{end}}</td></tr>{{end}}</tbody>
  </table></div>
  <div style="display:flex;gap:2rem;margin-top:1rem;flex-wrap:wrap">
    <div><div class="label">Host RAM</div><div style="font-family:var(--serif);font-size:20px">{{.Stats.RAMUsed}} <span class="muted" style="font-size:12px">/ {{.Stats.RAMTotal}}</span></div></div>
    <div><div class="label">Disk free</div><div style="font-family:var(--serif);font-size:20px">{{.Stats.DiskFree}}</div></div>
    <div><div class="label">Projects</div><div style="font-family:var(--serif);font-size:20px">{{.Stats.NProjects}}</div></div>
  </div>
</div>
<div class="card">
  <h2>Resilience</h2>
  <ul class="muted" style="font-size:13px;line-height:1.9;padding-left:1.1rem;margin:.5rem 0 0">
    <li><b style="color:hsl(var(--fg))">Database is independent.</b> Postgres runs in its own container - if the control plane restarts or crashes, your databases keep serving connections on 5432/6543 with no interruption.</li>
    <li><b style="color:hsl(var(--fg))">Control plane self-heals.</b> pgforged runs under systemd with <code>Restart=always</code>; a crash restarts it in ~2s. Data API processes restart on the next request.</li>
    <li><b style="color:hsl(var(--fg))">Backups.</b> Nightly logical dumps + continuous WAL + basebackups (PITR) + off-box S3 - the platform can be rebuilt from a fresh install and restored.</li>
    <li><b style="color:hsl(var(--fg))">Safe rollback.</b> Deploy any earlier commit and it runs against the same database; schema changes are additive only.</li>
  </ul>
</div>
<div class="card" style="margin-top:1rem;text-align:center">
  <p class="muted" style="font-size:13px;margin:.2rem 0">ForgeBase is designed, built and maintained by
    <a href="https://ffstudios.io" target="_blank" style="color:hsl(var(--primary))"><b>FutureForge Studios Private Limited</b></a>.</p>
  <p class="muted" style="font-size:12px;margin:.2rem 0">Crafted with a lot of care in India ♥</p>
</div>`

const changelogBody = `
<div class="pagehead"><h1>What's New</h1><p>Every feature and every release in ForgeBase. You're running <b>v{{.AppVersion}}</b> · build <code>{{.Build}}</code>. <a href="/system" style="color:hsl(var(--primary))">Check for updates</a></p></div>

<h2 style="font-size:15px;margin:.4rem 0 .8rem">Features</h2>
<div class="grid g3" style="margin-bottom:1.6rem">
  {{range .Features}}
  <div class="card">
    <h2 style="font-size:14px">{{.Name}}</h2>
    <ul style="list-style:none;padding:0;margin:.6rem 0 0">
      {{range .Items}}<li style="margin-bottom:.6rem"><div style="font-size:13px;font-weight:600">{{.Name}}</div><div class="muted" style="font-size:12px;line-height:1.5">{{.Desc}}</div></li>{{end}}
    </ul>
  </div>
  {{end}}
</div>

<h2 style="font-size:15px;margin:1.4rem 0 .8rem">Release history</h2>
<div class="card">
  {{range .Releases}}
  <div style="padding:.9rem 0;border-bottom:1px solid hsl(var(--border))">
    <div style="display:flex;align-items:baseline;gap:.6rem;flex-wrap:wrap">
      <span class="badge active" style="font-family:var(--mono)">v{{.Version}}</span>
      <span class="muted" style="font-size:12px">{{.Date}}</span>
    </div>
    <p style="font-size:13px;margin:.5rem 0 .3rem">{{.Summary}}</p>
    {{range .Sections}}
    <div style="margin-top:.5rem">
      <div class="label" style="margin-bottom:.2rem">{{.Kind}}</div>
      <ul class="muted" style="font-size:12.5px;line-height:1.7;padding-left:1.1rem;margin:0">
        {{range .Entries}}<li>{{.}}</li>{{end}}
      </ul>
    </div>
    {{end}}
  </div>
  {{end}}
</div>`

const syncBody = `
<div class="pagehead"><h1>Sync / Clone</h1><p>Import <b>{{.Slug}}</b> from an external Postgres, keep it in sync, or refresh it on demand.</p></div>
{{if not .HasSource}}
<div class="card" style="text-align:center;padding:2.4rem">
  <h2>No source database</h2>
  <p class="muted" style="font-size:13px;margin:.5rem auto .2rem;max-width:520px">This project wasn't created by cloning. To pull data in from an external Postgres, use <b>Import from Postgres</b> on the <a href="/" style="color:hsl(var(--primary))">Projects</a> page to create a cloned project.</p>
</div>
{{else}}
<div class="grid g3" style="margin-bottom:1rem">
  <div class="card stat"><div class="k">Clone status</div><div class="v" style="font-size:20px;text-transform:capitalize">{{if eq .Status "cloning"}}<span class="badge cloning">cloning…</span>{{else if eq .Status "error"}}<span class="badge paused">error</span>{{else}}<span class="badge active">done</span>{{end}}</div></div>
  <div class="card stat"><div class="k">Live sync</div><div class="v" style="font-size:20px">{{if .SyncOn}}<span class="badge active">on</span>{{else}}<span class="badge paused">off</span>{{end}}</div></div>
  <div class="card stat"><div class="k">Auto-refresh</div><div class="v" style="font-size:14px">reload to update</div></div>
</div>
<div class="card" style="margin-bottom:1rem">
  <h2>Source</h2>
  <div class="cs" style="margin-top:.6rem"><span class="tag">From</span><code>{{.Source}}</code></div>
  {{if .Message}}<p class="muted" style="font-size:12px;margin-top:.6rem">Last result: {{.Message}}</p>{{end}}
  {{if eq .Status "cloning"}}<p class="muted" style="font-size:12px;margin-top:.6rem">Cloning is running in the background - reload this page to see when it finishes.</p>{{end}}
</div>
<div class="grid g2">
  <div class="card">
    <h2>Refresh now</h2>
    <p class="muted" style="font-size:12.5px;margin:.3rem 0 .8rem">Re-copy the whole database from the source, replacing the current data. Good for a periodic manual sync.</p>
    <form method="post" action="/p/{{.Slug}}/sync-refresh" onsubmit="return confirm('Replace all data in {{.Slug}} with a fresh copy from the source?')"><button class="btn btn-primary" {{if eq .Status "cloning"}}disabled{{end}}>{{icon "restore"}} Refresh from source</button></form>
  </div>
  <div class="card">
    <h2>Live sync</h2>
    <p class="muted" style="font-size:12.5px;margin:.3rem 0 .8rem">Stream changes continuously via logical replication (near-instant). Requires the source to have logical replication enabled (<code>wal_level=logical</code>).</p>
    {{if .SyncOn}}
    <form method="post" action="/p/{{.Slug}}/livesync-disable"><button class="btn btn-ghost">Disable live sync</button></form>
    {{else}}
    <form method="post" action="/p/{{.Slug}}/livesync-enable"><button class="btn btn-primary" {{if eq .Status "cloning"}}disabled{{end}}>{{icon "bolt"}} Enable live sync</button></form>
    {{end}}
  </div>
</div>
{{end}}`

// ----- shared JS snippets -----

const copyJS = `
<script>
function cp(id){
  var t=document.getElementById(id).textContent;
  navigator.clipboard.writeText(t);
  var btn=event.target.closest('.copy');
  if(btn){btn.style.color='hsl(var(--primary))';setTimeout(function(){btn.style.color='';},1000);}
}
</script>`

const delDialog = `
<dialog id="delDlg" style="border:1px solid hsl(var(--border));border-radius:1rem;padding:1.4rem;max-width:360px;background:hsl(var(--card));color:hsl(var(--fg))">
  <form method="post" action="/delete">
    <h2 style="font-size:17px;margin-bottom:.3rem">Delete <span id="delName"></span>?</h2>
    <p class="muted" style="font-size:12.5px;margin:0 0 .9rem">This permanently drops the database. Type the project name to confirm.</p>
    <input type="hidden" name="slug" id="delSlug">
    <input type="text" name="confirm" id="delConfirm" placeholder="type project name" style="margin-bottom:.9rem">
    <div style="display:flex;gap:.5rem;justify-content:flex-end">
      <button type="button" class="btn btn-ghost btn-sm" onclick="document.getElementById('delDlg').close()">Cancel</button>
      <button type="submit" class="btn btn-danger btn-sm">Delete forever</button>
    </div>
  </form>
</dialog>
<script>
function askDel(s){document.getElementById('delName').textContent=s;document.getElementById('delSlug').value=s;
  document.getElementById('delConfirm').value='';document.getElementById('delDlg').showModal();}
</script>`

const editCellJS = `
<script>
function editCell(td){
  if(td.querySelector('input'))return;
  var old=td.textContent==='null'?'':td.textContent;
  if(/\[(binary|bytea) · [^\]]*\]$/.test(old)||/- edit via SQL\]$/.test(old)){
    alert('This value is too large to edit inline - use the SQL editor.');return;}
  var meta=td.getAttribute('data-c'); if(!meta)return;
  var tr=td.parentElement, form=tr.querySelector('form');
  var pk={}; form.querySelectorAll('input[name^=pk_]').forEach(function(i){pk[i.name]=i.value;});
  var inp=document.createElement('input'); inp.type='text'; inp.value=old;
  inp.style.width='100%'; inp.style.font='inherit';
  td.textContent=''; td.appendChild(inp); inp.focus();
  function save(){
    // Dirty check: leaving a cell unchanged (e.g. after double-clicking only to
    // read it, including a NULL cell) must not overwrite the row with a blank.
    if(inp.value===old){ td.textContent=old===''?'null':old; return; }
    var body=new URLSearchParams();
    body.set('__table', form.querySelector('input[name=__table]').value);
    body.set('__col', meta); body.set('__val', inp.value);
    for(var k in pk){body.set(k, pk[k]);}
    fetch(location.pathname.replace(/\/tables.*/,'')+'/row-update',{method:'POST',headers:{'Content-Type':'application/x-www-form-urlencoded'},body:body})
      .then(function(r){ if(r.ok){td.textContent=inp.value===''?'null':inp.value;} else {td.textContent=old===''?'null':old; td.style.color='hsl(var(--destructive))';} });
  }
  inp.addEventListener('blur',save);
  inp.addEventListener('keydown',function(e){if(e.key==='Enter'){e.preventDefault();inp.blur();}if(e.key==='Escape'){td.textContent=old===''?'null':old;}});
}
</script>`
