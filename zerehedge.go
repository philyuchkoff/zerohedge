package main

import (
	"crypto/md5"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
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
	TelegramToken   = "YOUR_TELEGRAM_BOT_TOKEN" // Замените на свой токен
	TelegramChatID  = "@YOUR_CHANNEL_NAME"      // Или ID чата/канала
	DeepSeekSummary = "https://api.deepseek.com/v1/summarize" // Если API доступно (пример)
)

// Структура для хранения последней статьи
type LastPost struct {
	URL  string `json:"url"`
	Hash string `json:"hash"`
}

// Получаем последнюю статью с ZeroHedge
func getLatestArticle() (string, error) {
	resp, err := http.Get(ZeroHedgeURL)
	if err != nil {
		return "", fmt.Errorf("ошибка запроса: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("статус код: %d", resp.StatusCode)
	}

	doc, err := goquery.NewDocumentFromReader(resp.Body)
	if err != nil {
		return "", fmt.Errorf("ошибка парсинга: %v", err)
	}

	// Ищем последнюю статью (настройте селектор под актуальную верстку)
	latestArticle := ""
	doc.Find("article").First().Each(func(i int, s *goquery.Selection) {
		link, exists := s.Find("a").First().Attr("href")
		if exists {
			latestArticle = link
		}
	})

	if latestArticle == "" {
		return "", fmt.Errorf("статья не найдена")
	}

	return latestArticle, nil
}

// Получаем текст статьи
func getArticleContent(url string) (string, error) {
	resp, err := http.Get(url)
	if err != nil {
		return "", fmt.Errorf("ошибка запроса: %v", err)
	}
	defer resp.Body.Close()

	doc, err := goquery.NewDocumentFromReader(resp.Body)
	if err != nil {
		return "", fmt.Errorf("ошибка парсинга: %v", err)
	}

	// Извлекаем текст (настройте селектор под ZeroHedge)
	content := ""
	doc.Find(".article-content").Each(func(i int, s *goquery.Selection) {
		content = s.Text()
	})

	if content == "" {
		return "", fmt.Errorf("контент не найден")
	}

	return content, nil
}

// Саммаризация (заглушка, если нет API)
func summarizeWithDeepSeek(text string) string {
	// TODO: Если DeepSeek дает API, можно реализовать запрос сюда
	return fmt.Sprintf("📌 **Саммари статьи** (пример):\n\n%s\n\n...", text[:200])
}

// Отправка сообщения в Telegram
func sendToTelegram(text string) error {
	apiURL := fmt.Sprintf(TelegramBotAPI, TelegramToken)
	payload := map[string]string{
		"chat_id": TelegramChatID,
		"text":    text,
	}

	jsonData, err := json.Marshal(payload)
	if err != nil {
		return fmt.Errorf("ошибка JSON: %v", err)
	}

	resp, err := http.Post(apiURL, "application/json", strings.NewReader(string(jsonData)))
	if err != nil {
		return fmt.Errorf("ошибка запроса: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("Telegram API ошибка: %d", resp.StatusCode)
	}

	return nil
}

// Чтение последней статьи из файла
func getLastPost() (LastPost, error) {
	var lastPost LastPost

	if _, err := os.Stat(LastPostFile); os.IsNotExist(err) {
		return lastPost, nil
	}

	file, err := os.ReadFile(LastPostFile)
	if err != nil {
		return lastPost, fmt.Errorf("ошибка чтения файла: %v", err)
	}

	if err := json.Unmarshal(file, &lastPost); err != nil {
		return lastPost, fmt.Errorf("ошибка JSON: %v", err)
	}

	return lastPost, nil
}

// Сохранение последней статьи в файл
func saveLastPost(url string) error {
	hash := md5.Sum([]byte(url))
	lastPost := LastPost{
		URL:  url,
		Hash: hex.EncodeToString(hash[:]),
	}

	jsonData, err := json.Marshal(lastPost)
	if err != nil {
		return fmt.Errorf("ошибка JSON: %v", err)
	}

	if err := os.WriteFile(LastPostFile, jsonData, 0644); err != nil {
		return fmt.Errorf("ошибка записи: %v", err)
	}

	return nil
}

// Основная логика
func main() {
	fmt.Println("🔄 Проверка новых статей на ZeroHedge...")

	latestArticle, err := getLatestArticle()
	if err != nil {
		fmt.Printf("❌ Ошибка: %v\n", err)
		return
	}

	lastPost, err := getLastPost()
	if err != nil {
		fmt.Printf("❌ Ошибка чтения последнего поста: %v\n", err)
	}

	currentHash := md5.Sum([]byte(latestArticle))
	currentHashStr := hex.EncodeToString(currentHash[:])

	if currentHashStr != lastPost.Hash {
		fmt.Println("🔍 Найдена новая статья!")

		content, err := getArticleContent(latestArticle)
		if err != nil {
			fmt.Printf("❌ Ошибка получения контента: %v\n", err)
			return
		}

		summary := summarizeWithDeepSeek(content)
		message := fmt.Sprintf("📢 **Новая статья на ZeroHedge**\n\n%s\n\n🔗 Читать полностью: %s", summary, latestArticle)

		if err := sendToTelegram(message); err != nil {
			fmt.Printf("❌ Ошибка отправки в Telegram: %v\n", err)
			return
		}

		if err := saveLastPost(latestArticle); err != nil {
			fmt.Printf("❌ Ошибка сохранения поста: %v\n", err)
			return
		}

		fmt.Println("✅ Сообщение отправлено в Telegram!")
	} else {
		fmt.Println("🔄 Новых статей нет.")
	}
}
