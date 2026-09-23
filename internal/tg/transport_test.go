package tg

import (
	"io"
	"net/http"
	"strings"
	"testing"
)

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

// Тело из io.Pipe, как у библиотеки, должно доехать целиком и оставаться
// доступным для повтора: иначе транспорт не переотправит запрос после GOAWAY.
func TestRewindableSetsGetBody(t *testing.T) {
	pr, pw := io.Pipe()
	go func() {
		io.WriteString(pw, "chat_id=1&text=привет")
		pw.Close()
	}()
	req, err := http.NewRequest(http.MethodPost, "https://example.test/sendMessage", pr)
	if err != nil {
		t.Fatal(err)
	}
	if req.GetBody != nil {
		t.Fatal("у тела из трубы не должно быть GetBody — иначе тест ничего не проверяет")
	}

	var got *http.Request
	rt := rewindable{next: roundTripFunc(func(r *http.Request) (*http.Response, error) {
		got = r
		return &http.Response{StatusCode: http.StatusOK, Body: http.NoBody}, nil
	})}
	if _, err := rt.RoundTrip(req); err != nil {
		t.Fatal(err)
	}
	if got.GetBody == nil {
		t.Fatal("GetBody не выставлен — запрос после GOAWAY снова не повторится")
	}
	for i := range 2 {
		var body io.Reader = got.Body
		if i > 0 {
			rc, err := got.GetBody()
			if err != nil {
				t.Fatal(err)
			}
			body = rc
		}
		b, _ := io.ReadAll(body)
		if string(b) != "chat_id=1&text=привет" {
			t.Errorf("попытка %d: тело %q", i+1, b)
		}
	}
	if got.ContentLength != int64(len("chat_id=1&text=привет")) {
		t.Errorf("ContentLength = %d", got.ContentLength)
	}
}

func TestRewindableLeavesBodylessRequests(t *testing.T) {
	req, _ := http.NewRequest(http.MethodGet, "https://example.test/getMe", nil)
	rt := rewindable{next: roundTripFunc(func(r *http.Request) (*http.Response, error) {
		if r != req {
			t.Error("запрос без тела зря склонирован")
		}
		return &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(strings.NewReader(""))}, nil
	})}
	if _, err := rt.RoundTrip(req); err != nil {
		t.Fatal(err)
	}
}
