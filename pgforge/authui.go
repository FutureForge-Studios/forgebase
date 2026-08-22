package main

import "html/template"

var authTmpl = template.Must(template.New("auth").Parse(`<!doctype html>
<html lang="en"><head><meta charset="utf-8"><meta name="viewport" content="width=device-width, initial-scale=1">
<title>{{.Title}} · ForgeBase</title>
<link rel="icon" type="image/svg+xml" href="/favicon.svg"><link rel="apple-touch-icon" href="/favicon.svg">
<link rel="preconnect" href="https://fonts.googleapis.com"><link rel="preconnect" href="https://fonts.gstatic.com" crossorigin>
<link href="https://fonts.googleapis.com/css2?family=Fraunces:opsz,wght@9..144,400;9..144,500;9..144,600&family=Inter:wght@400;500;600;700&family=JetBrains+Mono:wght@400;500&display=swap" rel="stylesheet">
<style>` + cssDesign + `</style></head>
<body>
<div class="aurora"></div>
<div class="authwrap"><div class="authcard">
  <div style="display:flex;justify-content:center"><span class="brand">{{.Brand}}</span></div>
  <div class="label" style="text-align:center;margin-top:1rem">ForgeBase</div>
  <h1>{{.Title}}</h1>
  {{if .Subtitle}}<div class="sub">{{.Subtitle}}</div>{{end}}
  {{.Body}}
  <div style="text-align:center;margin-top:1.4rem;font-size:11px;color:hsl(var(--muted-fg));line-height:1.6">
    © 2026 <a href="https://ffstudios.io" target="_blank" style="color:hsl(var(--muted-fg))">FutureForge Studios Private Limited</a><br>Made with care in India ♥
  </div>
</div></div>
</body></html>`))

const loginForm = `
{{if .Err}}<div class="flash err" style="margin-bottom:1rem">{{.Err}}</div>{{end}}
<form method="post" action="/login">
  <label class="fld"><span class="lt">Email or username</span>
    <input type="text" name="email" autocomplete="username" autofocus required></label>
  <label class="fld"><span class="lt">Password</span>
    <input type="password" name="pass" autocomplete="current-password" required></label>
  <label style="display:flex;align-items:center;gap:.45rem;font-size:13px;margin:-.3rem 0 .9rem;cursor:pointer">
    <input type="checkbox" name="remember" style="width:auto;margin:0"> Remember me for 7 days</label>
  <button class="btn btn-primary" type="submit">Sign in</button>
</form>
<div class="authfoot">
{{if .NoUsers}}No accounts yet. <a href="/register">Create the first account</a>.
{{else}}Need an account? <a href="/register">Register</a>.{{end}}
</div>`

const registerForm = `
{{if .Err}}<div class="flash err" style="margin-bottom:1rem">{{.Err}}</div>{{end}}
<form method="post" action="/register">
  <label class="fld"><span class="lt">Full name</span>
    <input type="text" name="name" value="{{.Name}}" autocomplete="name" autofocus required></label>
  <label class="fld"><span class="lt">Email</span>
    <input type="email" name="email" value="{{.Email}}" autocomplete="email" required></label>
  <label class="fld"><span class="lt">Password</span>
    <input type="password" name="pass" autocomplete="new-password" minlength="8" required>
    <span class="muted" style="font-size:11px">At least 8 characters.</span></label>
  <button class="btn btn-primary" type="submit">Create account</button>
</form>
<div class="authfoot">Already have an account? <a href="/login">Sign in</a>.</div>`

const closedForm = `
<div class="flash err" style="margin:1rem 0">This ForgeBase platform is invite-only. Registration is closed.</div>
<p class="muted" style="font-size:13px;text-align:center">An existing owner can add you from the <b>Team</b> page. You'll receive a temporary password to sign in with.</p>
<div class="authfoot">Already have an account? <a href="/login">Sign in</a>.</div>`

const inviteForm = `
{{if .Err}}<div class="flash err" style="margin-bottom:1rem">{{.Err}}</div>{{end}}
<form method="post" action="/invite">
  <input type="hidden" name="token" value="{{.Token}}">
  <label class="fld"><span class="lt">Email</span><input type="text" value="{{.Email}}" disabled></label>
  <label class="fld"><span class="lt">Choose a password</span>
    <input type="password" name="pass" autocomplete="new-password" minlength="8" autofocus required>
    <span class="muted" style="font-size:11px">At least 8 characters.</span></label>
  <button class="btn btn-primary" type="submit">Set password &amp; sign in</button>
</form>`

const accountBody = `
<div class="pagehead"><h1>Account settings</h1><p>Manage your profile, sign-in and personal API keys.</p></div>
{{if .NewKey}}<div class="flash" style="margin-bottom:1rem">New API key (copy now, shown once): <code id="nk" style="color:hsl(var(--primary))">{{.NewKey}}</code> <button class="copy" onclick="cp('nk')" style="vertical-align:middle">{{icon "copy"}}</button></div>{{end}}
{{if not .HasRow}}<div class="flash err" style="margin-bottom:1rem">You are signed in with the built-in admin login. <a href="/register" style="color:hsl(var(--primary))">Register a personal account</a> to manage a profile, email and API keys.</div>{{end}}
<div class="grid g2">
  <div class="card">
    <h2>Personal information</h2>
    <form method="post" action="/account/profile" style="margin-top:1rem">
      <label class="fld"><span class="lt">First name</span><input type="text" name="first" value="{{.First}}"></label>
      <label class="fld"><span class="lt">Last name</span><input type="text" name="last" value="{{.Last}}"></label>
      <button class="btn btn-primary btn-sm" type="submit">Save</button>
    </form>
  </div>
  <div class="card">
    <h2>Email</h2>
    <p class="muted" style="font-size:12.5px;margin:.4rem 0 .8rem">Current: <b style="color:hsl(var(--fg))">{{if .Email}}{{.Email}}{{else}}-{{end}}</b></p>
    <form method="post" action="/account/email">
      <label class="fld"><span class="lt">Change email</span><input type="email" name="email" placeholder="you@example.com"></label>
      <button class="btn btn-ghost btn-sm" type="submit">Update email</button>
    </form>
  </div>
  <div class="card">
    <h2>Set password</h2>
    <form method="post" action="/account/password" style="margin-top:1rem">
      <label class="fld"><span class="lt">Current password</span><input type="password" name="current" autocomplete="current-password" required></label>
      <label class="fld"><span class="lt">New password</span><input type="password" name="new" autocomplete="new-password" minlength="8" required></label>
      <button class="btn btn-primary btn-sm" type="submit">Set password</button>
    </form>
  </div>
  <div class="card">
    <h2>Personal API keys</h2>
    <p class="muted" style="font-size:12.5px;margin:.4rem 0 .8rem">For programmatic access. Never share a key or expose it client-side.</p>
    <form method="post" action="/account/apikey-create" style="display:flex;gap:.5rem;margin-bottom:.9rem">
      <input type="text" name="name" placeholder="key name" style="flex:1">
      <button class="btn btn-ghost btn-sm" type="submit">{{icon "plus"}} Create</button>
    </form>
    {{if .Keys}}
    <div class="tblwrap"><table class="data">
      <thead><tr><th>Name</th><th>Prefix</th><th>Created</th><th>Last used</th><th></th></tr></thead>
      <tbody>{{range .Keys}}<tr><td>{{.Name}}</td><td><code>{{.Prefix}}…</code></td><td>{{.Created}}</td><td>{{.LastUsed}}</td>
        <td style="text-align:right"><form method="post" action="/account/apikey-revoke" onsubmit="return confirm('Revoke this key?')"><input type="hidden" name="id" value="{{.ID}}"><button class="copy" style="color:hsl(var(--destructive))">{{icon "trash"}}</button></form></td></tr>{{end}}</tbody>
    </table></div>
    {{else}}<p class="muted" style="font-size:12.5px">No API keys yet.</p>{{end}}
  </div>
</div>
` + copyJS
