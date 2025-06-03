package main

import (
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
	"path/filepath"
	"strings"
	"time"

	"github.com/PuerkitoBio/goquery"
)

const (
	ZeroHedgeURL   = "https://www.zerohedge.com/"
	LastPostFile   = "last_post.txt"
	TelegramBotAPI = "https://api.telegram.org/bot%s/sendMessage"
	MaxRetries     = 3
	RetryDelay     = 5 * time.Second
)

var (
	TelegramToken  = os.Getenv("TG_TOKEN")  // Безопасное хранение токена
	TelegramChatID = os.Getenv("TG_CHAT_ID")
	LogFile        = "zerohedge_monitor.log"
)

type LastPost struct {
	URL  string `json:"url"`
	Hash string `json:"hash"`
}

// Инициализация логгера
func setupLogger() (*slog.Logger, error) {
	logFile, err := os.OpenFile(LogFile, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0644)
	if err != nil {
		return nil, fmt.Errorf("не удалось открыть лог-файл: %w", err)
	}

	// Логирование в файл + консоль
	multiWriter := io.MultiWriter(os.Stdout, logFile)

	logger := slog.New(
		slog.NewJSONHandler(multiWriter, &slog.HandlerOptions{
			Level:     slog.LevelDebug,
			AddSource: true,
		}),
	)

	// Устанавливаем логгер по умолчанию
	slog.SetDefault(logger)
	return logger, nil
}

// Логирующий HTTP-клиент
type loggingRoundTripper struct {
	proxied http.RoundTripper
}

func (lrt loggingRoundTripper) RoundTrip(req *http.Request) (*http.Response, error) {
	slog.Debug("HTTP запрос",
		"method", req.Method,
		"url", req.URL,
		"headers", req.Header,
	)

	resp, err := lrt.proxied.RoundTrip(req)
	if err != nil {
		slog.Error("HTTP ошибка", "err", err)
		return nil, err
	}

	slog.Debug("HTTP ответ",
		"status", resp.Status,
		"headers", resp.Header,
	)

	return resp, nil
}

var httpClient = &http.Client{
	Timeout:   30 * time.Second,
	Transport: loggingRoundTripper{proxied: http.DefaultTransport},
}

// [Остальные функции (fetchWithRetries, sendToTelegram и др.) остаются такими же, 
// но теперь используют slog вместо log]

// Пример обновленной функции с логированием
func getLatestArticle(ctx context.Context) (string, error) {
	logger := slog.FromContext(ctx)
	logger.Info("Поиск последней статьи")

	resp, err := fetchWithRetries(ZeroHedgeURL)
	if err != nil {
		logger.Error("Ошибка при запросе", "err", err)
		return "", fmt.Errorf("не удалось загрузить страницу: %w", err)
	}
	defer resp.Body.Close()

	// [Остальная часть функции]
}

func main() {
	// Инициализация логгера
	logger, err := setupLogger()
	if err != nil {
		panic(fmt.Sprintf("Ошибка инициализации логгера: %v", err))
	}

	ctx := context.Background()
	ctx = slog.NewContext(ctx, logger)

	logger.Info("Запуск ZeroHedge Monitor", "version", "1.0")

	if err := run(ctx); err != nil {
		logger.Error("Критическая ошибка", "err", err)
		errorMsg := fmt.Sprintf("🚨 **Ошибка в ZeroHedge Monitor**\n\n%s", err)
		if err := sendToTelegram(ctx, errorMsg); err != nil {
			logger.Error("Не удалось отправить ошибку в Telegram", "err", err)
		}
		os.Exit(1)
	}
}
