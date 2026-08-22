package main

import (
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"time"

	"golang.org/x/crypto/bcrypt"
)

// People / Team - the organization members (rows in users). Owners can add
// members, change roles, and remove them.

// member first so the Add-member form defaults to least privilege.
var roles = []string{"member", "admin", "owner"}

func (a *app) peoplePage(w http.ResponseWriter, r *http.Request) {
	type member struct {
		ID, Name, Email, Role, Created, Scope string
	}
	var members []member
	rows, _ := a.db.Query(`SELECT id, coalesce(nullif(trim(coalesce(first_name,'')||' '||coalesce(last_name,'')),''), name),
		email, role, to_char(created_at,'Mon DD, YYYY'), coalesce(project_scope,'') FROM users ORDER BY created_at`)
	if rows != nil {
		for rows.Next() {
			var m member
			rows.Scan(&m.ID, &m.Name, &m.Email, &m.Role, &m.Created, &m.Scope)
			members = append(members, m)
		}
		rows.Close()
	}
	var owners, admins, mem int
	a.db.QueryRow(`SELECT count(*) FILTER (WHERE role='owner'), count(*) FILTER (WHERE role='admin'),
		count(*) FILTER (WHERE role='member') FROM users`).Scan(&owners, &admins, &mem)
	content := renderContent(peopleBody, map[string]any{
		"Members": members, "Roles": roles, "Invite": r.URL.Query().Get("invite"),
		"Total": len(members), "Owners": owners, "Admins": admins, "Mem": mem,
	})
	a.renderShell(w, r, shellData{Title: "Team", Nav: "people",
		Crumbs: []crumb{{Label: "Team"}}}, content)
}

func (a *app) addMember(w http.ResponseWriter, r *http.Request) {
	name := strings.TrimSpace(r.FormValue("name"))
	email := strings.ToLower(strings.TrimSpace(r.FormValue("email")))
	role := r.FormValue("role")
	if !contains(roles, role) {
		role = "member"
	}
	if name == "" || !strings.Contains(email, "@") {
		redirectErr(w, r, "/people", "Enter a name and a valid email.")
		return
	}
	// Names are the session identity and are matched case-insensitively at login,
	// so a duplicate name (or one equal to the break-glass admin) would let one
	// account act as another / silently gain owner. Enforce uniqueness here.
	if strings.EqualFold(name, a.cfg.panelUser) {
		redirectErr(w, r, "/people", "That name is reserved; choose another.")
		return
	}
	var nameTaken bool
	a.db.QueryRow(`SELECT EXISTS(SELECT 1 FROM users WHERE lower(name)=lower($1))`, name).Scan(&nameTaken)
	if nameTaken {
		redirectErr(w, r, "/people", "A member with that display name already exists; choose another.")
		return
	}
	var exists bool
	a.db.QueryRow(`SELECT EXISTS(SELECT 1 FROM users WHERE lower(email)=$1)`, email).Scan(&exists)
	if exists {
		redirectErr(w, r, "/people", "A member with that email already exists.")
		return
	}
	// unusable random password until they set their own via the invite link
	hash, _ := bcrypt.GenerateFromPassword([]byte(randHex(24)), bcrypt.DefaultCost)
	if _, err := a.db.Exec(`INSERT INTO users(name,email,pass_hash,role,invite_pending) VALUES ($1,$2,$3,$4,true)`,
		name, email, string(hash), role); err != nil {
		redirectErr(w, r, "/people", "Could not add member: "+err.Error())
		return
	}
	a.audit(r, "add-member", email)
	exp := time.Now().Add(7 * 24 * time.Hour).Unix()
	token := a.signState(fmt.Sprintf("invite|%s|%d", email, exp))
	link := "https://" + a.cfg.domain + "/invite?token=" + token
	redirectMsg(w, r, "/people?invite="+url.QueryEscape(link), "Member added. Share the invite link below.")
}

func (a *app) setMemberRole(w http.ResponseWriter, r *http.Request) {
	id := r.FormValue("id")
	role := r.FormValue("role")
	if !contains(roles, role) {
		redirectErr(w, r, "/people", "Invalid role.")
		return
	}
	var email string
	a.db.QueryRow(`SELECT email FROM users WHERE id=$1`, id).Scan(&email)
	a.db.Exec(`UPDATE users SET role=$1 WHERE id=$2`, role, id)
	a.audit(r, "set-role", email+" -> "+role)
	redirectMsg(w, r, "/people", "Role updated.")
}

func (a *app) removeMember(w http.ResponseWriter, r *http.Request) {
	id := r.FormValue("id")
	// never remove the last owner
	var owners int
	a.db.QueryRow(`SELECT count(*) FROM users WHERE role='owner'`).Scan(&owners)
	var isOwner bool
	a.db.QueryRow(`SELECT role='owner' FROM users WHERE id=$1`, id).Scan(&isOwner)
	if isOwner && owners <= 1 {
		redirectErr(w, r, "/people", "You cannot remove the last owner.")
		return
	}
	var email string
	a.db.QueryRow(`SELECT email FROM users WHERE id=$1`, id).Scan(&email)
	a.db.Exec(`DELETE FROM users WHERE id=$1`, id)
	a.audit(r, "remove-member", email)
	redirectMsg(w, r, "/people", "Member removed.")
}

// setMemberScope restricts a member to specific projects (empty = all).
// Owners are never scoped - the role outranks the fence.
func (a *app) setMemberScope(w http.ResponseWriter, r *http.Request) {
	id := r.FormValue("id")
	scope := strings.TrimSpace(r.FormValue("scope"))
	var cleaned []string
	for _, s := range strings.Split(scope, ",") {
		s = strings.TrimSpace(strings.ToLower(s))
		if s == "" {
			continue
		}
		if !slugRe.MatchString(s) || !a.projectExists(s) {
			redirectErr(w, r, "/people", "No such project: "+s)
			return
		}
		cleaned = append(cleaned, s)
	}
	var email string
	a.db.QueryRow(`SELECT email FROM users WHERE id=$1`, id).Scan(&email)
	a.db.Exec(`UPDATE users SET project_scope=$2 WHERE id=$1`, id, strings.Join(cleaned, ","))
	what := "all projects"
	if len(cleaned) > 0 {
		what = strings.Join(cleaned, ", ")
	}
	a.audit(r, "set-scope", email+" -> "+what)
	redirectMsg(w, r, "/people", "Project access updated: "+what+".")
}

// signOutMember revokes every panel session a member holds (owner action).
func (a *app) signOutMember(w http.ResponseWriter, r *http.Request) {
	id := r.FormValue("id")
	var name, email string
	if a.db.QueryRow(`SELECT name, email FROM users WHERE id=$1`, id).Scan(&name, &email) != nil {
		redirectErr(w, r, "/people", "No such member.")
		return
	}
	a.db.Exec(`DELETE FROM panel_sessions WHERE user_name=$1`, name)
	a.audit(r, "signout-member", email)
	redirectMsg(w, r, "/people", name+" is signed out everywhere.")
}
