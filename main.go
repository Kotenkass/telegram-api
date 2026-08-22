package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	applogger "github.com/Kotenkass/telegram-api/internal/logger"
	requestlogger "github.com/Kotenkass/telegram-api/internal/middleware"
	"github.com/labstack/echo/v4"
	"github.com/redis/go-redis/v9"
	"github.com/sirupsen/logrus"
	telebot "gopkg.in/telebot.v3"
)

const (
	userServiceURL = "http://user-service/users"
	answersURL     = "http://answers:8080"
	redisChannel   = "send_message"

	defaultTelegramMessage = "Поддерживается только текст"
)

type userClient struct {
	httpClient *http.Client
	baseURL    string
}

type webAdminClient struct {
	httpClient *http.Client
	baseURL    string
}

type telegramUser struct {
	ChatID       int64  `json:"chatID"`
	TelegramID   int64  `json:"telegramID"`
	FirstName    string `json:"firstName"`
	LastName     string `json:"lastName"`
	LanguageCode string `json:"languageCode"`
	Username     string `json:"username"`
}

type userService struct {
	client *userClient
}

type webAdminService struct {
	client     *webAdminClient
	publicBase string
}

type answerClient struct {
	httpClient *http.Client
	baseURL    string
}

type answerRequest struct {
	ChatID     int64  `json:"chat_id"`
	TelegramID int64  `json:"telegram_id"`
	Text       string `json:"text"`
	SentAt     string `json:"sent_at"`
}

type answerService struct {
	client *answerClient
}

type tokenRequest struct {
	ChatID int64 `json:"chat_id"`
}

type tokenResponse struct {
	Token     string `json:"token"`
	ExpiresAt int64  `json:"expires_at"`
}

func main() {
	log := applogger.NewLogger()

	token := os.Getenv("TELEGRAM_TOKEN")
	if token == "" {
		log.Error("TELEGRAM_TOKEN environment variable is required")
		os.Exit(1)
	}

	log.WithField("log_level", os.Getenv(applogger.EnvLogLevel)).Info("telegram-api starting")

	redisURL := os.Getenv("REDIS_URL")
	if redisURL == "" {
		redisURL = "redis://localhost:6379/0"
	}

	bot, err := telebot.NewBot(telebot.Settings{
		Token:  token,
		Poller: &telebot.LongPoller{Timeout: 10 * time.Second},
	})
	if err != nil {
		log.WithError(err).Fatal("create telegram bot failed")
	}
	log.Info("telegram bot initialized")

	svc := &userService{client: newUserClient()}
	answerSvc := newAnswerService()
	webAdminSvc := newWebAdminService()

	e := setupHTTPServer(log)
	registerBotHandlers(log, bot, svc, answerSvc, webAdminSvc)

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	go func() {
		<-ctx.Done()
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := e.Shutdown(shutdownCtx); err != nil {
			log.WithError(err).Error("shutdown http server failed")
		}
	}()

	go subscribeToRedisMessages(ctx, redisURL, bot, svc, log)

	bot.Start()
}

func newUserClient() *userClient {
	baseURL := strings.TrimRight(os.Getenv("USER_SERVICE_URL"), "/")
	if baseURL == "" {
		baseURL = userServiceURL
	}

	return &userClient{
		httpClient: &http.Client{Timeout: 10 * time.Second},
		baseURL:    baseURL,
	}
}

func newAnswerService() *answerService {
	baseURL := strings.TrimRight(os.Getenv("ANSWERS_URL"), "/")
	if baseURL == "" {
		baseURL = answersURL
	}

	return &answerService{client: &answerClient{
		httpClient: &http.Client{Timeout: 3 * time.Second},
		baseURL:    baseURL,
	}}
}

func newWebAdminService() *webAdminService {
	baseURL := strings.TrimRight(os.Getenv("WEB_ADMIN_INTERNAL_URL"), "/")
	if baseURL == "" {
		baseURL = strings.TrimRight(os.Getenv("WEB_ADMIN_URL"), "/")
	}
	if baseURL == "" {
		baseURL = "http://web-admin:8080"
	}

	publicBase := strings.TrimRight(os.Getenv("WEB_ADMIN_PUBLIC_URL"), "/")
	if publicBase == "" {
		publicBase = "https://admin.sparktonapp.ru"
	}

	return &webAdminService{
		client: &webAdminClient{
			httpClient: &http.Client{Timeout: 5 * time.Second},
			baseURL:    baseURL,
		},
		publicBase: publicBase,
	}
}

func setupHTTPServer(log *logrus.Logger) *echo.Echo {
	e := echo.New()
	e.HideBanner = true
	e.HidePort = true
	e.Use(requestlogger.RequestLogger(log))
	e.GET("/healthz", healthzHandler)

	startHTTPServer(log, e)

	return e
}

func healthzHandler(c echo.Context) error {
	return c.String(http.StatusOK, "ok")
}

func startHTTPServer(log *logrus.Logger, e *echo.Echo) {
	go func() {
		log.WithField("addr", ":8080").Info("http server starting")
		if err := e.Start(":8080"); err != nil && !errors.Is(err, http.ErrServerClosed) {
			log.WithError(err).Error("http server failed")
		}
	}()
}

func registerBotHandlers(log *logrus.Logger, bot *telebot.Bot, svc *userService, answerSvc *answerService, webAdminSvc *webAdminService) {
	bot.Handle("/start", func(c telebot.Context) error {
		return handleStartCommand(c, log, svc)
	})

	bot.Handle("/cabinet", func(c telebot.Context) error {
		return handleCabinetCommand(c, log, webAdminSvc)
	})

	bot.Handle(telebot.OnText, func(c telebot.Context) error {
		return handleTextMessage(c, log, answerSvc)
	})

	registerUnsupportedMessageHandlers(bot)
}

func handleStartCommand(c telebot.Context, log *logrus.Logger, svc *userService) error {
	chat := c.Chat()
	if chat == nil {
		log.Error("cannot create user: missing chat information")
		return c.Send("Cannot create user: missing chat information.")
	}

	sender := c.Sender()
	if sender == nil {
		log.WithField("chat_id", chat.ID).Error("cannot create user: missing sender information")
		return c.Send("Cannot create user: missing sender information.")
	}

	exists, err := svc.userExists(context.Background(), chat.ID)
	if err != nil {
		log.WithError(err).WithField("chat_id", chat.ID).Error("check user exists failed")
		return c.Send("Failed to check user status. Please try again later.")
	}
	if exists {
		log.WithField("chat_id", chat.ID).Info("telegram user already registered")
		return c.Send("Welcome back!")
	}

	user := telegramUser{
		ChatID:       chat.ID,
		TelegramID:   sender.ID,
		FirstName:    sender.FirstName,
		LastName:     sender.LastName,
		LanguageCode: sender.LanguageCode,
		Username:     sender.Username,
	}

	if err := svc.createUser(context.Background(), user); err != nil {
		log.WithFields(logrus.Fields{
			"chat_id":     chat.ID,
			"telegram_id": sender.ID,
		}).WithError(err).Error("create user failed")
		return c.Send("Failed to register user. Please try again later.")
	}

	log.WithFields(logrus.Fields{
		"chat_id":     chat.ID,
		"telegram_id": sender.ID,
		"username":    sender.Username,
	}).Info("telegram user registered")

	return c.Send("Welcome! You have been registered.")
}

func handleCabinetCommand(c telebot.Context, log *logrus.Logger, webAdminSvc *webAdminService) error {
	chat := c.Chat()
	if chat == nil {
		log.Error("cannot create cabinet link: missing chat information")
		return c.Send("Не получилось открыть кабинет: отсутствует информация о чате.")
	}

	token, err := webAdminSvc.createToken(context.Background(), tokenRequest{ChatID: chat.ID})
	if err != nil {
		log.WithError(err).WithField("chat_id", chat.ID).Error("create web-admin token failed")
		return c.Send("Не получилось открыть кабинет, попробуйте позже.")
	}

	link := webAdminSvc.cabinetLink(token.Token)
	log.WithFields(logrus.Fields{
		"chat_id": chat.ID,
	}).Info("web-admin cabinet link created")

	return c.Send("Откройте личный кабинет: " + link)
}

func handleTextMessage(c telebot.Context, log *logrus.Logger, answerSvc *answerService) error {
	msg := c.Message()
	if msg == nil || msg.Text == "" {
		return nil
	}
	if strings.HasPrefix(msg.Text, "/") {
		return nil
	}
	if msg.Sender == nil || msg.Chat == nil {
		log.WithField("message_id", msg.ID).Error("cannot save answer: missing sender or chat information")
		return c.Reply("Не получилось сохранить, попробуйте позже")
	}

	if err := answerSvc.saveText(context.Background(), answerRequest{
		ChatID:     msg.Chat.ID,
		TelegramID: msg.Sender.ID,
		Text:       msg.Text,
		SentAt:     msg.Time().UTC().Format(time.RFC3339),
	}); err != nil {
		log.WithError(err).WithFields(logrus.Fields{
			"chat_id":     msg.Chat.ID,
			"telegram_id": msg.Sender.ID,
		}).Error("save answer failed")
		return c.Reply("Не получилось сохранить, попробуйте позже")
	}

	return c.Reply("Записал")
}

func registerUnsupportedMessageHandlers(bot *telebot.Bot) {
	bot.Handle(telebot.OnMedia, handleUnsupportedMessageType)
	bot.Handle(telebot.OnSticker, handleUnsupportedMessageType)
	bot.Handle(telebot.OnVoice, handleUnsupportedMessageType)
	bot.Handle(telebot.OnPhoto, handleUnsupportedMessageType)
}

func handleUnsupportedMessageType(c telebot.Context) error {
	return c.Reply(defaultTelegramMessage)
}

func (c *userClient) doJSONRequest(ctx context.Context, method, path string, in any, out any) error {
	var body io.Reader
	if in != nil {
		payload, err := json.Marshal(in)
		if err != nil {
			return fmt.Errorf("marshal request body: %w", err)
		}
		body = bytes.NewReader(payload)
	} else {
		body = nil
	}

	req, err := http.NewRequestWithContext(ctx, method, c.baseURL+path, body)
	if err != nil {
		return fmt.Errorf("create request: %w", err)
	}

	if in != nil {
		req.Header.Set("Content-Type", "application/json")
	}

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return fmt.Errorf("execute request: %w", err)
	}
	defer resp.Body.Close()

	if out == nil {
		if resp.StatusCode < 200 || resp.StatusCode >= 300 {
			return fmt.Errorf("unexpected status %d", resp.StatusCode)
		}
		return nil
	}

	if resp.StatusCode == http.StatusNotFound {
		return nil
	}

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("unexpected status %d", resp.StatusCode)
	}

	if err := json.NewDecoder(resp.Body).Decode(out); err != nil {
		return fmt.Errorf("decode response: %w", err)
	}

	return nil
}

func (c *webAdminClient) doJSONRequest(ctx context.Context, method, path string, in any, out any) error {
	var body io.Reader
	if in != nil {
		payload, err := json.Marshal(in)
		if err != nil {
			return fmt.Errorf("marshal request body: %w", err)
		}
		body = bytes.NewReader(payload)
	} else {
		body = nil
	}

	req, err := http.NewRequestWithContext(ctx, method, c.baseURL+path, body)
	if err != nil {
		return fmt.Errorf("create request: %w", err)
	}

	if in != nil {
		req.Header.Set("Content-Type", "application/json")
	}

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return fmt.Errorf("execute request: %w", err)
	}
	defer resp.Body.Close()

	if out == nil {
		if resp.StatusCode < 200 || resp.StatusCode >= 300 {
			return fmt.Errorf("unexpected status %d", resp.StatusCode)
		}
		return nil
	}

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("unexpected status %d", resp.StatusCode)
	}

	if err := json.NewDecoder(resp.Body).Decode(out); err != nil {
		return fmt.Errorf("decode response: %w", err)
	}

	return nil
}

func (s *userService) userExists(ctx context.Context, chatID int64) (bool, error) {
	var user telegramUser
	if err := s.client.doJSONRequest(ctx, http.MethodGet, fmt.Sprintf("/%d", chatID), nil, &user); err != nil {
		return false, err
	}
	return true, nil
}

func (s *userService) createUser(ctx context.Context, user telegramUser) error {
	var created telegramUser
	return s.client.doJSONRequest(ctx, http.MethodPost, "/", user, &created)
}

func (s *userService) listUsers(ctx context.Context) ([]telegramUser, error) {
	var users []telegramUser
	if err := s.client.doJSONRequest(ctx, http.MethodGet, "/", nil, &users); err != nil {
		return nil, err
	}
	return users, nil
}

func (s *webAdminService) createToken(ctx context.Context, req tokenRequest) (*tokenResponse, error) {
	var resp tokenResponse
	if err := s.client.doJSONRequest(ctx, http.MethodPost, "/internal/token", req, &resp); err != nil {
		return nil, err
	}
	return &resp, nil
}

func (s *webAdminService) cabinetLink(token string) string {
	return s.publicBase + "/c/" + token
}

func (s *answerService) saveText(ctx context.Context, req answerRequest) error {
	payload, err := json.Marshal(req)
	if err != nil {
		return fmt.Errorf("marshal answer request: %w", err)
	}

	if err := s.client.post(ctx, "/answers", payload); err != nil {
		return err
	}

	return nil
}

func (c *answerClient) post(ctx context.Context, path string, body []byte) error {
	url := c.baseURL + path
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return fmt.Errorf("create request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return fmt.Errorf("execute request: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("unexpected status %d", resp.StatusCode)
	}

	return nil
}

func subscribeToRedisMessages(ctx context.Context, redisURL string, bot *telebot.Bot, svc *userService, log *logrus.Logger) {
	opt, err := redis.ParseURL(redisURL)
	if err != nil {
		log.WithError(err).Error("parse redis url failed")
		return
	}

	rdb := redis.NewClient(opt)
	defer rdb.Close()

	if err := rdb.Ping(ctx).Err(); err != nil {
		log.WithError(err).Error("connect redis failed")
		return
	}

	pubsub := rdb.Subscribe(ctx, redisChannel)
	defer pubsub.Close()

	ch := pubsub.Channel()
	for {
		select {
		case <-ctx.Done():
			return
		case msg := <-ch:
			if msg == nil {
				continue
			}
			if strings.TrimSpace(msg.Payload) == "" {
				continue
			}
			log.WithFields(logrus.Fields{
				"channel":        redisChannel,
				"payload_length": len(msg.Payload),
			}).Info("redis message received")
			if err := sendBroadcast(ctx, bot, svc, msg.Payload); err != nil {
				log.WithError(err).Error("send broadcast message failed")
			}
		}
	}
}

func sendBroadcast(ctx context.Context, bot *telebot.Bot, svc *userService, text string) error {
	users, err := svc.listUsers(ctx)
	if err != nil {
		return fmt.Errorf("list users: %w", err)
	}

	var errs []string
	for _, user := range users {
		if user.ChatID == 0 {
			continue
		}
		if _, err := bot.Send(&telebot.Chat{ID: user.ChatID}, text); err != nil {
			errs = append(errs, fmt.Sprintf("chat_id=%d: %v", user.ChatID, err))
		}
	}

	if len(errs) > 0 {
		return errors.New(strings.Join(errs, "; "))
	}
	return nil
}
