package main

import (
	"bytes"
	"context"
	"crypto/md5"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/PuerkitoBio/goquery"
)

// Конфигурация
const (
	ZeroHedgeURL    = "https://www.zerohedge.com/"
	LastPostFile    = "last_post.txt"
	TelegramBotAPI  = "https://api.telegram.org/bot%s/sendMessage"
	YandexTranslate = "https://translate.api.cloud.yandex.net/translate/v2/translate"
	MaxRetries      = 3
	RetryDelay      = 5 * time.Second
	LogFile         = "zerohedge_monitor.log"
)

var (
	TelegramToken   = os.Getenv("TG_TOKEN")
	TelegramChatID  = os.Getenv("TG_CHAT_ID")
	YandexAPIKey    = os.Getenv("YANDEX_TRANSLATE_KEY")
	YandexFolderID  = os.Getenv("YANDEX_FOLDER_ID")
)

// Структуры данных
type LastPost struct {
	URL  string `json:"url"`
	Hash string `json:"hash"`
}

type YandexTranslationResponse struct {
	Translations []struct {
		Text string `json:"text"`
	} `json:"translations"`
}

// Инициализация логгера
func setupLogger() (*slog.Logger, error) {
	logFile, err := os.OpenFile(LogFile, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0644)
	if err != nil {
		return nil, fmt.Errorf("не удалось открыть лог-файл: %w", err)
	}

	logger := slog.New(
		slog.NewJSONHandler(io.MultiWriter(os.Stdout, logFile), &slog.HandlerOptions{
			Level:     slog.LevelDebug,
			AddSource: true,
		}),
	)

	slog.SetDefault(logger)
	return logger, nil
}

// HTTP-клиент с логированием
var httpClient = &http.Client{
	Timeout: 30 * time.Second,
	Transport: loggingRoundTripper{
		proxied: http.DefaultTransport,
	},
}

type loggingRoundTripper struct {
	proxied http.RoundTripper
}

func (lrt loggingRoundTripper) RoundTrip(req *http.Request) (*http.Response, error) {
	slog.Debug("HTTP запрос", "method", req.Method, "url", req.URL.Redacted())
	resp, err := lrt.proxied.RoundTrip(req)
	if err != nil {
		slog.Error("HTTP ошибка", "err", err)
	}
	return resp, err
}

// Основные функции
func translateWithYandex(ctx context.Context, text string) (string, error) {
	if YandexAPIKey == "" || YandexFolderID == "" {
		return "", errors.New("не заданы Yandex API ключи")
	}

	payload := map[string]interface{}{
		"folder_id":           YandexFolderID,
		"texts":              []string{text},
		"targetLanguageCode": "ru",
	}

	jsonData, err := json.Marshal(payload)
	if err != nil {
		return "", fmt.Errorf("ошибка сериализации: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, "POST", YandexTranslate, bytes.NewBuffer(jsonData))
	if err != nil {
		return "", fmt.Errorf("ошибка создания запроса: %w", err)
	}

	req.Header.Set("Authorization", "Api-Key "+YandexAPIKey)
	req.Header.Set("Content-Type", "application/json")

	resp, err := httpClient.Do(req)
	if err != nil {
		return "", fmt.Errorf("ошибка запроса: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return "", fmt.Errorf("статус %d: %s", resp.StatusCode, body)
	}

	var result YandexTranslationResponse
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return "", fmt.Errorf("ошибка разбора JSON: %w", err)
	}

	if len(result.Translations) == 0 {
		return "", errors.New("пустой ответ от API")
	}

	return result.Translations[0].Text, nil
}

func getLatestArticle(ctx context.Context) (string, error) {
	resp, err := httpClient.Get(ZeroHedgeURL)
	if err != nil {
		return "", fmt.Errorf("ошибка запроса: %w", err)
	}
	defer resp.Body.Close()

	doc, err := goquery.NewDocumentFromReader(resp.Body)
	if err != nil {
		return "", fmt.Errorf("ошибка парсинга: %w", err)
	}

	// Ищем статью (настройте селектор под актуальную верстку)
	article := doc.Find("article").First()
	if article == nil {
		return "", errors.New("статья не найдена")
	}

	link, exists := article.Find("a").First().Attr("href")
	if !exists {
		return "", errors.New("ссылка не найдена")
	}

	if !strings.HasPrefix(link, "http") {
		link = ZeroHedgeURL + strings.TrimPrefix(link, "/")
	}

	return link, nil
}

func getArticleContent(ctx context.Context, url string) (string, error) {
	resp, err := httpClient.Get(url)
	if err != nil {
		return "", fmt.Errorf("ошибка запроса: %w", err)
	}
	defer resp.Body.Close()

	doc, err := goquery.NewDocumentFromReader(resp.Body)
	if err != nil {
		return "", fmt.Errorf("ошибка парсинга: %w", err)
	}

	// Извлекаем основной текст (настройте селектор)
	content := doc.Find(".article-content").Text()
	if content == "" {
		return "", errors.New("контент не найден")
	}

	return content, nil
}

func sendToTelegram(ctx context.Context, text string) error {
	apiURL := fmt.Sprintf(TelegramBotAPI, TelegramToken)
	payload := map[string]string{
		"chat_id": TelegramChatID,
		"text":    text,
	}

	jsonData, err := json.Marshal(payload)
	if err != nil {
		return fmt.Errorf("ошибка сериализации: %w", err)
	}

	resp, err := httpClient.Post(apiURL, "application/json", bytes.NewReader(jsonData))
	if err != nil {
		return fmt.Errorf("ошибка запроса: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("статус %d", resp.StatusCode)
	}

	return nil
}

func processArticle(ctx context.Context, articleURL string) error {
	logger := slog.FromContext(ctx)

	// Получаем текст статьи
	content, err := getArticleContent(ctx, articleURL)
	if err != nil {
		return fmt.Errorf("ошибка получения контента: %w", err)
	}

	// Переводим через Yandex
	translation, err := translateWithYandex(ctx, content)
	if err != nil {
		return fmt.Errorf("ошибка перевода: %w", err)
	}

	// Формируем сообщение (первые 500 символов)
	summary := shortenText(translation, 500)
	message := fmt.Sprintf("📌 **Перевод статьи**\n\n%s\n\n🔗 Читать полностью: %s", summary, articleURL)

	// Отправляем в Telegram
	if err := sendToTelegram(ctx, message); err != nil {
		return fmt.Errorf("ошибка отправки: %w", err)
	}

	logger.Info("Статья успешно обработана")
	return nil
}

// Вспомогательные функции
func shortenText(text string, maxLen int) string {
	if len(text) <= maxLen {
		return text
	}
	return text[:maxLen] + "..."
}

func readLastPost() (LastPost, error) {
	var lastPost LastPost

	data, err := os.ReadFile(LastPostFile)
	if err != nil {
		if os.IsNotExist(err) {
			return lastPost, nil
		}
		return lastPost, fmt.Errorf("ошибка чтения: %w", err)
	}

	if err := json.Unmarshal(data, &lastPost); err != nil {
		return lastPost, fmt.Errorf("ошибка разбора JSON: %w", err)
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

// Главная функция
func run(ctx context.Context) error {
	logger := slog.FromContext(ctx)
	logger.Info("Запуск мониторинга ZeroHedge")

	// Получаем последнюю статью
	articleURL, err := getLatestArticle(ctx)
	if err != nil {
		return fmt.Errorf("ошибка поиска статьи: %w", err)
	}

	// Проверяем, была ли статья уже обработана
	lastPost, err := readLastPost()
	if err != nil {
		return fmt.Errorf("ошибка чтения истории: %w", err)
	}

	currentHash := md5.Sum([]byte(articleURL))
	if hex.EncodeToString(currentHash[:]) == lastPost.Hash {
		logger.Info("Новых статей нет")
		return nil
	}

	// Обрабатываем новую статью
	if err := processArticle(ctx, articleURL); err != nil {
		return err
	}

	// Сохраняем состояние
	if err := saveLastPost(articleURL); err != nil {
		return fmt.Errorf("ошибка сохранения: %w", err)
	}

	return nil
}

func main() {
	// Инициализация логгера
	logger, err := setupLogger()
	if err != nil {
		panic(fmt.Sprintf("Ошибка инициализации логгера: %v", err))
	}

	ctx := slog.NewContext(context.Background(), logger)

	// Проверка переменных окружения
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
			logger.Error("Не задана обязательная переменная окружения", "var", v.name)
			os.Exit(1)
		}
	}

	// Запуск
	if err := run(ctx); err != nil {
		logger.Error("Критическая ошибка", "err", err)
		errorMsg := fmt.Sprintf("🚨 Ошибка в ZeroHedge Monitor:\n\n%s", err)
		if err := sendToTelegram(ctx, errorMsg); err != nil {
			logger.Error("Не удалось отправить ошибку в Telegram", "err", err)
		}
		os.Exit(1)
	}
}
