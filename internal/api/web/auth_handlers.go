package web

import (
	"log"
	"net/http"
	"time"

	"github.com/google/uuid"
	"gorm.io/gorm"

	"mini-aigw/internal/api/middleware"
	"mini-aigw/internal/auth"
	"mini-aigw/internal/config"
	"mini-aigw/internal/models"
	"mini-aigw/internal/services"
	"mini-aigw/internal/views"
)

type AuthHandler struct {
	db          *gorm.DB
	cfg         *config.Config
	oidcService *services.OIDCService
	renderer    *views.Renderer
}

func NewAuthHandler(
	db *gorm.DB,
	cfg *config.Config,
	oidcService *services.OIDCService,
	renderer *views.Renderer,
) *AuthHandler {
	return &AuthHandler{
		db:          db,
		cfg:         cfg,
		oidcService: oidcService,
		renderer:    renderer,
	}
}

func (h *AuthHandler) ShowLogin(w http.ResponseWriter, r *http.Request) {
	// If already authenticated, redirect to dashboard
	if u := middleware.GetUserFromContext(r.Context()); u != nil {
		http.Redirect(w, r, "/", http.StatusSeeOther)
		return
	}

	data := map[string]interface{}{
		"OIDCEnabled": h.oidcService.IsEnabled(),
		"Error":       r.URL.Query().Get("error"),
	}
	_ = h.renderer.Render(w, "login.html", data)
}

func (h *AuthHandler) HandleLocalLogin(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		h.renderLoginError(w, "無効なリクエストです")
		return
	}

	username := r.FormValue("username")
	password := r.FormValue("password")

	var user models.User
	if err := h.db.First(&user, "username = ?", username).Error; err != nil {
		h.renderLoginError(w, "ユーザー名またはパスワードが正しくありません")
		return
	}

	if user.PasswordHash == nil {
		h.renderLoginError(w, "このアカウントはSSO専用です")
		return
	}

	valid, err := auth.VerifyPassword(password, *user.PasswordHash)
	if err != nil || !valid {
		h.renderLoginError(w, "ユーザー名またはパスワードが正しくありません")
		return
	}

	// Session fixation protection: create brand new session
	h.createSessionAndRedirect(w, r, user.ID, nil)
}

func (h *AuthHandler) HandleOIDCLogin(w http.ResponseWriter, r *http.Request) {
	if !h.oidcService.IsEnabled() {
		http.Error(w, "OIDC is not enabled", http.StatusBadRequest)
		return
	}

	authURL, state, err := h.oidcService.GenerateAuthURL()
	if err != nil {
		log.Printf("Failed to generate OIDC auth URL: %v", err)
		http.Redirect(w, r, "/login?error=OIDC認証の初期化に失敗しました", http.StatusSeeOther)
		return
	}

	// Set temporary state cookie
	http.SetCookie(w, &http.Cookie{
		Name:     "aigw_oidc_state",
		Value:    state,
		Path:     "/",
		MaxAge:   600,
		HttpOnly: true,
		SameSite: http.SameSiteLaxMode,
		Secure:   h.cfg.SessionCookieSecure,
	})

	http.Redirect(w, r, authURL, http.StatusSeeOther)
}

func (h *AuthHandler) HandleOIDCCallback(w http.ResponseWriter, r *http.Request) {
	state := r.URL.Query().Get("state")
	code := r.URL.Query().Get("code")

	stateCookie, err := r.Cookie("aigw_oidc_state")
	if err != nil || stateCookie.Value != state {
		http.Redirect(w, r, "/login?error=不正な認証リクエストです (State不整合)", http.StatusSeeOther)
		return
	}

	// Clear state cookie
	http.SetCookie(w, &http.Cookie{
		Name:     "aigw_oidc_state",
		Value:    "",
		Path:     "/",
		MaxAge:   -1,
		HttpOnly: true,
	})

	userInfo, err := h.oidcService.ExchangeCode(r.Context(), state, code)
	if err != nil {
		log.Printf("OIDC exchange error: %v", err)
		http.Redirect(w, r, "/login?error=IDトークンの検証に失敗しました", http.StatusSeeOther)
		return
	}

	// sub based user lookup/provisioning
	var user models.User
	if err := h.db.First(&user, "oidc_sub = ?", userInfo.Sub).Error; err != nil {
		// New user provisioning
		user = models.User{
			AuthType:    "oidc",
			OIDCSub:     &userInfo.Sub,
			Username:    userInfo.Username,
			Email:       &userInfo.Email,
			DisplayName: &userInfo.Name,
			Roles:       `["user"]`,
		}
		user.SetGroups(userInfo.Groups)
		if err := h.db.Create(&user).Error; err != nil {
			log.Printf("Failed to provision OIDC user: %v", err)
			http.Redirect(w, r, "/login?error=ユーザーの登録に失敗しました", http.StatusSeeOther)
			return
		}
	} else {
		// Sync groups and email
		user.SetGroups(userInfo.Groups)
		if userInfo.Email != "" {
			user.Email = &userInfo.Email
		}
		if userInfo.Name != "" {
			user.DisplayName = &userInfo.Name
		}
		h.db.Save(&user)
	}

	h.createSessionAndRedirect(w, r, user.ID, &userInfo.RawIDToken)
}

func (h *AuthHandler) HandleLogout(w http.ResponseWriter, r *http.Request) {
	var rawIDToken *string
	cookie, err := r.Cookie(h.cfg.SessionCookieName)
	if err == nil && cookie.Value != "" {
		var session models.Session
		if err := h.db.First(&session, "session_id = ?", cookie.Value).Error; err == nil {
			rawIDToken = session.RawIDToken
			h.db.Delete(&session)
		}
	}

	// Delete session cookie
	http.SetCookie(w, &http.Cookie{
		Name:     h.cfg.SessionCookieName,
		Value:    "",
		Path:     "/",
		MaxAge:   -1,
		HttpOnly: true,
		SameSite: http.SameSiteLaxMode,
		Secure:   h.cfg.SessionCookieSecure,
	})

	// RP-Initiated Logout if available
	if rawIDToken != nil && h.oidcService.IsEnabled() {
		logoutURL := h.oidcService.GetEndSessionURL(*rawIDToken, h.cfg.PublicBaseURL+"/login")
		if logoutURL != "" {
			http.Redirect(w, r, logoutURL, http.StatusSeeOther)
			return
		}
	}

	http.Redirect(w, r, "/login", http.StatusSeeOther)
}

func (h *AuthHandler) createSessionAndRedirect(w http.ResponseWriter, r *http.Request, userID string, rawIDToken *string) {
	sessionID := uuid.New().String()
	expiresAt := time.Now().Add(time.Duration(h.cfg.SessionMaxAgeSeconds) * time.Second)

	session := models.Session{
		SessionID:  sessionID,
		UserID:     userID,
		RawIDToken: rawIDToken,
		ExpiresAt:  expiresAt,
		CreatedAt:  time.Now(),
	}
	if err := h.db.Create(&session).Error; err != nil {
		log.Printf("Failed to create session: %v", err)
		http.Redirect(w, r, "/login?error=セッション生成に失敗しました", http.StatusSeeOther)
		return
	}

	http.SetCookie(w, &http.Cookie{
		Name:     h.cfg.SessionCookieName,
		Value:    sessionID,
		Path:     "/",
		MaxAge:   h.cfg.SessionMaxAgeSeconds,
		HttpOnly: true,
		SameSite: http.SameSiteLaxMode,
		Secure:   h.cfg.SessionCookieSecure,
	})

	http.Redirect(w, r, "/", http.StatusSeeOther)
}

func (h *AuthHandler) renderLoginError(w http.ResponseWriter, errMsg string) {
	data := map[string]interface{}{
		"OIDCEnabled": h.oidcService.IsEnabled(),
		"Error":       errMsg,
	}
	_ = h.renderer.Render(w, "login.html", data)
}

