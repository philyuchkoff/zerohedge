package main

import (
	"crypto/md5"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/PuerkitoBio/goquery"
)

const (
	ZeroHedgeURL    = "https://www.zerohedge.com/"
	LastPostFile    = "last_post.txt"
	TelegramBotAPI  = "https://api.telegram.org/bot%s/sendMessage"
	TelegramToken   = "YOUR_TELEGRAM_BOT_TOKEN"
	TelegramChatID  = "@YOUR_CHANNEL_NAME"
	MaxRetries      = 3               // Максимальное количество попыток повтора
	RetryDelay      = 5 * time.Second // Задержка между попытками
)

type LastPost struct {
	URL  string `json:"url"`
	Hash string `json:"hash"`
}

// --- HTTP-клиент с таймаутом и повторными попытками ---
var httpClient = &http.Client{
	Timeout: 30 * time.Second,
}

func fetchWithRetries(url string) (*http.Response, error) {
	var lastErr error
	for i := 0; i < MaxRetries; i++ {
		resp, err := httpClient.Get(url)
		if err == nil && resp.StatusCode == http.StatusOK {
			return resp, nil
		}
		if err != nil {
			lastErr = err
		} else {
			lastErr = fmt.Errorf("HTTP %d", resp.StatusCode)
			resp.Body.Close()
		}
		time.Sleep(RetryDelay)
	}
	return nil, fmt.Errorf("после %d попыток: %v", MaxRetries, lastErr)
}

// --- Обработка ошибок Telegram ---
func sendToTelegram(text string) error {
	apiURL := fmt.Sprintf(TelegramBotAPI, TelegramToken)
	payload := map[string]string{
		"chat_id": TelegramChatID,
		"text":    text,
	}

	jsonData, err := json.Marshal(payload)
	if err != nil {
		return fmt.Errorf("ошибка сериализации JSON: %v", err)
	}

	resp, err := httpClient.Post(apiURL, "application/json", strings.NewReader(string(jsonData)))
	if err != nil {
		return fmt.Errorf("ошибка отправки в Telegram: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("Telegram API ошибка %d: %s", resp.StatusCode, body)
	}

	return nil
}

// --- Улучшенный парсинг статей ---
func getLatestArticle() (string, error) {
	resp, err := fetchWithRetries(ZeroHedgeURL)
	if err != nil {
		return "", fmt.Errorf("не удалось загрузить страницу: %v", err)
	}
	defer resp.Body.Close()

	doc, err := goquery.NewDocumentFromReader(resp.Body)
	if err != nil {
		return "", fmt.Errorf("ошибка парсинга HTML: %v", err)
	}

	// Ищем статью с резервными вариантами селекторов
	selectors := []string{
		"article a[href]",       // Основной селектор
		".teaser-title a[href]", // Резервный вариант
		"h2 a[href]",            // Еще один вариант
	}

	for _, selector := range selectors {
		if link, exists := doc.Find(selector).First().Attr("href"); exists {
			if strings.HasPrefix(link, "http") {
				return link, nil
			}
			return ZeroHedgeURL + strings.TrimPrefix(link, "/"), nil
		}
	}

	return "", errors.New("не найдено ни одной статьи")
}

// --- Защищенное чтение/запись файла ---
func readLastPost() (LastPost, error) {
	var lastPost LastPost

	file, err := os.ReadFile(LastPostFile)
	if err != nil {
		if os.IsNotExist(err) {
			return lastPost, nil // Файла нет — это не ошибка
		}
		return lastPost, fmt.Errorf("ошибка чтения файла: %v", err)
	}

	if err := json.Unmarshal(file, &lastPost); err != nil {
		return lastPost, fmt.Errorf("ошибка разбора JSON: %v", err)
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

	tmpFile := LastPostFile + ".tmp"
	if err := os.WriteFile(tmpFile, data, 0644); err != nil {
		return fmt.Errorf("ошибка записи временного файла: %v", err)
	}

	if err := os.Rename(tmpFile, LastPostFile); err != nil {
		return fmt.Errorf("ошибка переименования файла: %v", err)
	}

	return nil
}

// --- Главная функция с обработкой паник ---
func run() (err error) {
	defer func() {
		if r := recover(); r != nil {
			err = fmt.Errorf("паника: %v", r)
		}
	}()

	log.Println("🔍 Проверка новых статей...")

	latestArticle, err := getLatestArticle()
	if err != nil {
		return fmt.Errorf("ошибка получения статьи: %v", err)
	}

	lastPost, err := readLastPost()
	if err != nil {
		return fmt.Errorf("ошибка чтения истории: %v", err)
	}

	currentHash := md5.Sum([]byte(latestArticle))
	if hex.EncodeToString(currentHash[:]) == lastPost.Hash {
		log.Println("🔄 Новых статей нет")
		return nil
	}

	log.Println("📢 Найдена новая статья!")
	content, err := getArticleContent(latestArticle)
	if err != nil {
		return fmt.Errorf("ошибка получения контента: %v", err)
	}

	summary := summarizeContent(content)
	msg := fmt.Sprintf("📢 **Новая статья на ZeroHedge**\n\n%s\n\n🔗 %s", summary, latestArticle)

	if err := sendToTelegram(msg); err != nil {
		return fmt.Errorf("ошибка отправки в Telegram: %v", err)
	}

	if err := saveLastPost(latestArticle); err != nil {
		return fmt.Errorf("ошибка сохранения состояния: %v", err)
	}

	log.Println("✅ Успешно отправлено!")
	return nil
}

func main() {
	if err := run(); err != nil {
		log.Printf("❌ Критическая ошибка: %v", err)
		// Пытаемся отправить ошибку в Telegram
		errorMsg := fmt.Sprintf("🚨 **Ошибка в ZeroHedge Monitor**\n\n%s", err)
		if err := sendToTelegram(errorMsg); err != nil {
			log.Printf("Не удалось отправить ошибку в Telegram: %v", err)
		}
		os.Exit(1)
	}
}
