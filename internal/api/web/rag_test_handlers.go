package web

import (
	"fmt"
	"log"
	"net/http"
	"strings"

	bedrockTypes "github.com/aws/aws-sdk-go-v2/service/bedrockruntime/types"
	"gorm.io/gorm"

	"mini-aigw/internal/api/middleware"
	"mini-aigw/internal/config"
	"mini-aigw/internal/models"
	"mini-aigw/internal/prompts"
	"mini-aigw/internal/services"
	"mini-aigw/internal/views"
)

type RAGTestHandler struct {
	bedrockService   *services.BedrockService
	knowledgeService *services.KnowledgeService
	db               *gorm.DB
	cfg              *config.Config
	renderer         *views.Renderer
}

func NewRAGTestHandler(
	bedrockService *services.BedrockService,
	knowledgeService *services.KnowledgeService,
	db *gorm.DB,
	cfg *config.Config,
	renderer *views.Renderer,
) *RAGTestHandler {
	return &RAGTestHandler{
		bedrockService:   bedrockService,
		knowledgeService: knowledgeService,
		db:               db,
		cfg:              cfg,
		renderer:         renderer,
	}
}

func (h *RAGTestHandler) ShowRAGTest(w http.ResponseWriter, r *http.Request) {
	user := middleware.GetUserFromContext(r.Context())
	if user == nil {
		http.Redirect(w, r, "/login", http.StatusSeeOther)
		return
	}

	isAdmin := user.HasRole("admin")

	var groups []string
	groups = append(groups, "public")
	if isAdmin {
		var allGroups []models.Group
		h.db.Find(&allGroups)
		for _, g := range allGroups {
			groups = append(groups, g.Name)
		}
	} else {
		groups = append(groups, user.GetGroups()...)
	}

	// Fetch tags from documents
	var docs []models.KnowledgeDocument
	h.db.Find(&docs)
	tagSet := make(map[string]bool)
	for _, d := range docs {
		for _, t := range d.GetTags() {
			tagSet[t] = true
		}
	}
	var availableTags []string
	for t := range tagSet {
		availableTags = append(availableTags, t)
	}

	var activeModels []models.LLMModel
	h.db.Where("is_active = ?", true).Find(&activeModels)

	initQuery := strings.TrimSpace(r.URL.Query().Get("query"))
	initGroup := strings.TrimSpace(r.URL.Query().Get("group"))

	data := map[string]interface{}{
		"User":          user,
		"IsAdmin":       isAdmin,
		"CurrentPath":   "/rag-test",
		"Groups":        groups,
		"AvailableTags": availableTags,
		"Models":        activeModels,
		"Config":        h.cfg,
		"InitQuery":     initQuery,
		"InitGroup":     initGroup,
	}

	_ = h.renderer.Render(w, "rag_test.html", data)
}

func (h *RAGTestHandler) HandleRetrieve(w http.ResponseWriter, r *http.Request) {
	user := middleware.GetUserFromContext(r.Context())
	if user == nil {
		http.Error(w, "Unauthorized", http.StatusUnauthorized)
		return
	}

	if err := r.ParseForm(); err != nil {
		http.Error(w, "Invalid form", http.StatusBadRequest)
		return
	}

	query := strings.TrimSpace(r.FormValue("query"))
	group := strings.TrimSpace(r.FormValue("group"))
	if group == "" {
		group = "public"
	}
	tag := strings.TrimSpace(r.FormValue("tag"))

	allowedGroups := []string{group, "public"}
	var tags []string
	if tag != "" {
		tags = append(tags, tag)
	}

	matchedTags, matchedDocIDs := h.knowledgeService.FindMatchingMetadataFromQuery(query)

	results, err := h.bedrockService.RetrieveKnowledge(
		r.Context(),
		h.cfg.BedrockKBID,
		query,
		allowedGroups,
		h.cfg.RAGNumberOfResults,
		tags,
		matchedTags,
		matchedDocIDs,
	)

	data := map[string]interface{}{
		"Query":       query,
		"Group":       group,
		"SelectedTag": tag,
		"Results":     results,
	}
	if err != nil {
		data["Error"] = err.Error()
	}

	_ = h.renderer.RenderPartial(w, "rag_retrieve_results.html", data)
}

func (h *RAGTestHandler) HandleChat(w http.ResponseWriter, r *http.Request) {
	user := middleware.GetUserFromContext(r.Context())
	if user == nil {
		http.Error(w, "Unauthorized", http.StatusUnauthorized)
		return
	}

	if err := r.ParseForm(); err != nil {
		http.Error(w, "Invalid form", http.StatusBadRequest)
		return
	}

	query := strings.TrimSpace(r.FormValue("query"))
	group := strings.TrimSpace(r.FormValue("group"))
	if group == "" {
		group = "public"
	}
	modelID := strings.TrimSpace(r.FormValue("model_id"))
	if modelID == "" {
		modelID = h.cfg.DefaultModelID
	}

	allowedGroups := []string{group, "public"}

	// Retrieve knowledge
	matchedTags, matchedDocIDs := h.knowledgeService.FindMatchingMetadataFromQuery(query)
	results, err := h.bedrockService.RetrieveKnowledge(
		r.Context(),
		h.cfg.BedrockKBID,
		query,
		allowedGroups,
		h.cfg.RAGNumberOfResults,
		nil,
		matchedTags,
		matchedDocIDs,
	)
	if err != nil {
		log.Printf("RAG test retrieve error: %v", err)
	}

	filtered := services.FilterKnowledgeResultsByScore(results, h.cfg.RAGScoreThreshold)

	baseURL := h.cfg.PublicBaseURL
	if baseURL == "" {
		scheme := "http"
		if r.TLS != nil || r.Header.Get("X-Forwarded-Proto") == "https" {
			scheme = "https"
		}
		baseURL = fmt.Sprintf("%s://%s", scheme, r.Host)
	}

	var formattedContext []string
	type CitationItem struct {
		Index       int
		Filename    string
		Group       string
		DownloadURL string
	}
	var citations []CitationItem
	idx := 1

	for _, chunk := range filtered {
		s3URI := ""
		if loc, ok := chunk.Location["s3Location"].(map[string]interface{}); ok {
			s3URI, _ = loc["uri"].(string)
		}
		_, filename, grp, downloadURL := h.knowledgeService.ResolveDocumentAndDownloadURL(s3URI, chunk.Metadata, baseURL)

		formattedContext = append(formattedContext, fmt.Sprintf(
			"[%d] %s (%s):\n%s\nダウンロードURL: %s",
			idx, filename, grp, chunk.Content, downloadURL,
		))

		citations = append(citations, CitationItem{
			Index:       idx,
			Filename:    filename,
			Group:       grp,
			DownloadURL: downloadURL,
		})
		idx++
	}

	var systemInstruction string
	if len(formattedContext) > 0 {
		systemInstruction = fmt.Sprintf("%s\n\n【提供ナレッジ】\n%s", prompts.RAGTestSystemInstruction, strings.Join(formattedContext, "\n\n"))
	} else {
		systemInstruction = prompts.RAGTestSystemInstruction + "\n\n【提供ナレッジ】該当情報はありませんでした。"
	}

	sysBlocks := []bedrockTypes.SystemContentBlock{
		&bedrockTypes.SystemContentBlockMemberText{Value: systemInstruction},
	}
	userMsgs := []bedrockTypes.Message{
		{
			Role: bedrockTypes.ConversationRoleUser,
			Content: []bedrockTypes.ContentBlock{
				&bedrockTypes.ContentBlockMemberText{Value: query},
			},
		},
	}

	converseResp, err := h.bedrockService.CallConverse(r.Context(), modelID, sysBlocks, userMsgs, nil, nil, nil, nil)
	answer := "回答を取得できませんでした。"
	if err == nil && len(converseResp.Choices) > 0 {
		if c, ok := converseResp.Choices[0].Message["content"].(string); ok {
			answer = c
		}
	} else if err != nil {
		answer = fmt.Sprintf("エラーが発生しました: %v", err)
	}

	data := map[string]interface{}{
		"Query":     query,
		"ModelID":   modelID,
		"Answer":    answer,
		"Citations": citations,
	}

	_ = h.renderer.RenderPartial(w, "rag_chat_response.html", data)
}

