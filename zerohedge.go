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
	"os"
	"regexp"
	"strings"
	"time"

	"github.com/PuerkitoBio/goquery"
	"github.com/joho/godotenv"
)

// Конфигурация
const (
	ZeroHedgeURL       = "https://www.zerohedge.com/"
	LastPostFile       = "last_post.txt"
	TelegramBotAPI     = "https://api.telegram.org/bot%s/sendMessage"
	YandexTranslate    = "https://translate.api.cloud.yandex.net/translate/v2/translate"
	MaxRetries         = 3
	RetryDelay         = 5 * time.Second
	LogFile            = "zerohedge_monitor.log"
	CheckInterval      = 1 * time.Minute // Интервал проверки
	MaxSummaryLength   = 4000             // Максимальная длина для Telegram
	SummarySentences   = 5                // Количество предложений для сокращения
)

var (
	TelegramToken   string
	TelegramChatID  string
	YandexAPIKey    string
	YandexFolderID  string
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

func getLatestArticle(ctx context.Context) (string, string, error) {
	ctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	resp, err := httpClient.Get(ZeroHedgeURL)
	if err != nil {
		return "", "", fmt.Errorf("ошибка запроса: %w", err)
	}
	defer resp.Body.Close()

	doc, err := goquery.NewDocumentFromReader(resp.Body)
	if err != nil {
		return "", "", fmt.Errorf("ошибка парсинга: %w", err)
	}

	// Улучшенный селектор для статьи
	article := doc.Find(".content article, article.node--type-article").First()
	if article == nil {
		return "", "", errors.New("статья не найдена")
	}

	link, exists := article.Find("a[href]:has(h2, h3, h4)").First().Attr("href")
	if !exists {
		return "", "", errors.New("ссылка не найдена")
	}

	title := article.Find("h2, h3, h4").First().Text()

	if !strings.HasPrefix(link, "http") {
		link = ZeroHedgeURL + strings.TrimPrefix(link, "/")
	}

	return link, strings.TrimSpace(title), nil
}

func getArticleContent(ctx context.Context, url string) (string, []string, error) {
	resp, err := httpClient.Get(url)
	if err != nil {
		return "", nil, fmt.Errorf("ошибка запроса: %w", err)
	}
	defer resp.Body.Close()

	doc, err := goquery.NewDocumentFromReader(resp.Body)
	if err != nil {
		return "", nil, fmt.Errorf("ошибка парсинга: %w", err)
	}

	// Улучшенный селектор для контента
	content := doc.Find(".article-content, .content .field--name-body, .body-content").Text()
	if content == "" {
		return "", nil, errors.New("контент не найден")
	}

	// Извлекаем изображения
	var images []string
	doc.Find(".article-content img, .content img, .field--name-body img").Each(func(i int, s *goquery.Selection) {
		if src, exists := s.Attr("src"); exists {
			if !strings.HasPrefix(src, "http") {
				src = ZeroHedgeURL + strings.TrimPrefix(src, "/")
			}
			images = append(images, src)
		}
	})

	return strings.TrimSpace(content), images, nil
}

func sendToTelegram(ctx context.Context, text string, images []string) error {
	apiURL := fmt.Sprintf(TelegramBotAPI, TelegramToken)
	
	// Сначала отправляем изображения (если есть)
	for _, img := range images {
		photoURL := fmt.Sprintf("https://api.telegram.org/bot%s/sendPhoto", TelegramToken)
		payload := map[string]string{
			"chat_id": TelegramChatID,
			"photo":   img,
			"caption": "",
		}

		jsonData, err := json.Marshal(payload)
		if err != nil {
			slog.Error("Ошибка сериализации изображения", "err", err)
			continue
		}

		resp, err := httpClient.Post(photoURL, "application/json", bytes.NewReader(jsonData))
		if err != nil {
			slog.Error("Ошибка отправки изображения", "err", err, "url", img)
			continue
		}
		resp.Body.Close()
	}

	// Затем отправляем текст
	payload := map[string]string{
		"chat_id":    TelegramChatID,
		"text":       text,
		"parse_mode": "HTML",
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

func processArticle(ctx context.Context, articleURL, articleTitle string) error {
	logger := ctx.Value("logger").(*slog.Logger)

	// Получаем текст статьи и изображения
	content, images, err := getArticleContent(ctx, articleURL)
	if err != nil {
		return fmt.Errorf("ошибка получения контента: %w", err)
	}

	// Переводим через Yandex
	translation, err := translateWithYandex(ctx, content)
	if err != nil {
		return fmt.Errorf("ошибка перевода: %w", err)
	}

	// Интеллектуальное сокращение текста
	summary := intelligentSummary(translation, SummarySentences, MaxSummaryLength)
	
	// Формируем сообщение
	message := fmt.Sprintf(
		"<b>📌 %s</b>\n\n%s\n\n<a href=\"%s\">🔗 Читать полностью</a>",
		articleTitle,
		summary,
		articleURL,
	)

	// Отправляем в Telegram
	if err := sendToTelegram(ctx, message, images); err != nil {
		return fmt.Errorf("ошибка отправки: %w", err)
	}

	logger.Info("Статья успешно обработана", "url", articleURL)
	return nil
}

// Вспомогательные функции
func intelligentSummary(text string, sentences int, maxLen int) string {
	// Сначала сокращаем до максимальной длины
	if len(text) > maxLen {
		text = text[:maxLen]
	}

	// Затем пытаемся обрезать по последнему предложению
	r := regexp.MustCompile(`(?s)(.*?[.!?](\s|$))`)
	matches := r.FindAllString(text, -1)

	if len(matches) > 0 {
		if sentences > len(matches) {
			sentences = len(matches)
		}
		summary := strings.Join(matches[:sentences], " ")
		return strings.TrimSpace(summary) + "…"
	}

	// Если не нашли предложения, просто обрезаем
	return shortenText(text, maxLen)
}

func shortenText(text string, maxLen int) string {
	if len(text) <= maxLen {
		return text
	}
	return text[:maxLen] + "…"
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
	logger := ctx.Value("logger").(*slog.Logger)
	logger.Info("Запуск мониторинга ZeroHedge")

	ticker := time.NewTicker(CheckInterval)
	defer ticker.Stop()

	logger.Info("RUN: Таймер создан", "interval", CheckInterval) 

	for {
		select {
		case <-ctx.Done():
			return nil
		case <-ticker.C:
			// Получаем последнюю статью
			articleURL, articleTitle, err := getLatestArticle(ctx)
			if err != nil {
				logger.Error("Ошибка поиска статьи", "err", err)
				continue
			}

			// Проверяем, была ли статья уже обработана
			lastPost, err := readLastPost()
			if err != nil {
				logger.Error("Ошибка чтения истории", "err", err)
				continue
			}

			currentHash := md5.Sum([]byte(articleURL))
			if hex.EncodeToString(currentHash[:]) == lastPost.Hash {
				logger.Info("Новых статей нет")
				continue
			}

			// Обрабатываем новую статью
			if err := processArticle(ctx, articleURL, articleTitle); err != nil {
				logger.Error("Ошибка обработки статьи", "err", err, "url", articleURL)
				continue
			}

			// Сохраняем состояние
			if err := saveLastPost(articleURL); err != nil {
				logger.Error("Ошибка сохранения", "err", err)
				continue
			}
		}
	}
}

func main() {
	slog.Info("MAIN: Начало выполнения")
	slog.Info("Параметры окружения",
            "CheckInterval", CheckInterval,
            "ZeroHedgeURL", ZeroHedgeURL,
            "TelegramToken", TelegramToken != "",
            "YandexAPIKey", YandexAPIKey != "")
	// Загрузка .env файла
	if err := godotenv.Load(); err != nil {
		fmt.Println("Не удалось загрузить .env файл:", err)
	}

	// Инициализация переменных окружения
	TelegramToken = os.Getenv("TG_TOKEN")
	TelegramChatID = os.Getenv("TG_CHAT_ID")
	YandexAPIKey = os.Getenv("YANDEX_TRANSLATE_KEY")
	YandexFolderID = os.Getenv("YANDEX_FOLDER_ID")

	// Инициализация логгера
	logger, err := setupLogger()
	if err != nil {
		panic(fmt.Sprintf("Ошибка инициализации логгера: %v", err))
	}

        // Создаем контекст с логгером
        ctx := context.WithValue(context.Background(), "logger", logger)

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
		if err := sendToTelegram(ctx, errorMsg, nil); err != nil {
			logger.Error("Не удалось отправить ошибку в Telegram", "err", err)
		}
		os.Exit(1)
	}
}
