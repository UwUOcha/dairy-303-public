package tg

import (
	"bytes"
	"io"
	"net/http"
	"time"
)

// pollTimeout — потолок на один запрос к Bot API, как у библиотеки по
// умолчанию: длинный опрос сам держит соединение до полуминуты.
const pollTimeout = time.Minute

// newHTTPClient возвращает клиент, который умеет повторить запрос после
// GOAWAY.
//
// Серверы телеграма время от времени закрывают HTTP/2-соединение, и запрос,
// тело которого уже ушло, транспорт повторяет на новом соединении, только если
// может получить тело заново через Request.GetBody. Библиотека пишет форму в
// io.Pipe, GetBody не задаёт — и такой запрос падал ошибкой «cannot retry err
// … define Request.GetBody»: ответ пользователю или уведомление терялись.
func newHTTPClient() *http.Client {
	return &http.Client{
		Timeout:   pollTimeout,
		Transport: rewindable{next: http.DefaultTransport},
	}
}

// rewindable читает тело запроса в память и выставляет GetBody. Запросы к
// Bot API здесь — короткие формы без файлов, так что буфер копеечный.
type rewindable struct {
	next http.RoundTripper
}

func (t rewindable) RoundTrip(req *http.Request) (*http.Response, error) {
	if req.Body == nil || req.Body == http.NoBody || req.GetBody != nil {
		return t.next.RoundTrip(req)
	}
	body, err := io.ReadAll(req.Body)
	req.Body.Close()
	if err != nil {
		return nil, err
	}
	r := req.Clone(req.Context())
	r.ContentLength = int64(len(body))
	r.GetBody = func() (io.ReadCloser, error) {
		return io.NopCloser(bytes.NewReader(body)), nil
	}
	r.Body, _ = r.GetBody()
	return t.next.RoundTrip(r)
}
