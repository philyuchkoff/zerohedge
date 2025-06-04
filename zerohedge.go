package main

import (
	"bytes"
	"context"
	"crypto/md5"
	"encoding/hex"
	"encoding/json"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"regexp"
	"strings"
	"time"

	"github.com/joho/godotenv"
)

// RSS структуры
type RSS struct {
	Channel struct {
		Items []struct {
			Title       string `xml:"title"`
			Link        string `xml:"link"`
			Description string `xml:"description"`
			PubDate     string `xml:"pubDate"`
		} `xml:"item"`
	} `xml:"channel"`
}

// Конфигурация
const (
	RSSURL            = "https://cms.zerohedge.com/fullrss2.xml"
	LastPostFile      = "last_post.txt"
	TelegramBotAPI    = "https://api.telegram.org/bot%s/sendMessage"
	YandexTranslate   = "https://translate.api.cloud.yandex.net/translate/v2/translate"
	MaxRetries        = 3
	RetryDelay        = 5 * time.Second
	LogFile           = "zerohedge.log"
	CheckInterval     = 1 * time.Minute
	MaxSummaryLength  = 3000
	SummarySentences  = 5 // Количество предложений для сокращения
	MaxArticlesToSend = 3 // Сколько новых статей отправлять за раз
)

var (
	TelegramToken  string
	TelegramChatID string
	YandexAPIKey   string
	YandexFolderID string
)

type LastPost struct {
	URL  string `json:"url"`
	Hash string `json:"hash"`
}

type YandexTranslationResponse struct {
	Translations []struct {
		Text string `json:"text"`
	} `json:"translations"`
}

func setupLogger() (*slog.Logger, error) {
	logFile, err := os.OpenFile(LogFile, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0644)
	if err != nil {
		return nil, fmt.Errorf("failed to open log file: %w", err)
	}

	handler := slog.NewJSONHandler(
		io.MultiWriter(os.Stdout, logFile),
		&slog.HandlerOptions{
			Level:     slog.LevelDebug,
			AddSource: true,
		},
	)

	logger := slog.New(handler)
	slog.SetDefault(logger)
	return logger, nil
}

var httpClient = &http.Client{
	Timeout: 30 * time.Second,
}

func fetchRSSFeed(ctx context.Context) (*RSS, error) {
	req, err := http.NewRequestWithContext(ctx, "GET", RSSURL, nil)
	if err != nil {
		return nil, fmt.Errorf("error creating request: %w", err)
	}

	req.Header.Set("User-Agent", "Mozilla/5.0 (compatible; ZeroHedgeMonitor/1.0)")

	resp, err := httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("error fetching RSS: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("RSS feed returned status: %d", resp.StatusCode)
	}

	var rss RSS
	if err := xml.NewDecoder(resp.Body).Decode(&rss); err != nil {
		return nil, fmt.Errorf("error decoding RSS: %w", err)
	}

	return &rss, nil
}

func translateWithYandex(ctx context.Context, text string) (string, error) {
	if YandexAPIKey == "" || YandexFolderID == "" {
		return "", errors.New("Yandex API keys not set")
	}

	payload := map[string]interface{}{
		"folder_id":           YandexFolderID,
		"texts":              []string{text},
		"targetLanguageCode": "ru",
	}

	jsonData, err := json.Marshal(payload)
	if err != nil {
		return "", fmt.Errorf("serialization error: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, "POST", YandexTranslate, bytes.NewBuffer(jsonData))
	if err != nil {
		return "", fmt.Errorf("request creation error: %w", err)
	}

	req.Header.Set("Authorization", "Api-Key "+YandexAPIKey)
	req.Header.Set("Content-Type", "application/json")

	resp, err := httpClient.Do(req)
	if err != nil {
		return "", fmt.Errorf("request error: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return "", fmt.Errorf("status %d: %s", resp.StatusCode, body)
	}

	var result YandexTranslationResponse
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return "", fmt.Errorf("JSON decode error: %w", err)
	}

	if len(result.Translations) == 0 {
		return "", errors.New("empty API response")
	}

	return result.Translations[0].Text, nil
}

func sendToTelegram(ctx context.Context, text string) error {
	apiURL := fmt.Sprintf(TelegramBotAPI, TelegramToken)

	payload := map[string]string{
		"chat_id":    TelegramChatID,
		"text":       text,
		"parse_mode": "HTML",
	}

	jsonData, err := json.Marshal(payload)
	if err != nil {
		return fmt.Errorf("serialization error: %w", err)
	}

	resp, err := httpClient.Post(apiURL, "application/json", bytes.NewReader(jsonData))
	if err != nil {
		return fmt.Errorf("request error: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("status %d", resp.StatusCode)
	}

	return nil
}

func intelligentSummary(text string) string {
	if len(text) <= MaxSummaryLength {
		return text
	}

	r := regexp.MustCompile(`(?s)(.*?[.!?](\s|$))`)
	matches := r.FindAllString(text, -1)

	if len(matches) > 0 {
		sentences := SummarySentences
		if sentences > len(matches) {
			sentences = len(matches)
		}
		summary := strings.Join(matches[:sentences], " ")
		return strings.TrimSpace(summary) + "…"
	}

	return text[:MaxSummaryLength] + "…"
}

func readLastPost() (LastPost, error) {
	var lastPost LastPost

	data, err := os.ReadFile(LastPostFile)
	if err != nil {
		if os.IsNotExist(err) {
			return lastPost, nil
		}
		return lastPost, fmt.Errorf("read error: %w", err)
	}

	if err := json.Unmarshal(data, &lastPost); err != nil {
		return lastPost, fmt.Errorf("JSON decode error: %w", err)
	}

	return lastPost, nil
}

func saveLastPost(url string) error {
	hash := md5.Sum([]byte(url))
	data, err := json.Marshal(LastPost{
		URL:  url,
		Hash: hex.EncodeToString(hash[:]),
	})
	if err != nil {
		return err
	}

	return os.WriteFile(LastPostFile, data, 0644)
}

func processNewArticles(ctx context.Context, logger *slog.Logger) error {
	rss, err := fetchRSSFeed(ctx)
	if err != nil {
		return fmt.Errorf("error fetching RSS: %w", err)
	}

	if len(rss.Channel.Items) == 0 {
		return errors.New("no articles in RSS feed")
	}

	lastPost, err := readLastPost()
	if err != nil {
		return fmt.Errorf("error reading last post: %w", err)
	}

	newArticles := 0
	for i, item := range rss.Channel.Items {
		if newArticles >= MaxArticlesToSend {
			break
		}

		currentHash := md5.Sum([]byte(item.Link))
		if hex.EncodeToString(currentHash[:]) == lastPost.Hash {
			logger.Debug("Found already processed article", "url", item.Link)
			if i == 0 {
				logger.Info("No new articles found")
			}
			break
		}

		// Для первой статьи обновляем last_post.txt
		if i == 0 {
			if err := saveLastPost(item.Link); err != nil {
				return fmt.Errorf("error saving last post: %w", err)
			}
		}

		// Используем описание из RSS вместо контента статьи
		content := item.Description
		if content == "" {
			content = item.Title + ". Read more at the link below."
		}

		translation, err := translateWithYandex(ctx, content)
		if err != nil {
			logger.Error("Translation error", "err", err, "url", item.Link)
			continue
		}

		summary := intelligentSummary(translation)
		message := fmt.Sprintf(
			"<b>📌 %s</b>\n\n%s\n\n<b>📅 %s</b>\n🔗 <a href=\"%s\">Read full article</a>",
			item.Title,
			summary,
			item.PubDate,
			item.Link,
		)

		if err := sendToTelegram(ctx, message); err != nil {
			logger.Error("Error sending to Telegram", "err", err)
			continue
		}

		logger.Info("Article processed", "title", item.Title, "url", item.Link)
		newArticles++
	}

	return nil
}

func run(ctx context.Context, logger *slog.Logger) error {
	logger.Info("Starting ZeroHedge RSS monitor")

	ticker := time.NewTicker(CheckInterval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return nil
		case <-ticker.C:
			if err := processNewArticles(ctx, logger); err != nil {
				logger.Error("Error processing articles", "err", err)
			}
		}
	}
}

func main() {
	logger, err := setupLogger()
	if err != nil {
		panic(fmt.Sprintf("Failed to setup logger: %v", err))
	}

	if err := godotenv.Load(); err != nil {
		logger.Warn("No .env file found or error loading", "err", err)
	}

	TelegramToken = os.Getenv("TG_TOKEN")
	TelegramChatID = os.Getenv("TG_CHAT_ID")
	YandexAPIKey = os.Getenv("YANDEX_TRANSLATE_KEY")
	YandexFolderID = os.Getenv("YANDEX_FOLDER_ID")

	requiredVars := []struct {
		name  string
		value string
	}{
		{"TG_TOKEN", TelegramToken},
		{"TG_CHAT_ID", TelegramChatID},
		{"YANDEX_TRANSLATE_KEY", YandexAPIKey},
		{"YANDEX_FOLDER_ID", YandexFolderID},
	}

	for _, v := range requiredVars {
		if v.value == "" {
			logger.Error("Required environment variable not set", "var", v.name)
			os.Exit(1)
		}
	}

	ctx := context.Background()
	if err := run(ctx, logger); err != nil {
		logger.Error("Critical error", "err", err)
		errorMsg := fmt.Sprintf("🚨 ZeroHedge Monitor error:\n\n%s", err)
		if err := sendToTelegram(ctx, errorMsg); err != nil {
			logger.Error("Failed to send error to Telegram", "err", err)
		}
		os.Exit(1)
	}
}
