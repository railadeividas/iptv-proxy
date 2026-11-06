package server

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"iptv-proxy/internal/auth"
	"iptv-proxy/internal/proxy"
	"iptv-proxy/internal/store"
	"iptv-proxy/internal/version"
)

type ctxKey struct{}

// userFrom is the signed-in user of a request that went through requireAuth.
func userFrom(r *http.Request) *store.User {
	u, _ := r.Context().Value(ctxKey{}).(*store.User)
	return u
}

// can reports whether the signed-in user has a permission.
func can(r *http.Request, p auth.Perm) bool {
	u := userFrom(r)
	return u != nil && auth.Can(u.Role, p)
}

// owner is the user whose profiles a request may see: 0 (everybody's) for those who may see all of them.
func owner(r *http.Request) int64 {
	if can(r, auth.AllProfiles) {
		return 0
	}
	return userFrom(r).ID
}

// sessionUser loads the user a session cookie belongs to. A cookie stops working when the user is
// deleted or the password changes.
func (s *Server) sessionUser(r *http.Request) *store.User {
	c, err := r.Cookie("session")
	if err != nil {
		return nil
	}
	p, err := s.signer.Decode(c.Value)
	if err != nil {
		return nil
	}
	f := strings.Split(p.U, ":")
	if len(f) != 3 || f[0] != "session" {
		return nil
	}
	id, err := strconv.ParseInt(f[1], 10, 64)
	if err != nil {
		return nil
	}
	u, err := s.st.GetUser(id)
	if err != nil || auth.Fingerprint(u.Password) != f[2] {
		return nil
	}
	return u
}

func (s *Server) requireAuth(h http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		u := s.sessionUser(r)
		if u == nil {
			httpErr(w, http.StatusUnauthorized, "sign in required")
			return
		}
		h(w, r.WithContext(context.WithValue(r.Context(), ctxKey{}, u)))
	}
}

// requirePerm is requireAuth plus a permission the user's role must have.
func (s *Server) requirePerm(p auth.Perm, h http.HandlerFunc) http.HandlerFunc {
	return s.requireAuth(func(w http.ResponseWriter, r *http.Request) {
		if !can(r, p) {
			httpErr(w, http.StatusForbidden, "your account is not allowed to do that")
			return
		}
		h(w, r)
	})
}

func (s *Server) setSession(w http.ResponseWriter, r *http.Request, u *store.User) {
	ttl := time.Duration(s.set.Int("session_days")) * 24 * time.Hour
	tok := s.signer.Encode(proxy.Payload{U: fmt.Sprintf("session:%d:%s", u.ID, auth.Fingerprint(u.Password)), E: time.Now().Add(ttl).Unix()})
	http.SetCookie(w, &http.Cookie{Name: "session", Value: tok, Path: "/", HttpOnly: true, SameSite: http.SameSiteStrictMode,
		Secure: strings.HasPrefix(s.pub(r), "https://"), MaxAge: int(ttl.Seconds())})
}

type userView struct {
	ID       int64       `json:"id"`
	Username string      `json:"username"`
	Role     string      `json:"role"`
	Perms    []auth.Perm `json:"perms"`
}

func viewOf(u *store.User) *userView {
	return &userView{ID: u.ID, Username: u.Username, Role: u.Role, Perms: auth.Perms(u.Role)}
}

// session tells the UI who is signed in, or that the first-start setup is still to be done.
func (s *Server) session(w http.ResponseWriter, r *http.Request) {
	out := map[string]any{"setup_needed": s.st.CountUsers() == 0, "user": nil, "version": version.String()}
	if u := s.sessionUser(r); u != nil {
		out["user"] = viewOf(u)
	}
	writeJSON(w, out)
}

type credentials struct{ Username, Password string }

// checkNewPassword applies the minimum length setting.
func (s *Server) checkNewPassword(pw string) error {
	if n := s.set.Int("password_min_length"); len(pw) < n {
		return fmt.Errorf("The password must be at least %d characters", n)
	}
	return nil
}

// setup creates the superuser on the first start; it works only while there are no users.
func (s *Server) setup(w http.ResponseWriter, r *http.Request) {
	var in credentials
	if !decode(w, r, &in) {
		return
	}
	in.Username = strings.TrimSpace(in.Username)
	if err := auth.CheckUsername(in.Username); err != nil {
		httpErr(w, http.StatusBadRequest, err.Error())
		return
	}
	if err := s.checkNewPassword(in.Password); err != nil {
		httpErr(w, http.StatusBadRequest, err.Error())
		return
	}
	u, err := s.st.CreateFirstUser(in.Username, auth.Hash(in.Password), auth.Superuser)
	if err != nil {
		httpErr(w, http.StatusConflict, err.Error())
		return
	}
	s.setSession(w, r, u)
	writeJSON(w, viewOf(u))
}

var (
	dummyOnce sync.Once
	dummyHash string
)

func (s *Server) login(w http.ResponseWriter, r *http.Request) {
	ip := clientIP(r)
	if s.rateLimited(ip) {
		httpErr(w, http.StatusTooManyRequests, "too many failed attempts, try again in a minute")
		return
	}
	var in credentials
	if json.NewDecoder(http.MaxBytesReader(w, r.Body, 4096)).Decode(&in) != nil {
		httpErr(w, http.StatusBadRequest, "invalid request")
		return
	}
	u, err := s.st.UserByName(strings.TrimSpace(in.Username))
	hash := ""
	if err == nil {
		hash = u.Password
	} else {
		// same work for an unknown user name, so the answer time does not tell whether it exists
		dummyOnce.Do(func() { dummyHash = auth.Hash("x") })
		hash = dummyHash
	}
	if !auth.Verify(hash, in.Password) || err != nil {
		s.recordFailure(ip)
		httpErr(w, http.StatusUnauthorized, "wrong user name or password")
		return
	}
	s.setSession(w, r, u)
	writeJSON(w, viewOf(u))
}

func (s *Server) logout(w http.ResponseWriter, r *http.Request) {
	http.SetCookie(w, &http.Cookie{Name: "session", Value: "", Path: "/", MaxAge: -1, HttpOnly: true})
	writeJSON(w, map[string]bool{"ok": true})
}

// changeOwnPassword lets any signed-in user set a new password after giving the current one.
func (s *Server) changeOwnPassword(w http.ResponseWriter, r *http.Request) {
	var in struct{ Current, Password string }
	if !decode(w, r, &in) {
		return
	}
	u := userFrom(r)
	if !auth.Verify(u.Password, in.Current) {
		httpErr(w, http.StatusForbidden, "The current password is wrong")
		return
	}
	if err := s.checkNewPassword(in.Password); err != nil {
		httpErr(w, http.StatusBadRequest, err.Error())
		return
	}
	if err := s.st.UpdateUser(u.ID, "", auth.Hash(in.Password), ""); err != nil {
		httpErr(w, 500, err.Error())
		return
	}
	nu, _ := s.st.GetUser(u.ID)
	s.setSession(w, r, nu) // the old cookie stops working with the old password
	writeJSON(w, map[string]bool{"ok": true})
}

// ---- users (superuser) ----

func (s *Server) listUsers(w http.ResponseWriter, r *http.Request) {
	us, err := s.st.ListUsers()
	if err != nil {
		httpErr(w, 500, err.Error())
		return
	}
	writeJSON(w, us)
}

func (s *Server) createUser(w http.ResponseWriter, r *http.Request) {
	var in struct{ Username, Password, Role string }
	if !decode(w, r, &in) {
		return
	}
	in.Username = strings.TrimSpace(in.Username)
	if err := auth.CheckUsername(in.Username); err != nil {
		httpErr(w, http.StatusBadRequest, err.Error())
		return
	}
	if !auth.ValidRole(in.Role) {
		httpErr(w, http.StatusBadRequest, "The role must be admin or user")
		return
	}
	if err := s.checkNewPassword(in.Password); err != nil {
		httpErr(w, http.StatusBadRequest, err.Error())
		return
	}
	u, err := s.st.CreateUser(in.Username, auth.Hash(in.Password), in.Role)
	if errors.Is(err, store.ErrExists) {
		httpErr(w, http.StatusConflict, err.Error())
		return
	} else if err != nil {
		httpErr(w, 500, err.Error())
		return
	}
	s.st.Notify2("users")
	writeJSON(w, u)
}

func (s *Server) patchUser(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	target, err := s.st.GetUser(id)
	if err != nil {
		httpErr(w, 404, "user not found")
		return
	}
	var in struct{ Username, Password, Role *string }
	if !decode(w, r, &in) {
		return
	}
	var name, hash, role string
	if in.Username != nil && strings.TrimSpace(*in.Username) != target.Username {
		name = strings.TrimSpace(*in.Username)
		if err := auth.CheckUsername(name); err != nil {
			httpErr(w, http.StatusBadRequest, err.Error())
			return
		}
	}
	if in.Password != nil && *in.Password != "" {
		if err := s.checkNewPassword(*in.Password); err != nil {
			httpErr(w, http.StatusBadRequest, err.Error())
			return
		}
		hash = auth.Hash(*in.Password)
	}
	if in.Role != nil && *in.Role != target.Role {
		if target.Role == auth.Superuser {
			httpErr(w, http.StatusBadRequest, "The role of the superuser can't be changed")
			return
		}
		if !auth.ValidRole(*in.Role) {
			httpErr(w, http.StatusBadRequest, "The role must be admin or user")
			return
		}
		role = *in.Role
	}
	if err := s.st.UpdateUser(id, name, hash, role); errors.Is(err, store.ErrExists) {
		httpErr(w, http.StatusConflict, err.Error())
		return
	} else if err != nil {
		httpErr(w, 500, err.Error())
		return
	}
	if me := userFrom(r); me.ID == id && hash != "" {
		nu, _ := s.st.GetUser(id)
		s.setSession(w, r, nu) // changing your own password must not sign you out
	}
	writeJSON(w, map[string]bool{"ok": true})
}

func (s *Server) deleteUser(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	target, err := s.st.GetUser(id)
	if err != nil {
		httpErr(w, 404, "user not found")
		return
	}
	if target.Role == auth.Superuser {
		httpErr(w, http.StatusBadRequest, "The superuser can't be deleted")
		return
	}
	if err := s.st.DeleteUser(id); err != nil {
		httpErr(w, 500, err.Error())
		return
	}
	s.epg.MarkDirty() // their profiles are gone
	writeJSON(w, map[string]bool{"ok": true})
}
