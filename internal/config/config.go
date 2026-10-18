package config

import (
	"os"
	"strconv"
	"strings"

	"github.com/joho/godotenv"
)

type Config struct {
	ProjectName   string
	Debug         bool
	Host          string
	Port          int
	PublicBaseURL string
	SecretKey     string

	// SQLite
	DatabaseURL string
	DataDir     string

	// Session
	SessionCookieName     string
	SessionMaxAgeSeconds  int
	SessionCookieSecure   bool
	SessionCookieSameSite string

	// AWS
	AWSRegion             string
	AWSEndpointURL        string
	AWSConnectTimeoutSec  int
	AWSReadTimeoutSec     int
	AWSMaxRetryAttempts   int
	AWSMaxPoolConnections int
	BedrockKBID           string
	BedrockDataSourceID   string
	KnowledgeS3Bucket     string
	MaxUploadFileSizeMB   int
	DefaultModelID        string
	AvailableModels       []string

	// RAG Configuration
	RAGScoreThreshold  float64
	RAGNumberOfResults int

	// Inference Parameters
	DefaultTemperature *float64
	DefaultMaxTokens   int
	MaxAllowedTokens   int

	// mini-id OIDC
	OIDCIssuer             string
	OIDCClientID           string
	OIDCClientSecret       string
	OIDCRedirectURI        string
	OIDCScopes             string
	OIDCEndSessionEndpoint string

	// Document Extraction (xberg)
	XbergPath string
}

var AppConfig *Config

func LoadConfig() *Config {
	// .env ファイルが存在すればロード（エラーは無視）
	_ = godotenv.Load(".env")

	cfg := &Config{
		ProjectName:           getEnv("PROJECT_NAME", "mini-aigw"),
		Debug:                 getEnvAsBool("DEBUG", false),
		Host:                  getEnv("HOST", "0.0.0.0"),
		Port:                  getEnvAsInt("PORT", 8000),
		PublicBaseURL:         getEnv("PUBLIC_BASE_URL", ""),
		SecretKey:             getEnv("SECRET_KEY", "mini-aigw-secret-key-development-mode-change-in-production"),
		DatabaseURL:           getEnv("DATABASE_URL", "sqlite:///data/gateway.db"),
		DataDir:               getEnv("DATA_DIR", "data"),
		SessionCookieName:     getEnv("SESSION_COOKIE_NAME", "aigw_session"),
		SessionMaxAgeSeconds:  getEnvAsInt("SESSION_MAX_AGE_SECONDS", 60*60*24*7),
		SessionCookieSecure:   getEnvAsBool("SESSION_COOKIE_SECURE", false),
		SessionCookieSameSite: getEnv("SESSION_COOKIE_SAMESITE", "lax"),

		AWSRegion:             getEnv("AWS_REGION", "ap-northeast-1"),
		AWSEndpointURL:        getEnv("AWS_ENDPOINT_URL", ""),
		AWSConnectTimeoutSec:  getEnvAsInt("AWS_CONNECT_TIMEOUT", 10),
		AWSReadTimeoutSec:     getEnvAsInt("AWS_READ_TIMEOUT", 120),
		AWSMaxRetryAttempts:   getEnvAsInt("AWS_MAX_RETRY_ATTEMPTS", 4),
		AWSMaxPoolConnections: getEnvAsInt("AWS_MAX_POOL_CONNECTIONS", 50),
		BedrockKBID:           getEnv("BEDROCK_KB_ID", "SAMPLE_KB_ID"),
		BedrockDataSourceID:   getEnv("BEDROCK_DATA_SOURCE_ID", "SAMPLE_DS_ID"),
		KnowledgeS3Bucket:     getEnv("KNOWLEDGE_S3_BUCKET", "sample-knowledge-bucket"),
		MaxUploadFileSizeMB:   getEnvAsInt("MAX_UPLOAD_FILE_SIZE_MB", 50),
		DefaultModelID:        getEnv("DEFAULT_MODEL_ID", "jp.amazon.nova-2-lite-v1:0"),
		AvailableModels:       getEnvAsSlice("AVAILABLE_MODELS", []string{"jp.amazon.nova-2-lite-v1:0", "jp.anthropic.claude-haiku-5-5"}),

		RAGScoreThreshold:  getEnvAsFloat("RAG_SCORE_THRESHOLD", 0.30),
		RAGNumberOfResults: getEnvAsInt("RAG_NUMBER_OF_RESULTS", 5),

		DefaultMaxTokens: getEnvAsInt("DEFAULT_MAX_TOKENS", 4096),
		MaxAllowedTokens: getEnvAsInt("MAX_ALLOWED_TOKENS", 8192),

		OIDCIssuer:             getEnv("OIDC_ISSUER", ""),
		OIDCClientID:           getEnv("OIDC_CLIENT_ID", ""),
		OIDCClientSecret:       getEnv("OIDC_CLIENT_SECRET", ""),
		OIDCRedirectURI:        getEnv("OIDC_REDIRECT_URI", "http://localhost:8000/auth/oidc/callback"),
		OIDCScopes:             getEnv("OIDC_SCOPES", "openid profile email groups"),
		OIDCEndSessionEndpoint: getEnv("OIDC_END_SESSION_ENDPOINT", ""),

		XbergPath: getEnv("XBERG_PATH", "xberg"),
	}

	if tempStr := os.Getenv("DEFAULT_TEMPERATURE"); tempStr != "" {
		if val, err := strconv.ParseFloat(tempStr, 64); err == nil {
			cfg.DefaultTemperature = &val
		}
	}

	AppConfig = cfg
	return cfg
}

func getEnv(key, defaultVal string) string {
	val, ok := os.LookupEnv(key)
	if !ok || val == "" {
		return defaultVal
	}
	return val
}

func getEnvAsInt(key string, defaultVal int) int {
	valStr := os.Getenv(key)
	if val, err := strconv.Atoi(valStr); err == nil {
		return val
	}
	return defaultVal
}

func getEnvAsFloat(key string, defaultVal float64) float64 {
	valStr := os.Getenv(key)
	if val, err := strconv.ParseFloat(valStr, 64); err == nil {
		return val
	}
	return defaultVal
}

func getEnvAsBool(key string, defaultVal bool) bool {
	valStr := os.Getenv(key)
	if val, err := strconv.ParseBool(valStr); err == nil {
		return val
	}
	return defaultVal
}

func getEnvAsSlice(key string, defaultVal []string) []string {
	valStr := os.Getenv(key)
	if valStr == "" {
		return defaultVal
	}
	items := strings.Split(valStr, ",")
	var result []string
	for _, item := range items {
		trimmed := strings.TrimSpace(item)
		if trimmed != "" {
			result = append(result, trimmed)
		}
	}
	return result
}
