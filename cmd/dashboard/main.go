package main

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"html/template"
	"io"
	"log"
	"net/http"
	"os"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/glebarez/sqlite"
	"github.com/go-chi/chi/v5"
	"github.com/starfederation/datastar-go/datastar"
	"golang.org/x/crypto/bcrypt"
	"gorm.io/gorm"
)

const (
	roleAdmin = "admin"
	roleUser  = "user"
)

type endpoint struct {
	ID      uint   `gorm:"primaryKey"`
	Name    string `gorm:"size:100;not null;uniqueIndex"`
	BaseURL string `gorm:"size:255;not null;uniqueIndex"`
	Token   string `gorm:"size:255"`
}

type user struct {
	ID           uint   `gorm:"primaryKey"`
	Username     string `gorm:"size:100;not null;uniqueIndex"`
	PasswordHash string `gorm:"size:255;not null"`
	Role         string `gorm:"size:20;not null"`
}

type domain struct {
	ID     uint   `gorm:"primaryKey"`
	Name   string `gorm:"size:255;not null;uniqueIndex"`
	UserID uint   `gorm:"index;not null"`
}

type session struct {
	ID        uint      `gorm:"primaryKey"`
	Token     string    `gorm:"size:64;not null;uniqueIndex"`
	UserID    uint      `gorm:"index;not null"`
	ExpiresAt time.Time `gorm:"index;not null"`
}

type edgeZoneSnapshot struct {
	ID         uint      `gorm:"primaryKey"`
	EndpointID uint      `gorm:"index;not null"`
	Zone       string    `gorm:"size:255;index;not null"`
	NSJSON     string    `gorm:"type:text;not null"`
	SOATTL     uint32    `gorm:"not null"`
	SyncedAt   time.Time `gorm:"index;not null"`
}

type edgeRecordSnapshot struct {
	ID         uint      `gorm:"primaryKey"`
	EndpointID uint      `gorm:"index;not null"`
	Name       string    `gorm:"size:255;index;not null"`
	Type       string    `gorm:"size:16;index;not null"`
	Value      string    `gorm:"type:text;not null"`
	TTL        uint32    `gorm:"not null"`
	Zone       string    `gorm:"size:255;index;not null"`
	SyncedAt   time.Time `gorm:"index;not null"`
}

type server struct {
	db         *gorm.DB
	httpClient *http.Client
	tplLogin   *template.Template
	tplApp     *template.Template
	tplNotice  *template.Template
	tplResults *template.Template
	tplRecords *template.Template
	syncEvery  time.Duration
}

type ctxKey string

const ctxUserKey = ctxKey("user")

type actionResult struct {
	Endpoint string
	Action   string
	Success  bool
	Status   int
	Body     string
	Error    string
}

type endpointState struct {
	Endpoint string
	Success  bool
	Error    string
	Zones    []zoneView
	Records  []recordView
}

type zoneView struct {
	Zone   string
	NS     []string
	SOATTL uint32
}

type recordView struct {
	Name  string
	Type  string
	Value string
	TTL   uint32
	Zone  string
}

type userRecordView struct {
	Endpoint string
	Name     string
	Type     string
	Value    string
	TTL      uint32
	Zone     string
	SyncedAt string
}

type userGroupedRow struct {
	Host      string
	Type      string
	Values    string
	TTL       uint32
	Endpoints string
	SyncedAt  string
}

type userDomainRecords struct {
	Zone string
	Rows []userGroupedRow
}

type appData struct {
	User        user
	Message     string
	Results     []actionResult
	States      []endpointState
	UserGrouped []userDomainRecords
	RecordsInfo string
	Endpoints   []endpoint
	Users       []user
	AllDomains  []domain
	UserDomains []domain
	Now         string
}

func main() {
	listen := envOrDefault("DASHBOARD_LISTEN", ":8090")
	dbPath := envOrDefault("DASHBOARD_DB", "dashboard.db")
	adminUser := envOrDefault("DASHBOARD_ADMIN_USER", "admin")
	adminPass := envOrDefault("DASHBOARD_ADMIN_PASSWORD", "admin")
	syncEvery := envOrDefaultDuration("DASHBOARD_SYNC_INTERVAL", 60*time.Second)

	db, err := gorm.Open(sqlite.Open(dbPath), &gorm.Config{})
	if err != nil {
		log.Fatalf("open dashboard db failed: %v", err)
	}
	if err := db.AutoMigrate(&endpoint{}, &user{}, &domain{}, &session{}, &edgeZoneSnapshot{}, &edgeRecordSnapshot{}); err != nil {
		log.Fatalf("dashboard migrate failed: %v", err)
	}

	if err := ensureAdmin(db, adminUser, adminPass); err != nil {
		log.Fatalf("ensure admin failed: %v", err)
	}

	tplLogin := template.Must(template.New("login").Parse(loginHTML))
	tplApp := template.Must(template.New("app").Parse(appHTML))
	tplNotice := template.Must(template.New("notice").Parse(noticeFragmentHTML))
	tplResults := template.Must(template.New("results").Parse(resultsFragmentHTML))
	tplRecords := template.Must(template.New("records").Parse(recordsFragmentHTML))

	s := &server{
		db: db,
		httpClient: &http.Client{
			Timeout: 5 * time.Second,
		},
		tplLogin:   tplLogin,
		tplApp:     tplApp,
		tplNotice:  tplNotice,
		tplResults: tplResults,
		tplRecords: tplRecords,
		syncEvery:  syncEvery,
	}

	go s.periodicEdgeSync()

	r := chi.NewRouter()
	r.Get("/login", s.handleLoginPage)
	r.Post("/login", s.handleLoginPost)

	r.Group(func(r chi.Router) {
		r.Use(s.requireAuth)
		r.Get("/", s.handleApp)
		r.Get("/app", s.handleApp)
		r.Post("/logout", s.handleLogout)

		r.Post("/actions/user/park", s.handleUserPark)
		r.Post("/actions/user/record", s.handleUserRecord)
		r.Post("/actions/user/records-refresh", s.handleUserRefreshRecords)
		r.Post("/sse/user/park", s.handleSSEUserPark)
		r.Post("/sse/user/record", s.handleSSEUserRecord)
		r.Post("/sse/user/records-refresh", s.handleSSEUserRefreshRecords)

		r.Group(func(r chi.Router) {
			r.Use(s.requireAdmin)
			r.Post("/admin/endpoints/add", s.handleAdminEndpointAdd)
			r.Post("/admin/endpoints/delete", s.handleAdminEndpointDelete)
			r.Post("/admin/users/add", s.handleAdminUserAdd)
			r.Post("/admin/domains/assign", s.handleAdminDomainAssign)
			r.Post("/admin/domains/transfer", s.handleAdminDomainTransfer)
			r.Post("/admin/actions/query-state", s.handleAdminQueryState)
			r.Post("/admin/actions/zone-upsert", s.handleAdminZoneUpsert)
			r.Post("/admin/actions/record-upsert", s.handleAdminRecordUpsert)
			r.Post("/admin/actions/record-add", s.handleAdminRecordAdd)
			r.Post("/admin/actions/record-remove", s.handleAdminRecordRemove)
			r.Post("/admin/actions/record-delete", s.handleAdminRecordDelete)
		})
	})

	log.Printf("dashboard listening on %s", listen)
	if err := http.ListenAndServe(listen, r); err != nil {
		log.Fatalf("dashboard server failed: %v", err)
	}
}

func ensureAdmin(db *gorm.DB, username, password string) error {
	var existing user
	err := db.Where("username = ?", username).First(&existing).Error
	if err == nil {
		return nil
	}
	if err != nil && err != gorm.ErrRecordNotFound {
		return err
	}

	hash, err := bcrypt.GenerateFromPassword([]byte(password), bcrypt.DefaultCost)
	if err != nil {
		return err
	}
	return db.Create(&user{Username: username, PasswordHash: string(hash), Role: roleAdmin}).Error
}

func (s *server) handleLoginPage(w http.ResponseWriter, r *http.Request) {
	_ = s.tplLogin.Execute(w, map[string]any{"Message": strings.TrimSpace(r.URL.Query().Get("msg"))})
}

func (s *server) handleLoginPost(w http.ResponseWriter, r *http.Request) {
	username := strings.TrimSpace(r.FormValue("username"))
	password := r.FormValue("password")

	var u user
	if err := s.db.Where("username = ?", username).First(&u).Error; err != nil {
		http.Redirect(w, r, "/login?msg=invalid+credentials", http.StatusSeeOther)
		return
	}
	if err := bcrypt.CompareHashAndPassword([]byte(u.PasswordHash), []byte(password)); err != nil {
		http.Redirect(w, r, "/login?msg=invalid+credentials", http.StatusSeeOther)
		return
	}

	tok, err := randomToken(32)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	ss := session{Token: tok, UserID: u.ID, ExpiresAt: time.Now().UTC().Add(24 * time.Hour)}
	if err := s.db.Create(&ss).Error; err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	http.SetCookie(w, &http.Cookie{
		Name:     "dash_session",
		Value:    tok,
		Path:     "/",
		HttpOnly: true,
		SameSite: http.SameSiteLaxMode,
		Secure:   false,
		Expires:  ss.ExpiresAt,
	})
	http.Redirect(w, r, "/app", http.StatusSeeOther)
}

func (s *server) handleLogout(w http.ResponseWriter, r *http.Request) {
	c, err := r.Cookie("dash_session")
	if err == nil {
		_ = s.db.Where("token = ?", c.Value).Delete(&session{}).Error
	}
	http.SetCookie(w, &http.Cookie{Name: "dash_session", Value: "", Path: "/", MaxAge: -1, HttpOnly: true})
	http.Redirect(w, r, "/login?msg=logged+out", http.StatusSeeOther)
}

func (s *server) requireAuth(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		c, err := r.Cookie("dash_session")
		if err != nil || strings.TrimSpace(c.Value) == "" {
			http.Redirect(w, r, "/login?msg=please+login", http.StatusSeeOther)
			return
		}

		var ss session
		if err := s.db.Where("token = ?", c.Value).First(&ss).Error; err != nil {
			http.Redirect(w, r, "/login?msg=session+expired", http.StatusSeeOther)
			return
		}
		if time.Now().UTC().After(ss.ExpiresAt) {
			_ = s.db.Delete(&session{}, ss.ID).Error
			http.Redirect(w, r, "/login?msg=session+expired", http.StatusSeeOther)
			return
		}

		var u user
		if err := s.db.First(&u, ss.UserID).Error; err != nil {
			http.Redirect(w, r, "/login?msg=user+not+found", http.StatusSeeOther)
			return
		}

		ctx := context.WithValue(r.Context(), ctxUserKey, u)
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}

func (s *server) requireAdmin(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		u := currentUser(r)
		if u.Role != roleAdmin {
			s.renderApp(w, r, "forbidden: admin role required", nil, nil)
			return
		}
		next.ServeHTTP(w, r)
	})
}

func currentUser(r *http.Request) user {
	v, _ := r.Context().Value(ctxUserKey).(user)
	return v
}

func (s *server) handleApp(w http.ResponseWriter, r *http.Request) {
	s.renderApp(w, r, "", nil, nil)
}

func (s *server) renderApp(w http.ResponseWriter, r *http.Request, message string, results []actionResult, states []endpointState) {
	u := currentUser(r)

	allDomains, _ := s.listAllDomains()
	userDomains, _ := s.listUserDomains(u)
	endpoints, _ := s.listEndpoints()
	users, _ := s.listUsers()
	userRecords := []userRecordView{}
	userGrouped := []userDomainRecords{}
	recordsInfo := ""
	if u.Role != roleAdmin {
		var err error
		userRecords, err = s.listUserRecords(u)
		if err != nil {
			recordsInfo = "failed to load records: " + err.Error()
		} else {
			userGrouped = groupUserRecords(userRecords)
		}
	}

	_ = s.tplApp.Execute(w, appData{
		User:        u,
		Message:     message,
		Results:     results,
		States:      states,
		UserGrouped: userGrouped,
		RecordsInfo: recordsInfo,
		Endpoints:   endpoints,
		Users:       users,
		AllDomains:  allDomains,
		UserDomains: userDomains,
		Now:         time.Now().UTC().Format(time.RFC3339),
	})
}

func (s *server) patchUserSSE(w http.ResponseWriter, r *http.Request, u user, message string, results []actionResult) {
	sse := datastar.NewSSE(w, r)
	_ = sse.PatchElements(s.renderNoticeFragment(message))
	_ = sse.PatchElements(s.renderResultsFragment(results))
	_ = sse.PatchElements(s.renderRecordsFragment(u))
}

func (s *server) renderNoticeFragment(message string) string {
	var buf bytes.Buffer
	if err := s.tplNotice.Execute(&buf, strings.TrimSpace(message)); err != nil {
		log.Printf("dashboard render notice failed: %v", err)
		return `<div id="flash-notice"><div class="notice">render error</div></div>`
	}
	return buf.String()
}

func (s *server) renderResultsFragment(results []actionResult) string {
	var buf bytes.Buffer
	if err := s.tplResults.Execute(&buf, results); err != nil {
		log.Printf("dashboard render results failed: %v", err)
		return `<section id="action-results-section" class="panel" style="display:none"></section>`
	}
	return buf.String()
}

func (s *server) renderRecordsFragment(u user) string {
	if u.Role == roleAdmin {
		return `<div id="user-records-section"></div>`
	}
	records := []userRecordView{}
	grouped := []userDomainRecords{}
	info := ""
	recs, err := s.listUserRecords(u)
	if err != nil {
		info = "failed to load records: " + err.Error()
	} else {
		records = recs
		grouped = groupUserRecords(records)
	}
	var buf bytes.Buffer
	data := struct {
		RecordsInfo string
		UserGrouped []userDomainRecords
	}{
		RecordsInfo: info,
		UserGrouped: grouped,
	}
	if err := s.tplRecords.Execute(&buf, data); err != nil {
		log.Printf("dashboard render records failed: %v", err)
		return `<div id="user-records-section"><div class="helper">render error</div></div>`
	}
	return buf.String()
}

func (s *server) handleAdminEndpointAdd(w http.ResponseWriter, r *http.Request) {
	e := endpoint{
		Name:    strings.TrimSpace(r.FormValue("name")),
		BaseURL: strings.TrimRight(strings.TrimSpace(r.FormValue("base_url")), "/"),
		Token:   strings.TrimSpace(r.FormValue("token")),
	}
	if e.Name == "" || e.BaseURL == "" {
		s.renderApp(w, r, "name and base URL required", nil, nil)
		return
	}
	if err := s.db.Create(&e).Error; err != nil {
		s.renderApp(w, r, "endpoint add failed: "+err.Error(), nil, nil)
		return
	}
	s.renderApp(w, r, "endpoint added", nil, nil)
}

func (s *server) handleAdminEndpointDelete(w http.ResponseWriter, r *http.Request) {
	id := strings.TrimSpace(r.FormValue("id"))
	if id == "" {
		s.renderApp(w, r, "endpoint id missing", nil, nil)
		return
	}
	if err := s.db.Delete(&endpoint{}, "id = ?", id).Error; err != nil {
		s.renderApp(w, r, "endpoint delete failed: "+err.Error(), nil, nil)
		return
	}
	s.renderApp(w, r, "endpoint deleted", nil, nil)
}

func (s *server) handleAdminUserAdd(w http.ResponseWriter, r *http.Request) {
	username := strings.TrimSpace(r.FormValue("username"))
	password := r.FormValue("password")
	role := strings.TrimSpace(r.FormValue("role"))
	if role != roleAdmin {
		role = roleUser
	}
	if username == "" || password == "" {
		s.renderApp(w, r, "username and password required", nil, nil)
		return
	}
	hash, err := bcrypt.GenerateFromPassword([]byte(password), bcrypt.DefaultCost)
	if err != nil {
		s.renderApp(w, r, "password hash failed: "+err.Error(), nil, nil)
		return
	}
	if err := s.db.Create(&user{Username: username, PasswordHash: string(hash), Role: role}).Error; err != nil {
		s.renderApp(w, r, "user add failed: "+err.Error(), nil, nil)
		return
	}
	s.renderApp(w, r, "user added", nil, nil)
}

func (s *server) handleAdminDomainAssign(w http.ResponseWriter, r *http.Request) {
	name := normalizeDomain(strings.TrimSpace(r.FormValue("domain")))
	username := strings.TrimSpace(r.FormValue("username"))
	if name == "" || username == "" {
		s.renderApp(w, r, "domain and username required", nil, nil)
		return
	}
	var u user
	if err := s.db.Where("username = ?", username).First(&u).Error; err != nil {
		s.renderApp(w, r, "user not found", nil, nil)
		return
	}
	if err := s.db.Where("name = ?", name).Delete(&domain{}).Error; err != nil {
		s.renderApp(w, r, "domain cleanup failed: "+err.Error(), nil, nil)
		return
	}
	if err := s.db.Create(&domain{Name: name, UserID: u.ID}).Error; err != nil {
		s.renderApp(w, r, "domain assign failed: "+err.Error(), nil, nil)
		return
	}
	s.renderApp(w, r, "domain assigned", nil, nil)
}

func (s *server) handleAdminDomainTransfer(w http.ResponseWriter, r *http.Request) {
	name := normalizeDomain(strings.TrimSpace(r.FormValue("domain")))
	toUser := strings.TrimSpace(r.FormValue("to_username"))
	if name == "" || toUser == "" {
		s.renderApp(w, r, "domain and to_username required", nil, nil)
		return
	}

	var u user
	if err := s.db.Where("username = ?", toUser).First(&u).Error; err != nil {
		s.renderApp(w, r, "target user not found", nil, nil)
		return
	}

	if err := s.db.Where("name = ?", name).Delete(&domain{}).Error; err != nil {
		s.renderApp(w, r, "domain transfer cleanup failed: "+err.Error(), nil, nil)
		return
	}
	if err := s.db.Create(&domain{Name: name, UserID: u.ID}).Error; err != nil {
		s.renderApp(w, r, "domain transfer failed: "+err.Error(), nil, nil)
		return
	}
	s.renderApp(w, r, "domain transferred to "+toUser, nil, nil)
}

func (s *server) handleUserPark(w http.ResponseWriter, r *http.Request) {
	u := currentUser(r)
	msg, results := s.doUserPark(r, u)
	s.renderApp(w, r, msg, results, nil)
}

func (s *server) handleUserRecord(w http.ResponseWriter, r *http.Request) {
	u := currentUser(r)
	msg, results := s.doUserRecord(r, u)
	s.renderApp(w, r, msg, results, nil)
}

func (s *server) handleUserRefreshRecords(w http.ResponseWriter, r *http.Request) {
	synced, failed := s.syncEdgeSnapshots()
	if failed > 0 {
		s.renderApp(w, r, fmt.Sprintf("records refresh finished: %d synced, %d failed", synced, failed), nil, nil)
		return
	}
	s.renderApp(w, r, fmt.Sprintf("records refreshed from edges: %d endpoints synced", synced), nil, nil)
}

func (s *server) handleSSEUserPark(w http.ResponseWriter, r *http.Request) {
	u := currentUser(r)
	msg, results := s.doUserPark(r, u)
	s.patchUserSSE(w, r, u, msg, results)
}

func (s *server) handleSSEUserRecord(w http.ResponseWriter, r *http.Request) {
	u := currentUser(r)
	msg, results := s.doUserRecord(r, u)
	s.patchUserSSE(w, r, u, msg, results)
}

func (s *server) handleSSEUserRefreshRecords(w http.ResponseWriter, r *http.Request) {
	u := currentUser(r)
	synced, failed := s.syncEdgeSnapshots()
	msg := fmt.Sprintf("records refreshed from edges: %d endpoints synced", synced)
	if failed > 0 {
		msg = fmt.Sprintf("records refresh finished: %d synced, %d failed", synced, failed)
	}
	s.patchUserSSE(w, r, u, msg, nil)
}

func (s *server) doUserPark(r *http.Request, u user) (string, []actionResult) {
	domainName := normalizeDomain(strings.TrimSpace(r.FormValue("domain")))
	a := strings.TrimSpace(r.FormValue("apex_a"))
	aaaa := strings.TrimSpace(r.FormValue("apex_aaaa"))
	if domainName == "" {
		return "domain required", nil
	}
	if !s.userCanManageDomain(u, domainName) {
		return "forbidden: you are not assigned to this domain. Ask admin to assign/transfer it.", nil
	}
	results := make([]actionResult, 0, 2)
	if a != "" {
		body := map[string]any{"type": "A", "ip": a, "ttl": 60, "zone": domainName}
		results = append(results, s.broadcastJSON(http.MethodPut, "/v1/records/"+domainName, body)...)
	}
	if aaaa != "" {
		body := map[string]any{"type": "AAAA", "ip": aaaa, "ttl": 60, "zone": domainName}
		results = append(results, s.broadcastJSON(http.MethodPut, "/v1/records/"+domainName, body)...)
	}
	if len(results) == 0 {
		return "set at least one of apex A/AAAA", nil
	}
	_, _ = s.syncEdgeSnapshots()
	return "park action executed", results
}

func (s *server) doUserRecord(r *http.Request, u user) (string, []actionResult) {
	domainName := normalizeDomain(strings.TrimSpace(r.FormValue("domain")))
	host := strings.TrimSpace(r.FormValue("host"))
	rtype := strings.ToUpper(strings.TrimSpace(r.FormValue("type")))
	value := strings.TrimSpace(r.FormValue("value"))
	mode := strings.ToLower(strings.TrimSpace(r.FormValue("mode")))
	if mode == "" {
		mode = "set"
	}
	if rtype != "A" && rtype != "AAAA" {
		return "type must be A or AAAA", nil
	}
	if domainName == "" || host == "" || value == "" {
		return "domain, host, type, value required", nil
	}
	if !s.userCanManageDomain(u, domainName) {
		return "forbidden: you are not assigned to this domain. Ask admin to assign/transfer it.", nil
	}

	name := buildHostName(host, domainName)
	if !nameWithinDomain(name, domainName) {
		return "host not within selected domain", nil
	}

	body := map[string]any{"type": rtype, "ip": value, "ttl": 60, "zone": domainName}
	path := "/v1/records/" + name
	method := http.MethodPut
	if mode == "add" {
		path += "/add"
		method = http.MethodPost
	}
	if mode == "remove" {
		path += "/remove"
		method = http.MethodPost
	}
	results := s.broadcastJSON(method, path, body)
	_, _ = s.syncEdgeSnapshots()
	return "record action executed", results
}

func (s *server) handleAdminQueryState(w http.ResponseWriter, r *http.Request) {
	states := s.queryStateAll()
	s.renderApp(w, r, "query-state done", nil, states)
}

func (s *server) handleAdminZoneUpsert(w http.ResponseWriter, r *http.Request) {
	zone := normalizeDomain(strings.TrimSpace(r.FormValue("zone")))
	ns1 := strings.TrimSpace(r.FormValue("ns1"))
	ns2 := strings.TrimSpace(r.FormValue("ns2"))
	if zone == "" || ns1 == "" || ns2 == "" {
		s.renderApp(w, r, "zone, ns1 and ns2 required", nil, nil)
		return
	}
	body := map[string]any{"ns": []string{ns1, ns2}, "soa_ttl": 60}
	results := s.broadcastJSON(http.MethodPut, "/v1/zones/"+zone, body)
	s.renderApp(w, r, "zone upsert done", results, nil)
}

func (s *server) handleAdminRecordUpsert(w http.ResponseWriter, r *http.Request) {
	s.handleAdminRecordAction(w, r, "set")
}

func (s *server) handleAdminRecordAdd(w http.ResponseWriter, r *http.Request) {
	s.handleAdminRecordAction(w, r, "add")
}

func (s *server) handleAdminRecordRemove(w http.ResponseWriter, r *http.Request) {
	s.handleAdminRecordAction(w, r, "remove")
}

func (s *server) handleAdminRecordDelete(w http.ResponseWriter, r *http.Request) {
	name := normalizeDomain(strings.TrimSpace(r.FormValue("name")))
	rtype := strings.ToUpper(strings.TrimSpace(r.FormValue("type")))
	if name == "" {
		s.renderApp(w, r, "name required", nil, nil)
		return
	}
	path := "/v1/records/" + name
	if rtype != "" {
		path += "?type=" + rtype
	}
	results := s.broadcastJSON(http.MethodDelete, path, nil)
	s.renderApp(w, r, "record delete done", results, nil)
}

func (s *server) handleAdminRecordAction(w http.ResponseWriter, r *http.Request, mode string) {
	name := normalizeDomain(strings.TrimSpace(r.FormValue("name")))
	rtype := strings.ToUpper(strings.TrimSpace(r.FormValue("type")))
	value := strings.TrimSpace(r.FormValue("value"))
	zone := normalizeDomain(strings.TrimSpace(r.FormValue("zone")))
	if name == "" || rtype == "" || value == "" {
		s.renderApp(w, r, "name, type, value required", nil, nil)
		return
	}
	body := map[string]any{"type": rtype, "ttl": 60, "zone": zone}
	switch rtype {
	case "TXT":
		body["text"] = value
	case "CNAME":
		body["target"] = value
	case "MX":
		p, tgt := parseMX(value)
		body["priority"] = p
		body["target"] = tgt
	default:
		body["ip"] = value
	}

	path := "/v1/records/" + name
	method := http.MethodPut
	if mode == "add" {
		path += "/add"
		method = http.MethodPost
	}
	if mode == "remove" {
		path += "/remove"
		method = http.MethodPost
	}
	results := s.broadcastJSON(method, path, body)
	s.renderApp(w, r, "record action done", results, nil)
}

func (s *server) userCanManageDomain(u user, name string) bool {
	if u.Role == roleAdmin {
		return true
	}
	var d domain
	err := s.db.Where("name = ? AND user_id = ?", normalizeDomain(name), u.ID).First(&d).Error
	return err == nil
}

func nameWithinDomain(name, domainName string) bool {
	name = normalizeDomain(name)
	domainName = normalizeDomain(domainName)
	if name == domainName {
		return true
	}
	return strings.HasSuffix(name, "."+domainName)
}

func buildHostName(host, domainName string) string {
	h := normalizeDomain(host)
	d := normalizeDomain(domainName)
	if h == "@" || h == d {
		return d
	}
	if strings.HasSuffix(h, "."+d) {
		return h
	}
	return h + "." + d
}

func (s *server) listEndpoints() ([]endpoint, error) {
	var out []endpoint
	err := s.db.Order("name asc").Find(&out).Error
	return out, err
}

func (s *server) listUsers() ([]user, error) {
	var out []user
	err := s.db.Order("username asc").Find(&out).Error
	return out, err
}

func (s *server) listAllDomains() ([]domain, error) {
	var out []domain
	err := s.db.Order("name asc").Find(&out).Error
	return out, err
}

func (s *server) listUserDomains(u user) ([]domain, error) {
	var out []domain
	if u.Role == roleAdmin {
		return s.listAllDomains()
	}
	err := s.db.Where("user_id = ?", u.ID).Order("name asc").Find(&out).Error
	return out, err
}

func (s *server) listUserRecords(u user) ([]userRecordView, error) {
	if u.Role == roleAdmin {
		return []userRecordView{}, nil
	}
	userDomains, err := s.listUserDomains(u)
	if err != nil {
		return nil, err
	}
	if len(userDomains) == 0 {
		return []userRecordView{}, nil
	}

	zoneNames := make([]string, 0, len(userDomains))
	zoneSet := make(map[string]struct{}, len(userDomains))
	for _, d := range userDomains {
		z := normalizeDomain(d.Name)
		zoneNames = append(zoneNames, z)
		zoneSet[z] = struct{}{}
	}

	endpoints, err := s.listEndpoints()
	if err != nil {
		return nil, err
	}
	endpointNameByID := make(map[uint]string, len(endpoints))
	for _, ep := range endpoints {
		endpointNameByID[ep.ID] = ep.Name
	}

	var rows []edgeRecordSnapshot
	if err := s.db.Where("zone IN ? OR zone = ''", zoneNames).Order("zone asc, name asc, type asc, value asc").Find(&rows).Error; err != nil {
		return nil, err
	}

	out := make([]userRecordView, 0, len(rows))
	for _, row := range rows {
		zone := normalizeDomain(row.Zone)
		name := normalizeDomain(row.Name)
		if !recordBelongsToUserDomains(zone, name, zoneSet) {
			continue
		}
		if zone == "" {
			zone = inferZoneFromName(name, zoneNames)
		}

		epName := endpointNameByID[row.EndpointID]
		if epName == "" {
			epName = fmt.Sprintf("endpoint#%d", row.EndpointID)
		}
		out = append(out, userRecordView{
			Endpoint: epName,
			Name:     name,
			Type:     row.Type,
			Value:    row.Value,
			TTL:      row.TTL,
			Zone:     zone,
			SyncedAt: row.SyncedAt.UTC().Format(time.RFC3339),
		})
	}

	sort.Slice(out, func(i, j int) bool {
		if out[i].Zone != out[j].Zone {
			return out[i].Zone < out[j].Zone
		}
		if out[i].Name != out[j].Name {
			return out[i].Name < out[j].Name
		}
		if out[i].Type != out[j].Type {
			return out[i].Type < out[j].Type
		}
		if out[i].Value != out[j].Value {
			return out[i].Value < out[j].Value
		}
		return out[i].Endpoint < out[j].Endpoint
	})
	if len(out) == 0 {
		live, err := s.listUserRecordsLive(zoneNames, zoneSet)
		if err == nil && len(live) > 0 {
			return live, nil
		}
	}

	return out, nil
}

func (s *server) listUserRecordsLive(zoneNames []string, zoneSet map[string]struct{}) ([]userRecordView, error) {
	eps, err := s.listEndpoints()
	if err != nil {
		return nil, err
	}
	if len(eps) == 0 {
		return []userRecordView{}, nil
	}

	now := time.Now().UTC().Format(time.RFC3339)
	out := make([]userRecordView, 0)
	for _, ep := range eps {
		st := s.queryEndpointState(ep)
		if !st.Success {
			continue
		}
		for _, rec := range st.Records {
			zone := normalizeDomain(rec.Zone)
			name := normalizeDomain(rec.Name)
			if !recordBelongsToUserDomains(zone, name, zoneSet) {
				continue
			}
			if zone == "" {
				zone = inferZoneFromName(name, zoneNames)
			}
			out = append(out, userRecordView{
				Endpoint: ep.Name,
				Name:     name,
				Type:     rec.Type,
				Value:    rec.Value,
				TTL:      rec.TTL,
				Zone:     zone,
				SyncedAt: now,
			})
		}
	}

	sort.Slice(out, func(i, j int) bool {
		if out[i].Zone != out[j].Zone {
			return out[i].Zone < out[j].Zone
		}
		if out[i].Name != out[j].Name {
			return out[i].Name < out[j].Name
		}
		if out[i].Type != out[j].Type {
			return out[i].Type < out[j].Type
		}
		if out[i].Value != out[j].Value {
			return out[i].Value < out[j].Value
		}
		return out[i].Endpoint < out[j].Endpoint
	})

	return out, nil
}

func recordBelongsToUserDomains(zone, name string, zoneSet map[string]struct{}) bool {
	if zone != "" {
		_, ok := zoneSet[zone]
		return ok
	}
	for z := range zoneSet {
		if nameWithinDomain(name, z) {
			return true
		}
	}
	return false
}

func inferZoneFromName(name string, zoneNames []string) string {
	best := ""
	for _, z := range zoneNames {
		if nameWithinDomain(name, z) {
			if len(z) > len(best) {
				best = z
			}
		}
	}
	return best
}

func groupUserRecords(records []userRecordView) []userDomainRecords {
	type aggRow struct {
		Host       string
		Type       string
		TTL        uint32
		Values     map[string]struct{}
		Endpoints  map[string]struct{}
		LatestSync string
	}
	type zoneAgg struct {
		Zone string
		Rows map[string]*aggRow
	}

	zoneMap := map[string]*zoneAgg{}
	for _, rec := range records {
		zg := zoneMap[rec.Zone]
		if zg == nil {
			zg = &zoneAgg{Zone: rec.Zone, Rows: map[string]*aggRow{}}
			zoneMap[rec.Zone] = zg
		}
		key := rec.Name + "|" + rec.Type
		ar := zg.Rows[key]
		if ar == nil {
			ar = &aggRow{Host: rec.Name, Type: rec.Type, TTL: rec.TTL, Values: map[string]struct{}{}, Endpoints: map[string]struct{}{}, LatestSync: rec.SyncedAt}
			zg.Rows[key] = ar
		}
		ar.Values[rec.Value] = struct{}{}
		ar.Endpoints[rec.Endpoint] = struct{}{}
		if rec.TTL < ar.TTL {
			ar.TTL = rec.TTL
		}
		if rec.SyncedAt > ar.LatestSync {
			ar.LatestSync = rec.SyncedAt
		}
	}

	zones := make([]string, 0, len(zoneMap))
	for z := range zoneMap {
		zones = append(zones, z)
	}
	sort.Strings(zones)

	out := make([]userDomainRecords, 0, len(zones))
	for _, zone := range zones {
		zg := zoneMap[zone]
		rows := make([]userGroupedRow, 0, len(zg.Rows))
		for _, ar := range zg.Rows {
			vals := make([]string, 0, len(ar.Values))
			for v := range ar.Values {
				vals = append(vals, v)
			}
			sort.Strings(vals)

			eps := make([]string, 0, len(ar.Endpoints))
			for ep := range ar.Endpoints {
				eps = append(eps, ep)
			}
			sort.Strings(eps)

			rows = append(rows, userGroupedRow{
				Host:      ar.Host,
				Type:      ar.Type,
				Values:    strings.Join(vals, ", "),
				TTL:       ar.TTL,
				Endpoints: strings.Join(eps, ", "),
				SyncedAt:  ar.LatestSync,
			})
		}

		sort.Slice(rows, func(i, j int) bool {
			if rows[i].Host != rows[j].Host {
				return rows[i].Host < rows[j].Host
			}
			return rows[i].Type < rows[j].Type
		})

		out = append(out, userDomainRecords{Zone: zone, Rows: rows})
	}

	return out
}

func (s *server) broadcastJSON(method, path string, payload any) []actionResult {
	eps, err := s.listEndpoints()
	if err != nil {
		return []actionResult{{Action: method + " " + path, Error: err.Error()}}
	}
	if len(eps) == 0 {
		return []actionResult{{Action: method + " " + path, Error: "no endpoints configured"}}
	}

	results := make([]actionResult, len(eps))
	var wg sync.WaitGroup
	for i, ep := range eps {
		wg.Add(1)
		go func(i int, ep endpoint) {
			defer wg.Done()
			results[i] = s.callEndpoint(ep, method, path, payload)
		}(i, ep)
	}
	wg.Wait()
	return results
}

func (s *server) callEndpoint(ep endpoint, method, path string, payload any) actionResult {
	res := actionResult{Endpoint: ep.Name, Action: method + " " + path}
	var body []byte
	if payload != nil {
		var err error
		body, err = json.Marshal(payload)
		if err != nil {
			res.Error = err.Error()
			return res
		}
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, method, ep.BaseURL+path, bytes.NewReader(body))
	if err != nil {
		res.Error = err.Error()
		return res
	}
	if payload != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if ep.Token != "" {
		req.Header.Set("Authorization", "Bearer "+ep.Token)
	}

	resp, err := s.httpClient.Do(req)
	if err != nil {
		res.Error = err.Error()
		return res
	}
	defer resp.Body.Close()
	buf, _ := io.ReadAll(io.LimitReader(resp.Body, 2048))
	res.Status = resp.StatusCode
	res.Body = strings.TrimSpace(string(buf))
	res.Success = resp.StatusCode >= 200 && resp.StatusCode < 300
	if !res.Success && res.Body == "" {
		res.Error = "non-success status"
	}
	return res
}

func (s *server) queryStateAll() []endpointState {
	eps, err := s.listEndpoints()
	if err != nil {
		return []endpointState{{Error: err.Error()}}
	}
	if len(eps) == 0 {
		return []endpointState{{Error: "no endpoints configured"}}
	}
	out := make([]endpointState, len(eps))
	var wg sync.WaitGroup
	for i, ep := range eps {
		wg.Add(1)
		go func(i int, ep endpoint) {
			defer wg.Done()
			st := s.queryEndpointState(ep)
			if st.Success {
				if err := s.saveSnapshot(ep, st); err != nil {
					log.Printf("dashboard snapshot save failed endpoint=%s err=%v", ep.Name, err)
				}
				out[i] = st
				return
			}
			cached, ok := s.loadSnapshot(ep)
			if ok {
				cached.Error = "live query failed, showing cached snapshot: " + st.Error
				out[i] = cached
				return
			}
			out[i] = st
		}(i, ep)
	}
	wg.Wait()
	return out
}

func (s *server) periodicEdgeSync() {
	if s.syncEvery <= 0 {
		return
	}
	ticker := time.NewTicker(s.syncEvery)
	defer ticker.Stop()

	syncOK, syncFail := s.syncEdgeSnapshots()
	if syncOK > 0 || syncFail > 0 {
		log.Printf("dashboard periodic sync: %d endpoints synced, %d failed", syncOK, syncFail)
	}
	for range ticker.C {
		syncOK, syncFail = s.syncEdgeSnapshots()
		if syncOK > 0 || syncFail > 0 {
			log.Printf("dashboard periodic sync: %d endpoints synced, %d failed", syncOK, syncFail)
		}
	}
}

func (s *server) syncEdgeSnapshots() (int, int) {
	eps, err := s.listEndpoints()
	if err != nil {
		log.Printf("dashboard periodic sync: list endpoints failed: %v", err)
		return 0, 0
	}
	if len(eps) == 0 {
		return 0, 0
	}
	success := 0
	failed := 0
	for _, ep := range eps {
		st := s.queryEndpointState(ep)
		if !st.Success {
			log.Printf("dashboard periodic sync: endpoint=%s query failed: %s", ep.Name, st.Error)
			failed++
			continue
		}
		if err := s.saveSnapshot(ep, st); err != nil {
			log.Printf("dashboard periodic sync: endpoint=%s save failed: %v", ep.Name, err)
			failed++
			continue
		}
		success++
	}
	return success, failed
}

func (s *server) saveSnapshot(ep endpoint, st endpointState) error {
	now := time.Now().UTC()
	return s.db.Transaction(func(tx *gorm.DB) error {
		if err := tx.Where("endpoint_id = ?", ep.ID).Delete(&edgeZoneSnapshot{}).Error; err != nil {
			return err
		}
		if err := tx.Where("endpoint_id = ?", ep.ID).Delete(&edgeRecordSnapshot{}).Error; err != nil {
			return err
		}

		for _, z := range st.Zones {
			nsJSON, err := json.Marshal(z.NS)
			if err != nil {
				return err
			}
			row := edgeZoneSnapshot{EndpointID: ep.ID, Zone: z.Zone, NSJSON: string(nsJSON), SOATTL: z.SOATTL, SyncedAt: now}
			if err := tx.Create(&row).Error; err != nil {
				return err
			}
		}

		for _, rec := range st.Records {
			row := edgeRecordSnapshot{EndpointID: ep.ID, Name: rec.Name, Type: rec.Type, Value: rec.Value, TTL: rec.TTL, Zone: rec.Zone, SyncedAt: now}
			if err := tx.Create(&row).Error; err != nil {
				return err
			}
		}
		return nil
	})
}

func (s *server) loadSnapshot(ep endpoint) (endpointState, bool) {
	var zonesRows []edgeZoneSnapshot
	if err := s.db.Where("endpoint_id = ?", ep.ID).Find(&zonesRows).Error; err != nil {
		return endpointState{}, false
	}
	var recordsRows []edgeRecordSnapshot
	if err := s.db.Where("endpoint_id = ?", ep.ID).Find(&recordsRows).Error; err != nil {
		return endpointState{}, false
	}
	if len(zonesRows) == 0 && len(recordsRows) == 0 {
		return endpointState{}, false
	}

	out := endpointState{Endpoint: ep.Name, Success: true}
	for _, z := range zonesRows {
		ns := []string{}
		_ = json.Unmarshal([]byte(z.NSJSON), &ns)
		out.Zones = append(out.Zones, zoneView{Zone: z.Zone, NS: ns, SOATTL: z.SOATTL})
	}
	for _, rec := range recordsRows {
		out.Records = append(out.Records, recordView{Name: rec.Name, Type: rec.Type, Value: rec.Value, TTL: rec.TTL, Zone: rec.Zone})
	}
	return out, true
}

func (s *server) queryEndpointState(ep endpoint) endpointState {
	st := endpointState{Endpoint: ep.Name}

	var zr struct {
		Zones []struct {
			Zone   string   `json:"zone"`
			NS     []string `json:"ns"`
			SOATTL uint32   `json:"soa_ttl"`
		} `json:"zones"`
	}
	if err := s.fetchJSON(ep, "/v1/zones", &zr); err != nil {
		st.Error = "zones fetch failed: " + err.Error()
		return st
	}
	for _, z := range zr.Zones {
		st.Zones = append(st.Zones, zoneView{Zone: z.Zone, NS: z.NS, SOATTL: z.SOATTL})
	}

	var rr struct {
		Records []struct {
			Name     string `json:"name"`
			Type     string `json:"type"`
			IP       string `json:"ip"`
			Text     string `json:"text"`
			Target   string `json:"target"`
			Priority uint16 `json:"priority"`
			TTL      uint32 `json:"ttl"`
			Zone     string `json:"zone"`
		} `json:"records"`
	}
	if err := s.fetchJSON(ep, "/v1/records", &rr); err != nil {
		st.Error = "records fetch failed: " + err.Error()
		return st
	}

	for _, rec := range rr.Records {
		value := rec.IP
		switch rec.Type {
		case "TXT":
			value = rec.Text
		case "CNAME":
			value = rec.Target
		case "MX":
			value = fmt.Sprintf("%d %s", rec.Priority, rec.Target)
		}
		st.Records = append(st.Records, recordView{Name: rec.Name, Type: rec.Type, Value: value, TTL: rec.TTL, Zone: rec.Zone})
	}
	sort.Slice(st.Records, func(i, j int) bool {
		if st.Records[i].Name == st.Records[j].Name {
			return st.Records[i].Type < st.Records[j].Type
		}
		return st.Records[i].Name < st.Records[j].Name
	})
	st.Success = true
	return st
}

func (s *server) fetchJSON(ep endpoint, path string, out any) error {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, ep.BaseURL+path, nil)
	if err != nil {
		return err
	}
	if ep.Token != "" {
		req.Header.Set("Authorization", "Bearer "+ep.Token)
	}
	resp, err := s.httpClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		b, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		return fmt.Errorf("status=%d body=%s", resp.StatusCode, strings.TrimSpace(string(b)))
	}
	return json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(out)
}

func parseMX(v string) (int, string) {
	parts := strings.Fields(strings.TrimSpace(v))
	if len(parts) >= 2 {
		prio := 10
		_, _ = fmt.Sscanf(parts[0], "%d", &prio)
		return prio, strings.Join(parts[1:], " ")
	}
	return 10, v
}

func normalizeDomain(v string) string {
	v = strings.TrimSpace(strings.ToLower(v))
	v = strings.Trim(v, ".")
	return v
}

func randomToken(n int) (string, error) {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return hex.EncodeToString(b), nil
}

func envOrDefault(key, fallback string) string {
	v := strings.TrimSpace(os.Getenv(key))
	if v == "" {
		return fallback
	}
	return v
}

func envOrDefaultDuration(key string, fallback time.Duration) time.Duration {
	v := strings.TrimSpace(os.Getenv(key))
	if v == "" {
		return fallback
	}
	d, err := time.ParseDuration(v)
	if err != nil {
		log.Printf("invalid %s=%q, using default %s", key, v, fallback)
		return fallback
	}
	return d
}

const noticeFragmentHTML = `<div id="flash-notice">{{if .}}<div class="notice">{{.}}</div>{{end}}</div>`

const resultsFragmentHTML = `<section id="action-results-section" class="panel" {{if not .}}style="display:none"{{end}}>
  <h2>Action Results</h2>
  {{if .}}
  <table>
    <thead><tr><th>Endpoint</th><th>Action</th><th>Status</th><th>Response</th></tr></thead>
    <tbody>
      {{range .}}
      <tr>
        <td>{{.Endpoint}}</td>
        <td class="mono">{{.Action}}</td>
        <td>{{if .Success}}<span class="ok">OK {{.Status}}</span>{{else}}<span class="bad">FAIL {{.Status}}</span>{{end}}</td>
        <td class="mono">{{if .Error}}{{.Error}}{{else}}{{.Body}}{{end}}</td>
      </tr>
      {{end}}
    </tbody>
  </table>
  {{end}}
</section>`

const recordsFragmentHTML = `<div id="user-records-section">
  <h3 style="margin-top:8px">Current Records</h3>
  <div class="helper">Read-only view from the latest synced edge snapshots for your assigned domains.</div>
  <form method="post" action="/actions/user/records-refresh" data-on:submit__prevent="@post('/sse/user/records-refresh', {contentType: 'form'})" style="margin-top:8px; max-width:240px;">
    <button type="submit" class="subtle-btn">Refresh Records Now</button>
  </form>
  {{if .RecordsInfo}}<div class="notice" style="margin-top:8px">{{.RecordsInfo}}</div>{{end}}
  {{if .UserGrouped}}
  {{range .UserGrouped}}
  <div style="border:1px solid var(--line); border-radius:10px; padding:10px; margin-top:10px;">
    <div style="display:flex; align-items:center; justify-content:space-between; gap:8px; margin-bottom:6px;">
      <strong class="mono">{{.Zone}}</strong>
      <span class="helper">{{len .Rows}} host/type rows</span>
    </div>
    <table>
      <thead><tr><th>Host</th><th>Type</th><th>Values</th><th>TTL</th><th>Endpoints</th><th>Synced</th></tr></thead>
      <tbody>
        {{range .Rows}}
        <tr>
          <td class="mono">{{.Host}}</td>
          <td>{{.Type}}</td>
          <td class="mono">{{.Values}}</td>
          <td>{{.TTL}}</td>
          <td>{{.Endpoints}}</td>
          <td class="mono">{{.SyncedAt}}</td>
        </tr>
        {{end}}
      </tbody>
    </table>
  </div>
  {{end}}
  {{else}}
  <div class="helper" style="margin-top:8px">No records found yet. Wait for periodic sync or ask admin to check edge connectivity.</div>
  {{end}}
</div>`

const loginHTML = `<!doctype html>
<html lang="en">
<head>
  <meta charset="utf-8">
  <meta name="viewport" content="width=device-width,initial-scale=1">
  <title>Cloudroof DNS Login</title>
  <style>
    :root { --bg:#e9eef4; --panel:#ffffff; --text:#0f172a; --muted:#64748b; --brand:#0f766e; --danger:#be123c; }
    * { box-sizing:border-box; }
    body {
      margin:0;
      min-height:100vh;
      display:grid;
      place-items:center;
      font-family: ui-sans-serif, -apple-system, BlinkMacSystemFont, "Segoe UI", sans-serif;
      background: radial-gradient(1200px 600px at 20% -10%, #dbeafe, transparent), linear-gradient(160deg, #eef2ff 0%, #e2e8f0 100%);
      color:var(--text);
    }
    .card {
      width:min(420px, 92vw);
      background:var(--panel);
      border-radius:18px;
      box-shadow:0 20px 55px rgba(15, 23, 42, 0.12);
      padding:28px;
    }
    .kicker { font-size:12px; text-transform:uppercase; letter-spacing:.12em; color:var(--muted); margin-bottom:6px; }
    h1 { margin:0 0 8px; font-size:24px; }
    p { margin:0 0 16px; color:var(--muted); font-size:14px; }
    label { display:block; font-size:12px; margin:12px 0 6px; color:var(--muted); }
    input {
      width:100%; border:1px solid #cbd5e1; border-radius:10px; padding:11px 12px; font-size:14px;
      outline:none; transition:border-color .15s ease, box-shadow .15s ease;
    }
    input:focus { border-color:#0ea5a5; box-shadow:0 0 0 4px rgba(14, 165, 165, .14); }
    button {
      width:100%; margin-top:16px; border:none; border-radius:10px; padding:12px;
      color:white; font-weight:700; background:linear-gradient(135deg, #0f766e, #0e7490); cursor:pointer;
    }
    .msg { margin-top:12px; color:var(--danger); font-size:13px; }
  </style>
</head>
<body>
  <div class="card">
    <div class="kicker">Cloudroof</div>
    <h1>DNS Control Login</h1>
    <p>Manage parked domains and edge DNS records securely.</p>
    <form method="post" action="/login">
      <label>Username</label>
      <input name="username" placeholder="username" required>
      <label>Password</label>
      <input name="password" type="password" placeholder="password" required>
      <button type="submit">Sign In</button>
    </form>
    {{if .Message}}<div class="msg">{{.Message}}</div>{{end}}
  </div>
</body>
</html>`

const appHTML = `<!doctype html>
<html lang="en">
<head>
  <meta charset="utf-8">
  <meta name="viewport" content="width=device-width,initial-scale=1">
  <title>Cloudroof DNS Dashboard</title>
  <script type="module" src="https://cdn.jsdelivr.net/gh/starfederation/datastar@1.0.0-RC.7/bundles/datastar.js"></script>
  <style>
    :root {
      --bg:#f1f5f9;
      --panel:#ffffff;
      --text:#0f172a;
      --muted:#64748b;
      --line:#e2e8f0;
      --primary:#0f766e;
      --primary-soft:#ccfbf1;
      --danger:#be123c;
      --ok:#166534;
      --shadow:0 8px 26px rgba(15,23,42,.08);
    }
    * { box-sizing:border-box; }
    body {
      margin:0;
      font-family: ui-sans-serif, -apple-system, BlinkMacSystemFont, "Segoe UI", sans-serif;
      color:var(--text);
      background: radial-gradient(1000px 450px at 0% -20%, #dbeafe, transparent), var(--bg);
    }
    .shell { max-width:1280px; margin:0 auto; padding:20px; }
    .topbar {
      display:flex; align-items:center; justify-content:space-between; gap:12px;
      background:var(--panel); border:1px solid var(--line); border-radius:16px; padding:14px 16px; box-shadow:var(--shadow);
    }
    .title { margin:0; font-size:22px; }
    .meta { margin-top:3px; font-size:12px; color:var(--muted); }
    .badge {
      display:inline-flex; align-items:center; gap:6px; padding:4px 9px; border-radius:999px;
      font-size:11px; text-transform:uppercase; letter-spacing:.06em; background:var(--primary-soft); color:#115e59; font-weight:700;
    }
    .logout { max-width:150px; }

    .notice {
      margin-top:14px; padding:10px 12px; border-radius:10px; border:1px solid #bae6fd; background:#ecfeff; color:#0f766e; font-size:13px;
    }

    .layout { margin-top:16px; display:grid; gap:16px; grid-template-columns: 1.35fr .95fr; }
    @media (max-width: 980px) { .layout { grid-template-columns: 1fr; } }

    .panel {
      background:var(--panel); border:1px solid var(--line); border-radius:16px; padding:14px; box-shadow:var(--shadow);
    }
    h2 { margin:0 0 10px; font-size:18px; }
    h3 { margin:0 0 8px; font-size:14px; text-transform:uppercase; letter-spacing:.06em; color:var(--muted); }
    .stack { display:grid; gap:12px; }
    .split { display:grid; gap:10px; grid-template-columns:1fr 1fr; }
    @media (max-width: 640px) { .split { grid-template-columns: 1fr; } }

    .domains {
      display:flex; flex-wrap:wrap; gap:8px;
      margin:8px 0 12px;
    }
    .chip {
      background:#eef2ff; border:1px solid #c7d2fe; color:#3730a3;
      border-radius:999px; padding:5px 10px; font-size:12px;
    }

    label { display:block; font-size:12px; color:var(--muted); margin:0 0 4px; }
    input, select, button {
      width:100%; border-radius:10px; border:1px solid #cbd5e1; padding:10px 11px; font-size:14px; background:white;
    }
    input:focus, select:focus {
      outline:none; border-color:#14b8a6; box-shadow:0 0 0 4px rgba(20,184,166,.12);
    }
    button {
      background:linear-gradient(135deg,#0f766e,#0e7490); color:white; border:none; font-weight:700; cursor:pointer;
    }
    .subtle-btn { background:#f8fafc; color:#0f172a; border:1px solid #cbd5e1; }
    .helper { color:var(--muted); font-size:12px; }

    .tabs { display:flex; gap:8px; margin-bottom:10px; }
    .tab { padding:6px 10px; border-radius:999px; font-size:12px; background:#f8fafc; border:1px solid var(--line); color:#334155; }

    table { width:100%; border-collapse:collapse; font-size:13px; }
    th, td { text-align:left; padding:8px; border-bottom:1px solid var(--line); vertical-align:top; }
    th { color:#475569; font-size:12px; text-transform:uppercase; letter-spacing:.04em; }
    .mono { font-family: ui-monospace, SFMono-Regular, Menlo, Consolas, monospace; }
    .ok { color:var(--ok); font-weight:700; }
    .bad { color:var(--danger); font-weight:700; }
  </style>
</head>
<body>
  <div class="shell">
    <header class="topbar">
      <div>
        <h1 class="title">Cloudroof DNS Dashboard</h1>
        <div class="meta">Signed in as <strong>{{.User.Username}}</strong> · {{.Now}} · <span class="badge">{{.User.Role}}</span></div>
      </div>
      <form method="post" action="/logout" class="logout">
        <button class="subtle-btn" type="submit">Log Out</button>
      </form>
    </header>

    <div id="flash-notice">{{if .Message}}<div class="notice">{{.Message}}</div>{{end}}</div>

    <div class="layout">
      <main class="stack">
        <section class="panel">
          <h2>Your Workspace</h2>
          <div class="tabs"><span class="tab">Domain Parking</span><span class="tab">Host Records</span><span class="tab">RR Pools</span></div>
          <div class="helper">Manage only domains assigned to your account. Admins can manage everything.</div>

          <h3 style="margin-top:12px">Assigned Domains</h3>
          <div class="domains">
            {{range .UserDomains}}<span class="chip">{{.Name}}</span>{{end}}
            {{if not .UserDomains}}<span class="helper">No domains assigned yet.</span>{{end}}
          </div>

          {{if ne .User.Role "admin"}}
          <div id="user-records-section">
            <h3 style="margin-top:8px">Current Records</h3>
            <div class="helper">Read-only view from the latest synced edge snapshots for your assigned domains.</div>
            <form method="post" action="/actions/user/records-refresh" data-on:submit__prevent="@post('/sse/user/records-refresh', {contentType: 'form'})" style="margin-top:8px; max-width:240px;">
              <button type="submit" class="subtle-btn">Refresh Records Now</button>
            </form>
            {{if .RecordsInfo}}<div class="notice" style="margin-top:8px">{{.RecordsInfo}}</div>{{end}}
            {{if .UserGrouped}}
            {{range .UserGrouped}}
            <div style="border:1px solid var(--line); border-radius:10px; padding:10px; margin-top:10px;">
              <div style="display:flex; align-items:center; justify-content:space-between; gap:8px; margin-bottom:6px;">
                <strong class="mono">{{.Zone}}</strong>
                <span class="helper">{{len .Rows}} host/type rows</span>
              </div>
              <table>
                <thead><tr><th>Host</th><th>Type</th><th>Values</th><th>TTL</th><th>Endpoints</th><th>Synced</th></tr></thead>
                <tbody>
                  {{range .Rows}}
                  <tr>
                    <td class="mono">{{.Host}}</td>
                    <td>{{.Type}}</td>
                    <td class="mono">{{.Values}}</td>
                    <td>{{.TTL}}</td>
                    <td>{{.Endpoints}}</td>
                    <td class="mono">{{.SyncedAt}}</td>
                  </tr>
                  {{end}}
                </tbody>
              </table>
            </div>
            {{end}}
            {{else}}
            <div class="helper" style="margin-top:8px">No records found yet. Wait for periodic sync or ask admin to check edge connectivity.</div>
            {{end}}
          </div>
          {{end}}

          <div class="split">
            <form method="post" action="/actions/user/park" data-on:submit__prevent="@post('/sse/user/park', {contentType: 'form'})" class="stack">
              <h3>Park Apex (A/AAAA)</h3>
              <div><label>Domain</label><input name="domain" placeholder="cloudroof.eu" required></div>
              <div><label>Apex A (optional)</label><input name="apex_a" placeholder="198.51.100.20"></div>
              <div><label>Apex AAAA (optional)</label><input name="apex_aaaa" placeholder="2001:db8::20"></div>
              <button type="submit">Apply Parking</button>
            </form>

            <form method="post" action="/actions/user/record" data-on:submit__prevent="@post('/sse/user/record', {contentType: 'form'})" class="stack">
              <h3>Host A/AAAA Update</h3>
              <div><label>Domain</label><input name="domain" placeholder="cloudroof.eu" required></div>
              <div><label>Host</label><input name="host" placeholder="www or api.cloudroof.eu" required></div>
              <div><label>Type</label><select name="type"><option>A</option><option>AAAA</option></select></div>
              <div><label>Value</label><input name="value" placeholder="IP address" required></div>
              <div><label>Mode</label><select name="mode"><option value="set">Set (replace RRset)</option><option value="add">Add (RR member)</option><option value="remove">Remove (RR member)</option></select></div>
              <button type="submit">Apply Host Change</button>
            </form>
          </div>
        </section>

        <section id="action-results-section" class="panel" {{if not .Results}}style="display:none"{{end}}>
          <h2>Action Results</h2>
          {{if .Results}}
          <table>
            <thead><tr><th>Endpoint</th><th>Action</th><th>Status</th><th>Response</th></tr></thead>
            <tbody>
              {{range .Results}}
              <tr>
                <td>{{.Endpoint}}</td>
                <td class="mono">{{.Action}}</td>
                <td>{{if .Success}}<span class="ok">OK {{.Status}}</span>{{else}}<span class="bad">FAIL {{.Status}}</span>{{end}}</td>
                <td class="mono">{{if .Error}}{{.Error}}{{else}}{{.Body}}{{end}}</td>
              </tr>
              {{end}}
            </tbody>
          </table>
          {{end}}
        </section>

        {{if .States}}
        <section class="panel">
          <h2>Cluster State</h2>
          {{range .States}}
            <div style="border:1px solid var(--line); border-radius:12px; padding:10px; margin-bottom:10px;">
              <strong>{{.Endpoint}}</strong>
              {{if .Success}}
                <div class="helper">Zones: {{len .Zones}} · Records: {{len .Records}}</div>
                <table>
                  <thead><tr><th>Name</th><th>Type</th><th>Value</th><th>TTL</th><th>Zone</th></tr></thead>
                  <tbody>{{range .Records}}<tr><td class="mono">{{.Name}}</td><td>{{.Type}}</td><td class="mono">{{.Value}}</td><td>{{.TTL}}</td><td class="mono">{{.Zone}}</td></tr>{{end}}</tbody>
                </table>
              {{else}}
                <div class="bad">{{.Error}}</div>
              {{end}}
            </div>
          {{end}}
        </section>
        {{end}}
      </main>

      {{if eq .User.Role "admin"}}
      <aside class="stack">
        <section class="panel">
          <h2>Admin Control Plane</h2>
          <div class="helper">Global controls for endpoints, users, and multi-edge DNS actions.</div>
        </section>

        <section class="panel stack">
          <h3>Endpoints</h3>
          <form method="post" action="/admin/endpoints/add" class="stack">
            <input name="name" placeholder="edge-1" required>
            <input name="base_url" placeholder="http://10.1.0.2:8080" required>
            <input name="token" placeholder="api token">
            <button type="submit">Add Endpoint</button>
          </form>
          <table>
            <thead><tr><th>Name</th><th>URL</th><th></th></tr></thead>
            <tbody>
              {{range .Endpoints}}
              <tr>
                <td>{{.Name}}</td>
                <td class="mono">{{.BaseURL}}</td>
                <td>
                  <form method="post" action="/admin/endpoints/delete">
                    <input type="hidden" name="id" value="{{.ID}}">
                    <button class="subtle-btn" type="submit">Delete</button>
                  </form>
                </td>
              </tr>
              {{end}}
            </tbody>
          </table>
        </section>

        <section class="panel stack">
          <h3>Users & Domain Access</h3>
          <form method="post" action="/admin/users/add" class="stack">
            <input name="username" placeholder="customer1" required>
            <input type="password" name="password" placeholder="password" required>
            <select name="role"><option value="user">User</option><option value="admin">Admin</option></select>
            <button type="submit">Create User</button>
          </form>
          <form method="post" action="/admin/domains/assign" class="stack">
            <input name="domain" placeholder="example.com" required>
            <input name="username" placeholder="assign to username" required>
            <button type="submit">Assign Domain</button>
          </form>
          <form method="post" action="/admin/domains/transfer" class="stack">
            <input name="domain" placeholder="example.com" required>
            <input name="to_username" placeholder="transfer to username" required>
            <button type="submit">Transfer Domain</button>
          </form>
        </section>

        <section class="panel stack">
          <h3>Admin DNS Actions</h3>
          <form method="post" action="/admin/actions/query-state"><button type="submit">Query State</button></form>
          <form method="post" action="/admin/actions/zone-upsert" class="stack">
            <input name="zone" placeholder="cloudroof.eu" required>
            <input name="ns1" placeholder="snail.cloudroof.eu" required>
            <input name="ns2" placeholder="rabbit.cloudroof.eu" required>
            <button type="submit">Zone Upsert</button>
          </form>
          <form method="post" action="/admin/actions/record-upsert" class="stack">
            <input name="name" placeholder="app.cloudroof.eu" required>
            <select name="type"><option>A</option><option>AAAA</option><option>TXT</option><option>CNAME</option><option>MX</option></select>
            <input name="value" placeholder="IP/TXT/TARGET or 'prio target' for MX" required>
            <input name="zone" placeholder="zone optional">
            <button type="submit">Record Upsert</button>
          </form>
          <form method="post" action="/admin/actions/record-add" class="stack">
            <input name="name" placeholder="pool.cloudroof.eu" required>
            <select name="type"><option>A</option><option>AAAA</option><option>TXT</option><option>CNAME</option><option>MX</option></select>
            <input name="value" placeholder="value" required>
            <input name="zone" placeholder="zone optional">
            <button type="submit">Record Add</button>
          </form>
          <form method="post" action="/admin/actions/record-remove" class="stack">
            <input name="name" placeholder="pool.cloudroof.eu" required>
            <select name="type"><option>A</option><option>AAAA</option><option>TXT</option><option>CNAME</option><option>MX</option></select>
            <input name="value" placeholder="value" required>
            <input name="zone" placeholder="zone optional">
            <button type="submit">Record Remove</button>
          </form>
          <form method="post" action="/admin/actions/record-delete" class="stack">
            <input name="name" placeholder="pool.cloudroof.eu" required>
            <select name="type"><option value="">ALL</option><option>A</option><option>AAAA</option><option>TXT</option><option>CNAME</option><option>MX</option></select>
            <button type="submit">Record Delete</button>
          </form>
        </section>
      </aside>
      {{end}}
    </div>
  </div>
</body>
</html>`
