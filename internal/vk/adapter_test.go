package vk

import (
	"errors"
	"fmt"
	"testing"
	"time"

	vkapi "github.com/SevereCloud/vksdk/v3/api"

	"github.com/UwUOcha/dairy-303-public/internal/botcore"
)

// Адаптер обязан оставаться отправителем для рассылки — иначе botd не
// соберётся, но узнать об этом лучше здесь.
var _ botcore.Reacher = (*Adapter)(nil)

// Рассыльщику важно различать три исхода: повторить, выключить рассылку и
// просто записать ошибку. Спутать первый с третьим — потерять сообщение,
// спутать второй с первым — долбиться в человека, который закрыл личку.
func TestErrorClassification(t *testing.T) {
	a := &Adapter{}

	tests := []struct {
		name          string
		err           error
		wantRetry     time.Duration
		wantUndeliver bool
	}{
		{name: "успех", err: nil},
		{
			name:      "слишком часто",
			err:       vkErr(vkapi.ErrTooMany),
			wantRetry: time.Second,
		},
		{
			name:      "внутренняя ошибка вконтакте",
			err:       vkErr(vkapi.ErrServer),
			wantRetry: 3 * time.Second,
		},
		{
			name:          "сообщество в чёрном списке",
			err:           vkErr(vkapi.ErrMessagesUserBlocked),
			wantUndeliver: true,
		},
		{
			name:          "писать запрещено",
			err:           vkErr(vkapi.ErrMessagesDenySend),
			wantUndeliver: true,
		},
		{
			name:          "закрытые личные сообщения",
			err:           vkErr(vkapi.ErrMessagesPrivacy),
			wantUndeliver: true,
		},
		{
			// Сообщение длиннее лимита — наша вина, а не адресата: ретраить
			// бессмысленно, но и рассылку человеку выключать не за что.
			name: "сообщение слишком длинное",
			err:  vkErr(vkapi.ErrMessagesTooBig),
		},
		{name: "не от вконтакте", err: errors.New("сеть отвалилась")},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			wait, ok := a.Retryable(tt.err)
			if ok != (tt.wantRetry > 0) || wait != tt.wantRetry {
				t.Errorf("Retryable = %v, %v; ожидалось %v", wait, ok, tt.wantRetry)
			}
			if got := a.Undeliverable(tt.err); got != tt.wantUndeliver {
				t.Errorf("Undeliverable = %v, ожидалось %v", got, tt.wantUndeliver)
			}
		})
	}
}

func TestNewRejectsEmptyToken(t *testing.T) {
	if _, err := New("", true, nil, nil); err == nil {
		t.Error("пустой ключ доступа принят — бот молча не заработает")
	}
}

// vkErr собирает ошибку так, как её отдаёт vksdk: обёрнутой в контекст вызова.
func vkErr(code vkapi.ErrorType) error {
	return fmt.Errorf("api.DefaultHandler: %w", &vkapi.Error{Code: code, Message: "тест"})
}
