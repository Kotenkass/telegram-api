package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/redis/go-redis/v9"
	telebot "gopkg.in/telebot.v3"
)

const (
	userServiceURL = "http://user-service/users"
	redisChannel   = "send_message"
)

type userClient struct {
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

func main() {
	token := os.Getenv("TELEGRAM_TOKEN")
	if token == "" {
		log.Fatal("TELEGRAM_TOKEN environment variable is required")
	}

	redisURL := os.Getenv("REDIS_URL")
	if redisURL == "" {
		redisURL = "redis://localhost:6379/0"
	}

	bot, err := telebot.NewBot(telebot.Settings{
		Token:  token,
		Poller: &telebot.LongPoller{Timeout: 10 * time.Second},
	})
	if err != nil {
		log.Fatalf("create telegram bot: %v", err)
	}

	client := &userClient{
		httpClient: &http.Client{Timeout: 10 * time.Second},
		baseURL:    strings.TrimRight(os.Getenv("USER_SERVICE_URL"), "/"),
	}
	if client.baseURL == "" {
		client.baseURL = userServiceURL
	}

	svc := &userService{client: client}

	bot.Handle("/start", func(c telebot.Context) error {
		chat := c.Chat()
		if chat == nil {
			return c.Send("Cannot create user: missing chat information.")
		}

		sender := c.Sender()
		if sender == nil {
			return c.Send("Cannot create user: missing sender information.")
		}

		exists, err := svc.userExists(context.Background(), chat.ID)
		if err != nil {
			log.Printf("check user exists chat_id=%d: %v", chat.ID, err)
			return c.Send("Failed to check user status. Please try again later.")
		}
		if exists {
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
			log.Printf("create user chat_id=%d: %v", chat.ID, err)
			return c.Send("Failed to register user. Please try again later.")
		}

		return c.Send("Welcome! You have been registered.")
	})

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	go subscribeToRedisMessages(ctx, redisURL, bot, svc)

	bot.Start()
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

func subscribeToRedisMessages(ctx context.Context, redisURL string, bot *telebot.Bot, svc *userService) {
	opt, err := redis.ParseURL(redisURL)
	if err != nil {
		log.Printf("parse redis url %q: %v", redisURL, err)
		return
	}

	rdb := redis.NewClient(opt)
	defer rdb.Close()

	if err := rdb.Ping(ctx).Err(); err != nil {
		log.Printf("connect redis: %v", err)
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
			if err := sendBroadcast(ctx, bot, svc, msg.Payload); err != nil {
				log.Printf("send broadcast message: %v", err)
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
