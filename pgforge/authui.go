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
    <h2>AI assistant (bring your own key)</h2>
    <p class="muted" style="font-size:12.5px;margin:.4rem 0 .8rem">Powers "Ask AI" in the SQL editor. Pick a provider, paste your key, load its live model list and choose one; your key is encrypted at rest and only ever sent to the endpoint below.</p>
    <form method="post" action="/account/ai" autocomplete="off">
      <label class="fld"><span class="lt">Provider</span>
        <select id="aiprov" onchange="aiProv()">
          <option value="anthropic">Claude (Anthropic)</option>
          <option value="openai">OpenAI</option>
          <option value="custom">Custom OpenAI-compatible endpoint</option>
        </select></label>
      <label class="fld"><span class="lt">Endpoint base URL</span><input type="text" id="aibase" name="ai_base" value="{{.AIBase}}" placeholder="https://api.anthropic.com" autocomplete="off"></label>
      <label class="fld"><span class="lt">API key</span><input type="password" id="aikey" name="ai_key" autocomplete="new-password" placeholder="{{if .AIHasKey}}saved - leave blank to keep{{else}}sk-...{{end}}"></label>
      <label class="fld"><span class="lt">Model</span>
        <div style="display:flex;gap:.4rem">
          <select id="aimodel" name="ai_model" style="flex:1">
            {{if .AIModel}}<option value="{{.AIModel}}" selected>{{.AIModel}}</option>{{else}}<option value="">load models, then pick one</option>{{end}}
          </select>
          <button class="btn btn-ghost btn-sm" type="button" onclick="aiLoadModels(this)">Load models</button>
        </div></label>
      <button class="btn btn-primary btn-sm" type="submit">Save AI settings</button>
    </form>
    <script>
    function aiProv(){
      var p=document.getElementById('aiprov').value, b=document.getElementById('aibase');
      if(p==='anthropic'){b.value='https://api.anthropic.com';b.readOnly=true;}
      else if(p==='openai'){b.value='https://api.openai.com/v1';b.readOnly=true;}
      else{b.readOnly=false;if(b.value==='https://api.anthropic.com'||b.value==='https://api.openai.com/v1'){b.value='';}b.focus();}
    }
    (function(){
      var b=document.getElementById('aibase').value, p=document.getElementById('aiprov');
      if(b.indexOf('anthropic')>=0||b===''){p.value='anthropic';if(b===''){document.getElementById('aibase').value='https://api.anthropic.com';}}
      else if(b.indexOf('api.openai.com')>=0){p.value='openai';}
      else{p.value='custom';}
      if(p.value!=='custom'){document.getElementById('aibase').readOnly=true;}
    })();
    function aiLoadModels(btn){
      btn.disabled=true;btn.textContent='Loading...';
      fetch('/account/ai-models',{method:'POST',headers:{'Content-Type':'application/json'},
        body:JSON.stringify({base:document.getElementById('aibase').value,key:document.getElementById('aikey').value})})
        .then(function(r){return r.json()})
        .then(function(d){
          btn.disabled=false;btn.textContent='Load models';
          if(!d.models){alert(d.message||'could not list models');return;}
          var sel=document.getElementById('aimodel'), cur=sel.value;
          sel.innerHTML='';
          for(var i=0;i<d.models.length;i++){
            var o=document.createElement('option');
            o.value=d.models[i].ID;o.textContent=d.models[i].Name;
            if(d.models[i].ID===cur){o.selected=true;}
            sel.appendChild(o);
          }
        })
        .catch(function(){btn.disabled=false;btn.textContent='Load models';alert('could not reach the panel');});
    }
    </script>
  </div>
  <div class="card">
    <div style="display:flex;align-items:center;gap:.6rem"><h2>Devices &amp; sessions</h2><div class="spacer"></div>
      {{if .Sessions}}<form method="post" action="/account/session-revoke"><input type="hidden" name="others" value="1">
        <button class="btn btn-ghost btn-sm" type="submit">Sign out everywhere else</button></form>{{end}}</div>
    {{if not .Sessions}}<p class="muted" style="font-size:12.5px;margin:.4rem 0 0">Sessions appear here after your next sign-in.</p>
    {{else}}
    <div class="tblwrap" style="margin-top:.7rem"><table class="data">
      <thead><tr><th>Device</th><th>IP</th><th>Signed in</th><th>Last seen</th><th></th></tr></thead>
      <tbody>{{range .Sessions}}<tr>
        <td style="max-width:280px;overflow:hidden;text-overflow:ellipsis;white-space:nowrap;font-size:11.5px">{{if .Current}}<span class="badge active">this device</span> {{end}}{{.UA}}</td>
        <td class="muted" style="font-size:11.5px">{{.IP}}</td>
        <td class="muted" style="font-size:11.5px">{{.Created}}</td>
        <td class="muted" style="font-size:11.5px">{{.LastSeen}}</td>
        <td style="text-align:right"><form method="post" action="/account/session-revoke" style="display:inline" {{if .Current}}onsubmit="return confirm('This is the device you are using - signing it out ends this session. Continue?')"{{end}}>
          <input type="hidden" name="id" value="{{.ID}}"><button class="btn btn-ghost btn-sm">Sign out</button></form></td>
      </tr>{{end}}</tbody>
    </table></div>{{end}}
  </div>
  <div class="card">
    <h2>Two-factor authentication</h2>
    {{if not .HasRow}}
    <p class="muted" style="font-size:12.5px;margin:.4rem 0 0">Available on registered accounts (not the built-in admin login).</p>
    {{else if .TOTPOn}}
    <p style="font-size:12.5px;margin:.4rem 0 .8rem"><span class="badge active">2FA on</span> <span class="muted">Sign-in requires your password plus an authenticator code.</span></p>
    <form method="post" action="/account/totp-disable" style="display:flex;gap:.5rem;align-items:flex-end">
      <label class="fld" style="margin:0"><span class="lt">Current code</span><input type="text" name="code" inputmode="numeric" pattern="[0-9]{6}" maxlength="6" required style="width:110px;font-family:var(--mono)"></label>
      <button class="btn btn-ghost btn-sm" type="submit" style="color:hsl(var(--destructive))">Turn off 2FA</button>
    </form>
    {{else if .TOTPSecret}}
    <p class="muted" style="font-size:12.5px;margin:.4rem 0 .6rem">Add this key to your authenticator app (Google Authenticator, 1Password, Aegis...), then confirm with the current code:</p>
    <div class="cs"><span class="tag">Key</span><code id="totpsec">{{.TOTPSecret}}</code><button class="copy" onclick="cp('totpsec')">{{icon "copy"}}</button></div>
    <div class="cs"><span class="tag">URI</span><code id="totpuri" style="font-size:10.5px">{{.TOTPUri}}</code><button class="copy" onclick="cp('totpuri')">{{icon "copy"}}</button></div>
    <div id="qrbox" title="Scan with your authenticator app"></div>
    <form method="post" action="/account/totp-confirm" style="display:flex;gap:.5rem;align-items:flex-end;margin-top:.7rem">
      <label class="fld" style="margin:0"><span class="lt">Code from the app</span><input type="text" name="code" inputmode="numeric" pattern="[0-9]{6}" maxlength="6" required autofocus style="width:110px;font-family:var(--mono)"></label>
      <button class="btn btn-primary btn-sm" type="submit">Confirm and enable</button>
    </form>
    <form method="post" action="/account/totp-disable" style="margin-top:.4rem"><button class="btn btn-ghost btn-sm" type="submit">Cancel setup</button></form>
    {{else}}
    <p class="muted" style="font-size:12.5px;margin:.4rem 0 .8rem">Protect your panel account with an authenticator app: sign-in then needs your password AND a rotating 6-digit code.</p>
    <form method="post" action="/account/totp-setup"><button class="btn btn-primary btn-sm" type="submit">{{icon "shield"}} Enable 2FA</button></form>
    {{end}}
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
` + copyJS + qrVendorJS
